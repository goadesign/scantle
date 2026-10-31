package controlplane

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/model"

	genscantle "github.com/goadesign/scantle/gen/scantle"
	"github.com/goadesign/scantle/internal/goldenpath"
)

// compositionProgram is an OpenUI Lang program that refers to the card's
// ambiguity and examples only by 1-based position, as the prompt requires.
const compositionProgram = `root = DecisionWorkspace("Refund limit", [choice, table])
choice = AmbiguityChoiceCard(1, "Tax and shipping change who waits for a manager")
table = PolicySimulatorTable("Who waits?", 1, [row])
row = PolicySimulatorRow("$48 item + $6 shipping", "$54.00 total", ["Instant credit", "Waits for a manager"], 1)`

// recordingCP1Provider is a model.Provider that records the CP1 request it
// receives and answers with a fixed analysis.
type recordingCP1Provider struct {
	// analysis is the JSON returned as assistant text.
	analysis string
	// mu guards request.
	mu sync.Mutex
	// request is the last request the analyzer sent.
	request *model.Request
}

func TestProposeChangeBindsTheModelCompositionToTheRevisionItAnalyzed(t *testing.T) {
	ctx := WithPrincipal(context.Background(), Principal{ID: "alice@example.com"})
	provider := &recordingCP1Provider{analysis: compositionAnalysis(t, &[]string{compositionProgram}[0])}
	cp := newCompositionControlPlane(t, provider)

	card, err := cp.ProposeChange(ctx, &genscantle.ProposeChangePayload{ChangeID: "chg", Prompt: "Refunds up to $50 are instant."})
	require.NoError(t, err)

	req := provider.lastRequest(t)
	require.NotEmpty(t, req.Messages)
	system := req.Messages[0].Parts[0].(model.TextPart).Text
	assert.Equal(t, goldenpath.ArchitectSystemPrompt(), system, "the model must see the composition instruction and catalog")
	assert.Contains(t, system, "1-based position")
	assert.Contains(t, string(req.StructuredOutput.Schema), `"ui_composition"`)

	require.NotNil(t, card.UIComposition)
	assert.Equal(t, compositionProgram, card.UIComposition.Program, "the program must round-trip byte for byte")
	assert.Equal(t, card.RequestRevision, card.UIComposition.ComposedForRevision)
	assert.Equal(t, "chg-ambiguity-01", card.Ambiguities[0].ID, "ordinal 1 in the program names this ambiguity")

	plain := newCompositionControlPlane(t, &recordingCP1Provider{analysis: compositionAnalysis(t, nil)})
	plainCard, err := plain.ProposeChange(ctx, &genscantle.ProposeChangePayload{ChangeID: "chg", Prompt: "Refunds up to $50 are instant."})
	require.NoError(t, err)
	assert.Nil(t, plainCard.UIComposition, "no composition from the model means none on the card")
	assert.Equal(t, plainCard.RequestRevision, card.RequestRevision, "the composition must not be part of request_revision")
}

func TestProposeChangeRejectsABlankComposition(t *testing.T) {
	ctx := WithPrincipal(context.Background(), Principal{ID: "alice@example.com"})
	blank := " \n\t"
	cp := newCompositionControlPlane(t, &recordingCP1Provider{analysis: compositionAnalysis(t, &blank)})

	_, err := cp.ProposeChange(ctx, &genscantle.ProposeChangePayload{ChangeID: "chg", Prompt: "Refunds up to $50 are instant."})
	require.EqualError(t, err, "invalid cp1 analysis: ui_composition must not be blank")
	cp.mu.RLock()
	_, stored := cp.cards["chg"]
	cp.mu.RUnlock()
	assert.False(t, stored, "a rejected analysis stores no card")
}

func TestCompositionDescribesOnlyTheRevisionItWasComposedFor(t *testing.T) {
	ctx := WithPrincipal(context.Background(), Principal{ID: "alice@example.com"})
	cp := newCompositionControlPlane(t, &recordingCP1Provider{analysis: compositionAnalysis(t, &[]string{compositionProgram}[0])})
	proposed, err := cp.ProposeChange(ctx, &genscantle.ProposeChangePayload{ChangeID: "chg", Prompt: "Refunds up to $50 are instant."})
	require.NoError(t, err)

	first, err := cp.ConfirmIntent(ctx, &genscantle.ConfirmIntentPayload{
		ChangeID: "chg", RequestRevision: proposed.RequestRevision,
		Answers: map[string]string{"chg-ambiguity-01": "order_total"}, Examples: proposed.Examples,
	})
	require.NoError(t, err)
	assert.Nil(t, first.UIComposition, "confirming answers creates a new revision the composition did not analyze")

	read, err := cp.GetChange(ctx, &genscantle.GetChangePayload{ChangeID: "chg"})
	require.NoError(t, err)
	assert.Nil(t, read.UIComposition, "get_change must not restore the earlier revision's composition")

	noop, err := cp.ConfirmIntent(ctx, &genscantle.ConfirmIntentPayload{
		ChangeID: "chg", RequestRevision: first.RequestRevision,
		Answers: map[string]string{"chg-ambiguity-01": "order_total"}, Examples: first.Examples,
	})
	require.NoError(t, err)
	assert.Equal(t, first.RequestRevision, noop.RequestRevision)
	assert.Nil(t, noop.UIComposition)
}

