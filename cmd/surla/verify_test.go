package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goadesign/surla/internal/verify"
)

func TestParseVerifyFlagsBuildsTheExplicitPlan(t *testing.T) {
	complete := []string{
		"-candidate-repo", "/repos/candidate",
		"-candidate-commit", "1111111111111111111111111111111111111111",
		"-candidate-module", "examples/orders",
		"-binary", "orders=./cmd/orders",
		"-binary", "orders-worker=./cmd/orders-worker",
		"-suite-repo", "/repos/suite",
		"-suite-commit", "2222222222222222222222222222222222222222",
		"-suite-module", "examples/orders-eval",
		"-suite-command", "./cmd/orders_acceptance-evals",
		"-request-revision", "rev-1",
		"-work-dir", "/private/work",
		"-report-dir", "/private/reports",
	}
	wantPlan := verify.Plan{
		CandidateRepo:   "/repos/candidate",
		CandidateCommit: "1111111111111111111111111111111111111111",
		CandidateModule: "examples/orders",
		Binaries:        map[string]string{"orders": "./cmd/orders", "orders-worker": "./cmd/orders-worker"},
		SuiteRepo:       "/repos/suite",
		SuiteCommit:     "2222222222222222222222222222222222222222",
		SuiteModule:     "examples/orders-eval",
		SuiteCommand:    "./cmd/orders_acceptance-evals",
		RequestRevision: "rev-1",
		WorkDir:         "/private/work",
		ReportDir:       "/private/reports",
	}

	cases := []struct {
		name            string
		args            []string
		wantBuilderView bool
		wantErr         string
	}{
		{name: "complete plan", args: complete},
		{name: "builder view", args: append([]string{"-builder-view"}, complete...), wantBuilderView: true},
		{
			name:    "no flags names every required flag",
			args:    nil,
			wantErr: "verify requires -candidate-repo, -candidate-commit, -suite-repo, -suite-commit, -suite-command, -request-revision, -work-dir, -report-dir, -binary",
		},
		{name: "missing request revision", args: without(complete, "-request-revision"), wantErr: "verify requires -request-revision"},
		{name: "missing binaries", args: without(complete, "-binary"), wantErr: "verify requires -binary"},
		{name: "repeated binary name", args: append(complete, "-binary", "orders=./cmd/other"), wantErr: `binary name "orders" is repeated`},
		{name: "binary without package", args: append(complete, "-binary", "tool="), wantErr: `binary "tool=" must be name=package`},
		{name: "binary without name", args: append(complete, "-binary", "=./cmd/tool"), wantErr: `binary "=./cmd/tool" must be name=package`},
		{name: "binary without separator", args: append(complete, "-binary", "tool"), wantErr: `binary "tool" must be name=package`},
		{name: "positional argument", args: append(complete, "extra"), wantErr: `unexpected arguments ["extra"]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan, builderView, err := parseVerifyFlags(tc.args)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, wantPlan, plan)
			assert.Equal(t, tc.wantBuilderView, builderView)
		})
	}
	t.Run("module directories default to the repository root", func(t *testing.T) {
		plan, _, err := parseVerifyFlags(without(without(complete, "-candidate-module"), "-suite-module"))
		require.NoError(t, err)
		want := wantPlan
		want.CandidateModule, want.SuiteModule = ".", "."
		assert.Equal(t, want, plan)
	})
}

func TestWriteVerifyReportPrintsBeforeFailing(t *testing.T) {
	report := verify.Report{
		Digest: "abc123",
		Counts: verify.Counts{Required: 2, Passed: 1, Failed: 1},
		Scenarios: []verify.ScenarioResult{
			{ID: "refund_within_limit", Outcome: verify.OutcomePassed},
			{ID: "cross_customer_refund_refused", Holdout: true, Outcome: verify.OutcomeFailed},
		},
	}
	cases := []struct {
		name        string
		passed      bool
		builderView bool
		wantIDs     []string
		wantErr     string
	}{
		{name: "passed full report", passed: true, wantIDs: []string{"refund_within_limit", "cross_customer_refund_refused"}},
		{name: "failed full report", wantIDs: []string{"refund_within_limit", "cross_customer_refund_refused"}, wantErr: "verification did not pass (report abc123)"},
		{name: "failed builder view hides holdout names", builderView: true, wantIDs: []string{"refund_within_limit", "holdout-1"}, wantErr: "verification did not pass (report abc123)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := report
			r.Passed = tc.passed
			var out bytes.Buffer
			err := writeVerifyReport(&out, r, tc.builderView)
			if tc.wantErr != "" {
				require.EqualError(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
			}
			var printed struct {
				Scenarios []struct {
					ID string `json:"id"`
				} `json:"scenarios"`
			}
			require.NoError(t, json.Unmarshal(out.Bytes(), &printed))
			ids := make([]string, 0, len(printed.Scenarios))
			for _, s := range printed.Scenarios {
				ids = append(ids, s.ID)
			}
			assert.Equal(t, tc.wantIDs, ids)
			if tc.builderView {
				assert.NotContains(t, out.String(), "cross_customer_refund_refused")
			}
		})
	}
}

func TestRunPrintsHelpAndDispatchesCommands(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		wantUsage bool
		wantErr   string
	}{
		{name: "no arguments", args: nil, wantUsage: true},
		{name: "help", args: []string{"help"}, wantUsage: true},
		{name: "-h", args: []string{"-h"}, wantUsage: true},
		{name: "--help", args: []string{"--help"}, wantUsage: true},
		{name: "help for a command", args: []string{"help", "verify"}},
		{name: "command -h", args: []string{"preview", "-h"}},
		{name: "command --help", args: []string{"approve", "--help"}},
		{name: "help for an unknown command", args: []string{"help", "deploy"}, wantErr: `unknown command "deploy"`},
		{name: "help with two commands", args: []string{"help", "verify", "approve"}, wantErr: "unexpected arguments"},
		{name: "unknown command", args: []string{"unknown"}, wantErr: `unknown command "unknown"; run "surla help"`},
		{name: "verify without flags", args: []string{"verify"}, wantErr: "verify requires -candidate-repo"},
		{name: "approve without flags", args: []string{"approve"}, wantErr: "approve requires -report"},
		{name: "serve without credentials", args: []string{"serve"}, wantErr: "serve requires -principal and -auth-token"},
		{name: "preview with a bad flag", args: []string{"preview", "-nope"}, wantErr: "flag provided but not defined"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SURLA_AUTH_TOKEN", "")
			var out bytes.Buffer
			err := run(tc.args, &out)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			if tc.wantUsage {
				assert.Equal(t, usage, out.String())
			} else {
				assert.Empty(t, out.String())
			}
		})
	}
}

// without returns a copy of args with every occurrence of the flag named name
// and its value removed.
func without(args []string, name string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == name {
			i++
			continue
		}
		out = append(out, args[i])
	}
	return out
}
