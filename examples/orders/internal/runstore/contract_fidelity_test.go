package runstore_test

// Stores return caller values byte for byte: declaration text is not
// normalized and record timestamps keep their instant and zone offset.

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	genregistry "goa.design/goa-ai/registry/gen/registry"
	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent/prompt"
	"goa.design/goa-ai/runtime/agent/storage"
)

// ValuesRoundTripExactly proves values the reference store accepts come back
// unchanged: rendered-prompt text and command IDs with invalid UTF-8 survive
// exact retries and reads, a tool call ID with invalid UTF-8 is rejected
// without binding anything, and record timestamps far in
// the future or with a -60 second zone keep their instant, offset and name.
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

	for _, at := range []time.Time{
		time.Date(2300, time.January, 1, 1, 0, 0, 0, time.FixedZone("plus-one", 3600)),
		// Go's binary time encoding reserves a -1 minute offset for UTC.
		time.Date(2026, time.March, 4, 5, 6, 7, 8_000_000, time.FixedZone("minus-sixty", -60)),
	} {
		_, wantOffset := at.Zone()
		key := fmt.Sprint("note", wantOffset)
		note := record(key, start.RunID, start.Subject, start.SessionID, "note")
		note.Timestamp = at
		stored, err := store.AppendRunRecord(t.Context(), note)
		require.NoError(t, err)
		again, err := store.AppendRunRecord(t.Context(), note)
		require.NoError(t, err)
		assert.Equal(t, stored.ID, again.ID)
		assert.False(t, again.Inserted)
		saved := recordByKey(t, store, start.RunID, key)
		assert.True(t, at.Equal(saved.Timestamp))
		name, offset := saved.Timestamp.Zone()
		wantName, _ := at.Zone()
		assert.Equal(t, wantOffset, offset)
		assert.Equal(t, wantName, name)
	}

	origin := genregistry.ExecutionInvocation{Invocation: genregistry.NewExecutionInvocationKindTool(&genregistry.ToolInvocation{RunID: start.RunID, ToolCallID: "call"})}
	subject := genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("child-agent")}
	child := storage.SeedDeclaration{Subject: subject, RunID: "child", SessionID: start.SessionID, CommandID: "command\xfd", AttemptID: "attempt", Kind: storage.SeedLiteral}
	bindSeedTestOperation(t, store, child, origin)
	seed, err := store.BeginRunSeed(t.Context(), child)
	require.NoError(t, err)
	assert.Equal(t, origin, seed.Origin)
	assert.Equal(t, subject, seed.Declaration.Subject)
	retried, err := store.BeginRunSeed(t.Context(), child)
	require.NoError(t, err, "an exact retry with invalid UTF-8 identity strings is accepted")
	assert.Equal(t, seed, retried)
	require.NoError(t, store.PublishRunSeed(t.Context(), appendSeedTestCompletion(t, store, child, storage.EmptySeedEndID)))
	prepared, found, err := store.FindRunPreparation(t.Context(), storage.PreparationOperation{Subject: subject, RunID: "child", SessionID: start.SessionID, CommandID: "command\xfd"})
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, origin, prepared.Seed.Origin)

	// Invocation and subject strings are validated by the generated codecs,
	// so invalid UTF-8 there is rejected before anything is bound.
	rejected := storage.RunPreparationBegin{
		Operation: storage.PreparationOperation{Subject: subject, RunID: "rejected", SessionID: start.SessionID, CommandID: "command"},
		Origin:    genregistry.ExecutionInvocation{Invocation: genregistry.NewExecutionInvocationKindTool(&genregistry.ToolInvocation{RunID: start.RunID, ToolCallID: "call\xff"})},
	}
	_, err = store.BeginRunPreparationOperation(t.Context(), rejected)
	var contractErr *storage.ContractError
	require.ErrorAs(t, err, &contractErr)
	rejected.Origin = origin
	_, err = store.BeginRunPreparationOperation(t.Context(), rejected)
	require.NoError(t, err, "the rejected invocation bound nothing")
}