func TestCompositionSurvivesAnUnchangedResubmission(t *testing.T) {
	ctx := WithPrincipal(context.Background(), Principal{ID: "alice@example.com"})
	unambiguous := compositionAnalysis(t, &[]string{compositionProgram}[0])
	var analysis CP1SemanticAnalysis
	require.NoError(t, json.Unmarshal([]byte(unambiguous), &analysis))
	analysis.Ambiguities = []CP1ModelAmbiguity{}
	raw, err := json.Marshal(analysis)
	require.NoError(t, err)
	cp := newCompositionControlPlane(t, &recordingCP1Provider{analysis: string(raw)})
	proposed, err := cp.ProposeChange(ctx, &genscantle.ProposeChangePayload{ChangeID: "chg", Prompt: "Refunds up to $50 are instant."})
	require.NoError(t, err)
	require.Equal(t, StatusConfirmed, proposed.Status)
	require.NotNil(t, proposed.UIComposition)

	same, err := cp.ConfirmIntent(ctx, &genscantle.ConfirmIntentPayload{
		ChangeID: "chg", RequestRevision: proposed.RequestRevision, Answers: map[string]string{}, Examples: proposed.Examples,
	})
	require.NoError(t, err)
	assert.Equal(t, proposed.RequestRevision, same.RequestRevision)
	require.NotNil(t, same.UIComposition, "an unchanged card keeps the composition composed for its revision")
	assert.Equal(t, proposed.RequestRevision, same.UIComposition.ComposedForRevision)

	ex := *proposed.Examples[0]
	ex.ExpectedOutcome = "Waits for a manager"
	changed, err := cp.ConfirmIntent(ctx, &genscantle.ConfirmIntentPayload{
		ChangeID: "chg", RequestRevision: proposed.RequestRevision, Answers: map[string]string{}, Examples: []*genscantle.ExampleItem{&ex},
	})
	require.NoError(t, err)
	assert.Nil(t, changed.UIComposition, "an edited example creates a revision the composition did not analyze")
}

func TestProposeChangeReportsProgressOverMCP(t *testing.T) {
	cp := newCompositionControlPlane(t, &recordingCP1Provider{analysis: compositionAnalysis(t, nil)})
	srv := httptest.NewServer(NewHTTPHandler(cp, NewTokenAuthenticator(map[string]Principal{"alice-token": {ID: "alice@example.com"}})))
	defer srv.Close()

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{` +
		`"name":"propose_change","arguments":{"change_id":"chg","prompt":"Refunds up to $50 are instant."},` +
		`"_meta":{"progressToken":"p1","io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/mcp", strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer alice-token")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", "2026-07-28")
	req.Header.Set("Mcp-Method", "tools/call")
	req.Header.Set("Mcp-Name", "propose_change")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.True(t, strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream"), "Content-Type %q", resp.Header.Get("Content-Type"))

	var messages []string
	var progress []map[string]any
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for scanner.Scan() {
		data, ok := strings.CutPrefix(scanner.Text(), "data: ")
		if !ok {
			continue
		}
		var msg struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
			Result map[string]any `json:"result"`
		}
		require.NoError(t, json.Unmarshal([]byte(data), &msg))
		if msg.Method == "notifications/progress" {
			progress = append(progress, msg.Params)
			messages = append(messages, msg.Params["message"].(string))
		}
	}
	require.NoError(t, scanner.Err())
	assert.Equal(t, []string{"Analyzing the feature request with the CP1 model", "Validating the CP1 analysis"}, messages)
	for _, p := range progress {
		assert.Equal(t, "p1", p["progressToken"])
		assert.EqualValues(t, 1, p["total"])
	}
}

func TestReportProgressWithoutARequestedTokenIsANoOp(t *testing.T) {
	assert.NoError(t, reportProgress(context.Background(), 0, 1, "unused"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.True(t, errors.Is(reportProgress(ctx, 0, 1, "unused"), context.Canceled))
}

// Complete records req and returns the fixed analysis as assistant text.
func (p *recordingCP1Provider) Complete(_ context.Context, req *model.Request) (*model.Response, error) {
	p.mu.Lock()
	p.request = req
	p.mu.Unlock()
	return &model.Response{
		Content:    []model.Message{{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: p.analysis}}}},
		StopReason: "stop",
	}, nil
}

// Stream is unused because the CP1 analyzer only calls Complete.
func (p *recordingCP1Provider) Stream(context.Context, *model.Request) (model.Streamer, error) {
	return nil, errors.New("streaming not used by CP1 analyzer")
}

// lastRequest returns the request the analyzer last sent.
func (p *recordingCP1Provider) lastRequest(t *testing.T) *model.Request {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	require.NotNil(t, p.request, "the analyzer sent no request")
	return p.request
}

// compositionAnalysis returns a CP1 analysis with one ambiguity and one
// example, carrying program as ui_composition when it is not nil.
func compositionAnalysis(t *testing.T, program *string) string {
	t.Helper()
	raw, err := json.Marshal(CP1SemanticAnalysis{
		Summary:            "Refunds up to $50 are instant.",
		Ambiguities:        []CP1ModelAmbiguity{{Question: "Is the limit the item subtotal or the order total?", Options: []string{"item_subtotal", "order_total"}}},
		Examples:           []CP1ModelExample{{Scenario: "Refund with shipping", InputSummary: "$48 item + $6 shipping", ExpectedOutcome: "Depends on the limit", VerificationKind: "deterministic"}},
		PreservedBehaviors: []string{},
		UIComposition:      program,
	})
	require.NoError(t, err)
	return string(raw)
}

// newCompositionControlPlane returns a ControlPlane whose CP1 analyzer calls
// provider through a goa-ai model client.
func newCompositionControlPlane(t *testing.T, provider model.Provider) *ControlPlane {
	t.Helper()
	client, err := model.NewClient(provider)
	require.NoError(t, err)
	return New(Options{Analyzer: NewModelCP1Analyzer(client)})
}
