// Package orders implements the orders service: the refund assistant's public
// API and the methods that back the refund agent's tools.
package orders

import (
	"context"
	"errors"
	"fmt"

	gencommerce "github.com/goadesign/scantle/examples/orders/gen/commerce"
	genorders "github.com/goadesign/scantle/examples/orders/gen/orders"
	genpayments "github.com/goadesign/scantle/examples/orders/gen/payments"
	goa "goa.design/goa/v3/pkg"
)

type (
	// OrderSource reads a customer's orders from the commerce system. The
	// generated commerce client satisfies it.
	OrderSource interface {
		// ListOrders returns one page of the customer's orders.
		ListOrders(ctx context.Context, p *gencommerce.ListOrdersPayload) (*gencommerce.OrderPage, error)
		// GetOrder loads one order the customer owns, or fails with the
		// commerce not_found error.
		GetOrder(ctx context.Context, p *gencommerce.GetOrderPayload) (*gencommerce.Order, error)
	}

	// RefundRunner returns money for an order exactly once. It owns the
	// durable execution that survives worker crashes; repeated calls for the
	// same order return the original receipt.
	RefundRunner interface {
		// Refund refunds the order and returns the provider receipt.
		Refund(ctx context.Context, r Refund) (*genpayments.Receipt, error)
	}

	// Refund is one refund decision made by the orders service.
	Refund struct {
		// TenantID is the merchant tenant that owns the order.
		TenantID string
		// OrderID is the refunded order.
		OrderID string
		// AmountCents is the full order total, including tax and shipping.
		AmountCents int64
		// Method is how the money is returned: "store_credit" or "card".
		Method string
	}
)

const (
	// StoreCreditLimitCents is the largest order total, including tax and
	// shipping, refunded as instant store credit without a manager. Larger
	// totals are refunded to the card after a manager approves.
	StoreCreditLimitCents int64 = 5000

	// methodStoreCredit returns the money as instant store credit.
	methodStoreCredit = "store_credit"
	// methodCard returns the money to the customer's card.
	methodCard = "card"
)

// RefundMethod returns how an order with the given total, including tax and
// shipping, is refunded: store credit up to and including the limit, card
// above it.
func RefundMethod(totalCents int64) string {
	if totalCents <= StoreCreditLimitCents {
		return methodStoreCredit
	}
	return methodCard
}

// ListOrders backs the list_orders and continue_orders tools: it returns one
// page of the customer's orders with the bounds the runtime shows the model.
func (s *Service) ListOrders(ctx context.Context, p *genorders.ListOrdersPayload) (*genorders.ListOrdersResult, error) {
	page, err := s.orders.ListOrders(ctx, &gencommerce.ListOrdersPayload{
		TenantID:   p.TenantID,
		CustomerID: p.CustomerID,
		Cursor:     p.Cursor,
		Limit:      ordersPageSize,
	})
	if err != nil {
		return nil, fmt.Errorf("list orders: %w", err)
	}
	res := &genorders.ListOrdersResult{
		Orders:     make([]*genorders.Order, len(page.Orders)),
		Returned:   len(page.Orders),
		Total:      page.Total,
		Truncated:  page.NextCursor != nil,
		NextCursor: page.NextCursor,
	}
	for i, o := range page.Orders {
		res.Orders[i] = orderView(o)
	}
	return res, nil
}

// IssueStoreCredit backs the issue_store_credit tool. It refunds an order whose
// total is at or under the store-credit limit and rejects larger orders with an
// error that tells the agent to use request_refund instead.
func (s *Service) IssueStoreCredit(ctx context.Context, p *genorders.RefundCommand) (*genorders.RefundOutcome, error) {
	return s.refund(ctx, p, methodStoreCredit)
}

// RequestRefund backs the request_refund tool, which the runtime only executes
// after a manager approves it. It refunds an order whose total is over the
// store-credit limit to the card and rejects smaller orders with an error that
// tells the agent to use issue_store_credit instead.
func (s *Service) RequestRefund(ctx context.Context, p *genorders.RefundCommand) (*genorders.RefundOutcome, error) {
	return s.refund(ctx, p, methodCard)
}

// refund loads the customer's order, enforces the refund method the order total
// requires, and runs the refund exactly once.
func (s *Service) refund(ctx context.Context, p *genorders.RefundCommand, method string) (*genorders.RefundOutcome, error) {
	order, err := s.orders.GetOrder(ctx, &gencommerce.GetOrderPayload{
		TenantID:   p.TenantID,
		CustomerID: p.CustomerID,
		OrderID:    p.OrderID,
	})
	if err != nil {
		var se *goa.ServiceError
		if errors.As(err, &se) && se.Name == "not_found" {
			return nil, genorders.MakeInvalidRefund(fmt.Errorf("order %s is not one of this customer's orders", p.OrderID))
		}
		return nil, fmt.Errorf("load order %s: %w", p.OrderID, err)
	}
	if want := RefundMethod(order.TotalCents); want != method {
		return nil, genorders.MakeInvalidRefund(wrongMethodError(order, want))
	}
	receipt, err := s.refunds.Refund(ctx, Refund{
		TenantID:    p.TenantID,
		OrderID:     order.ID,
		AmountCents: order.TotalCents,
		Method:      method,
	})
	if err != nil {
		return nil, fmt.Errorf("refund order %s: %w", order.ID, err)
	}
	return &genorders.RefundOutcome{
		Status:      "refunded",
		OrderID:     receipt.OrderID,
		AmountCents: &receipt.AmountCents,
		Method:      &receipt.Method,
		ReceiptID:   &receipt.ReceiptID,
	}, nil
}

// wrongMethodError explains to the agent which tool the order's total requires.
func wrongMethodError(order *gencommerce.Order, want string) error {
	total := formatCents(order.TotalCents)
	limit := formatCents(StoreCreditLimitCents)
	if want == methodCard {
		return fmt.Errorf("order %s totals %s including tax and shipping, which is over %s: use request_refund so a manager can approve it", order.ID, total, limit)
	}
	return fmt.Errorf("order %s totals %s including tax and shipping, which is %s or less: use issue_store_credit", order.ID, total, limit)
}

// formatCents renders an amount in cents as dollars, for example "$54.00".
func formatCents(cents int64) string {
	return fmt.Sprintf("$%d.%02d", cents/100, cents%100)
}

// orderView converts a commerce order into the orders service's order type.
func orderView(o *gencommerce.Order) *genorders.Order {
	return &genorders.Order{
		ID:            o.ID,
		PlacedAt:      o.PlacedAt,
		ItemsCents:    o.ItemsCents,
		TaxCents:      o.TaxCents,
		ShippingCents: o.ShippingCents,
		TotalCents:    o.TotalCents,
	}
}
