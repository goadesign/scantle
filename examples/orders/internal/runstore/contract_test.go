package runstore_test

// The contract suite ports goa-ai's in-memory reference store tests so they use
// only the public storage.Store and host session API. runContract runs every
// case against the reference store and against the SQLite store; both must
// pass every case.

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/goadesign/scantle/examples/orders/internal/runstore"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	genrunterminal "goa.design/goa-ai/registry/gen/runterminal"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
	"goa.design/goa-ai/runtime/agent/storage/inmem"
)

type (
	// contractStore is the public surface shared by the reference store and
	// the SQLite store: the worker-facing Store plus host session methods.
	contractStore interface {
		storage.Store
		// CreateSession creates the session or returns the existing one.
		CreateSession(ctx context.Context, sessionID string, createdAt time.Time) (session.Session, error)
		// LoadSession returns one session.
		LoadSession(ctx context.Context, sessionID string) (session.Session, error)
		// EndSession ends one session.
		EndSession(ctx context.Context, sessionID string, endedAt time.Time) (session.Session, error)
		// PurgeSession permanently removes an ended session.
		PurgeSession(ctx context.Context, sessionID string) error
		// ListRunsBySession lists the session's runs filtered by status.
		ListRunsBySession(ctx context.Context, sessionID string, statuses []session.RunStatus) ([]session.RunMeta, error)
	}

	// factory creates a fresh, empty store for one contract case.
	factory struct {
		// open returns a new empty store owned by t.
		open func(t *testing.T) contractStore
	}

	// contractCase is one ported reference test.
	contractCase struct {
		// name is the reference test name without its Test prefix.
		name string
		// run executes the case against stores created by f.
		run func(f factory, t *testing.T)
	}
)

const (
	// transcriptPageMaxRecords is the reference per-page record limit.
	transcriptPageMaxRecords = 512
	// transcriptPageMaxPayloadBytes is the reference per-page payload limit.
	transcriptPageMaxPayloadBytes = 1 << 20
)

