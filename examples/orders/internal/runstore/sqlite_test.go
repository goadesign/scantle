package runstore_test

// These tests cover what only a durable store can show: state survives closing
// and reopening the database file, and separate Store handles on one file
// still choose exactly one winner for each single-winner operation.

import (
	"bytes"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goadesign/surla/examples/orders/internal/runstore"
	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
	"goa.design/goa-ai/runtime/agent/transcript"
)

type (
	// durableSnapshot is everything a reopened store must return unchanged.
	durableSnapshot struct {
		// session is the host session.
		session session.Session
		// runs maps run IDs to their metadata.
		runs map[string]session.RunMeta
		// records maps run IDs to their complete ordered records.
		records map[string][]*runlog.Event
		// sessionRecords is the complete session commit order.
		sessionRecords []*runlog.Event
		// suspension is the suspended run's checkpoint.
		suspension session.RunSuspension
		// seed is the root run's publication.
		seed storage.RunSeed
		// seedRecords is the root run's published history.
		seedRecords storage.SeedPage
		// preparation is the root run's accepted preparation.
		preparation storage.RunPreparation
	}
)

// TestReopenPreservesState proves closing and reopening the file returns
// identical sessions, runs, records, checkpoint bytes, resolutions and
// publications, and that exact retries still return their original positions.
func TestReopenPreservesState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.db")
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Millisecond)
	checkpoint := session.RunSuspension{ID: "checkpoint", Data: []byte("{\"saved\":\"\xe9 exact bytes \\u0000\"}\n")}
	subject := genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("agent")}
	root := session.RunStart{Subject: subject, RunID: "root", SessionID: "session", StartedAt: now, Labels: map[string]string{"tenant": "t1"}}
	literal, err := transcript.EncodeRunLogDelta([]*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "refund order 7"}}}})
	require.NoError(t, err)

	first, err := runstore.Open(ctx, path)
	require.NoError(t, err)
	_, err = first.CreateSession(ctx, "session", now)
	require.NoError(t, err)
	declaration := storage.SeedDeclaration{Subject: subject, RunID: root.RunID, SessionID: root.SessionID, CommandID: root.RunID, AttemptID: "attempt", Kind: storage.SeedLiteral}
	bindSeedTestOperation(t, first, declaration, rootExecutionOrigin(root.RunID))
	_, err = first.BeginRunSeed(ctx, declaration)
	require.NoError(t, err)
	literalAppend := storage.SeedAppend{RunID: root.RunID, AttemptID: "attempt", Record: storage.SeedRecord{Key: "input", PreviousID: storage.EmptySeedEndID, Messages: literal}}
	root.SeedEndID, err = first.AppendRunSeed(ctx, literalAppend)
	require.NoError(t, err)
	require.NoError(t, first.PublishRunSeed(ctx, appendSeedTestCompletion(t, first, declaration, root.SeedEndID)))
	rootCommand := rootStartCommand(t, root)
	started, err := first.StartRootRun(ctx, rootCommand)
	require.NoError(t, err)
	ordinary := record("note", root.RunID, subject, root.SessionID, "note")
	noted, err := first.AppendRunRecord(ctx, ordinary)
	require.NoError(t, err)
	_, err = first.RecordRunSuspension(ctx, storage.RunSuspension{RunID: root.RunID, Suspension: checkpoint, Record: suspendedRecord(t, "suspended", root, checkpoint.ID)})
	require.NoError(t, err)
	successor := publishStartHistory(t, first, session.RunStart{Subject: subject, RunID: "successor", SessionID: root.SessionID, PredecessorRunID: root.RunID, StartedAt: now}, "")
	successorCommand := rootStartCommand(t, successor)
	admitted, err := first.StartRootRun(ctx, successorCommand)
	require.NoError(t, err)
	_, err = first.RecordRunSuspension(ctx, storage.RunSuspension{RunID: successor.RunID, Suspension: checkpoint, Record: suspendedRecord(t, "suspended", successor, checkpoint.ID)})
	require.NoError(t, err)
	relinquished, err := first.RelinquishRunSuspension(ctx, storage.RunSuspensionRelinquishment{RunID: successor.RunID, SuspensionID: checkpoint.ID, Reason: "user_requested"})
	require.NoError(t, err)
	before := takeDurableSnapshot(t, first, declaration, root, successor)
	require.NoError(t, first.Close())

	reopened := openStoreAt(t, path)
	after := takeDurableSnapshot(t, reopened, declaration, root, successor)
	assert.Equal(t, before, after)
	assert.Equal(t, checkpoint.Data, after.suspension.Data, "checkpoint bytes survive byte for byte")
	assert.Equal(t, session.ContinuedSuspension{RunID: successor.RunID}, after.runs[root.RunID].SuspensionResolution)
	assert.Equal(t, relinquished, after.runs[successor.RunID].SuspensionResolution)

	position, err := reopened.AppendRunSeed(ctx, literalAppend)
	require.NoError(t, err)
	assert.Equal(t, root.SeedEndID, position, "seed retries keep their position after reopen")
	retried, err := reopened.AppendRunRecord(ctx, ordinary)
	require.NoError(t, err)
	assert.Equal(t, noted.ID, retried.ID)
	assert.False(t, retried.Inserted)
	replayed, err := reopened.StartRootRun(ctx, rootCommand)
	require.NoError(t, err)
	assert.Equal(t, started.Started.ID, replayed.Started.ID)
	assert.Equal(t, session.RunStatusSuspended, replayed.RunStatus)
	replayedSuccessor, err := reopened.StartRootRun(ctx, successorCommand)
	require.NoError(t, err)
	assert.Equal(t, admitted.Started.ID, replayedSuccessor.Started.ID)
	resolution, err := reopened.RelinquishRunSuspension(ctx, storage.RunSuspensionRelinquishment{RunID: successor.RunID, SuspensionID: checkpoint.ID, Reason: "user_requested"})
	require.NoError(t, err)
	assert.Equal(t, relinquished, resolution, "the first closure time survives reopen")
	assert.Equal(t, after, takeDurableSnapshot(t, reopened, declaration, root, successor), "exact retries write nothing")
}

