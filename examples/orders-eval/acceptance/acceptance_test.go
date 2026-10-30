//go:build e2e

package acceptance

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	genevals "github.com/goadesign/scantle/examples/orders-eval/gen/evals/orders_acceptance"
	"github.com/goadesign/scantle/examples/orders-eval/harness"
	"github.com/goadesign/scantle/verify/candidate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/eval"
)

// TestAcceptanceSuite builds the candidate binaries from ../../orders and runs
// every scenario of the acceptance suite against them. It needs the temporal
// CLI. Run with: go test -tags e2e ./acceptance
func TestAcceptanceSuite(t *testing.T) {
	report := runSuite(t, buildCandidate(t, nil))
	for _, s := range report.Scenarios {
		assert.True(t, s.Passed, "scenario %s failed: %s", s.ID, describe(s))
	}
	assert.True(t, report.Passed)
}

// TestSeededPayloadBloatFailsTheBudget builds the candidate with its
// list_orders page raised from 10 to 50 orders. The candidate still refunds
// the right order, so every behavior check passes, but it sends the model 40
// orders it never needed, and the token budget check fails the scenario.
func TestSeededPayloadBloatFailsTheBudget(t *testing.T) {
	src := filepath.Join("..", "..", "orders", "internal", "orders", "service.go")
	original, err := os.ReadFile(src)
	require.NoError(t, err)
	const pageSize = "ordersPageSize = 10"
	require.Equal(t, 1, strings.Count(string(original), pageSize), "the seed must change exactly the page size")
	seeded := filepath.Join(t.TempDir(), "service.go")
	require.NoError(t, os.WriteFile(seeded, []byte(strings.Replace(string(original), pageSize, "ordersPageSize = 50", 1)), 0o600))
	abs, err := filepath.Abs(src)
	require.NoError(t, err)

	report := runSuite(t, buildCandidate(t, map[string]string{abs: seeded}), "store_credit_within_limit")
	require.Len(t, report.Scenarios, 1)
	s := report.Scenarios[0]
	require.NotNil(t, s.Result, describe(s))
	assert.False(t, s.Passed, "the bloated candidate must not pass")
	for _, c := range s.Result.Checks {
		if c.Name == "within_token_budget" {
			assert.False(t, c.Passed, "the budget check fails")
			t.Logf("budget check: %s", c.Diagnostic)
			continue
		}
		assert.True(t, c.Passed, "behavior check %s still passes: %s", c.Name, c.Diagnostic)
	}
}

// buildCandidate builds and pins the candidate binaries from ../../orders,
// with overlay replacing source files (absolute path to replacement path).
func buildCandidate(t *testing.T, overlay map[string]string) *harness.Env {
	t.Helper()
	bin := t.TempDir()
	args := []string{"build"}
	if len(overlay) > 0 {
		b, err := json.Marshal(map[string]any{"Replace": overlay})
		require.NoError(t, err)
		path := filepath.Join(t.TempDir(), "overlay.json")
		require.NoError(t, os.WriteFile(path, b, 0o600))
		args = append(args, "-overlay", path)
	}
	for _, cmd := range []string{"orders", "orders-worker"} {
		build := exec.Command("go", append(args, "-o", filepath.Join(bin, cmd), "./cmd/"+cmd)...)
		build.Dir = filepath.Join("..", "..", "orders")
		out, err := build.CombinedOutput()
		require.NoError(t, err, "build %s: %s", cmd, out)
	}
	store := filepath.Join(t.TempDir(), "artifacts")
	t.Cleanup(func() { require.NoError(t, candidate.RemoveStore(store)) })
	orders, err := candidate.Pin(store, filepath.Join(bin, "orders"))
	require.NoError(t, err)
	worker, err := candidate.Pin(store, filepath.Join(bin, "orders-worker"))
	require.NoError(t, err)
	env, err := harness.NewEnv(orders, worker)
	require.NoError(t, err)
	return env
}

// runSuite runs the given scenarios, or all of them, against env.
func runSuite(t *testing.T, env *harness.Env, scenarios ...string) eval.Report {
	t.Helper()
	suite, err := genevals.New(New(env), genevals.Inputs{})
	require.NoError(t, err)
	runner, err := eval.NewRunner(nil, eval.RunnerConfig{MaxConcurrency: 4})
	require.NoError(t, err)
	var report eval.Report
	if len(scenarios) > 0 {
		report, err = runner.RunScenarios(t.Context(), suite, scenarios...)
	} else {
		report, err = runner.Run(t.Context(), suite)
	}
	require.NoError(t, err)
	return report
}

// describe lists a scenario's environment error and its failed checks with
// their diagnostics.
func describe(s eval.ScenarioReport) string {
	if s.Error != "" {
		return "error: " + s.Error
	}
	if s.Result == nil {
		return "no result"
	}
	var failed []string
	for _, c := range s.Result.Checks {
		if !c.Passed {
			failed = append(failed, c.Name+": "+c.Diagnostic)
		}
	}
	return strings.Join(failed, "; ")
}
