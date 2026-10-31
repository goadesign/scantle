package design

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa-ai/eval/dsl"
	. "goa.design/goa/v3/dsl"
)

// The acceptance suite is owned by the evaluator. Scenarios tagged "repair"
// report full diagnostics to builders while they fix a change; scenarios
// tagged "holdout" run only at promotion, and builders see only their counts.
var _ = Suite("orders_acceptance", func() {
	Description("Proves the orders platform's confirmed refund behavior through its public API.")
	Timeout("3m")

	Scenario("store_credit_within_limit", func() {
		Description("An order totalling $38.50 with tax is refunded as store credit without a manager.")
		Tags("repair")
	})
	Scenario("card_refund_needs_manager", func() {
		Description("An order totalling $54.00 including shipping waits for a manager; only that tenant's manager can approve it, and exactly one card refund follows.")
		Tags("repair")
	})
	Scenario("refund_survives_worker_crash", func() {
		Description("Killing the refund worker right after the payment provider accepted a refund still yields exactly one refund and a completed run.")
		Tags("repair")
	})
	Scenario("pending_approval_survives_api_restart", func() {
		Description("A refund waiting for a manager survives killing and restarting the orders API process; the manager's approval afterwards still yields exactly one card refund.")
		Tags("repair")
	})
	Scenario("other_customers_order_refused", func() {
		Description("A customer asking to refund another customer's order gets no refund.")
		Tags("holdout")
	})
})
