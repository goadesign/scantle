package harness

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net"
	"sync"
	"time"

	"github.com/goadesign/surla/examples/orders-eval/scriptedmodel"
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
		// Unreported counts calls whose usage is not known to be complete, so
		// the totals are only a lower bound.
		Unreported int
		// ByModel sums every observed token count per provider-reported
		// model, which is what prices apply to, including the partial counts
		// of unreported calls. Usage without a model is keyed by the empty
		// string.
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

// record adds one finished call. Every token count the call observed is
// added once, whether it comes from a response or from a failed call's error.
// Separately, a call counts as unreported unless it succeeded and its provider
// explicitly reported complete usage: a call without usage, with partial
// usage, or that failed (an error's usage does not say whether it is complete)
// leaves the totals unproven, so they are a lower bound.
func (m *meter) record(resp *model.Response, err error) {
	var observed *model.TokenUsage
	complete := false
	switch {
	case err != nil:
		observed = model.UsageFromError(err)
	case resp.HasObservedUsage():
		observed = &resp.Usage
		complete = resp.HasCompleteUsage()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.usage.Calls++
	if !complete {
		m.usage.Unreported++
	}
	if observed == nil {
		return
	}
	if m.usage.ByModel == nil {
		m.usage.ByModel = map[string]Tokens{}
	}
	t := m.usage.ByModel[observed.Model]
	t.Input += observed.InputTokens
	t.Output += observed.OutputTokens
	t.CacheRead += observed.CacheReadTokens
	t.CacheWrite += observed.CacheWriteTokens
	m.usage.ByModel[observed.Model] = t
}

// total returns a copy of the usage recorded so far.
func (m *meter) total() Usage {
	m.mu.Lock()
	defer m.mu.Unlock()
	u := m.usage
	u.ByModel = maps.Clone(m.usage.ByModel)
	return u
}
