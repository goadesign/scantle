package runstore

// Run starts check the published initial history, the continuation
// predecessor, the request binding and the record keys, then write the run,
// the predecessor's resolution and the start records in one write
// transaction. A closed run's exact accepted request selects its original
// records without writing.

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"time"

	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
	"goa.design/goa-ai/runtime/agent/storage/lifecycle"
)

type (
	// sessionRunStartResult is the common result used while a session start
	// chooses the active-session or ended-session records.
	sessionRunStartResult struct {
		// outcome is the immutable decision made by the first start.
		outcome session.RunStartOutcome
		// cancellationReason is the stop reason when outcome is stop.
		cancellationReason string
		// runStatus is the run state observed in the same transaction.
		runStatus session.RunStatus
		// parentRecord identifies the parent's child-link record.
		parentRecord storage.AppendResult
		// started identifies the RunStarted record.
		started storage.AppendResult
		// cancellation identifies the stopped start's intent record.
		cancellation storage.AppendResult
	}
)

// StartRootRun atomically stores the root metadata and start record. When the
// session has ended, it also stores cancellation intent.
func (s *Store) StartRootRun(ctx context.Context, command storage.RootRunStart) (storage.RootRunStartResult, error) {
	if err := lifecycle.ValidateRootRunStart(command); err != nil {
		return contractResult(storage.RootRunStartResult{}, err)
	}
	var result sessionRunStartResult
	err := s.write(ctx, func(t *txn) error {
		var err error
		result, err = t.startSessionRun(command.RequestDigest, command.Run, false, nil, command.Started)
		return err
	})
	return contractResult(storage.RootRunStartResult{
		Outcome:            result.outcome,
		CancellationReason: result.cancellationReason,
		RunStatus:          result.runStatus,
		Started:            result.started,
		Cancellation:       result.cancellation,
	}, err)
}

// StartChildRun atomically stores the child metadata, parent link, and start
// record. When the session has ended, it also stores cancellation intent.
func (s *Store) StartChildRun(ctx context.Context, command storage.ChildRunStart) (storage.ChildRunStartResult, error) {
	if err := lifecycle.ValidateChildRunStart(command); err != nil {
		return contractResult(storage.ChildRunStartResult{}, err)
	}
	var result sessionRunStartResult
	err := s.write(ctx, func(t *txn) error {
		var err error
		result, err = t.startSessionRun(command.RequestDigest, command.Run, true, command.ParentLinked, command.Started)
		return err
	})
	return contractResult(storage.ChildRunStartResult{
		Outcome:            result.outcome,
		CancellationReason: result.cancellationReason,
		RunStatus:          result.runStatus,
		ParentRecord:       result.parentRecord,
		Started:            result.started,
		Cancellation:       result.cancellation,
	}, err)
}

// StartOneShotRun stores a sessionless run and its first record together.
func (s *Store) StartOneShotRun(ctx context.Context, command storage.OneShotRunStart) (storage.OneShotRunStartResult, error) {
	if err := lifecycle.ValidateOneShotRunStart(command); err != nil {
		return contractResult(storage.OneShotRunStartResult{}, err)
	}
	var result storage.OneShotRunStartResult
	err := s.write(ctx, func(t *txn) error {
		var err error
		result, err = t.startSessionlessRun(command.Run, command.Started, engineRunStart, command.RequestDigest)
		return err
	})
	return contractResult(result, err)
}

// StartOneShotChildRun stores a sessionless parent link and child start in one
// transaction.
func (s *Store) StartOneShotChildRun(ctx context.Context, command storage.OneShotChildRunStart) (storage.OneShotChildRunStartResult, error) {
	if err := lifecycle.ValidateOneShotChildRunStart(command); err != nil {
		return contractResult(storage.OneShotChildRunStartResult{}, err)
	}
	var result storage.OneShotChildRunStartResult
	err := s.write(ctx, func(t *txn) error {
		var err error
		result, err = t.startOneShotChildRun(command)
		return err
	})
	return contractResult(result, err)
}

