// Package harness starts the candidate orders platform for one evaluation
// scenario and drives it through its public API.
//
// The evaluator never imports candidate code. It launches the candidate's built
// binaries as separate processes, points their dependency URLs at a simulator
// owned by the scenario, and talks to them only with clients generated from the
// evaluator's own copy of the approved contracts. Bearer tokens come from a
// test identity issuer whose key is generated per environment and given to the
// candidate's sim configuration; no real identity provider key is involved.
package harness

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	genordersclient "github.com/goadesign/scantle/examples/orders-eval/gen/http/orders/client"
	genorders "github.com/goadesign/scantle/examples/orders-eval/gen/orders"
	"github.com/golang-jwt/jwt/v5"
	goahttp "goa.design/goa/v3/http"
)

type (
	// Env is the shared environment of one evaluation run: the candidate's
	// binaries and the test identity issuer's key. Every platform it starts
	// gets its own Temporal dev server, because the candidate's task queues
	// are fixed by its design and would otherwise be shared between
	// scenarios.
	Env struct {
		// OrdersBin is the candidate's orders API binary.
		OrdersBin string
		// WorkerBin is the candidate's refund worker binary.
		WorkerBin string
		// signingKey signs test bearer tokens and is given to the candidate.
		signingKey []byte
	}

	// Platform is one running instance of the candidate.
	Platform struct {
		// Client calls the candidate's public orders API.
		Client *genorders.Client
		// env is the environment the platform runs in.
		env *Env
		// workerArgs restarts the refund worker with the same configuration.
		workerArgs []string
		// worker is the current refund worker process.
		worker *process
		// api is the orders API process.
		api *process
	}

	// process is a started candidate process and its captured output.
	process struct {
		// cmd is the running command.
		cmd *exec.Cmd
		// out collects its stdout and stderr for diagnostics.
		out *bytes.Buffer
		// done is closed once the process exited.
		done chan struct{}
	}
)

// NewEnv returns an environment for the candidate binaries with a fresh test
// identity issuer key.
func NewEnv(ordersBin, workerBin string) (*Env, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate test issuer key: %w", err)
	}
	return &Env{OrdersBin: ordersBin, WorkerBin: workerBin, signingKey: key}, nil
}

// Token signs a test bearer token for the tenant, subject and space-separated
// scopes.
func (e *Env) Token(tenant, subject, scope string) (string, error) {
	return jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": subject, "tenant_id": tenant, "scope": scope, "exp": time.Now().Add(time.Hour).Unix(),
	}).SignedString(e.signingKey)
}

// Start launches a Temporal dev server, a candidate orders API and a refund
// worker whose dependencies point at depsURL, and waits until the API accepts
// connections. Everything stops when ctx ends.
func (e *Env) Start(ctx context.Context, depsURL string) (*Platform, error) {
	temporalAddr, err := startTemporal(ctx)
	if err != nil {
		return nil, err
	}
	p := &Platform{env: e, workerArgs: []string{
		"--env", "sim", "--payments-url", depsURL, "--temporal", temporalAddr,
	}}
	if p.worker, err = launch(ctx, e.WorkerBin, nil, p.workerArgs...); err != nil {
		return nil, err
	}
	addr, err := freeAddr()
	if err != nil {
		return nil, err
	}
	p.api, err = launch(ctx, e.OrdersBin, []string{"ORDERS_JWT_KEY=" + string(e.signingKey)},
		"--env", "sim", "--commerce-url", depsURL, "--temporal", temporalAddr, "--listen", addr,
		"--model-provider", "scripted")
	if err != nil {
		return nil, err
	}
	if err := WaitListening(ctx, addr); err != nil {
		return nil, fmt.Errorf("orders API never listened: %w\n%s", err, p.api.out.String())
	}
	c := genordersclient.NewClient("http", addr, http.DefaultClient, goahttp.RequestEncoder, goahttp.ResponseDecoder, false)
	p.Client = genorders.NewClient(c.Chat(), c.RunStatus(), c.ApproveRefund())
	return p, nil
}

// KillWorker sends SIGKILL to the refund worker and waits for it to exit.
func (p *Platform) KillWorker() error {
	if err := p.worker.cmd.Process.Signal(syscall.SIGKILL); err != nil {
		return fmt.Errorf("kill refund worker: %w", err)
	}
	<-p.worker.done
	return nil
}

// RestartWorker starts a replacement refund worker with the same configuration.
func (p *Platform) RestartWorker(ctx context.Context) error {
	w, err := launch(ctx, p.env.WorkerBin, nil, p.workerArgs...)
	if err != nil {
		return err
	}
	p.worker = w
	return nil
}

// Logs returns the candidate processes' output for diagnostics.
func (p *Platform) Logs() string {
	return "orders:\n" + p.api.out.String() + "\nworker:\n" + p.worker.out.String()
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

// startTemporal starts an in-memory Temporal dev server and returns its
// frontend address. It stops when ctx ends.
func startTemporal(ctx context.Context) (string, error) {
	addr, err := freeAddr()
	if err != nil {
		return "", err
	}
	uiAddr, err := freeAddr()
	if err != nil {
		return "", err
	}
	_, port, _ := net.SplitHostPort(addr)
	_, uiPort, _ := net.SplitHostPort(uiAddr)
	proc, err := launch(ctx, "temporal", nil, "server", "start-dev", "--headless",
		"--ip", "127.0.0.1", "--port", port, "--ui-port", uiPort, "--log-level", "error")
	if err != nil {
		return "", err
	}
	waitCtx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if err := WaitListening(waitCtx, addr); err != nil {
		return "", fmt.Errorf("temporal never listened: %w\n%s", err, proc.out.String())
	}
	return addr, nil
}

// launch starts a process that is killed when ctx ends.
func launch(ctx context.Context, name string, env []string, args ...string) (*process, error) {
	out := &bytes.Buffer{}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout, cmd.Stderr = &lockedWriter{w: out}, &lockedWriter{w: out}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", name, err)
	}
	p := &process{cmd: cmd, out: out, done: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		close(p.done)
	}()
	return p, nil
}

// freeAddr returns a loopback address with a currently free port.
func freeAddr() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("find free port: %w", err)
	}
	addr := ln.Addr().String()
	return addr, ln.Close()
}

// lockedWriter serializes writes from a process's stdout and stderr.
type lockedWriter struct {
	// mu guards w.
	mu sync.Mutex
	// w receives the output.
	w *bytes.Buffer
}

// Write appends p under the lock.
func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