// TestOpenChecksSchemaVersion proves reopening is idempotent and an unknown
// schema version or a DSN-like path is rejected.
func TestOpenChecksSchemaVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.db")
	store, err := runstore.Open(t.Context(), path)
	require.NoError(t, err)
	require.NoError(t, store.Close())
	again, err := runstore.Open(t.Context(), path)
	require.NoError(t, err, "opening the current schema is idempotent")
	require.NoError(t, again.Close())
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	_, err = raw.ExecContext(t.Context(), "PRAGMA user_version = 99")
	require.NoError(t, err)
	require.NoError(t, raw.Close())
	_, err = runstore.Open(t.Context(), path)
	require.ErrorContains(t, err, "unsupported schema version 99")
	_, err = runstore.Open(t.Context(), path+"?mode=ro")
	require.ErrorContains(t, err, "invalid database path")
}

// TestHandlesRaceSeedPublication races two Store handles on one file publishing
// competing attempts and proves exactly one attempt is published.
func TestHandlesRaceSeedPublication(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.db")
	handles := [2]*runstore.Store{openStoreAt(t, path), openStoreAt(t, path)}
	ctx := t.Context()
	subject := genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("agent")}
	for round := range 20 {
		runID := fmt.Sprintf("run-%d", round)
		operation := storage.PreparationOperation{Subject: subject, RunID: runID, CommandID: "command"}
		_, err := handles[0].BeginRunPreparationOperation(ctx, storage.RunPreparationBegin{Operation: operation, Origin: rootExecutionOrigin(runID)})
		require.NoError(t, err)
		publications := make([]storage.SeedPublication, len(handles))
		for i, handle := range handles {
			declaration := storage.SeedDeclaration{Subject: subject, RunID: runID, CommandID: "command", AttemptID: fmt.Sprintf("attempt-%d", i), Kind: storage.SeedLiteral}
			_, err := handle.BeginRunSeed(ctx, declaration)
			require.NoError(t, err)
			publications[i] = appendSeedTestCompletion(t, handle, declaration, storage.EmptySeedEndID)
		}
		errs := raceHandles(handles, func(i int, handle *runstore.Store) error {
			return handle.PublishRunSeed(ctx, publications[i])
		})
		winner := requireOneWinner(t, errs, storage.ErrSeedConflict)
		for i, handle := range handles {
			accepted, found, err := handle.FindRunPreparation(ctx, operation)
			require.NoError(t, err)
			require.True(t, found)
			assert.Equal(t, publications[winner].AttemptID, accepted.Seed.Declaration.AttemptID)
			settled, found, err := handle.SettleRunPreparation(ctx, storage.PreparationAttempt{Operation: operation, AttemptID: publications[i].AttemptID})
			require.NoError(t, err)
			require.True(t, found, "settling either attempt returns the single winner")
			assert.Equal(t, accepted, settled)
		}
		require.ErrorIs(t, handles[1-winner].PublishRunSeed(ctx, publications[1-winner]), storage.ErrSeedConflict)
	}
}