// contractCases lists every ported reference test.
var contractCases = []contractCase{
	{"ChildContinuationPreservesCallAcrossExecutionParents", factory.ChildContinuationPreservesCallAcrossExecutionParents},
	{"ClosedRequestSelectsOriginalStart", factory.ClosedRequestSelectsOriginalStart},
	{"RequestBindingDoesNotResumeRunningRun", factory.RequestBindingDoesNotResumeRunningRun},
	{"ClosedRequestKeepsEndedSessionDecision", factory.ClosedRequestKeepsEndedSessionDecision},
	{"ClosedRequestContendsWithClosure", factory.ClosedRequestContendsWithClosure},
	{"EndedSessionStartRejectsCompletionAndSuspension", factory.EndedSessionStartRejectsCompletionAndSuspension},
	{"ClosedRequestRequiresExactPublishedHistory", factory.ClosedRequestRequiresExactPublishedHistory},
	{"ClosedRequestCannotRecreatePurgedPublication", factory.ClosedRequestCannotRecreatePurgedPublication},
	{"ContinuationAdmissionSelectsOneSuccessor", factory.ContinuationAdmissionSelectsOneSuccessor},
	{"UnstartedTerminalFirstDecisionWinsWithoutRun", factory.UnstartedTerminalFirstDecisionWinsWithoutRun},
	{"UnstartedTerminalOrdersWithStartAndPublication", factory.UnstartedTerminalOrdersWithStartAndPublication},
	{"UnstartedTerminalRacesFirstStart", factory.UnstartedTerminalRacesFirstStart},
	{"UnstartedTerminalRejectsLatePreparationPublication", factory.UnstartedTerminalRejectsLatePreparationPublication},
	{"UnstartedTerminalRejectsChildCollisionBeforePreparation", factory.UnstartedTerminalRejectsChildCollisionBeforePreparation},
	{"UnstartedContinuationKeepsOriginalInvocation", factory.UnstartedContinuationKeepsOriginalInvocation},
	{"UnstartedServiceContinuationPreservesActualStart", factory.UnstartedServiceContinuationPreservesActualStart},
	{"RootStartPersistsOneImmutableOutcome", factory.RootStartPersistsOneImmutableOutcome},
	{"RootStartRetryKeepsOriginalOutcomeAfterCompletion", factory.RootStartRetryKeepsOriginalOutcomeAfterCompletion},
	{"StartRejectsMismatchedStartedRecord", factory.StartRejectsMismatchedStartedRecord},
	{"RecordRetryRequiresExactTimestamp", factory.RecordRetryRequiresExactTimestamp},
	{"AppendRunRecordRejectsLifecycleRecord", factory.AppendRunRecordRejectsLifecycleRecord},
	{"AppendRunRecordRejectsNewRecordsAfterRunStops", factory.AppendRunRecordRejectsNewRecordsAfterRunStops},
	{"RecordListsRejectUnknownStateAndInvalidCursor", factory.RecordListsRejectUnknownStateAndInvalidCursor},
	{"EndedSessionStartStoresCancellationIntent", factory.EndedSessionStartStoresCancellationIntent},
	{"ChildStartStoresParentLinkAndChildStartTogether", factory.ChildStartStoresParentLinkAndChildStartTogether},
	{"OneShotChildStartStoresParentLinkAndChildStartTogether", factory.OneShotChildStartStoresParentLinkAndChildStartTogether},
	{"OneShotChildStartRequiresRunningSessionlessParent", factory.OneShotChildStartRequiresRunningSessionlessParent},
	{"SessionChildStartRequiresRunningParent", factory.SessionChildStartRequiresRunningParent},
	{"ChildStartAfterPurgeReportsPurgedSession", factory.ChildStartAfterPurgeReportsPurgedSession},
	{"RunRecordsMustMatchStoredRunOwner", factory.RunRecordsMustMatchStoredRunOwner},
	{"OneShotCancellationAndTerminalAreRecorded", factory.OneShotCancellationAndTerminalAreRecorded},
	{"SuspensionCheckpointAndTerminalRecordAreAtomic", factory.SuspensionCheckpointAndTerminalRecordAreAtomic},
	{"RepairRunTerminalKeepsWorkflowResult", factory.RepairRunTerminalKeepsWorkflowResult},
	{"RepairRunTerminalRetryReportsExistingState", factory.RepairRunTerminalRetryReportsExistingState},
	{"RepairRunSuspensionKeepsWorkflowResult", factory.RepairRunSuspensionKeepsWorkflowResult},
	{"RepairRunSuspensionRetryReportsExistingState", factory.RepairRunSuspensionRetryReportsExistingState},
	{"LifecycleRetriesRequireOriginalRecordKeys", factory.LifecycleRetriesRequireOriginalRecordKeys},
	{"LifecycleCommandsRejectContradictoryTypedRecords", factory.LifecycleCommandsRejectContradictoryTypedRecords},
	{"OneShotChildContinuationClaimsOneSuccessor", factory.OneShotChildContinuationClaimsOneSuccessor},
	{"PreparationOwnerRejectsRootChildCollision", factory.PreparationOwnerRejectsRootChildCollision},
	{"PreparationSettlementBeforeBeginDoesNotChooseOwner", factory.PreparationSettlementBeforeBeginDoesNotChooseOwner},
	{"SeedOriginComesFromPreparationBinding", factory.SeedOriginComesFromPreparationBinding},
	{"LargeSeedOriginSurvivesChildStartAndEarlyTerminal", factory.LargeSeedOriginSurvivesChildStartAndEarlyTerminal},
	{"RecordRunTerminalValidatesTextBeforeAppend", factory.RecordRunTerminalValidatesTextBeforeAppend},
	{"SeedChainLinearHistoryAndComposedReasoning", factory.SeedChainLinearHistoryAndComposedReasoning},
	{"SeedLiteralClosureAndExactRetry", factory.SeedLiteralClosureAndExactRetry},
	{"SeedLiteralSettlementAndPurge", factory.SeedLiteralSettlementAndPurge},
	{"SeedLiteralReaderAcrossRealPages", factory.SeedLiteralReaderAcrossRealPages},
	{"SeedPublicationAndExactRetry", factory.SeedPublicationAndExactRetry},
	{"SeedPurgeRemovesUnstartedPublication", factory.SeedPurgeRemovesUnstartedPublication},
	{"SeedCapturedSourceAndSingleReference", factory.SeedCapturedSourceAndSingleReference},
	{"SeedRejectsUnusableSources", factory.SeedRejectsUnusableSources},
	{"SeedCancellationBeforeReadOrWrite", factory.SeedCancellationBeforeReadOrWrite},
	{"ServiceContinuationHasOneSuccessor", factory.ServiceContinuationHasOneSuccessor},
	{"StartReplayReturnsCurrentRunStatus", factory.StartReplayReturnsCurrentRunStatus},
	{"ServiceSubjectStorageIsolationAndReplay", factory.ServiceSubjectStorageIsolationAndReplay},
	{"ServicePreparationSettlementCopiesSubject", factory.ServicePreparationSettlementCopiesSubject},
	{"ServiceSubjectLifecycleAndAgentChild", factory.ServiceSubjectLifecycleAndAgentChild},
	{"RelinquishSuspensionPreservesHistoryAndRetry", factory.RelinquishSuspensionPreservesHistoryAndRetry},
	{"RelinquishSuspensionCompetesWithContinuation", factory.RelinquishSuspensionCompetesWithContinuation},
	{"RelinquishSuspensionReturnsSuccessorWithDifferentPredecessorReason", factory.RelinquishSuspensionReturnsSuccessorWithDifferentPredecessorReason},
	{"RelinquishSuspensionRejectsWrongOwnerOrCheckpoint", factory.RelinquishSuspensionRejectsWrongOwnerOrCheckpoint},
	{"SynchronousStartPreservesExactCommand", factory.SynchronousStartPreservesExactCommand},
	{"SynchronousAndEngineStartIdentitiesCannotMix", factory.SynchronousAndEngineStartIdentitiesCannotMix},
	{"SynchronousAndEngineStartsContendAtomically", factory.SynchronousAndEngineStartsContendAtomically},
	{"TerminalSelectedResultSharesFirstWinner", factory.TerminalSelectedResultSharesFirstWinner},
	{"TerminalSelectedResultRejectsMalformedBeforeClosure", factory.TerminalSelectedResultRejectsMalformedBeforeClosure},
	{"TranscriptPageIncludesExactPayloadByteLimit", factory.TranscriptPageIncludesExactPayloadByteLimit},
	{"TranscriptPageStopsBeforeCopyingTheNextRecord", factory.TranscriptPageStopsBeforeCopyingTheNextRecord},
	{"TranscriptPageCapsAnOversizedRequestedCount", factory.TranscriptPageCapsAnOversizedRequestedCount},
	{"TranscriptPageRejectsAnUnsupportedSingleRecord", factory.TranscriptPageRejectsAnUnsupportedSingleRecord},
}

