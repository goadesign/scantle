// Package controlplane implements the Scantle MCP software factory control
// plane across Checkpoints 1 through 3 (CP1–CP3): authenticated intent &
// ambiguity resolution, evaluated C4 architecture & cost/safety guardrail
// preview, and target-bound Fast Draft Mode prompt overrides.
package controlplane

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"

	genscantlehttp "github.com/goadesign/scantle/gen/http/scantle/server"
	genmcpscantle "github.com/goadesign/scantle/gen/jsonrpc/mcp_scantle/server"
	genmcpadapter "github.com/goadesign/scantle/gen/mcp_scantle"
	genscantle "github.com/goadesign/scantle/gen/scantle"
	"github.com/goadesign/scantle/internal/designeval"
	"github.com/goadesign/scantle/internal/diff"
	"github.com/goadesign/scantle/internal/goldenpath"
	"github.com/goadesign/scantle/internal/swarm"
	"goa.design/goa-ai/runtime/agent/model"
	goahttp "goa.design/goa/v3/http"
)

type (
	// Principal identifies the authenticated operator invoking the Scantle control plane.
	Principal struct {
		// ID is the stable authenticated operator identifier (for example, "alice@example.com").
		ID string
	}

	// RequestAuthenticator verifies an incoming HTTP request and returns the
	// authenticated Principal.
	RequestAuthenticator func(r *http.Request) (Principal, error)

	// principalContextKey is the context key storing the authenticated Principal.
	principalContextKey struct{}

	// CP1Analyzer performs semantic Checkpoint 1 analysis of an operator feature
	// request against the current Goa design source.
	CP1Analyzer interface {
		// Analyze inspects the prompt and design source and returns structured
		// summary, open domain questions, concrete examples, and preserved
		// behaviors.
		Analyze(ctx context.Context, promptText, designSource string) (CP1SemanticAnalysis, error)
	}

	// CP1ModelAmbiguity is an open domain question returned by a CP1Analyzer
	// model call before Scantle assigns its mechanical question ID.
	CP1ModelAmbiguity struct {
		// Question is the plain-English ambiguity question.
		Question string `json:"question"`
		// Options lists the candidate answers (at least 2 distinct options).
		Options []string `json:"options"`
	}

	// CP1ModelExample is a concrete input/outcome example returned by a
	// CP1Analyzer model call before Scantle assigns its mechanical check ID.
	CP1ModelExample struct {
		// Scenario is the plain-English scenario description.
		Scenario string `json:"scenario"`
		// InputSummary describes the representative user request and fixture state.
		InputSummary string `json:"input_summary"`
		// ExpectedOutcome describes the expected side effect and response behavior.
		ExpectedOutcome string `json:"expected_outcome"`
		// VerificationKind classifies the check ("deterministic" or "model_judged").
		VerificationKind string `json:"verification_kind"`
	}

	// CP1SemanticAnalysis is the structured semantic output returned by a
	// CP1Analyzer model call.
	CP1SemanticAnalysis struct {
		// Summary is the plain-English description of the requested behavior.
		Summary string `json:"summary"`
		// Ambiguities lists genuine domain questions that require human confirmation.
		Ambiguities []CP1ModelAmbiguity `json:"ambiguities"`
		// Examples lists concrete input/outcome examples for the Change Card.
		Examples []CP1ModelExample `json:"examples"`
		// PreservedBehaviors lists existing contract invariants preserved by the change.
		PreservedBehaviors []string `json:"preserved_behaviors"`
	}

	// ModelCP1Analyzer implements CP1Analyzer using a Goa-AI model.Client with
	// schema-validated structured JSON output.
	ModelCP1Analyzer struct {
		// client is the underlying Goa-AI model.Client.
		client model.Client
	}

	// TargetPromptUpdater applies a Checkpoint 3 Fast Draft Mode prompt template
	// override to the bound target platform's runtime prompt store.
	TargetPromptUpdater interface {
		// ApplyPromptOverride updates promptID with newContent on the bound target
		// platform and returns the resulting prompt version identifier.
		ApplyPromptOverride(ctx context.Context, promptID, newContent string) (version string, err error)
	}

	// Options configures a ControlPlane instance.
	Options struct {
		// Analyzer performs CP1 semantic analysis via a model call.
		Analyzer CP1Analyzer
		// TargetUpdater applies CP3 Fast Draft Mode prompt overrides to the bound target platform.
		TargetUpdater TargetPromptUpdater
		// SwarmProfiles lists server-configured coding-agent provider profiles for
		// proposing Chorus multi-agent role assignments at CP2.
		SwarmProfiles []swarm.ProviderProfile
	}

	// storedChangeCard holds a versioned Change Card alongside its authenticated
	// owner principal and original design source snapshot.
	storedChangeCard struct {
		// card is the latest versioned ChangeCardResult.
		card *genscantle.ChangeCardResult
		// owner is the authenticated Principal.ID that created the card.
		owner string
		// designSource is the Goa design source snapshot inspected at CP1.
		designSource string
	}

	// canonicalAnswerPair is a deterministic key/value entry for JSON hashing.
	canonicalAnswerPair struct {
		// ID is the ambiguity question ID.
		ID string `json:"id"`
		// Answer is the selected option value.
		Answer string `json:"answer"`
	}

	// canonicalCardRevision is the canonical JSON structure hashed to produce
	// RequestRevision without delimiter ambiguity.
	canonicalCardRevision struct {
		// ChangeID is the Change Card identifier.
		ChangeID string `json:"change_id"`
		// Owner is the authenticated creator principal ID.
		Owner string `json:"owner"`
		// Status is the Change Card status.
		Status string `json:"status"`
		// Prompt is the original operator feature request.
		Prompt string `json:"prompt"`
		// DesignSource is the Goa design source inspected at CP1.
		DesignSource string `json:"design_source"`
		// Summary is the plain-English summary.
		Summary string `json:"summary"`
		// Ambiguities is the ordered slice of surfaced domain questions.
		Ambiguities []*genscantle.AmbiguityQuestion `json:"ambiguities"`
		// Answers is the key-sorted slice of confirmed ambiguity answers.
		Answers []canonicalAnswerPair `json:"answers"`
		// Examples is the ordered slice of confirmed example checks.
		Examples []*genscantle.ExampleItem `json:"examples"`
		// PreservedBehaviors is the ordered slice of preserved contract invariants.
		PreservedBehaviors []string `json:"preserved_behaviors"`
		// ConfirmedBy is the authenticated principal ID that confirmed the card.
		ConfirmedBy string `json:"confirmed_by"`
	}

	// ControlPlane implements genscantle.Service for Checkpoints 1 through 3.
	ControlPlane struct {
		// mu guards cards.
		mu sync.RWMutex
		// cards stores pending and confirmed CP1 Change Cards by change_id.
		cards map[string]storedChangeCard
		// analyzer performs semantic CP1 analysis.
		analyzer CP1Analyzer
		// targetUpdater applies CP3 Fast Draft Mode prompt overrides to the target platform.
		targetUpdater TargetPromptUpdater
		// swarmProfiles stores server-configured coding-agent provider profiles.
		swarmProfiles []swarm.ProviderProfile
	}
)

