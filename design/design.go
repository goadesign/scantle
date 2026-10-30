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
	Attribute("prompt", String, "Original operator feature request.")
	Attribute("summary", String, "Plain-English summary of requested behavior.")
	Attribute("ambiguities", ArrayOf(AmbiguityQuestion), "Domain questions surfaced at CP1, retained for audit after confirmation.")
	Attribute("answers", MapOf(String, String), "Confirmed resolution per ambiguity question ID.")
	Attribute("examples", ArrayOf(ExampleItem), "Concrete examples mapped to evaluator checks.")
	Attribute("preserved_behaviors", ArrayOf(String), "Existing contract invariants preserved by this change.")
	Attribute("confirmed_by", String, "Authenticated principal that confirmed the Change Card.")
	Attribute("request_revision", String, "SHA-256 digest of the canonical versioned Change Card.")
	Required("change_id", "status", "prompt", "summary", "examples", "preserved_behaviors", "request_revision")
})

// ConfirmIntentPayload resolves CP1 ambiguities and locks the Change Card.
// Operator authority is derived from the authenticated request context, never
// from a caller-supplied payload field.
var ConfirmIntentPayload = Type("ConfirmIntentPayload", func() {
	Description("Authenticated operator confirmation of Checkpoint 1 answers and examples.")
	Attribute("change_id", String, "Change identifier.")
	Attribute("answers", MapOf(String, String), "Selected resolution per ambiguity question ID.")
	Attribute("examples", ArrayOf(ExampleItem), "Confirmed example table.")
	Required("change_id", "answers", "examples")
})

// C4ContainerItem describes one container in a Checkpoint 2 C4 preview.
var C4ContainerItem = Type("C4ContainerItem", func() {
	Description("C4 container derived from the target Goa/Goa-AI design.")
	Attribute("id", String, "Stable container identifier.")
	Attribute("name", String, "Human-readable container name.")
	Attribute("kind", String, "Container role (service, agent, toolset, or dependency).", func() {
		Enum("service", "agent", "toolset", "dependency")
	})
	Attribute("description", String, "Summary of the container's responsibility.")
	Required("id", "name", "kind", "description")
})

// C4EdgeItem describes a directed relationship between two C4 containers.
var C4EdgeItem = Type("C4EdgeItem", func() {
	Description("Directed contract relationship between two C4 containers.")
	Attribute("from_id", String, "Source container ID.")
	Attribute("to_id", String, "Target container ID.")
	Attribute("protocol", String, "Contract or transport relationship.")
	Required("from_id", "to_id", "protocol")
})

// GuardrailItem reports the active Goa-AI cost and safety guardrails extracted
// from the target design.
var GuardrailItem = Type("GuardrailItem", func() {
	Description("Active Goa-AI cost and safety guardrails extracted from the design.")
	Attribute("max_execution_seconds", Int, "Per-run execution time budget in seconds.")
	Attribute("max_tool_calls", Int, "Per-run retrieval tool-call budget.")
	Attribute("compress_at_turns", Int, "Turn threshold for history compression.")
	Attribute("keep_recent_turns", Int, "Recent turns retained uncompressed.")
	Required("max_execution_seconds", "max_tool_calls", "compress_at_turns", "keep_recent_turns")
})

// PreviewArchitecturePayload is the input to preview_architecture (CP2).
var PreviewArchitecturePayload = Type("PreviewArchitecturePayload", func() {
	Description("Checkpoint 2 request binding a confirmed Change Card to a candidate Goa design.")
	Attribute("change_id", String, "Confirmed Change Card identifier.")
	Attribute("request_revision", String, "Expected CP1 request_revision digest.")
	Attribute("design_source", String, "Candidate Goa and Goa-AI design source to inspect.")
	Required("change_id", "request_revision", "design_source")
})

// ArchitecturePreviewResult is the Checkpoint 2 (CP2) output.
var ArchitecturePreviewResult = Type("ArchitecturePreviewResult", func() {
	Description("Checkpoint 2 C4 topology and guardrail preview derived from the candidate Goa design.")
	Attribute("change_id", String, "Bound Change Card identifier.")
	Attribute("request_revision", String, "Bound CP1 request_revision digest.")
	Attribute("design_digest", String, "SHA-256 digest of the inspected Goa design source.")
	Attribute("system_name", String, "API or primary service name declared in the design.")
	Attribute("containers", ArrayOf(C4ContainerItem), "Derived C4 containers.")
	Attribute("edges", ArrayOf(C4EdgeItem), "Validated directed edges between declared containers.")
	Attribute("guardrails", GuardrailItem, "Extracted Goa-AI runtime guardrails.")
	Attribute("banner", String, "Plain-English summary of topology and guardrails.")
	Required("change_id", "request_revision", "design_digest", "system_name", "containers", "edges", "guardrails", "banner")
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
	Description("Fast Draft Mode result distinguishing 0s target prompt reload from compiled Go rebuild.")
	Attribute("applied_in_store", Boolean, "True when updated in the target platform's prompt.Store for 0s reload.")
	Attribute("requires_rebuild", Boolean, "True when the change modifies a compiled Go business rule.")
	Attribute("prompt_version", String, "New prompt version digest returned by the target platform.")
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
	Attribute("has_changes", Boolean, "True when any declaration, contract body, or module directive changed.")
	Attribute("added_declarations", ArrayOf(String), "Added scoped declarations.")
	Attribute("removed_declarations", ArrayOf(String), "Removed scoped declarations.")
	Attribute("modified_declarations", ArrayOf(String), "Declarations whose payload, result, route, or policy changed.")
	Attribute("version_deltas", ArrayOf(String), "Changed module require, replace, or exclude directives.")
	Attribute("summary", String, "Human-readable structural diff summary.")
	Required("has_changes", "added_declarations", "removed_declarations", "modified_declarations", "version_deltas", "summary")
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

	Method("preview_architecture", func() {
		Description("Extract and validate the Checkpoint 2 C4 container topology and Goa-AI guardrails from a candidate Goa design bound to a confirmed Change Card.")
		Tool("preview_architecture", "Generate Checkpoint 2 (CP2) architecture and guardrail preview.")
		Payload(PreviewArchitecturePayload)
		Result(ArchitecturePreviewResult)
		HTTP(func() {
			POST("/changes/preview")
			Response(StatusOK)
		})
	})

	Method("draft_override", func() {
		Description("Apply a Fast Draft Mode prompt override on the bound target or reject a compiled-rule prompt hack.")
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
		Description("Compare Goa design ASTs and go.mod directives without emitting a blanket compatibility verdict.")
		Tool("diff", "Compute structural Goa AST and go.mod version diff.")
		Payload(DiffPayload)
		Result(DiffResult)
		HTTP(func() {
			POST("/diff")
			Response(StatusOK)
		})
	})
})
