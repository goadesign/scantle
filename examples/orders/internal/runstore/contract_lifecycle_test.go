package runstore_test

// These tests prove that the in-memory reference store exposes lifecycle state
// and matching records as one change. Durable host implementations must satisfy
// the same retry, conflict, and ordering behavior.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
)

// RootStartPersistsOneImmutableOutcome proves a root start's outcome survives a
// later session end on retry.
func (f factory) RootStartPersistsOneImmutableOutcome(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	store := f.newStore(t)
	_, err := store.CreateSession(ctx, "session", now)
	require.NoError(t, err)

	start := session.RunStart{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("agent"))}, RunID: "run", SessionID: "session", StartedAt: now}
	start = publishStartHistory(t, store, start, "")
	command := rootStartCommand(t, start)
	first, err := store.StartRootRun(ctx, command)
	require.NoError(t, err)
	require.Equal(t, session.RunStartProceed, first.Outcome)
	require.True(t, first.Started.Inserted)

	_, err = store.EndSession(ctx, "session", now.Add(time.Second))
	require.NoError(t, err)
	retry, err := store.StartRootRun(ctx, command)
	require.NoError(t, err)
	require.Equal(t, session.RunStartProceed, retry.Outcome)
	require.False(t, retry.Started.Inserted)

	page, err := store.ListRunRecords(ctx, "run", "", 10)
	require.NoError(t, err)
	require.Len(t, page.Events, 1)
	require.Equal(t, hooks.RunStarted, page.Events[0].Type)
}

// RootStartRetryKeepsOriginalOutcomeAfterCompletion proves a root start retry
// after completion returns the original outcome without inserting.
func (f factory) RootStartRetryKeepsOriginalOutcomeAfterCompletion(t *testing.T) {
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Millisecond)
	store := f.newStore(t)
	_, err := store.CreateSession(ctx, "session", now)
	require.NoError(t, err)
	start := session.RunStart{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("agent"))}, RunID: "run", SessionID: "session", StartedAt: now}
	start = publishStartHistory(t, store, start, "")
	command := rootStartCommand(t, start)
	_, err = store.StartRootRun(ctx, command)
	require.NoError(t, err)
	_, err = store.RecordRunTerminal(ctx, storage.RunTerminal{
		RunID: "run", Status: session.RunStatusCompleted,
		Record: completedRecord(t, "terminal", start, "success", nil),
	})
	require.NoError(t, err)
	retry, err := store.StartRootRun(ctx, command)
	require.NoError(t, err)
	require.Equal(t, session.RunStartProceed, retry.Outcome)
	require.False(t, retry.Started.Inserted)
}

// StartRejectsMismatchedStartedRecord proves a start whose record names another
// run is rejected and stores no run.
func (f factory) StartRejectsMismatchedStartedRecord(t *testing.T) {
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Millisecond)
	store := f.newStore(t)
	_, err := store.CreateSession(ctx, "session", now)
	require.NoError(t, err)
	start := session.RunStart{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("agent"))}, RunID: "run", SessionID: "session", StartedAt: now}
	other := start
	other.RunID = "other-run"
	_, err = store.StartRootRun(ctx, storage.RootRunStart{RequestDigest: [32]byte{1},
		Run:     start,
		Started: startedRecord(t, "started", other),
	})
	require.ErrorIs(t, err, storage.ErrRunRecordOwnerMismatch)
	_, err = store.LoadRun(ctx, "run")
	require.ErrorIs(t, err, session.ErrRunNotFound)
}

// RecordRetryRequiresExactTimestamp proves an ordinary record retry with a
// different timestamp conflicts.
func (f factory) RecordRetryRequiresExactTimestamp(t *testing.T) {
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Millisecond)
	store := f.newStore(t)
	start := session.RunStart{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("agent"))}, RunID: "run", StartedAt: now}
	_, err := store.StartOneShotRun(ctx, storage.OneShotRunStart{RequestDigest: [32]byte{1}, Run: publishStartHistory(t, store, start, ""), Started: startedRecord(t, "started", start)})
	require.NoError(t, err)
	record := record("note", "run", genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("agent"))}, "", "note")
	_, err = store.AppendRunRecord(ctx, record)
	require.NoError(t, err)
	changed := *record
	changed.Timestamp = record.Timestamp.Add(time.Millisecond)
	_, err = store.AppendRunRecord(ctx, &changed)
	require.ErrorIs(t, err, runlog.ErrEventConflict)
}

// AppendRunRecordRejectsLifecycleRecord proves AppendRunRecord rejects a
// lifecycle record and leaves the run running.
func (f factory) AppendRunRecordRejectsLifecycleRecord(t *testing.T) {
	store, start := f.runningRootStore(t)

	_, err := store.AppendRunRecord(
		t.Context(),
		completedRecord(t, "forged-terminal", start, "success", nil),
	)

	var contractErr *storage.ContractError
	require.ErrorAs(t, err, &contractErr)
	meta, err := store.LoadRun(t.Context(), start.RunID)
	require.NoError(t, err)
	require.Equal(t, session.RunStatusRunning, meta.Status)
}