const (
	// StatusNeedsConfirmation indicates CP1 halted on genuine domain ambiguity.
	StatusNeedsConfirmation = "needs_confirmation"
	// StatusConfirmed indicates CP1 is locked and ready for codegen/verification.
	StatusConfirmed = "confirmed"
	// TargetKindPromptTemplate identifies a 0-second target prompt override in CP3.
	TargetKindPromptTemplate = "prompt_template"
	// TargetKindCompiledRule identifies a compiled Go business rule that cannot be overridden via prompt store.
	TargetKindCompiledRule = "compiled_rule"
	// cp1AnalysisMaxOutputTokens is the output allowance for the one CP1
	// inference that writes the structured Change Card analysis. It caps that
	// response only, not the cost of a whole change. Providers such as
	// Anthropic require a positive allowance on every request. A response the
	// provider marks OutputLimited fails Analyze even when its JSON parses, so
	// no card is stored from an analysis that may be incomplete.
	cp1AnalysisMaxOutputTokens = 4096
	// checkpointUIHTML is the stateless MCP Apps (`ui://scantle/checkpoint`,
	// `text/html;profile=mcp-app`) HTML document host. It performs the MCP Apps
	// JSON-RPC 2.0 initialization handshake (`ui/initialize` ->
	// `ui/notifications/initialized`) with `window.parent` and renders validated
	// CP1 ChangeCardResult, CP2 ArchitecturePreviewResult, and CP3
	// DraftOverrideResult payloads delivered via `ui/notifications/tool-result`
	// using safe DOM construction (`createElement` and `textContent`).
	checkpointUIHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <title>Scantle Checkpoint Host</title>
  <style>
    :root {
      color-scheme: light dark;
      font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
      line-height: 1.45;
    }
    body {
      margin: 0;
      padding: 1rem 1.25rem;
    }
    header {
      border-bottom: 1px solid rgba(127, 127, 127, 0.35);
      padding-bottom: 0.75rem;
      margin-bottom: 1rem;
    }
    h1 {
      font-size: 1.25rem;
      margin: 0 0 0.35rem 0;
    }
    h2 {
      font-size: 1.05rem;
      margin: 0 0 0.5rem 0;
    }
    h3 {
      font-size: 0.95rem;
      margin: 0.85rem 0 0.35rem 0;
    }
    p {
      margin: 0.25rem 0;
    }
    code, pre {
      font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
      font-size: 0.85rem;
    }
    code {
      overflow-wrap: anywhere;
      word-break: break-word;
    }
    pre {
      padding: 0.65rem;
      border-radius: 6px;
      border: 1px solid rgba(127, 127, 127, 0.35);
      overflow-x: auto;
      white-space: pre-wrap;
      overflow-wrap: anywhere;
    }
    .scantle-meta, .scantle-card {
      border: 1px solid rgba(127, 127, 127, 0.35);
      border-radius: 6px;
      padding: 0.75rem;
      margin-bottom: 0.75rem;
      overflow-wrap: anywhere;
    }
    .scantle-badge {
      display: inline-block;
      padding: 0.1rem 0.45rem;
      border-radius: 4px;
      border: 1px solid rgba(127, 127, 127, 0.5);
      font-size: 0.8rem;
      font-weight: 600;
      margin-right: 0.4rem;
    }
    .scantle-error {
      border: 2px solid #c62828;
      border-radius: 6px;
      padding: 0.75rem;
      margin-top: 0.5rem;
    }
    ul {
      margin: 0.35rem 0;
      padding-left: 1.25rem;
    }
    li {
      margin-bottom: 0.25rem;
    }
  </style>
</head>
<body>
  <main id="scantle-checkpoint-root" data-profile="mcp-app" data-state="waiting">
    <header>
      <h1>Scantle Checkpoint Viewer</h1>
      <p>Displays validated CP1 Change Cards, CP2 C4 Architecture Previews, and CP3 Fast Draft Override results from structured MCP tool outputs.</p>
      <p><small>Visual inspection does not grant approval authority; invoke <code>confirm_intent</code>, <code>preview_architecture</code>, <code>draft_override</code>, or <code>scantle verify</code> through your MCP host or CLI to advance the workflow.</small></p>
    </header>
    <p id="scantle-checkpoint-status" role="status" aria-live="polite">Awaiting MCP Apps host initialization and checkpoint tool result...</p>
    <section id="scantle-checkpoint-view" aria-label="Checkpoint Payload"></section>
  </main>
  <script>
  (function () {
    "use strict";

    var INIT_REQUEST_ID = 1;
    var PROTOCOL_VERSION = "2026-01-26";
    var initPending = false;
    var root = document.getElementById("scantle-checkpoint-root");
    var statusEl = document.getElementById("scantle-checkpoint-status");
    var viewEl = document.getElementById("scantle-checkpoint-view");

    function isPlainObject(v) {
      return Boolean(v) && typeof v === "object" && !Array.isArray(v);
    }

    function isStringArray(v) {
      return Array.isArray(v) && v.every(function (item) {
        return typeof item === "string";
      });
    }

    function isStringMap(v) {
      if (!isPlainObject(v)) {
        return false;
      }
      return Object.keys(v).every(function (k) {
        return typeof v[k] === "string";
      });
    }

    function el(tag, attrs, children) {
      var node = document.createElement(tag);
      if (isPlainObject(attrs)) {
        Object.keys(attrs).forEach(function (k) {
          if (k === "className") {
            node.className = attrs[k];
          } else if (k === "textContent") {
            node.textContent = String(attrs[k]);
          } else {
            node.setAttribute(k, String(attrs[k]));
          }
        });
      }
      if (Array.isArray(children)) {
        children.forEach(function (child) {
          if (child === null || child === undefined) {
            return;
          }
          if (typeof child === "string" || typeof child === "number" || typeof child === "boolean") {
            node.appendChild(document.createTextNode(String(child)));
          } else {
            node.appendChild(child);
          }
        });
      }
      return node;
    }

    function clearDatasetKeys() {
      delete root.dataset.changeId;
      delete root.dataset.requestRevision;
      delete root.dataset.designDigest;
    }

    function replaceChildren(container, nodes) {
      while (container.firstChild) {
        container.removeChild(container.firstChild);
      }
      nodes.forEach(function (n) {
        if (n) {
          container.appendChild(n);
        }
      });
    }

    function renderWaiting(message) {
      clearDatasetKeys();
      root.dataset.state = "waiting";
      statusEl.textContent = message;
      replaceChildren(viewEl, []);
    }

    function renderCancelled(reason) {
      clearDatasetKeys();
      root.dataset.state = "cancelled";
      statusEl.textContent = "Checkpoint tool call cancelled";
      var detail = typeof reason === "string" && reason.length > 0
        ? "Tool call was cancelled (" + reason + "). Re-run the Scantle checkpoint tool to inspect a fresh result."
        : "Tool call was cancelled before completion. Re-run the Scantle checkpoint tool to inspect a fresh result.";
      replaceChildren(viewEl, [
        el("div", { className: "scantle-meta", role: "status" }, [
          el("strong", { textContent: "Cancelled: " }),
          detail
        ])
      ]);
    }

    function renderError(message) {
      clearDatasetKeys();
      root.dataset.state = "error";
      statusEl.textContent = "Checkpoint payload error";
      replaceChildren(viewEl, [
        el("div", { className: "scantle-error", role: "alert" }, [
          el("strong", { textContent: "Error: " }),
          String(message)
        ])
      ]);
    }

    function isValidAmbiguity(a) {
      return isPlainObject(a) &&
        typeof a.id === "string" &&
        typeof a.question === "string" &&
        isStringArray(a.options);
    }

    function isValidExample(ex) {
      return isPlainObject(ex) &&
        typeof ex.id === "string" &&
        typeof ex.scenario === "string" &&
        typeof ex.input_summary === "string" &&
        typeof ex.expected_outcome === "string" &&
        (ex.verification_kind === "deterministic" || ex.verification_kind === "model_judged");
    }

    function isValidChangeCard(d) {
      return isPlainObject(d) &&
        typeof d.change_id === "string" &&
        (d.status === "needs_confirmation" || d.status === "confirmed") &&
        typeof d.prompt === "string" &&
        typeof d.summary === "string" &&
        typeof d.request_revision === "string" &&
        Array.isArray(d.examples) && d.examples.every(isValidExample) &&
        isStringArray(d.preserved_behaviors) &&
        (d.ambiguities === undefined || (Array.isArray(d.ambiguities) && d.ambiguities.every(isValidAmbiguity))) &&
        (d.answers === undefined || isStringMap(d.answers)) &&
        (d.confirmed_by === undefined || typeof d.confirmed_by === "string");
    }

    function isValidContainer(c) {
      return isPlainObject(c) &&
        typeof c.id === "string" &&
        typeof c.name === "string" &&
        typeof c.kind === "string" &&
        typeof c.description === "string";
    }

    function isValidEdge(e) {
      return isPlainObject(e) &&
        typeof e.from_id === "string" &&
        typeof e.to_id === "string" &&
        typeof e.protocol === "string";
    }

    function isValidAgentPolicy(p) {
      return isPlainObject(p) &&
        typeof p.agent_id === "string" &&
        typeof p.max_execution_seconds === "number" &&
        typeof p.max_tool_calls === "number" &&
        typeof p.max_recovery_turns === "number" &&
        typeof p.compress_at_turns === "number" &&
        typeof p.keep_recent_turns === "number" &&
        typeof p.cache_after_system === "boolean" &&
        typeof p.cache_after_tools === "boolean";
    }

    function isValidTool(t) {
      return isPlainObject(t) &&
        typeof t.id === "string" &&
        typeof t.toolset === "string" &&
        typeof t.name === "string" &&
        typeof t.description === "string" &&
        typeof t.args_schema === "string" &&
        typeof t.return_schema === "string" &&
        isStringArray(t.injected_fields) &&
        typeof t.requires_ui === "boolean" &&
        typeof t.requires_confirmation === "boolean" &&
        typeof t.bounded_result === "boolean";
    }

    function isValidSwarmRole(r) {
      return isPlainObject(r) &&
        typeof r.handle === "string" &&
        typeof r.role === "string" &&
        typeof r.scope_name === "string" &&
        typeof r.profile_id === "string" &&
        typeof r.provider === "string" &&
        typeof r.model === "string" &&
        typeof r.family === "string" &&
        isStringArray(r.owned_paths) &&
        typeof r.access_mode === "string";
    }

    function isValidSwarm(s) {
      return isPlainObject(s) &&
        (s.status === "configured" || s.status === "unconfigured" || s.status === "missing_capability") &&
        Array.isArray(s.assignments) && s.assignments.every(isValidSwarmRole) &&
        typeof s.cross_family_review === "boolean" &&
        typeof s.cross_family_eval === "boolean" &&
        typeof s.single_family === "boolean" &&
        typeof s.summary === "string" &&
        (s.missing_roles === undefined || isStringArray(s.missing_roles)) &&
        (s.manifest === undefined || typeof s.manifest === "string");
    }

    function isValidArchitecturePreview(d) {
      return isPlainObject(d) &&
        typeof d.change_id === "string" &&
        typeof d.request_revision === "string" &&
        typeof d.design_digest === "string" &&
        typeof d.system_name === "string" &&
        typeof d.banner === "string" &&
        Array.isArray(d.containers) && d.containers.every(isValidContainer) &&
        Array.isArray(d.edges) && d.edges.every(isValidEdge) &&
        Array.isArray(d.agent_policies) && d.agent_policies.every(isValidAgentPolicy) &&
        Array.isArray(d.tools) && d.tools.every(isValidTool) &&
        isValidSwarm(d.swarm);
    }

    function isValidDraftOverride(d) {
      return isPlainObject(d) &&
        typeof d.applied_in_store === "boolean" &&
        typeof d.requires_rebuild === "boolean" &&
        typeof d.message === "string" &&
        (d.prompt_version === undefined || typeof d.prompt_version === "string");
    }

    function renderChangeCard(card) {
      clearDatasetKeys();
      root.dataset.state = "cp1-change-card";
      root.dataset.changeId = card.change_id;
      root.dataset.requestRevision = card.request_revision;
      statusEl.textContent = "Rendered Checkpoint 1 Change Card (" + card.status + ")";

      var metaRows = [
        el("h2", { textContent: "CP1 Change Card: " + card.change_id }),
        el("p", {}, [
          el("span", { className: "scantle-badge", textContent: card.status }),
          "request_revision: ",
          el("code", { textContent: card.request_revision })
        ]),
        el("p", {}, [el("strong", { textContent: "Summary: " }), card.summary]),
        el("p", {}, [el("strong", { textContent: "Prompt: " }), card.prompt])
      ];
      if (card.confirmed_by) {
        metaRows.push(el("p", {}, [el("strong", { textContent: "Confirmed by: " }), card.confirmed_by]));
      }

      var ambiguities = Array.isArray(card.ambiguities) ? card.ambiguities : [];
      var answers = isPlainObject(card.answers) ? card.answers : {};
      var ambItems = ambiguities.map(function (amb) {
        var answerText = answers[amb.id] ? " [Confirmed answer: " + answers[amb.id] + "]" : "";
        return el("li", {}, [
          el("code", { textContent: amb.id }),
          ": ",
          amb.question,
          " (options: " + amb.options.join(", ") + ")",
          answerText
        ]);
      });

      var exampleItems = card.examples.map(function (ex) {
        return el("li", {}, [
          el("code", { textContent: ex.id }),
          " [",
          ex.verification_kind,
          "] ",
          el("strong", { textContent: ex.scenario }),
          " — Input: ",
          ex.input_summary,
          " -> Expected: ",
          ex.expected_outcome
        ]);
      });

      var preservedItems = card.preserved_behaviors.map(function (pb) {
        return el("li", { textContent: pb });
      });

      replaceChildren(viewEl, [
        el("div", { className: "scantle-meta" }, metaRows),
        el("div", { className: "scantle-card" }, [
          el("h3", { textContent: "Ambiguities (" + ambiguities.length + ")" }),
          ambItems.length > 0 ? el("ul", {}, ambItems) : el("p", { textContent: "No open domain ambiguities." }),
          el("h3", { textContent: "Concrete Examples (" + card.examples.length + ")" }),
          el("ul", {}, exampleItems),
          el("h3", { textContent: "Preserved Contract Invariants (" + card.preserved_behaviors.length + ")" }),
          preservedItems.length > 0 ? el("ul", {}, preservedItems) : el("p", { textContent: "None declared." })
        ])
      ]);
    }

    function renderArchitecturePreview(preview) {
      clearDatasetKeys();
      root.dataset.state = "cp2-architecture-preview";
      root.dataset.changeId = preview.change_id;
      root.dataset.requestRevision = preview.request_revision;
      root.dataset.designDigest = preview.design_digest;
      statusEl.textContent = "Rendered Checkpoint 2 Architecture Preview (" + preview.system_name + ")";

      var containerItems = preview.containers.map(function (c) {
        return el("li", {}, [
          el("span", { className: "scantle-badge", textContent: c.kind }),
          el("code", { textContent: c.id }),
          " (",
          c.name,
          "): ",
          c.description
        ]);
      });

      var edgeItems = preview.edges.map(function (e) {
        return el("li", {}, [
          el("code", { textContent: e.from_id }),
          " -> ",
          el("code", { textContent: e.to_id }),
          " [",
          e.protocol,
          "]"
        ]);
      });

      var policyItems = preview.agent_policies.map(function (p) {
        return el("li", {}, [
          el("code", { textContent: p.agent_id }),
          ": time_budget=" + p.max_execution_seconds + "s, max_tool_calls=" + p.max_tool_calls +
          ", max_recovery_turns=" + p.max_recovery_turns +
          ", history=" + p.compress_at_turns + "/" + p.keep_recent_turns +
          ", cache_after_system=" + p.cache_after_system +
          ", cache_after_tools=" + p.cache_after_tools
        ]);
      });

      var toolItems = preview.tools.map(function (t) {
        var badges = [];
        if (t.injected_fields.length > 0) {
          badges.push(el("span", { className: "scantle-badge", textContent: "Inject: " + t.injected_fields.join(", ") }));
        }
        if (t.requires_confirmation) {
          badges.push(el("span", { className: "scantle-badge", textContent: "Confirmation" }));
        }
        if (t.bounded_result) {
          badges.push(el("span", { className: "scantle-badge", textContent: "BoundedResult" }));
        }
        if (t.requires_ui) {
          badges.push(el("span", { className: "scantle-badge", textContent: "UI required" }));
        }
        return el("li", {}, [
          el("code", { textContent: t.id }),
          " (",
          t.toolset,
          "): ",
          t.description,
          " "
        ].concat(badges));
      });

      var swarmRoleItems = preview.swarm.assignments.map(function (r) {
        return el("li", {}, [
          el("code", { textContent: "@" + r.handle }),
          " [",
          r.role,
          " / ",
          r.scope_name,
          "] -> ",
          r.provider + ":" + r.model + " (" + r.family + "), access=" + r.access_mode +
          ", paths=" + r.owned_paths.join(", ")
        ]);
      });

      var swarmNodes = [
        el("h3", { textContent: "Proposed Chorus Swarm (" + preview.swarm.status + ")" }),
        el("p", { textContent: preview.swarm.summary }),
        el("p", {}, [
          el("span", { className: "scantle-badge", textContent: "cross_family_review=" + preview.swarm.cross_family_review }),
          el("span", { className: "scantle-badge", textContent: "cross_family_eval=" + preview.swarm.cross_family_eval }),
          el("span", { className: "scantle-badge", textContent: "single_family=" + preview.swarm.single_family })
        ])
      ];
      if (swarmRoleItems.length > 0) {
        swarmNodes.push(el("ul", {}, swarmRoleItems));
      }
      if (preview.swarm.manifest) {
        swarmNodes.push(el("pre", { textContent: preview.swarm.manifest }));
      }

      replaceChildren(viewEl, [
        el("div", { className: "scantle-meta" }, [
          el("h2", { textContent: "CP2 Architecture Preview: " + preview.system_name }),
          el("p", { textContent: preview.banner }),
          el("p", {}, ["change_id: ", el("code", { textContent: preview.change_id })]),
          el("p", {}, ["request_revision: ", el("code", { textContent: preview.request_revision })]),
          el("p", {}, ["design_digest: ", el("code", { textContent: preview.design_digest })])
        ]),
        el("div", { className: "scantle-card" }, [
          el("h3", { textContent: "C4 Containers (" + preview.containers.length + ")" }),
          el("ul", {}, containerItems),
          el("h3", { textContent: "Directed Contract Edges (" + preview.edges.length + ")" }),
          el("ul", {}, edgeItems),
          el("h3", { textContent: "Evaluated Agent Policies (" + preview.agent_policies.length + ")" }),
          el("ul", {}, policyItems),
          el("h3", { textContent: "Evaluated Tool Guardrails (" + preview.tools.length + ")" }),
          el("ul", {}, toolItems)
        ]),
        el("div", { className: "scantle-card" }, swarmNodes)
      ]);
    }

    function renderDraftOverride(draft) {
      clearDatasetKeys();
      root.dataset.state = "cp3-draft-override";
      statusEl.textContent = "Rendered Checkpoint 3 Fast Draft Override";

      var rows = [
        el("h2", { textContent: "CP3 Fast Draft Mode" }),
        el("p", {}, [
          el("span", { className: "scantle-badge", textContent: "applied_in_store=" + draft.applied_in_store }),
          el("span", { className: "scantle-badge", textContent: "requires_rebuild=" + draft.requires_rebuild })
        ]),
        el("p", { textContent: draft.message })
      ];
      if (draft.prompt_version) {
        rows.push(el("p", {}, ["prompt_version: ", el("code", { textContent: draft.prompt_version })]));
      }
      replaceChildren(viewEl, [el("div", { className: "scantle-meta" }, rows)]);
    }

    function extractPayloadFromToolResult(params) {
      if (isPlainObject(params.structuredContent)) {
        return { ok: true, value: params.structuredContent };
      }
      if (Array.isArray(params.content)) {
        for (var i = 0; i < params.content.length; i++) {
          var item = params.content[i];
          if (isPlainObject(item) && item.type === "text" && typeof item.text === "string") {
            try {
              var parsed = JSON.parse(item.text);
              if (isPlainObject(parsed)) {
                return { ok: true, value: parsed };
              }
            } catch (err) {
              return { ok: false, error: "Invalid JSON in tool-result text content: " + err.message };
            }
          }
        }
      }
      return { ok: false, error: "Tool result is missing structuredContent object." };
    }

    function extractErrorText(params) {
      if (Array.isArray(params.content)) {
        for (var i = 0; i < params.content.length; i++) {
          var item = params.content[i];
          if (isPlainObject(item) && item.type === "text" && typeof item.text === "string" && item.text.length > 0) {
            return item.text;
          }
        }
      }
      return "Tool execution failed.";
    }

    function handleToolResult(params) {
      if (!isPlainObject(params)) {
        renderError("Malformed ui/notifications/tool-result: params must be an object.");
        return;
      }
      if (params.isError === true) {
        renderError(extractErrorText(params));
        return;
      }
      var extracted = extractPayloadFromToolResult(params);
      if (!extracted.ok) {
        renderError(extracted.error);
        return;
      }
      var payload = extracted.value;
      if (isValidArchitecturePreview(payload)) {
        renderArchitecturePreview(payload);
        return;
      }
      if (isValidChangeCard(payload)) {
        renderChangeCard(payload);
        return;
      }
      if (isValidDraftOverride(payload)) {
        renderDraftOverride(payload);
        return;
      }
      renderError("Unsupported or malformed Scantle checkpoint payload.");
    }

    function handleMessage(event) {
      if (!event || event.source !== window.parent) {
        return;
      }
      var msg = event.data;
      if (!isPlainObject(msg) || msg.jsonrpc !== "2.0") {
        return;
      }
      if (typeof msg.method === "string") {
        if (msg.id !== undefined && msg.id !== null) {
          if (msg.method === "ui/resource-teardown") {
            clearDatasetKeys();
            root.dataset.state = "terminated";
            statusEl.textContent = "Checkpoint resource torn down.";
            replaceChildren(viewEl, []);
            window.parent.postMessage({
              jsonrpc: "2.0",
              id: msg.id,
              result: {}
            }, "*");
            return;
          }
          if (msg.method === "ping") {
            window.parent.postMessage({
              jsonrpc: "2.0",
              id: msg.id,
              result: {}
            }, "*");
            return;
          }
          window.parent.postMessage({
            jsonrpc: "2.0",
            id: msg.id,
            error: {
              code: -32601,
              message: "Method not found: " + msg.method
            }
          }, "*");
          return;
        }
        if (msg.method === "ui/notifications/tool-input") {
          if (!isPlainObject(msg.params)) {
            renderError("Malformed ui/notifications/tool-input notification.");
            return;
          }
          renderWaiting("Received tool input; awaiting validated checkpoint result...");
          return;
        }
        if (msg.method === "ui/notifications/tool-cancelled") {
          if (msg.params !== undefined && !isPlainObject(msg.params)) {
            renderError("Malformed ui/notifications/tool-cancelled notification.");
            return;
          }
          var cancelReason = isPlainObject(msg.params) && typeof msg.params.reason === "string"
            ? msg.params.reason
            : "";
          renderCancelled(cancelReason);
          return;
        }
        if (msg.method === "ui/notifications/tool-result") {
          handleToolResult(msg.params);
        }
        return;
      }
      if (initPending && msg.id === INIT_REQUEST_ID) {
        initPending = false;
        if (msg.error) {
          var errMsg = isPlainObject(msg.error) && typeof msg.error.message === "string"
            ? msg.error.message
            : "MCP Apps host rejected ui/initialize.";
          renderError(errMsg);
          return;
        }
        if (!isPlainObject(msg.result)) {
          renderError("MCP Apps host returned invalid ui/initialize result.");
          return;
        }
        root.dataset.initialized = "true";
        statusEl.textContent = "MCP Apps handshake complete; awaiting checkpoint tool result...";
        window.parent.postMessage({
          jsonrpc: "2.0",
          method: "ui/notifications/initialized",
          params: {}
        }, "*");
      }
    }

    window.addEventListener("message", handleMessage);

    if (window.parent && window.parent !== window) {
      initPending = true;
      window.parent.postMessage({
        jsonrpc: "2.0",
        id: INIT_REQUEST_ID,
        method: "ui/initialize",
        params: {
          protocolVersion: PROTOCOL_VERSION,
          appInfo: {
            name: "scantle-checkpoint",
            version: "1.0.0"
          },
          appCapabilities: {}
        }
      }, "*");
    }
  })();
  </script>
</body>
</html>`
)

var (
	// cp1AnalysisJSONSchema is the canonical JSON Schema (Draft 2020-12) enforced
	// at the goa-ai model.Client boundary for CP1 semantic analysis.
	cp1AnalysisJSONSchema = []byte(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "additionalProperties": false,
  "required": ["summary", "ambiguities", "examples", "preserved_behaviors"],
  "properties": {
    "summary": {
      "type": "string",
      "minLength": 1
    },
    "ambiguities": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["question", "options"],
        "properties": {
          "question": {
            "type": "string",
            "minLength": 1
          },
          "options": {
            "type": "array",
            "minItems": 2,
            "items": {
              "type": "string",
              "minLength": 1
            }
          }
        }
      }
    },
    "examples": {
      "type": "array",
      "minItems": 1,
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["scenario", "input_summary", "expected_outcome", "verification_kind"],
        "properties": {
          "scenario": {
            "type": "string",
            "minLength": 1
          },
          "input_summary": {
            "type": "string",
            "minLength": 1
          },
          "expected_outcome": {
            "type": "string",
            "minLength": 1
          },
          "verification_kind": {
            "type": "string",
            "enum": ["deterministic", "model_judged"]
          }
        }
      }
    },
    "preserved_behaviors": {
      "type": "array",
      "items": {
        "type": "string",
        "minLength": 1
      }
    }
  }
}`)
)

