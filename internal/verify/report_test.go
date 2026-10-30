package verify

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/eval"
)

func TestAssembleAccountsForEveryDeclaredScenario(t *testing.T) {
	both := testBinding().Artifacts
	ok := scenario("a", true, both)
	cases := []struct {
		name      string
		scenarios []eval.ScenarioReport
		suiteErr  string
		want      []Outcome
		counts    Counts
		passed    bool
	}{
		{"all passed", []eval.ScenarioReport{ok, scenario("b", true, both), scenario("h", true, both)}, "",
			[]Outcome{OutcomePassed, OutcomePassed, OutcomePassed}, Counts{Required: 3, Passed: 3}, true},
		{"missing scenario is not run, never passed", []eval.ScenarioReport{ok, scenario("h", true, both)}, "",
			[]Outcome{OutcomePassed, OutcomeNotRun, OutcomePassed}, Counts{Required: 3, Passed: 2, NotRun: 1}, false},
		{"failed check", []eval.ScenarioReport{ok, scenario("b", false, both), scenario("h", true, both)}, "",
			[]Outcome{OutcomePassed, OutcomeFailed, OutcomePassed}, Counts{Required: 3, Passed: 2, Failed: 1}, false},
		{"environment error", []eval.ScenarioReport{ok, {ID: "b", StartedAt: time.Now(), Error: "temporal never listened"}, scenario("h", true, both)}, "",
			[]Outcome{OutcomePassed, OutcomeHarnessError, OutcomePassed}, Counts{Required: 3, Passed: 2, HarnessError: 1}, false},
		{"canceled before start", []eval.ScenarioReport{ok, {ID: "b"}, scenario("h", true, both)}, "",
			[]Outcome{OutcomePassed, OutcomeNotRun, OutcomePassed}, Counts{Required: 3, Passed: 2, NotRun: 1}, false},
		{"passed against a different binary", []eval.ScenarioReport{ok, scenario("b", true, map[string]string{"orders": "other", "worker": "d2"}), scenario("h", true, both)}, "",
			[]Outcome{OutcomePassed, OutcomeHarnessError, OutcomePassed}, Counts{Required: 3, Passed: 2, HarnessError: 1}, false},
		{"passed reporting only one of two bound binaries", []eval.ScenarioReport{ok, scenario("b", true, map[string]string{"orders": "d1"}), scenario("h", true, both)}, "",
			[]Outcome{OutcomePassed, OutcomeHarnessError, OutcomePassed}, Counts{Required: 3, Passed: 2, HarnessError: 1}, false},
		{"passed reporting an extra binary", []eval.ScenarioReport{ok, scenario("b", true, map[string]string{"orders": "d1", "worker": "d2", "extra": "d3"}), scenario("h", true, both)}, "",
			[]Outcome{OutcomePassed, OutcomeHarnessError, OutcomePassed}, Counts{Required: 3, Passed: 2, HarnessError: 1}, false},
		{"passed without reporting a binary", []eval.ScenarioReport{ok, scenario("b", true, nil), scenario("h", true, both)}, "",
			[]Outcome{OutcomePassed, OutcomeHarnessError, OutcomePassed}, Counts{Required: 3, Passed: 2, HarnessError: 1}, false},
		{"suite-level error fails an all-green run", []eval.ScenarioReport{ok, scenario("b", true, both), scenario("h", true, both)}, "judge calibration failed",
			[]Outcome{OutcomePassed, OutcomePassed, OutcomePassed}, Counts{Required: 3, Passed: 3}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, err := Assemble(testBinding(), testSuite(), eval.Report{SuiteID: "orders_acceptance", Scenarios: c.scenarios, Error: c.suiteErr}, 0)
			require.NoError(t, err)
			got := make([]Outcome, len(r.Scenarios))
			for i, s := range r.Scenarios {
				got[i] = s.Outcome
			}
			assert.Equal(t, c.want, got)
			assert.Equal(t, c.counts, r.Counts)
			assert.Equal(t, c.passed, r.Passed)
			assert.Equal(t, c.suiteErr, r.SuiteError)
			assert.Equal(t, c.scenarios, r.Evidence.Scenarios, "the original evidence is kept unchanged")
		})
	}
}

func TestAssembleRejectsMalformedInput(t *testing.T) {
	both := testBinding().Artifacts
	cases := []struct {
		name     string
		suite    Suite
		evidence []eval.ScenarioReport
	}{
		{"empty suite", Suite{ID: "orders_acceptance"}, nil},
		{"empty scenario ID", Suite{ID: "orders_acceptance", Scenarios: []Scenario{{ID: ""}}}, nil},
		{"duplicate declared ID", Suite{ID: "orders_acceptance", Scenarios: []Scenario{{ID: "a"}, {ID: "a"}}}, nil},
		{"duplicate result ID", testSuite(), []eval.ScenarioReport{scenario("a", true, both), scenario("a", false, both)}},
		{"undeclared result ID", testSuite(), []eval.ScenarioReport{scenario("zzz", true, both)}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Assemble(testBinding(), c.suite, eval.Report{SuiteID: "orders_acceptance", Scenarios: c.evidence}, 0)
			assert.Error(t, err)
		})
	}
}

