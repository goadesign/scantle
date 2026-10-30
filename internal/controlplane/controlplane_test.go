// Package controlplane tests Checkpoints 1 through 3 (CP1–CP3) of the Scantle
// MCP software factory control plane.
package controlplane

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	genmcpadapter "github.com/goadesign/scantle/gen/mcp_scantle"
	genscantle "github.com/goadesign/scantle/gen/scantle"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/prompt"
)

type (
	// stubModelClient implements ModelCompleter for testing ModelCP1Analyzer.
	stubModelClient struct {
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

func (s *stubModelClient) Complete(_ context.Context, _ *model.Request) (*model.Response, error) {
	return &model.Response{
		Content: []model.Message{
			{
				Role:  model.ConversationRoleAssistant,
				Parts: []model.Part{model.TextPart{Text: s.responseJSON}},
			},
		},
	}, nil
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

func TestCP1AmbiguityResolutionStrictValidationOwnershipAndConfirmationLock(t *testing.T) {
	aliceCtx := WithPrincipal(context.Background(), Principal{ID: "alice@example.com"})
	bobCtx := WithPrincipal(context.Background(), Principal{ID: "bob@example.com"})

	ambiguousAnalysis := CP1SemanticAnalysis{
		Summary: "Auto-approve refunds under $50 and route larger refunds to manager approval.",
		Ambiguities: []*genscantle.AmbiguityQuestion{
			{
				ID:       "amount-basis",
				Question: "Does the $50 threshold apply to item subtotal or total order amount including tax?",
				Options:  []string{"item_subtotal_cents", "total_order_cents"},
			},
			{
				ID:       "exact-boundary",
				Question: "Should an exact $50.00 refund auto-approve or require manager approval?",
				Options:  []string{"auto_approve_lte_5000", "require_manager_at_5000"},
			},
		},
		Examples: []*genscantle.ExampleItem{
			{
				Scenario:         "Refund under $50 auto-approves",
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
			"Cross-tenant order lookups return 404 Not Found.",
			"Customer principals cannot call approve_refund.",
		},
	}
	rawBytes, err := json.Marshal(ambiguousAnalysis)
	if err != nil {
		t.Fatalf("marshal analysis: %v", err)
	}

	cp := New(Options{
		Analyzer: NewModelCP1Analyzer(&stubModelClient{responseJSON: string(rawBytes)}),
	})

	designSrc := `package design // orders service`
	// Unauthenticated call must fail closed.
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
	if len(proposed.Ambiguities) != 2 {
		t.Fatalf("expected 2 ambiguities, got %d", len(proposed.Ambiguities))
	}
	if len(proposed.Examples) != 3 || proposed.Examples[0].ID != "chg-refund-50-check-01" {
		t.Fatalf("unexpected examples: %+v", proposed.Examples)
	}

	// Non-owner principal cannot confirm Alice's Change Card.
	if _, err := cp.ConfirmIntent(bobCtx, &genscantle.ConfirmIntentPayload{
		ChangeID: "chg-refund-50",
		Answers: map[string]string{
			"amount-basis":   "item_subtotal_cents",
			"exact-boundary": "require_manager_at_5000",
		},
		Examples: proposed.Examples,
	}); err == nil {
		t.Fatal("expected ConfirmIntent by non-owner principal to be rejected")
	}

	// Missing answer, invalid option, or extra unknown ambiguity ID must fail.
	if _, err := cp.ConfirmIntent(aliceCtx, &genscantle.ConfirmIntentPayload{
		ChangeID: "chg-refund-50",
		Answers:  map[string]string{"amount-basis": "item_subtotal_cents"},
		Examples: proposed.Examples,
	}); err == nil {
		t.Fatal("expected ConfirmIntent to reject incomplete ambiguity answers")
	}
	if _, err := cp.ConfirmIntent(aliceCtx, &genscantle.ConfirmIntentPayload{
		ChangeID: "chg-refund-50",
		Answers: map[string]string{
			"amount-basis":   "not-a-valid-option",
			"exact-boundary": "require_manager_at_5000",
		},
		Examples: proposed.Examples,
	}); err == nil {
		t.Fatal("expected ConfirmIntent to reject option not in Ambiguities.Options")
	}
	if _, err := cp.ConfirmIntent(aliceCtx, &genscantle.ConfirmIntentPayload{
		ChangeID: "chg-refund-50",
		Answers: map[string]string{
			"amount-basis":   "item_subtotal_cents",
			"exact-boundary": "require_manager_at_5000",
			"extra-id":       "value",
		},
		Examples: proposed.Examples,
	}); err == nil {
		t.Fatal("expected ConfirmIntent to reject unknown ambiguity key")
	}

	confirmed, err := cp.ConfirmIntent(aliceCtx, &genscantle.ConfirmIntentPayload{
		ChangeID: "chg-refund-50",
		Answers: map[string]string{
			"amount-basis":   "item_subtotal_cents",
			"exact-boundary": "require_manager_at_5000",
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

func TestCP1RejectsMalformedModelAnalysis(t *testing.T) {
	ctx := WithPrincipal(context.Background(), Principal{ID: "alice@example.com"})
	invalidKindAnalysis := `{"summary":"Valid summary","ambiguities":[],"examples":[{"scenario":"s","input_summary":"i","expected_outcome":"o","verification_kind":"bogus_kind"}],"preserved_behaviors":[]}`
	cp := New(Options{
		Analyzer: NewModelCP1Analyzer(&stubModelClient{responseJSON: invalidKindAnalysis}),
	})
	if _, err := cp.ProposeChange(ctx, &genscantle.ProposeChangePayload{
		ChangeID: "chg-bad-kind",
		Prompt:   "Test invalid verification_kind rejection",
	}); err == nil {
		t.Fatal("expected ProposeChange to reject invalid verification_kind")
	}
}

func TestCP2PreviewArchitectureAndCP3FastDraftModeAndMCPAdapter(t *testing.T) {
	ctx := WithPrincipal(context.Background(), Principal{ID: "alice@example.com"})
	clearAnalysis := CP1SemanticAnalysis{
		Summary:     "Refunds with item_subtotal_cents < 5000 auto-approve; >= 5000 require manager approval.",
		Ambiguities: nil,
		Examples: []*genscantle.ExampleItem{
			{
				ID:               "check-subtotal-4999",
				Scenario:         "Subtotal 4999 cents auto-approves",
				InputSummary:     "item_subtotal_cents=4999",
				ExpectedOutcome:  "Auto-approved",
				VerificationKind: "deterministic",
			},
		},
		PreservedBehaviors: []string{"Idempotency key refund/<tenant>/<order>"},
	}
	rawBytes, _ := json.Marshal(clearAnalysis)

	targetStore := prompt.NewInMemoryStore()
	cp := New(Options{
		Analyzer:      NewModelCP1Analyzer(&stubModelClient{responseJSON: string(rawBytes)}),
		TargetUpdater: &boundTargetPromptUpdater{store: targetStore},
	})

	candidateDesign := `package design

import (
	"time"
	. "goa.design/goa/v3/dsl"
	. "goa.design/goa-ai/dsl"
)

var _ = API("orders", func() {})

var RefundTools = Toolset("refund_tools", func() {
	Description("Order lookup and payment refund tools")
	Tool("issue_refund", "Issue a payment refund", func() {})
})

var _ = Service("orders", func() {
	Description("Public Orders HTTP and MCP service")
	Agent("refund_agent", "Executes durable order refund workflows", func() {
		Use(RefundTools)
		RunPolicy(func() {
			TimeBudget(2 * time.Minute)
			MaxToolCalls(6)
			History(func() {
				Compress(20, 6)
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

	// CP2: PreviewArchitecture extracts C4 topology, edges, and guardrails from the Goa design AST.
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
	if preview.Guardrails.MaxExecutionSeconds != 120 || preview.Guardrails.MaxToolCalls != 6 || preview.Guardrails.CompressAtTurns != 20 || preview.Guardrails.KeepRecentTurns != 6 {
		t.Fatalf("unexpected CP2 guardrails: %+v", preview.Guardrails)
	}

	// CP2 must reject mismatched request_revision or undeclared toolset references.
	if _, err := cp.PreviewArchitecture(ctx, &genscantle.PreviewArchitecturePayload{
		ChangeID:        card.ChangeID,
		RequestRevision: "stale-revision",
		DesignSource:    candidateDesign,
	}); err == nil {
		t.Fatal("expected PreviewArchitecture to reject stale request_revision")
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
	resolved, err := targetStore.Resolve(ctx, prompt.Ident(promptID), prompt.Scope{})
	if err != nil || resolved == nil || resolved.Template != "You are a concise, empathetic refund specialist." {
		t.Fatalf("expected bound target prompt.Store to contain updated template, got %+v (err=%v)", resolved, err)
	}

	draftRule, err := cp.DraftOverride(ctx, &genscantle.DraftOverridePayload{
		TargetKind: TargetKindCompiledRule,
		NewContent: "Change $50 threshold to $100 in prompt",
	})
	if err != nil {
		t.Fatalf("DraftOverride(compiled_rule) failed: %v", err)
	}
	if draftRule.AppliedInStore || !draftRule.RequiresRebuild {
		t.Fatalf("expected compiled_rule override to require rebuild and not apply in store: %+v", draftRule)
	}

	// MCP Adapter ToolsList (5 tools: propose_change, confirm_intent, preview_architecture, draft_override, diff) and ResourcesRead.
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