// WithPrincipal returns a child context carrying the authenticated Principal.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, p)
}

// PrincipalFromContext extracts the authenticated Principal from ctx.
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	if ctx == nil {
		return Principal{}, false
	}
	p, ok := ctx.Value(principalContextKey{}).(Principal)
	if !ok || strings.TrimSpace(p.ID) == "" {
		return Principal{}, false
	}
	return Principal{ID: strings.TrimSpace(p.ID)}, true
}

// NewTokenAuthenticator returns a RequestAuthenticator that validates
// `Authorization: Bearer <token>` headers in constant time against the
// configured token-to-principal map.
func NewTokenAuthenticator(tokens map[string]Principal) RequestAuthenticator {
	cloned := maps.Clone(tokens)
	return func(r *http.Request) (Principal, error) {
		if r == nil {
			return Principal{}, errors.New("missing http request")
		}
		raw := strings.TrimSpace(r.Header.Get("Authorization"))
		token, ok := strings.CutPrefix(raw, "Bearer ")
		if !ok || strings.TrimSpace(token) == "" {
			return Principal{}, errors.New("missing bearer token")
		}
		token = strings.TrimSpace(token)
		for expected, principal := range cloned {
			if subtle.ConstantTimeCompare([]byte(token), []byte(expected)) == 1 {
				if strings.TrimSpace(principal.ID) == "" {
					return Principal{}, errors.New("empty principal configured for token")
				}
				return Principal{ID: strings.TrimSpace(principal.ID)}, nil
			}
		}
		return Principal{}, errors.New("invalid bearer token")
	}
}