// StartSynchronousRun stores a callback start or returns its exact earlier
// command. Another invocation cannot substitute a new occurrence time.
func (s *Store) StartSynchronousRun(ctx context.Context, command storage.SynchronousRunStart) (storage.OneShotRunStartResult, error) {
	if err := lifecycle.ValidateSynchronousRunStart(command); err != nil {
		return contractResult(storage.OneShotRunStartResult{}, err)
	}
	var result storage.OneShotRunStartResult
	err := s.write(ctx, func(t *txn) error {
		var err error
		result, err = t.startSessionlessRun(command.Run, command.Started, synchronousRunStart, [32]byte{})
		return err
	})
	return contractResult(result, err)
}

// checkStartDigest requires the request bound by the first atomic start. A
// missing binding cannot prove that another execution accepted the same input.
func checkStartDigest(row runRow, digest [32]byte) error {
	if err := checkStartKind(row, engineRunStart); err != nil {
		return err
	}
	if row.keys.requestDigest == ([32]byte{}) {
		return errors.New("stored run has no accepted request digest")
	}
	if row.keys.requestDigest != digest {
		return session.ErrRunConflict
	}
	return nil
}

// checkStartKind rejects another execution owner before any record can be
// selected or written. Invalid metadata never identifies an owner.
func checkStartKind(row runRow, kind runStartKind) error {
	if row.keys.startKind != engineRunStart && row.keys.startKind != synchronousRunStart {
		return errors.New("stored run has no valid start kind")
	}
	if row.keys.startKind != kind {
		return session.ErrRunConflict
	}
	if kind == synchronousRunStart && row.keys.requestDigest != ([32]byte{}) {
		return errors.New("stored synchronous run has an engine request digest")
	}
	return nil
}

// sameStartRecordKeys checks every record key stored by the first durable
// start decision.
func sameStartRecordKeys(keys lifecycleKeys, outcome session.RunStartOutcome, parent, started, cancellation *runlog.Event) bool {
	if parent != nil && keys.parentLink != parent.EventKey {
		return false
	}
	if keys.start != started.EventKey {
		return false
	}
	return outcome != session.RunStartStop || keys.cancellation == cancellation.EventKey
}

// sameStartCandidate compares every immutable event field except the new
// execution's proposed occurrence time. The caller first validates the
// selected original record against the stored run.
func sameStartCandidate(original, proposed *runlog.Event) bool {
	candidate := *proposed
	candidate.Timestamp = original.Timestamp
	return sameEvent(original, &candidate)
}

// sameRunStart compares a stored run's immutable start identity with a
// proposed start.
func sameRunStart(stored session.RunMeta, requested session.RunStart) bool {
	return stored.SeedEndID == requested.SeedEndID &&
		stored.RunID == requested.RunID && reflect.DeepEqual(stored.Subject, requested.Subject) &&
		stored.SessionID == requested.SessionID && stored.ParentRunID == requested.ParentRunID &&
		stored.StartedAt.Equal(requested.StartedAt.UTC()) && maps.Equal(stored.Labels, requested.Labels)
}

// newRunMeta builds the metadata stored by a run's first start.
func newRunMeta(start session.RunStart, origin genregistry.ExecutionInvocation, outcome session.RunStartOutcome, status session.RunStatus) session.RunMeta {
	cancellationReason := ""
	if outcome == session.RunStartStop {
		cancellationReason = run.CancellationReasonSessionEnded
	}
	return session.RunMeta{
		Origin:    origin,
		SeedEndID: start.SeedEndID,
		Subject:   start.Subject, RunID: start.RunID, SessionID: start.SessionID,
		ParentRunID: start.ParentRunID, Status: status, StartOutcome: outcome,
		StartedAt: start.StartedAt.UTC(), UpdatedAt: time.Now().UTC(), Labels: maps.Clone(start.Labels),
		CancellationReason: cancellationReason,
	}
}

