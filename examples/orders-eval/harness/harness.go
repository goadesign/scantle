// Package harness starts the candidate orders platform for one evaluation
// scenario and drives it through its public API.
//
// The evaluator never imports or directly executes candidate code. It starts
// the candidate's digest-pinned binaries only through Scantle's
// verify/candidate.Exec, which runs each one as a single sandboxed process: an
// explicit environment, a private state directory, loopback access to the
// assigned ports only, and no child processes. The harness points the
// candidate's dependency URLs at a simulator owned by the scenario and talks to
// it only with clients generated from the evaluator's own copy of the approved
// contracts. Bearer tokens come from a test identity issuer whose key is
// generated per environment; no real identity provider key is involved.
//
// The Temporal dev server each platform uses is trusted test infrastructure and
// runs outside the sandbox.
package harness

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	genordersclient "github.com/goadesign/scantle/examples/orders-eval/gen/http/orders/client"
	genorders "github.com/goadesign/scantle/examples/orders-eval/gen/orders"
	"github.com/goadesign/scantle/verify/candidate"
	"github.com/golang-jwt/jwt/v5"
	goahttp "goa.design/goa/v3/http"
)

type (
	// Env is the shared environment of one evaluation run: the candidate's
	// pinned binaries and the test identity issuer's key. Every platform it
	// starts gets its own Temporal dev server, because the candidate's task
	// queues are fixed by its design and would otherwise be shared between
	// scenarios.
	Env struct {
		// Orders is the candidate's orders API binary.
		Orders candidate.Artifact
		// Worker is the candidate's refund worker binary.
		Worker candidate.Artifact
		// signingKey signs test bearer tokens and is given to the candidate as
		// ORDERS_JWT_KEY. It is hex text so it is always a valid environment
		// value; both sides use its bytes as the HS256 key.
		signingKey string
	}

	// Platform is one running instance of the candidate.
	Platform struct {
		// logDir holds the candidate processes' output files; it is owned by
		// the evaluator and lies outside every candidate state directory.
		logDir string
		// Client calls the candidate's public orders API.
		Client *genorders.Client
		// env is the environment the platform runs in.
		env *Env
		// workerSpec restarts the refund worker with the same configuration.
		workerSpec candidate.Spec
		// worker is the current refund worker process.
		worker *candidate.Process
		// api is the orders API process.
		api *candidate.Process
	}

	// output is the single synchronized owner of a trusted helper process's
	// combined output.
	output struct {
		// mu guards buf.
		mu sync.Mutex
		// buf accumulates output.
		buf bytes.Buffer
	}
)

// maxExcerptBytes bounds how much of one process's output a diagnostic shows.
const maxExcerptBytes = 16 << 10

// NewEnv returns an environment for the pinned candidate binaries with a fresh
// test identity issuer key.
func NewEnv(orders, worker candidate.Artifact) (*Env, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate test issuer key: %w", err)
	}
	return &Env{Orders: orders, Worker: worker, signingKey: hex.EncodeToString(key)}, nil
}

// Token signs a test bearer token for the tenant, subject and space-separated
// scopes.
func (e *Env) Token(tenant, subject, scope string) (string, error) {
	return jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": subject, "tenant_id": tenant, "scope": scope, "exp": time.Now().Add(time.Hour).Unix(),
	}).SignedString([]byte(e.signingKey))
}

// Start launches a Temporal dev server and, in the sandbox, a candidate orders
// API and refund worker whose dependencies point at depsURL; it waits until the
// API accepts connections. Everything stops when ctx ends.
func (e *Env) Start(ctx context.Context, depsURL string) (*Platform, error) {
	depsPort, err := urlPort(depsURL)
	if err != nil {
		return nil, err
	}
	temporalAddr, temporalPort, err := startTemporal(ctx)
	if err != nil {
		return nil, err
	}
	workerState, err := stateDir()
	if err != nil {
		return nil, err
	}
	logDir, err := os.MkdirTemp("", "orders-eval-logs-*")
	if err != nil {
		return nil, fmt.Errorf("create log directory: %w", err)
	}
	p := &Platform{env: e, logDir: logDir, workerSpec: candidate.Spec{
		Args:         []string{"--env", "sim", "--payments-url", depsURL, "--temporal", temporalAddr},
		StateDir:     workerState,
		ConnectPorts: []int{temporalPort, depsPort},
		LogFile:      filepath.Join(logDir, "worker.log"),
	}}
	if p.worker, err = candidate.Exec(ctx, e.Worker, p.workerSpec); err != nil {
		return nil, fmt.Errorf("start refund worker: %w", err)
	}
	apiPort, err := freePort()
	if err != nil {
		return nil, err
	}
	apiState, err := stateDir()
	if err != nil {
		return nil, err
	}
	addr := "127.0.0.1:" + strconv.Itoa(apiPort)
	p.api, err = candidate.Exec(ctx, e.Orders, candidate.Spec{
		Args: []string{"--env", "sim", "--commerce-url", depsURL, "--temporal", temporalAddr,
			"--listen", addr, "--model-provider", "scripted", "--state-dir", apiState},
		Env:          []string{"ORDERS_JWT_KEY=" + e.signingKey},
		StateDir:     apiState,
		ConnectPorts: []int{temporalPort, depsPort},
		ListenPort:   apiPort,
		LogFile:      filepath.Join(logDir, "orders.log"),
	})
	if err != nil {
		return nil, fmt.Errorf("start orders API: %w", err)
	}
	if err := WaitListening(ctx, addr); err != nil {
		return nil, fmt.Errorf("orders API never listened: %w\n%s", err, excerpt("orders", p.api))
	}
	c := genordersclient.NewClient("http", addr, http.DefaultClient, goahttp.RequestEncoder, goahttp.ResponseDecoder, false)
	p.Client = genorders.NewClient(c.Chat(), c.RunStatus(), c.ApproveRefund())
	return p, nil
}

