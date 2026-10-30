package runstore_test

// These tests submit canonical start records with a later execution time.
// Only the same accepted request may select a closed run's original records.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
)

const (
	// requestKindRoot selects StartRootRun.
	requestKindRoot = "root"
	// requestKindChild selects StartChildRun.
	requestKindChild = "child"
	// requestKindOneShot selects StartOneShotRun.
	requestKindOneShot = "one shot"
	// requestKindOneShotChild selects StartOneShotChildRun.
	requestKindOneShotChild = "one shot child"
)

// ClosedRequestSelectsOriginalStart proves the same accepted request selects a
// closed run's original records at a later time without writing, and that a
// different digest, a missing digest or changed labels conflict.
func (f factory) ClosedRequestSelectsOriginalStart(t *testing.T) {
	for _, kind := range []string{requestKindRoot, requestKindChild, requestKindOneShot, requestKindOneShotChild} {
		for _, status := range []session.RunStatus{
			session.RunStatusSuspended, session.RunStatusCompleted, session.RunStatusFailed, session.RunStatusCanceled,
		} {
			t.Run(kind+"/"+string(status), func(t *testing.T) {
				store, start := f.requestStartFixture(t, kind)
				first, err := applyRequestStart(t, store, kind, start, [32]byte{1})
				require.NoError(t, err)
				closeReplayRun(t, store, start, status)
				meta, err := store.LoadRun(t.Context(), start.RunID)
				require.NoError(t, err)
				records := requestRecordSnapshot(t, store, start)
				later := start
				later.StartedAt = start.StartedAt.Add(time.Hour)
				result, err := applyRequestStart(t, store, kind, later, [32]byte{1})
				require.NoError(t, err)
				assert.Equal(t, status, result.runStatus)
				assert.Equal(t, first.outcome, result.outcome)
				require.Len(t, result.records, len(first.records))
				for i, record := range result.records {
					assert.Equal(t, first.records[i].ID, record.ID)
					assert.False(t, record.Inserted)
				}
				after, err := store.LoadRun(t.Context(), start.RunID)
				require.NoError(t, err)
				assert.Equal(t, meta, after)
				assert.Equal(t, records, requestRecordSnapshot(t, store, start))

				_, err = applyRequestStart(t, store, kind, later, [32]byte{2})
				require.ErrorIs(t, err, session.ErrRunConflict)
				_, err = applyRequestStart(t, store, kind, later, [32]byte{})
				require.ErrorContains(t, err, "digest is required")
				later.Labels = map[string]string{"site": "different"}
				_, err = applyRequestStart(t, store, kind, later, [32]byte{1})
				require.ErrorIs(t, err, session.ErrRunConflict)
				assert.Equal(t, records, requestRecordSnapshot(t, store, start))
			})
		}
	}
}

// RequestBindingDoesNotResumeRunningRun proves a running run accepts only its
// exact original start; a later time or another digest conflicts.
func (f factory) RequestBindingDoesNotResumeRunningRun(t *testing.T) {
	for _, kind := range []string{requestKindRoot, requestKindChild, requestKindOneShot, requestKindOneShotChild} {
		t.Run(kind, func(t *testing.T) {
			store, start := f.requestStartFixture(t, kind)
			first, err := applyRequestStart(t, store, kind, start, [32]byte{1})
			require.NoError(t, err)
			retry, err := applyRequestStart(t, store, kind, start, [32]byte{1})
			require.NoError(t, err)
			assert.Equal(t, first.records[0].ID, retry.records[0].ID)
			before := requestRecordSnapshot(t, store, start)
			later := start
			later.StartedAt = start.StartedAt.Add(time.Second)
			_, err = applyRequestStart(t, store, kind, later, [32]byte{1})
			require.ErrorIs(t, err, session.ErrRunConflict)
			_, err = applyRequestStart(t, store, kind, start, [32]byte{2})
			require.ErrorIs(t, err, session.ErrRunConflict)
			assert.Equal(t, before, requestRecordSnapshot(t, store, start))
			start.RunID = "next-run"
			toolCallID := ""
			if start.ParentRunID != "" {
				toolCallID = "tool-call"
			}
			start = publishStartHistory(t, store, start, toolCallID)
			_, err = applyRequestStart(t, store, kind, start, [32]byte{2})
			require.NoError(t, err)
		})
	}
}