// rootExecutionOrigin is the invocation of a run started directly by a host.
func rootExecutionOrigin(runID string) genregistry.ExecutionInvocation {
	return genregistry.ExecutionInvocation{Invocation: genregistry.NewExecutionInvocationKindRoot(&genregistry.RootInvocation{RunID: runID})}
}

// childExecutionOrigin compares the current parent link with the published
// preparation and retains the original issuing Run. Parent continuations may
// change the current parent without changing the call that created the child.
func childExecutionOrigin(parent *runlog.Event, prepared genregistry.ExecutionInvocation) (genregistry.ExecutionInvocation, error) {
	event, err := hooks.DecodeRunlogEvent(parent)
	if err != nil {
		return genregistry.ExecutionInvocation{}, err
	}
	link, ok := event.(*hooks.ChildRunLinkedEvent)
	if !ok {
		return genregistry.ExecutionInvocation{}, errors.New("child origin requires a child-link record")
	}
	original, child := prepared.Invocation.AsTool()
	if !child || original.ToolCallID != link.ToolCallID {
		return genregistry.ExecutionInvocation{}, errors.New("child preparation and parent link name different invocations")
	}
	return prepared, nil
}

// startSessionRun chooses the active or ended-session path in the same
// transaction that EndSession and PurgeSession serialize with.
func (t *txn) startSessionRun(requestDigest [32]byte, start session.RunStart, child bool, parent, started *runlog.Event) (sessionRunStartResult, error) {
	purged, err := t.isPurged(start.SessionID)
	if err != nil {
		return sessionRunStartResult{}, err
	}
	if purged {
		return sessionRunStartResult{}, session.ErrSessionPurged
	}
	if err := t.validateStartSeed(start); err != nil {
		return sessionRunStartResult{}, err
	}
	if err := t.validatePredecessor(start, parent); err != nil {
		return sessionRunStartResult{}, err
	}
	existing, exists, err := t.loadRun(start.RunID)
	if err != nil {
		return sessionRunStartResult{}, err
	}
	current, sessionFound, err := t.loadSession(start.SessionID)
	if err != nil {
		return sessionRunStartResult{}, err
	}
	var cancellation *runlog.Event
	reason := ""
	if exists {
		if existing.meta.StartOutcome == session.RunStartStop {
			reason = existing.meta.CancellationReason
		}
	} else if current.Status == session.StatusEnded {
		reason = run.CancellationReasonSessionEnded
	}
	if reason != "" {
		cancellation, err = lifecycle.StartCancellation(started, reason)
		if err != nil {
			return sessionRunStartResult{}, err
		}
	}
	if exists {
		if start.PredecessorRunID != "" {
			predecessor, _, err := t.loadRun(start.PredecessorRunID)
			if err != nil {
				return sessionRunStartResult{}, err
			}
			if predecessor.meta.SuspensionResolution != (session.ContinuedSuspension{RunID: start.RunID}) {
				return sessionRunStartResult{}, session.ErrRunConflict
			}
		}
		if err := checkStartDigest(existing, requestDigest); err != nil {
			return sessionRunStartResult{}, err
		}
		if session.IsTerminalRunStatus(existing.meta.Status) {
			return t.closedStart(existing, start, parent, started, cancellation)
		}
		if !sameRunStart(existing.meta, start) {
			return sessionRunStartResult{}, session.ErrRunConflict
		}
		if !sameStartRecordKeys(existing.keys, existing.meta.StartOutcome, parent, started, cancellation) {
			return sessionRunStartResult{}, session.ErrRunConflict
		}
		if parent != nil {
			if err := t.checkAppend(parent); err != nil {
				return sessionRunStartResult{}, err
			}
		}
		return t.appendStartRecords(existing.meta, parent, started, cancellation)
	}
	if parent != nil {
		if err := t.checkAppend(parent); err != nil {
			return sessionRunStartResult{}, err
		}
	}
	if !sessionFound {
		return sessionRunStartResult{}, session.ErrSessionNotFound
	}
	if child {
		parentRun, found, err := t.loadRun(start.ParentRunID)
		if err != nil {
			return sessionRunStartResult{}, err
		}
		if !found {
			return sessionRunStartResult{}, session.ErrRunNotFound
		}
		if parentRun.meta.SessionID != start.SessionID {
			return sessionRunStartResult{}, session.ErrRunSessionMismatch
		}
		if parentRun.meta.Status != session.RunStatusRunning {
			return sessionRunStartResult{}, session.ErrRunNotActive
		}
	}
	outcome := session.RunStartProceed
	if current.Status == session.StatusEnded {
		outcome = session.RunStartStop
	}
	if err := t.checkNewRunRecord(started, start); err != nil {
		return sessionRunStartResult{}, err
	}
	if outcome == session.RunStartStop {
		if err := t.checkNewRunRecord(cancellation, start); err != nil {
			return sessionRunStartResult{}, err
		}
		if started.EventKey == cancellation.EventKey {
			return sessionRunStartResult{}, errors.New("started and cancellation records require different event keys")
		}
	}
	origin := rootExecutionOrigin(start.RunID)
	if start.PredecessorRunID != "" {
		predecessor, _, err := t.loadRun(start.PredecessorRunID)
		if err != nil {
			return sessionRunStartResult{}, err
		}
		origin = predecessor.meta.Origin
	} else if parent != nil {
		binding, _, err := t.loadBinding(start.RunID)
		if err != nil {
			return sessionRunStartResult{}, err
		}
		origin, err = childExecutionOrigin(parent, binding.Origin)
		if err != nil {
			return sessionRunStartResult{}, err
		}
	}
	// All ownership and record checks have succeeded. Claim this successor in
	// the same transaction that writes its run and first records.
	if start.PredecessorRunID != "" {
		if err := t.claimSuccessor(start.PredecessorRunID, start.RunID); err != nil {
			return sessionRunStartResult{}, err
		}
	}
	keys := lifecycleKeys{startKind: engineRunStart, requestDigest: requestDigest, start: started.EventKey}
	if parent != nil {
		keys.parentLink = parent.EventKey
	}
	if outcome == session.RunStartStop {
		keys.cancellation = cancellation.EventKey
	}
	meta := newRunMeta(start, origin, outcome, session.RunStatusRunning)
	if err := t.insertRun(runRow{meta: meta, keys: keys}); err != nil {
		return sessionRunStartResult{}, err
	}
	return t.appendStartRecords(meta, parent, started, cancellation)
}