// AppendRunRecordRejectsNewRecordsAfterRunStops proves ordinary appends stop
// after every terminal state while exact retries keep their IDs.
func (f factory) AppendRunRecordRejectsNewRecordsAfterRunStops(t *testing.T) {
	tests := []struct {
		name string
		stop func(*testing.T, contractStore, session.RunStart)
	}{
		{
			name: "completed",
			stop: func(t *testing.T, store contractStore, start session.RunStart) {
				_, err := store.RecordRunTerminal(t.Context(), storage.RunTerminal{
					RunID: start.RunID, Status: session.RunStatusCompleted,
					Record: completedRecord(t, "terminal", start, "success", nil),
				})
				require.NoError(t, err)
			},
		},
		{
			name: "failed",
			stop: func(t *testing.T, store contractStore, start session.RunStart) {
				_, err := store.RecordRunTerminal(t.Context(), storage.RunTerminal{
					RunID: start.RunID, Status: session.RunStatusFailed,
					Record: completedRecord(t, "terminal", start, "failed", nil),
				})
				require.NoError(t, err)
			},
		},
		{
			name: "canceled",
			stop: func(t *testing.T, store contractStore, start session.RunStart) {
				const reason = run.CancellationReasonUserRequested
				_, err := store.RecordRunCancellation(t.Context(), storage.RunCancellation{
					RunID: start.RunID, Reason: reason,
					Record: cancellationRecord(t, "cancel", start, reason),
				})
				require.NoError(t, err)
				_, err = store.RecordRunTerminal(t.Context(), storage.RunTerminal{
					RunID: start.RunID, Status: session.RunStatusCanceled,
					Record: completedRecord(t, "terminal", start, "canceled", &run.Cancellation{Reason: reason}),
				})
				require.NoError(t, err)
			},
		},
		{
			name: "suspended",
			stop: func(t *testing.T, store contractStore, start session.RunStart) {
				_, err := store.RecordRunSuspension(t.Context(), storage.RunSuspension{
					RunID: start.RunID,
					Suspension: session.RunSuspension{
						ID: "checkpoint", Data: []byte(`{"version":"v6"}`),
					},
					Record: suspendedRecord(t, "terminal", start, "checkpoint"),
				})
				require.NoError(t, err)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, start := f.runningRootStore(t)
			stored := record("ordinary", start.RunID, start.Subject, start.SessionID, "ordinary")
			first, err := store.AppendRunRecord(t.Context(), stored)
			require.NoError(t, err)
			require.True(t, first.Inserted)
			test.stop(t, store, start)

			retry, err := store.AppendRunRecord(t.Context(), stored)
			require.NoError(t, err)
			require.False(t, retry.Inserted)
			require.Equal(t, first.ID, retry.ID)

			_, err = store.AppendRunRecord(
				t.Context(),
				record("late", start.RunID, start.Subject, start.SessionID, "ordinary"),
			)
			require.ErrorIs(t, err, session.ErrRunNotActive)
			var contractErr *storage.ContractError
			require.ErrorAs(t, err, &contractErr)
		})
	}
}

// RecordListsRejectUnknownStateAndInvalidCursor proves record and run listings
// reject unknown or purged owners and invalid cursors.
func (f factory) RecordListsRejectUnknownStateAndInvalidCursor(t *testing.T) {
	ctx := t.Context()
	store := f.newStore(t)
	_, err := store.ListRunRecords(ctx, "missing", "", 10)
	require.ErrorIs(t, err, session.ErrRunNotFound)
	_, err = store.ListSessionRunRecords(ctx, "missing", "", 10)
	require.ErrorIs(t, err, session.ErrSessionNotFound)
	_, err = store.ListRunsBySession(ctx, "missing", nil)
	require.ErrorIs(t, err, session.ErrSessionNotFound)

	now := time.Now().UTC().Truncate(time.Millisecond)
	_, err = store.CreateSession(ctx, "session", now)
	require.NoError(t, err)
	_, err = store.ListSessionRunRecords(ctx, "session", "0", 10)
	require.ErrorContains(t, err, "invalid cursor")
	_, err = store.EndSession(ctx, "session", now.Add(time.Second))
	require.NoError(t, err)
	require.NoError(t, store.PurgeSession(ctx, "session"))
	_, err = store.ListSessionRunRecords(ctx, "session", "", 10)
	require.ErrorIs(t, err, session.ErrSessionPurged)
	_, err = store.ListRunsBySession(ctx, "session", nil)
	require.ErrorIs(t, err, session.ErrSessionPurged)
}

