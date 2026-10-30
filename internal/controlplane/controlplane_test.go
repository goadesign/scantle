// Package controlplane tests Checkpoints 1 through 3 (CP1–CP3) of the Scantle
// MCP software factory control plane.
package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	anthropicsdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/ssestream"
	"goa.design/goa-ai/features/model/anthropic"

	genmcpadapter "github.com/goadesign/scantle/gen/mcp_scantle"
	genscantle "github.com/goadesign/scantle/gen/scantle"
	"github.com/goadesign/scantle/internal/goldenpath"
	"github.com/goadesign/scantle/internal/swarm"
	"github.com/goadesign/scantle/internal/testmodule"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/prompt"
)

type (
	// stubProvider implements model.Provider for testing ModelCP1Analyzer through
	// the goa-ai model.NewClient StructuredOutput validation wrapper.
	stubProvider struct {
		// responseJSON is the structured JSON payload returned by Complete.
		responseJSON string
	}

	// boundTargetPromptUpdater binds a target platform's prompt.Store to the
	// TargetPromptUpdater contract for testing CP3 Fast Draft Mode.
	boundTargetPromptUpdater struct {
		// store is the target platform's prompt.Store.
		store prompt.Store
	}

	// recordingAnthropicMessages is an anthropic.MessagesClient that records the
	// encoded request and returns a fixed message.
	recordingAnthropicMessages struct {
		// resp is the message returned by New.
		resp *anthropicsdk.Message
		// last is the most recent request body encoded by the adapter.
		last anthropicsdk.MessageNewParams
		// calls counts New invocations.
		calls int
	}
)

