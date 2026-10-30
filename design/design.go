// Package design defines the Goa and Goa-AI MCP contract for the Scantle
// control plane.
package design

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

var _ = API("scantle", func() {
	Title("Scantle MCP Software Factory")
	Description("Design-first MCP control plane for creating, verifying, and evolving production agentic platforms on the Goa Design suite.")
	Version("1.0.0")
})

// ExampleItem describes one concrete input/outcome row in a CP1 Change Card.
var ExampleItem = Type("ExampleItem", func() {
	Description("Concrete input and expected outcome confirmed at Checkpoint 1.")
	Attribute("id", String, "Stable example identifier.")
	Attribute("scenario", String, "Plain-English scenario description.")
	Attribute("input_summary", String, "Representative user request and domain fixture state.")
	Attribute("expected_outcome", String, "Expected side effect and response behavior.")
	Attribute("verification_kind", String, "Evaluator check classification (deterministic or model_judged).", func() {
		Enum("deterministic", "model_judged")
	})
	Required("id", "scenario", "input_summary", "expected_outcome", "verification_kind")
})

// AmbiguityQuestion captures a domain question that requires human confirmation
// at Checkpoint 1 before code generation proceeds.
var AmbiguityQuestion = Type("AmbiguityQuestion", func() {
	Description("Domain ambiguity surfaced with concrete examples at Checkpoint 1.")
	Attribute("id", String, "Stable question identifier.")
	Attribute("question", String, "Plain-English question for the operator.")
	Attribute("options", ArrayOf(String), "Concrete resolution choices.")
	Required("id", "question", "options")
})

// ProposeChangePayload is the input to propose_change.
var ProposeChangePayload = Type("ProposeChangePayload", func() {
	Description("Natural-language feature request or contract evolution.")
	Attribute("change_id", String, "Client-supplied or generated change identifier.")
	Attribute("prompt", String, "Operator feature request.")
	Attribute("design_source", String, "Current target Goa design source inspected by the CP1 model.")
	Required("change_id", "prompt")
})

// ChangeCardResult is the Checkpoint 1 (CP1) output.
var ChangeCardResult = Type("ChangeCardResult", func() {
	Description("Frozen or pending Checkpoint 1 Change Card.")
	Attribute("change_id", String, "Change identifier.")
	Attribute("status", String, "Checkpoint status (needs_confirmation or confirmed).", func() {
		Enum("needs_confirmation", "confirmed")
	})
	Attribute("summary", String, "Plain-English summary of requested behavior.")
	Attribute("ambiguities", ArrayOf(AmbiguityQuestion), "Unresolved domain questions, empty when clear.")
	Attribute("examples", ArrayOf(ExampleItem), "Concrete examples mapped to evaluator checks.")
	Attribute("preserved_behaviors", ArrayOf(String), "Existing contract invariants preserved by this change.")
	Attribute("request_revision", String, "SHA-256 digest of the confirmed Change Card.")
	Required("change_id", "status", "summary", "examples", "preserved_behaviors", "request_revision")
})

// ConfirmIntentPayload resolves CP1 ambiguities and locks the Change Card.
var ConfirmIntentPayload = Type("ConfirmIntentPayload", func() {
	Description("Operator confirmation of Checkpoint 1 answers and examples.")
	Attribute("change_id", String, "Change identifier.")
	Attribute("operator", String, "Authenticated operator identity.")
	Attribute("answers", MapOf(String, String), "Selected resolution per ambiguity question ID.")
	Attribute("examples", ArrayOf(ExampleItem), "Confirmed example table.")
	Required("change_id", "operator", "answers", "examples")
})

// DraftOverridePayload applies a Fast Draft Mode tweak in CP3.
var DraftOverridePayload = Type("DraftOverridePayload", func() {
	Description("Fast Draft Mode override request in Checkpoint 3.")
	Attribute("target_kind", String, "Override target kind (prompt_template or compiled_rule).", func() {
		Enum("prompt_template", "compiled_rule")
	})
	Attribute("prompt_id", String, "Registered prompt identifier when target_kind is prompt_template.")
	Attribute("new_content", String, "Updated prompt template text or rule expression.")
	Required("target_kind", "new_content")
})