// EndedSessionStartStoresCancellationIntent proves a start in an ended session
// stores RunStarted and the cancellation intent with stop outcome, and that
// retries keep the original keys.
func (f factory) EndedSessionStartStoresCancellationIntent(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	store := f.newStore(t)
	_, err := store.CreateSession(ctx, "session", now)
	require.NoError(t, err)
	start := session.RunStart{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("agent"))}, RunID: "run", SessionID: "session", StartedAt: now}
	start = publishStartHistory(t, store, start, "")
	_, err = store.EndSession(ctx, "session", now.Add(time.Second))
	require.NoError(t, err)
	accepted, found, err := store.FindRunPreparation(ctx, storage.PreparationOperation{
		Subject: start.Subject, RunID: start.RunID, SessionID: start.SessionID, CommandID: start.RunID,
	})
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, start.SeedEndID, accepted.Seed.EndID)
	require.Positive(t, accepted.PreparedBytes)
	command := rootStartCommand(t, start)
	result, err := store.StartRootRun(ctx, command)
	require.NoError(t, err)
	require.Equal(t, session.RunStartStop, result.Outcome)
	meta, err := store.LoadRun(ctx, "run")
	require.NoError(t, err)
	require.Equal(t, session.RunStatusRunning, meta.Status)
	require.Equal(t, session.RunStartStop, meta.StartOutcome)
	require.NotEmpty(t, meta.CancellationReason)
	intent := runRecords(t, store, start.RunID)
	require.Len(t, intent, 2)
	require.Equal(t, command.Started.EventKey, intent[0].EventKey)
	require.Equal(t, "runtime/cancellation-intent", intent[1].EventKey)
	require.Equal(t, run.CancellationReasonSessionEnded, result.CancellationReason)
	retry, err := store.StartRootRun(ctx, command)
	require.NoError(t, err)
	require.False(t, retry.Started.Inserted)
	require.False(t, retry.Cancellation.Inserted)
	changedStarted := *command.Started
	changedStarted.EventKey = "different-started"
	command.Started = &changedStarted
	_, err = store.StartRootRun(ctx, command)
	require.ErrorIs(t, err, session.ErrRunConflict)
	page, err := store.ListRunRecords(ctx, "run", "", 10)
	require.NoError(t, err)
	require.Equal(t, []runlog.Type{hooks.RunStarted, storage.CancellationRecordType}, []runlog.Type{
		page.Events[0].Type,
		page.Events[1].Type,
	})
	require.Equal(t, command.Started.Timestamp, page.Events[1].Timestamp)
}

// ChildStartStoresParentLinkAndChildStartTogether proves a child start stores
// the parent link and the child's start together.
func (f factory) ChildStartStoresParentLinkAndChildStartTogether(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	store := f.newStore(t)
	_, err := store.CreateSession(ctx, "session", now)
	require.NoError(t, err)
	parentStart := session.RunStart{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("parent"))}, RunID: "parent", SessionID: "session", StartedAt: now}
	parentStart = publishStartHistory(t, store, parentStart, "")
	_, err = store.StartRootRun(ctx, rootStartCommand(t, parentStart))
	require.NoError(t, err)

	childStart := session.RunStart{
		Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("child"))}, RunID: "child", SessionID: "session", ParentRunID: "parent", StartedAt: now,
	}
	childStart = publishStartHistory(t, store, childStart, "tool-call")
	result, err := store.StartChildRun(ctx, storage.ChildRunStart{RequestDigest: [32]byte{1},
		Run:          childStart,
		ParentLinked: childLinkRecord(t, "child-link", parentStart, childStart),
		Started:      startedRecord(t, "child-start", childStart),
	})
	require.NoError(t, err)
	require.Equal(t, session.RunStartProceed, result.Outcome)
	require.True(t, result.ParentRecord.Inserted)
	require.True(t, result.Started.Inserted)
	parent, err := store.ListRunRecords(ctx, "parent", "", 10)
	require.NoError(t, err)
	require.Len(t, parent.Events, 2)
	child, err := store.ListRunRecords(ctx, "child", "", 10)
	require.NoError(t, err)
	require.Len(t, child.Events, 1)
}