// TestCP1AmbiguityResolutionStrictValidationOwnershipAndRevisionLock verifies
// CP1 ambiguity detection, code-owned question/check IDs, authenticated owner
// binding, and canonical revision locking.
func TestCP1AmbiguityResolutionStrictValidationOwnershipAndRevisionLock(t *testing.T) {
	aliceCtx := WithPrincipal(context.Background(), Principal{ID: "alice@example.com"})
	bobCtx := WithPrincipal(context.Background(), Principal{ID: "bob@example.com"})

	ambiguousAnalysis := CP1SemanticAnalysis{
		Summary: "  Auto-approve refunds under $50 and route larger refunds to manager approval.\n",
		Ambiguities: []CP1ModelAmbiguity{
			{
				Question: "  Does the $50 threshold apply to item subtotal or total order amount including tax? ",
				Options:  []string{"item_subtotal_cents", "total_order_cents"},
			},
			{
				Question: "Should an exact $50.00 refund auto-approve or require manager approval?",
				Options:  []string{"auto_approve_lte_5000", "require_manager_at_5000"},
			},
		},
		Examples: []CP1ModelExample{
			{
				Scenario:         "  Refund under $50 auto-approves  ",
				InputSummary:     "Order subtotal $48.00, tax $4.32 ($52.32 total)",
				ExpectedOutcome:  "Auto-approved immediately via payments provider",
				VerificationKind: "deterministic",
			},
			{
				Scenario:         "Exact $50.00 boundary",
				InputSummary:     "Order subtotal $50.00",
				ExpectedOutcome:  "Requires manager approval before payment execution",
				VerificationKind: "deterministic",
			},
			{
				Scenario:         "Polite customer explanation",
				InputSummary:     "Customer asks why $50.00 refund is pending",
				ExpectedOutcome:  "Agent explains manager review policy clearly",
				VerificationKind: "model_judged",
			},
		},
		PreservedBehaviors: []string{
			"  Cross-tenant order lookups return 404 Not Found.  ",
			"Customer principals cannot call approve_refund.",
		},
	}
	rawBytes, err := json.Marshal(ambiguousAnalysis)
	if err != nil {
		t.Fatalf("marshal analysis: %v", err)
	}

	cp := New(Options{
		Analyzer: NewModelCP1Analyzer(newStubModelClient(t, string(rawBytes))),
	})

	designSrc := `package design // orders service`
	if _, err := cp.ProposeChange(context.Background(), &genscantle.ProposeChangePayload{
		ChangeID:     "chg-refund-50",
		Prompt:       "Refunds under $50 should be automatic, otherwise ask a manager.",
		DesignSource: &designSrc,
	}); err == nil {
		t.Fatal("expected unauthenticated ProposeChange to fail")
	}

	proposed, err := cp.ProposeChange(aliceCtx, &genscantle.ProposeChangePayload{
		ChangeID:     "chg-refund-50",
		Prompt:       "Refunds under $50 should be automatic, otherwise ask a manager.",
		DesignSource: &designSrc,
	})
	if err != nil {
		t.Fatalf("ProposeChange failed: %v", err)
	}
	if proposed.Status != StatusNeedsConfirmation {
		t.Fatalf("expected status %q, got %q", StatusNeedsConfirmation, proposed.Status)
	}
	if len(proposed.Ambiguities) != 2 ||
		proposed.Ambiguities[0].ID != "chg-refund-50-ambiguity-01" ||
		proposed.Ambiguities[1].ID != "chg-refund-50-ambiguity-02" {
		t.Fatalf("expected 2 ambiguities with code-owned IDs, got %+v", proposed.Ambiguities)
	}
	if len(proposed.Examples) != 3 || proposed.Examples[0].ID != "chg-refund-50-check-01" {
		t.Fatalf("unexpected examples: %+v", proposed.Examples)
	}
	if proposed.Summary != ambiguousAnalysis.Summary ||
		proposed.Ambiguities[0].Question != ambiguousAnalysis.Ambiguities[0].Question ||
		proposed.Examples[0].Scenario != ambiguousAnalysis.Examples[0].Scenario ||
		proposed.PreservedBehaviors[0] != ambiguousAnalysis.PreservedBehaviors[0] {
		t.Fatalf("expected accepted authored strings to be preserved verbatim without TrimSpace rewriting")
	}

	// Attempting to overwrite an existing change_id must be rejected.
	if _, err := cp.ProposeChange(bobCtx, &genscantle.ProposeChangePayload{
		ChangeID: "chg-refund-50",
		Prompt:   "Attempt to clobber Alice's change_id",
	}); err == nil {
		t.Fatal("expected duplicate change_id in ProposeChange to be rejected")
	}

	// Non-owner principal cannot confirm Alice's Change Card.
	if _, err := cp.ConfirmIntent(bobCtx, &genscantle.ConfirmIntentPayload{
		ChangeID:        "chg-refund-50",
		RequestRevision: proposed.RequestRevision,
		Answers: map[string]string{
			"chg-refund-50-ambiguity-01": "item_subtotal_cents",
			"chg-refund-50-ambiguity-02": "require_manager_at_5000",
		},
		Examples: proposed.Examples,
	}); err == nil {
		t.Fatal("expected ConfirmIntent by non-owner principal to be rejected")
	}

	// Stale request_revision must be rejected.
	if _, err := cp.ConfirmIntent(aliceCtx, &genscantle.ConfirmIntentPayload{
		ChangeID:        "chg-refund-50",
		RequestRevision: "stale-revision",
		Answers: map[string]string{
			"chg-refund-50-ambiguity-01": "item_subtotal_cents",
			"chg-refund-50-ambiguity-02": "require_manager_at_5000",
		},
		Examples: proposed.Examples,
	}); err == nil {
		t.Fatal("expected ConfirmIntent with mismatched request_revision to be rejected")
	}

	confirmed, err := cp.ConfirmIntent(aliceCtx, &genscantle.ConfirmIntentPayload{
		ChangeID:        "chg-refund-50",
		RequestRevision: proposed.RequestRevision,
		Answers: map[string]string{
			"chg-refund-50-ambiguity-01": "item_subtotal_cents",
			"chg-refund-50-ambiguity-02": "require_manager_at_5000",
		},
		Examples: proposed.Examples,
	})
	if err != nil {
		t.Fatalf("ConfirmIntent failed: %v", err)
	}
	if confirmed.Status != StatusConfirmed {
		t.Fatalf("expected status %q, got %q", StatusConfirmed, confirmed.Status)
	}
	if len(confirmed.Ambiguities) != 2 || len(confirmed.Answers) != 2 {
		t.Fatalf("expected ambiguities and answers to be retained for audit, got %d ambiguities and %d answers", len(confirmed.Ambiguities), len(confirmed.Answers))
	}
	if confirmed.ConfirmedBy == nil || *confirmed.ConfirmedBy != "alice@example.com" {
		t.Fatalf("expected ConfirmedBy alice@example.com, got %v", confirmed.ConfirmedBy)
	}
	if confirmed.RequestRevision == "" || confirmed.RequestRevision == proposed.RequestRevision {
		t.Fatalf("expected new confirmed RequestRevision, got %q (proposed %q)", confirmed.RequestRevision, proposed.RequestRevision)
	}
}

