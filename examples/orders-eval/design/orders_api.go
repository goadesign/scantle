// Package design is the evaluator's own copy of the approved contracts for the
// orders platform: the public orders API it drives, the dependency APIs it
// simulates, and the acceptance suite it runs. The evaluator generates its
// clients and servers from this package with the trusted generator and never
// imports code from the candidate's repository.
package design

import (
	. "goa.design/goa/v3/dsl"
)

// JWTAuth is the approved bearer-token scheme of the orders API.
var JWTAuth = JWTSecurity("jwt", func() {
	Description("Bearer token issued by the merchant's identity provider.")
	Scope("orders:chat", "Talk to the refund assistant about your own orders.")
	Scope("refunds:approve", "Approve or deny refunds that need a manager.")
})

var _ = API("orders", func() {
	Title("Orders reference platform (approved public contract)")
})

// RunView is the approved caller-visible state of a refund conversation.
var RunView = Type("RunView", func() {
	Description("State of a refund assistant run.")
	Attribute("run_id", String, "Run identifier.")
	Attribute("status", String, "Run state.", func() { Enum("running", "awaiting_approval", "completed", "failed") })
	Attribute("reply", String, "Assistant reply once the run completes.")
	Attribute("approval_id", String, "Pending approval to decide when status is awaiting_approval.")
	Attribute("approval_prompt", String, "What the manager is asked to approve.")
	Required("run_id", "status")
})

// The orders service here is only the public HTTP contract the evaluator
// drives; the candidate's agent and tool-backing methods are not part of it.
var _ = Service("orders", func() {
	Description("Refund assistant for a merchant's customers.")
	Security(JWTAuth, func() { Scope("orders:chat") })
	Error("unauthorized", ErrorResult, "The bearer token is missing or invalid.")
	Error("forbidden", ErrorResult, "The caller may not perform this action.")
	Error("not_found", ErrorResult, "The run or approval does not exist.")
	HTTP(func() {
		Response("unauthorized", StatusUnauthorized)
		Response("forbidden", StatusForbidden)
		Response("not_found", StatusNotFound)
	})

	Method("chat", func() {
		Description("Send a message to the refund assistant and start a run.")
		Payload(func() {
			Token("token", String, "Customer bearer token.")
			Attribute("message", String, "What the customer says.", func() { MinLength(1) })
			Required("token", "message")
		})
		Result(RunView)
		HTTP(func() {
			POST("/chat")
			Response(StatusAccepted)
		})
	})

	Method("run_status", func() {
		Description("Read the state of a run the caller started.")
		Payload(func() {
			Token("token", String, "Customer bearer token.")
			Attribute("run_id", String, "Run to read.", func() { MinLength(1) })
			Required("token", "run_id")
		})
		Result(RunView)
		HTTP(func() {
			GET("/runs/{run_id}")
			Response(StatusOK)
		})
	})

	Method("approve_refund", func() {
		Description("Approve or deny a refund that is waiting for a manager.")
		Security(JWTAuth, func() { Scope("refunds:approve") })
		Payload(func() {
			Token("token", String, "Manager bearer token.")
			Attribute("run_id", String, "Run waiting for approval.", func() { MinLength(1) })
			Attribute("approval_id", String, "Approval being decided, as reported by run_status.", func() { MinLength(1) })
			Attribute("approved", Boolean, "Whether the refund is approved.")
			Required("token", "run_id", "approval_id", "approved")
		})
		Result(RunView)
		HTTP(func() {
			POST("/runs/{run_id}/approval")
			Response(StatusAccepted)
		})
	})
})
