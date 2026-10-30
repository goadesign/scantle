package runstore_test

// Stores return caller values byte for byte: declaration text is not
// normalized and record timestamps keep their instant and zone offset.

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/prompt"
	"goa.design/goa-ai/runtime/agent/storage"
)

// ValuesRoundTripExactly proves rendered-prompt text containing invalid UTF-8
// survives an exact declaration retry and reads, and that a record timestamp
// far in the future with a fixed zone is returned unchanged and retries.
func (f factory) ValuesRoundTripExactly(t *testing.T) {
	store, start := f.runningRootStore(t)
	declaration := storage.SeedDeclaration{
		Subject: start.Subject, RunID: "next", SessionID: start.SessionID, CommandID: "command", AttemptID: "attempt",
		Kind: storage.SeedLiteral, RenderedPrompts: []prompt.RenderEvent{{
			PromptID: "prompt", Version: "v\xff",
			Scope: prompt.Scope{SessionID: start.SessionID, Labels: map[string]string{"k\xfe": "v\xfd"}},
		}},
	}
	bindSeedTestOperation(t, store, declaration, rootExecutionOrigin("next"))
	first, err := store.BeginRunSeed(t.Context(), declaration)
	require.NoError(t, err)
	retry, err := store.BeginRunSeed(t.Context(), declaration)
	require.NoError(t, err, "an exact declaration retry is accepted")
	assert.Equal(t, declaration.RenderedPrompts, retry.Declaration.RenderedPrompts)
	assert.Equal(t, first, retry)

	note := record("future", start.RunID, start.Subject, start.SessionID, "note")
	note.Timestamp = time.Date(2300, time.January, 1, 1, 0, 0, 0, time.FixedZone("plus-one", 3600))
	stored, err := store.AppendRunRecord(t.Context(), note)
	require.NoError(t, err)
	again, err := store.AppendRunRecord(t.Context(), note)
	require.NoError(t, err)
	assert.Equal(t, stored.ID, again.ID)
	assert.False(t, again.Inserted)
	saved := recordByKey(t, store, start.RunID, "future")
	assert.True(t, note.Timestamp.Equal(saved.Timestamp))
	_, offset := saved.Timestamp.Zone()
	assert.Equal(t, 3600, offset)
}
