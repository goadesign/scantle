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

	nodePath, err := exec.LookPath("node")
	if err != nil {
		return
	}

	scriptBody := extractCheckpointScript(t, htmlDoc)
	harnessInput, err := json.Marshal(map[string]any{
		"script":            scriptBody,
		"cp1Structured":     json.RawMessage(proposeOut.StructuredContent),
		"cp2Structured":     json.RawMessage(previewOut.StructuredContent),
		"cp3Structured":     json.RawMessage(draftOut.StructuredContent),
		"wantCP1Revision":   proposedWire.RequestRevision,
		"wantCP2Revision":   confirmedWire.RequestRevision,
		"wantDesignDigest":  previewWire.DesignDigest,
		"wantAmbiguityText": "Does the $50 limit include tax?",
	})
	if err != nil {
		t.Fatalf("marshal harnessInput: %v", err)
	}

	nodeHarness := `
const fs = require("fs");
const vm = require("vm");
const input = JSON.parse(fs.readFileSync(0, "utf8"));

function makeElement(tag) {
  return {
    tagName: String(tag).toUpperCase(),
    className: "",
    textContent: "",
    dataset: {},
    attributes: {},
    children: [],
    get firstChild() {
      return this.children.length > 0 ? this.children[0] : null;
    },
    setAttribute(k, v) {
      this.attributes[k] = String(v);
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
    return makeElement(tag);
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

// 3. Deliver CP1 ChangeCardResult via ui/notifications/tool-result.
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

// 4. Deliver CP2 ArchitecturePreviewResult -> must replace CP1 view completely.
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
