// Package controlplane implements the Scantle MCP software factory control
// plane across Checkpoints 1 through 3 (CP1–CP3): intent & ambiguity
// resolution, C4 architecture & cost/safety guardrail preview, interactive
// sandbox role-switching, Fast Draft Mode prompt overrides, run-log example
// promotion, and ToolUI/OpenUI completed-composition gating.
package controlplane

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	genscantlehttp "github.com/goadesign/scantle/gen/http/scantle/server"
	genmcpscantle "github.com/goadesign/scantle/gen/jsonrpc/mcp_scantle/server"
	genmcpadapter "github.com/goadesign/scantle/gen/mcp_scantle"
	genscantle "github.com/goadesign/scantle/gen/scantle"
	"github.com/goadesign/scantle/internal/diff"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/prompt"
	"goa.design/goa-ai/runtime/agent/storage"
	"goa.design/goa-ai/runtime/agent/transcript"
	goahttp "goa.design/goa/v3/http"
)

type (
	// CP1Analyzer performs semantic Checkpoint 1 analysis of an operator feature
	// request against the current Goa design source.
	CP1Analyzer interface {
		// Analyze inspects the prompt and design source and returns structured
		// summary, open domain questions, concrete examples, and preserved
		// behaviors.
		Analyze(ctx context.Context, promptText, designSource string) (CP1SemanticAnalysis, error)
	}

	// CP1SemanticAnalysis is the structured semantic output returned by a
	// CP1Analyzer model call.
	CP1SemanticAnalysis struct {
		// Summary is the plain-English description of the requested behavior.
		Summary string `json:"summary"`
		// Ambiguities lists genuine domain questions that require human confirmation.
		Ambiguities []*genscantle.AmbiguityQuestion `json:"ambiguities"`
		// Examples lists concrete input/outcome examples for the Change Card.
		Examples []*genscantle.ExampleItem `json:"examples"`
		// PreservedBehaviors lists existing contract invariants preserved by the change.
		PreservedBehaviors []string `json:"preserved_behaviors"`
	}

	// ModelCompleter is the subset of goa-ai model.Client required by
	// ModelCP1Analyzer. Every goa-ai model.Client satisfies this interface.
	ModelCompleter interface {
		// Complete executes a single model request and returns its response.
		Complete(ctx context.Context, req *model.Request) (*model.Response, error)
	}

	// ModelCP1Analyzer implements CP1Analyzer using a Goa-AI model.Client with
	// structured JSON output.
	ModelCP1Analyzer struct {
		// client is the underlying Goa-AI model completer.
		client ModelCompleter
	}

	// TokenSource issues scoped bearer tokens for Checkpoint 3 Role Switcher
	// requests in sim or staging environments without giving the control plane a
	// production signing key.
	TokenSource interface {
		// IssueToken returns a bearer token for the given environment ("sim" or
		// "staging") and role (for example, "customer" or "manager").
		IssueToken(ctx context.Context, environment, role string) (string, error)
	}

	// C4Container describes one runtime container in a Checkpoint 2 C4 preview.
	C4Container struct {
		// ID is the stable container identifier.
		ID string
		// Name is the human-readable container name.
		Name string
		// Kind identifies the container role (service, worker, store, or external_api).
		Kind string
		// Description summarizes the container's responsibility.
		Description string
	}

	// C4Edge describes a directed dependency between two C4 containers.
	C4Edge struct {
		// FromID is the caller container ID.
		FromID string
		// ToID is the callee container ID.
		ToID string
		// Protocol describes the transport or contract boundary.
		Protocol string
	}

	// GuardrailSummary reports the active Goa-AI cost and safety guardrails
	// displayed at Checkpoint 2.
	GuardrailSummary struct {
		// MaxExecutionTime is the hard per-run wall-clock timeout.
		MaxExecutionTime time.Duration
		// MaxToolCalls is the retrieval tool-call budget per run.
		MaxToolCalls int
		// CompressThreshold is the context-window token threshold for history compression.
		CompressThreshold int
		// RetainRecentTurns is the number of recent turns preserved uncompressed.
		RetainRecentTurns int
	}

	// ArchitectureSpec supplies the container topology and runtime policy for
	// Checkpoint 2 preview generation.
	ArchitectureSpec struct {
		// SystemName is the target platform name.
		SystemName string
		// Containers lists the C4 containers in the target topology.
		Containers []C4Container
		// Edges lists the directed relationships between containers.
		Edges []C4Edge
		// Guardrails holds the active Goa-AI cost and safety budgets.
		Guardrails GuardrailSummary
	}

	// ArchitecturePreview is the Checkpoint 2 output rendered before sandbox
	// startup.
	ArchitecturePreview struct {
		// SystemName is the target platform name.
		SystemName string
		// Containers is the validated C4 container list.
		Containers []C4Container
		// Edges is the validated C4 relationship list.
		Edges []C4Edge
		// Guardrails is the active cost/safety guardrail configuration.
		Guardrails GuardrailSummary
		// Banner summarizes the topology and guardrail budgets in plain English.
		Banner string
	}

	// SandboxEnvironment identifies the isolated Checkpoint 3 execution target
	// and its expected candidate binary digest.
	SandboxEnvironment struct {
		// Profile is the environment profile ("sim" or "staging").
		Profile string
		// ExpectedBinaryDigest is the SHA-256 hex digest of the built candidate binary.
		ExpectedBinaryDigest string
	}

	// PromotedExample is a canonical regression test case created from a real
	// Checkpoint 3 run via `[+ Make This an Example]`.
	PromotedExample struct {
		// ScenarioID is the stable evaluator scenario identifier.
		ScenarioID string
		// RunID is the source run identifier in the canonical run log.
		RunID string
		// InputSummary describes the user request and fixture state.
		InputSummary string
		// ExpectedOutcome is the human-confirmed expected outcome (never self-graded).
		ExpectedOutcome string
		// VerificationKind is "deterministic" or "model_judged".
		VerificationKind string
		// Transcript is the canonical provider-ready message trajectory replayed from runlog.
		Transcript []*model.Message
	}

	// ToolUIAction describes one interactive button or action inside a
	// completed OpenUI/ToolUI composition.
	ToolUIAction struct {
		// ID is the stable action identifier.
		ID string `json:"id"`
		// Label is the human-readable button label.
		Label string `json:"label"`
	}

	// ToolUIComposition is the completed JSON payload validated before an MCP
	// Apps (`ui://`) host enables interactive action dispatch.
	ToolUIComposition struct {
		// Schema identifies the UI composition schema ("openui.v1").
		Schema string `json:"schema"`
		// Component names the root UI widget to render.
		Component string `json:"component"`
		// Props holds the structured properties passed to Component.
		Props map[string]any `json:"props"`
		// Actions lists the interactive actions exposed by the widget.
		Actions []ToolUIAction `json:"actions"`
	}

	// ToolUIValidationResult reports whether a ToolUI/OpenUI composition passed
	// completed-JSON validation while always preserving the ordinary text result.
	ToolUIValidationResult struct {
		// Valid is true when the JSON composition conforms to openui.v1.
		Valid bool
		// ActionDispatchEnabled is true only when Valid is true.
		ActionDispatchEnabled bool
		// Composition is the parsed composition when Valid is true.
		Composition *ToolUIComposition
		// FallbackText is the ordinary text result preserved whether or not UI validation succeeds.
		FallbackText string
		// ValidationError records why ActionDispatchEnabled was blocked when Valid is false.
		ValidationError string
	}

	// Options configures a ControlPlane instance.
	Options struct {
		// Analyzer performs CP1 semantic analysis via a model call.
		Analyzer CP1Analyzer
		// PromptStore persists CP3 Fast Draft Mode prompt overrides.
		PromptStore prompt.Store
		// TokenSource issues CP3 Role Switcher bearer tokens for sim or staging.
		TokenSource TokenSource
		// HTTPClient dispatches CP3 Role Switcher HTTP requests against the candidate.
		HTTPClient *http.Client
	}

	// ControlPlane implements genscantle.Service and the CP1–CP3 control-plane
	// workflows.
	ControlPlane struct {
		// mu guards cards.
		mu sync.RWMutex
		// cards stores pending and confirmed CP1 Change Cards by change_id.
		cards map[string]*genscantle.ChangeCardResult
		// analyzer performs semantic CP1 analysis.
		analyzer CP1Analyzer
		// promptStore backs CP3 Fast Draft Mode 0-second prompt hot reloads.
		promptStore prompt.Store
		// tokenSource issues CP3 Role Switcher tokens for sim or staging.
		tokenSource TokenSource
		// httpClient executes CP3 Role Switcher requests against the candidate API.
		httpClient *http.Client
	}
)