// TestHandlesRaceContinuations races two Store handles on one file continuing
// one suspension and proves one successor wins permanently while the loser
// leaves no run or records.
func TestHandlesRaceContinuations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.db")
	handles := [2]*runstore.Store{openStoreAt(t, path), openStoreAt(t, path)}
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Millisecond)
	subject := genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("agent")}
	_, err := handles[0].CreateSession(ctx, "session", now)
	require.NoError(t, err)
	for round := range 20 {
		predecessor := session.RunStart{Subject: subject, RunID: fmt.Sprintf("predecessor-%d", round), SessionID: "session", StartedAt: now}
		predecessor = publishStartHistory(t, handles[0], predecessor, "")
		_, err := handles[1].StartRootRun(ctx, rootStartCommand(t, predecessor))
		require.NoError(t, err)
		_, err = handles[0].RecordRunSuspension(ctx, storage.RunSuspension{
			RunID: predecessor.RunID, Suspension: session.RunSuspension{ID: "checkpoint", Data: []byte(`{"saved":true}`)},
			Record: suspendedRecord(t, "suspended", predecessor, "checkpoint"),
		})
		require.NoError(t, err)
		commands := make([]storage.RootRunStart, len(handles))
		for i := range handles {
			successor := session.RunStart{Subject: subject, RunID: fmt.Sprintf("successor-%d-%d", round, i), SessionID: "session", PredecessorRunID: predecessor.RunID, StartedAt: now}
			commands[i] = rootStartCommand(t, publishStartHistory(t, handles[i], successor, ""))
		}
		errs := raceHandles(handles, func(i int, handle *runstore.Store) error {
			_, err := handle.StartRootRun(ctx, commands[i])
			return err
		})
		winner := requireOneWinner(t, errs, session.ErrRunConflict)
		loser := commands[1-winner].Run.RunID
		for _, handle := range handles {
			meta, err := handle.LoadRun(ctx, predecessor.RunID)
			require.NoError(t, err)
			assert.Equal(t, session.ContinuedSuspension{RunID: commands[winner].Run.RunID}, meta.SuspensionResolution)
			_, err = handle.LoadRun(ctx, loser)
			require.ErrorIs(t, err, session.ErrRunNotFound)
			_, err = handle.ListRunRecords(ctx, loser, "", 10)
			require.ErrorIs(t, err, session.ErrRunNotFound, "the losing successor leaves no records")
			_, err = handle.StartRootRun(ctx, commands[1-winner])
			require.ErrorIs(t, err, session.ErrRunConflict, "the winner stays permanent")
		}
	}
}

