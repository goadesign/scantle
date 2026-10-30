//go:build e2e

package e2e

import (
	"net/http"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/goadesign/scantle/examples/orders-eval/sim"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRefundSurvivesWorkerCrash kills the refund worker with SIGKILL right
// after the payment provider accepted a refund and before the worker could
// record that it succeeded. A replacement worker must finish the refund
// without paying twice: Temporal retries the activity and the provider returns
// the original receipt for the repeated idempotency key.
func TestRefundSurvivesWorkerCrash(t *testing.T) {
	ctx := t.Context()
	bin := buildBinaries(t)
	temporalAddr := startTemporal(t)

	s := sim.New()
	simURL, err := s.Serve(ctx)
	require.NoError(t, err)
	s.Reset(fixture())

	workerArgs := []string{"--env", "sim", "--payments-url", simURL, "--temporal", temporalAddr, "--refund-queue", "crash.refunds"}
	worker := start(t, filepath.Join(bin, "orders-worker"), nil, workerArgs...)
	api := freeAddr(t)
	start(t, filepath.Join(bin, "orders"), []string{"ORDERS_JWT_KEY=" + signingKey},
		"--env", "sim", "--commerce-url", simURL, "--temporal", temporalAddr, "--listen", api,
		"--agent-queue", "crash.agents", "--refund-queue", "crash.refunds", "--model-provider", "scripted")
	waitListening(t, api)
	base := "http://" + api
	customer := token(t, "t1", "c1", "orders:chat")

	accepted := s.HoldNextRefund()
	run := post(t, base+"/chat", customer, map[string]any{"message": "Please refund order o-1"}, http.StatusAccepted)

	select {
	case orderID := <-accepted:
		assert.Equal(t, "o-1", orderID)
	case <-time.After(60 * time.Second):
		t.Fatal("the refund never reached the payment provider")
	}
	ledger, _ := s.Refunds()
	require.Len(t, ledger, 1, "the provider accepted the refund before the crash")

	require.NoError(t, worker.Process.Signal(syscall.SIGKILL))
	_ = worker.Wait()
	start(t, filepath.Join(bin, "orders-worker"), nil, workerArgs...)

	done := waitStatus(t, base, customer, run.RunID, "completed")
	assert.Contains(t, done.Reply, "refunded")
	ledger, calls := s.Refunds()
	require.Len(t, ledger, 1, "exactly one refund despite the crash")
	assert.Equal(t, int64(3850), ledger[0].AmountCents)
	assert.GreaterOrEqual(t, calls, 2, "the replacement worker retried the refund with the same key")
}