// OneShotChildStartStoresParentLinkAndChildStartTogether proves a sessionless
// child start stores both records and retries without inserting.
func (f factory) OneShotChildStartStoresParentLinkAndChildStartTogether(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	store := f.newStore(t)
	parent := session.RunStart{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("parent"))}, RunID: "parent", StartedAt: now}
	_, err := store.StartOneShotRun(t.Context(), storage.OneShotRunStart{RequestDigest: [32]byte{1},
		Run: publishStartHistory(t, store, parent, ""), Started: startedRecord(t, "parent-start", parent),
	})
	require.NoError(t, err)
	child := session.RunStart{
		Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("child"))}, RunID: "child", ParentRunID: parent.RunID, StartedAt: now,
	}
	child = publishStartHistory(t, store, child, "tool-call")
	command := storage.OneShotChildRunStart{RequestDigest: [32]byte{1},
		Run:          child,
		ParentLinked: childLinkRecord(t, "child-link", parent, child),
		Started:      startedRecord(t, "child-start", child),
	}

	result, err := store.StartOneShotChildRun(t.Context(), command)

	require.NoError(t, err)
	require.True(t, result.ParentRecord.Inserted)
	require.True(t, result.Started.Inserted)
	require.Empty(t, result.ParentRecord.SessionStatus)
	require.Empty(t, result.Started.SessionStatus)
	retry, err := store.StartOneShotChildRun(t.Context(), command)
	require.NoError(t, err)
	require.False(t, retry.ParentRecord.Inserted)
	require.False(t, retry.Started.Inserted)
	parentRecords, err := store.ListRunRecords(t.Context(), parent.RunID, "", 10)
	require.NoError(t, err)
	require.Equal(t, []runlog.Type{hooks.RunStarted, hooks.ChildRunLinked}, []runlog.Type{
		parentRecords.Events[0].Type, parentRecords.Events[1].Type,
	})
	childRecords, err := store.ListRunRecords(t.Context(), child.RunID, "", 10)
	require.NoError(t, err)
	require.Len(t, childRecords.Events, 1)
	require.Equal(t, hooks.RunStarted, childRecords.Events[0].Type)
}

// OneShotChildStartRequiresRunningSessionlessParent proves a sessionless child
// needs a sessionless, running parent.
func (f factory) OneShotChildStartRequiresRunningSessionlessParent(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	childFor := func(store contractStore, parent session.RunStart) storage.OneShotChildRunStart {
		child := session.RunStart{
			Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("child"))}, RunID: "child", ParentRunID: parent.RunID, StartedAt: now,
		}
		child = publishStartHistory(t, store, child, "tool-call")
		return storage.OneShotChildRunStart{RequestDigest: [32]byte{1},
			Run: child, ParentLinked: childLinkRecord(t, "child-link", parent, child),
			Started: startedRecord(t, "child-start", child),
		}
	}
	t.Run("session parent", func(t *testing.T) {
		store := f.newStore(t)
		_, err := store.CreateSession(t.Context(), "session", now)
		require.NoError(t, err)
		parent := session.RunStart{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("parent"))}, RunID: "parent", SessionID: "session", StartedAt: now}
		parent = publishStartHistory(t, store, parent, "")
		_, err = store.StartRootRun(t.Context(), rootStartCommand(t, parent))
		require.NoError(t, err)
		_, err = store.StartOneShotChildRun(t.Context(), childFor(store, parent))
		require.ErrorContains(t, err, "parent identity does not match child run")
	})
	t.Run("completed parent", func(t *testing.T) {
		store := f.newStore(t)
		parent := session.RunStart{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("parent"))}, RunID: "parent", StartedAt: now}
		_, err := store.StartOneShotRun(t.Context(), storage.OneShotRunStart{RequestDigest: [32]byte{1},
			Run: publishStartHistory(t, store, parent, ""), Started: startedRecord(t, "parent-start", parent),
		})
		require.NoError(t, err)
		_, err = store.RecordRunTerminal(t.Context(), storage.RunTerminal{
			RunID: parent.RunID, Status: session.RunStatusCompleted,
			Record: completedRecord(t, "parent-complete", parent, "success", nil),
		})
		require.NoError(t, err)
		_, err = store.StartOneShotChildRun(t.Context(), childFor(store, parent))
		require.ErrorIs(t, err, session.ErrRunNotActive)
	})
}

// SessionChildStartRequiresRunningParent proves a new child needs a running
// parent while an exact retry succeeds after the parent stops.
func (f factory) SessionChildStartRequiresRunningParent(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	newStore := func(t *testing.T) (contractStore, session.RunStart) {
		t.Helper()
		store := f.newStore(t)
		_, err := store.CreateSession(t.Context(), "session", now)
		require.NoError(t, err)
		parent := session.RunStart{
			Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("parent"))}, RunID: "parent", SessionID: "session", StartedAt: now,
		}
		parent = publishStartHistory(t, store, parent, "")
		_, err = store.StartRootRun(t.Context(), rootStartCommand(t, parent))
		require.NoError(t, err)
		return store, parent
	}
	childCommand := func(t *testing.T, store contractStore, parent session.RunStart) storage.ChildRunStart {
		t.Helper()
		child := session.RunStart{
			Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("child"))}, RunID: "child", SessionID: parent.SessionID,
			ParentRunID: parent.RunID, StartedAt: now,
		}
		child = publishStartHistory(t, store, child, "tool-call")
		return storage.ChildRunStart{RequestDigest: [32]byte{1},
			Run: child, ParentLinked: childLinkRecord(t, "child-link", parent, child),
			Started: startedRecord(t, "child-start", child),
		}
	}

	t.Run("new child", func(t *testing.T) {
		store, parent := newStore(t)
		_, err := store.RecordRunTerminal(t.Context(), storage.RunTerminal{
			RunID: parent.RunID, Status: session.RunStatusCompleted,
			Record: completedRecord(t, "parent-complete", parent, "success", nil),
		})
		require.NoError(t, err)

		_, err = store.StartChildRun(t.Context(), childCommand(t, store, parent))
		require.ErrorIs(t, err, session.ErrRunNotActive)
	})

	t.Run("exact retry", func(t *testing.T) {
		store, parent := newStore(t)
		command := childCommand(t, store, parent)
		first, err := store.StartChildRun(t.Context(), command)
		require.NoError(t, err)
		_, err = store.RecordRunTerminal(t.Context(), storage.RunTerminal{
			RunID: parent.RunID, Status: session.RunStatusCompleted,
			Record: completedRecord(t, "parent-complete", parent, "success", nil),
		})
		require.NoError(t, err)

		retry, err := store.StartChildRun(t.Context(), command)
		require.NoError(t, err)
		require.False(t, retry.ParentRecord.Inserted)
		require.False(t, retry.Started.Inserted)
		require.Equal(t, first.ParentRecord.ID, retry.ParentRecord.ID)
		require.Equal(t, first.Started.ID, retry.Started.ID)
	})
}