// startSessionlessRun shares the record write for engine and synchronous
// starts. The explicit kind controls identity; a missing digest never selects
// a kind. Only the engine operation can select a closed run at a newly
// proposed time.
func (t *txn) startSessionlessRun(start session.RunStart, started *runlog.Event, kind runStartKind, digest [32]byte) (storage.OneShotRunStartResult, error) {
	if err := t.validateStartSeed(start); err != nil {
		return storage.OneShotRunStartResult{}, err
	}
	if err := t.validatePredecessor(start, nil); err != nil {
		return storage.OneShotRunStartResult{}, err
	}
	existing, exists, err := t.loadRun(start.RunID)
	if err != nil {
		return storage.OneShotRunStartResult{}, err
	}
	if exists {
		if start.PredecessorRunID != "" {
			predecessor, _, err := t.loadRun(start.PredecessorRunID)
			if err != nil {
				return storage.OneShotRunStartResult{}, err
			}
			resolution, continued := predecessor.meta.SuspensionResolution.(session.ContinuedSuspension)
			if !continued || resolution.RunID != start.RunID {
				return storage.OneShotRunStartResult{}, session.ErrRunConflict
			}
		}
		if kind == engineRunStart {
			if err := checkStartDigest(existing, digest); err != nil {
				return storage.OneShotRunStartResult{}, err
			}
			if session.IsTerminalRunStatus(existing.meta.Status) {
				result, err := t.closedStart(existing, start, nil, started, nil)
				return storage.OneShotRunStartResult{Outcome: session.RunStartProceed, RunStatus: result.runStatus, Record: result.started}, err
			}
		} else if err := checkStartKind(existing, kind); err != nil {
			return storage.OneShotRunStartResult{}, err
		}
		if !sameRunStart(existing.meta, start) || existing.meta.StartOutcome != session.RunStartProceed {
			return storage.OneShotRunStartResult{}, session.ErrRunConflict
		}
		if existing.keys.start != started.EventKey {
			return storage.OneShotRunStartResult{}, session.ErrRunConflict
		}
		result, err := t.append(started)
		return storage.OneShotRunStartResult{Outcome: session.RunStartProceed, RunStatus: existing.meta.Status, Record: result}, err
	}
	if err := t.checkNewRunRecord(started, start); err != nil {
		return storage.OneShotRunStartResult{}, err
	}
	origin := rootExecutionOrigin(start.RunID)
	if start.PredecessorRunID != "" {
		predecessor, _, err := t.loadRun(start.PredecessorRunID)
		if err != nil {
			return storage.OneShotRunStartResult{}, err
		}
		origin = predecessor.meta.Origin
		if err := t.claimSuccessor(start.PredecessorRunID, start.RunID); err != nil {
			return storage.OneShotRunStartResult{}, err
		}
	}
	meta := newRunMeta(start, origin, session.RunStartProceed, session.RunStatusRunning)
	keys := lifecycleKeys{startKind: kind, requestDigest: digest, start: started.EventKey}
	if err := t.insertRun(runRow{meta: meta, keys: keys}); err != nil {
		return storage.OneShotRunStartResult{}, err
	}
	result, err := t.append(started)
	return storage.OneShotRunStartResult{Outcome: session.RunStartProceed, RunStatus: session.RunStatusRunning, Record: result}, err
}

