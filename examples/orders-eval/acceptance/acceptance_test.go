//go:build e2e

package acceptance

import (
	"os/exec"
	"path/filepath"
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
	bin := t.TempDir()
	for _, cmd := range []string{"orders", "orders-worker"} {
		build := exec.Command("go", "build", "-o", filepath.Join(bin, cmd), "./cmd/"+cmd)
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
	suite, err := genevals.New(New(env), genevals.Inputs{})
	require.NoError(t, err)
	runner, err := eval.NewRunner(nil, eval.RunnerConfig{MaxConcurrency: 4})
	require.NoError(t, err)

	report, err := runner.Run(t.Context(), suite)
	require.NoError(t, err)
	for _, s := range report.Scenarios {
		assert.True(t, s.Passed, "scenario %s failed: %+v", s.ID, s)
	}
	assert.True(t, report.Passed)
}
