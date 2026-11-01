package controlplane

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/goadesign/surla/internal/verify"
	"github.com/goadesign/surla/internal/verifyrun"
)

func TestVerifyChangeAcceptsOnlyTheOwnersCurrentConfirmedRevision(t *testing.T) {
	alice := WithPrincipal(context.Background(), Principal{ID: "alice@example.com"})
	bob := WithPrincipal(context.Background(), Principal{ID: "bob@example.com"})
	commit := strings.Repeat("a", 40)
	cp, card, cfg := verificationSetup(t)

	start := func(ctx context.Context, revision string) (*gensurla.VerificationStatusResult, error) {
		return cp.VerifyChange(ctx, &gensurla.VerifyChangePayload{ChangeID: "chg", RequestRevision: revision, CandidateCommit: commit})
	}
	for _, tc := range []struct {
		name    string
		ctx     context.Context
		rev     string
		wantErr string
	}{
		{"another principal", bob, card.RequestRevision, "is not the owner"},
		{"stale revision", alice, "rev-old", "request_revision mismatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := start(tc.ctx, tc.rev)
			assert.ErrorContains(t, err, tc.wantErr)
		})
	}
	t.Run("unknown change", func(t *testing.T) {
		_, err := cp.VerifyChange(alice, &gensurla.VerifyChangePayload{ChangeID: "nope", RequestRevision: "r", CandidateCommit: commit})
		assert.ErrorContains(t, err, `unknown change_id "nope"`)
	})
	t.Run("no verification configured", func(t *testing.T) {
		unconfigured := newControlPlane(t, Options{})
		_, err := unconfigured.VerifyChange(alice, &gensurla.VerifyChangePayload{ChangeID: "chg", RequestRevision: "r", CandidateCommit: commit})
		assert.ErrorIs(t, err, errVerificationUnavailable)
		_, err = unconfigured.GetVerification(alice, &gensurla.GetVerificationPayload{RunID: verifyrun.NewRunID()})
		assert.ErrorIs(t, err, errVerificationUnavailable)
	})

	t.Run("accepted run carries the operator's suite and the caller's commit and revision", func(t *testing.T) {
		started, err := start(alice, card.RequestRevision)
		require.NoError(t, err)
		assert.Equal(t, "chg", started.ChangeID)
		assert.Equal(t, card.RequestRevision, started.RequestRevision)
		assert.Equal(t, commit, started.CandidateCommit)
		assert.Equal(t, cfg.Plan.SuiteCommit, started.SuiteCommit)
		assert.Equal(t, gensurla.StateKindRunning, started.State.Kind())

		saved, err := os.ReadFile(filepath.Join(cfg.RunsDir, started.RunID, "request.json"))
		require.NoError(t, err)
		var req verifyrun.Request
		require.NoError(t, json.Unmarshal(saved, &req))
		assert.Equal(t, "alice@example.com", req.Owner)
		assert.Equal(t, cfg.Plan.SuiteRepo, req.Plan.SuiteRepo, "the suite comes from the operator, never the caller")
		assert.Equal(t, cfg.Plan.Binaries, req.Plan.Binaries)

		_, err = cp.GetVerification(bob, &gensurla.GetVerificationPayload{RunID: started.RunID})
		assert.ErrorContains(t, err, "is not the owner of run_id")
	})
}

