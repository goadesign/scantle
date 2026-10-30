package design

import (
	. "goa.design/goa/v3/dsl"
)

// Order is one customer order as the commerce system reports it. All money
// amounts are integer cents so refund limits compare exactly.
var Order = Type("Order", func() {
	Description("A customer order with its charged amounts in cents.")
	Attribute("id", String, "Order identifier.", func() { MinLength(1) })
	Attribute("placed_at", String, "When the order was placed.", func() { Format(FormatDateTime) })
	Attribute("items_cents", Int64, "Item subtotal in cents.", func() { Minimum(0) })
	Attribute("tax_cents", Int64, "Tax charged in cents.", func() { Minimum(0) })
	Attribute("shipping_cents", Int64, "Shipping charged in cents.", func() { Minimum(0) })
	Attribute("total_cents", Int64, "Total charged in cents, including tax and shipping.", func() { Minimum(0) })
	Required("id", "placed_at", "items_cents", "tax_cents", "shipping_cents", "total_cents")
})

// OrderPage is one page of a customer's orders.
var OrderPage = Type("OrderPage", func() {
	Description("One page of a customer's orders, newest first.")
	Attribute("orders", ArrayOf(Order), "Orders on this page.")
	Attribute("total", Int, "Number of orders the customer has in total.", func() { Minimum(0) })
	Attribute("next_cursor", String, "Cursor for the next page; absent on the last page.")
	Required("orders", "total")
})

// Receipt is the payment provider's record of one refund.
var Receipt = Type("Receipt", func() {
	Description("The payment provider's record of a refund.")
	Attribute("receipt_id", String, "Provider receipt identifier.", func() { MinLength(1) })
	Attribute("order_id", String, "Refunded order.", func() { MinLength(1) })
	Attribute("amount_cents", Int64, "Refunded amount in cents.", func() { Minimum(1) })
	Attribute("method", String, "How the money was returned.", func() { Enum("store_credit", "card") })
	Required("receipt_id", "order_id", "amount_cents", "method")
})

// The commerce service is the contract of the external order system the
// candidate reads from. The candidate only uses its generated client; the
// evaluator implements its generated server in sim, and staging points the
// client at the real system.
var _ = Service("commerce", func() {
	Description("External order system: lists a customer's orders.")
	Method("list_orders", func() {
		Description("List one page of a customer's orders, newest first.")
		Payload(func() {
			Attribute("tenant_id", String, "Merchant tenant.", func() { MinLength(1) })
			Attribute("customer_id", String, "Customer whose orders to list.", func() { MinLength(1) })
			Attribute("cursor", String, "Cursor returned by the previous page.")
			Attribute("limit", Int, "Maximum orders to return.", func() {
				Minimum(1)
				Maximum(50)
				Default(20)
			})
			Required("tenant_id", "customer_id")
		})
		Result(OrderPage)
		HTTP(func() {
			GET("/tenants/{tenant_id}/customers/{customer_id}/orders")
			Param("cursor")
			Param("limit")
			Response(StatusOK)
		})
	})
	Method("get_order", func() {
		Description("Load one of a customer's orders. Orders of other customers are not found.")
		Payload(func() {
			Attribute("tenant_id", String, "Merchant tenant.", func() { MinLength(1) })
			Attribute("customer_id", String, "Customer who must own the order.", func() { MinLength(1) })
			Attribute("order_id", String, "Order to load.", func() { MinLength(1) })
			Required("tenant_id", "customer_id", "order_id")
		})
		Result(Order)
		Error("not_found", ErrorResult, "The customer has no such order.")
		HTTP(func() {
			GET("/tenants/{tenant_id}/customers/{customer_id}/orders/{order_id}")
			Response(StatusOK)
			Response("not_found", StatusNotFound)
		})
	})
})

// The payments service is the contract of the external payment provider. The
// provider removes duplicate refunds that share an idempotency key and returns
// the original receipt for them.
var _ = Service("payments", func() {
	Description("External payment provider: issues refunds exactly once per idempotency key.")
	Method("refund", func() {
		Description("Refund an order. A repeated idempotency key returns the original receipt.")
		Payload(func() {
			Attribute("tenant_id", String, "Merchant tenant.", func() { MinLength(1) })
			Attribute("order_id", String, "Order to refund.", func() { MinLength(1) })
			Attribute("amount_cents", Int64, "Amount to refund in cents.", func() { Minimum(1) })
			Attribute("method", String, "How to return the money.", func() { Enum("store_credit", "card") })
			Attribute("idempotency_key", String, "Key that identifies this refund across retries.", func() { MinLength(1) })
			Required("tenant_id", "order_id", "amount_cents", "method", "idempotency_key")
		})
		Result(Receipt)
		HTTP(func() {
			POST("/tenants/{tenant_id}/refunds")
			Header("idempotency_key:Idempotency-Key")
			Response(StatusOK)
		})
	})
})
