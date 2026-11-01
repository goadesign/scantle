package controlplane

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCardsSurviveANewControlPlane(t *testing.T) {
	dir := t.TempDir()
	cfg := verificationConfig(t)
	alice := WithPrincipal(context.Background(), Principal{ID: "alice@example.com"})
	analysis, err := json.Marshal(CP1SemanticAnalysis{
		Summary: "Refunds up to $50 are instant.",
		Ambiguities: []CP1ModelAmbiguity{{
			Question: "Does the limit include tax?", Options: []string{"with_tax", "without_tax"},
		}},
		Examples: []CP1ModelExample{{
			Scenario: "Refund at the limit", InputSummary: "Order total $50.00",
			ExpectedOutcome: "Instant store credit", VerificationKind: "deterministic",
		}},
		PreservedBehaviors: []string{"Managers approve larger refunds."},
	})
	require.NoError(t, err)

	first, err := New(Options{CardsDir: dir, Analyzer: NewModelCP1Analyzer(newStubModelClient(t, string(analysis))), Verification: cfg})
	require.NoError(t, err)
	proposed, err := first.ProposeChange(alice, &gensurla.ProposeChangePayload{ChangeID: "chg", Prompt: "Refunds up to $50 are instant."})
	require.NoError(t, err)
	require.Equal(t, StatusNeedsConfirmation, proposed.Status)
	confirmed, err := first.ConfirmIntent(alice, &gensurla.ConfirmIntentPayload{
		ChangeID: "chg", RequestRevision: proposed.RequestRevision,
		Answers:  map[string]string{proposed.Ambiguities[0].ID: "with_tax"},
		Examples: proposed.Examples,
	})
	require.NoError(t, err)
	require.Equal(t, StatusConfirmed, confirmed.Status)
	require.NoError(t, first.Close())

	restarted := newControlPlane(t, Options{CardsDir: dir, Analyzer: NewModelCP1Analyzer(newStubModelClient(t, string(analysis))), Verification: cfg})
	got, err := restarted.GetChange(alice, &gensurla.GetChangePayload{ChangeID: "chg"})
	require.NoError(t, err)
	assert.Equal(t, confirmed, got, "the saved card comes back unchanged")

	_, err = restarted.GetChange(WithPrincipal(context.Background(), Principal{ID: "bob@example.com"}), &gensurla.GetChangePayload{ChangeID: "chg"})
	assert.ErrorContains(t, err, "is not the owner", "ownership is saved with the card")

	again, err := restarted.ConfirmIntent(alice, &gensurla.ConfirmIntentPayload{
		ChangeID: "chg", RequestRevision: confirmed.RequestRevision,
		Answers: confirmed.Answers, Examples: confirmed.Examples,
	})
	require.NoError(t, err)
	assert.Equal(t, confirmed.RequestRevision, again.RequestRevision, "the design source is saved, so the revision is reproduced")

	run, err := restarted.VerifyChange(alice, &gensurla.VerifyChangePayload{
		ChangeID: "chg", RequestRevision: confirmed.RequestRevision, CandidateCommit: confirmed.RequestRevision[:40],
	})
	require.NoError(t, err)
	_, running := run.State.AsRunning()
	assert.True(t, running, "a restarted control plane verifies a card confirmed before the restart")

	_, err = restarted.ProposeChange(alice, &gensurla.ProposeChangePayload{ChangeID: "chg", Prompt: "Again."})
	assert.ErrorContains(t, err, "already exists")
}

func TestCardsDirectoryIsHeldByOneControlPlane(t *testing.T) {
	dir := t.TempDir()
	newControlPlane(t, Options{CardsDir: dir})
	_, err := New(Options{CardsDir: dir})
	assert.ErrorContains(t, err, "is in use by another process")

	_, err = New(Options{})
	assert.ErrorContains(t, err, "cards directory is required")
}

func TestUnreadableCardsAreErrors(t *testing.T) {
	alice := WithPrincipal(context.Background(), Principal{ID: "alice@example.com"})
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{"not JSON", "{", "decode change card"},
		{"unknown field", `{"card":{"ChangeID":"chg"},"owner":"alice@example.com","design_source":"","extra":1}`, "unknown field"},
		{"another card's content", `{"card":{"ChangeID":"other"},"owner":"alice@example.com","design_source":""}`, "does not hold that card"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cp := newControlPlane(t, Options{})
			require.NoError(t, os.WriteFile(cp.cards.path("chg"), []byte(c.content), 0o600))
			_, err := cp.GetChange(alice, &gensurla.GetChangePayload{ChangeID: "chg"})
			assert.ErrorContains(t, err, c.want)
		})
	}
}

func TestCardFilesStayInsideTheDirectory(t *testing.T) {
	cp := newControlPlane(t, Options{})
	path := cp.cards.path("../../etc/passwd")
	assert.Equal(t, cp.cards.dir, filepath.Dir(path))
	assert.NotEqual(t, cp.cards.path("Req-1"), cp.cards.path("req-1"), "IDs differing only in case get different files")
}
