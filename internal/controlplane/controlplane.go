// Package controlplane implements the Scantle MCP software factory control
// plane across Checkpoints 1 through 3 (CP1–CP3): authenticated intent &
// ambiguity resolution, AST-derived C4 architecture & cost/safety guardrail
// preview, and target-bound Fast Draft Mode prompt overrides.
package controlplane

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"

	genscantlehttp "github.com/goadesign/scantle/gen/http/scantle/server"
	genmcpscantle "github.com/goadesign/scantle/gen/jsonrpc/mcp_scantle/server"
	genmcpadapter "github.com/goadesign/scantle/gen/mcp_scantle"
	genscantle "github.com/goadesign/scantle/gen/scantle"
	"github.com/goadesign/scantle/internal/diff"
	"goa.design/goa-ai/runtime/agent/model"
	goahttp "goa.design/goa/v3/http"
)

type (
	// Principal identifies the authenticated operator invoking the Scantle control plane.
	Principal struct {
		// ID is the stable authenticated operator identifier (for example, "alice@example.com").
		ID string
	}

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
	// HeaderPrincipal is the HTTP request header carrying the authenticated operator principal ID.
	HeaderPrincipal = "X-Scantle-Principal"
	// checkpointUIHTML is the MCP Apps (`ui://scantle/checkpoint`) HTML resource shell.
	checkpointUIHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8" />
  <title>Scantle Checkpoint Host</title>
</head>
<body>
  <main id="scantle-checkpoint-root" data-profile="mcp-app">
    <h1>Scantle Interactive Checkpoint</h1>
    <p>Renders validated CP1 Change Cards, CP2 C4 Architecture Previews, and CP3 Sandbox controls while preserving ordinary text fallback.</p>
  </main>
</body>
</html>`
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

// New constructs a ControlPlane with the provided Options.
func New(opts Options) *ControlPlane {
	return &ControlPlane{
		cards:         make(map[string]storedChangeCard),
		analyzer:      opts.Analyzer,
		targetUpdater: opts.TargetUpdater,
	}
}

// NewHTTPHandler mounts both the Goa HTTP endpoints and the MCP JSON-RPC
// server (`/mcp`) for svc onto a single HTTP handler, binding the authenticated
// principal from X-Scantle-Principal into the request context.
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

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id := strings.TrimSpace(r.Header.Get(HeaderPrincipal)); id != "" {
			r = r.WithContext(WithPrincipal(r.Context(), Principal{ID: id}))
		}
		mux.ServeHTTP(w, r)
	})
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
// configured CP1Analyzer model call, validates all model-emitted fields
// strictly, binds ownership to the authenticated principal, and stops for human
// confirmation only when genuine domain ambiguities are present.
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
	c.cards[p.ChangeID] = storedChangeCard{
		card:         cloneChangeCard(card),
		owner:        principal.ID,
		designSource: designSrc,
	}
	c.mu.Unlock()

	return card, nil
}

// ConfirmIntent resolves all open Checkpoint 1 ambiguity questions for the
// authenticated card owner, records the confirmed example table and answers,
// and freezes the Change Card revision.
func (c *ControlPlane) ConfirmIntent(ctx context.Context, p *genscantle.ConfirmIntentPayload) (*genscantle.ChangeCardResult, error) {
	principal, ok := PrincipalFromContext(ctx)
	if !ok {
		return nil, errors.New("authenticated principal is required")
	}
	if p == nil || strings.TrimSpace(p.ChangeID) == "" {
		return nil, errors.New("change_id is required")
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

	if err := validateAmbiguityAnswers(existing.Ambiguities, p.Answers); err != nil {
		return nil, err
	}
	examples, err := validateAndNormalizeExamples(p.ChangeID, p.Examples)
	if err != nil {
		return nil, fmt.Errorf("invalid confirmed examples: %w", err)
	}

	confirmedBy := principal.ID
	confirmed := &genscantle.ChangeCardResult{
		ChangeID:           existing.ChangeID,
		Status:             StatusConfirmed,
		Prompt:             existing.Prompt,
		Summary:            existing.Summary,
		Ambiguities:        cloneAmbiguities(existing.Ambiguities),
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
		card:         cloneChangeCard(confirmed),
		owner:        record.owner,
		designSource: record.designSource,
	}
	return confirmed, nil
}

// PreviewArchitecture extracts and validates the Checkpoint 2 C4 container
// topology, directed contract edges, and Goa-AI runtime guardrails from a
// candidate Goa design source bound to a confirmed CP1 Change Card.
func (c *ControlPlane) PreviewArchitecture(ctx context.Context, p *genscantle.PreviewArchitecturePayload) (*genscantle.ArchitecturePreviewResult, error) {
	principal, ok := PrincipalFromContext(ctx)
	if !ok {
		return nil, errors.New("authenticated principal is required")
	}
	if p == nil || strings.TrimSpace(p.ChangeID) == "" || strings.TrimSpace(p.RequestRevision) == "" {
		return nil, errors.New("change_id and request_revision are required")
	}
	if strings.TrimSpace(p.DesignSource) == "" {
		return nil, errors.New("design_source is required")
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

	return extractArchitecturePreview(p.ChangeID, p.RequestRevision, p.DesignSource)
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
func (c *ControlPlane) Diff(_ context.Context, p *genscantle.DiffPayload) (*genscantle.DiffResult, error) {
	if p == nil {
		return nil, errors.New("diff payload is required")
	}
	rep, err := diff.Compare(p.OldDesign, p.NewDesign, p.OldGomod, p.NewGomod)
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
// silent coercion.
func validateCP1Analysis(
	changeID string,
	in CP1SemanticAnalysis,
) (string, []*genscantle.AmbiguityQuestion, []*genscantle.ExampleItem, []string, error) {
	summary := strings.TrimSpace(in.Summary)
	if summary == "" {
		return "", nil, nil, nil, errors.New("summary is required")
	}
	ambiguities, err := validateAmbiguities(in.Ambiguities)
	if err != nil {
		return "", nil, nil, nil, err
	}
	examples, err := validateAndNormalizeExamples(changeID, in.Examples)
	if err != nil {
		return "", nil, nil, nil, err
	}
	preserved := make([]string, 0, len(in.PreservedBehaviors))
	for i, item := range in.PreservedBehaviors {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			return "", nil, nil, nil, fmt.Errorf("preserved_behaviors[%d] is empty", i)
		}
		preserved = append(preserved, trimmed)
	}
	return summary, ambiguities, examples, preserved, nil
}

// validateAmbiguities validates that every ambiguity question has a unique
// non-empty ID, a non-empty question, and at least two distinct non-empty options.
func validateAmbiguities(in []*genscantle.AmbiguityQuestion) ([]*genscantle.AmbiguityQuestion, error) {
	if len(in) == 0 {
		return nil, nil
	}
	seenIDs := make(map[string]bool, len(in))
	out := make([]*genscantle.AmbiguityQuestion, 0, len(in))
	for i, q := range in {
		if q == nil {
			return nil, fmt.Errorf("ambiguities[%d] is nil", i)
		}
		id := strings.TrimSpace(q.ID)
		question := strings.TrimSpace(q.Question)
		if id == "" || question == "" {
			return nil, fmt.Errorf("ambiguities[%d] requires non-empty id and question", i)
		}
		if seenIDs[id] {
			return nil, fmt.Errorf("duplicate ambiguity id %q", id)
		}
		seenIDs[id] = true
		if len(q.Options) < 2 {
			return nil, fmt.Errorf("ambiguity %q requires at least 2 options", id)
		}
		seenOpts := make(map[string]bool, len(q.Options))
		opts := make([]string, 0, len(q.Options))
		for _, opt := range q.Options {
			trimmed := strings.TrimSpace(opt)
			if trimmed == "" {
				return nil, fmt.Errorf("ambiguity %q has an empty option", id)
			}
			if seenOpts[trimmed] {
				return nil, fmt.Errorf("ambiguity %q has duplicate option %q", id, trimmed)
			}
			seenOpts[trimmed] = true
			opts = append(opts, trimmed)
		}
		out = append(out, &genscantle.AmbiguityQuestion{
			ID:       id,
			Question: question,
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

// validateAndNormalizeExamples validates every ExampleItem strictly and assigns
// deterministic check IDs only when ID is omitted by the model.
func validateAndNormalizeExamples(changeID string, in []*genscantle.ExampleItem) ([]*genscantle.ExampleItem, error) {
	if len(in) == 0 {
		return nil, errors.New("at least one example is required")
	}
	seenIDs := make(map[string]bool, len(in))
	out := make([]*genscantle.ExampleItem, 0, len(in))
	for i, item := range in {
		if item == nil {
			return nil, fmt.Errorf("examples[%d] is nil", i)
		}
		scenario := strings.TrimSpace(item.Scenario)
		inputSummary := strings.TrimSpace(item.InputSummary)
		expectedOutcome := strings.TrimSpace(item.ExpectedOutcome)
		kind := strings.TrimSpace(item.VerificationKind)
		if scenario == "" || inputSummary == "" || expectedOutcome == "" {
			return nil, fmt.Errorf("examples[%d] requires non-empty scenario, input_summary, and expected_outcome", i)
		}
		if kind != "deterministic" && kind != "model_judged" {
			return nil, fmt.Errorf("examples[%d] has invalid verification_kind %q", i, item.VerificationKind)
		}
		id := strings.TrimSpace(item.ID)
		if id == "" {
			id = fmt.Sprintf("%s-check-%02d", changeID, i+1)
		}
		if seenIDs[id] {
			return nil, fmt.Errorf("duplicate example id %q", id)
		}
		seenIDs[id] = true
		out = append(out, &genscantle.ExampleItem{
			ID:               id,
			Scenario:         scenario,
			InputSummary:     inputSummary,
			ExpectedOutcome:  expectedOutcome,
			VerificationKind: kind,
		})
	}
	return out, nil
}

// extractArchitecturePreview parses a Goa/Goa-AI design source AST and constructs
// the validated Checkpoint 2 C4 topology and guardrail summary.
func extractArchitecturePreview(changeID, requestRevision, designSource string) (*genscantle.ArchitecturePreviewResult, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "design.go", designSource, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("parse design_source: %w", err)
	}

	var (
		systemName   string
		containers   = make([]*genscantle.C4ContainerItem, 0)
		containerMap = make(map[string]*genscantle.C4ContainerItem)
		edges        = make([]*genscantle.C4EdgeItem, 0)
		guardrails   = &genscantle.GuardrailItem{}
		hasAgent     bool
		varToolsets  = make(map[string]string)
	)

	addContainer := func(item *genscantle.C4ContainerItem) {
		if existing, ok := containerMap[item.ID]; ok {
			if existing.Description == "" && item.Description != "" {
				existing.Description = item.Description
			}
			return
		}
		containerMap[item.ID] = item
		containers = append(containers, item)
	}

	// Pass 1: discover top-level `var X = Toolset("name", ...)` bindings.
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok || len(spec.Names) == 0 || len(spec.Values) == 0 {
			return true
		}
		call, ok := spec.Values[0].(*ast.CallExpr)
		if !ok || callName(call.Fun) != "Toolset" || len(call.Args) == 0 {
			return true
		}
		if tsName := stringLitArg(call.Args[0]); tsName != "" {
			varToolsets[spec.Names[0].Name] = tsName
			addContainer(&genscantle.C4ContainerItem{
				ID:          "toolset:" + tsName,
				Name:        tsName,
				Kind:        "toolset",
				Description: extractDescriptionFromCall(call),
			})
		}
		return true
	})

	// Pass 2: walk Service, Agent, Toolset/Use, and RunPolicy blocks.
	var walkScope func(n ast.Node, currentService, currentAgent string)
	walkScope = func(n ast.Node, currentService, currentAgent string) {
		if n == nil {
			return
		}
		ast.Inspect(n, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch callName(call.Fun) {
			case "API":
				if name := stringLitArgAt(call, 0); name != "" && systemName == "" {
					systemName = name
				}
			case "Service":
				svcName := stringLitArgAt(call, 0)
				if svcName == "" {
					return true
				}
				if systemName == "" {
					systemName = svcName
				}
				svcID := "service:" + svcName
				addContainer(&genscantle.C4ContainerItem{
					ID:          svcID,
					Name:        svcName,
					Kind:        "service",
					Description: extractDescriptionFromCall(call),
				})
				for _, arg := range call.Args[1:] {
					walkScope(arg, svcID, "")
				}
				return false
			case "Agent":
				agName := stringLitArgAt(call, 0)
				if agName == "" {
					return true
				}
				hasAgent = true
				agID := "agent:" + agName
				desc := stringLitArgAt(call, 1)
				if desc == "" {
					desc = extractDescriptionFromCall(call)
				}
				addContainer(&genscantle.C4ContainerItem{
					ID:          agID,
					Name:        agName,
					Kind:        "agent",
					Description: desc,
				})
				if currentService != "" {
					edges = append(edges, &genscantle.C4EdgeItem{
						FromID:   currentService,
						ToID:     agID,
						Protocol: "Goa-AI Agent Workflow",
					})
				}
				for _, arg := range call.Args[1:] {
					walkScope(arg, currentService, agID)
				}
				return false
			case "Toolset":
				tsName := stringLitArgAt(call, 0)
				if tsName != "" {
					addContainer(&genscantle.C4ContainerItem{
						ID:          "toolset:" + tsName,
						Name:        tsName,
						Kind:        "toolset",
						Description: extractDescriptionFromCall(call),
					})
				}
			case "Use":
				if len(call.Args) == 0 {
					return true
				}
				var tsID string
				if lit := stringLitArg(call.Args[0]); lit != "" {
					tsID = "toolset:" + lit
					addContainer(&genscantle.C4ContainerItem{
						ID:          tsID,
						Name:        lit,
						Kind:        "toolset",
						Description: extractDescriptionFromCall(call),
					})
				} else if ident, ok := call.Args[0].(*ast.Ident); ok {
					if mapped, exists := varToolsets[ident.Name]; exists {
						tsID = "toolset:" + mapped
					} else {
						tsID = "toolset:" + ident.Name
					}
				}
				if currentAgent != "" && tsID != "" {
					edges = append(edges, &genscantle.C4EdgeItem{
						FromID:   currentAgent,
						ToID:     tsID,
						Protocol: "Goa-AI Toolset",
					})
				}
			case "TimeBudget", "MaxExecutionTime":
				if secs := parseSecondsExpr(call.Args); secs > 0 {
					guardrails.MaxExecutionSeconds = secs
				}
			case "MaxToolCalls":
				if n := intLitArgAt(call, 0); n > 0 {
					guardrails.MaxToolCalls = n
				}
			case "Compress":
				if trigger := intLitArgAt(call, 0); trigger > 0 {
					guardrails.CompressAtTurns = trigger
				}
				if keep := intLitArgAt(call, 1); keep > 0 {
					guardrails.KeepRecentTurns = keep
				}
			}
			return true
		})
	}
	walkScope(file, "", "")

	if systemName == "" || len(containers) == 0 {
		return nil, errors.New("design_source must declare at least one Goa API or Service container")
	}
	for _, edge := range edges {
		if _, ok := containerMap[edge.FromID]; !ok {
			return nil, fmt.Errorf("edge source container %q is not declared in design_source", edge.FromID)
		}
		if _, ok := containerMap[edge.ToID]; !ok {
			return nil, fmt.Errorf("edge target container %q is not declared in design_source", edge.ToID)
		}
	}
	if hasAgent && (guardrails.MaxExecutionSeconds <= 0 || guardrails.MaxToolCalls <= 0) {
		return nil, errors.New("agent design_source must declare positive TimeBudget/MaxExecutionTime and MaxToolCalls guardrails in RunPolicy")
	}

	sum := sha256.Sum256([]byte(designSource))
	designDigest := hex.EncodeToString(sum[:])
	banner := fmt.Sprintf(
		"%s: %d container(s), %d boundary edge(s) | Guardrails: MaxExecutionSeconds=%ds, MaxToolCalls=%d, CompressAtTurns=%d (keep %d recent turns)",
		systemName,
		len(containers),
		len(edges),
		guardrails.MaxExecutionSeconds,
		guardrails.MaxToolCalls,
		guardrails.CompressAtTurns,
		guardrails.KeepRecentTurns,
	)

	return &genscantle.ArchitecturePreviewResult{
		ChangeID:        changeID,
		RequestRevision: requestRevision,
		DesignDigest:    designDigest,
		SystemName:      systemName,
		Containers:      containers,
		Edges:           edges,
		Guardrails:      guardrails,
		Banner:          banner,
	}, nil
}

// callName returns the unqualified identifier name of an AST call target.
func callName(expr ast.Expr) string {
	switch v := expr.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return v.Sel.Name
	default:
		return ""
	}
}

// stringLitArg extracts an unquoted string literal from expr when present.
func stringLitArg(expr ast.Expr) string {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return ""
	}
	unquoted, err := strconv.Unquote(lit.Value)
	if err != nil {
		return strings.Trim(lit.Value, "\"`")
	}
	return strings.TrimSpace(unquoted)
}

