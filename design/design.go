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

// GetChangePayload is the input to the get_change tool.
var GetChangePayload = Type("GetChangePayload", func() {
	Description("Identifies the Change Card the authenticated owner wants to read.")
	Attribute("change_id", String, "Change identifier.")
	Required("change_id")
})

// UICompositionItem is an optional model-authored decision view for one
// Change Card revision.
var UICompositionItem = Type("UICompositionItem", func() {
	Description("Model-authored OpenUI Lang decision view for exactly one Change Card revision. It is presentation only: it is never hashed into request_revision, confirm_intent never reads it, and it describes the card only while composed_for_revision equals the card's request_revision.")
	Attribute("program", String, "OpenUI Lang program composed by the CP1 model from the Scantle decision component catalog, exactly as the model returned it. The server does not check it; the checkpoint viewer validates it and shows its error state, with the rest of the card still usable, when the program is not a valid composition.")
	Attribute("composed_for_revision", String, "request_revision of the Change Card this program was composed for.")
	Required("program", "composed_for_revision")
})

// ChangeCardResult is the output of CP1 propose_change, confirm_intent and get_change.
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
	Attribute("ui_composition", UICompositionItem, "Decision view the CP1 model composed for this revision, when it produced one. A confirm_intent that changes answers or examples returns the new revision without it.")
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