// TestContract runs the ported reference suite against both stores.
func TestContract(t *testing.T) {
	t.Run("inmem", func(t *testing.T) {
		t.Parallel()
		runContract(t, func(*testing.T) contractStore { return inmem.New() })
	})
	t.Run("sqlite", func(t *testing.T) {
		t.Parallel()
		runContract(t, openTestStore)
	})
}

// runContract runs every contract case with a fresh store from newStore.
func runContract(t *testing.T, newStore func(t *testing.T) contractStore) {
	f := factory{open: newStore}
	for _, test := range contractCases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			test.run(f, t)
		})
	}
}

// openTestStore opens a SQLite store in a new temporary directory and closes
// it when the test ends.
func openTestStore(t *testing.T) contractStore {
	t.Helper()
	return openStoreAt(t, filepath.Join(t.TempDir(), "runtime.db"))
}

// openStoreAt opens one SQLite store handle on path and closes it when the
// test ends.
func openStoreAt(t *testing.T, path string) *runstore.Store {
	t.Helper()
	store, err := runstore.Open(t.Context(), path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	return store
}

// rootExecutionOrigin is the invocation of a run started directly by a host.
func rootExecutionOrigin(runID string) genregistry.ExecutionInvocation {
	return genregistry.ExecutionInvocation{Invocation: genregistry.NewExecutionInvocationKindRoot(&genregistry.RootInvocation{RunID: runID})}
}

// runRecords returns every record of a run through the public paging API,
// or nil when the run does not exist.
func runRecords(t *testing.T, store contractStore, runID string) []*runlog.Event {
	t.Helper()
	var result []*runlog.Event
	cursor := ""
	for {
		page, err := store.ListRunRecords(t.Context(), runID, cursor, 100)
		if errors.Is(err, session.ErrRunNotFound) {
			return nil
		}
		require.NoError(t, err)
		result = append(result, page.Events...)
		if page.NextCursor == "" {
			return result
		}
		cursor = page.NextCursor
	}
}

// recordByKey returns the run's record stored under an event key, failing
// the test when it is missing.
func recordByKey(t *testing.T, store contractStore, runID, key string) *runlog.Event {
	t.Helper()
	for _, record := range runRecords(t, store, runID) {
		if record.EventKey == key {
			return record
		}
	}
	require.FailNow(t, "record not found", "run %q key %q", runID, key)
	return nil
}

// recordSnapshot returns every record of the named runs, keyed by run ID.
// Runs without records are omitted, matching the reference store's map.
func recordSnapshot(t *testing.T, store contractStore, runIDs ...string) map[string][]*runlog.Event {
	t.Helper()
	result := make(map[string][]*runlog.Event)
	for _, runID := range runIDs {
		if records := runRecords(t, store, runID); len(records) > 0 {
			result[runID] = records
		}
	}
	return result
}

// unstartedTerminalReplays reports whether command's exact early terminal
// outcome is stored, by replaying it: only a stored identical outcome replays.
func unstartedTerminalReplays(t *testing.T, store contractStore, command genrunterminal.UnstartedExecutionTerminal) bool {
	t.Helper()
	result, err := store.RecordUnstartedExecutionTerminal(t.Context(), command)
	require.NoError(t, err)
	_, replayed := result.Result.AsReplayed()
	return replayed
}

// newStore returns a fresh empty store for the running case.
func (f factory) newStore(t *testing.T) contractStore {
	t.Helper()
	return f.open(t)
}