// ChildStartAfterPurgeReportsPurgedSession proves a child start in a purged
// session returns ErrSessionPurged.
func (f factory) ChildStartAfterPurgeReportsPurgedSession(t *testing.T) {
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Millisecond)
	store := f.newStore(t)
	_, err := store.CreateSession(ctx, "session", now)
	require.NoError(t, err)
	parent := session.RunStart{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("parent"))}, RunID: "parent", SessionID: "session", StartedAt: now}
	parent = publishStartHistory(t, store, parent, "")
	_, err = store.StartRootRun(ctx, rootStartCommand(t, parent))
	require.NoError(t, err)
	_, err = store.RecordRunTerminal(ctx, storage.RunTerminal{
		RunID: parent.RunID, Status: session.RunStatusCompleted,
		Record: completedRecord(t, "parent-terminal", parent, "success", nil),
	})
	require.NoError(t, err)
	_, err = store.EndSession(ctx, parent.SessionID, now.Add(time.Second))
	require.NoError(t, err)
	require.NoError(t, store.PurgeSession(ctx, parent.SessionID))

	child := session.RunStart{
		Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("child"))}, RunID: "child", SessionID: parent.SessionID,
		ParentRunID: parent.RunID, StartedAt: now.Add(2 * time.Second),
	}
	_, err = store.StartChildRun(ctx, storage.ChildRunStart{RequestDigest: [32]byte{1},
		Run:          child,
		ParentLinked: childLinkRecord(t, "child-link", parent, child),
		Started:      startedRecord(t, "child-start", child),
	})
	require.ErrorIs(t, err, session.ErrSessionPurged)
}

// RunRecordsMustMatchStoredRunOwner proves records naming another subject or session are rejected.
func (f factory) RunRecordsMustMatchStoredRunOwner(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	store := f.newStore(t)
	_, err := store.CreateSession(ctx, "session", now)
	require.NoError(t, err)
	start := session.RunStart{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("agent"))}, RunID: "run", SessionID: "session", StartedAt: now}
	start = publishStartHistory(t, store, start, "")
	_, err = store.StartRootRun(ctx, rootStartCommand(t, start))
	require.NoError(t, err)

	_, err = store.AppendRunRecord(ctx, record("wrong-agent", "run", genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("other"))}, "session", "note"))
	require.ErrorIs(t, err, storage.ErrRunRecordOwnerMismatch)
	_, err = store.RecordRunTerminal(ctx, storage.RunTerminal{
		RunID: "run", Status: session.RunStatusCompleted,
		Record: completedRecord(t, "wrong-session", session.RunStart{
			Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("agent"))}, RunID: "run", SessionID: "other", StartedAt: now,
		}, "success", nil),
	})
	require.ErrorIs(t, err, storage.ErrRunRecordOwnerMismatch)
}