// KillWorker sends SIGKILL to the refund worker and waits for it to exit.
func (p *Platform) KillWorker() error {
	return p.worker.Kill()
}

// RestartWorker starts a replacement refund worker with the same configuration,
// logging to a new file so the crashed worker's output is kept.
func (p *Platform) RestartWorker(ctx context.Context) error {
	spec := p.workerSpec
	spec.LogFile = filepath.Join(p.logDir, "worker-restarted.log")
	w, err := candidate.Exec(ctx, p.env.Worker, spec)
	if err != nil {
		return fmt.Errorf("restart refund worker: %w", err)
	}
	p.worker = w
	return nil
}

// Logs returns bounded excerpts of the candidate processes' output for
// diagnostics; full output stays in the evaluator-owned log directory.
func (p *Platform) Logs() string {
	return excerpt("orders", p.api) + "\n" + excerpt("worker", p.worker) + "\nfull logs: " + p.logDir
}

// WaitStatus polls a run until it reaches want or a terminal status, and
// returns its last state. It reports an error only when the API cannot be
// read or ctx ends first.
func (p *Platform) WaitStatus(ctx context.Context, token, runID, want string) (*genorders.RunView, error) {
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		v, err := p.Client.RunStatus(ctx, &genorders.RunStatusPayload{Token: token, RunID: runID})
		if err != nil {
			return nil, fmt.Errorf("read run %s: %w", runID, err)
		}
		if v.Status == want || v.Status == "completed" || v.Status == "failed" {
			return v, nil
		}
		select {
		case <-ctx.Done():
			return v, fmt.Errorf("run %s still %s: %w", runID, v.Status, ctx.Err())
		case <-tick.C:
		}
	}
}

// WaitListening waits until addr accepts TCP connections or ctx ends.
func WaitListening(ctx context.Context, addr string) error {
	for {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			return c.Close()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// excerpt renders the end of one process's output, noting truncation.
func excerpt(name string, p *candidate.Process) string {
	text, truncated, err := p.Excerpt(maxExcerptBytes)
	if err != nil {
		return name + ": output unavailable: " + err.Error()
	}
	if truncated {
		return name + " (last " + strconv.Itoa(maxExcerptBytes) + " bytes):\n" + text
	}
	return name + ":\n" + text
}

// startTemporal starts an in-memory Temporal dev server and returns its
// frontend address and port. It stops when ctx ends.
func startTemporal(ctx context.Context) (string, int, error) {
	port, err := freePort()
	if err != nil {
		return "", 0, err
	}
	uiPort, err := freePort()
	if err != nil {
		return "", 0, err
	}
	out := &output{}
	cmd := exec.CommandContext(ctx, "temporal", "server", "start-dev", "--headless",
		"--ip", "127.0.0.1", "--port", strconv.Itoa(port), "--ui-port", strconv.Itoa(uiPort), "--log-level", "error")
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		return "", 0, fmt.Errorf("start temporal: %w", err)
	}
	go func() { _ = cmd.Wait() }()
	addr := "127.0.0.1:" + strconv.Itoa(port)
	waitCtx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if err := WaitListening(waitCtx, addr); err != nil {
		return "", 0, fmt.Errorf("temporal never listened: %w\n%s", err, out.String())
	}
	return addr, port, nil
}

// stateDir creates a private state directory for one candidate process.
func stateDir() (string, error) {
	dir, err := os.MkdirTemp("", "orders-eval-state-*")
	if err != nil {
		return "", fmt.Errorf("create state directory: %w", err)
	}
	return dir, nil
}

// urlPort returns the TCP port of an http URL.
func urlPort(raw string) (int, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", raw, err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		return 0, fmt.Errorf("%s has no numeric port: %w", raw, err)
	}
	return port, nil
}

// freePort returns a currently free loopback port.
func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("find free port: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	return port, ln.Close()
}

// Write appends p under the lock.
func (o *output) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buf.Write(p)
}

// String returns a snapshot under the lock.
func (o *output) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buf.String()
}