// startOneShotChildRun applies the sessionless child start in the transaction
// that also writes the parent's link record.
func (t *txn) startOneShotChildRun(command storage.OneShotChildRunStart) (storage.OneShotChildRunStartResult, error) {
	if err := t.validateStartSeed(command.Run); err != nil {
		return storage.OneShotChildRunStartResult{}, err
	}
	if err := t.validatePredecessor(command.Run, command.ParentLinked); err != nil {
		return storage.OneShotChildRunStartResult{}, err
	}
	existing, exists, err := t.loadRun(command.Run.RunID)
	if err != nil {
		return storage.OneShotChildRunStartResult{}, err
	}
	if exists {
		if command.Run.PredecessorRunID != "" {
			predecessor, _, err := t.loadRun(command.Run.PredecessorRunID)
			if err != nil {
				return storage.OneShotChildRunStartResult{}, err
			}
			resolution, continued := predecessor.meta.SuspensionResolution.(session.ContinuedSuspension)
			if !continued || resolution.RunID != command.Run.RunID {
				return storage.OneShotChildRunStartResult{}, session.ErrRunConflict
			}
		}
		if err := checkStartDigest(existing, command.RequestDigest); err != nil {
			return storage.OneShotChildRunStartResult{}, err
		}
		if session.IsTerminalRunStatus(existing.meta.Status) {
			result, err := t.closedStart(existing, command.Run, command.ParentLinked, command.Started, nil)
			return storage.OneShotChildRunStartResult{Outcome: session.RunStartProceed, RunStatus: result.runStatus, ParentRecord: result.parentRecord, Started: result.started}, err
		}
		if !sameRunStart(existing.meta, command.Run) || existing.meta.StartOutcome != session.RunStartProceed ||
			existing.keys.parentLink != command.ParentLinked.EventKey || existing.keys.start != command.Started.EventKey {
			return storage.OneShotChildRunStartResult{}, session.ErrRunConflict
		}
		parentRecord, err := t.append(command.ParentLinked)
		if err != nil {
			return storage.OneShotChildRunStartResult{}, err
		}
		started, err := t.append(command.Started)
		return storage.OneShotChildRunStartResult{
			Outcome:   session.RunStartProceed,
			RunStatus: existing.meta.Status, ParentRecord: parentRecord, Started: started,
		}, err
	}
	parent, found, err := t.loadRun(command.Run.ParentRunID)
	if err != nil {
		return storage.OneShotChildRunStartResult{}, err
	}
	if !found {
		return storage.OneShotChildRunStartResult{}, session.ErrRunNotFound
	}
	if parent.meta.SessionID != "" {
		return storage.OneShotChildRunStartResult{}, session.ErrRunSessionMismatch
	}
	if parent.meta.Status != session.RunStatusRunning {
		return storage.OneShotChildRunStartResult{}, session.ErrRunNotActive
	}
	if !reflect.DeepEqual(command.ParentLinked.Subject, parent.meta.Subject) {
		return storage.OneShotChildRunStartResult{}, storage.ErrRunRecordOwnerMismatch
	}
	if err := t.checkAppend(command.ParentLinked); err != nil {
		return storage.OneShotChildRunStartResult{}, err
	}
	if err := t.checkNewRunRecord(command.Started, command.Run); err != nil {
		return storage.OneShotChildRunStartResult{}, err
	}
	binding, _, err := t.loadBinding(command.Run.RunID)
	if err != nil {
		return storage.OneShotChildRunStartResult{}, err
	}
	origin, err := childExecutionOrigin(command.ParentLinked, binding.Origin)
	if err != nil {
		return storage.OneShotChildRunStartResult{}, err
	}
	// The predecessor and both records have passed validation. Claim its one
	// successor in the transaction that also stores the new child and link.
	if command.Run.PredecessorRunID != "" {
		predecessor, _, err := t.loadRun(command.Run.PredecessorRunID)
		if err != nil {
			return storage.OneShotChildRunStartResult{}, err
		}
		origin = predecessor.meta.Origin
		if err := t.claimSuccessor(command.Run.PredecessorRunID, command.Run.RunID); err != nil {
			return storage.OneShotChildRunStartResult{}, err
		}
	}
	keys := lifecycleKeys{
		startKind:     engineRunStart,
		requestDigest: command.RequestDigest,
		parentLink:    command.ParentLinked.EventKey,
		start:         command.Started.EventKey,
	}
	if err := t.insertRun(runRow{meta: newRunMeta(command.Run, origin, session.RunStartProceed, session.RunStatusRunning), keys: keys}); err != nil {
		return storage.OneShotChildRunStartResult{}, err
	}
	parentRecord, err := t.append(command.ParentLinked)
	if err != nil {
		return storage.OneShotChildRunStartResult{}, err
	}
	started, err := t.append(command.Started)
	return storage.OneShotChildRunStartResult{
		Outcome:   session.RunStartProceed,
		RunStatus: session.RunStatusRunning, ParentRecord: parentRecord, Started: started,
	}, err
}