// OneShotCancellationAndTerminalAreRecorded proves the first cancellation
// reason is permanent and an exact terminal retry changes nothing.
func (f factory) OneShotCancellationAndTerminalAreRecorded(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	store := f.newStore(t)
	start := session.RunStart{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("agent"))}, RunID: "run", StartedAt: now}
	_, err := store.StartOneShotRun(ctx, storage.OneShotRunStart{RequestDigest: [32]byte{1}, Run: publishStartHistory(t, store, start, ""), Started: startedRecord(t, "started", start)})
	require.NoError(t, err)

	_, err = store.RecordRunCancellation(ctx, storage.RunCancellation{
		RunID: "run", Reason: run.CancellationReasonUserRequested,
		Record: cancellationRecord(t, "cancel", start, run.CancellationReasonUserRequested),
	})
	require.NoError(t, err)
	_, err = store.RecordRunCancellation(ctx, storage.RunCancellation{
		RunID: "run", Reason: run.CancellationReasonSessionEnded,
		Record: cancellationRecord(t, "cancel", start, run.CancellationReasonSessionEnded),
	})
	require.ErrorIs(t, err, session.ErrRunCancellationConflict)
	terminal := storage.RunTerminal{
		RunID: "run", Status: session.RunStatusCompleted,
		Record: completedRecord(t, "terminal", start, "success", nil),
	}
	result, err := store.RecordRunTerminal(ctx, terminal)
	require.NoError(t, err)
	require.True(t, result.Inserted)
	meta, err := store.LoadRun(ctx, "run")
	require.NoError(t, err)
	require.Equal(t, session.RunStatusCompleted, meta.Status)
	updatedAt := meta.UpdatedAt
	time.Sleep(time.Millisecond)
	result, err = store.RecordRunTerminal(ctx, terminal)
	require.NoError(t, err)
	require.False(t, result.Inserted)
	meta, err = store.LoadRun(ctx, "run")
	require.NoError(t, err)
	require.Equal(t, updatedAt, meta.UpdatedAt)
}

// SuspensionCheckpointAndTerminalRecordAreAtomic proves a suspension stores its
// checkpoint and suspended state together and an exact retry changes nothing.
func (f factory) SuspensionCheckpointAndTerminalRecordAreAtomic(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	store := f.newStore(t)
	_, err := store.CreateSession(ctx, "session", now)
	require.NoError(t, err)
	start := session.RunStart{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("agent"))}, RunID: "run", SessionID: "session", StartedAt: now}
	start = publishStartHistory(t, store, start, "")
	_, err = store.StartRootRun(ctx, rootStartCommand(t, start))
	require.NoError(t, err)

	suspension := storage.RunSuspension{
		RunID: "run", Suspension: session.RunSuspension{ID: "suspension", Data: []byte(`{"id":"suspension"}`)},
		Record: suspendedRecord(t, "terminal", start, "suspension"),
	}
	result, err := store.RecordRunSuspension(ctx, suspension)
	require.NoError(t, err)
	require.True(t, result.Inserted)
	meta, err := store.LoadRun(ctx, "run")
	require.NoError(t, err)
	require.Equal(t, session.RunStatusSuspended, meta.Status)
	updatedAt := meta.UpdatedAt
	checkpoint, err := store.LoadRunSuspension(ctx, "run")
	require.NoError(t, err)
	require.Equal(t, "suspension", checkpoint.ID)
	time.Sleep(time.Millisecond)
	result, err = store.RecordRunSuspension(ctx, suspension)
	require.NoError(t, err)
	require.False(t, result.Inserted)
	meta, err = store.LoadRun(ctx, "run")
	require.NoError(t, err)
	require.Equal(t, updatedAt, meta.UpdatedAt)
}

// RepairRunTerminalKeepsWorkflowResult proves a terminal repair reports the
// workflow's existing terminal record instead of replacing it.
func (f factory) RepairRunTerminalKeepsWorkflowResult(t *testing.T) {
	store, start := f.runningRootStore(t)
	workflow := storage.RunTerminal{
		RunID: start.RunID, Status: session.RunStatusCompleted,
		Record: completedRecord(t, "terminal", start, "success", nil),
	}
	_, err := store.RecordRunTerminal(t.Context(), workflow)
	require.NoError(t, err)
	repair := workflow
	record := *workflow.Record
	repair.Record = &record
	repair.Record.Timestamp = repair.Record.Timestamp.Add(time.Second)

	result, err := store.RepairRunTerminal(t.Context(), repair)
	require.NoError(t, err)
	require.Equal(t, storage.RunRepairDifferentTerminal, result.Outcome)
	require.Equal(t, session.RunStatusCompleted, result.Status)
	require.Equal(t, storage.AppendResult{}, result.Record)
	page, err := store.ListRunRecords(t.Context(), start.RunID, "", 10)
	require.NoError(t, err)
	require.Len(t, page.Events, 2)
	require.Equal(t, workflow.Record.Timestamp, page.Events[1].Timestamp)
}

// RepairRunTerminalRetryReportsExistingState proves a repeated terminal repair
// reports the stored repair record.
func (f factory) RepairRunTerminalRetryReportsExistingState(t *testing.T) {
	store, start := f.runningRootStore(t)
	command := storage.RunTerminal{
		RunID: start.RunID, Status: session.RunStatusCompleted,
		Record: completedRecord(t, "terminal", start, "success", nil),
	}

	first, err := store.RepairRunTerminal(t.Context(), command)
	require.NoError(t, err)
	require.Equal(t, storage.RunRepairStored, first.Outcome)
	require.True(t, first.Record.Inserted)
	retry, err := store.RepairRunTerminal(t.Context(), command)
	require.NoError(t, err)
	require.Equal(t, storage.RunRepairAlreadyStored, retry.Outcome)
	require.Equal(t, session.RunStatusCompleted, retry.Status)
	require.Equal(t, first.Record.ID, retry.Record.ID)
	require.False(t, retry.Record.Inserted)
	require.Equal(t, first.Record.SessionStatus, retry.Record.SessionStatus)
}