// TestPagesDoNotReadRecordsBeyondThePage proves transcript and run-record
// pages never read rows after the returned page. The test corrupts the stored
// subject of every record after the page through a separate connection; any
// read of those rows fails to decode, so the first pages succeed only if the
// store never reads them, and the following page then fails.
func TestPagesDoNotReadRecordsBeyondThePage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.db")
	store := openStoreAt(t, path)
	ctx := t.Context()
	start := session.RunStart{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("agent")}, RunID: "run", StartedAt: time.Now().UTC().Truncate(time.Millisecond)}
	start = publishStartHistory(t, store, start, "")
	_, err := store.StartOneShotRun(ctx, storage.OneShotRunStart{RequestDigest: [32]byte{1}, Run: start, Started: startedRecord(t, "started", start)})
	require.NoError(t, err)
	// Record 1 is RunStarted; records 2-5 are transcript records. Records 2
	// and 3 fill the 1 MiB transcript budget exactly.
	var ids []string
	for i, size := range []int{transcriptPageMaxPayloadBytes - 256, 256, 256, 256} {
		message := &model.Message{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{}}}
		empty, err := transcript.EncodeRunLogDelta([]*model.Message{message})
		require.NoError(t, err)
		message.Parts[0] = model.TextPart{Text: strings.Repeat("x", size-len(empty))}
		payload, err := transcript.EncodeRunLogDelta([]*model.Message{message})
		require.NoError(t, err)
		stored, err := store.AppendRunRecord(ctx, &runlog.Event{
			RunID: start.RunID, Subject: start.Subject, EventKey: fmt.Sprint("message-", i),
			Timestamp: start.StartedAt, Type: transcript.RunLogMessagesAppended, Payload: payload,
		})
		require.NoError(t, err)
		ids = append(ids, stored.ID)
	}
	raw, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(30000)")
	require.NoError(t, err)
	_, err = raw.ExecContext(ctx, `UPDATE records SET subject = x'00' WHERE run_id = ? AND seq > 3`, start.RunID)
	require.NoError(t, err)
	require.NoError(t, raw.Close())

	page, err := store.ListRunTranscriptRecords(ctx, start.RunID, ids[3], "", 10000)
	require.NoError(t, err, "the transcript page reads only its own records")
	require.Len(t, page.Events, 2)
	assert.Equal(t, ids[1], page.NextCursor)
	_, err = store.ListRunTranscriptRecords(ctx, start.RunID, ids[3], page.NextCursor, 10000)
	require.ErrorContains(t, err, "subject", "the corrupted records are detected when read")

	records, err := store.ListRunRecords(ctx, start.RunID, "", 3)
	require.NoError(t, err, "the run-record page reads only its own records")
	require.Len(t, records.Events, 3)
	assert.Equal(t, "3", records.NextCursor)
	_, err = store.ListRunRecords(ctx, start.RunID, records.NextCursor, 3)
	require.ErrorContains(t, err, "subject")
}