// TestCP1RejectsMalformedOrUnknownFieldsViaStructuredOutput verifies that
// invalid enum values, unknown fields, and model-supplied IDs are rejected at
// the CP1 model boundary.
func TestCP1RejectsMalformedOrUnknownFieldsViaStructuredOutput(t *testing.T) {
	ctx := WithPrincipal(context.Background(), Principal{ID: "alice@example.com"})
	cases := []struct {
		name string
		raw  string
	}{
		{
			name: "invalid_verification_kind",
			raw:  `{"summary":"Valid summary","ambiguities":[],"examples":[{"scenario":"s","input_summary":"i","expected_outcome":"o","verification_kind":"bogus_kind"}],"preserved_behaviors":[]}`,
		},
		{
			name: "unknown_top_level_field",
			raw:  `{"summary":"Valid summary","unexpected":true,"ambiguities":[],"examples":[{"scenario":"s","input_summary":"i","expected_outcome":"o","verification_kind":"deterministic"}],"preserved_behaviors":[]}`,
		},
		{
			name: "model_supplied_ambiguity_id",
			raw:  `{"summary":"Valid summary","ambiguities":[{"id":"m1","question":"q","options":["a","b"]}],"examples":[{"scenario":"s","input_summary":"i","expected_outcome":"o","verification_kind":"deterministic"}],"preserved_behaviors":[]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cp := New(Options{
				Analyzer: NewModelCP1Analyzer(newStubModelClient(t, tc.raw)),
			})
			if _, err := cp.ProposeChange(ctx, &genscantle.ProposeChangePayload{
				ChangeID: "chg-" + tc.name,
				Prompt:   "Test structured output validation rejection",
			}); err == nil {
				t.Fatalf("expected ProposeChange to reject %s", tc.name)
			}
		})
	}
}

// TestCP2EvaluatedArchitecturePreviewCP3FastDraftAndTokenAuthenticator verifies
// CP2 design evaluation, Golden Path MCP resource serving, proposed swarm
// planning, CP3 Fast Draft Mode prompt overrides, and bearer-token authentication.
func TestCP2EvaluatedArchitecturePreviewCP3FastDraftAndTokenAuthenticator(t *testing.T) {
	ctx := WithPrincipal(context.Background(), Principal{ID: "alice@example.com"})
	clearAnalysis := CP1SemanticAnalysis{
		Summary:     "Refunds with item_subtotal_cents < 5000 auto-approve; >= 5000 require manager approval.",
		Ambiguities: []CP1ModelAmbiguity{},
		Examples: []CP1ModelExample{
			{
				Scenario:         "Subtotal 4999 cents auto-approves",
				InputSummary:     "item_subtotal_cents=4999",
				ExpectedOutcome:  "Auto-approved",
				VerificationKind: "deterministic",
			},
		},
		PreservedBehaviors: []string{"Idempotency key refund/<tenant>/<order>"},
	}
	rawBytes, err := json.Marshal(clearAnalysis)
	if err != nil {
		t.Fatalf("marshal clearAnalysis: %v", err)
	}

	targetStore := prompt.NewInMemoryStore()
	cp := New(Options{
		Analyzer:      NewModelCP1Analyzer(newStubModelClient(t, string(rawBytes))),
		TargetUpdater: &boundTargetPromptUpdater{store: targetStore},
	})

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
			History(func() {
				CompressAtTurns(20)
				KeepMaxTurns(6)
			})
			Cache(func() {
				AfterSystem()
				AfterTools()
			})
		})
	})
})
`
	card, err := cp.ProposeChange(ctx, &genscantle.ProposeChangePayload{
		ChangeID:     "chg-clear",
		Prompt:       "Auto-approve when item_subtotal_cents < 5000; require manager when item_subtotal_cents >= 5000.",
		DesignSource: &candidateDesign,
	})
	if err != nil {
		t.Fatalf("ProposeChange failed: %v", err)
	}
	if card.Status != StatusConfirmed {
		t.Fatalf("expected clear request to be immediately %q, got %q", StatusConfirmed, card.Status)
	}

	preview, err := cp.PreviewArchitecture(ctx, &genscantle.PreviewArchitecturePayload{
		ChangeID:        card.ChangeID,
		RequestRevision: card.RequestRevision,
		Design:          &genscantle.DesignSnapshot{Files: testmodule.Snapshot(t, "", candidateDesign)},
	})
	if err != nil {
		t.Fatalf("PreviewArchitecture failed: %v", err)
	}
	if preview.SystemName != "orders" || len(preview.Containers) != 3 || len(preview.Edges) != 2 {
		t.Fatalf("unexpected CP2 topology: system=%q containers=%d edges=%d", preview.SystemName, len(preview.Containers), len(preview.Edges))
	}
	if len(preview.AgentPolicies) != 1 {
		t.Fatalf("expected 1 agent policy, got %d", len(preview.AgentPolicies))
	}
	pol := preview.AgentPolicies[0]
	if pol.MaxExecutionSeconds != 120 || pol.MaxToolCalls != 6 || pol.MaxRecoveryTurns != 2 || pol.CompressAtTurns != 20 || pol.KeepRecentTurns != 6 || !pol.CacheAfterSystem || !pol.CacheAfterTools {
		t.Fatalf("unexpected evaluated AgentPolicyItem: %+v", pol)
	}
	if len(preview.Tools) != 1 || !preview.Tools[0].RequiresConfirmation || len(preview.Tools[0].InjectedFields) != 1 || preview.Tools[0].InjectedFields[0] != "tenant_id" {
		t.Fatalf("unexpected evaluated ToolContractItem: %+v", preview.Tools)
	}
	if preview.Swarm == nil || preview.Swarm.Status != string(swarm.StatusUnconfigured) || len(preview.Swarm.MissingRoles) != 4 || preview.Swarm.Manifest != nil {
		t.Fatalf("expected unconfigured swarm plan when no SwarmProfiles are configured, got %+v", preview.Swarm)
	}

	// CP3: Fast Draft Mode updates the bound TargetPromptUpdater and rejects compiled_rule hacks.
	promptID := "orders.refund.system"
	draftPrompt, err := cp.DraftOverride(ctx, &genscantle.DraftOverridePayload{
		TargetKind: TargetKindPromptTemplate,
		PromptID:   &promptID,
		NewContent: "You are a concise, empathetic refund specialist.",
	})
	if err != nil {
		t.Fatalf("DraftOverride(prompt_template) failed: %v", err)
	}
	if !draftPrompt.AppliedInStore || draftPrompt.RequiresRebuild || draftPrompt.PromptVersion == nil || *draftPrompt.PromptVersion == "" {
		t.Fatalf("unexpected prompt_template DraftOverrideResult: %+v", draftPrompt)
	}

	// Verified bearer-token authenticator rejects unauthenticated HTTP requests and accepts valid bearer token.
	auth := NewTokenAuthenticator(map[string]Principal{
		"secret-token-alice": {ID: "alice@example.com"},
	})
	srv := httptest.NewServer(NewHTTPHandler(cp, auth))
	defer srv.Close()

	unauthResp, err := http.Get(srv.URL + "/ui/checkpoint")
	if err != nil {
		t.Fatalf("GET /ui/checkpoint failed: %v", err)
	}
	if closeErr := unauthResp.Body.Close(); closeErr != nil {
		t.Fatalf("close unauthResp: %v", closeErr)
	}
	if unauthResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized without bearer token, got %d", unauthResp.StatusCode)
	}

	authReq, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/ui/checkpoint", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	authReq.Header.Set("Authorization", "Bearer secret-token-alice")
	authResp, err := http.DefaultClient.Do(authReq)
	if err != nil {
		t.Fatalf("authenticated GET /ui/checkpoint failed: %v", err)
	}
	if closeErr := authResp.Body.Close(); closeErr != nil {
		t.Fatalf("close authResp: %v", closeErr)
	}
	if authResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK with valid bearer token, got %d", authResp.StatusCode)
	}

	// MCP Adapter ToolsList (5 tools) and ResourcesRead.
	adapter := genmcpadapter.NewMCPAdapter(genscantle.NewEndpoints(cp), nil)
	toolsRes, err := adapter.ToolsList(ctx, &genmcpadapter.ToolsListPayload{})
	if err != nil || len(toolsRes.Tools) != 5 {
		t.Fatalf("expected 5 MCP tools from ToolsList, got %+v (err=%v)", toolsRes, err)
	}
	resRead, err := adapter.ResourcesRead(ctx, &genmcpadapter.ResourcesReadPayload{URI: "ui://scantle/checkpoint"})
	if err != nil {
		t.Fatalf("ResourcesRead(ui://scantle/checkpoint) failed: %v", err)
	}
	completeRes, ok := resRead.Outcome.AsComplete()
	if !ok || len(completeRes.Contents) != 1 || completeRes.Contents[0].Text == nil || !strings.Contains(*completeRes.Contents[0].Text, "scantle-checkpoint-root") {
		t.Fatalf("unexpected ResourcesRead content: %+v", resRead)
	}

	goldenRes, err := adapter.ResourcesRead(ctx, &genmcpadapter.ResourcesReadPayload{URI: goldenpath.ResourceURI})
	if err != nil {
		t.Fatalf("ResourcesRead(%s) failed: %v", goldenpath.ResourceURI, err)
	}
	goldenComplete, ok := goldenRes.Outcome.AsComplete()
	if !ok || len(goldenComplete.Contents) != 1 || goldenComplete.Contents[0].Text == nil || *goldenComplete.Contents[0].Text != goldenpath.PlaybookMarkdown() {
		t.Fatalf("unexpected scantle://golden-path content: %+v", goldenRes)
	}

	// CP2 with server-configured multi-family provider profiles proposes cross-family swarm roster.
	cpWithSwarm := New(Options{
		Analyzer: NewModelCP1Analyzer(newStubModelClient(t, string(rawBytes))),
		SwarmProfiles: []swarm.ProviderProfile{
			{
				ID:            "claude-opus",
				Provider:      "claude",
				Model:         "claude-opus-4-6",
				Family:        "anthropic",
				Authenticated: true,
				Capabilities: []swarm.RoleKind{
					swarm.RoleArchitect,
					swarm.RoleBuilder,
					swarm.RoleEvalAuthor,
					swarm.RoleReviewer,
				},
			},
			{
				ID:            "codex-gpt5",
				Provider:      "codex",
				Model:         "gpt-5.4",
				Family:        "openai",
				Authenticated: true,
				Capabilities: []swarm.RoleKind{
					swarm.RoleBuilder,
					swarm.RoleEvalAuthor,
					swarm.RoleReviewer,
				},
			},
			{
				ID:            "gemini-pro",
				Provider:      "gemini",
				Model:         "gemini-3.1-pro",
				Family:        "google",
				Authenticated: true,
				Capabilities: []swarm.RoleKind{
					swarm.RoleArchitect,
					swarm.RoleEvalAuthor,
					swarm.RoleReviewer,
				},
			},
		},
	})
	cardSwarm, err := cpWithSwarm.ProposeChange(ctx, &genscantle.ProposeChangePayload{
		ChangeID:     "chg-swarm",
		Prompt:       "Auto-approve when item_subtotal_cents < 5000; require manager when item_subtotal_cents >= 5000.",
		DesignSource: &candidateDesign,
	})
	if err != nil {
		t.Fatalf("ProposeChange(chg-swarm) failed: %v", err)
	}
	previewSwarm, err := cpWithSwarm.PreviewArchitecture(ctx, &genscantle.PreviewArchitecturePayload{
		ChangeID:        cardSwarm.ChangeID,
		RequestRevision: cardSwarm.RequestRevision,
		Design:          &genscantle.DesignSnapshot{Files: testmodule.Snapshot(t, "", candidateDesign)},
		Builders: []*genscantle.BuilderScopeItem{
			{Name: "orders", Paths: []string{"internal/orders/**"}},
			{Name: "api", Paths: []string{"cmd/orders-api/**"}},
		},
	})
	if err != nil {
		t.Fatalf("PreviewArchitecture(chg-swarm) failed: %v", err)
	}
	if previewSwarm.Swarm == nil ||
		previewSwarm.Swarm.Status != string(swarm.StatusConfigured) ||
		!previewSwarm.Swarm.CrossFamilyReview ||
		!previewSwarm.Swarm.CrossFamilyEval ||
		previewSwarm.Swarm.SingleFamily ||
		len(previewSwarm.Swarm.Assignments) != 5 ||
		previewSwarm.Swarm.Manifest == nil ||
		!strings.Contains(*previewSwarm.Swarm.Manifest, previewSwarm.DesignDigest) {
		t.Fatalf("unexpected configured previewSwarm.Swarm: %+v", previewSwarm.Swarm)
	}
}

