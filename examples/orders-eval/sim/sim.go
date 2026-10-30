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
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	gencommerce "github.com/goadesign/scantle/examples/orders/gen/commerce"
	gencommerceserver "github.com/goadesign/scantle/examples/orders/gen/http/commerce/server"
	genpaymentsserver "github.com/goadesign/scantle/examples/orders/gen/http/payments/server"
	genpayments "github.com/goadesign/scantle/examples/orders/gen/payments"
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
		// receipts maps an idempotency key to the receipt it produced.
		receipts map[string]*genpayments.Receipt
		// refunded maps tenant/order to the receipt of its refund.
		refunded map[string]*genpayments.Receipt
		// ledger lists distinct refunds in the order they were issued.
		ledger []*genpayments.Receipt
		// calls counts every refund request, duplicates included.
		calls int
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

// Reset replaces all state with the fixtures and an empty refund ledger.
func (s *Sim) Reset(fixtures ...Fixture) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.orders = map[string][]*gencommerce.Order{}
	s.owners = map[string]string{}
	s.totals = map[string]int64{}
	s.receipts = map[string]*genpayments.Receipt{}
	s.refunded = map[string]*genpayments.Receipt{}
	s.ledger = nil
	s.calls = 0
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
	go func() { _ = srv.Serve(ln) }()
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	return "http://" + ln.Addr().String(), nil
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
// a repeated key returns the original receipt. A second refund of the same
// order under a different key, or a refund larger than the order total, is
// refused.
func (s *Sim) Refund(_ context.Context, p *genpayments.RefundPayload) (*genpayments.Receipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if r, ok := s.receipts[p.IdempotencyKey]; ok {
		return r, nil
	}
	orderKey := key(p.TenantID, p.OrderID)
	if _, ok := s.owners[orderKey]; !ok {
		return nil, fmt.Errorf("tenant %s has no order %s", p.TenantID, p.OrderID)
	}
	if prior, ok := s.refunded[orderKey]; ok {
		return nil, fmt.Errorf("order %s was already refunded by receipt %s", p.OrderID, prior.ReceiptID)
	}
	if total := s.totals[orderKey]; p.AmountCents > total {
		return nil, fmt.Errorf("refund of %d cents exceeds order total of %d cents", p.AmountCents, s.totals[orderKey])
	}
	r := &genpayments.Receipt{
		ReceiptID:   fmt.Sprintf("rcpt-%d", len(s.ledger)+1),
		OrderID:     p.OrderID,
		AmountCents: p.AmountCents,
		Method:      p.Method,
	}
	s.receipts[p.IdempotencyKey] = r
	s.refunded[orderKey] = r
	s.ledger = append(s.ledger, r)
	return r, nil
}

// key joins identifiers into a map key.
func key(a, b string) string {
	return a + "/" + b
}