func TestForBuildersExposesNoHoldoutDetail(t *testing.T) {
	both := testBinding().Artifacts
	r, err := Assemble(testBinding(), testSuite(), eval.Report{SuiteID: "orders_acceptance", Scenarios: []eval.ScenarioReport{
		scenario("a", true, both), scenario("b", true, both), holdoutResult(),
	}}, 0)
	require.NoError(t, err)
	view := r.ForBuilders()
	assert.Equal(t, []BuilderScenario{
		{ID: "a", Outcome: OutcomePassed, Checks: []eval.Check{{Name: "check", Passed: true}}},
		{ID: "b", Outcome: OutcomePassed, Checks: []eval.Check{{Name: "check", Passed: true}}},
		{ID: "holdout-1", Outcome: OutcomeFailed},
	}, view.Scenarios)
	serialized, err := json.Marshal(view)
	require.NoError(t, err)
	for _, secret := range []string{`"h"`, "secret_holdout_check", "holdout diagnostic", "d1", "d2", "candidate:"} {
		assert.NotContains(t, string(serialized), secret, "the serialized builder response must not reveal %s", secret)
	}
}

func TestApproveRefusesAStaleOrInconsistentReport(t *testing.T) {
	both := testBinding().Artifacts
	r, err := Assemble(testBinding(), testSuite(), eval.Report{SuiteID: "orders_acceptance", Scenarios: []eval.ScenarioReport{
		scenario("a", true, both), scenario("b", true, both), scenario("h", true, both),
	}}, 0)
	require.NoError(t, err)
	require.NoError(t, Approve(testBinding(), r))

	changes := map[string]func(*Binding){
		"commit":        func(b *Binding) { b.CandidateCommit = "def" },
		"request":       func(b *Binding) { b.RequestRevision = "rev-2" },
		"suite":         func(b *Binding) { b.SuiteCommit = "suite-2" },
		"binary":        func(b *Binding) { b.Artifacts = map[string]string{"orders": "rebuilt", "worker": "d2"} },
		"environment":   func(b *Binding) { b.Environment = "staging" },
		"configuration": func(b *Binding) { b.ConfigDigest = "prompt-v2" },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			current := testBinding()
			change(&current)
			assert.ErrorIs(t, Approve(current, r), ErrStaleReport)
		})
	}

	failing, err := Assemble(testBinding(), testSuite(), eval.Report{SuiteID: "orders_acceptance", Scenarios: []eval.ScenarioReport{
		scenario("a", true, both), scenario("b", false, both), scenario("h", true, both),
	}}, 0)
	require.NoError(t, err)
	t.Run("forged verdict with a recomputed digest", func(t *testing.T) {
		forged := failing
		forged.Passed = true
		forged.Counts = Counts{Required: 3, Passed: 3}
		forged.Scenarios = append([]ScenarioResult(nil), failing.Scenarios...)
		forged.Scenarios[1].Outcome = OutcomePassed
		digest, err := forged.computeDigest()
		require.NoError(t, err)
		forged.Digest = digest
		assert.ErrorIs(t, Approve(testBinding(), forged), ErrStaleReport)
	})
	t.Run("suite exited without running cleanly", func(t *testing.T) {
		crashed, err := Assemble(testBinding(), testSuite(), eval.Report{SuiteID: "orders_acceptance", Scenarios: []eval.ScenarioReport{
			scenario("a", true, both), scenario("b", true, both), scenario("h", true, both),
		}}, 2)
		require.NoError(t, err)
		assert.False(t, crashed.Passed)
		assert.ErrorIs(t, Approve(testBinding(), crashed), ErrStaleReport)
	})
	t.Run("contradicted verdict with the original digest kept", func(t *testing.T) {
		contradicted := failing
		contradicted.Passed = true
		contradicted.Counts = Counts{Required: 3, Passed: 3}
		assert.ErrorIs(t, Approve(testBinding(), contradicted), ErrStaleReport)
	})
	t.Run("failed report", func(t *testing.T) {
		assert.ErrorIs(t, Approve(testBinding(), failing), ErrStaleReport)
	})
}

// holdoutResult is a failed holdout scenario whose name, checks and
// diagnostics must stay inside the evaluator.
func holdoutResult() eval.ScenarioReport {
	s := scenario("h", false, testBinding().Artifacts)
	s.Result.Checks = []eval.Check{{Name: "secret_holdout_check", Passed: false, Diagnostic: "holdout diagnostic"}}
	return s
}

// testBinding returns a binding for two candidate binaries.
func testBinding() Binding {
	return Binding{
		CandidateCommit: "abc", RequestRevision: "rev-1", SuiteID: "orders_acceptance", SuiteCommit: "suite-1",
		Artifacts: map[string]string{"orders": "d1", "worker": "d2"}, Environment: "sim", ConfigDigest: "prompt-v1",
	}
}

// testSuite declares two repair scenarios and one holdout.
func testSuite() Suite {
	return Suite{ID: "orders_acceptance", Scenarios: []Scenario{{ID: "a"}, {ID: "b"}, {ID: "h", Holdout: true}}}
}

// scenario builds a started scenario report that reports executing the given
// binaries (name to digest).
func scenario(id string, passed bool, binaries map[string]string) eval.ScenarioReport {
	res := &eval.Result{Checks: []eval.Check{{Name: "check", Passed: passed}}}
	for name, digest := range binaries {
		res.Artifacts = append(res.Artifacts, eval.Artifact{Name: "candidate:" + name, URI: "sha256:" + digest})
	}
	return eval.ScenarioReport{ID: id, StartedAt: time.Now(), Result: res, Passed: passed}
}
