package runstore_test

// Closed-request selection keeps the published history checks used by a fresh
// start. A matching engine digest never authorizes missing or different history.

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
)

// ClosedRequestRequiresExactPublishedHistory proves closed-request selection
// still requires the run's exact published history position.
func (f factory) ClosedRequestRequiresExactPublishedHistory(t *testing.T) {
	for _, kind := range []string{requestKindRoot, requestKindChild, requestKindOneShot, requestKindOneShotChild} {
		// The reference test also corrupts the stored publication (missing,
		// unpublished, owner, predecessor); those cases need write access to
		// private state and are not part of the public contract suite.
		for _, change := range []string{"position"} {
			t.Run(kind+"/"+change, func(t *testing.T) {
				store, start := f.requestStartFixture(t, kind)
				_, err := applyRequestStart(t, store, kind, start, [32]byte{1})
				require.NoError(t, err)
				closeReplayRun(t, store, start, session.RunStatusCompleted)
				before := requestRecordSnapshot(t, store, start)
				meta, err := store.LoadRun(t.Context(), start.RunID)
				require.NoError(t, err)
				later := start
				later.StartedAt = start.StartedAt.Add(time.Hour)
				// The candidate's own position is checked without altering storage.
				if change == "position" {
					later.SeedEndID = "99"
				}
				_, err = applyRequestStart(t, store, kind, later, [32]byte{1})
				var rejected *storage.ContractError
				require.ErrorAs(t, err, &rejected)
				after, err := store.LoadRun(t.Context(), start.RunID)
				require.NoError(t, err)
				assert.Equal(t, meta, after)
				assert.Equal(t, before, requestRecordSnapshot(t, store, start))
			})
		}
	}
}

// ClosedRequestCannotRecreatePurgedPublication proves a replayed start after
// purge returns ErrSessionPurged and recreates no run, record or publication.
func (f factory) ClosedRequestCannotRecreatePurgedPublication(t *testing.T) {
	store, start := f.requestStartFixture(t, requestKindRoot)
	_, err := applyRequestStart(t, store, requestKindRoot, start, [32]byte{1})
	require.NoError(t, err)
	closeReplayRun(t, store, start, session.RunStatusCompleted)
	_, err = store.EndSession(t.Context(), start.SessionID, start.StartedAt.Add(time.Second))
	require.NoError(t, err)
	require.NoError(t, store.PurgeSession(t.Context(), start.SessionID))
	start.StartedAt = start.StartedAt.Add(time.Hour)
	_, err = applyRequestStart(t, store, requestKindRoot, start, [32]byte{1})
	require.ErrorIs(t, err, session.ErrSessionPurged)
	_, err = store.LoadRun(t.Context(), start.RunID)
	assert.ErrorIs(t, err, session.ErrRunNotFound)
	assert.Empty(t, recordSnapshot(t, store, start.RunID))
	_, err = store.LoadRunSeed(t.Context(), start.RunID, start.SeedEndID)
	assert.ErrorIs(t, err, storage.ErrSeedNotFound)
}