// ClosedRequestKeepsEndedSessionDecision proves a closed run started in an
// ended session replays its original stop decision and records.
func (f factory) ClosedRequestKeepsEndedSessionDecision(t *testing.T) {
	for _, kind := range []string{requestKindRoot, requestKindChild} {
		for _, status := range []session.RunStatus{session.RunStatusCanceled, session.RunStatusFailed} {
			t.Run(kind+"/"+string(status), func(t *testing.T) {
				store, start := f.requestStartFixture(t, kind)
				_, err := store.EndSession(t.Context(), start.SessionID, start.StartedAt.Add(time.Second))
				require.NoError(t, err)
				first, err := applyRequestStart(t, store, kind, start, [32]byte{1})
				require.NoError(t, err)
				require.Equal(t, session.RunStartStop, first.outcome)
				assert.Equal(t, session.RunStatusRunning, first.runStatus)
				finished := start
				finished.StartedAt = start.StartedAt.Add(5 * time.Second)
				var provenance *run.Cancellation
				if status == session.RunStatusCanceled {
					provenance = &run.Cancellation{Reason: run.CancellationReasonSessionEnded}
				}
				_, err = store.RecordRunTerminal(t.Context(), storage.RunTerminal{
					RunID: start.RunID, Status: status,
					Record: completedRecord(t, "finished", finished, string(status), provenance),
				})
				require.NoError(t, err)
				before := requestRecordSnapshot(t, store, start)
				start.StartedAt = start.StartedAt.Add(time.Hour)
				retry, err := applyRequestStart(t, store, kind, start, [32]byte{1})
				require.NoError(t, err)
				assert.Equal(t, first.outcome, retry.outcome)
				assert.Equal(t, status, retry.runStatus)
				require.Len(t, retry.records, len(first.records))
				for i, record := range retry.records {
					assert.Equal(t, first.records[i].ID, record.ID)
					assert.False(t, record.Inserted)
				}
				assert.Equal(t, before, requestRecordSnapshot(t, store, start))
			})
		}
	}
}

// ClosedRequestContendsWithClosure proves a replayed start racing the run's
// terminal write never changes the original start or adds records.
func (f factory) ClosedRequestContendsWithClosure(t *testing.T) {
	store, start := f.requestStartFixture(t, requestKindRoot)
	_, err := applyRequestStart(t, store, requestKindRoot, start, [32]byte{1})
	require.NoError(t, err)
	later := start
	later.StartedAt = start.StartedAt.Add(time.Second)
	release := make(chan struct{})
	var group sync.WaitGroup
	var startErr, closeErr error
	var result startReplayResult
	terminal := completedRecord(t, "terminal", start, "success", nil)
	command := rootStartCommand(t, later)
	group.Add(2)
	go func() {
		defer group.Done()
		<-release
		stored, err := store.StartRootRun(context.Background(), command)
		startErr = err
		result = startReplayResult{stored.Outcome, stored.RunStatus, []storage.AppendResult{stored.Started}}
	}()
	go func() {
		defer group.Done()
		<-release
		_, closeErr = store.RecordRunTerminal(context.Background(), storage.RunTerminal{
			RunID: start.RunID, Status: session.RunStatusCompleted, Record: terminal,
		})
	}()
	close(release)
	group.Wait()
	require.NoError(t, closeErr)
	if startErr != nil {
		require.ErrorIs(t, startErr, session.ErrRunConflict)
	} else {
		assert.Equal(t, session.RunStatusCompleted, result.runStatus)
	}
	meta, err := store.LoadRun(t.Context(), start.RunID)
	require.NoError(t, err)
	assert.Equal(t, start.StartedAt, meta.StartedAt)
	require.Len(t, requestRecordSnapshot(t, store, start)[start.RunID], 2)
}