// New constructs a ControlPlane with the provided Options.
func New(opts Options) *ControlPlane {
	return &ControlPlane{
		cards:         make(map[string]storedChangeCard),
		analyzer:      opts.Analyzer,
		targetUpdater: opts.TargetUpdater,
		swarmProfiles: opts.SwarmProfiles,
	}
}

// NewHTTPHandler mounts both the Goa HTTP endpoints and the MCP JSON-RPC
// server (`/mcp`) for svc onto a single HTTP handler protected by auth.
func NewHTTPHandler(svc genscantle.Service, auth RequestAuthenticator) http.Handler {
	mux := goahttp.NewMuxer()
	endpoints := genscantle.NewEndpoints(svc)
	httpServer := genscantlehttp.New(
		endpoints,
		mux,
		goahttp.RequestDecoder,
		goahttp.ResponseEncoder,
		nil,
		nil,
	)
	genscantlehttp.Mount(mux, httpServer)

	mcpAdapter := genmcpadapter.NewMCPAdapter(endpoints, nil)
	mcpEndpoints := genmcpadapter.NewEndpoints(mcpAdapter)
	mcpServer := genmcpscantle.New(
		mcpEndpoints,
		mux,
		goahttp.RequestDecoder,
		goahttp.ResponseEncoder,
		nil,
	)
	genmcpscantle.Mount(mux, mcpServer)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth != nil {
			principal, err := auth(r)
			if err != nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			r = r.WithContext(WithPrincipal(r.Context(), principal))
		}
		mux.ServeHTTP(w, r)
	})
}

