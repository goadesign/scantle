//go:build e2e

// Package e2e drives the built orders binaries through their public HTTP API
// against the evaluator-hosted simulator and a local Temporal dev server.
// Run with: go test -tags e2e ./e2e
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gencommerce "github.com/goadesign/scantle/examples/orders/gen/commerce"
	"github.com/goadesign/scantle/examples/orders-eval/sim"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runView is the orders API's run representation.
type runView struct {
	RunID          string `json:"run_id"`
	Status         string `json:"status"`
	Reply          string `json:"reply"`
	ApprovalID     string `json:"approval_id"`
	ApprovalPrompt string `json:"approval_prompt"`
}

const signingKey = "e2e-signing-key"

func TestRefundFlows(t *testing.T) {
	ctx := t.Context()
	bin := buildBinaries(t)
	temporalAddr := startTemporal(t)

	s := sim.New()
	simURL, err := s.Serve(ctx)
	require.NoError(t, err)
	start(t, filepath.Join(bin, "orders-worker"), nil,
		"--env", "sim", "--payments-url", simURL, "--temporal", temporalAddr, "--refund-queue", "e2e.refunds")
	api := freeAddr(t)
	start(t, filepath.Join(bin, "orders"), []string{"ORDERS_JWT_KEY=" + signingKey},
		"--env", "sim", "--commerce-url", simURL, "--temporal", temporalAddr, "--listen", api,
		"--agent-queue", "e2e.agents", "--refund-queue", "e2e.refunds", "--model-provider", "scripted")
	waitListening(t, api)
	base := "http://" + api

	customer := token(t, "t1", "c1", "orders:chat")

	t.Run("order of $38.50 is refunded as store credit without approval", func(t *testing.T) {
		s.Reset(fixture())
		run := post(t, base+"/chat", customer, map[string]any{"message": "Please refund order o-1"}, http.StatusAccepted)
		done := waitStatus(t, base, customer, run.RunID, "completed")
		assert.Contains(t, done.Reply, "refunded")
		ledger, _ := s.Refunds()
		require.Len(t, ledger, 1)
		assert.Equal(t, "store_credit", ledger[0].Method)
		assert.Equal(t, int64(3850), ledger[0].AmountCents)
	})

	t.Run("order of $54.00 waits for a manager and only a manager may approve", func(t *testing.T) {
		s.Reset(fixture())
		run := post(t, base+"/chat", customer, map[string]any{"message": "Please refund order o-2"}, http.StatusAccepted)
		waiting := waitStatus(t, base, customer, run.RunID, "awaiting_approval")
		require.NotEmpty(t, waiting.ApprovalID)
		ledger, _ := s.Refunds()
		assert.Empty(t, ledger, "no money moves before approval")

		decide := fmt.Sprintf("%s/runs/%s/approval", base, waiting.RunID)
		approve := map[string]any{"approval_id": waiting.ApprovalID, "approved": true}
		post(t, decide, customer, approve, http.StatusForbidden)
		post(t, decide, token(t, "t2", "m2", "orders:chat refunds:approve"), approve, http.StatusNotFound)
		post(t, decide, token(t, "t1", "m1", "orders:chat refunds:approve"), map[string]any{"approval_id": "stale", "approved": true}, http.StatusNotFound)
		post(t, decide, token(t, "t1", "m1", "orders:chat refunds:approve"), approve, http.StatusAccepted)

		done := waitStatus(t, base, customer, run.RunID, "completed")
		assert.Contains(t, done.Reply, "refunded")
		ledger, _ = s.Refunds()
		require.Len(t, ledger, 1)
		assert.Equal(t, "card", ledger[0].Method)
		assert.Equal(t, int64(5400), ledger[0].AmountCents, "the total includes tax and shipping")
	})
}

