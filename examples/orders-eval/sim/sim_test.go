package sim

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	gencommerce "github.com/goadesign/scantle/examples/orders-eval/gen/commerce"
	gencommerceclient "github.com/goadesign/scantle/examples/orders-eval/gen/http/commerce/client"
	genpaymentsclient "github.com/goadesign/scantle/examples/orders-eval/gen/http/payments/client"
	genpayments "github.com/goadesign/scantle/examples/orders-eval/gen/payments"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	goahttp "goa.design/goa/v3/http"
	goa "goa.design/goa/v3/pkg"
)

// The tests drive the simulator through the generated HTTP clients, the same
// way the candidate does.
func TestSimulatorThroughGeneratedClients(t *testing.T) {
	ctx := t.Context()
	s := New()
	base, err := s.Serve(ctx)
	require.NoError(t, err)
	commerce, payments := clients(t, base)

	s.Reset(Fixture{TenantID: "t1", CustomerID: "c1", Orders: []*gencommerce.Order{
		{ID: "o-1", PlacedAt: "2026-10-01T10:00:00Z", ItemsCents: 3500, TaxCents: 350, TotalCents: 3850},
		{ID: "o-2", PlacedAt: "2026-10-02T10:00:00Z", ItemsCents: 4800, ShippingCents: 600, TotalCents: 5400},
		{ID: "o-3", PlacedAt: "2026-10-03T10:00:00Z", ItemsCents: 7500, TaxCents: 700, TotalCents: 8200},
	}})

	t.Run("pages newest first", func(t *testing.T) {
		limit := 2
		page, err := commerce.ListOrders(ctx, &gencommerce.ListOrdersPayload{TenantID: "t1", CustomerID: "c1", Limit: limit})
		require.NoError(t, err)
		assert.Equal(t, []string{"o-3", "o-2"}, ids(page.Orders))
		assert.Equal(t, 3, page.Total)
		require.NotNil(t, page.NextCursor)
		next, err := commerce.ListOrders(ctx, &gencommerce.ListOrdersPayload{TenantID: "t1", CustomerID: "c1", Cursor: page.NextCursor, Limit: limit})
		require.NoError(t, err)
		assert.Equal(t, []string{"o-1"}, ids(next.Orders))
		assert.Nil(t, next.NextCursor)
	})

	t.Run("hides other customers' orders", func(t *testing.T) {
		_, err := commerce.GetOrder(ctx, &gencommerce.GetOrderPayload{TenantID: "t1", CustomerID: "c2", OrderID: "o-2"})
		var se *goa.ServiceError
		require.ErrorAs(t, err, &se)
		assert.Equal(t, "not_found", se.Name)
	})

	t.Run("repeated idempotency key returns the original receipt", func(t *testing.T) {
		req := &genpayments.RefundPayload{TenantID: "t1", OrderID: "o-2", AmountCents: 5400, Method: "card", IdempotencyKey: "refund/t1/o-2"}
		first, err := payments.Refund(ctx, req)
		require.NoError(t, err)
		again, err := payments.Refund(ctx, req)
		require.NoError(t, err)
		assert.Equal(t, first.ReceiptID, again.ReceiptID)
		ledger, calls := s.Refunds()
		assert.Len(t, ledger, 1, "exactly one refund despite two requests")
		assert.Equal(t, 2, calls)
	})

	t.Run("key reused for a different refund is refused", func(t *testing.T) {
		_, err := payments.Refund(ctx, &genpayments.RefundPayload{TenantID: "t1", OrderID: "o-1", AmountCents: 3850, Method: "store_credit", IdempotencyKey: "refund/t1/o-2"})
		require.Error(t, err)
		ledger, _ := s.Refunds()
		assert.Len(t, ledger, 1, "the reused key must not return o-2's receipt or refund o-1")
	})

	t.Run("second key for a refunded order is refused", func(t *testing.T) {
		_, err := payments.Refund(ctx, &genpayments.RefundPayload{TenantID: "t1", OrderID: "o-2", AmountCents: 5400, Method: "card", IdempotencyKey: "other"})
		require.Error(t, err)
		ledger, _ := s.Refunds()
		assert.Len(t, ledger, 1)
	})

	t.Run("reset clears the ledger", func(t *testing.T) {
		s.Reset()
		ledger, calls := s.Refunds()
		assert.Empty(t, ledger)
		assert.Zero(t, calls)
	})
}

// clients returns generated commerce and payments clients for base.
func clients(t *testing.T, base string) (*gencommerce.Client, *genpayments.Client) {
	t.Helper()
	u, err := url.Parse(base)
	require.NoError(t, err)
	c := gencommerceclient.NewClient(u.Scheme, u.Host, http.DefaultClient, goahttp.RequestEncoder, goahttp.ResponseDecoder, false)
	p := genpaymentsclient.NewClient(u.Scheme, u.Host, http.DefaultClient, goahttp.RequestEncoder, goahttp.ResponseDecoder, false)
	return gencommerce.NewClient(c.ListOrders(), c.GetOrder()), genpayments.NewClient(p.Refund())
}

// ids returns the order IDs in order.
func ids(orders []*gencommerce.Order) []string {
	out := make([]string, len(orders))
	for i, o := range orders {
		out[i] = o.ID
	}
	return out
}

func TestStopReleasesAWithheldRefund(t *testing.T) {
	s := New()
	_, err := s.Serve(t.Context())
	require.NoError(t, err)
	s.Reset(Fixture{TenantID: "t1", CustomerID: "c1", Orders: []*gencommerce.Order{
		{ID: "o-1", PlacedAt: "2026-10-01T10:00:00Z", ItemsCents: 3500, TaxCents: 350, TotalCents: 3850},
	}})
	held := s.HoldNextRefund()
	// The caller never disconnects, as when net/http has not noticed a dead
	// client; only Stop can release the response.
	refunded := make(chan error, 1)
	go func() {
		_, err := s.Refund(context.Background(), &genpayments.RefundPayload{
			TenantID: "t1", OrderID: "o-1", AmountCents: 3850, Method: "store_credit", IdempotencyKey: "refund/t1/o-1",
		})
		refunded <- err
	}()
	assert.Equal(t, "o-1", <-held)

	stopCtx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	require.NoError(t, s.Stop(stopCtx))
	select {
	case err := <-refunded:
		assert.Error(t, err, "the withheld response is an error, never a receipt")
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not release the withheld refund")
	}
	ledger, _ := s.Refunds()
	assert.Len(t, ledger, 1, "the refund was still issued")
}