// RepairRunSuspensionKeepsWorkflowResult proves a suspension repair reports the
// workflow's existing suspension instead of replacing it.
func (f factory) RepairRunSuspensionKeepsWorkflowResult(t *testing.T) {
	store, start := f.runningRootStore(t)
	workflow := storage.RunSuspension{
		RunID: start.RunID,
		Suspension: session.RunSuspension{
			ID: "suspension", Data: []byte(`{"version":"v6"}`),
		},
		Record: suspendedRecord(t, "terminal", start, "suspension"),
	}
	_, err := store.RecordRunSuspension(t.Context(), workflow)
	require.NoError(t, err)
	repair := workflow
	record := *workflow.Record
	repair.Record = &record
	repair.Record.Timestamp = repair.Record.Timestamp.Add(time.Second)

	result, err := store.RepairRunSuspension(t.Context(), repair)
	require.NoError(t, err)
	require.Equal(t, storage.RunRepairDifferentTerminal, result.Outcome)
	require.Equal(t, session.RunStatusSuspended, result.Status)
	require.Equal(t, storage.AppendResult{}, result.Record)
	page, err := store.ListRunRecords(t.Context(), start.RunID, "", 10)
	require.NoError(t, err)
	require.Len(t, page.Events, 2)
	require.Equal(t, workflow.Record.Timestamp, page.Events[1].Timestamp)
}

// RepairRunSuspensionRetryReportsExistingState proves a repeated suspension
// repair reports the stored repair record.
func (f factory) RepairRunSuspensionRetryReportsExistingState(t *testing.T) {
	store, start := f.runningRootStore(t)
	command := storage.RunSuspension{
		RunID: start.RunID,
		Suspension: session.RunSuspension{
			ID: "suspension", Data: []byte(`{"version":"v6"}`),
		},
		Record: suspendedRecord(t, "terminal", start, "suspension"),
	}

	first, err := store.RepairRunSuspension(t.Context(), command)
	require.NoError(t, err)
	require.Equal(t, storage.RunRepairStored, first.Outcome)
	require.True(t, first.Record.Inserted)
	retry, err := store.RepairRunSuspension(t.Context(), command)
	require.NoError(t, err)
	require.Equal(t, storage.RunRepairAlreadyStored, retry.Outcome)
	require.Equal(t, session.RunStatusSuspended, retry.Status)
	require.Equal(t, first.Record.ID, retry.Record.ID)
	require.False(t, retry.Record.Inserted)
	require.Equal(t, first.Record.SessionStatus, retry.Record.SessionStatus)
}

// LifecycleRetriesRequireOriginalRecordKeys proves each lifecycle retry must
// reuse the record key of the first write.
func (f factory) LifecycleRetriesRequireOriginalRecordKeys(t *testing.T) {
	t.Run("root start", func(t *testing.T) {
		store, start := f.activeRunStore(t)
		start = publishStartHistory(t, store, start, "")
		command := rootStartCommand(t, start)
		_, err := store.StartRootRun(t.Context(), command)
		require.NoError(t, err)
		command.Started = startedRecord(t, "different-start", start)
		_, err = store.StartRootRun(t.Context(), command)
		require.ErrorIs(t, err, session.ErrRunConflict)
	})

	t.Run("cancellation", func(t *testing.T) {
		store, start := f.runningRootStore(t)
		command := storage.RunCancellation{
			RunID:  start.RunID,
			Reason: run.CancellationReasonUserRequested,
			Record: cancellationRecord(t, "cancel", start, run.CancellationReasonUserRequested),
		}
		_, err := store.RecordRunCancellation(t.Context(), command)
		require.NoError(t, err)
		command.Record = cancellationRecord(t, "different-cancel", start, command.Reason)
		_, err = store.RecordRunCancellation(t.Context(), command)
		require.ErrorIs(t, err, session.ErrRunCancellationConflict)
	})

	t.Run("suspension", func(t *testing.T) {
		store, start := f.runningRootStore(t)
		command := storage.RunSuspension{
			RunID:      start.RunID,
			Suspension: session.RunSuspension{ID: "suspension", Data: []byte(`{"version":"v6"}`)},
			Record:     suspendedRecord(t, "suspend", start, "suspension"),
		}
		_, err := store.RecordRunSuspension(t.Context(), command)
		require.NoError(t, err)
		command.Record = suspendedRecord(t, "different-suspend", start, "suspension")
		_, err = store.RecordRunSuspension(t.Context(), command)
		require.ErrorIs(t, err, session.ErrRunSuspensionConflict)
	})

	t.Run("terminal", func(t *testing.T) {
		store, start := f.runningRootStore(t)
		command := storage.RunTerminal{
			RunID:  start.RunID,
			Status: session.RunStatusCompleted,
			Record: completedRecord(t, "complete", start, "success", nil),
		}
		_, err := store.RecordRunTerminal(t.Context(), command)
		require.NoError(t, err)
		command.Record = completedRecord(t, "different-complete", start, "success", nil)
		_, err = store.RecordRunTerminal(t.Context(), command)
		require.ErrorIs(t, err, session.ErrRunTerminalConflict)
	})

	t.Run("child parent link", func(t *testing.T) {
		store, parent := f.runningRootStore(t)
		child := session.RunStart{
			Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("child"))}, RunID: "child", SessionID: parent.SessionID,
			ParentRunID: parent.RunID, StartedAt: parent.StartedAt,
		}
		child = publishStartHistory(t, store, child, "tool-call")
		command := storage.ChildRunStart{RequestDigest: [32]byte{1},
			Run:          child,
			ParentLinked: childLinkRecord(t, "child-link", parent, child),
			Started:      startedRecord(t, "child-start", child),
		}
		_, err := store.StartChildRun(t.Context(), command)
		require.NoError(t, err)
		command.ParentLinked = childLinkRecord(t, "different-link", parent, child)
		_, err = store.StartChildRun(t.Context(), command)
		require.ErrorIs(t, err, session.ErrRunConflict)
	})
}

