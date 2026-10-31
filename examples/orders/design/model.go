package design

import (
	m "goa.design/model/dsl"
)

// The C4 model of the orders platform: who uses it, which processes it runs,
// what each one owns, and which external systems it calls. goa gen renders it
// to gen/structurizr/model.json, and Surla's architecture preview shows it
// as the platform's containers and their connections.
var _ = m.Design("Orders platform", "Refund assistant for a merchant's customers.", func() {
	m.Person("Customer", "Shopper asking for a refund on their own orders.", func() {
		m.Uses("Orders platform/Orders API", "Asks for refunds and reads the outcome", "HTTPS, JSON", m.Synchronous)
	})
	m.Person("Manager", "Merchant staff who approve refunds over $50.00.", func() {
		m.Uses("Orders platform/Orders API", "Approves or denies card refunds", "HTTPS, JSON", m.Synchronous)
	})

	m.SoftwareSystem("Orders platform", "Refund assistant and the durable refund flow behind it.", func() {
		m.Container("Orders API", "Public HTTP API and refund agent. Authenticates every call, runs agent turns, and owns runs, transcripts and pending approvals.", "Go, Goa, Goa-AI", func() {
			m.Uses("Run store", "Saves runs, transcripts and pending approvals", "SQLite", m.Synchronous)
			m.Uses("Workflow engine", "Runs agent turns and starts one refund workflow per order", "Temporal, gRPC", m.Synchronous)
			m.Uses("Commerce system", "Lists and reads the customer's orders", "HTTPS, JSON", m.Synchronous)
			m.Uses("Language model", "Plans the next tool call through the configured model connection", "HTTPS to the provider, or gRPC to a goa-ai model gateway", m.Synchronous)
		})
		m.Container("Refund worker", "Completes each refund exactly once, retrying after crashes with the same idempotency key.", "Go, Temporal", func() {
			m.Uses("Workflow engine", "Polls for refund workflows", "Temporal, gRPC", m.Synchronous)
			m.Uses("Payment provider", "Issues the refund, keyed by tenant and order", "HTTPS, JSON", m.Synchronous)
		})
		m.Container("Run store", "Durable agent state owned by the Orders API alone.", "SQLite", func() {
			m.Tag("Database")
		})
		m.Container("Workflow engine", "Durable execution for agent turns and refund workflows.", "Temporal")
	})

	m.SoftwareSystem("Commerce system", "The merchant's order records.", func() {
		m.External()
	})
	m.SoftwareSystem("Payment provider", "Moves money for refunds; honors idempotency keys.", func() {
		m.External()
	})
	m.SoftwareSystem("Language model", "Model the refund agent uses to choose tools: a hosted provider by default (--model-provider anthropic), or any model behind a goa-ai model gateway (--model-provider gateway). The scripted demo stand-in (--model-provider scripted) runs inside the Orders API and needs no connection.", func() {
		m.External()
	})

	m.Views(func() {
		m.SystemContextView("Orders platform", "Context", "Who uses the orders platform and what it depends on.", func() {
			m.AddAll()
			m.AutoLayout(m.RankLeftRight)
		})
		m.ContainerView("Orders platform", "Containers", "The orders platform's processes and the state each one owns.", func() {
			m.AddAll()
			m.AutoLayout(m.RankLeftRight)
		})
	})
})
