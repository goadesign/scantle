package runstore_test

// Fixtures shared by the contract cases build typed lifecycle records with the
// production hooks codec and publish the initial history every start needs.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
	"goa.design/goa-ai/runtime/agent/tools"
)

// publishStartHistory binds the original invocation and publishes empty history
// or the suspended predecessor's saved history before a fixture starts the run.
func publishStartHistory(t *testing.T, store contractStore, start session.RunStart, toolCallID string) session.RunStart {
	t.Helper()
	origin := rootExecutionOrigin(start.RunID)
	declaration := storage.SeedDeclaration{
		Subject: start.Subject, RunID: start.RunID, SessionID: start.SessionID, CommandID: start.RunID, AttemptID: start.RunID, Kind: storage.SeedLiteral,
	}
	if start.ParentRunID != "" && start.PredecessorRunID == "" {
		require.NotEmpty(t, toolCallID, "a fresh child must name its parent tool call")
		origin = genregistry.ExecutionInvocation{Invocation: genregistry.NewExecutionInvocationKindTool(&genregistry.ToolInvocation{RunID: start.ParentRunID, ToolCallID: toolCallID})}
	}
	if start.PredecessorRunID != "" {
		predecessor, err := store.LoadRun(t.Context(), start.PredecessorRunID)
		require.NoError(t, err)
		origin = predecessor.Origin
		declaration.Kind = storage.SeedContinuation
		declaration.PredecessorRunID = start.PredecessorRunID
		declaration.SourceRunID = start.PredecessorRunID
		records := runRecords(t, store, start.PredecessorRunID)
		require.NotEmpty(t, records)
		declaration.SourceEndID = records[len(records)-1].ID
	}
	bindSeedTestOperation(t, store, declaration, origin)
	seed, err := store.BeginRunSeed(t.Context(), declaration)
	require.NoError(t, err)
	start.SeedEndID = storage.EmptySeedEndID
	if seed.Source != nil {
		start.SeedEndID, err = store.AppendRunSeed(t.Context(), storage.SeedAppend{
			RunID: start.RunID, AttemptID: declaration.AttemptID, Record: storage.SeedRecord{
				Key: "source", PreviousID: storage.EmptySeedEndID, Prefix: seed.Source,
			},
		})
		require.NoError(t, err)
	}
	require.NoError(t, store.PublishRunSeed(t.Context(), appendSeedTestCompletion(t, store, declaration, start.SeedEndID)))
	return start
}

// rootStartCommand builds the two typed records whose selection depends on the
// session state observed by StartRootRun.
func rootStartCommand(t *testing.T, start session.RunStart) storage.RootRunStart {
	t.Helper()
	return storage.RootRunStart{RequestDigest: [32]byte{1},
		Run:     start,
		Started: startedRecord(t, "started", start),
	}
}

// startedRecord encodes the immutable run identity with the hooks codec used
// by production workflows.
func startedRecord(t *testing.T, key string, start session.RunStart) *runlog.Event {
	t.Helper()
	return hookRecord(t, key, start.StartedAt, hooks.NewRunStartedEvent(
		start.RunID,
		start.Subject,
		start.SessionID,
		start.ParentRunID,
		start.PredecessorRunID,
		start.Labels,
	))
}

// completedRecord encodes one final lifecycle outcome for a run.
func completedRecord(t *testing.T, key string, start session.RunStart, status string, cancellation *run.Cancellation) *runlog.Event {
	t.Helper()
	phase := run.PhaseCompleted
	var eventErr error
	switch status {
	case "failed":
		phase = run.PhaseFailed
		eventErr = errors.New("failed")
	case "canceled":
		phase = run.PhaseCanceled
		eventErr = context.Canceled
	}
	return hookRecord(t, key, start.StartedAt, hooks.NewRunCompletedEvent(
		start.RunID,
		start.Subject,
		start.SessionID,
		status,
		phase,
		start.Labels,
		eventErr,
		cancellation,
	))
}

// childLinkRecord encodes the parent-owned record that names the exact child
// run accepted by StartChildRun.
func childLinkRecord(t *testing.T, key string, parent, child session.RunStart) *runlog.Event {
	t.Helper()
	return hookRecord(t, key, child.StartedAt, hooks.NewChildRunLinkedEvent(
		parent.RunID,
		parent.Subject,
		parent.SessionID,
		tools.Ident("child"),
		"tool-call",
		child.RunID,
		subjectAgent(t, child.Subject),
	))
}

// suspendedRecord encodes the terminal event that makes one checkpoint
// available for continuation.
func suspendedRecord(t *testing.T, key string, start session.RunStart, suspensionID string) *runlog.Event {
	t.Helper()
	return hookRecord(t, key, start.StartedAt, hooks.NewRunSuspendedEvent(
		start.RunID,
		start.Subject,
		start.SessionID,
		suspensionID,
		"v6",
		1,
		nil,
	))
}

// cancellationRecord builds the runtime-owned record that preserves the first
// accepted cancellation reason.
func cancellationRecord(t *testing.T, key string, start session.RunStart, reason string) *runlog.Event {
	t.Helper()
	payload, err := json.Marshal(map[string]string{"reason": reason})
	require.NoError(t, err)
	return &runlog.Event{
		EventKey:  key,
		RunID:     start.RunID,
		Subject:   start.Subject,
		SessionID: start.SessionID,
		Type:      storage.CancellationRecordType,
		Payload:   payload,
		Timestamp: start.StartedAt,
	}
}

// hookRecord serializes one event for a direct Store call. The supplied key
// and time preserve the record identity without creating a workflow request.
func hookRecord(t *testing.T, key string, at time.Time, event hooks.Event) *runlog.Event {
	t.Helper()
	payload, err := hooks.EncodeRecordPayload(event)
	require.NoError(t, err)
	return &runlog.Event{
		EventKey:  key,
		RunID:     event.RunID(),
		Subject:   event.Subject(),
		SessionID: event.SessionID(),
		TurnID:    event.TurnID(),
		Type:      event.Type(),
		Payload:   payload,
		Timestamp: time.UnixMilli(at.UnixMilli()).UTC(),
	}
}

// record builds an ordinary record for runID with an empty JSON payload.
func record(key, runID string, subject genrunidentity.RunSubject, sessionID string, typ runlog.Type) *runlog.Event {
	return &runlog.Event{
		EventKey: key, RunID: runID, Subject: subject, SessionID: sessionID,
		Type: typ, Payload: []byte(`{}`), Timestamp: time.Now().UTC().Truncate(time.Millisecond),
	}
}

// activeRunStore returns a store with one active session and an unpersisted
// root-run identity.
func (f factory) activeRunStore(t *testing.T) (contractStore, session.RunStart) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Millisecond)
	store := f.newStore(t)
	_, err := store.CreateSession(t.Context(), "session", now)
	require.NoError(t, err)
	return store, session.RunStart{
		Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("agent"))}, RunID: "run", SessionID: "session", StartedAt: now,
		Labels: map[string]string{"site": "one"},
	}
}

// runningRootStore starts the root identity returned with an active session.
func (f factory) runningRootStore(t *testing.T) (contractStore, session.RunStart) {
	t.Helper()
	store, start := f.activeRunStore(t)
	start = publishStartHistory(t, store, start, "")
	_, err := store.StartRootRun(t.Context(), rootStartCommand(t, start))
	require.NoError(t, err)
	return store, start
}