// stringLitArgAt extracts the string literal at index idx of call.Args.
func stringLitArgAt(call *ast.CallExpr, idx int) string {
	if call == nil || idx < 0 || idx >= len(call.Args) {
		return ""
	}
	return stringLitArg(call.Args[idx])
}

// intLitArgAt extracts a positive integer literal at index idx of call.Args.
func intLitArgAt(call *ast.CallExpr, idx int) int {
	if call == nil || idx < 0 || idx >= len(call.Args) {
		return 0
	}
	lit, ok := call.Args[idx].(*ast.BasicLit)
	if !ok || lit.Kind != token.INT {
		return 0
	}
	val, err := strconv.Atoi(lit.Value)
	if err != nil {
		return 0
	}
	return val
}

// parseSecondsExpr parses duration expressions such as `2 * time.Minute`,
// `30 * time.Second`, or string literals `"2m"` into whole seconds.
func parseSecondsExpr(args []ast.Expr) int {
	if len(args) == 0 {
		return 0
	}
	switch v := args[0].(type) {
	case *ast.BasicLit:
		if v.Kind == token.INT {
			n, _ := strconv.Atoi(v.Value)
			return n
		}
	case *ast.BinaryExpr:
		if v.Op == token.MUL {
			leftLit, ok := v.X.(*ast.BasicLit)
			if !ok || leftLit.Kind != token.INT {
				return 0
			}
			mult, err := strconv.Atoi(leftLit.Value)
			if err != nil {
				return 0
			}
			unit := callName(v.Y)
			switch unit {
			case "Second":
				return mult
			case "Minute":
				return mult * 60
			case "Hour":
				return mult * 3600
			}
		}
	case *ast.SelectorExpr:
		switch v.Sel.Name {
		case "Second":
			return 1
		case "Minute":
			return 60
		case "Hour":
			return 3600
		}
	}
	return 0
}