// fixture returns customer c1's orders: $38.50 and $54.00 ($48.00 + $6.00 shipping).
func fixture() sim.Fixture {
	return sim.Fixture{TenantID: "t1", CustomerID: "c1", Orders: []*gencommerce.Order{
		{ID: "o-1", PlacedAt: "2026-10-01T10:00:00Z", ItemsCents: 3500, TaxCents: 350, TotalCents: 3850},
		{ID: "o-2", PlacedAt: "2026-10-02T10:00:00Z", ItemsCents: 4800, ShippingCents: 600, TotalCents: 5400},
	}}
}

// buildBinaries builds the orders commands into a temporary directory.
func buildBinaries(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, cmd := range []string{"orders", "orders-worker"} {
		build := exec.Command("go", "build", "-o", filepath.Join(dir, cmd), "./cmd/"+cmd)
		build.Dir = filepath.Join("..", "..", "orders")
		out, err := build.CombinedOutput()
		require.NoError(t, err, "build %s: %s", cmd, out)
	}
	return dir
}

// startTemporal starts an in-memory Temporal dev server and returns its address.
func startTemporal(t *testing.T) string {
	t.Helper()
	addr := freeAddr(t)
	_, port, err := net.SplitHostPort(addr)
	require.NoError(t, err)
	start(t, "temporal", nil, "server", "start-dev", "--headless", "--ip", "127.0.0.1", "--port", port,
		"--ui-port", strings.TrimPrefix(freeAddr(t), "127.0.0.1:"), "--log-level", "error")
	waitListening(t, addr)
	return addr
}

// start runs a command until the test ends, logging its output on failure,
// and returns it so a test can kill it early.
func start(t *testing.T, name string, env []string, args ...string) *exec.Cmd {
	t.Helper()
	var out bytes.Buffer
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout, cmd.Stderr = &out, &out
	require.NoError(t, cmd.Start(), "start %s", name)
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if t.Failed() {
			t.Logf("%s output:\n%s", filepath.Base(name), out.String())
		}
	})
	return cmd
}

// freeAddr returns a loopback address with a free port.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	return addr
}

// waitListening waits until addr accepts TCP connections.
func waitListening(t *testing.T, addr string) {
	t.Helper()
	require.Eventually(t, func() bool {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err != nil {
			return false
		}
		_ = c.Close()
		return true
	}, 60*time.Second, 200*time.Millisecond, "%s never listened", addr)
}

// waitStatus polls the run until it reaches want.
func waitStatus(t *testing.T, base, bearer, runID, want string) runView {
	t.Helper()
	var last runView
	require.Eventually(t, func() bool {
		last = get(t, base+"/runs/"+runID, bearer)
		return last.Status == want || last.Status == "failed"
	}, 90*time.Second, 250*time.Millisecond, "run %s never reached %s (last %+v)", runID, want, last)
	require.Equal(t, want, last.Status, "run %s ended in %s", runID, last.Status)
	return last
}

// post sends a JSON request and checks the status code.
func post(t *testing.T, url, bearer string, body any, wantStatus int) runView {
	t.Helper()
	b, err := json.Marshal(body)
	require.NoError(t, err)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewReader(b))
	require.NoError(t, err)
	return do(t, req, bearer, wantStatus)
}

// get reads a run.
func get(t *testing.T, url, bearer string) runView {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	require.NoError(t, err)
	return do(t, req, bearer, http.StatusOK)
}

// do sends the request with the bearer token and decodes a run view.
func do(t *testing.T, req *http.Request, bearer string, wantStatus int) runView {
	t.Helper()
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	require.Equal(t, wantStatus, resp.StatusCode, "%s %s: %s", req.Method, req.URL, buf.String())
	var v runView
	if wantStatus < 300 {
		require.NoError(t, json.Unmarshal(buf.Bytes(), &v))
	}
	return v
}

// token signs a bearer token for the tenant, subject and scopes.
func token(t *testing.T, tenant, subject, scope string) string {
	t.Helper()
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": subject, "tenant_id": tenant, "scope": scope, "exp": time.Now().Add(time.Hour).Unix(),
	}).SignedString([]byte(signingKey))
	require.NoError(t, err)
	return signed
}