// TestSeedPagesDoNotReadRecordsBeyondThePage proves seed history and
// compiled-start pages never read rows after the returned page. Each range
// holds three records of which only one fits the page budget. The test makes
// every later row unreadable through a separate connection (a literal_final
// value that is not a boolean); the first pages succeed only if the store
// never reads those rows, and the following pages then fail.
func TestSeedPagesDoNotReadRecordsBeyondThePage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.db")
	store := openStoreAt(t, path)
	ctx := t.Context()
	declaration := storage.SeedDeclaration{
		Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("agent")},
		RunID:   "run", CommandID: "command", AttemptID: "attempt", Kind: storage.SeedLiteral,
	}
	bindSeedTestOperation(t, store, declaration, rootExecutionOrigin(declaration.RunID))
	_, err := store.BeginRunSeed(ctx, declaration)
	require.NoError(t, err)
	end := storage.EmptySeedEndID
	appendRecord := func(key string, record storage.SeedRecord) {
		record.Key, record.PreviousID = key, end
		end, err = store.AppendRunSeed(ctx, storage.SeedAppend{RunID: declaration.RunID, AttemptID: declaration.AttemptID, Record: record})
		require.NoError(t, err)
	}
	// Each history record encodes to about 600 KiB, so two exceed the page.
	for i := range 3 {
		messages, err := transcript.EncodeRunLogDelta([]*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: strings.Repeat("x", 600<<10)}}}})
		require.NoError(t, err)
		appendRecord(fmt.Sprint("history-", i), storage.SeedRecord{Messages: messages})
	}
	historyEnd := end
	// Each compiled-start part base64-encodes to about 600 KiB.
	prepared := bytes.Repeat([]byte{7}, 450<<10)
	for i := range 3 {
		appendRecord(fmt.Sprint("prepared-", i), storage.SeedRecord{Prepared: prepared})
	}
	require.NoError(t, store.PublishRunSeed(ctx, storage.SeedPublication{
		RunID: declaration.RunID, AttemptID: declaration.AttemptID, SeedEndID: historyEnd, EndID: end,
		PreparedBytes: int64(3 * len(prepared)),
	}))
	raw, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(30000)")
	require.NoError(t, err)
	_, err = raw.ExecContext(ctx, `UPDATE seed_records SET literal_final = 5 WHERE run_id = ? AND position NOT IN (1, 4)`, declaration.RunID)
	require.NoError(t, err)
	require.NoError(t, raw.Close())

	history, err := store.ListRunSeedRecords(ctx, declaration.RunID, historyEnd, "", 10)
	require.NoError(t, err, "the history page reads only its own records")
	require.Len(t, history.Records, 1)
	assert.Equal(t, "1", history.NextCursor)
	_, err = store.ListRunSeedRecords(ctx, declaration.RunID, historyEnd, history.NextCursor, 10)
	require.ErrorContains(t, err, "bool", "the unreadable rows are detected when read")

	compiled, err := store.ListRunPreparationRecords(ctx, declaration.RunID, end, "", 10)
	require.NoError(t, err, "the compiled-start page reads only its own records")
	require.Len(t, compiled.Records, 1)
	assert.Equal(t, "4", compiled.NextCursor)
	assert.Equal(t, prepared, compiled.Records[0].Prepared)
	_, err = store.ListRunPreparationRecords(ctx, declaration.RunID, end, compiled.NextCursor, 10)
	require.ErrorContains(t, err, "bool")
}

// TestHandlesRaceRelinquishAndContinuation races two Store handles on one
// file: one relinquishes a root suspension while the other starts its
// continuation. Exactly one resolution wins permanently, both handles see it,
// and a losing successor leaves no run or records.
func TestHandlesRaceRelinquishAndContinuation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.db")
	handles := [2]*runstore.Store{openStoreAt(t, path), openStoreAt(t, path)}
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Millisecond)
	subject := genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("agent")}
	_, err := handles[0].CreateSession(ctx, "session", now)
	require.NoError(t, err)
	outcomes := make(map[string]int)
	for round := range 40 {
		predecessor := session.RunStart{Subject: subject, RunID: fmt.Sprintf("predecessor-%d", round), SessionID: "session", StartedAt: now}
		predecessor = publishStartHistory(t, handles[0], predecessor, "")
		_, err := handles[0].StartRootRun(ctx, rootStartCommand(t, predecessor))
		require.NoError(t, err)
		_, err = handles[1].RecordRunSuspension(ctx, storage.RunSuspension{
			RunID: predecessor.RunID, Suspension: session.RunSuspension{ID: "checkpoint", Data: []byte(`{"saved":true}`)},
			Record: suspendedRecord(t, "suspended", predecessor, "checkpoint"),
		})
		require.NoError(t, err)
		successor := session.RunStart{Subject: subject, RunID: fmt.Sprintf("successor-%d", round), SessionID: "session", PredecessorRunID: predecessor.RunID, StartedAt: now}
		command := rootStartCommand(t, publishStartHistory(t, handles[1], successor, ""))
		relinquish := storage.RunSuspensionRelinquishment{RunID: predecessor.RunID, SuspensionID: "checkpoint", Reason: "user_requested"}
		var resolution session.SuspensionResolution
		errs := raceHandles(handles, func(i int, handle *runstore.Store) error {
			if i == 0 {
				var err error
				resolution, err = handle.RelinquishRunSuspension(ctx, relinquish)
				return err
			}
			_, err := handle.StartRootRun(ctx, command)
			return err
		})
		require.NoError(t, errs[0], "relinquishment returns the winning resolution")
		for _, handle := range handles {
			meta, err := handle.LoadRun(ctx, predecessor.RunID)
			require.NoError(t, err)
			assert.Equal(t, resolution, meta.SuspensionResolution)
			again, err := handle.RelinquishRunSuspension(ctx, relinquish)
			require.NoError(t, err)
			assert.Equal(t, resolution, again, "the winner stays permanent")
		}
		switch resolution.(type) {
		case session.RelinquishedSuspension:
			outcomes["relinquished"]++
			require.ErrorIs(t, errs[1], session.ErrRunConflict)
			for _, handle := range handles {
				_, err := handle.LoadRun(ctx, successor.RunID)
				require.ErrorIs(t, err, session.ErrRunNotFound)
				_, err = handle.ListRunRecords(ctx, successor.RunID, "", 10)
				require.ErrorIs(t, err, session.ErrRunNotFound, "the losing successor leaves no records")
				_, err = handle.StartRootRun(ctx, command)
				require.ErrorIs(t, err, session.ErrRunConflict)
			}
		case session.ContinuedSuspension:
			outcomes["continued"]++
			require.NoError(t, errs[1])
			assert.Equal(t, session.ContinuedSuspension{RunID: successor.RunID}, resolution)
		default:
			require.FailNow(t, "missing exclusive resolution")
		}
	}
	t.Logf("outcomes over 40 rounds: %v", outcomes)
}