func TestGetVerificationReadsTheRunFromDiskInEveryState(t *testing.T) {
	alice := WithPrincipal(context.Background(), Principal{ID: "alice@example.com"})
	cp, card, cfg := verificationSetup(t)
	commit := strings.Repeat("a", 40)
	inputs := PromotionInputs{SuiteID: "orders_acceptance", SuiteCommit: cfg.Plan.SuiteCommit, Environment: "sim",
		Artifacts: map[string]string{"orders": "d1"}}

	// writeRun saves a finished run as its supervisor would.
	writeRun := func(t *testing.T, result string) string {
		t.Helper()
		runID := verifyrun.NewRunID()
		dir := filepath.Join(cfg.RunsDir, runID)
		require.NoError(t, os.MkdirAll(dir, 0o700))
		plan := cfg.Plan
		plan.CandidateCommit, plan.RequestRevision = commit, card.RequestRevision
		req, err := json.Marshal(verifyrun.Request{RunID: runID, Owner: "alice@example.com", ChangeID: "chg",
			AcceptedAt: time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC), Plan: plan})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "request.json"), req, 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "lock"), nil, 0o600))
		if result != "" {
			require.NoError(t, os.WriteFile(filepath.Join(dir, "result.json"), []byte(result), 0o600))
		}
		return runID
	}
	get := func(t *testing.T, runID string) *gensurla.VerificationStatusResult {
		t.Helper()
		res, err := cp.GetVerification(alice, &gensurla.GetVerificationPayload{RunID: runID})
		require.NoError(t, err)
		assert.Equal(t, card.RequestRevision, res.RequestRevision)
		assert.Equal(t, commit, res.CandidateCommit)
		return res
	}

	t.Run("completed returns only the builder view", func(t *testing.T) {
		report := failingReport(t, card.RequestRevision, commit, inputs)
		b, err := json.Marshal(report)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(cfg.Plan.ReportDir, report.Digest+".json"), b, 0o600))
		res := get(t, writeRun(t, `{"kind":"report","report_digest":"`+report.Digest+`"}`))
		completed, ok := res.State.AsCompleted()
		require.True(t, ok)
		assert.Equal(t, "orders_acceptance", completed.SuiteID)
		assert.Equal(t, "sim", completed.Environment)
		assert.False(t, completed.Report.Passed)
		assert.Equal(t, report.Digest, completed.Report.ReportDigest)
		assert.Equal(t, &gensurla.VerificationCounts{Required: 2, Passed: 1, Failed: 1}, completed.Report.Counts)
		require.Len(t, completed.Report.Scenarios, 2)
		assert.Equal(t, "a", completed.Report.Scenarios[0].ID)
		assert.Equal(t, "holdout-1", completed.Report.Scenarios[1].ID, "holdout names never reach builders")
		assert.Empty(t, completed.Report.Scenarios[1].Checks)
		wire, err := json.Marshal(res)
		require.NoError(t, err)
		assert.NotContains(t, string(wire), `"h"`)
	})

	t.Run("not completed returns where it stopped", func(t *testing.T) {
		res := get(t, writeRun(t, `{"kind":"no_report","failure":"execution_failed","stage":"build","summary":"The candidate did not build."}`))
		failed, ok := res.State.AsNotCompleted()
		require.True(t, ok)
		assert.Equal(t, &gensurla.VerificationNotCompleted{Kind: "execution_failed", Stage: "build", Summary: "The candidate did not build."}, failed)
	})

	t.Run("a run whose processes ended without a result is interrupted", func(t *testing.T) {
		res := get(t, writeRun(t, ""))
		failed, ok := res.State.AsNotCompleted()
		require.True(t, ok)
		assert.Equal(t, "interrupted", failed.Kind)
	})

	t.Run("a new control plane reads runs started by an earlier one", func(t *testing.T) {
		runID := writeRun(t, `{"kind":"no_report","failure":"interrupted","stage":"supervise","summary":"The verification was canceled."}`)
		restarted := newControlPlane(t, Options{Verification: cfg})
		res, err := restarted.GetVerification(alice, &gensurla.GetVerificationPayload{RunID: runID})
		require.NoError(t, err, "the run does not depend on the saved Change Card")
		assert.Equal(t, "chg", res.ChangeID)
	})

	t.Run("unknown run", func(t *testing.T) {
		_, err := cp.GetVerification(alice, &gensurla.GetVerificationPayload{RunID: verifyrun.NewRunID()})
		assert.ErrorContains(t, err, "unknown run_id")
	})
}

// verificationSetup returns a control plane with a verification
// configuration whose runs are supervised by a command that exits at once,
// and a confirmed card owned by alice.
func verificationSetup(t *testing.T) (*ControlPlane, *gensurla.ChangeCardResult, *VerificationConfig) {
	t.Helper()
	analysis, err := json.Marshal(CP1SemanticAnalysis{
		Summary:     "Refunds up to $50 are instant.",
		Ambiguities: []CP1ModelAmbiguity{},
		Examples: []CP1ModelExample{{
			Scenario: "Refund at the limit", InputSummary: "Order total $50.00",
			ExpectedOutcome: "Instant store credit", VerificationKind: "deterministic",
		}},
		PreservedBehaviors: []string{},
	})
	require.NoError(t, err)
	cfg := verificationConfig(t)
	cp := newControlPlane(t, Options{Analyzer: NewModelCP1Analyzer(newStubModelClient(t, string(analysis))), Verification: cfg})
	card, err := cp.ProposeChange(WithPrincipal(context.Background(), Principal{ID: "alice@example.com"}),
		&gensurla.ProposeChangePayload{ChangeID: "chg", Prompt: "Refunds up to $50 are instant."})
	require.NoError(t, err)
	require.Equal(t, StatusConfirmed, card.Status)
	return cp, card, cfg
}

// verificationConfig returns a verification configuration whose runs are
// supervised by a command that exits at once.
func verificationConfig(t *testing.T) *VerificationConfig {
	t.Helper()
	return &VerificationConfig{
		RunsDir: t.TempDir(),
		Plan: verify.Plan{
			CandidateRepo: "/repos/candidate", CandidateModule: ".", Binaries: map[string]string{"orders": "./cmd/orders"},
			SuiteRepo: "/repos/suite", SuiteCommit: strings.Repeat("b", 40), SuiteModule: "eval",
			SuiteCommand: "./cmd/orders-evals", ReportDir: t.TempDir(),
		},
		// The supervisor exits at once; these tests cover acceptance and
		// reading, and verifyrun's own tests run the real lifecycle.
		Command: []string{"/bin/sh", "-c", "exit 0", "sh"},
		Timeout: time.Hour,
	}
}
