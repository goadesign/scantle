// Package controlplane tests Checkpoints 1 through 3 (CP1–CP3) of the Scantle
// MCP software factory control plane.
package controlplane

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	genmcpadapter "github.com/goadesign/scantle/gen/mcp_scantle"
	genscantle "github.com/goadesign/scantle/gen/scantle"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/prompt"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
	storageinmem "goa.design/goa-ai/runtime/agent/storage/inmem"
	"goa.design/goa-ai/runtime/agent/transcript"
)

type (
	// stubModelClient implements model.Client for testing ModelCP1Analyzer.
	stubModelClient struct {
		// responseJSON is the structured JSON payload returned by Complete.
		responseJSON string
	}

	// simTokenIssuer issues test bearer tokens for sim role-switching tests.
	simTokenIssuer struct{}
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

func (simTokenIssuer) IssueToken(_ context.Context, environment, role string) (string, error) {
	return environment + "-token-for-" + role, nil
}

func TestCP1AmbiguityResolutionViaModelAndConfirmationLock(t *testing.T) {
	ctx := context.Background()
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
	proposed, err := cp.ProposeChange(ctx, &genscantle.ProposeChangePayload{
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

	// Attempting to confirm with missing answers must fail.
	if _, err := cp.ConfirmIntent(ctx, &genscantle.ConfirmIntentPayload{
		ChangeID: "chg-refund-50",
		Operator: "alice@example.com",
		Answers:  map[string]string{"amount-basis": "item_subtotal_cents"},
		Examples: proposed.Examples,
	}); err == nil {
		t.Fatal("expected ConfirmIntent to reject incomplete ambiguity answers")
	}

	confirmed, err := cp.ConfirmIntent(ctx, &genscantle.ConfirmIntentPayload{
		ChangeID: "chg-refund-50",
		Operator: "alice@example.com",
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
	if len(confirmed.Ambiguities) != 0 {
		t.Fatalf("expected ambiguities to be cleared after confirmation, got %d", len(confirmed.Ambiguities))
	}
	if confirmed.RequestRevision == "" || confirmed.RequestRevision == proposed.RequestRevision {
		t.Fatalf("expected new confirmed RequestRevision, got %q (proposed %q)", confirmed.RequestRevision, proposed.RequestRevision)
	}
}

func TestCP1ClearRequestSkipsUnnecessaryConfirmationStop(t *testing.T) {
	ctx := context.Background()
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
	cp := New(Options{
		Analyzer: NewModelCP1Analyzer(&stubModelClient{responseJSON: string(rawBytes)}),
	})

	card, err := cp.ProposeChange(ctx, &genscantle.ProposeChangePayload{
		ChangeID: "chg-clear",
		Prompt:   "Auto-approve when item_subtotal_cents < 5000; require manager when item_subtotal_cents >= 5000.",
	})
	if err != nil {
		t.Fatalf("ProposeChange failed: %v", err)
	}
	if card.Status != StatusConfirmed {
		t.Fatalf("expected clear request to be immediately %q, got %q", StatusConfirmed, card.Status)
	}
}

func TestCP2ArchitecturePreviewAndCP3SandboxRoleSwitcherAndFastDraftMode(t *testing.T) {
	ctx := context.Background()
	promptStore := prompt.NewInMemoryStore()
	cp := New(Options{
		PromptStore: promptStore,
		TokenSource: simTokenIssuer{},
	})

	// CP2: C4 container topology + Goa-AI guardrails.
	preview, err := cp.BuildArchitecturePreview(ArchitectureSpec{
		SystemName: "orders",
		Containers: []C4Container{
			{ID: "orders-api", Name: "Orders HTTP/MCP Service", Kind: "service", Description: "Public Goa HTTP & MCP server"},
			{ID: "orders-worker", Name: "Orders Goa-AI Worker", Kind: "worker", Description: "Executes RefundWorkflow and agent turns"},
		},
		Edges: []C4Edge{
			{FromID: "orders-api", ToID: "orders-worker", Protocol: "Goa-AI Durable Workflow Engine"},
		},
		Guardrails: GuardrailSummary{
			MaxExecutionTime:  2 * time.Minute,
			MaxToolCalls:      6,
			CompressThreshold: 16000,
			RetainRecentTurns: 4,
		},
	})
	if err != nil {
		t.Fatalf("BuildArchitecturePreview failed: %v", err)
	}
	if !strings.Contains(preview.Banner, "MaxToolCalls=6") || !strings.Contains(preview.Banner, "CompressThreshold=16000") {
		t.Fatalf("unexpected CP2 banner: %q", preview.Banner)
	}

	// CP3: Sandbox environment banner & binary digest check.
	env := SandboxEnvironment{
		Profile:              "sim",
		ExpectedBinaryDigest: "sha256:candidate-binary-v1",
	}
	if err := cp.VerifySandboxEnvironment(env, "sha256:candidate-binary-v1"); err != nil {
		t.Fatalf("VerifySandboxEnvironment failed: %v", err)
	}
	if err := cp.VerifySandboxEnvironment(env, "sha256:stale-binary"); err == nil {
		t.Fatal("expected VerifySandboxEnvironment to reject mismatched binary digest")
	}

	// CP3: Role Switcher driving the candidate's real HTTP approval endpoint.
	candidateSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		switch auth {
		case "Bearer sim-token-for-customer":
			http.Error(w, "forbidden: customer cannot approve refund", http.StatusForbidden)
		case "Bearer sim-token-for-manager":
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"status":"approved"}`)
		default:
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		}
	}))
	defer candidateSrv.Close()

	reqCustomer, _ := http.NewRequestWithContext(ctx, http.MethodPost, candidateSrv.URL+"/orders/ord-1/approve", nil)
	respCustomer, err := cp.SwitchRoleAndInvoke(ctx, env, "customer", reqCustomer)
	if err != nil {
		t.Fatalf("SwitchRoleAndInvoke(customer) failed: %v", err)
	}
	_ = respCustomer.Body.Close()
	if respCustomer.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for customer role, got %d", respCustomer.StatusCode)
	}

	reqManager, _ := http.NewRequestWithContext(ctx, http.MethodPost, candidateSrv.URL+"/orders/ord-1/approve", nil)
	respManager, err := cp.SwitchRoleAndInvoke(ctx, env, "manager", reqManager)
	if err != nil {
		t.Fatalf("SwitchRoleAndInvoke(manager) failed: %v", err)
	}
	_ = respManager.Body.Close()
	if respManager.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted for manager role, got %d", respManager.StatusCode)
	}

	// CP3: Fast Draft Mode (0s prompt.Store reload vs compiled Go business rule rejection).
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
	resolved, err := promptStore.Resolve(ctx, prompt.Ident(promptID), prompt.Scope{})
	if err != nil || resolved == nil || resolved.Template != "You are a concise, empathetic refund specialist." {
		t.Fatalf("expected prompt.Store to contain updated template, got %+v (err=%v)", resolved, err)
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
}

func TestCP3PromoteRunToExampleAndMCPAdapter(t *testing.T) {
	ctx := context.Background()
	cp := New(Options{})

	store := newTestRunLogStore(t, ctx, "run-refund-42")
	payload, err := transcript.EncodeRunLogDelta([]*model.Message{
		{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "Please refund order ord-42 ($48 subtotal)."}},
		},
		{
			Role: model.ConversationRoleAssistant,
			Parts: []model.Part{
				model.ToolUsePart{
					ID:    "call-refund-1",
					Name:  "orders.issue_refund",
					Input: rawjson.Message(`{"order_id":"ord-42","amount_cents":4800}`),
				},
			},
		},
		{
			Role: model.ConversationRoleUser,
			Parts: []model.Part{
				model.ToolResultPart{
					ToolUseID: "call-refund-1",
					Content:   map[string]any{"status": "refunded", "refund_id": "ref-1"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("EncodeRunLogDelta failed: %v", err)
	}
	_, err = store.AppendRunRecord(ctx, &runlog.Event{
		EventKey:  "evt-1",
		RunID:     "run-refund-42",
		Subject:   genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent(agent.Ident("orders.refund_agent")))},
		SessionID: "sess-1",
		TurnID:    "turn-1",
		Type:      transcript.RunLogMessagesAppended,
		Payload:   payload,
		Timestamp: time.Now().UTC().Truncate(time.Millisecond),
	})
	if err != nil {
		t.Fatalf("AppendRunRecord failed: %v", err)
	}

	// Self-grading (empty humanConfirmedOutcome) must be rejected.
	if _, err := cp.PromoteRunToExample(ctx, store, "run-refund-42", "scen-42", "Order ord-42 ($48 subtotal)", "", "deterministic"); err == nil {
		t.Fatal("expected PromoteRunToExample to reject empty humanConfirmedOutcome")
	}

	promoted, err := cp.PromoteRunToExample(
		ctx,
		store,
		"run-refund-42",
		"scen-42",
		"Order ord-42 ($48 subtotal)",
		"Calls orders.issue_refund with 4800 cents and completes without manager approval",
		"deterministic",
	)
	if err != nil {
		t.Fatalf("PromoteRunToExample failed: %v", err)
	}
	if len(promoted.Transcript) != 3 {
		t.Fatalf("expected 3 canonical messages in promoted transcript, got %d", len(promoted.Transcript))
	}

	// MCP Adapter ToolsList, ResourcesRead (ui://scantle/checkpoint), and ToolsCall (diff).
	adapter := genmcpadapter.NewMCPAdapter(genscantle.NewEndpoints(cp), nil)
	toolsRes, err := adapter.ToolsList(ctx, &genmcpadapter.ToolsListPayload{})
	if err != nil || len(toolsRes.Tools) != 4 {
		t.Fatalf("expected 4 MCP tools from ToolsList, got %+v (err=%v)", toolsRes, err)
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

func newTestRunLogStore(t *testing.T, ctx context.Context, runID string) *storageinmem.Store {
	t.Helper()
	store := storageinmem.New()
	now := time.Now().UTC().Truncate(time.Millisecond)
	if _, err := store.CreateSession(ctx, "sess-1", now); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	subject := genrunidentity.RunSubject{
		Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("orders.refund_agent")),
	}
	origin := genregistry.ExecutionInvocation{
		Invocation: genregistry.NewExecutionInvocationKindRoot(&genregistry.RootInvocation{RunID: runID}),
	}
	if _, err := store.BeginRunPreparationOperation(ctx, storage.RunPreparationBegin{
		Operation: storage.PreparationOperation{Subject: subject, RunID: runID, SessionID: "sess-1", CommandID: runID},
		Origin:    origin,
	}); err != nil {
		t.Fatalf("BeginRunPreparationOperation: %v", err)
	}
	if _, err := store.BeginRunSeed(ctx, storage.SeedDeclaration{
		Subject: subject, RunID: runID, SessionID: "sess-1", CommandID: runID, AttemptID: runID, Kind: storage.SeedLiteral,
	}); err != nil {
		t.Fatalf("BeginRunSeed: %v", err)
	}
	prepared := []byte(`{}`)
	end, err := store.AppendRunSeed(ctx, storage.SeedAppend{
		RunID:     runID,
		AttemptID: runID,
		Record:    storage.SeedRecord{Key: "prepared", PreviousID: storage.EmptySeedEndID, Prepared: prepared},
	})
	if err != nil {
		t.Fatalf("AppendRunSeed: %v", err)
	}
	if err := store.PublishRunSeed(ctx, storage.SeedPublication{
		RunID: runID, AttemptID: runID, SeedEndID: storage.EmptySeedEndID, EndID: end, PreparedBytes: int64(len(prepared)),
	}); err != nil {
		t.Fatalf("PublishRunSeed: %v", err)
	}
	input, err := hooks.EncodeToRecordInput(
		hooks.NewRunStartedEvent(runID, subject, "sess-1", "", "", nil),
		hooks.EncodeOptions{EventKey: "run-started", TimestampMS: now.UnixMilli()},
	)
	if err != nil {
		t.Fatalf("EncodeToRecordInput: %v", err)
	}
	started := &runlog.Event{
		EventKey:  input.EventKey,
		RunID:     input.RunID,
		Subject:   input.Subject,
		SessionID: input.SessionID,
		TurnID:    input.TurnID,
		Type:      input.Type,
		Payload:   input.Payload,
		Timestamp: time.UnixMilli(input.TimestampMS).UTC(),
	}
	if _, err := store.StartRootRun(ctx, storage.RootRunStart{
		RequestDigest: [32]byte{1},
		Run:           session.RunStart{Subject: subject, RunID: runID, SessionID: "sess-1", SeedEndID: storage.EmptySeedEndID, StartedAt: now},
		Started:       started,
	}); err != nil {
		t.Fatalf("StartRootRun: %v", err)
	}
	return store
}
