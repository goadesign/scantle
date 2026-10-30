package harness

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net"
	"sync"
	"time"

	"github.com/goadesign/scantle/examples/orders-eval/scriptedmodel"
	"goa.design/goa-ai/features/model/gateway"
	"goa.design/goa-ai/runtime/agent/model"
	"google.golang.org/grpc"
)

type (
	// Usage is the token usage of every model call one platform made, as
	// measured at the evaluator's model gateway.
	Usage struct {
		// Calls counts completed model calls, successful or not.
		Calls int
		// Unreported counts calls whose usage the provider did not report,
		// so the totals cannot be proven complete.
		Unreported int
		// ByModel sums the reported usage per provider-reported model, which
		// is what prices apply to. Usage reported without a model is keyed by
		// the empty string.
		ByModel map[string]Tokens
	}

	// Tokens counts tokens by kind.
	Tokens struct {
		// Input is tokens the model read, excluding cache reads.
		Input int
		// Output is tokens the model produced.
		Output int
		// CacheRead is tokens read from the provider's prompt cache.
		CacheRead int
		// CacheWrite is tokens written to the provider's prompt cache.
		CacheWrite int
	}

	// modelGateway is the evaluator-owned goa-ai model gateway one platform's
	// candidate sends every model call to, and the meter that measures them.
	modelGateway struct {
		// addr is the gateway's loopback gRPC address.
		addr string
		// port is the gateway's TCP port, granted to the candidate.
		port int
		// server is the gRPC server hosting the gateway.
		server *grpc.Server
		// served is closed once the gRPC server stopped serving.
		served chan struct{}
		// meter measures every call.
		meter *meter
	}

	// meter records the usage of every call through the gateway.
	meter struct {
		// mu guards usage.
		mu sync.Mutex
		// usage accumulates the calls.
		usage Usage
	}
)

// gatewayStopTimeout bounds how long stopping the gateway waits for calls
// still in flight to finish.
const gatewayStopTimeout = 10 * time.Second

// startGateway serves the evaluator's scripted model through a goa-ai model
// gateway on a loopback port. Every call passes through the meter, which runs
// in the evaluator process outside the candidate's sandbox.
func startGateway() (*modelGateway, error) {
	m := &meter{}
	gw, err := gateway.NewServer(
		gateway.WithProvider(scriptedmodel.New()),
		gateway.WithUnary(m.unary),
		gateway.WithStream(m.stream),
	)
	if err != nil {
		return nil, fmt.Errorf("create model gateway: %w", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen for model gateway: %w", err)
	}
	g := &modelGateway{
		addr: ln.Addr().String(), port: ln.Addr().(*net.TCPAddr).Port,
		server: grpc.NewServer(), served: make(chan struct{}), meter: m,
	}
	gateway.RegisterGRPCServer(g.server, gw)
	go func() {
		defer close(g.served)
		_ = g.server.Serve(ln)
	}()
	return g, nil
}

// stop stops the gateway once every call in flight returned, so the meter
// holds the final usage. Calls still running after gatewayStopTimeout are an
// error: their usage was never recorded.
func (g *modelGateway) stop() error {
	stopped := make(chan struct{})
	go func() {
		g.server.GracefulStop()
		close(stopped)
	}()
	select {
	case <-stopped:
		<-g.served
		return nil
	case <-time.After(gatewayStopTimeout):
		g.server.Stop()
		<-g.served
		return errors.New("model gateway calls still running after the candidate stopped; their usage is unknown")
	}
}

// unary records the usage of each unary call.
func (m *meter) unary(next gateway.UnaryHandler) gateway.UnaryHandler {
	return func(ctx context.Context, req *model.Request) (*model.Response, error) {
		resp, err := next(ctx, req)
		m.record(resp, err)
		return resp, err
	}
}

// stream records the usage of each streamed call from its final response;
// the per-chunk usage it streamed is part of that total and is not added
// again.
func (m *meter) stream(next gateway.StreamHandler) gateway.StreamHandler {
	return func(ctx context.Context, req *model.Request, send func(model.Chunk) error) (*model.Response, error) {
		resp, err := next(ctx, req, send)
		m.record(resp, err)
		return resp, err
	}
}

// record adds one finished call. A successful call counts only usage the
// provider explicitly reported as complete; a failed call counts usage carried
// by its error. A call with no reported usage, or with partial usage, is
// counted as unreported, never as zero or as an undercount.
func (m *meter) record(resp *model.Response, err error) {
	var usage *model.TokenUsage
	switch {
	case err != nil:
		usage = model.UsageFromError(err)
	case resp.HasUsage() && !resp.UsageIncomplete:
		usage = &resp.Usage
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.usage.Calls++
	if usage == nil {
		m.usage.Unreported++
		return
	}
	if m.usage.ByModel == nil {
		m.usage.ByModel = map[string]Tokens{}
	}
	t := m.usage.ByModel[usage.Model]
	t.Input += usage.InputTokens
	t.Output += usage.OutputTokens
	t.CacheRead += usage.CacheReadTokens
	t.CacheWrite += usage.CacheWriteTokens
	m.usage.ByModel[usage.Model] = t
}

// total returns a copy of the usage recorded so far.
func (m *meter) total() Usage {
	m.mu.Lock()
	defer m.mu.Unlock()
	u := m.usage
	u.ByModel = maps.Clone(m.usage.ByModel)
	return u
}