// NewModelCP1Analyzer constructs a CP1Analyzer backed by a Goa-AI model.Client.
func NewModelCP1Analyzer(client model.Client) *ModelCP1Analyzer {
	return &ModelCP1Analyzer{client: client}
}

// Analyze invokes the underlying Goa-AI model.Client with a JSON Schema
// StructuredOutput contract for CP1 semantic analysis and strictly decodes the
// validated CP1SemanticAnalysis response.
func (a *ModelCP1Analyzer) Analyze(ctx context.Context, promptText, designSource string) (CP1SemanticAnalysis, error) {
	if a == nil || a.client == nil {
		return CP1SemanticAnalysis{}, errors.New("cp1 model client is required")
	}
	req := &model.Request{
		Messages: []*model.Message{
			{
				Role: model.ConversationRoleSystem,
				Parts: []model.Part{
					model.TextPart{
						Text: goldenpath.ArchitectSystemPrompt(),
					},
				},
			},
			{
				Role: model.ConversationRoleUser,
				Parts: []model.Part{
					model.TextPart{
						Text: fmt.Sprintf("Request:\n%s\n\nDesign:\n%s", promptText, designSource),
					},
				},
			},
		},
		StructuredOutput: &model.StructuredOutput{
			Name:   "cp1_change_card_analysis",
			Schema: cp1AnalysisJSONSchema,
		},
		MaxTokens: cp1AnalysisMaxOutputTokens,
	}
	resp, err := a.client.Complete(ctx, req)
	if err != nil {
		return CP1SemanticAnalysis{}, fmt.Errorf("cp1 model completion: %w", err)
	}
	if resp.OutputLimited {
		return CP1SemanticAnalysis{}, fmt.Errorf("cp1 model analysis may be incomplete: provider reported an output limit (stop reason %q)", resp.StopReason)
	}
	raw := extractAssistantText(resp)
	if raw == "" {
		return CP1SemanticAnalysis{}, errors.New("cp1 model returned empty analysis")
	}
	var parsed CP1SemanticAnalysis
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&parsed); err != nil {
		return CP1SemanticAnalysis{}, fmt.Errorf("decode cp1 model analysis: %w", err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return CP1SemanticAnalysis{}, errors.New("cp1 model analysis contains trailing JSON tokens")
	}
	return parsed, nil
}