// EndedSessionStartRejectsCompletionAndSuspension proves a stopped start can
// neither complete successfully nor suspend, and writes nothing when rejected.
func (f factory) EndedSessionStartRejectsCompletionAndSuspension(t *testing.T) {
	store, start := f.requestStartFixture(t, requestKindRoot)
	_, err := store.EndSession(t.Context(), start.SessionID, start.StartedAt)
	require.NoError(t, err)
	_, err = applyRequestStart(t, store, requestKindRoot, start, [32]byte{1})
	require.NoError(t, err)
	before := requestRecordSnapshot(t, store, start)
	_, err = store.RecordRunTerminal(t.Context(), storage.RunTerminal{
		RunID: start.RunID, Status: session.RunStatusCompleted,
		Record: completedRecord(t, "finished", start, "success", nil),
	})
	require.ErrorContains(t, err, "stopped start cannot complete successfully")
	_, err = store.RecordRunSuspension(t.Context(), storage.RunSuspension{
		RunID: start.RunID, Suspension: session.RunSuspension{ID: "checkpoint", Data: []byte(`{"version":"v6"}`)},
		Record: suspendedRecord(t, "finished", start, "checkpoint"),
	})
	require.ErrorContains(t, err, "stopped start cannot suspend")
	assert.Equal(t, before, requestRecordSnapshot(t, store, start))
	meta, err := store.LoadRun(t.Context(), start.RunID)
	require.NoError(t, err)
	assert.Equal(t, session.RunStatusRunning, meta.Status)
}

// applyRequestStart encodes the same event contracts as workflow code. Only the
// test-supplied request digest and immutable input distinguish each submission.
func applyRequestStart(t *testing.T, store contractStore, kind string, start session.RunStart, digest [32]byte) (startReplayResult, error) {
	t.Helper()
	root := rootStartCommand(t, start)
	root.RequestDigest = digest
	parent := start
	parent.RunID, parent.ParentRunID = start.ParentRunID, ""
	switch kind {
	case requestKindRoot:
		result, err := store.StartRootRun(t.Context(), root)
		records := []storage.AppendResult{result.Started}
		if result.Outcome == session.RunStartStop {
			records = append(records, result.Cancellation)
		}
		return startReplayResult{result.Outcome, result.RunStatus, records}, err
	case requestKindChild:
		result, err := store.StartChildRun(t.Context(), storage.ChildRunStart{
			RequestDigest: digest, Run: start, Started: root.Started,
			ParentLinked: childLinkRecord(t, "link-"+start.RunID, parent, start),
		})
		records := []storage.AppendResult{result.ParentRecord, result.Started}
		if result.Outcome == session.RunStartStop {
			records = append(records, result.Cancellation)
		}
		return startReplayResult{result.Outcome, result.RunStatus, records}, err
	case requestKindOneShot:
		result, err := store.StartOneShotRun(t.Context(), storage.OneShotRunStart{
			RequestDigest: digest, Run: start, Started: root.Started,
		})
		return startReplayResult{session.RunStartProceed, result.RunStatus, []storage.AppendResult{result.Record}}, err
	default:
		result, err := store.StartOneShotChildRun(t.Context(), storage.OneShotChildRunStart{
			RequestDigest: digest, Run: start, Started: root.Started,
			ParentLinked: childLinkRecord(t, "link-"+start.RunID, parent, start),
		})
		return startReplayResult{session.RunStartProceed, result.RunStatus, []storage.AppendResult{result.ParentRecord, result.Started}}, err
	}
}

// requestRecordSnapshot reads every record of the tested run and its parent,
// so mutation checks include the parent's event list as well as the child's.
func requestRecordSnapshot(t *testing.T, store contractStore, start session.RunStart) map[string][]*runlog.Event {
	t.Helper()
	return recordSnapshot(t, store, start.RunID, start.ParentRunID)
}

// requestStartFixture creates the Session and running parent required by the
// chosen operation. The tested run itself has not been created.
func (f factory) requestStartFixture(t *testing.T, kind string) (contractStore, session.RunStart) {
	t.Helper()
	store, start := f.activeRunStore(t)
	if kind == requestKindOneShot || kind == requestKindOneShotChild {
		start.SessionID = ""
	}
	toolCallID := ""
	if kind == requestKindChild || kind == requestKindOneShotChild {
		parent := start
		parent.RunID = "parent"
		parentKind := requestKindRoot
		if start.SessionID == "" {
			parentKind = requestKindOneShot
		}
		parent = publishStartHistory(t, store, parent, "")
		_, err := applyRequestStart(t, store, parentKind, parent, [32]byte{3})
		require.NoError(t, err)
		start.ParentRunID = parent.RunID
		toolCallID = "tool-call"
	}
	return store, publishStartHistory(t, store, start, toolCallID)
}
