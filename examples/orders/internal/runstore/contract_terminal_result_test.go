// These tests keep the selected result independent of the event payload while
// requiring the event, final status, and result to share one immutable winner.
package runstore_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genrunoutput "goa.design/goa-ai/registry/gen/runoutput"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
)

// TerminalSelectedResultSharesFirstWinner proves the selected terminal result
// belongs to the first terminal writer and absent differs from empty.
func (f factory) TerminalSelectedResultSharesFirstWinner(t *testing.T) {
	for _, repairFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "workflow_first", true: "repair_first"}[repairFirst], func(t *testing.T) {
			store, start := f.runningRootStore(t)
			success := &genrunoutput.OperationSuccess{Value: genrunoutput.NewOperationValueJSON(genrunoutput.OperationValueBranchJSON(`{"count":9007199254740993}`))}
			result := &genrunoutput.OperationResult{Outcome: genrunoutput.NewOperationOutcomeSuccess(success)}
			command := storage.RunTerminal{RunID: start.RunID, Status: session.RunStatusCompleted, Record: completedRecord(t, "terminal", start, "success", nil), OperationResult: result}
			encoded, err := genrunoutput.EncodeOperationResult(result)
			require.NoError(t, err)
			if repairFirst {
				saved, err := store.RepairRunTerminal(t.Context(), command)
				require.NoError(t, err)
				assert.Equal(t, storage.RunRepairStored, saved.Outcome)
			} else {
				saved, err := store.RecordRunTerminal(t.Context(), command)
				require.NoError(t, err)
				assert.True(t, saved.Inserted)
			}
			saved, err := store.RecordRunTerminal(t.Context(), command)
			require.NoError(t, err)
			assert.False(t, saved.Inserted)
			success.Value = genrunoutput.NewOperationValueEmpty(&genrunoutput.EmptyOperationResult{})
			_, err = store.RecordRunTerminal(t.Context(), storage.RunTerminal{RunID: start.RunID, Status: session.RunStatusCompleted, Record: completedRecord(t, "terminal", start, "success", nil), OperationResult: mustDecodeResult(t, encoded)})
			assert.NoError(t, err, "caller mutation cannot change saved result")
			_, err = store.RecordRunTerminal(t.Context(), command)
			assert.ErrorIs(t, err, session.ErrRunTerminalConflict) //nolint:testifylint // Repair independently checks the competing outcome against the retained terminal result.
			competing, err := store.RepairRunTerminal(t.Context(), command)
			require.NoError(t, err)
			assert.Equal(t, storage.RunRepairDifferentTerminal, competing.Outcome)
			command.OperationResult = nil
			_, err = store.RecordRunTerminal(t.Context(), command)
			assert.ErrorIs(t, err, session.ErrRunTerminalConflict, "absent and explicit empty are different outcomes") //nolint:testifylint // The retained result is still checked after the conflicting write.
			command.OperationResult = mustDecodeResult(t, encoded)
			saved, err = store.RecordRunTerminal(t.Context(), command)
			require.NoError(t, err, "the retained result is unchanged")
			assert.False(t, saved.Inserted)
		})
	}
}

// TerminalSelectedResultRejectsMalformedBeforeClosure proves a malformed
// selected result is rejected before the run closes.
func (f factory) TerminalSelectedResultRejectsMalformedBeforeClosure(t *testing.T) {
	store, start := f.runningRootStore(t)
	command := storage.RunTerminal{RunID: start.RunID, Status: session.RunStatusCompleted, Record: completedRecord(t, "terminal", start, "success", nil), OperationResult: &genrunoutput.OperationResult{}}
	_, err := store.RecordRunTerminal(t.Context(), command)
	require.Error(t, err)
	meta, err := store.LoadRun(t.Context(), start.RunID)
	require.NoError(t, err)
	assert.Equal(t, session.RunStatusRunning, meta.Status)
	command.OperationResult = nil
	saved, err := store.RecordRunTerminal(t.Context(), command)
	require.NoError(t, err, "the rejected result was not retained")
	assert.True(t, saved.Inserted)
}

// mustDecodeResult decodes a fresh OperationResult from encoded bytes, so the
// retained result can be compared through an exact terminal retry.
func mustDecodeResult(t *testing.T, encoded []byte) *genrunoutput.OperationResult {
	t.Helper()
	result, err := genrunoutput.DecodeOperationResult(encoded)
	require.NoError(t, err)
	return result
}