// validatePredecessor proves that a continuation restores a suspended run
// with the same owner and logical call before the successor is stored.
func (t *txn) validatePredecessor(start session.RunStart, parent *runlog.Event) error {
	if start.PredecessorRunID == "" {
		return nil
	}
	stored, found, err := t.loadRun(start.PredecessorRunID)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("continuation predecessor %q: %w", start.PredecessorRunID, session.ErrRunNotFound)
	}
	predecessor := stored.meta
	if predecessor.Status != session.RunStatusSuspended {
		return fmt.Errorf(
			"continuation predecessor %q has status %q, want %q",
			start.PredecessorRunID,
			predecessor.Status,
			session.RunStatusSuspended,
		)
	}
	if predecessor.SessionID != start.SessionID {
		return fmt.Errorf(
			"continuation predecessor %q session id %q does not match successor %q",
			start.PredecessorRunID,
			predecessor.SessionID,
			start.SessionID,
		)
	}
	if !reflect.DeepEqual(predecessor.Subject, start.Subject) {
		return fmt.Errorf(
			"continuation predecessor %q subject %v does not match successor %v",
			start.PredecessorRunID,
			predecessor.Subject,
			start.Subject,
		)
	}
	if predecessor.ParentRunID == "" || start.ParentRunID == "" {
		if predecessor.ParentRunID != start.ParentRunID {
			return fmt.Errorf(
				"continuation predecessor %q parent run id %q does not match successor %q",
				start.PredecessorRunID, predecessor.ParentRunID, start.ParentRunID,
			)
		}
		return nil
	}
	previousParent, found, err := t.loadRun(predecessor.ParentRunID)
	if err != nil {
		return err
	}
	if !found {
		return session.ErrRunNotFound
	}
	previousLink, err := t.selectedStartRecord(predecessor.ParentRunID, stored.keys.parentLink)
	if err != nil {
		return err
	}
	if err := lifecycle.ValidateStoredChildLink(previousLink, previousParent.meta, predecessor); err != nil {
		return fmt.Errorf("continuation predecessor child link: %w", err)
	}
	return lifecycle.ValidateChildContinuationLinks(previousLink, parent)
}

