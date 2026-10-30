// Package controlplane tests the stateless MCP Apps (`ui://scantle/checkpoint`)
// viewer and its JSON-RPC 2.0 host lifecycle bridge.
package controlplane

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	genmcpadapter "github.com/goadesign/scantle/gen/mcp_scantle"
	genscantle "github.com/goadesign/scantle/gen/scantle"
	"github.com/goadesign/scantle/internal/swarm"
	"github.com/goadesign/scantle/internal/testmodule"
	"goa.design/goa-ai/runtime/agent/prompt"
)

// TestCheckpointUIMCPAppsHandshakeAndToolResultRendering verifies that
// `ui://scantle/checkpoint` serves a stateless MCP Apps viewer that performs
// the `ui/initialize` -> `ui/notifications/initialized` postMessage handshake
// with `window.parent`, rejects cross-origin messages where `event.source !==
// window.parent`, replaces prior views on each new tool result without server
// preview state, and renders exact `request_revision` and `design_digest`
// values for CP1, CP2, and CP3 tool results.
func TestCheckpointUIMCPAppsHandshakeAndToolResultRendering(t *testing.T) {
	ctx := WithPrincipal(context.Background(), Principal{ID: "alice@example.com"})
	analysisJSON, err := json.Marshal(CP1SemanticAnalysis{
		Summary: "Route refunds >= $50 to manager approval.",
		Ambiguities: []CP1ModelAmbiguity{
			{
				Question: "Does the $50 limit include tax?",
				Options:  []string{"subtotal_only", "total_with_tax"},
			},
		},
		Examples: []CP1ModelExample{
			{
				Scenario:         "Refund under $50",
				InputSummary:     "$40 subtotal",
				ExpectedOutcome:  "Auto-approved",
				VerificationKind: "deterministic",
			},
		},
		PreservedBehaviors: []string{"Cross-tenant lookups return 404."},
	})
	if err != nil {
		t.Fatalf("marshal analysis: %v", err)
	}

	targetStore := prompt.NewInMemoryStore()
	cp := New(Options{
		Analyzer:      NewModelCP1Analyzer(newStubModelClient(t, string(analysisJSON))),
		TargetUpdater: &boundTargetPromptUpdater{store: targetStore},
		SwarmProfiles: []swarm.ProviderProfile{
			{
				ID:            "claude-opus",
				Provider:      "claude",
				Model:         "claude-opus-4-6",
				Family:        "anthropic",
				Authenticated: true,
				Capabilities:  []swarm.RoleKind{swarm.RoleArchitect, swarm.RoleBuilder, swarm.RoleEvalAuthor, swarm.RoleReviewer},
			},
			{
				ID:            "codex-gpt5",
				Provider:      "codex",
				Model:         "gpt-5.4",
				Family:        "openai",
				Authenticated: true,
				Capabilities:  []swarm.RoleKind{swarm.RoleBuilder, swarm.RoleEvalAuthor, swarm.RoleReviewer},
			},
		},
	})
	adapter := genmcpadapter.NewMCPAdapter(genscantle.NewEndpoints(cp), nil)

	resRead, err := adapter.ResourcesRead(ctx, &genmcpadapter.ResourcesReadPayload{URI: "ui://scantle/checkpoint"})
	if err != nil {
		t.Fatalf("ResourcesRead(ui://scantle/checkpoint): %v", err)
	}
	completeRes, ok := resRead.Outcome.AsComplete()
	if !ok || len(completeRes.Contents) != 1 || completeRes.Contents[0].Text == nil || completeRes.Contents[0].MimeType == nil {
		t.Fatalf("unexpected ResourcesRead outcome: %+v", resRead)
	}
	if got := *completeRes.Contents[0].MimeType; got != "text/html;profile=mcp-app" {
		t.Fatalf("MimeType = %q, want %q", got, "text/html;profile=mcp-app")
	}
	htmlDoc := *completeRes.Contents[0].Text
	if strings.Contains(htmlDoc, "innerHTML") {
		t.Fatal("checkpointUIHTML must not use innerHTML")
	}
	for _, want := range []string{
		`data-profile="mcp-app"`,
		`"ui/initialize"`,
		`"ui/notifications/initialized"`,
		`"ui/notifications/tool-input"`,
		`"ui/notifications/tool-cancelled"`,
		`"ui/notifications/tool-result"`,
		`"ui/resource-teardown"`,
		`event.source !== window.parent`,
	} {
		if !strings.Contains(htmlDoc, want) {
			t.Fatalf("checkpointUIHTML missing %q", want)
		}
	}

	// Execute real MCP ToolsCall operations for CP1, CP2, and CP3 to obtain wire results.
	proposeArgs, err := json.Marshal(map[string]any{
		"change_id": "chg-ui-test",
		"prompt":    "Route refunds >= $50 to manager approval.",
	})
	if err != nil {
		t.Fatalf("marshal proposeArgs: %v", err)
	}
	proposeCall, err := adapter.ToolsCall(ctx, &genmcpadapter.ToolsCallPayload{
		Name:      "propose_change",
		Arguments: proposeArgs,
	})
	if err != nil {
		t.Fatalf("ToolsCall(propose_change): %v", err)
	}
	proposeOut, ok := proposeCall.Outcome.AsComplete()
	if !ok || len(proposeOut.StructuredContent) == 0 {
		t.Fatalf("unexpected propose_change outcome: %+v", proposeCall)
	}
	var proposedWire struct {
		ChangeID        string `json:"change_id"`
		RequestRevision string `json:"request_revision"`
	}
	if err := json.Unmarshal(proposeOut.StructuredContent, &proposedWire); err != nil {
		t.Fatalf("unmarshal proposedWire: %v", err)
	}

	confirmArgs, err := json.Marshal(map[string]any{
		"change_id":        proposedWire.ChangeID,
		"request_revision": proposedWire.RequestRevision,
		"answers": map[string]string{
			"chg-ui-test-ambiguity-01": "subtotal_only",
		},
		"examples": []map[string]any{
			{
				"id":                "chg-ui-test-check-01",
				"scenario":          "Refund under $50",
				"input_summary":     "$40 subtotal",
				"expected_outcome":  "Auto-approved",
				"verification_kind": "deterministic",
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal confirmArgs: %v", err)
	}
	confirmCall, err := adapter.ToolsCall(ctx, &genmcpadapter.ToolsCallPayload{
		Name:      "confirm_intent",
		Arguments: confirmArgs,
	})
	if err != nil {
		t.Fatalf("ToolsCall(confirm_intent): %v", err)
	}
	confirmOut, ok := confirmCall.Outcome.AsComplete()
	if !ok || len(confirmOut.StructuredContent) == 0 {
		t.Fatalf("unexpected confirm_intent outcome: %+v", confirmCall)
	}
	var confirmedWire struct {
		ChangeID        string `json:"change_id"`
		RequestRevision string `json:"request_revision"`
	}
	if err := json.Unmarshal(confirmOut.StructuredContent, &confirmedWire); err != nil {
		t.Fatalf("unmarshal confirmedWire: %v", err)
	}

	candidateDesign := `package design

import (
	. "goa.design/goa/v3/dsl"
	. "goa.design/goa-ai/dsl"
)

var _ = API("orders", func() {})

var RefundTools = Toolset("refund_tools", func() {
	Description("Order lookup and payment refund tools")
	Tool("issue_refund", "Issue a payment refund", func() {
		Args(func() {
			Attribute("tenant_id", String)
			Attribute("order_id", String)
			Required("tenant_id", "order_id")
		})
		Return(func() {
			Attribute("status", String)
			Required("status")
		})
		Inject("tenant_id")
		Confirmation(func() {
			Title("Approve refund")
			PromptTemplate("Approve {{ .order_id }}?")
			DeniedResultTemplate(` + "`{\"status\":\"denied\"}`" + `)
		})
	})
})

var _ = Service("orders", func() {
	Description("Public Orders HTTP and MCP service")
	Agent("refund_agent", "Executes durable order refund workflows", func() {
		Use(RefundTools)
		RunPolicy(func() {
			DefaultCaps(MaxToolCalls(6), MaxRecoveryTurns(2))
			TimeBudget("2m")
		})
	})
})
`
	previewArgs, err := json.Marshal(map[string]any{
		"change_id":        confirmedWire.ChangeID,
		"request_revision": confirmedWire.RequestRevision,
		"design": map[string]any{
			"files": testmodule.Snapshot(t, "", candidateDesign),
		},
		"builders": []map[string]any{
			{
				"name":  "orders",
				"paths": []string{"internal/**"},
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal previewArgs: %v", err)
	}
	previewCall, err := adapter.ToolsCall(ctx, &genmcpadapter.ToolsCallPayload{
		Name:      "preview_architecture",
		Arguments: previewArgs,
	})
	if err != nil {
		t.Fatalf("ToolsCall(preview_architecture): %v", err)
	}
	previewOut, ok := previewCall.Outcome.AsComplete()
	if !ok || len(previewOut.StructuredContent) == 0 {
		t.Fatalf("unexpected preview_architecture outcome: %+v", previewCall)
	}
	var previewWire struct {
		DesignDigest string `json:"design_digest"`
	}
	if err := json.Unmarshal(previewOut.StructuredContent, &previewWire); err != nil {
		t.Fatalf("unmarshal previewWire: %v", err)
	}

	draftArgs, err := json.Marshal(map[string]any{
		"target_kind": TargetKindPromptTemplate,
		"prompt_id":   "orders.refund.system",
		"new_content": "You are a concise refund specialist.",
	})
	if err != nil {
		t.Fatalf("marshal draftArgs: %v", err)
	}
	draftCall, err := adapter.ToolsCall(ctx, &genmcpadapter.ToolsCallPayload{
		Name:      "draft_override",
		Arguments: draftArgs,
	})
	if err != nil {
		t.Fatalf("ToolsCall(draft_override): %v", err)
	}
	draftOut, ok := draftCall.Outcome.AsComplete()
	if !ok || len(draftOut.StructuredContent) == 0 {
		t.Fatalf("unexpected draft_override outcome: %+v", draftCall)
	}

	staleConfirmCall, err := adapter.ToolsCall(ctx, &genmcpadapter.ToolsCallPayload{
		Name:      "confirm_intent",
		Arguments: confirmArgs,
	})
	if err != nil {
		t.Fatalf("ToolsCall(confirm_intent stale): %v", err)
	}
	staleConfirmOut, ok := staleConfirmCall.Outcome.AsComplete()
	if !ok || staleConfirmOut.IsError == nil || !*staleConfirmOut.IsError || len(staleConfirmOut.Content) == 0 || staleConfirmOut.Content[0].Text == nil {
		t.Fatalf("expected stale confirm_intent tool error outcome, got: %+v", staleConfirmCall)
	}

	baselineDesign := `package design

import . "goa.design/goa/v3/dsl"

var _ = API("orders", func() {})

var _ = Service("orders", func() {
	Description("Baseline Orders service")
})
`
	diffArgs, err := json.Marshal(map[string]any{
		"old": map[string]any{
			"files": testmodule.Snapshot(t, "", baselineDesign),
		},
		"new": map[string]any{
			"files": testmodule.Snapshot(t, "", candidateDesign),
		},
	})
	if err != nil {
		t.Fatalf("marshal diffArgs: %v", err)
	}
	diffCall, err := adapter.ToolsCall(ctx, &genmcpadapter.ToolsCallPayload{
		Name:      "diff",
		Arguments: diffArgs,
	})
	if err != nil {
		t.Fatalf("ToolsCall(diff): %v", err)
	}
	diffOut, ok := diffCall.Outcome.AsComplete()
	if !ok || len(diffOut.StructuredContent) == 0 {
		t.Fatalf("unexpected diff outcome: %+v", diffCall)
	}

	nodePath, err := exec.LookPath("node")
	if err != nil {
		return
	}

	scriptBody := extractCheckpointScript(t, htmlDoc)
	harnessInput, err := json.Marshal(map[string]any{
		"script":                 scriptBody,
		"cp1Structured":          json.RawMessage(proposeOut.StructuredContent),
		"cp1ConfirmedStructured": json.RawMessage(confirmOut.StructuredContent),
		"cp2Structured":          json.RawMessage(previewOut.StructuredContent),
		"diffStructured":         json.RawMessage(diffOut.StructuredContent),
		"cp3Structured":          json.RawMessage(draftOut.StructuredContent),
		"staleErrorText":         *staleConfirmOut.Content[0].Text,
		"wantCP1Revision":        proposedWire.RequestRevision,
		"wantCP2Revision":        confirmedWire.RequestRevision,
		"wantDesignDigest":       previewWire.DesignDigest,
		"wantAmbiguityText":      "Does the $50 limit include tax?",
	})
	if err != nil {
		t.Fatalf("marshal harnessInput: %v", err)
	}

	nodeHarness := `
const fs = require("fs");
const vm = require("vm");
const input = JSON.parse(fs.readFileSync(0, "utf8"));

function makeElement(tag, ns) {
  return {
    tagName: String(tag).toUpperCase(),
    namespaceURI: ns || "",
    className: "",
    textContent: "",
    value: "",
    dataset: {},
    attributes: {},
    listeners: {},
    children: [],
    get firstChild() {
      return this.children.length > 0 ? this.children[0] : null;
    },
    setAttribute(k, v) {
      this.attributes[k] = String(v);
    },
    addEventListener(type, fn) {
      if (!this.listeners[type]) this.listeners[type] = [];
      this.listeners[type].push(fn);
    },
    dispatch(type, evt) {
      const list = this.listeners[type] || [];
      list.forEach((fn) => fn(evt || { target: this }));
    },
    appendChild(child) {
      this.children.push(child);
      return child;
    },
    removeChild(child) {
      const idx = this.children.indexOf(child);
      if (idx >= 0) this.children.splice(idx, 1);
      return child;
    }
  };
}

function findAll(node, pred, acc) {
  const out = acc || [];
  if (!node || node.nodeType === 3) return out;
  if (pred(node)) out.push(node);
  (node.children || []).forEach((c) => findAll(c, pred, out));
  return out;
}

function renderText(node) {
  if (!node) return "";
  if (node.nodeType === 3) return node.textContent;
  const own = node.textContent || "";
  const childText = (node.children || []).map(renderText).join(" ");
  return [own, childText].filter(Boolean).join(" ");
}

const root = makeElement("main");
root.dataset.profile = "mcp-app";
root.dataset.state = "waiting";
const statusEl = makeElement("p");
const viewEl = makeElement("section");

const hostMessages = [];
let messageListener = null;
const parentWindow = {
  postMessage(msg, targetOrigin) {
    hostMessages.push({ msg, targetOrigin });
  }
};
const guestWindow = {
  parent: parentWindow,
  addEventListener(type, fn) {
    if (type === "message") messageListener = fn;
  }
};
const documentObj = {
  getElementById(id) {
    if (id === "scantle-checkpoint-root") return root;
    if (id === "scantle-checkpoint-status") return statusEl;
    if (id === "scantle-checkpoint-view") return viewEl;
    return null;
  },
  createElement(tag) {
    return makeElement(tag, "");
  },
  createElementNS(ns, tag) {
    return makeElement(tag, ns);
  },
  createTextNode(text) {
    return { nodeType: 3, textContent: String(text) };
  }
};

vm.runInNewContext(input.script, {
  window: guestWindow,
  document: documentObj
});

if (typeof messageListener !== "function") {
  throw new Error("guest did not register window message listener");
}
if (hostMessages.length !== 1 || hostMessages[0].msg.method !== "ui/initialize" || hostMessages[0].msg.id !== 1) {
  throw new Error("expected initial ui/initialize request, got: " + JSON.stringify(hostMessages));
}

// 1. Forged message from non-parent source must be ignored.
messageListener({
  source: {},
  data: {
    jsonrpc: "2.0",
    method: "ui/notifications/tool-result",
    params: { structuredContent: input.cp1Structured }
  }
});
if (root.dataset.state !== "waiting") {
  throw new Error("forged non-parent message mutated state: " + root.dataset.state);
}

// 2. Host replies to ui/initialize -> guest sends ui/notifications/initialized.
messageListener({
  source: parentWindow,
  data: {
    jsonrpc: "2.0",
    id: 1,
    result: {
      protocolVersion: "2026-01-26",
      hostInfo: {
        name: "scantle-test-host",
        version: "1.0.0"
      },
      hostCapabilities: {}
    }
  }
});
if (hostMessages.length !== 2 || hostMessages[1].msg.method !== "ui/notifications/initialized") {
  throw new Error("expected ui/notifications/initialized after init response, got: " + JSON.stringify(hostMessages));
}
if (root.dataset.initialized !== "true") {
  throw new Error("expected root.dataset.initialized === 'true'");
}

// 3. Deliver CP1 ChangeCardResult via ui/notifications/tool-result and exercise interactive confirm_intent form.
messageListener({
  source: parentWindow,
  data: {
    jsonrpc: "2.0",
    method: "ui/notifications/tool-result",
    params: { structuredContent: input.cp1Structured }
  }
});
const cp1Text = renderText(viewEl);
if (root.dataset.state !== "cp1-change-card" || root.dataset.requestRevision !== input.wantCP1Revision) {
  throw new Error("unexpected CP1 dataset: " + JSON.stringify(root.dataset));
}
if (!cp1Text.includes(input.wantCP1Revision) || !cp1Text.includes(input.wantAmbiguityText)) {
  throw new Error("CP1 view missing revision or ambiguity question: " + cp1Text);
}

// 3a. Click an ambiguity option pill, edit an example outcome input, and submit confirm_intent via tools/call.
const optionPills = findAll(viewEl, (n) => n.attributes["data-ambiguity-id"] === "chg-ui-test-ambiguity-01" && n.attributes["data-option"] === "subtotal_only");
if (optionPills.length !== 1) {
  throw new Error("expected ambiguity option pill button, got: " + optionPills.length);
}
optionPills[0].dispatch("click");
const outcomeInputs = findAll(viewEl, (n) => n.attributes["data-example-id"] === "chg-ui-test-check-01" && n.attributes["data-field"] === "expected_outcome");
if (outcomeInputs.length !== 1) {
  throw new Error("expected example expected_outcome input, got: " + outcomeInputs.length);
}
outcomeInputs[0].value = "Instant store credit up to $50.00";
outcomeInputs[0].dispatch("input", { target: outcomeInputs[0] });
const submitBtns = findAll(viewEl, (n) => n.attributes["id"] === "scantle-confirm-submit");
if (submitBtns.length !== 1) {
  throw new Error("expected #scantle-confirm-submit button");
}
const msgCountBeforeConfirm = hostMessages.length;
submitBtns[0].dispatch("click");
if (hostMessages.length !== msgCountBeforeConfirm + 1) {
  throw new Error("expected tools/call request after clicking confirm submit");
}
const confirmReq = hostMessages[msgCountBeforeConfirm].msg;
if (confirmReq.method !== "tools/call" ||
    confirmReq.params.name !== "confirm_intent" ||
    confirmReq.params.arguments.request_revision !== input.wantCP1Revision ||
    confirmReq.params.arguments.answers["chg-ui-test-ambiguity-01"] !== "subtotal_only" ||
    confirmReq.params.arguments.examples[0].expected_outcome !== "Instant store credit up to $50.00") {
  throw new Error("unexpected confirm_intent tools/call payload: " + JSON.stringify(confirmReq));
}

// Host replies with confirmed ChangeCardResult -> guest transitions to confirmed revision.
messageListener({
  source: parentWindow,
  data: {
    jsonrpc: "2.0",
    id: confirmReq.id,
    result: {
      content: [],
      structuredContent: input.cp1ConfirmedStructured
    }
  }
});
if (root.dataset.state !== "cp1-change-card" || root.dataset.requestRevision !== input.wantCP2Revision) {
  throw new Error("expected confirmed CP1 revision after tools/call reply, got: " + JSON.stringify(root.dataset));
}

// 3b. Exercise stale-revision CP1 form submission: render older rev1 card, edit example, submit, receive server stale-revision error -> edits preserved and recovery guidance displayed.
messageListener({
  source: parentWindow,
  data: {
    jsonrpc: "2.0",
    method: "ui/notifications/tool-result",
    params: { structuredContent: input.cp1Structured }
  }
});
const stalePills = findAll(viewEl, (n) => n.attributes["data-ambiguity-id"] === "chg-ui-test-ambiguity-01" && n.attributes["data-option"] === "total_with_tax");
stalePills[0].dispatch("click");
const staleOutcomeInputs = findAll(viewEl, (n) => n.attributes["data-example-id"] === "chg-ui-test-check-01" && n.attributes["data-field"] === "expected_outcome");
staleOutcomeInputs[0].value = "Edited outcome preserved across stale error";
staleOutcomeInputs[0].dispatch("input", { target: staleOutcomeInputs[0] });
const staleSubmitBtns = findAll(viewEl, (n) => n.attributes["id"] === "scantle-confirm-submit");
const msgCountBeforeStale = hostMessages.length;
staleSubmitBtns[0].dispatch("click");
const staleReq = hostMessages[msgCountBeforeStale].msg;
messageListener({
  source: parentWindow,
  data: {
    jsonrpc: "2.0",
    id: staleReq.id,
    result: {
      isError: true,
      content: [{ type: "text", text: input.staleErrorText }]
    }
  }
});
const staleViewText = renderText(viewEl);
if (root.dataset.state !== "cp1-change-card" ||
    !staleViewText.includes(input.staleErrorText) ||
    !staleViewText.includes("Your edited answers and examples remain in this form") ||
    staleOutcomeInputs[0].value !== "Edited outcome preserved across stale error") {
  throw new Error("stale form submission did not preserve edits or show recovery guidance: " + staleViewText);
}

// 3c. Click #scantle-load-latest-card after stale error -> guest calls get_change and renders latest card alongside #scantle-preserved-draft.
const loadLatestBtns = findAll(viewEl, (n) => n.attributes["id"] === "scantle-load-latest-card");
if (loadLatestBtns.length !== 1) {
  throw new Error("expected #scantle-load-latest-card button after stale error");
}
const msgCountBeforeGetChange = hostMessages.length;
loadLatestBtns[0].dispatch("click");
if (hostMessages.length !== msgCountBeforeGetChange + 1) {
  throw new Error("expected tools/call get_change request after clicking load latest card");
}
const getChangeReq = hostMessages[msgCountBeforeGetChange].msg;
if (getChangeReq.method !== "tools/call" ||
    getChangeReq.params.name !== "get_change" ||
    getChangeReq.params.arguments.change_id !== "chg-ui-test") {
  throw new Error("unexpected get_change tools/call payload: " + JSON.stringify(getChangeReq));
}
messageListener({
  source: parentWindow,
  data: {
    jsonrpc: "2.0",
    id: getChangeReq.id,
    result: {
      content: [],
      structuredContent: input.cp1ConfirmedStructured
    }
  }
});
const recoveredViewText = renderText(viewEl);
const preservedPanels = findAll(viewEl, (n) => n.attributes["id"] === "scantle-preserved-draft");
if (root.dataset.state !== "cp1-change-card" ||
    root.dataset.requestRevision !== input.wantCP2Revision ||
    preservedPanels.length !== 1 ||
    !recoveredViewText.includes("Preserved Unsaved Draft Edits") ||
    !recoveredViewText.includes("total_with_tax") ||
    !recoveredViewText.includes("Edited outcome preserved across stale error")) {
  throw new Error("get_change recovery did not render latest card + preserved draft panel: " + recoveredViewText);
}

// 4. Deliver CP2 ArchitecturePreviewResult -> must replace CP1 view completely and render both SVG graph and inspectable list.
messageListener({
  source: parentWindow,
  data: {
    jsonrpc: "2.0",
    method: "ui/notifications/tool-result",
    params: { structuredContent: input.cp2Structured }
  }
});
const cp2Text = renderText(viewEl);
if (root.dataset.state !== "cp2-architecture-preview" ||
    root.dataset.requestRevision !== input.wantCP2Revision ||
    root.dataset.designDigest !== input.wantDesignDigest) {
  throw new Error("unexpected CP2 dataset: " + JSON.stringify(root.dataset));
}
if (cp2Text.includes(input.wantAmbiguityText)) {
  throw new Error("CP2 view retained stale CP1 ambiguity text: " + cp2Text);
}
if (!cp2Text.includes(input.wantCP2Revision) ||
    !cp2Text.includes(input.wantDesignDigest) ||
    !cp2Text.includes("Inject: tenant_id") ||
    !cp2Text.includes("Confirmation")) {
  throw new Error("CP2 view missing expected digests or guardrails: " + cp2Text);
}
const cp2Svg = findAll(viewEl, (n) => n.attributes["id"] === "scantle-c4-svg");
const cp2SvgNodes = findAll(viewEl, (n) => n.className === "scantle-svg-node");
if (cp2Svg.length !== 1 || cp2SvgNodes.length !== input.cp2Structured.containers.length) {
  throw new Error("expected CP2 SVG graph with all containers, got svg=" + cp2Svg.length + " nodes=" + cp2SvgNodes.length);
}

// 4a. Deliver 15-node cyclic + colliding-name + self-loop CP2 fixture -> no node cutoff, all 15 nodes and cyclic/self-loop edges rendered alongside inspectable list.
const largeContainers = [];
for (let i = 1; i <= 15; i++) {
  largeContainers.push({
    id: "node-" + i,
    name: i <= 3 ? "Duplicate Display Name" : "Container " + i,
    kind: i === 1 ? "person" : (i <= 8 ? "service" : "container"),
    description: "Description for node-" + i
  });
}
const cyclicEdges = [
  { from_id: "node-1", to_id: "node-2", protocol: "HTTPS" },
  { from_id: "node-2", to_id: "node-3", protocol: "gRPC" },
  { from_id: "node-3", to_id: "node-2", protocol: "Callback gRPC" },
  { from_id: "node-2", to_id: "node-2", protocol: "Self Loop" },
  { from_id: "node-8", to_id: "node-15", protocol: "SQLite" }
];
const largePreview = Object.assign({}, input.cp2Structured, {
  containers: largeContainers,
  edges: cyclicEdges
});
messageListener({
  source: parentWindow,
  data: {
    jsonrpc: "2.0",
    method: "ui/notifications/tool-result",
    params: { structuredContent: largePreview }
  }
});
const largeSvgNodes = findAll(viewEl, (n) => n.className === "scantle-svg-node");
const largeSvgEdges = findAll(viewEl, (n) => n.className === "scantle-svg-edge");
const largeText = renderText(viewEl);
if (largeSvgNodes.length !== 15 || largeSvgEdges.length !== 5 || !largeText.includes("node-15") || !largeText.includes("Self Loop")) {
  throw new Error("expected 15 SVG nodes and 5 cyclic/self-loop edges with inspectable list, got nodes=" + largeSvgNodes.length + " edges=" + largeSvgEdges.length);
}

// 4b. Deliver CP2 DiffResult from MCP diff codec -> renders unbound structural diff and clears CP1/CP2 dataset keys.
messageListener({
  source: parentWindow,
  data: {
    jsonrpc: "2.0",
    method: "ui/notifications/tool-result",
    params: { structuredContent: input.diffStructured }
  }
});
const diffText = renderText(viewEl);
if (root.dataset.state !== "cp2-diff" ||
    root.dataset.hasChanges !== "true" ||
    root.dataset.changeId !== undefined ||
    root.dataset.requestRevision !== undefined ||
    root.dataset.designDigest !== undefined) {
  throw new Error("unexpected cp2-diff dataset: " + JSON.stringify(root.dataset));
}
if (!diffText.includes("Standalone structural comparison") || !diffText.includes("refund_agent")) {
  throw new Error("cp2-diff view missing unbound label or added declaration: " + diffText);
}

// 5. Deliver CP3 DraftOverrideResult -> replaces CP2 view.
messageListener({
  source: parentWindow,
  data: {
    jsonrpc: "2.0",
    method: "ui/notifications/tool-result",
    params: { structuredContent: input.cp3Structured }
  }
});
const cp3Text = renderText(viewEl);
if (root.dataset.state !== "cp3-draft-override" || root.dataset.designDigest !== undefined) {
  throw new Error("unexpected CP3 dataset: " + JSON.stringify(root.dataset));
}
if (!cp3Text.includes("applied_in_store=true") || !cp3Text.includes("requires_rebuild=false")) {
  throw new Error("CP3 view missing fast-draft badges: " + cp3Text);
}

// 6. Deliver tool-input notification -> transitions back to waiting state.
messageListener({
  source: parentWindow,
  data: {
    jsonrpc: "2.0",
    method: "ui/notifications/tool-input",
    params: { arguments: { change_id: "chg-next" } }
  }
});
if (root.dataset.state !== "waiting") {
  throw new Error("expected waiting state on tool-input notification, got: " + root.dataset.state);
}

// 7. Deliver isError tool-result -> transitions to explicit error state with error text.
messageListener({
  source: parentWindow,
  data: {
    jsonrpc: "2.0",
    method: "ui/notifications/tool-result",
    params: {
      isError: true,
      content: [{ type: "text", text: "stale request_revision" }]
    }
  }
});
if (root.dataset.state !== "error" || !renderText(viewEl).includes("stale request_revision")) {
  throw new Error("expected error state with tool error text, got: " + root.dataset.state + " / " + renderText(viewEl));
}

// 8. Deliver malformed tool-result -> transitions to explicit error state.
messageListener({
  source: parentWindow,
  data: {
    jsonrpc: "2.0",
    method: "ui/notifications/tool-result",
    params: { structuredContent: { bogus: true } }
  }
});
if (root.dataset.state !== "error") {
  throw new Error("expected error state on malformed payload, got: " + root.dataset.state);
}

// 9. Deliver tool-cancelled notification -> clears attribution and transitions to cancelled state.
messageListener({
  source: parentWindow,
  data: {
    jsonrpc: "2.0",
    method: "ui/notifications/tool-cancelled",
    params: { reason: "user aborted" }
  }
});
if (root.dataset.state !== "cancelled" || root.dataset.requestRevision !== undefined || !renderText(viewEl).includes("user aborted")) {
  throw new Error("expected cancelled state with cleared attribution, got: " + JSON.stringify(root.dataset) + " / " + renderText(viewEl));
}

// 10. Deliver host ping and ui/resource-teardown requests with id === 1 (same as INIT_REQUEST_ID) -> must respond with JSON-RPC results, not misclassify as init response.
const beforeCount = hostMessages.length;
messageListener({
  source: parentWindow,
  data: {
    jsonrpc: "2.0",
    id: 1,
    method: "ping",
    params: {}
  }
});
if (hostMessages.length !== beforeCount + 1 || hostMessages[beforeCount].msg.id !== 1 || !hostMessages[beforeCount].msg.result) {
  throw new Error("expected ping result for id=1, got: " + JSON.stringify(hostMessages.slice(beforeCount)));
}

messageListener({
  source: parentWindow,
  data: {
    jsonrpc: "2.0",
    id: 1,
    method: "ui/resource-teardown",
    params: {}
  }
});
if (hostMessages.length !== beforeCount + 2 || hostMessages[beforeCount + 1].msg.id !== 1 || !hostMessages[beforeCount + 1].msg.result || root.dataset.state !== "terminated") {
  throw new Error("expected resource-teardown result for id=1 and terminated state, got: " + JSON.stringify(hostMessages.slice(beforeCount)) + " / " + root.dataset.state);
}
`
	cmd := exec.CommandContext(ctx, nodePath, "-e", nodeHarness)
	cmd.Stdin = strings.NewReader(string(harnessInput))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("MCP Apps JS bridge harness failed: %v\n%s", err, string(out))
	}
}

// extractCheckpointScript extracts the embedded <script>...</script> body from
// the CheckpointUI HTML document.
func extractCheckpointScript(t *testing.T, htmlDoc string) string {
	t.Helper()
	const openTag = "<script>"
	const closeTag = "</script>"
	start := strings.Index(htmlDoc, openTag)
	end := strings.LastIndex(htmlDoc, closeTag)
	if start < 0 || end < 0 || end <= start+len(openTag) {
		t.Fatal("checkpointUIHTML missing <script>...</script> block")
	}
	return htmlDoc[start+len(openTag) : end]
}