// LifecycleCommandsRejectContradictoryTypedRecords proves lifecycle commands
// reject records whose typed payload contradicts the command.
func (f factory) LifecycleCommandsRejectContradictoryTypedRecords(t *testing.T) {
	t.Run("start labels", func(t *testing.T) {
		store, start := f.activeRunStore(t)
		start = publishStartHistory(t, store, start, "")
		command := rootStartCommand(t, start)
		changed := start
		changed.Labels = map[string]string{"site": "other"}
		command.Started = startedRecord(t, "started", changed)
		_, err := store.StartRootRun(t.Context(), command)
		require.ErrorContains(t, err, "labels do not match run")
	})

	t.Run("cancellation reason", func(t *testing.T) {
		store, start := f.runningRootStore(t)
		_, err := store.RecordRunCancellation(t.Context(), storage.RunCancellation{
			RunID:  start.RunID,
			Reason: run.CancellationReasonUserRequested,
			Record: cancellationRecord(t, "cancel", start, run.CancellationReasonSessionEnded),
		})
		require.ErrorContains(t, err, "reason does not match command")
	})

	t.Run("suspension id", func(t *testing.T) {
		store, start := f.runningRootStore(t)
		_, err := store.RecordRunSuspension(t.Context(), storage.RunSuspension{
			RunID:      start.RunID,
			Suspension: session.RunSuspension{ID: "expected", Data: []byte(`{"version":"v6"}`)},
			Record:     suspendedRecord(t, "suspend", start, "different"),
		})
		require.ErrorContains(t, err, "does not match checkpoint")
	})

	t.Run("terminal status", func(t *testing.T) {
		store, start := f.runningRootStore(t)
		_, err := store.RecordRunTerminal(t.Context(), storage.RunTerminal{
			RunID:  start.RunID,
			Status: session.RunStatusCompleted,
			Record: completedRecord(t, "terminal", start, "failed", nil),
		})
		require.ErrorContains(t, err, "outcome does not match command")
	})

	t.Run("terminal labels", func(t *testing.T) {
		store, start := f.runningRootStore(t)
		changed := start
		changed.Labels = map[string]string{"site": "other"}
		_, err := store.RecordRunTerminal(t.Context(), storage.RunTerminal{
			RunID:  start.RunID,
			Status: session.RunStatusCompleted,
			Record: completedRecord(t, "terminal", changed, "success", nil),
		})
		require.ErrorContains(t, err, "labels do not match run")
	})

	t.Run("terminal cancellation reason", func(t *testing.T) {
		store, start := f.runningRootStore(t)
		_, err := store.RecordRunCancellation(t.Context(), storage.RunCancellation{
			RunID:  start.RunID,
			Reason: run.CancellationReasonUserRequested,
			Record: cancellationRecord(t, "cancel", start, run.CancellationReasonUserRequested),
		})
		require.NoError(t, err)
		_, err = store.RecordRunTerminal(t.Context(), storage.RunTerminal{
			RunID:  start.RunID,
			Status: session.RunStatusCanceled,
			Record: completedRecord(t, "terminal", start, "canceled", &run.Cancellation{
				Reason: run.CancellationReasonEngineCanceled,
			}),
		})
		require.ErrorContains(t, err, "cancellation reason does not match run")
	})
}