// appendStartRecords appends the exact record set selected by the run's
// start outcome and reports the run state from meta.
func (t *txn) appendStartRecords(meta session.RunMeta, parent, started, cancellation *runlog.Event) (sessionRunStartResult, error) {
	result := sessionRunStartResult{outcome: meta.StartOutcome, runStatus: meta.Status}
	if meta.StartOutcome == session.RunStartStop {
		result.cancellationReason = meta.CancellationReason
	}
	if parent != nil {
		stored, err := t.append(parent)
		if err != nil {
			return sessionRunStartResult{}, err
		}
		result.parentRecord = stored
	}
	stored, err := t.append(started)
	if err != nil {
		return sessionRunStartResult{}, err
	}
	result.started = stored
	if meta.StartOutcome == session.RunStartStop {
		stored, err = t.append(cancellation)
		if err != nil {
			return sessionRunStartResult{}, err
		}
		result.cancellation = stored
	}
	return result, nil
}

// checkNewRunRecord validates a start or cancellation-intent record's owner
// and rejects a different record already stored under its key.
func (t *txn) checkNewRunRecord(record *runlog.Event, start session.RunStart) error {
	if err := storage.ValidateRunRecord(record); err != nil {
		return err
	}
	if record.RunID != start.RunID {
		return errors.New("start record does not match run")
	}
	if !reflect.DeepEqual(record.Subject, start.Subject) || record.SessionID != start.SessionID {
		return storage.ErrRunRecordOwnerMismatch
	}
	existing, err := t.recordByKey(record.RunID, record.EventKey)
	if err != nil {
		return err
	}
	if existing != nil && !sameEvent(existing, record) {
		return &runlog.EventConflictError{RunID: record.RunID, EventKey: record.EventKey}
	}
	return nil
}