// DraftOverrideResult reports how the override was applied.
var DraftOverrideResult = Type("DraftOverrideResult", func() {
	Description("Fast Draft Mode result distinguishing 0s prompt reload from compiled Go rebuild.")
	Attribute("applied_in_store", Boolean, "True when updated in prompt.Store for 0s reload.")
	Attribute("requires_rebuild", Boolean, "True when the change modifies a compiled Go business rule.")
	Attribute("prompt_version", String, "New prompt version digest when applied in prompt.Store.")
	Attribute("message", String, "Operator-facing status explanation.")
	Required("applied_in_store", "requires_rebuild", "message")
})

// DiffPayload supplies two design and go.mod snapshots to compare.
var DiffPayload = Type("DiffPayload", func() {
	Description("Old and new design and go.mod sources for structural comparison.")
	Attribute("old_design", String, "Previous Go/Goa design source.")
	Attribute("new_design", String, "Candidate Go/Goa design source.")
	Attribute("old_gomod", String, "Previous go.mod file contents.")
	Attribute("new_gomod", String, "Candidate go.mod file contents.")
	Required("old_design", "new_design", "old_gomod", "new_gomod")
})

// DiffResult reports structural AST and go.mod version deltas.
var DiffResult = Type("DiffResult", func() {
	Description("Factual structural and module version diff report.")
	Attribute("has_changes", Boolean, "True when any declaration or module pin changed.")
	Attribute("added_declarations", ArrayOf(String), "Added services, methods, agents, or tools.")
	Attribute("removed_declarations", ArrayOf(String), "Removed services, methods, agents, or tools.")
	Attribute("version_deltas", ArrayOf(String), "Changed module dependency pins.")
	Attribute("summary", String, "Human-readable structural diff summary.")
	Required("has_changes", "added_declarations", "removed_declarations", "version_deltas", "summary")
})

var _ = Service("scantle", func() {
	Description("Scantle MCP software factory control plane.")
	MCP("scantle", "1.0.0")
	JSONRPC(func() {
		POST("/mcp")
	})

	Method("checkpoint_ui", func() {
		Description("Serve the interactive MCP Apps HTML host for Scantle checkpoints.")
		Result(String)
		Resource("checkpoint_ui", "ui://scantle/checkpoint", "text/html;profile=mcp-app")
		HTTP(func() {
			GET("/ui/checkpoint")
			Response(StatusOK)
		})
	})

	Method("propose_change", func() {
		Description("Evaluate a feature request at Checkpoint 1, surfacing concrete examples and any genuine domain ambiguity.")
		Tool("propose_change", "Evaluate a feature request at Checkpoint 1 (CP1).", func() {
			ToolUI("ui://scantle/checkpoint")
		})
		Payload(ProposeChangePayload)
		Result(ChangeCardResult)
		HTTP(func() {
			POST("/changes/propose")
			Response(StatusOK)
		})
	})

	Method("confirm_intent", func() {
		Description("Confirm Checkpoint 1 answers and examples to freeze the Change Card.")
		Tool("confirm_intent", "Confirm Checkpoint 1 (CP1) examples and ambiguity answers.")
		Payload(ConfirmIntentPayload)
		Result(ChangeCardResult)
		HTTP(func() {
			POST("/changes/confirm")
			Response(StatusOK)
		})
	})

	Method("draft_override", func() {
		Description("Apply a Fast Draft Mode prompt override in prompt.Store or reject a compiled-rule prompt hack.")
		Tool("draft_override", "Apply a Fast Draft Mode override in Checkpoint 3 (CP3).", func() {
			ToolUI("ui://scantle/checkpoint")
		})
		Payload(DraftOverridePayload)
		Result(DraftOverrideResult)
		HTTP(func() {
			POST("/sandbox/draft")
			Response(StatusOK)
		})
	})

	Method("diff", func() {
		Description("Compare Goa design ASTs and go.mod versions without emitting a blanket compatibility verdict.")
		Tool("diff", "Compute structural Goa AST and go.mod version diff.")
		Payload(DiffPayload)
		Result(DiffResult)
		HTTP(func() {
			POST("/diff")
			Response(StatusOK)
		})
	})
})
