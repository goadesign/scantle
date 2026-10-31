package controlplane

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"time"

	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/goadesign/surla/internal/verify"
	"github.com/goadesign/surla/internal/verifyrun"
)

// VerificationConfig is the operator's Checkpoint 4 configuration: what is
// built, which evaluator suite runs, and where runs and reports live. A caller
// of verify_change names only the Change Card revision and the candidate
// commit; everything here comes from the operator, so a builder cannot choose
// how its own work is graded.
type VerificationConfig struct {
	// RunsDir holds one directory per verification run (see verifyrun). It
	// must be private to the evaluator.
	RunsDir string
	// Plan is the operator's part of every run's verify.Plan: CandidateRepo,
	// CandidateModule, Binaries, SuiteRepo, SuiteCommit, SuiteModule,
	// SuiteCommand and ReportDir. CandidateCommit, RequestRevision and
	// WorkDir are set per run and must be empty here.
	Plan verify.Plan
	// Command is the argument list that runs this surla executable; runs
	// start `<Command> verify-run supervise <dir>`.
	Command []string
}

// errVerificationUnavailable is returned by the CP4 verification methods of a
// control plane without a VerificationConfig.
var errVerificationUnavailable = errors.New("verification is not configured on this control plane")

// VerifyChange starts Checkpoint 4 verification of a candidate commit for the
// caller's confirmed Change Card revision and returns at once with the run in
// the running state. The run is accepted into its own directory under the
// configured RunsDir and carried out by processes that outlive this call and
// this server (see verifyrun). The caller must own the card, the card must be
// confirmed, and request_revision must be its current revision.
func (c *ControlPlane) VerifyChange(ctx context.Context, p *gensurla.VerifyChangePayload) (*gensurla.VerificationStatusResult, error) {
	principal, ok := PrincipalFromContext(ctx)
	if !ok {
		return nil, errors.New("authenticated principal is required")
	}
	if c.verification == nil {
		return nil, errVerificationUnavailable
	}
	c.mu.RLock()
	record, exists := c.cards[p.ChangeID]
	c.mu.RUnlock()
	switch {
	case !exists:
		return nil, fmt.Errorf("unknown change_id %q", p.ChangeID)
	case record.owner != principal.ID:
		return nil, fmt.Errorf("principal %q is not the owner of change_id %q", principal.ID, p.ChangeID)
	case record.card.Status != StatusConfirmed:
		return nil, fmt.Errorf("change_id %q must be confirmed before verification (status %q)", p.ChangeID, record.card.Status)
	case record.card.RequestRevision != p.RequestRevision:
		return nil, fmt.Errorf("request_revision mismatch for %q: got %q, want %q", p.ChangeID, p.RequestRevision, record.card.RequestRevision)
	}
	plan := c.verification.Plan
	plan.CandidateCommit = p.CandidateCommit
	plan.RequestRevision = p.RequestRevision
	req := verifyrun.Request{
		RunID:      verifyrun.NewRunID(),
		Owner:      principal.ID,
		ChangeID:   p.ChangeID,
		AcceptedAt: time.Now().UTC(),
		Plan:       plan,
	}
	if _, err := verifyrun.Start(c.verification.RunsDir, req, c.verification.Command); err != nil {
		return nil, fmt.Errorf("start verification: %w", err)
	}
	return statusResult(req, verifyrun.State{Kind: verifyrun.StateRunning}), nil
}

// GetVerification returns the state of a verification run to the principal
// that started it. The run is read from its directory, not from the Change
// Card, so it keeps the revision it was started for and stays readable after
// the card changes or this server restarts. A completed run returns only the
// builder view of its report.
func (c *ControlPlane) GetVerification(ctx context.Context, p *gensurla.GetVerificationPayload) (*gensurla.VerificationStatusResult, error) {
	principal, ok := PrincipalFromContext(ctx)
	if !ok {
		return nil, errors.New("authenticated principal is required")
	}
	if c.verification == nil {
		return nil, errVerificationUnavailable
	}
	req, state, err := verifyrun.Read(c.verification.RunsDir, p.RunID)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("unknown run_id %q", p.RunID)
	case err != nil:
		return nil, fmt.Errorf("read verification %s: %w", p.RunID, err)
	case req.Owner != principal.ID:
		return nil, fmt.Errorf("principal %q is not the owner of run_id %q", principal.ID, p.RunID)
	}
	return statusResult(req, state), nil
}

// statusResult returns the wire form of a run: its request and its state,
// with a completed run's report reduced to the builder view.
func statusResult(req verifyrun.Request, state verifyrun.State) *gensurla.VerificationStatusResult {
	res := &gensurla.VerificationStatusResult{
		RunID:           req.RunID,
		ChangeID:        req.ChangeID,
		RequestRevision: req.Plan.RequestRevision,
		CandidateCommit: req.Plan.CandidateCommit,
		SuiteCommit:     req.Plan.SuiteCommit,
	}
	switch state.Kind {
	case verifyrun.StateRunning:
		res.State = gensurla.NewStateRunning(&gensurla.VerificationRunning{StartedAt: req.AcceptedAt.Format(time.RFC3339)})
	case verifyrun.StateCompleted:
		res.State = gensurla.NewStateCompleted(&gensurla.VerificationCompleted{
			SuiteID:     state.Report.Binding.SuiteID,
			Environment: state.Report.Binding.Environment,
			Report:      builderReport(state.Report.ForBuilders()),
		})
	case verifyrun.StateNotCompleted:
		res.State = gensurla.NewStateNotCompleted(&gensurla.VerificationNotCompleted{
			Kind:    string(state.Result.Failure),
			Stage:   state.Result.Stage,
			Summary: state.Result.Summary,
		})
	}
	return res
}

// builderReport converts the builder view of a report to its wire form.
func builderReport(b verify.BuilderReport) *gensurla.BuilderVerificationReport {
	out := &gensurla.BuilderVerificationReport{
		ReportDigest: b.ReportDigest,
		Counts: &gensurla.VerificationCounts{
			Required: b.Counts.Required, Passed: b.Counts.Passed, Failed: b.Counts.Failed,
			NotRun: b.Counts.NotRun, HarnessError: b.Counts.HarnessError,
		},
		Passed:      b.Passed,
		SuiteFailed: b.SuiteFailed,
		Scenarios:   make([]*gensurla.VerificationScenario, 0, len(b.Scenarios)),
	}
	for _, s := range b.Scenarios {
		scenario := &gensurla.VerificationScenario{ID: s.ID, Outcome: string(s.Outcome)}
		if s.Error != "" {
			scenario.Error = &s.Error
		}
		for _, check := range s.Checks {
			wire := &gensurla.VerificationCheck{Name: check.Name, Passed: check.Passed}
			if check.Diagnostic != "" {
				wire.Diagnostic = &check.Diagnostic
			}
			scenario.Checks = append(scenario.Checks, wire)
		}
		out.Scenarios = append(out.Scenarios, scenario)
	}
	return out
}
