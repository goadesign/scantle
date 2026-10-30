// Package controlplane implements the Scantle MCP software factory control
// plane across Checkpoints 1 through 3 (CP1–CP3): authenticated intent &
// ambiguity resolution, evaluated C4 architecture & cost/safety guardrail
// preview, and target-bound Fast Draft Mode prompt overrides.
package controlplane

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	_ "embed"
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
)

var (
	// checkpointUIHTML is the stateless MCP Apps (`ui://scantle/checkpoint`,
	// `text/html;profile=mcp-app`) HTML document host embedded from
	// checkpoint.html.
	//
	//go:embed checkpoint.html
	checkpointUIHTML string

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

// GetChange returns the current Change Card for change_id to its owner. It
// fails for an unknown change_id or a caller who does not own the card, and
// never changes the card.
func (c *ControlPlane) GetChange(ctx context.Context, p *genscantle.GetChangePayload) (*genscantle.ChangeCardResult, error) {
	principal, ok := PrincipalFromContext(ctx)
	if !ok {
		return nil, errors.New("authenticated principal is required")
	}
	c.mu.RLock()
	record, ok := c.cards[p.ChangeID]
	c.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown change_id %q", p.ChangeID)
	}
	if record.owner != principal.ID {
		return nil, fmt.Errorf("principal %q is not the owner of change_id %q", principal.ID, p.ChangeID)
	}
	return record.card, nil
}

// ConfirmIntent records the owner's complete answers and example table for a
// Change Card that needs confirmation or is already confirmed. The caller
// must be the card's owner and send the card's current request_revision, so
// an edit based on an older revision fails. Every open ambiguity must have an
// allowed answer. When a confirmed card's answers and examples are unchanged,
// the card is returned as it is; otherwise the card is confirmed under a new
// request_revision, so previews and verification reports bound to the earlier
// revision no longer match it. The prompt, summary and preserved behaviors
// never change here: a new requirement needs a new propose_change.
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
	if rev == existing.RequestRevision {
		// Same answers and examples on an already confirmed card.
		return existing, nil
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
