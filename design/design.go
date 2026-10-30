// Package design defines the Goa and Goa-AI MCP contract for the Scantle
// software factory control plane.
package design

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

var _ = API("scantle", func() {
	Title("Scantle MCP Software Factory Control Plane")
	Description("MCP-native control plane for creating, verifying, and evolving production Goa-AI agentic platforms.")
	Version("1.0.0")
})

// AmbiguityQuestion represents a genuine domain question surfaced at Checkpoint 1.
var AmbiguityQuestion = Type("AmbiguityQuestion", func() {
	Description("Domain ambiguity surfaced with concrete examples at Checkpoint 1.")
	Attribute("id", String, "Stable question identifier.")
	Attribute("question", String, "Plain-English question for the operator.")
	Attribute("options", ArrayOf(String), "Concrete resolution choices.")
	Required("id", "question", "options")
})

// ExampleItem represents a concrete input/outcome row in a Checkpoint 1 Change Card.
var ExampleItem = Type("ExampleItem", func() {
	Description("Concrete input and expected outcome confirmed at Checkpoint 1.")
	Attribute("id", String, "Code-assigned stable example identifier.")
	Attribute("scenario", String, "Plain-English scenario description.")
	Attribute("input_summary", String, "Representative user request and domain fixture state.")
	Attribute("expected_outcome", String, "Expected side effect and response behavior.")
	Attribute("verification_kind", String, "Evaluator check classification (deterministic or model_judged).", func() {
		Enum("deterministic", "model_judged")
	})
	Required("id", "scenario", "input_summary", "expected_outcome", "verification_kind")
})

// ProposeChangePayload is the input to the CP1 propose_change tool.
var ProposeChangePayload = Type("ProposeChangePayload", func() {
	Description("Operator feature request submitted for Checkpoint 1 ambiguity and example analysis.")
	Attribute("change_id", String, "Unique change identifier.")
	Attribute("prompt", String, "Operator feature request.")
	Attribute("design_source", String, "Current target Goa design source inspected by the CP1 model.")
	Required("change_id", "prompt")
})