// TestCP1AnalyzerSendsOutputAllowanceThroughAnthropicAdapter runs the CP1
// analyzer through the real Anthropic adapter configured as NewFromAPIKey
// configures it (default model only, no client-wide MaxTokens). The request
// must carry cp1AnalysisMaxOutputTokens, and a response stopped at that
// allowance must fail without storing a Change Card, whether or not its JSON
// happens to parse.
func TestCP1AnalyzerSendsOutputAllowanceThroughAnthropicAdapter(t *testing.T) {
	analysis, err := json.Marshal(CP1SemanticAnalysis{
		Summary:            "Give VIP customers instant store credit up to $150.",
		Ambiguities:        []CP1ModelAmbiguity{},
		Examples:           []CP1ModelExample{{Scenario: "VIP refund", InputSummary: "$120.00 total", ExpectedOutcome: "Instant store credit", VerificationKind: "deterministic"}},
		PreservedBehaviors: []string{},
	})
	if err != nil {
		t.Fatalf("marshal analysis: %v", err)
	}
	ctx := WithPrincipal(context.Background(), Principal{ID: "operator@example.com"})

	cases := []struct {
		name       string
		stopReason string
		text       string
		wantErr    bool
	}{
		{name: "complete response", stopReason: "end_turn", text: string(analysis)},
		{name: "malformed response cut off at the allowance", stopReason: "max_tokens", text: string(analysis[:len(analysis)/2]), wantErr: true},
		{name: "valid JSON stopped at the allowance", stopReason: "max_tokens", text: string(analysis), wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msgs := &recordingAnthropicMessages{resp: anthropicTextMessage(t, tc.text, tc.stopReason)}
			client, err := anthropic.New(msgs, anthropic.Options{DefaultModel: "claude-sonnet-4-5"})
			if err != nil {
				t.Fatalf("anthropic.New: %v", err)
			}
			cp := New(Options{Analyzer: NewModelCP1Analyzer(client)})

			card, err := cp.ProposeChange(ctx, &genscantle.ProposeChangePayload{ChangeID: "vip", Prompt: "VIP refunds up to $150."})
			if msgs.calls != 1 {
				t.Fatalf("Anthropic Messages.New calls = %d, want 1 (err: %v)", msgs.calls, err)
			}
			if got := msgs.last.MaxTokens; got != cp1AnalysisMaxOutputTokens {
				t.Fatalf("encoded max_tokens = %d, want %d", got, cp1AnalysisMaxOutputTokens)
			}
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("ProposeChange: %v", err)
				}
				if card.Status != StatusConfirmed {
					t.Fatalf("status = %q, want %q", card.Status, StatusConfirmed)
				}
				return
			}
			if err == nil {
				t.Fatal("ProposeChange succeeded on a cut-off response")
			}
			cp.mu.RLock()
			_, stored := cp.cards["vip"]
			cp.mu.RUnlock()
			if stored {
				t.Fatal("cut-off response stored a Change Card")
			}
		})
	}
}

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

