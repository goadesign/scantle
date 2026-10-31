// Package sim simulates the orders platform's dependencies, the commerce
// system and the payment provider, inside the evaluator's process.
//
// The candidate only receives the simulator's URL. All state (order fixtures
// and the refund ledger) lives here, outside the candidate's memory and
// credentials, and is reset before every scenario or sample so no experiment
// inherits another's state.
package sim

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	gencommerce "github.com/goadesign/scantle/examples/orders-eval/gen/commerce"
	gencommerceserver "github.com/goadesign/scantle/examples/orders-eval/gen/http/commerce/server"
	genpaymentsserver "github.com/goadesign/scantle/examples/orders-eval/gen/http/payments/server"
	genpayments "github.com/goadesign/scantle/examples/orders-eval/gen/payments"
	goahttp "goa.design/goa/v3/http"
)

type (
	// Sim implements the generated commerce and payments services.
	Sim struct {
		// mu guards all fields below.
		mu sync.Mutex
		// orders maps tenant/customer to that customer's orders, newest first.
		orders map[string][]*gencommerce.Order
		// owners maps tenant/order to the customer who owns it.
		owners map[string]string
		// totals maps tenant/order to the order total in cents.
		totals map[string]int64
		// receipts maps an idempotency key to the refund it was first used
		// for and the receipt that refund produced.
		receipts map[string]keyedRefund
		// refunded maps tenant/order to the receipt of its refund.
		refunded map[string]*genpayments.Receipt
		// ledger lists distinct refunds in the order they were issued.
		ledger []*genpayments.Receipt
		// calls counts every refund request, duplicates included.
		calls int
		// srv serves the simulator's HTTP APIs once Serve ran.
		srv *http.Server
		// shutdown runs the server's graceful shutdown exactly once.
		shutdown sync.Once
		// stopping is closed when shutdown begins; it releases a withheld
		// refund response.
		stopping chan struct{}
		// stopped is closed once every accepted request finished.
		stopped chan struct{}
		// hold, when set, receives the order ID of the next new refund after
		// it is recorded; the simulator then withholds that response until
		// the caller disconnects or the simulator stops. It is disarmed after one use.
		hold chan string
	}

	// keyedRefund is the refund command an idempotency key was first used
	// with and its receipt.
	keyedRefund struct {
		// command is the original refund request with the key cleared.
		command genpayments.RefundPayload
		// receipt is the receipt returned for it.
		receipt *genpayments.Receipt
	}

	// Fixture is one customer's orders loaded before a scenario.
	Fixture struct {
		// TenantID is the merchant tenant.
		TenantID string
		// CustomerID owns the orders.
		CustomerID string
		// Orders are the customer's orders.
		Orders []*gencommerce.Order
	}
)

// New returns an empty simulator.
func New() *Sim {
	s := &Sim{}
	s.Reset()
	return s
}

// Reset replaces all state with the fixtures and an empty refund ledger. One
// Sim serves one scenario at a time; concurrent scenarios each use their own
// Sim and URL.
func (s *Sim) Reset(fixtures ...Fixture) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.orders = map[string][]*gencommerce.Order{}
	s.owners = map[string]string{}
	s.totals = map[string]int64{}
	s.receipts = map[string]keyedRefund{}
	s.refunded = map[string]*genpayments.Receipt{}
	s.ledger = nil
	s.calls = 0
	s.hold = nil
	for _, f := range fixtures {
		orders := append([]*gencommerce.Order(nil), f.Orders...)
		sort.SliceStable(orders, func(i, j int) bool { return orders[i].PlacedAt > orders[j].PlacedAt })
		s.orders[key(f.TenantID, f.CustomerID)] = orders
		for _, o := range orders {
			s.owners[key(f.TenantID, o.ID)] = f.CustomerID
			s.totals[key(f.TenantID, o.ID)] = o.TotalCents
		}
	}
}

// Refunds returns the distinct refunds issued since the last Reset, in
// issuance order, and the number of refund requests received.
func (s *Sim) Refunds() ([]genpayments.Receipt, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]genpayments.Receipt, len(s.ledger))
	for i, r := range s.ledger {
		out[i] = *r
	}
	return out, s.calls
}

// HoldNextRefund arms a one-shot fault: the next new refund is recorded (the
// money moves) and its order ID is sent on the returned channel, but the
// response is withheld until the caller disconnects or the simulator stops.
// Crash tests use it to kill the worker at the exact point after the provider
// accepted the refund and before the worker learned of it.
func (s *Sim) HoldNextRefund() <-chan string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hold = make(chan string, 1)
	return s.hold
}

// Serve serves the commerce and payments APIs on a loopback port until ctx
// ends and returns the base URL the candidate should use for both.
func (s *Sim) Serve(ctx context.Context) (string, error) {
	mux := goahttp.NewMuxer()
	gencommerceserver.Mount(mux, gencommerceserver.New(gencommerce.NewEndpoints(s), mux,
		goahttp.RequestDecoder, goahttp.ResponseEncoder, nil, nil))
	genpaymentsserver.Mount(mux, genpaymentsserver.New(genpayments.NewEndpoints(s), mux,
		goahttp.RequestDecoder, goahttp.ResponseEncoder, nil, nil))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("listen: %w", err)
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	s.mu.Lock()
	s.srv = srv
	s.stopping = make(chan struct{})
	s.stopped = make(chan struct{})
	s.mu.Unlock()
	go func() { _ = srv.Serve(ln) }()
	go func() {
		<-ctx.Done()
		s.beginShutdown()
	}()
	return "http://" + ln.Addr().String(), nil
}