// takeDurableSnapshot reads every value TestReopenPreservesState writes.
func takeDurableSnapshot(t *testing.T, store contractStore, declaration storage.SeedDeclaration, runs ...session.RunStart) durableSnapshot {
	t.Helper()
	ctx := t.Context()
	snapshot := durableSnapshot{runs: make(map[string]session.RunMeta), records: make(map[string][]*runlog.Event)}
	var err error
	snapshot.session, err = store.LoadSession(ctx, declaration.SessionID)
	require.NoError(t, err)
	for _, start := range runs {
		snapshot.runs[start.RunID], err = store.LoadRun(ctx, start.RunID)
		require.NoError(t, err)
		snapshot.records[start.RunID] = runRecords(t, store, start.RunID)
	}
	page, err := store.ListSessionRunRecords(ctx, declaration.SessionID, "", 1000)
	require.NoError(t, err)
	snapshot.sessionRecords = page.Events
	snapshot.suspension, err = store.LoadRunSuspension(ctx, runs[0].RunID)
	require.NoError(t, err)
	snapshot.seed, err = store.LoadRunSeed(ctx, runs[0].RunID, runs[0].SeedEndID)
	require.NoError(t, err)
	snapshot.seedRecords, err = store.ListRunSeedRecords(ctx, runs[0].RunID, runs[0].SeedEndID, "", 10)
	require.NoError(t, err)
	var found bool
	snapshot.preparation, found, err = store.FindRunPreparation(ctx, storage.PreparationOperation{
		Subject: declaration.Subject, RunID: declaration.RunID, SessionID: declaration.SessionID, CommandID: declaration.CommandID,
	})
	require.NoError(t, err)
	require.True(t, found)
	return snapshot
}

// raceHandles releases one call per handle at the same time and returns each
// call's error by handle index.
func raceHandles(handles [2]*runstore.Store, call func(int, *runstore.Store) error) [2]error {
	var (
		errs    [2]error
		release = make(chan struct{})
		workers sync.WaitGroup
	)
	for i, handle := range handles {
		workers.Go(func() {
			<-release
			errs[i] = call(i, handle)
		})
	}
	close(release)
	workers.Wait()
	return errs
}

// requireOneWinner requires exactly one successful call and a permanent
// rejection of the other, and returns the winning handle index.
func requireOneWinner(t *testing.T, errs [2]error, rejection error) int {
	t.Helper()
	require.True(t, (errs[0] == nil) != (errs[1] == nil), "exactly one call must win: %v", errs)
	winner := 0
	if errs[0] != nil {
		winner = 1
	}
	loserErr := errs[1-winner]
	require.ErrorIs(t, loserErr, rejection)
	var contractErr *storage.ContractError
	require.ErrorAs(t, loserErr, &contractErr)
	return winner
}