// ChangeCardResult is the output of CP1 propose_change and confirm_intent.
var ChangeCardResult = Type("ChangeCardResult", func() {
	Description("Checkpoint 1 Change Card containing examples, preserved behaviors, and audit metadata.")
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

// ConfirmIntentPayload locks the operator's confirmed ambiguity answers and example table at CP1.
var ConfirmIntentPayload = Type("ConfirmIntentPayload", func() {
	Description("Operator confirmation of Checkpoint 1 ambiguity answers and concrete examples.")
	Attribute("change_id", String, "Change identifier.")
	Attribute("request_revision", String, "Reviewed CP1 request_revision digest being confirmed.")
	Attribute("answers", MapOf(String, String), "Selected resolution per ambiguity question ID.")
	Attribute("examples", ArrayOf(ExampleItem), "Confirmed example table.")
	Required("change_id", "request_revision", "answers", "examples")
})

// C4ContainerItem describes one runtime container in a Checkpoint 2 C4 preview.
var C4ContainerItem = Type("C4ContainerItem", func() {
	Description("C4 container derived from the evaluated target Goa/Goa-AI design.")
	Attribute("id", String, "Stable container identifier.")
	Attribute("name", String, "Human-readable container name.")
	Attribute("kind", String, "Container role (service, agent, or toolset).", func() {
		Enum("service", "agent", "toolset")
	})
	Attribute("description", String, "Summary of the container's responsibility.")
	Required("id", "name", "kind", "description")
})

// C4EdgeItem describes a directed contract relationship between two C4 containers.
var C4EdgeItem = Type("C4EdgeItem", func() {
	Description("Directed contract relationship between two C4 containers.")
	Attribute("from_id", String, "Source container ID.")
	Attribute("to_id", String, "Target container ID.")
	Attribute("protocol", String, "Contract or transport relationship.")
	Required("from_id", "to_id", "protocol")
})

// AgentPolicyItem reports the evaluated Goa-AI runtime guardrails and cache policy for one agent.
var AgentPolicyItem = Type("AgentPolicyItem", func() {
	Description("Evaluated Goa-AI cost, safety, history, and cache policy for a single agent.")
	Attribute("agent_id", String, "Scoped agent container identifier.")
	Attribute("max_execution_seconds", Int, "Per-run execution time budget in seconds.")
	Attribute("max_tool_calls", Int, "Per-run retrieval tool-call budget.")
	Attribute("max_recovery_turns", Int, "Maximum consecutive recovery turns after validation failure.")
	Attribute("compress_at_turns", Int, "Turn threshold for history compression.")
	Attribute("keep_recent_turns", Int, "Recent turns retained uncompressed.")
	Attribute("cache_after_system", Boolean, "True when prompt caching places a checkpoint after system prompts.")
	Attribute("cache_after_tools", Boolean, "True when prompt caching places a checkpoint after tool definitions.")
	Required(
		"agent_id",
		"max_execution_seconds",
		"max_tool_calls",
		"max_recovery_turns",
		"compress_at_turns",
		"keep_recent_turns",
		"cache_after_system",
		"cache_after_tools",
	)
})

// ToolContractItem reports the evaluated tool schema, server-data injections,
// confirmation gate, and bounded-result contract for one tool.
var ToolContractItem = Type("ToolContractItem", func() {
	Description("Evaluated Goa-AI tool contract summary surfaced at Checkpoint 2.")
	Attribute("id", String, "Scoped tool identifier (<toolset>.<tool>).")
	Attribute("toolset", String, "Owning toolset name.")
	Attribute("name", String, "Tool name.")
	Attribute("description", String, "Tool description.")
	Attribute("args_schema", String, "Canonical evaluated argument schema summary.")
	Attribute("return_schema", String, "Canonical evaluated return schema summary.")
	Attribute("injected_fields", ArrayOf(String), "Server-supplied InjectedFields hidden from the model.")
	Attribute("requires_confirmation", Boolean, "True when the tool declares a design-time Confirmation gate.")
	Attribute("bounded_result", Boolean, "True when the tool declares a BoundedResult cursor/truncation contract.")
	Required(
		"id",
		"toolset",
		"name",
		"description",
		"args_schema",
		"return_schema",
		"injected_fields",
		"requires_confirmation",
		"bounded_result",
	)
})

// DesignSnapshot is an immutable copy of a Go module holding a Goa design.
var DesignSnapshot = Type("DesignSnapshot", func() {
	Description("Exact files of one Go module containing a Goa/Goa-AI design package at design/. Paths are clean, relative to the module root, and name only .go, go.mod and go.sum files; go.mod, go.sum and at least one design/*.go file are required. The evaluator stages exactly these bytes, resolves dependencies only from go.mod and go.sum, and never reads host paths.")
	Attribute("files", MapOf(String, String), "File contents keyed by module-relative path, such as design/design.go or go.mod.")
	Required("files")
})

// PreviewArchitecturePayload is the input to the CP2 preview_architecture tool.
var PreviewArchitecturePayload = Type("PreviewArchitecturePayload", func() {
	Description("Request to derive the Checkpoint 2 C4 topology and guardrails from a candidate Goa design bound to a confirmed Change Card.")
	Attribute("change_id", String, "Confirmed Change Card identifier.")
	Attribute("request_revision", String, "Expected CP1 request_revision digest.")
	Attribute("design", DesignSnapshot, "Candidate module whose Goa and Goa-AI design is evaluated.")
	Required("change_id", "request_revision", "design")
})

// ArchitecturePreviewResult is the output of the CP2 preview_architecture tool.
var ArchitecturePreviewResult = Type("ArchitecturePreviewResult", func() {
	Description("Checkpoint 2 C4 container topology, tool contracts, and per-agent Goa-AI guardrails derived from the evaluated candidate design.")
	Attribute("change_id", String, "Bound Change Card identifier.")
	Attribute("request_revision", String, "Bound CP1 request_revision digest.")
	Attribute("design_digest", String, "SHA-256 digest of the evaluated design snapshot (sorted path and content pairs).")
	Attribute("system_name", String, "API or primary service name declared in the design.")
	Attribute("containers", ArrayOf(C4ContainerItem), "Derived C4 containers.")
	Attribute("edges", ArrayOf(C4EdgeItem), "Validated directed edges between declared containers.")
	Attribute("agent_policies", ArrayOf(AgentPolicyItem), "Evaluated Goa-AI runtime policies per declared agent.")
	Attribute("tools", ArrayOf(ToolContractItem), "Evaluated Goa-AI tool contracts.")
	Attribute("banner", String, "Plain-English summary of topology and guardrails.")
	Required(
		"change_id",
		"request_revision",
		"design_digest",
		"system_name",
		"containers",
		"edges",
		"agent_policies",
		"tools",
		"banner",
	)
})

// DraftOverridePayload is the input to the CP3 Fast Draft Mode tool.
var DraftOverridePayload = Type("DraftOverridePayload", func() {
	Description("Fast Draft Mode request to hot-reload a prompt template on the bound target platform or reject a compiled business-rule edit.")
	Attribute("target_kind", String, "Override target kind (prompt_template or compiled_rule).", func() {
		Enum("prompt_template", "compiled_rule")
	})
	Attribute("prompt_id", String, "Registered prompt identifier when target_kind is prompt_template.")
	Attribute("new_content", String, "Updated prompt template text or rule expression.")
	Required("target_kind", "new_content")
})

// DraftOverrideResult reports whether the CP3 edit was hot-reloaded in 0s or requires a full Go rebuild.
var DraftOverrideResult = Type("DraftOverrideResult", func() {
	Description("Result of a Checkpoint 3 Fast Draft Mode override request.")
	Attribute("applied_in_store", Boolean, "True when updated in the target platform's prompt.Store for 0s reload.")
	Attribute("requires_rebuild", Boolean, "True when the change modifies a compiled Go business rule.")
	Attribute("prompt_version", String, "New prompt version digest returned by the target platform.")
	Attribute("message", String, "Operator-facing status explanation.")
	Required("applied_in_store", "requires_rebuild", "message")
})

// DiffPayload is the input to the structural design and module diff tool.
var DiffPayload = Type("DiffPayload", func() {
	Description("Previous and candidate design modules to compare structurally, each evaluated with its own go.mod dependencies.")
	Attribute("old", DesignSnapshot, "Previous design module.")
	Attribute("new", DesignSnapshot, "Candidate design module.")
	Required("old", "new")
})

// DiffResult reports structural declaration and go.mod version deltas without a blanket compatibility claim.
var DiffResult = Type("DiffResult", func() {
	Description("Structural diff of evaluated Goa/Goa-AI declarations and go.mod directives.")
	Attribute("has_changes", Boolean, "True when any declaration, evaluated contract schema, or module directive changed.")
	Attribute("added_declarations", ArrayOf(String), "Added scoped declarations.")
	Attribute("removed_declarations", ArrayOf(String), "Removed scoped declarations.")
	Attribute("modified_declarations", ArrayOf(String), "Declarations whose evaluated schema, route, or policy changed.")
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
		Payload(ProposeChangePayload)
		Result(ChangeCardResult)
		Tool("propose_change", "Propose a feature change and generate a Checkpoint 1 Change Card.", func() {
			ToolUI("ui://scantle/checkpoint")
		})
		HTTP(func() {
			POST("/changes/propose")
			Response(StatusOK)
		})
	})

	Method("confirm_intent", func() {
		Description("Confirm Checkpoint 1 answers and examples to freeze the Change Card.")
		Payload(ConfirmIntentPayload)
		Result(ChangeCardResult)
		Tool("confirm_intent", "Confirm Checkpoint 1 ambiguity answers and example checks.", func() {
			ToolUI("ui://scantle/checkpoint")
		})
		HTTP(func() {
			POST("/changes/confirm")
			Response(StatusOK)
		})
	})

	Method("preview_architecture", func() {
		Description("Extract and validate the Checkpoint 2 C4 container topology, tool contracts, and Goa-AI guardrails from a candidate Goa design bound to a confirmed Change Card.")
		Payload(PreviewArchitecturePayload)
		Result(ArchitecturePreviewResult)
		Tool("preview_architecture", "Preview Checkpoint 2 C4 container topology and Goa-AI guardrails for a confirmed Change Card.", func() {
			ToolUI("ui://scantle/checkpoint")
		})
		HTTP(func() {
			POST("/changes/preview")
			Response(StatusOK)
		})
	})

	Method("draft_override", func() {
		Description("Apply a Fast Draft Mode prompt override on the bound target or reject a compiled-rule prompt hack.")
		Payload(DraftOverridePayload)
		Result(DraftOverrideResult)
		Tool("draft_override", "Hot-reload a prompt template in 0s or flag a compiled business rule change.", func() {
			ToolUI("ui://scantle/checkpoint")
		})
		HTTP(func() {
			POST("/sandbox/draft")
			Response(StatusOK)
		})
	})

	Method("diff", func() {
		Description("Compare evaluated Goa designs and go.mod directives without emitting a blanket compatibility verdict.")
		Payload(DiffPayload)
		Result(DiffResult)
		Tool("diff", "Compute structural Goa design and go.mod dependency diffs.")
		HTTP(func() {
			POST("/diff")
			Response(StatusOK)
		})
	})
})