// C4ContainerItem describes one architecture element in a Checkpoint 2 preview.
var C4ContainerItem = Type("C4ContainerItem", func() {
	Description("Architecture element declared by the target Goa, Goa-AI, or Model design.")
	Attribute("id", String, "Stable container identifier.")
	Attribute("name", String, "Human-readable container name.")
	Attribute("kind", String, "Declared service, agent, toolset, person, system, container, or component.", func() {
		Enum("service", "agent", "toolset", "person", "system", "container", "component")
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

// ToolInputBindingItem lists the compiled ordinary and lookup input choices for
// one consumer; it does not assert which installer the application invokes.
var ToolInputBindingItem = Type("ToolInputBindingItem", func() {
	Description("Compiled consumer input contracts declared for one selected tool. The application installs a binder and supplies values at runtime; compilation does not prove that wiring.")
	Attribute("args_schema", String, "Generated ordinary bound model input.")
	Attribute("text_only_args_schema", String, "Generated ordinary text-only input, when supported.")
	Attribute("lookup_args_schema", String, "Generated lookup-key input, when declared; application lookup values are not inspected.")
	Attribute("lookup_text_only_args_schema", String, "Generated text-only lookup-key input, when supported.")
	Attribute("injected_fields", ArrayOf(String), "Compiler-listed fields supplied outside the ordinary bound model input.")
	Required("args_schema", "injected_fields")
})

// ToolContractItem reports the evaluated tool schema, server-data injections,
// confirmation gate, and bounded-result contract for one tool.
var ToolContractItem = Type("ToolContractItem", func() {
	Description("Evaluated Goa-AI tool contract summary surfaced at Checkpoint 2.")
	Attribute("id", String, "Consumer agent ID followed by the compiler's tool ID (<agent_id>/<tool_id>).")
	Attribute("toolset", String, "Compiler-selected toolset registration route.")
	Attribute("name", String, "Tool name.")
	Attribute("description", String, "Tool description.")
	Attribute("args_schema", String, "Generated provider model input before consumer binding.")
	Attribute("text_only_args_schema", String, "Generated provider text-only input, when supported.")
	Attribute("input_binding", ToolInputBindingItem, "Compiled consumer input choices, when binding is declared.")
	Attribute("return_schema", String, "Generated model-visible result schema; empty when the tool declares no result.")
	Attribute("injected_fields", ArrayOf(String), "Compiler-listed runtime or provider fields omitted from the provider model input.")
	Attribute("requires_ui", Boolean, "True when the tool requires a UI-capable runtime.")
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
		"requires_ui",
		"bounded_result",
	)
})

// BuilderScopeItem defines an explicit file-ownership slice for one builder agent.
var BuilderScopeItem = Type("BuilderScopeItem", func() {
	Description("Explicit repository path ownership slice for a builder agent role.")
	Attribute("name", String, "Human-readable builder scope name (for example, orders or api).")
	Attribute("paths", ArrayOf(String), "Repository-relative path globs owned by this builder.")
	Attribute("preferred_profile_id", String, "Optional preference selecting a server-configured authenticated profile ID.")
	Required("name", "paths")
})

// SwarmRoleItem describes one proposed agent role assignment in a Scantle build swarm.
var SwarmRoleItem = Type("SwarmRoleItem", func() {
	Description("Proposed Chorus agent role assignment derived from server-owned provider profiles and explicit workspace scopes.")
	Attribute("handle", String, "Deterministic Chorus agent handle (without leading '@', at most 32 characters).")
	Attribute("role", String, "Assigned swarm role kind.", func() {
		Enum("architect", "builder", "eval_author", "reviewer")
	})
	Attribute("scope_name", String, "Human-readable scope label for this assignment.")
	Attribute("profile_id", String, "Assigned server-configured provider profile ID.")
	Attribute("provider", String, "Assigned coding-agent provider name.")
	Attribute("model", String, "Assigned model identifier.")
	Attribute("family", String, "Canonical model family identifier.")
	Attribute("owned_paths", ArrayOf(String), "Repository or workspace path globs owned by this role.")
	Attribute("access_mode", String, "Workspace access boundary required of the job runner.", func() {
		Enum("read_only_after_cp2", "read_write_worktree", "isolated_eval_workspace", "read_only_review")
	})
	Required(
		"handle",
		"role",
		"scope_name",
		"profile_id",
		"provider",
		"model",
		"family",
		"owned_paths",
		"access_mode",
	)
})

// SwarmPlanItem reports the proposed Chorus role roster and model-family routing at Checkpoint 2.
var SwarmPlanItem = Type("SwarmPlanItem", func() {
	Description("Proposed Chorus role assignments and model-family diversity facts surfaced at Checkpoint 2. Role and model assignments do not prove launch or replace process/workspace isolation.")
	Attribute("status", String, "Roster configuration status.", func() {
		Enum("configured", "unconfigured", "missing_capability")
	})
	Attribute("assignments", ArrayOf(SwarmRoleItem), "Deterministic role assignments when status is configured.")
	Attribute("cross_family_review", Boolean, "True if and only if status is configured and the reviewer model family is disjoint from every assigned builder model family.")
	Attribute("cross_family_eval", Boolean, "True if and only if status is configured and the eval_author model family is disjoint from every assigned builder model family.")
	Attribute("single_family", Boolean, "True when status is configured and all assigned roles share one model family.")
	Attribute("missing_roles", ArrayOf(String), "Required roles lacking an authenticated capable profile when status is unconfigured or missing_capability.")
	Attribute("summary", String, "Plain-English summary of the proposed swarm roster and model-family posture.")
	Attribute("manifest", String, "Formatted Chorus collaboration manifest text when status is configured; does not claim publication until sent via Chorus.")
	Required(
		"status",
		"assignments",
		"cross_family_review",
		"cross_family_eval",
		"single_family",
		"summary",
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
	Description("Request to derive the Checkpoint 2 C4 topology, guardrails, and proposed swarm roster from a candidate Goa design bound to a confirmed Change Card.")
	Attribute("change_id", String, "Confirmed Change Card identifier.")
	Attribute("request_revision", String, "Expected CP1 request_revision digest.")
	Attribute("design", DesignSnapshot, "Candidate module whose Goa and Goa-AI design is evaluated.")
	Attribute("builders", ArrayOf(BuilderScopeItem), "Optional explicit builder path scopes for proposing the Chorus build roster.")
	Required("change_id", "request_revision", "design")
})

// ArchitecturePreviewResult is the output of the CP2 preview_architecture tool.
var ArchitecturePreviewResult = Type("ArchitecturePreviewResult", func() {
	Description("Checkpoint 2 C4 container topology, tool contracts, per-agent Goa-AI guardrails, and proposed Chorus swarm roster derived from the evaluated candidate design.")
	Attribute("change_id", String, "Bound Change Card identifier.")
	Attribute("request_revision", String, "Bound CP1 request_revision digest.")
	Attribute("design_digest", String, "SHA-256 digest of the evaluated design snapshot (sorted path and content pairs).")
	Attribute("system_name", String, "API or primary service name declared in the design.")
	Attribute("containers", ArrayOf(C4ContainerItem), "Derived C4 containers.")
	Attribute("edges", ArrayOf(C4EdgeItem), "Validated directed edges between declared containers.")
	Attribute("agent_policies", ArrayOf(AgentPolicyItem), "Evaluated Goa-AI runtime policies per declared agent.")
	Attribute("tools", ArrayOf(ToolContractItem), "Evaluated Goa-AI tool contracts.")
	Attribute("swarm", SwarmPlanItem, "Proposed Chorus multi-agent role assignments and model-family routing.")
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
		"swarm",
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

	Method("golden_path", func() {
		Description("Serve the Scantle Golden Path architecture playbook for Goa-AI agentic platforms.")
		Result(String)
		Resource("golden_path", "scantle://golden-path", "text/markdown")
		HTTP(func() {
			GET("/golden-path")
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
		Description("Confirm or refine a Checkpoint 1 Change Card with the complete ambiguity answers and example table, bound to its current request_revision. Changed answers or examples produce a new request_revision; unchanged ones return the card as it is.")
		Payload(ConfirmIntentPayload)
		Result(ChangeCardResult)
		Tool("confirm_intent", "Confirm or refine Checkpoint 1 ambiguity answers and example checks for the card's current request_revision.", func() {
			ToolUI("ui://scantle/checkpoint")
		})
		HTTP(func() {
			POST("/changes/confirm")
			Response(StatusOK)
		})
	})

	Method("get_change", func() {
		Description("Return the current Change Card, including its request_revision, to its owner, for example after confirm_intent refused an edit made against an older revision. Reading never confirms or changes the card.")
		Payload(GetChangePayload)
		Result(ChangeCardResult)
		Tool("get_change", "Read the current Checkpoint 1 Change Card and its request_revision.", func() {
			ToolUI("ui://scantle/checkpoint")
		})
		HTTP(func() {
			GET("/changes/{change_id}")
			Response(StatusOK)
		})
	})

	Method("preview_architecture", func() {
		Description("Extract and validate the Checkpoint 2 C4 container topology, tool contracts, Goa-AI guardrails, and proposed Chorus swarm roster from a candidate Goa design bound to a confirmed Change Card.")
		Payload(PreviewArchitecturePayload)
		Result(ArchitecturePreviewResult)
		Tool("preview_architecture", "Preview Checkpoint 2 C4 container topology, Goa-AI guardrails, and proposed Chorus swarm roster for a confirmed Change Card.", func() {
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
		Tool("diff", "Compute structural Goa design and go.mod dependency diffs.", func() {
			ToolUI("ui://scantle/checkpoint")
		})
		HTTP(func() {
			POST("/diff")
			Response(StatusOK)
		})
	})
})