// Complete returns the preconfigured JSON payload as an assistant TextPart.
func (s *stubProvider) Complete(_ context.Context, _ *model.Request) (*model.Response, error) {
	return &model.Response{
		Content: []model.Message{
			{
				Role:  model.ConversationRoleAssistant,
				Parts: []model.Part{model.TextPart{Text: s.responseJSON}},
			},
		},
		StopReason: "stop",
	}, nil
}

// Stream returns an error because ModelCP1Analyzer uses synchronous Complete calls.
func (s *stubProvider) Stream(_ context.Context, _ *model.Request) (model.Streamer, error) {
	return nil, errors.New("streaming not used by CP1 analyzer")
}

// ApplyPromptOverride writes newContent into the target prompt.Store and returns the resolved version.
func (u *boundTargetPromptUpdater) ApplyPromptOverride(ctx context.Context, promptID, newContent string) (string, error) {
	id := prompt.Ident(promptID)
	if err := u.store.Set(ctx, id, prompt.Scope{}, newContent, map[string]string{"source": "cp3_fast_draft"}); err != nil {
		return "", err
	}
	override, err := u.store.Resolve(ctx, id, prompt.Scope{})
	if err != nil || override == nil {
		return "", err
	}
	return override.Version, nil
}

// New records body and returns the fixed message.
func (m *recordingAnthropicMessages) New(_ context.Context, body anthropicsdk.MessageNewParams, _ ...option.RequestOption) (*anthropicsdk.Message, error) {
	m.last = body
	m.calls++
	return m.resp, nil
}