const (
	// StatusNeedsConfirmation indicates CP1 halted on genuine domain ambiguity.
	StatusNeedsConfirmation = "needs_confirmation"
	// StatusConfirmed indicates CP1 is locked and ready for codegen/verification.
	StatusConfirmed = "confirmed"
	// TargetKindPromptTemplate identifies a 0-second prompt.Store override in CP3.
	TargetKindPromptTemplate = "prompt_template"
	// TargetKindCompiledRule identifies a compiled Go business rule that cannot be overridden via prompt.Store.
	TargetKindCompiledRule = "compiled_rule"
	// OpenUISchemaV1 is the required schema identifier for completed ToolUI/OpenUI compositions.
	OpenUISchemaV1 = "openui.v1"
	// checkpointUIHTML is the MCP Apps (`ui://scantle/checkpoint`) HTML resource shell.
	checkpointUIHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8" />
  <title>Scantle Checkpoint Host</title>
</head>
<body>
  <main id="scantle-checkpoint-root" data-profile="mcp-app" data-schema="openui.v1">
    <h1>Scantle Interactive Checkpoint</h1>
    <p>Renders validated CP1 Change Cards and CP3 Sandbox controls while preserving ordinary text fallback.</p>
  </main>
</body>
</html>`
)

// New constructs a ControlPlane with the provided Options.
func New(opts Options) *ControlPlane {
	store := opts.PromptStore
	if store == nil {
		store = prompt.NewInMemoryStore()
	}
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &ControlPlane{
		cards:       make(map[string]*genscantle.ChangeCardResult),
		analyzer:    opts.Analyzer,
		promptStore: store,
		tokenSource: opts.TokenSource,
		httpClient:  httpClient,
	}
}

// NewHTTPHandler mounts both the Goa HTTP endpoints and the MCP JSON-RPC
// server (`/mcp`) for svc onto a single HTTP handler.
func NewHTTPHandler(svc genscantle.Service) http.Handler {
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
	return mux
}

// NewModelCP1Analyzer constructs a CP1Analyzer backed by a Goa-AI model.Client.
func NewModelCP1Analyzer(client ModelCompleter) *ModelCP1Analyzer {
	return &ModelCP1Analyzer{client: client}
}

// Analyze invokes the underlying model.Client with the operator request and Goa
// design source and decodes the structured CP1SemanticAnalysis JSON response.
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
						Text: "Analyze the operator feature request against the Goa design source. Return JSON with fields: summary, ambiguities, examples, preserved_behaviors.",
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
	}
	resp, err := a.client.Complete(ctx, req)
	if err != nil {
		return CP1SemanticAnalysis{}, fmt.Errorf("cp1 model completion: %w", err)
	}
	raw := extractAssistantText(resp)
	if strings.TrimSpace(raw) == "" {
		return CP1SemanticAnalysis{}, errors.New("cp1 model returned empty analysis")
	}
	var parsed CP1SemanticAnalysis
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return CP1SemanticAnalysis{}, fmt.Errorf("decode cp1 model analysis: %w", err)
	}
	return parsed, nil
}

// CheckpointUI returns the interactive MCP Apps HTML host for `ui://scantle/checkpoint`.
func (c *ControlPlane) CheckpointUI(_ context.Context) (string, error) {
	return checkpointUIHTML, nil
}

// ProposeChange evaluates a feature request at Checkpoint 1 (CP1) via the
// configured CP1Analyzer model call, assigns mechanical check IDs and request
// revision digests, and stops for human confirmation only when genuine domain
// ambiguities are returned.
func (c *ControlPlane) ProposeChange(ctx context.Context, p *genscantle.ProposeChangePayload) (*genscantle.ChangeCardResult, error) {
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

	analysis, err := c.analyzer.Analyze(ctx, p.Prompt, designSrc)
	if err != nil {
		return nil, err
	}

	examples := normalizeExamples(p.ChangeID, analysis.Examples)
	preserved := slices.Clone(analysis.PreservedBehaviors)
	if preserved == nil {
		preserved = []string{}
	}
	ambiguities := cloneAmbiguities(analysis.Ambiguities)

	status := StatusConfirmed
	if len(ambiguities) > 0 {
		status = StatusNeedsConfirmation
	}
	summary := strings.TrimSpace(analysis.Summary)
	if summary == "" {
		summary = p.Prompt
	}

	card := &genscantle.ChangeCardResult{
		ChangeID:           p.ChangeID,
		Status:             status,
		Summary:            summary,
		Ambiguities:        ambiguities,
		Examples:           examples,
		PreservedBehaviors: preserved,
	}
	card.RequestRevision = computeChangeCardRevision(card, nil, "")

	c.mu.Lock()
	c.cards[p.ChangeID] = cloneChangeCard(card)
	c.mu.Unlock()

	return card, nil
}

// ConfirmIntent resolves all open Checkpoint 1 ambiguity questions, records the
// operator's confirmed example table, and freezes the Change Card revision.
func (c *ControlPlane) ConfirmIntent(_ context.Context, p *genscantle.ConfirmIntentPayload) (*genscantle.ChangeCardResult, error) {
	if p == nil || strings.TrimSpace(p.ChangeID) == "" {
		return nil, errors.New("change_id is required")
	}
	if strings.TrimSpace(p.Operator) == "" {
		return nil, errors.New("authenticated operator is required")
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	existing, ok := c.cards[p.ChangeID]
	if !ok {
		return nil, fmt.Errorf("unknown change_id %q", p.ChangeID)
	}

	for _, q := range existing.Ambiguities {
		if q == nil {
			continue
		}
		ans, has := p.Answers[q.ID]
		if !has || strings.TrimSpace(ans) == "" {
			return nil, fmt.Errorf("missing confirmation answer for ambiguity %q", q.ID)
		}
	}

	examples := normalizeExamples(p.ChangeID, p.Examples)
	if len(examples) == 0 {
		examples = cloneExamples(existing.Examples)
	}

	confirmed := &genscantle.ChangeCardResult{
		ChangeID:           existing.ChangeID,
		Status:             StatusConfirmed,
		Summary:            existing.Summary,
		Ambiguities:        nil,
		Examples:           examples,
		PreservedBehaviors: slices.Clone(existing.PreservedBehaviors),
	}
	confirmed.RequestRevision = computeChangeCardRevision(confirmed, p.Answers, p.Operator)
	c.cards[p.ChangeID] = cloneChangeCard(confirmed)
	return confirmed, nil
}

// DraftOverride applies a 0-second prompt override in prompt.Store when
// target_kind is "prompt_template", or refuses to mask a compiled Go business
// rule when target_kind is "compiled_rule".
func (c *ControlPlane) DraftOverride(ctx context.Context, p *genscantle.DraftOverridePayload) (*genscantle.DraftOverrideResult, error) {
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
		id := prompt.Ident(strings.TrimSpace(*p.PromptID))
		if err := c.promptStore.Set(ctx, id, prompt.Scope{}, p.NewContent, map[string]string{"source": "cp3_fast_draft"}); err != nil {
			return nil, fmt.Errorf("persist prompt override: %w", err)
		}
		override, err := c.promptStore.Resolve(ctx, id, prompt.Scope{})
		if err != nil {
			return nil, fmt.Errorf("resolve updated prompt override: %w", err)
		}
		var version string
		if override != nil {
			version = override.Version
		}
		return &genscantle.DraftOverrideResult{
			AppliedInStore:  true,
			RequiresRebuild: false,
			PromptVersion:   &version,
			Message:         fmt.Sprintf("Updated prompt %q in prompt.Store (version %s) for 0s hot reload.", id, version),
		}, nil
	default:
		return nil, fmt.Errorf("unsupported target_kind %q", p.TargetKind)
	}
}

// Diff compares two Goa design sources and go.mod snapshots and returns the
// structural diff without emitting a blanket compatibility verdict.
func (c *ControlPlane) Diff(_ context.Context, p *genscantle.DiffPayload) (*genscantle.DiffResult, error) {
	if p == nil {
		return nil, errors.New("diff payload is required")
	}
	rep, err := diff.Compare(p.OldDesign, p.NewDesign, p.OldGomod, p.NewGomod)
	if err != nil {
		return nil, err
	}
	return &genscantle.DiffResult{
		HasChanges:          rep.HasChanges,
		AddedDeclarations:   rep.AddedDeclarations,
		RemovedDeclarations: rep.RemovedDeclarations,
		VersionDeltas:       rep.VersionDeltas,
		Summary:             rep.Summary,
	}, nil
}

// BuildArchitecturePreview constructs the Checkpoint 2 C4 container topology
// and active Goa-AI cost/safety guardrail summary.
func (c *ControlPlane) BuildArchitecturePreview(spec ArchitectureSpec) (ArchitecturePreview, error) {
	if strings.TrimSpace(spec.SystemName) == "" {
		return ArchitecturePreview{}, errors.New("system name is required")
	}
	if len(spec.Containers) == 0 {
		return ArchitecturePreview{}, errors.New("at least one C4 container is required")
	}
	if spec.Guardrails.MaxExecutionTime <= 0 || spec.Guardrails.MaxToolCalls <= 0 {
		return ArchitecturePreview{}, errors.New("positive MaxExecutionTime and MaxToolCalls guardrails are required")
	}
	banner := fmt.Sprintf(
		"%s: %d container(s), %d boundary edge(s) | Guardrails: MaxExecutionTime=%s, MaxToolCalls=%d, CompressThreshold=%d tokens (retain %d recent turns)",
		spec.SystemName,
		len(spec.Containers),
		len(spec.Edges),
		spec.Guardrails.MaxExecutionTime,
		spec.Guardrails.MaxToolCalls,
		spec.Guardrails.CompressThreshold,
		spec.Guardrails.RetainRecentTurns,
	)
	return ArchitecturePreview{
		SystemName: spec.SystemName,
		Containers: slices.Clone(spec.Containers),
		Edges:      slices.Clone(spec.Edges),
		Guardrails: spec.Guardrails,
		Banner:     banner,
	}, nil
}

// VerifySandboxEnvironment validates that the Checkpoint 3 sandbox declares a
// supported non-production profile ("sim" or "staging") and that the running
// candidate binary matches ExpectedBinaryDigest.
func (c *ControlPlane) VerifySandboxEnvironment(env SandboxEnvironment, actualBinaryDigest string) error {
	if env.Profile != "sim" && env.Profile != "staging" {
		return fmt.Errorf("sandbox profile must be \"sim\" or \"staging\", got %q", env.Profile)
	}
	if strings.TrimSpace(env.ExpectedBinaryDigest) == "" {
		return errors.New("expected candidate binary digest is required")
	}
	if actualBinaryDigest != env.ExpectedBinaryDigest {
		return fmt.Errorf("candidate binary digest mismatch: running %q, expected %q", actualBinaryDigest, env.ExpectedBinaryDigest)
	}
	return nil
}

// SwitchRoleAndInvoke obtains a scoped bearer token for role in env.Profile
// from the external TokenSource and executes req against the candidate's real
// public HTTP endpoint.
func (c *ControlPlane) SwitchRoleAndInvoke(ctx context.Context, env SandboxEnvironment, role string, req *http.Request) (*http.Response, error) {
	if env.Profile != "sim" && env.Profile != "staging" {
		return nil, fmt.Errorf("role switcher only supports sim or staging, got %q", env.Profile)
	}
	if c.tokenSource == nil {
		return nil, errors.New("external token source is required for role switching")
	}
	if req == nil {
		return nil, errors.New("http request is required")
	}
	token, err := c.tokenSource.IssueToken(ctx, env.Profile, role)
	if err != nil {
		return nil, fmt.Errorf("issue %s token for role %q: %w", env.Profile, role, err)
	}
	cloned := req.Clone(ctx)
	cloned.Header.Set("Authorization", "Bearer "+token)
	cloned.Header.Set("X-Scantle-Environment", env.Profile)
	return c.httpClient.Do(cloned)
}

// PromoteRunToExample reconstructs the canonical message trajectory of runID
// from store using transcript.BuildMessagesFromRunLog and pairs it with the
// operator's explicitly confirmed expected outcome.
func (c *ControlPlane) PromoteRunToExample(
	ctx context.Context,
	store storage.Store,
	runID string,
	scenarioID string,
	inputSummary string,
	humanConfirmedOutcome string,
	verificationKind string,
) (PromotedExample, error) {
	if strings.TrimSpace(humanConfirmedOutcome) == "" {
		return PromotedExample{}, errors.New("human-confirmed expected outcome is required to promote a run to an example")
	}
	if verificationKind != "deterministic" && verificationKind != "model_judged" {
		return PromotedExample{}, fmt.Errorf("invalid verification_kind %q", verificationKind)
	}
	msgs, err := transcript.BuildMessagesFromRunLog(ctx, store, runID)
	if err != nil {
		return PromotedExample{}, fmt.Errorf("replay canonical runlog transcript for %q: %w", runID, err)
	}
	if len(msgs) == 0 {
		return PromotedExample{}, fmt.Errorf("run %q has an empty canonical transcript", runID)
	}
	return PromotedExample{
		ScenarioID:       scenarioID,
		RunID:            runID,
		InputSummary:     inputSummary,
		ExpectedOutcome:  humanConfirmedOutcome,
		VerificationKind: verificationKind,
		Transcript:       msgs,
	}, nil
}

// ValidateToolUIComposition validates a completed OpenUI/ToolUI JSON payload
// before enabling interactive action dispatch, while always preserving the
// ordinary fallbackText for non-UI or failed-UI hosts.
func (c *ControlPlane) ValidateToolUIComposition(rawJSON []byte, fallbackText string) ToolUIValidationResult {
	res := ToolUIValidationResult{
		FallbackText: fallbackText,
	}
	if len(bytesTrimSpace(rawJSON)) == 0 {
		res.ValidationError = "empty ToolUI composition JSON"
		return res
	}
	var comp ToolUIComposition
	dec := json.NewDecoder(strings.NewReader(string(rawJSON)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&comp); err != nil {
		res.ValidationError = fmt.Sprintf("invalid ToolUI composition JSON: %v", err)
		return res
	}
	if comp.Schema != OpenUISchemaV1 {
		res.ValidationError = fmt.Sprintf("unsupported ToolUI schema %q (want %q)", comp.Schema, OpenUISchemaV1)
		return res
	}
	if strings.TrimSpace(comp.Component) == "" {
		res.ValidationError = "ToolUI composition requires a non-empty component name"
		return res
	}
	if comp.Props == nil {
		res.ValidationError = "ToolUI composition requires a props object"
		return res
	}
	if len(comp.Actions) == 0 {
		res.ValidationError = "ToolUI composition requires at least one interactive action"
		return res
	}
	for i, act := range comp.Actions {
		if strings.TrimSpace(act.ID) == "" || strings.TrimSpace(act.Label) == "" {
			res.ValidationError = fmt.Sprintf("ToolUI action[%d] requires non-empty id and label", i)
			return res
		}
	}
	res.Valid = true
	res.ActionDispatchEnabled = true
	res.Composition = &comp
	return res
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

// normalizeExamples assigns deterministic check IDs when absent and clones the
// slice.
func normalizeExamples(changeID string, in []*genscantle.ExampleItem) []*genscantle.ExampleItem {
	out := make([]*genscantle.ExampleItem, 0, len(in))
	for i, item := range in {
		if item == nil {
			continue
		}
		cloned := *item
		if strings.TrimSpace(cloned.ID) == "" {
			cloned.ID = fmt.Sprintf("%s-check-%02d", changeID, i+1)
		}
		if cloned.VerificationKind != "deterministic" && cloned.VerificationKind != "model_judged" {
			cloned.VerificationKind = "deterministic"
		}
		out = append(out, &cloned)
	}
	return out
}

// cloneAmbiguities returns a deep copy of an AmbiguityQuestion slice.
func cloneAmbiguities(in []*genscantle.AmbiguityQuestion) []*genscantle.AmbiguityQuestion {
	if len(in) == 0 {
		return nil
	}
	out := make([]*genscantle.AmbiguityQuestion, 0, len(in))
	for _, q := range in {
		if q == nil {
			continue
		}
		out = append(out, &genscantle.AmbiguityQuestion{
			ID:       q.ID,
			Question: q.Question,
			Options:  slices.Clone(q.Options),
		})
	}
	return out
}

// cloneExamples returns a deep copy of an ExampleItem slice.
func cloneExamples(in []*genscantle.ExampleItem) []*genscantle.ExampleItem {
	out := make([]*genscantle.ExampleItem, 0, len(in))
	for _, item := range in {
		if item == nil {
			continue
		}
		cp := *item
		out = append(out, &cp)
	}
	return out
}

// cloneChangeCard returns a deep copy of a ChangeCardResult.
func cloneChangeCard(in *genscantle.ChangeCardResult) *genscantle.ChangeCardResult {
	if in == nil {
		return nil
	}
	return &genscantle.ChangeCardResult{
		ChangeID:           in.ChangeID,
		Status:             in.Status,
		Summary:            in.Summary,
		Ambiguities:        cloneAmbiguities(in.Ambiguities),
		Examples:           cloneExamples(in.Examples),
		PreservedBehaviors: slices.Clone(in.PreservedBehaviors),
		RequestRevision:    in.RequestRevision,
	}
}

// computeChangeCardRevision computes a deterministic SHA-256 hex digest over
// the ChangeCardResult, sorted answers, and confirming operator.
func computeChangeCardRevision(card *genscantle.ChangeCardResult, answers map[string]string, operator string) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "change:%s\nstatus:%s\nsummary:%s\noperator:%s\n", card.ChangeID, card.Status, card.Summary, operator)
	for _, ex := range card.Examples {
		if ex == nil {
			continue
		}
		_, _ = fmt.Fprintf(h, "ex:%s|%s|%s|%s|%s\n", ex.ID, ex.Scenario, ex.InputSummary, ex.ExpectedOutcome, ex.VerificationKind)
	}
	for _, pb := range card.PreservedBehaviors {
		_, _ = fmt.Fprintf(h, "preserved:%s\n", pb)
	}
	keys := make([]string, 0, len(answers))
	for k := range answers {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		_, _ = fmt.Fprintf(h, "ans:%s=%s\n", k, answers[k])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// bytesTrimSpace trims ASCII whitespace from a byte slice without allocating.
func bytesTrimSpace(b []byte) []byte {
	return []byte(strings.TrimSpace(string(b)))
}
