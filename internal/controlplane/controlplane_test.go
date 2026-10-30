// Package controlplane tests Checkpoints 1 through 3 (CP1–CP3) of the Scantle
// MCP software factory control plane.
package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	genmcpadapter "github.com/goadesign/scantle/gen/mcp_scantle"
	genscantle "github.com/goadesign/scantle/gen/scantle"
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
)

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

func (s *stubProvider) Stream(_ context.Context, _ *model.Request) (model.Streamer, error) {
	return nil, errors.New("streaming not used by CP1 analyzer")
}

func newStubModelClient(t *testing.T, responseJSON string) model.Client {
	t.Helper()
	client, err := model.NewClient(&stubProvider{responseJSON: responseJSON})
	if err != nil {
		t.Fatalf("model.NewClient: %v", err)
	}
	return client
}

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
		DesignSource:    candidateDesign,
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
}