// CheckpointUI returns the interactive MCP Apps HTML host for `ui://scantle/checkpoint`.
func (c *ControlPlane) CheckpointUI(_ context.Context) (string, error) {
	return checkpointUIHTML, nil
}

// GoldenPath returns the Scantle Golden Path architecture playbook Markdown
// for `scantle://golden-path`.
func (c *ControlPlane) GoldenPath(_ context.Context) (string, error) {
	return goldenpath.PlaybookMarkdown(), nil
}

// ProposeChange evaluates a feature request at Checkpoint 1 (CP1) via the
// configured CP1Analyzer model call, validates all model-emitted fields
// strictly, assigns code-owned ambiguity and example IDs, binds ownership to
// the authenticated principal, and stops for human confirmation only when
// genuine domain ambiguities are present.
func (c *ControlPlane) ProposeChange(ctx context.Context, p *genscantle.ProposeChangePayload) (*genscantle.ChangeCardResult, error) {
	principal, ok := PrincipalFromContext(ctx)
	if !ok {
		return nil, errors.New("authenticated principal is required")
	}
	if p == nil || strings.TrimSpace(p.ChangeID) == "" || strings.TrimSpace(p.Prompt) == "" {
		return nil, errors.New("change_id and prompt are required")
	}
	if c.analyzer == nil {
		return nil, errors.New("cp1 semantic analyzer is required")
	}
	var designSrc string
	if p.DesignSource != nil {
		designSrc = *p.DesignSource
	}

	c.mu.RLock()
	_, alreadyExists := c.cards[p.ChangeID]
	c.mu.RUnlock()
	if alreadyExists {
		return nil, fmt.Errorf("change_id %q already exists", p.ChangeID)
	}

	analysis, err := c.analyzer.Analyze(ctx, p.Prompt, designSrc)
	if err != nil {
		return nil, err
	}

	summary, ambiguities, examples, preserved, err := validateCP1Analysis(p.ChangeID, analysis)
	if err != nil {
		return nil, fmt.Errorf("invalid cp1 analysis: %w", err)
	}

	status := StatusConfirmed
	var confirmedBy *string
	if len(ambiguities) > 0 {
		status = StatusNeedsConfirmation
	} else {
		ownerCopy := principal.ID
		confirmedBy = &ownerCopy
	}

	card := &genscantle.ChangeCardResult{
		ChangeID:           p.ChangeID,
		Status:             status,
		Prompt:             p.Prompt,
		Summary:            summary,
		Ambiguities:        ambiguities,
		Examples:           examples,
		PreservedBehaviors: preserved,
		ConfirmedBy:        confirmedBy,
	}
	rev, err := computeCanonicalRevision(card, principal.ID, designSrc)
	if err != nil {
		return nil, err
	}
	card.RequestRevision = rev

	c.mu.Lock()
	if _, exists := c.cards[p.ChangeID]; exists {
		c.mu.Unlock()
		return nil, fmt.Errorf("change_id %q already exists", p.ChangeID)
	}
	c.cards[p.ChangeID] = storedChangeCard{
		card:         card,
		owner:        principal.ID,
		designSource: designSrc,
	}
	c.mu.Unlock()

	return card, nil
}