// closedStart validates the original start and closing facts before
// returning their committed IDs. The caller has validated the complete
// proposed command and its request digest in this transaction.
func (t *txn) closedStart(row runRow, proposed session.RunStart, parent, started, cancellation *runlog.Event) (sessionRunStartResult, error) {
	meta, keys := row.meta, row.keys
	if meta.StartOutcome != session.RunStartProceed && meta.StartOutcome != session.RunStartStop {
		return sessionRunStartResult{}, errors.New("stored run has invalid start outcome")
	}
	if meta.SessionID != "" {
		_, found, err := t.loadSession(meta.SessionID)
		if err != nil {
			return sessionRunStartResult{}, err
		}
		if !found {
			return sessionRunStartResult{}, session.ErrSessionNotFound
		}
	}
	original, err := t.selectedStartRecord(meta.RunID, keys.start)
	if err != nil {
		return sessionRunStartResult{}, err
	}
	if err := lifecycle.ValidateStoredRunStart(original, meta); err != nil {
		return sessionRunStartResult{}, fmt.Errorf("stored run start: %w", err)
	}
	// The stored event must prove the original time. Only after that proof can
	// a new execution select it without using its newly proposed time.
	proposed.StartedAt = original.Timestamp
	if !sameRunStart(meta, proposed) || !sameStartCandidate(original, started) {
		return sessionRunStartResult{}, session.ErrRunConflict
	}
	startedResult, err := t.existingRecordResult(original)
	if err != nil {
		return sessionRunStartResult{}, err
	}
	result := sessionRunStartResult{outcome: meta.StartOutcome, runStatus: meta.Status, started: startedResult}
	if parent != nil {
		parentRun, found, err := t.loadRun(meta.ParentRunID)
		if err != nil {
			return sessionRunStartResult{}, err
		}
		if !found {
			return sessionRunStartResult{}, session.ErrRunNotFound
		}
		link, err := t.selectedStartRecord(meta.ParentRunID, keys.parentLink)
		if err != nil {
			return sessionRunStartResult{}, err
		}
		if err := lifecycle.ValidateStoredChildLink(link, parentRun.meta, meta); err != nil {
			return sessionRunStartResult{}, fmt.Errorf("stored child link: %w", err)
		}
		if !sameStartCandidate(link, parent) {
			return sessionRunStartResult{}, session.ErrRunConflict
		}
		if result.parentRecord, err = t.existingRecordResult(link); err != nil {
			return sessionRunStartResult{}, err
		}
	} else if keys.parentLink != "" {
		return sessionRunStartResult{}, session.ErrRunConflict
	}
	closing, err := t.selectedStartRecord(meta.RunID, keys.terminal)
	if err != nil {
		return sessionRunStartResult{}, err
	}
	if meta.Status == session.RunStatusSuspended {
		checkpoint, found, err := t.loadSuspension(meta.RunID)
		if err != nil {
			return sessionRunStartResult{}, err
		}
		if !found || keys.suspension != keys.terminal {
			return sessionRunStartResult{}, session.ErrRunSuspensionNotFound
		}
		err = lifecycle.ValidateRunSuspension(storage.RunSuspension{
			RunID: meta.RunID, Suspension: checkpoint, Record: closing,
		}, meta)
		if err != nil {
			return sessionRunStartResult{}, fmt.Errorf("stored run closing record: %w", err)
		}
	} else if err := lifecycle.ValidateRunTerminal(storage.RunTerminal{
		RunID: meta.RunID, Status: meta.Status, Record: closing,
	}, meta); err != nil {
		return sessionRunStartResult{}, fmt.Errorf("stored run closing record: %w", err)
	}
	if meta.StartOutcome == session.RunStartStop {
		intent, err := t.selectedStartRecord(meta.RunID, keys.cancellation)
		if err != nil {
			return sessionRunStartResult{}, err
		}
		if cancellation == nil || (meta.Status != session.RunStatusCanceled && meta.Status != session.RunStatusFailed) {
			return sessionRunStartResult{}, session.ErrRunConflict
		}
		if err := lifecycle.ValidateRunCancellation(storage.RunCancellation{
			RunID: meta.RunID, Reason: meta.CancellationReason, Record: intent,
		}, meta); err != nil {
			return sessionRunStartResult{}, fmt.Errorf("stored stopped-start intent: %w", err)
		}
		if !intent.Timestamp.Equal(meta.StartedAt) || !sameStartCandidate(intent, cancellation) {
			return sessionRunStartResult{}, session.ErrRunConflict
		}
		if result.cancellation, err = t.existingRecordResult(intent); err != nil {
			return sessionRunStartResult{}, err
		}
		result.cancellationReason = meta.CancellationReason
	}
	return result, nil
}

// selectedStartRecord requires an existing keyed record and its committed ID.
// Missing records are corruption, never permission to recreate history.
func (t *txn) selectedStartRecord(runID, key string) (*runlog.Event, error) {
	if key == "" {
		return nil, errors.New("stored run is missing a required lifecycle record")
	}
	record, err := t.recordByKey(runID, key)
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, errors.New("stored run is missing a required lifecycle record")
	}
	return record, nil
}