// Stop stops accepting requests and waits until every request already
// accepted has finished, so a following Refunds call sees every refund the
// simulator will ever issue for this scenario. Stop and the end of Serve's
// context share one graceful shutdown; neither closes connections under an
// accepted request.
func (s *Sim) Stop(ctx context.Context) error {
	s.beginShutdown()
	select {
	case <-s.stopped:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("stop simulator: accepted requests still running: %w", ctx.Err())
	}
}

// beginShutdown starts the one graceful shutdown, which closes stopped once
// every accepted request returned.
func (s *Sim) beginShutdown() {
	s.shutdown.Do(func() {
		close(s.stopping)
		go func() {
			_ = s.srv.Shutdown(context.Background())
			close(s.stopped)
		}()
	})
}

// ListOrders returns one page of the customer's orders. The cursor is the
// offset of the page's first order.
func (s *Sim) ListOrders(_ context.Context, p *gencommerce.ListOrdersPayload) (*gencommerce.OrderPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all := s.orders[key(p.TenantID, p.CustomerID)]
	start := 0
	if p.Cursor != nil {
		n, err := strconv.Atoi(*p.Cursor)
		if err != nil || n < 0 || n > len(all) {
			return nil, fmt.Errorf("invalid cursor %q", *p.Cursor)
		}
		start = n
	}
	end := min(start+p.Limit, len(all))
	page := &gencommerce.OrderPage{Orders: append([]*gencommerce.Order(nil), all[start:end]...), Total: len(all)}
	if end < len(all) {
		next := strconv.Itoa(end)
		page.NextCursor = &next
	}
	return page, nil
}

// GetOrder returns the order when the customer owns it and not_found
// otherwise, including for other customers' orders.
func (s *Sim) GetOrder(_ context.Context, p *gencommerce.GetOrderPayload) (*gencommerce.Order, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.owners[key(p.TenantID, p.OrderID)] != p.CustomerID {
		return nil, gencommerce.MakeNotFound(fmt.Errorf("customer %s has no order %s", p.CustomerID, p.OrderID))
	}
	for _, o := range s.orders[key(p.TenantID, p.CustomerID)] {
		if o.ID == p.OrderID {
			return o, nil
		}
	}
	return nil, gencommerce.MakeNotFound(fmt.Errorf("order %s not found", p.OrderID))
}

// Refund issues a refund once per idempotency key, as the real provider does:
// a repeated key for the same refund returns the original receipt, and a key
// reused for a different refund is refused. A second refund of the same
// order under a different key, or a refund larger than the order total, is
// refused.
func (s *Sim) Refund(ctx context.Context, p *genpayments.RefundPayload) (*genpayments.Receipt, error) {
	r, hold, err := s.refund(p)
	if err != nil || hold == nil {
		return r, err
	}
	hold <- p.OrderID
	// net/http reports a disconnect only once the request body was read to
	// its end, which the decoder does not guarantee, so the simulator's own
	// stop also releases the response. Scenarios stop the simulator only
	// after every candidate process has exited.
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("caller disconnected while the refund response was withheld: %w", ctx.Err())
	case <-s.stopping:
		return nil, errors.New("simulator stopped while the refund response was withheld")
	}
}

// refund applies one refund request and returns the receipt, plus the armed
// hold channel when this new refund must have its response withheld.
func (s *Sim) refund(p *genpayments.RefundPayload) (*genpayments.Receipt, chan string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	command := *p
	command.IdempotencyKey = ""
	if prior, ok := s.receipts[p.IdempotencyKey]; ok {
		if prior.command != command {
			return nil, nil, fmt.Errorf("idempotency key %q was already used for a different refund", p.IdempotencyKey)
		}
		return prior.receipt, nil, nil
	}
	orderKey := key(p.TenantID, p.OrderID)
	if _, ok := s.owners[orderKey]; !ok {
		return nil, nil, fmt.Errorf("tenant %s has no order %s", p.TenantID, p.OrderID)
	}
	if prior, ok := s.refunded[orderKey]; ok {
		return nil, nil, fmt.Errorf("order %s was already refunded by receipt %s", p.OrderID, prior.ReceiptID)
	}
	if total := s.totals[orderKey]; p.AmountCents > total {
		return nil, nil, fmt.Errorf("refund of %d cents exceeds order total of %d cents", p.AmountCents, s.totals[orderKey])
	}
	r := &genpayments.Receipt{
		ReceiptID:   fmt.Sprintf("rcpt-%d", len(s.ledger)+1),
		OrderID:     p.OrderID,
		AmountCents: p.AmountCents,
		Method:      p.Method,
	}
	s.receipts[p.IdempotencyKey] = keyedRefund{command: command, receipt: r}
	s.refunded[orderKey] = r
	s.ledger = append(s.ledger, r)
	hold := s.hold
	s.hold = nil
	return r, hold, nil
}

// key joins identifiers into a map key.
func key(a, b string) string {
	return a + "/" + b
}