// ConfirmIntent verifies the authenticated owner and reviewed request_revision,
// resolves all open Checkpoint 1 ambiguity questions, records the confirmed
// example table and answers, and freezes the Change Card revision.
func (c *ControlPlane) ConfirmIntent(ctx context.Context, p *genscantle.ConfirmIntentPayload) (*genscantle.ChangeCardResult, error) {
	principal, ok := PrincipalFromContext(ctx)
	if !ok {
		return nil, errors.New("authenticated principal is required")
	}
	if p == nil || strings.TrimSpace(p.ChangeID) == "" || strings.TrimSpace(p.RequestRevision) == "" {
		return nil, errors.New("change_id and request_revision are required")
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	record, ok := c.cards[p.ChangeID]
	if !ok {
		return nil, fmt.Errorf("unknown change_id %q", p.ChangeID)
	}
	if record.owner != principal.ID {
		return nil, fmt.Errorf("principal %q is not the owner of change_id %q", principal.ID, p.ChangeID)
	}
	existing := record.card
	if existing.Status != StatusNeedsConfirmation {
		return nil, fmt.Errorf("change_id %q does not require confirmation (status %q)", p.ChangeID, existing.Status)
	}
	if existing.RequestRevision != p.RequestRevision {
		return nil, fmt.Errorf("request_revision mismatch for %q: got %q, want %q", p.ChangeID, p.RequestRevision, existing.RequestRevision)
	}

	if err := validateAmbiguityAnswers(existing.Ambiguities, p.Answers); err != nil {
		return nil, err
	}
	examples, err := validateConfirmedExamples(p.Examples)
	if err != nil {
		return nil, fmt.Errorf("invalid confirmed examples: %w", err)
	}

	confirmedBy := principal.ID
	confirmed := &genscantle.ChangeCardResult{
		ChangeID:           existing.ChangeID,
		Status:             StatusConfirmed,
		Prompt:             existing.Prompt,
		Summary:            existing.Summary,
		Ambiguities:        existing.Ambiguities,
		Answers:            maps.Clone(p.Answers),
		Examples:           examples,
		PreservedBehaviors: slices.Clone(existing.PreservedBehaviors),
		ConfirmedBy:        &confirmedBy,
	}
	rev, err := computeCanonicalRevision(confirmed, record.owner, record.designSource)
	if err != nil {
		return nil, err
	}
	confirmed.RequestRevision = rev
	c.cards[p.ChangeID] = storedChangeCard{
		card:         confirmed,
		owner:        record.owner,
		designSource: record.designSource,
	}
	return confirmed, nil
}

// PreviewArchitecture evaluates the candidate design module snapshot via the
// real Goa evaluation engine (`designeval.Evaluate`) bound to a confirmed CP1
// Change Card and returns the validated C4 topology, directed edges, per-agent
// runtime policies, and tool contracts.
func (c *ControlPlane) PreviewArchitecture(ctx context.Context, p *genscantle.PreviewArchitecturePayload) (*genscantle.ArchitecturePreviewResult, error) {
	principal, ok := PrincipalFromContext(ctx)
	if !ok {
		return nil, errors.New("authenticated principal is required")
	}
	if p == nil || strings.TrimSpace(p.ChangeID) == "" || strings.TrimSpace(p.RequestRevision) == "" {
		return nil, errors.New("change_id and request_revision are required")
	}

	c.mu.RLock()
	record, exists := c.cards[p.ChangeID]
	c.mu.RUnlock()
	if !exists {
		return nil, fmt.Errorf("unknown change_id %q", p.ChangeID)
	}
	if record.owner != principal.ID {
		return nil, fmt.Errorf("principal %q is not the owner of change_id %q", principal.ID, p.ChangeID)
	}
	if record.card.Status != StatusConfirmed {
		return nil, fmt.Errorf("change_id %q must be confirmed before architecture preview (status %q)", p.ChangeID, record.card.Status)
	}
	if record.card.RequestRevision != p.RequestRevision {
		return nil, fmt.Errorf("request_revision mismatch for %q: got %q, want %q", p.ChangeID, p.RequestRevision, record.card.RequestRevision)
	}

	evaluated, err := designeval.Evaluate(ctx, p.Design.Files)
	if err != nil {
		return nil, err
	}
	if evaluated.SystemName == "" || len(evaluated.Containers) == 0 {
		return nil, errors.New("design must declare at least one Goa API or Service container")
	}
	for _, pol := range evaluated.AgentPolicies {
		if pol.MaxExecutionSeconds <= 0 || pol.MaxToolCalls <= 0 {
			return nil, fmt.Errorf("agent %q must declare positive TimeBudget and DefaultCaps(MaxToolCalls) in RunPolicy", pol.AgentID)
		}
	}

	designDigest := designeval.SnapshotDigest(p.Design.Files)
	swarmItem, err := planSwarmRoster(p.ChangeID, designDigest, c.swarmProfiles, p.Builders)
	if err != nil {
		return nil, err
	}
	banner := fmt.Sprintf(
		"%s: %d container(s), %d boundary edge(s), %d agent policy(s), %d tool contract(s)",
		evaluated.SystemName,
		len(evaluated.Containers),
		len(evaluated.Edges),
		len(evaluated.AgentPolicies),
		len(evaluated.Tools),
	)

	return &genscantle.ArchitecturePreviewResult{
		ChangeID:        p.ChangeID,
		RequestRevision: p.RequestRevision,
		DesignDigest:    designDigest,
		SystemName:      evaluated.SystemName,
		Containers:      evaluated.Containers,
		Edges:           evaluated.Edges,
		AgentPolicies:   evaluated.AgentPolicies,
		Tools:           evaluated.Tools,
		Swarm:           swarmItem,
		Banner:          banner,
	}, nil
}

// DraftOverride applies a 0-second prompt override on the bound target platform
// via TargetPromptUpdater when target_kind is "prompt_template", or refuses to
// mask a compiled Go business rule when target_kind is "compiled_rule".
func (c *ControlPlane) DraftOverride(ctx context.Context, p *genscantle.DraftOverridePayload) (*genscantle.DraftOverrideResult, error) {
	if _, ok := PrincipalFromContext(ctx); !ok {
		return nil, errors.New("authenticated principal is required")
	}
	if p == nil {
		return nil, errors.New("draft override payload is required")
	}
	switch p.TargetKind {
	case TargetKindCompiledRule:
		return &genscantle.DraftOverrideResult{
			AppliedInStore:  false,
			RequiresRebuild: true,
			Message:         "Compiled Go business rule cannot be overridden in prompt.Store; requires Go rebuild and CP4 verification.",
		}, nil
	case TargetKindPromptTemplate:
		if p.PromptID == nil || strings.TrimSpace(*p.PromptID) == "" {
			return nil, errors.New("prompt_id is required when target_kind is prompt_template")
		}
		if strings.TrimSpace(p.NewContent) == "" {
			return nil, errors.New("new_content is required when target_kind is prompt_template")
		}
		if c.targetUpdater == nil {
			return nil, errors.New("no target prompt updater is bound to this control plane")
		}
		id := strings.TrimSpace(*p.PromptID)
		version, err := c.targetUpdater.ApplyPromptOverride(ctx, id, p.NewContent)
		if err != nil {
			return nil, fmt.Errorf("apply target prompt override: %w", err)
		}
		if strings.TrimSpace(version) == "" {
			return nil, errors.New("target prompt updater returned an empty version")
		}
		return &genscantle.DraftOverrideResult{
			AppliedInStore:  true,
			RequiresRebuild: false,
			PromptVersion:   &version,
			Message:         fmt.Sprintf("Updated prompt %q on target platform (version %s) for 0s hot reload.", id, version),
		}, nil
	default:
		return nil, fmt.Errorf("unsupported target_kind %q", p.TargetKind)
	}
}

// Diff compares two Goa design sources and go.mod snapshots and returns the
// structural diff without emitting a blanket compatibility verdict.
func (c *ControlPlane) Diff(ctx context.Context, p *genscantle.DiffPayload) (*genscantle.DiffResult, error) {
	if p == nil {
		return nil, errors.New("diff payload is required")
	}
	rep, err := diff.Compare(ctx, p.Old.Files, p.New.Files)
	if err != nil {
		return nil, err
	}
	return &genscantle.DiffResult{
		HasChanges:           rep.HasChanges,
		AddedDeclarations:    rep.AddedDeclarations,
		RemovedDeclarations:  rep.RemovedDeclarations,
		ModifiedDeclarations: rep.ModifiedDeclarations,
		VersionDeltas:        rep.VersionDeltas,
		Summary:              rep.Summary,
	}, nil
}

// validateCP1Analysis validates all model-returned CP1 fields strictly without
// silent coercion and assigns code-owned mechanical ambiguity and check IDs.
func validateCP1Analysis(
	changeID string,
	in CP1SemanticAnalysis,
) (string, []*genscantle.AmbiguityQuestion, []*genscantle.ExampleItem, []string, error) {
	if strings.TrimSpace(in.Summary) == "" {
		return "", nil, nil, nil, errors.New("summary is required")
	}
	ambiguities, err := validateAmbiguities(changeID, in.Ambiguities)
	if err != nil {
		return "", nil, nil, nil, err
	}
	if len(in.Examples) == 0 {
		return "", nil, nil, nil, errors.New("at least one example is required")
	}
	examples := make([]*genscantle.ExampleItem, 0, len(in.Examples))
	for i, item := range in.Examples {
		if strings.TrimSpace(item.Scenario) == "" ||
			strings.TrimSpace(item.InputSummary) == "" ||
			strings.TrimSpace(item.ExpectedOutcome) == "" {
			return "", nil, nil, nil, fmt.Errorf("examples[%d] requires non-empty scenario, input_summary, and expected_outcome", i)
		}
		if item.VerificationKind != "deterministic" && item.VerificationKind != "model_judged" {
			return "", nil, nil, nil, fmt.Errorf("examples[%d] has invalid verification_kind %q", i, item.VerificationKind)
		}
		examples = append(examples, &genscantle.ExampleItem{
			ID:               fmt.Sprintf("%s-check-%02d", changeID, i+1),
			Scenario:         item.Scenario,
			InputSummary:     item.InputSummary,
			ExpectedOutcome:  item.ExpectedOutcome,
			VerificationKind: item.VerificationKind,
		})
	}
	preserved := make([]string, 0, len(in.PreservedBehaviors))
	for i, item := range in.PreservedBehaviors {
		if strings.TrimSpace(item) == "" {
			return "", nil, nil, nil, fmt.Errorf("preserved_behaviors[%d] is empty", i)
		}
		preserved = append(preserved, item)
	}
	return in.Summary, ambiguities, examples, preserved, nil
}

// validateAmbiguities validates that every ambiguity question has a non-empty
// question and at least two distinct non-empty options, and assigns a
// code-owned question ID (`<changeID>-ambiguity-NN`).
func validateAmbiguities(changeID string, in []CP1ModelAmbiguity) ([]*genscantle.AmbiguityQuestion, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make([]*genscantle.AmbiguityQuestion, 0, len(in))
	for i, q := range in {
		id := fmt.Sprintf("%s-ambiguity-%02d", changeID, i+1)
		if strings.TrimSpace(q.Question) == "" {
			return nil, fmt.Errorf("ambiguities[%d] requires non-empty question", i)
		}
		if len(q.Options) < 2 {
			return nil, fmt.Errorf("ambiguity %q requires at least 2 options", id)
		}
		seenOpts := make(map[string]bool, len(q.Options)*2)
		opts := make([]string, 0, len(q.Options))
		for _, opt := range q.Options {
			trimmed := strings.TrimSpace(opt)
			if trimmed == "" {
				return nil, fmt.Errorf("ambiguity %q has an empty option", id)
			}
			if seenOpts[opt] || seenOpts[trimmed] {
				return nil, fmt.Errorf("ambiguity %q has duplicate option %q", id, opt)
			}
			seenOpts[opt] = true
			seenOpts[trimmed] = true
			opts = append(opts, opt)
		}
		out = append(out, &genscantle.AmbiguityQuestion{
			ID:       id,
			Question: q.Question,
			Options:  opts,
		})
	}
	return out, nil
}

// validateAmbiguityAnswers verifies exact 1-to-1 resolution of open ambiguity
// questions against their declared options.
func validateAmbiguityAnswers(questions []*genscantle.AmbiguityQuestion, answers map[string]string) error {
	allowedByID := make(map[string][]string, len(questions))
	for _, q := range questions {
		allowedByID[q.ID] = q.Options
		ans, has := answers[q.ID]
		if !has || strings.TrimSpace(ans) == "" {
			return fmt.Errorf("missing confirmation answer for ambiguity %q", q.ID)
		}
		if !slices.Contains(q.Options, ans) {
			return fmt.Errorf("answer %q for ambiguity %q is not in allowed options %v", ans, q.ID, q.Options)
		}
	}
	for id := range answers {
		if _, ok := allowedByID[id]; !ok {
			return fmt.Errorf("unexpected confirmation answer for unknown ambiguity %q", id)
		}
	}
	return nil
}

// validateConfirmedExamples validates that every confirmed ExampleItem has a
// unique non-empty ID and valid fields.
func validateConfirmedExamples(in []*genscantle.ExampleItem) ([]*genscantle.ExampleItem, error) {
	if len(in) == 0 {
		return nil, errors.New("at least one example is required")
	}
	seenIDs := make(map[string]bool, len(in)*2)
	out := make([]*genscantle.ExampleItem, 0, len(in))
	for i, item := range in {
		if item == nil {
			return nil, fmt.Errorf("examples[%d] is nil", i)
		}
		trimmedID := strings.TrimSpace(item.ID)
		if trimmedID == "" ||
			strings.TrimSpace(item.Scenario) == "" ||
			strings.TrimSpace(item.InputSummary) == "" ||
			strings.TrimSpace(item.ExpectedOutcome) == "" {
			return nil, fmt.Errorf("examples[%d] requires non-empty id, scenario, input_summary, and expected_outcome", i)
		}
		if item.VerificationKind != "deterministic" && item.VerificationKind != "model_judged" {
			return nil, fmt.Errorf("examples[%d] has invalid verification_kind %q", i, item.VerificationKind)
		}
		if seenIDs[item.ID] || seenIDs[trimmedID] {
			return nil, fmt.Errorf("duplicate example id %q", item.ID)
		}
		seenIDs[item.ID] = true
		seenIDs[trimmedID] = true
		out = append(out, &genscantle.ExampleItem{
			ID:               item.ID,
			Scenario:         item.Scenario,
			InputSummary:     item.InputSummary,
			ExpectedOutcome:  item.ExpectedOutcome,
			VerificationKind: item.VerificationKind,
		})
	}
	return out, nil
}

// extractAssistantText concatenates TextPart payloads from a model.Response.
func extractAssistantText(resp *model.Response) string {
	if resp == nil {
		return ""
	}
	var b strings.Builder
	for _, msg := range resp.Content {
		for _, part := range msg.Parts {
			if tp, ok := part.(model.TextPart); ok {
				b.WriteString(tp.Text)
			}
		}
	}
	return b.String()
}

// computeCanonicalRevision computes a collision-free SHA-256 hex digest over
// the canonical JSON encoding of the ChangeCardResult, its owner principal, and
// its inspected design source.
func computeCanonicalRevision(card *genscantle.ChangeCardResult, owner, designSource string) (string, error) {
	keys := make([]string, 0, len(card.Answers))
	for k := range card.Answers {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	answers := make([]canonicalAnswerPair, 0, len(keys))
	for _, k := range keys {
		answers = append(answers, canonicalAnswerPair{
			ID:     k,
			Answer: card.Answers[k],
		})
	}
	var confirmedBy string
	if card.ConfirmedBy != nil {
		confirmedBy = *card.ConfirmedBy
	}
	payload := canonicalCardRevision{
		ChangeID:           card.ChangeID,
		Owner:              owner,
		Status:             card.Status,
		Prompt:             card.Prompt,
		DesignSource:       designSource,
		Summary:            card.Summary,
		Ambiguities:        card.Ambiguities,
		Answers:            answers,
		Examples:           card.Examples,
		PreservedBehaviors: card.PreservedBehaviors,
		ConfirmedBy:        confirmedBy,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal canonical change card revision: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// planSwarmRoster invokes swarm.Plan over server-owned provider profiles and
// explicit builder path scopes and projects the result into a genscantle.SwarmPlanItem.
func planSwarmRoster(
	changeID, designDigest string,
	profiles []swarm.ProviderProfile,
	in []*genscantle.BuilderScopeItem,
) (*genscantle.SwarmPlanItem, error) {
	builders := make([]swarm.BuilderScope, 0, len(in))
	for i, item := range in {
		if item == nil {
			return nil, fmt.Errorf("builders[%d] is nil", i)
		}
		var preferred string
		if item.PreferredProfileID != nil {
			preferred = *item.PreferredProfileID
		}
		builders = append(builders, swarm.BuilderScope{
			Name:               item.Name,
			Paths:              item.Paths,
			PreferredProfileID: preferred,
		})
	}
	plan, err := swarm.Plan(swarm.Request{
		ChangeID:     changeID,
		DesignDigest: designDigest,
		Profiles:     profiles,
		Builders:     builders,
	})
	if err != nil {
		return nil, fmt.Errorf("plan swarm roster: %w", err)
	}

	assignments := make([]*genscantle.SwarmRoleItem, 0, len(plan.Assignments))
	for _, a := range plan.Assignments {
		assignments = append(assignments, &genscantle.SwarmRoleItem{
			Handle:     a.Handle,
			Role:       string(a.Role),
			ScopeName:  a.ScopeName,
			ProfileID:  a.ProfileID,
			Provider:   a.Provider,
			Model:      a.Model,
			Family:     a.Family,
			OwnedPaths: a.OwnedPaths,
			AccessMode: a.AccessMode,
		})
	}

	var missingRoles []string
	if len(plan.MissingRoles) > 0 {
		missingRoles = make([]string, 0, len(plan.MissingRoles))
		for _, r := range plan.MissingRoles {
			missingRoles = append(missingRoles, string(r))
		}
	}

	var manifestPtr *string
	if plan.Status == swarm.StatusConfigured {
		manifest, err := swarm.FormatManifest(changeID, designDigest, plan)
		if err != nil {
			return nil, fmt.Errorf("format swarm manifest: %w", err)
		}
		manifestPtr = &manifest
	}

	return &genscantle.SwarmPlanItem{
		Status:            string(plan.Status),
		Assignments:       assignments,
		CrossFamilyReview: plan.CrossFamilyReview,
		CrossFamilyEval:   plan.CrossFamilyEval,
		SingleFamily:      plan.SingleFamily,
		MissingRoles:      missingRoles,
		Summary:           plan.Summary,
		Manifest:          manifestPtr,
	}, nil
}