// extractDescriptionFromCall finds a nested Description("...") call or falls
// back to the declaration kind and name.
func extractDescriptionFromCall(call *ast.CallExpr) string {
	var desc string
	ast.Inspect(call, func(n ast.Node) bool {
		if desc != "" {
			return false
		}
		inner, ok := n.(*ast.CallExpr)
		if !ok || callName(inner.Fun) != "Description" {
			return true
		}
		desc = stringLitArgAt(inner, 0)
		return false
	})
	if desc != "" {
		return desc
	}
	if name := stringLitArgAt(call, 0); name != "" {
		return callName(call.Fun) + " " + name
	}
	return callName(call.Fun)
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
	var confirmedBy *string
	if in.ConfirmedBy != nil {
		cp := *in.ConfirmedBy
		confirmedBy = &cp
	}
	return &genscantle.ChangeCardResult{
		ChangeID:           in.ChangeID,
		Status:             in.Status,
		Prompt:             in.Prompt,
		Summary:            in.Summary,
		Ambiguities:        cloneAmbiguities(in.Ambiguities),
		Answers:            maps.Clone(in.Answers),
		Examples:           cloneExamples(in.Examples),
		PreservedBehaviors: slices.Clone(in.PreservedBehaviors),
		ConfirmedBy:        confirmedBy,
		RequestRevision:    in.RequestRevision,
	}
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
