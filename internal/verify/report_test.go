package verify

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/eval"
)

func TestAssembleAccountsForEveryDeclaredScenario(t *testing.T) {
	binding := testBinding()
	ok := scenario("a", true, binding.Artifacts["orders"])
	cases := []struct {
		name      string
		scenarios []eval.ScenarioReport
		want      []Outcome
		counts    Counts
		passed    bool
	}{
		{"all passed", []eval.ScenarioReport{ok, scenario("b", true, binding.Artifacts["orders"]), scenario("h", true, binding.Artifacts["orders"])},
			[]Outcome{OutcomePassed, OutcomePassed, OutcomePassed}, Counts{Required: 3, Passed: 3}, true},
		{"missing scenario is not run, never passed", []eval.ScenarioReport{ok, scenario("h", true, binding.Artifacts["orders"])},
			[]Outcome{OutcomePassed, OutcomeNotRun, OutcomePassed}, Counts{Required: 3, Passed: 2, NotRun: 1}, false},
		{"failed check", []eval.ScenarioReport{ok, scenario("b", false, binding.Artifacts["orders"]), scenario("h", true, binding.Artifacts["orders"])},
			[]Outcome{OutcomePassed, OutcomeFailed, OutcomePassed}, Counts{Required: 3, Passed: 2, Failed: 1}, false},
		{"environment error", []eval.ScenarioReport{ok, {ID: "b", StartedAt: time.Now(), Error: "temporal never listened"}, scenario("h", true, binding.Artifacts["orders"])},
			[]Outcome{OutcomePassed, OutcomeHarnessError, OutcomePassed}, Counts{Required: 3, Passed: 2, HarnessError: 1}, false},
		{"canceled before start", []eval.ScenarioReport{ok, {ID: "b"}, scenario("h", true, binding.Artifacts["orders"])},
			[]Outcome{OutcomePassed, OutcomeNotRun, OutcomePassed}, Counts{Required: 3, Passed: 2, NotRun: 1}, false},
		{"passed against a different binary", []eval.ScenarioReport{ok, scenario("b", true, "other-digest"), scenario("h", true, binding.Artifacts["orders"])},
			[]Outcome{OutcomePassed, OutcomeHarnessError, OutcomePassed}, Counts{Required: 3, Passed: 2, HarnessError: 1}, false},
		{"passed without reporting a binary", []eval.ScenarioReport{ok, scenario("b", true, ""), scenario("h", true, binding.Artifacts["orders"])},
			[]Outcome{OutcomePassed, OutcomeHarnessError, OutcomePassed}, Counts{Required: 3, Passed: 2, HarnessError: 1}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, err := Assemble(binding, testSuite(), eval.Report{SuiteID: "orders_acceptance", Scenarios: c.scenarios})
			require.NoError(t, err)
			got := make([]Outcome, len(r.Scenarios))
			for i, s := range r.Scenarios {
				got[i] = s.Outcome
			}
			assert.Equal(t, c.want, got)
			assert.Equal(t, c.counts, r.Counts)
			assert.Equal(t, c.passed, r.Passed)
		})
	}
}

func TestBuilderViewRedactsHoldouts(t *testing.T) {
	binding := testBinding()
	r, err := Assemble(binding, testSuite(), eval.Report{SuiteID: "orders_acceptance", Scenarios: []eval.ScenarioReport{
		scenario("a", true, binding.Artifacts["orders"]),
		scenario("b", true, binding.Artifacts["orders"]),
		scenario("h", false, binding.Artifacts["orders"]),
	}})
	require.NoError(t, err)
	view := r.BuilderView()
	holdout := view.Scenarios[2]
	assert.Equal(t, "holdout-3", holdout.ID, "the holdout's name is hidden")
	assert.Nil(t, holdout.Detail, "the holdout's checks and diagnostics are hidden")
	assert.Equal(t, OutcomeFailed, holdout.Outcome, "builders still learn that a holdout failed")
	assert.Equal(t, r.Counts, view.Counts)
	assert.NotNil(t, r.Scenarios[2].Detail, "the operator report keeps the detail")
}

func TestApproveRefusesAStaleReport(t *testing.T) {
	binding := testBinding()
	r, err := Assemble(binding, testSuite(), eval.Report{SuiteID: "orders_acceptance", Scenarios: []eval.ScenarioReport{
		scenario("a", true, binding.Artifacts["orders"]),
		scenario("b", true, binding.Artifacts["orders"]),
		scenario("h", true, binding.Artifacts["orders"]),
	}})
	require.NoError(t, err)
	require.NoError(t, Approve(binding, r))

	changes := map[string]func(*Binding){
		"commit":        func(b *Binding) { b.CandidateCommit = "def" },
		"request":       func(b *Binding) { b.RequestRevision = "rev-2" },
		"suite":         func(b *Binding) { b.SuiteCommit = "suite-2" },
		"binary":        func(b *Binding) { b.Artifacts = map[string]string{"orders": "rebuilt"} },
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
	t.Run("edited report", func(t *testing.T) {
		edited := r
		edited.Counts.Failed = 0
		edited.Scenarios = append([]ScenarioResult(nil), r.Scenarios...)
		edited.Scenarios[0].Outcome = OutcomeFailed
		assert.ErrorIs(t, Approve(binding, edited), ErrStaleReport)
	})
}

// testBinding returns a binding for one orders binary.
func testBinding() Binding {
	return Binding{
		CandidateCommit: "abc", RequestRevision: "rev-1", SuiteID: "orders_acceptance", SuiteCommit: "suite-1",
		Artifacts: map[string]string{"orders": "d1"}, Environment: "sim", ConfigDigest: "prompt-v1",
	}
}

// testSuite declares two repair scenarios and one holdout.
func testSuite() Suite {
	return Suite{ID: "orders_acceptance", Scenarios: []Scenario{{ID: "a"}, {ID: "b"}, {ID: "h", Holdout: true}}}
}

// scenario builds a started scenario report that reports executing the orders
// binary with digest, or no binary when digest is empty.
func scenario(id string, passed bool, digest string) eval.ScenarioReport {
	res := &eval.Result{Checks: []eval.Check{{Name: "check", Passed: passed}}}
	if digest != "" {
		res.Artifacts = []eval.Artifact{{Name: "candidate:orders", URI: "sha256:" + digest}}
	}
	return eval.ScenarioReport{ID: id, StartedAt: time.Now(), Result: res, Passed: passed}
}