// NewStreaming is unused because the CP1 analyzer only calls Complete.
func (m *recordingAnthropicMessages) NewStreaming(context.Context, anthropicsdk.MessageNewParams, ...option.RequestOption) *ssestream.Stream[anthropicsdk.MessageStreamEventUnion] {
	panic("streaming not used by CP1 analyzer")
}

// CountTokens is unused because the CP1 analyzer does not count tokens.
func (m *recordingAnthropicMessages) CountTokens(context.Context, anthropicsdk.MessageCountTokensParams, ...option.RequestOption) (*anthropicsdk.MessageTokensCount, error) {
	return nil, errors.New("token counting not used by CP1 analyzer")
}

// newStubModelClient wraps a stubProvider in a goa-ai model.Client.
func newStubModelClient(t *testing.T, responseJSON string) model.Client {
	t.Helper()
	client, err := model.NewClient(&stubProvider{responseJSON: responseJSON})
	if err != nil {
		t.Fatalf("model.NewClient: %v", err)
	}
	return client
}

// anthropicTextMessage decodes a provider-shaped assistant message holding
// text with the given stop reason, as the Anthropic API would return it.
func anthropicTextMessage(t *testing.T, text, stopReason string) *anthropicsdk.Message {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"id":          "msg_test",
		"type":        "message",
		"role":        "assistant",
		"model":       "claude-sonnet-4-5",
		"content":     []map[string]any{{"type": "text", "text": text}},
		"stop_reason": stopReason,
		"usage":       map[string]any{"input_tokens": 10, "output_tokens": 20},
	})
	if err != nil {
		t.Fatalf("marshal message: %v", err)
	}
	var msg anthropicsdk.Message
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatalf("decode message: %v", err)
	}
	return &msg
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
