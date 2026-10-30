package runstore

// Lifecycle writes store one state change and its matching record in the same
// write transaction. Exact retries return the original record ID without
// writing; a different record or outcome for the same state conflicts.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"time"
	"unicode/utf8"

	genrunoutput "goa.design/goa-ai/registry/gen/runoutput"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
	"goa.design/goa-ai/runtime/agent/storage/lifecycle"
	"goa.design/goa-ai/runtime/agent/transcript"
)

const (
	// transcriptPageMaxRecords bounds the records returned by one transcript
	// or seed page, matching the reference store.
	transcriptPageMaxRecords = 512
	// transcriptPageMaxPayloadBytes bounds the payload bytes returned by one
	// transcript page, inclusive. It does not limit the saved transcript.
	transcriptPageMaxPayloadBytes = 1 << 20
)

// AppendRunRecord stores an ordinary record without changing lifecycle state.
func (s *Store) AppendRunRecord(ctx context.Context, record *runlog.Event) (storage.AppendResult, error) {
	if err := lifecycle.ValidateOrdinaryRunRecord(record); err != nil {
		return storage.AppendResult{}, storage.NewContractError(err)
	}
	var result storage.AppendResult
	err := s.write(ctx, func(t *txn) error {
		var err error
		result, err = t.appendOrdinary(record)
		return err
	})
	return contractResult(result, err)
}

// RecordRunCancellation atomically stores the first reason and its record.
func (s *Store) RecordRunCancellation(ctx context.Context, command storage.RunCancellation) (storage.AppendResult, error) {
	var result storage.AppendResult
	err := s.write(ctx, func(t *txn) error {
		var err error
		result, err = t.recordRunCancellation(command)
		return err
	})
	return contractResult(result, err)
}

// RecordRunSuspension atomically stores one checkpoint, its record, and the
// suspended terminal state.
func (s *Store) RecordRunSuspension(ctx context.Context, command storage.RunSuspension) (storage.AppendResult, error) {
	var result storage.AppendResult
	err := s.write(ctx, func(t *txn) error {
		row, err := t.mutableRun(command.RunID)
		if err != nil {
			return err
		}
		if err := lifecycle.ValidateRunSuspension(command, row.meta); err != nil {
			return err
		}
		result, err = t.recordRunSuspension(command, row)
		return err
	})
	return contractResult(result, err)
}

// RepairRunSuspension stores the supplied checkpoint and record only when the
// run is still active. Any existing terminal state remains unchanged.
func (s *Store) RepairRunSuspension(ctx context.Context, command storage.RunSuspension) (storage.RunRepairResult, error) {
	var result storage.RunRepairResult
	err := s.write(ctx, func(t *txn) error {
		var err error
		result, err = t.repairRunSuspension(command)
		return err
	})
	return contractResult(result, err)
}

// RecordRunTerminal atomically stores one terminal record and terminal state.
func (s *Store) RecordRunTerminal(ctx context.Context, command storage.RunTerminal) (storage.AppendResult, error) {
	var result storage.AppendResult
	err := s.write(ctx, func(t *txn) error {
		row, operationResult, err := t.terminalCommand(command)
		if err != nil {
			return err
		}
		result, err = t.recordRunTerminal(command, row, operationResult)
		return err
	})
	return contractResult(result, err)
}

// RepairRunTerminal stores the supplied terminal record only when the run is
// still active. Any existing terminal state remains unchanged.
func (s *Store) RepairRunTerminal(ctx context.Context, command storage.RunTerminal) (storage.RunRepairResult, error) {
	var result storage.RunRepairResult
	err := s.write(ctx, func(t *txn) error {
		var err error
		result, err = t.repairRunTerminal(command)
		return err
	})
	return contractResult(result, err)
}

// RelinquishRunSuspension stores a cancellation resolution only for the exact
// unresolved root suspension. A competing admitted successor is returned
// intact.
func (s *Store) RelinquishRunSuspension(ctx context.Context, command storage.RunSuspensionRelinquishment) (session.SuspensionResolution, error) {
	if command.RunID == "" || command.SuspensionID == "" || command.Reason == "" ||
		!utf8.ValidString(command.RunID) || !utf8.ValidString(command.SuspensionID) || !utf8.ValidString(command.Reason) {
		return contractResult[session.SuspensionResolution](nil, errors.New("suspension relinquishment requires valid run id, suspension id, and reason"))
	}
	var result session.SuspensionResolution
	err := s.write(ctx, func(t *txn) error {
		var err error
		result, err = t.relinquishRunSuspension(command)
		return err
	})
	return contractResult(result, err)
}

// LoadRun returns one run's metadata.
func (s *Store) LoadRun(ctx context.Context, runID string) (session.RunMeta, error) {
	var result session.RunMeta
	err := s.read(ctx, func(t *txn) error {
		row, found, err := t.loadRun(runID)
		if err != nil {
			return err
		}
		if !found {
			return session.ErrRunNotFound
		}
		result = row.meta
		return nil
	})
	return result, err
}

// LoadRunSuspension returns one run's exact checkpoint.
func (s *Store) LoadRunSuspension(ctx context.Context, runID string) (session.RunSuspension, error) {
	var result session.RunSuspension
	err := s.read(ctx, func(t *txn) error {
		_, found, err := t.loadRun(runID)
		if err != nil {
			return err
		}
		if !found {
			return session.ErrRunNotFound
		}
		value, found, err := t.loadSuspension(runID)
		if err != nil {
			return err
		}
		if !found {
			return session.ErrRunSuspensionNotFound
		}
		result = value
		return nil
	})
	return result, err
}

// ListRunRecords returns one page ordered by the store-assigned run sequence.
// The cursor is the last returned record ID.
func (s *Store) ListRunRecords(ctx context.Context, runID, cursor string, limit int) (runlog.Page, error) {
	if limit <= 0 {
		return runlog.Page{}, errors.New("limit must be greater than zero")
	}
	after, err := parseCursor(cursor)
	if err != nil {
		return runlog.Page{}, err
	}
	var page runlog.Page
	err = s.read(ctx, func(t *txn) error {
		_, found, err := t.loadRun(runID)
		if err != nil {
			return err
		}
		if !found {
			return session.ErrRunNotFound
		}
		records, err := t.queryRecords(`SELECT `+recordColumns+` FROM records WHERE run_id = ? AND seq > ? ORDER BY seq LIMIT ?`,
			runID, after, limit+1)
		page = recordPage(records, after, limit)
		return err
	})
	return page, err
}

// ListSessionRunRecords returns one page in session commit order. The cursor
// is the session position of the last returned record.
func (s *Store) ListSessionRunRecords(ctx context.Context, sessionID, cursor string, limit int) (runlog.Page, error) {
	if limit <= 0 {
		return runlog.Page{}, errors.New("limit must be greater than zero")
	}
	after, err := parseCursor(cursor)
	if err != nil {
		return runlog.Page{}, err
	}
	var page runlog.Page
	err = s.read(ctx, func(t *txn) error {
		purged, err := t.isPurged(sessionID)
		if err != nil {
			return err
		}
		if purged {
			return session.ErrSessionPurged
		}
		_, found, err := t.loadSession(sessionID)
		if err != nil {
			return err
		}
		if !found {
			return session.ErrSessionNotFound
		}
		records, err := t.queryRecords(`SELECT `+recordColumns+` FROM records WHERE session_id = ? AND session_seq > ? ORDER BY session_seq LIMIT ?`,
			sessionID, after, limit+1)
		page = recordPage(records, after, limit)
		return err
	})
	return page, err
}

// ListRunTranscriptRecords returns transcript records through one committed
// position belonging to runID, excluding all later appends. Each page contains
// at most 512 records and an inclusive 1 MiB of payload bytes. A single record
// larger than that byte limit is unsupported.
func (s *Store) ListRunTranscriptRecords(ctx context.Context, runID, throughRecordID, afterRecordID string, limit int) (runlog.Page, error) {
	if err := ctx.Err(); err != nil {
		return runlog.Page{}, err
	}
	if throughRecordID == "" || limit <= 0 {
		return runlog.Page{}, storage.NewContractError(storage.ErrInvalidTranscriptPosition)
	}
	var page runlog.Page
	err := s.read(ctx, func(t *txn) error {
		_, found, err := t.loadRun(runID)
		if err != nil {
			return err
		}
		if !found {
			return session.ErrRunNotFound
		}
		invalid := storage.NewContractError(fmt.Errorf("%w: run %q", storage.ErrInvalidTranscriptPosition, runID))
		end, err := t.committedPosition(runID, throughRecordID)
		if err != nil {
			return err
		}
		if end == 0 {
			return invalid
		}
		var after int64
		if afterRecordID != "" {
			after, err = t.committedPosition(runID, afterRecordID)
			if err != nil {
				return err
			}
			if after == 0 || after > end {
				return invalid
			}
			cursor, err := t.recordBySeq(runID, after)
			if err != nil {
				return err
			}
			if !isTranscriptRecord(cursor) {
				return invalid
			}
		}
		pageLimit := min(limit, transcriptPageMaxRecords)
		records, err := t.queryRecords(`SELECT `+recordColumns+` FROM records WHERE run_id = ? AND seq > ? AND seq <= ? AND type IN (?, ?) ORDER BY seq LIMIT ?`,
			runID, after, end, string(transcript.RunLogMessagesSeeded), string(transcript.RunLogMessagesAppended), pageLimit+1)
		if err != nil {
			return err
		}
		page, err = transcriptPage(records, pageLimit)
		return err
	})
	return page, err
}

// recordPage returns the first limit records read after position after and,
// when the query found one more record, the position of the last returned
// record. Run and session positions are contiguous from 1, so that position
// is after+limit.
func recordPage(records []*runlog.Event, after int64, limit int) runlog.Page {
	if len(records) <= limit {
		return runlog.Page{Events: records}
	}
	return runlog.Page{Events: records[:limit], NextCursor: strconv.FormatInt(after+int64(limit), 10)}
}

// transcriptPage applies the transcript byte budget to records already
// limited to at most limit+1 transcript records in commit order.
func transcriptPage(records []*runlog.Event, limit int) (runlog.Page, error) {
	var page runlog.Page
	payloadBytes := 0
	for _, record := range records {
		if len(page.Events) == limit {
			page.NextCursor = page.Events[len(page.Events)-1].ID
			break
		}
		size := len(record.Payload)
		if size > transcriptPageMaxPayloadBytes {
			return runlog.Page{}, storage.NewContractError(fmt.Errorf(
				"transcript record %q payload size %d exceeds supported page size %d bytes",
				record.ID, size, transcriptPageMaxPayloadBytes,
			))
		}
		if size > transcriptPageMaxPayloadBytes-payloadBytes {
			page.NextCursor = page.Events[len(page.Events)-1].ID
			break
		}
		page.Events = append(page.Events, record)
		payloadBytes += size
	}
	return page, nil
}

// isTranscriptRecord reports whether record carries transcript messages.
func isTranscriptRecord(record *runlog.Event) bool {
	return record.Type == transcript.RunLogMessagesSeeded || record.Type == transcript.RunLogMessagesAppended
}

// committedPosition returns the run sequence of an exact record ID, or zero
// when no record of the run has that ID.
func (t *txn) committedPosition(runID, recordID string) (int64, error) {
	position, err := strconv.ParseInt(recordID, 10, 64)
	if err != nil || position <= 0 || strconv.FormatInt(position, 10) != recordID {
		return 0, nil
	}
	record, err := t.recordBySeq(runID, position)
	if err != nil || record == nil {
		return 0, err
	}
	return position, nil
}

// recordRunCancellation applies the write-once cancellation reason.
func (t *txn) recordRunCancellation(command storage.RunCancellation) (storage.AppendResult, error) {
	row, err := t.mutableRun(command.RunID)
	if err != nil {
		return storage.AppendResult{}, err
	}
	meta := row.meta
	if err := lifecycle.ValidateRunCancellation(command, meta); err != nil {
		return storage.AppendResult{}, err
	}
	if meta.CancellationReason != "" && meta.CancellationReason != command.Reason {
		return storage.AppendResult{}, session.ErrRunCancellationConflict
	}
	if err := t.checkAppend(command.Record); err != nil {
		return storage.AppendResult{}, err
	}
	if row.keys.cancellation == "" && session.IsTerminalRunStatus(meta.Status) {
		return storage.AppendResult{}, session.ErrRunNotActive
	}
	if row.keys.cancellation != "" && row.keys.cancellation != command.Record.EventKey {
		return storage.AppendResult{}, session.ErrRunCancellationConflict
	}
	if meta.CancellationReason == "" {
		row.meta.CancellationReason = command.Reason
		row.meta.UpdatedAt = time.Now().UTC()
		row.keys.cancellation = command.Record.EventKey
		if err := t.updateRun(row); err != nil {
			return storage.AppendResult{}, err
		}
	}
	return t.append(command.Record)
}

// repairRunSuspension chooses the existing terminal state or the supplied
// repair in the same transaction as ordinary workflow writes.
func (t *txn) repairRunSuspension(command storage.RunSuspension) (storage.RunRepairResult, error) {
	row, err := t.mutableRun(command.RunID)
	if err != nil {
		return storage.RunRepairResult{}, err
	}
	if err := lifecycle.ValidateRunSuspension(command, row.meta); err != nil {
		return storage.RunRepairResult{}, err
	}
	if session.IsTerminalRunStatus(row.meta.Status) {
		stored, found, err := t.loadSuspension(command.RunID)
		if err != nil {
			return storage.RunRepairResult{}, err
		}
		record, err := t.recordByKey(command.RunID, command.Record.EventKey)
		if err != nil {
			return storage.RunRepairResult{}, err
		}
		if row.meta.Status == session.RunStatusSuspended &&
			row.keys.suspension == command.Record.EventKey &&
			record != nil && sameEvent(record, command.Record) &&
			found && stored.ID == command.Suspension.ID && bytes.Equal(stored.Data, command.Suspension.Data) {
			existing, err := t.existingRecordResult(record)
			return storage.RunRepairResult{Outcome: storage.RunRepairAlreadyStored, Status: row.meta.Status, Record: existing}, err
		}
		return storage.RunRepairResult{Outcome: storage.RunRepairDifferentTerminal, Status: row.meta.Status}, nil
	}
	if err := t.checkAppend(command.Record); err != nil {
		return storage.RunRepairResult{}, err
	}
	result, err := t.recordRunSuspension(command, row)
	return storage.RunRepairResult{Outcome: storage.RunRepairStored, Status: session.RunStatusSuspended, Record: result}, err
}

// recordRunSuspension applies one exact workflow-owned suspension to a run
// loaded in this transaction.
func (t *txn) recordRunSuspension(command storage.RunSuspension, row runRow) (storage.AppendResult, error) {
	if session.IsTerminalRunStatus(row.meta.Status) && row.meta.Status != session.RunStatusSuspended {
		return storage.AppendResult{}, session.ErrRunTerminalConflict
	}
	stored, found, err := t.loadSuspension(command.RunID)
	if err != nil {
		return storage.AppendResult{}, err
	}
	if found && (stored.ID != command.Suspension.ID || !bytes.Equal(stored.Data, command.Suspension.Data)) {
		return storage.AppendResult{}, session.ErrRunSuspensionConflict
	}
	if err := t.checkAppend(command.Record); err != nil {
		return storage.AppendResult{}, err
	}
	if row.keys.suspension != "" && row.keys.suspension != command.Record.EventKey {
		return storage.AppendResult{}, session.ErrRunSuspensionConflict
	}
	result, err := t.append(command.Record)
	if err != nil || !result.Inserted {
		return result, err
	}
	if err := t.insertSuspension(command.RunID, command.Suspension); err != nil {
		return storage.AppendResult{}, err
	}
	row.meta.Status = session.RunStatusSuspended
	row.meta.UpdatedAt = time.Now().UTC()
	row.keys.suspension = command.Record.EventKey
	row.keys.terminal = command.Record.EventKey
	return result, t.updateRun(row)
}

// terminalCommand loads and validates the run named by a terminal command
// and encodes its selected result.
func (t *txn) terminalCommand(command storage.RunTerminal) (runRow, []byte, error) {
	row, err := t.mutableRun(command.RunID)
	if err != nil {
		return runRow{}, nil, err
	}
	if err := lifecycle.ValidateRunTerminal(command, row.meta); err != nil {
		return runRow{}, nil, err
	}
	if command.OperationResult == nil {
		return row, nil, nil
	}
	operationResult, err := genrunoutput.EncodeOperationResult(command.OperationResult)
	return row, operationResult, err
}

// repairRunTerminal chooses the existing terminal state or the supplied
// repair in the same transaction as ordinary workflow writes.
func (t *txn) repairRunTerminal(command storage.RunTerminal) (storage.RunRepairResult, error) {
	row, operationResult, err := t.terminalCommand(command)
	if err != nil {
		return storage.RunRepairResult{}, err
	}
	if session.IsTerminalRunStatus(row.meta.Status) {
		record, err := t.recordByKey(command.RunID, command.Record.EventKey)
		if err != nil {
			return storage.RunRepairResult{}, err
		}
		if row.meta.Status == command.Status && row.keys.terminal == command.Record.EventKey &&
			record != nil && sameEvent(record, command.Record) &&
			bytes.Equal(row.terminalResult, operationResult) {
			existing, err := t.existingRecordResult(record)
			return storage.RunRepairResult{Outcome: storage.RunRepairAlreadyStored, Status: row.meta.Status, Record: existing}, err
		}
		return storage.RunRepairResult{Outcome: storage.RunRepairDifferentTerminal, Status: row.meta.Status}, nil
	}
	if err := t.checkAppend(command.Record); err != nil {
		return storage.RunRepairResult{}, err
	}
	result, err := t.recordRunTerminal(command, row, operationResult)
	return storage.RunRepairResult{Outcome: storage.RunRepairStored, Status: command.Status, Record: result}, err
}

// recordRunTerminal applies one exact workflow-owned terminal result to a run
// loaded in this transaction.
func (t *txn) recordRunTerminal(command storage.RunTerminal, row runRow, operationResult []byte) (storage.AppendResult, error) {
	if session.IsTerminalRunStatus(row.meta.Status) && row.meta.Status != command.Status {
		return storage.AppendResult{}, session.ErrRunTerminalConflict
	}
	if err := t.checkAppend(command.Record); err != nil {
		return storage.AppendResult{}, err
	}
	if row.keys.terminal != "" && (row.keys.terminal != command.Record.EventKey ||
		!bytes.Equal(row.terminalResult, operationResult)) {
		return storage.AppendResult{}, session.ErrRunTerminalConflict
	}
	result, err := t.append(command.Record)
	if err != nil || !result.Inserted {
		return result, err
	}
	row.meta.Status = command.Status
	row.meta.UpdatedAt = time.Now().UTC()
	row.terminalResult = operationResult
	row.keys.terminal = command.Record.EventKey
	return result, t.updateRun(row)
}

// relinquishRunSuspension validates the exact root and checkpoint before
// choosing a resolution in the transaction shared with starts and purge.
func (t *txn) relinquishRunSuspension(command storage.RunSuspensionRelinquishment) (session.SuspensionResolution, error) {
	row, found, err := t.loadRun(command.RunID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, session.ErrRunNotFound
	}
	if row.meta.Status != session.RunStatusSuspended || row.meta.ParentRunID != "" {
		return nil, session.ErrRunConflict
	}
	saved, found, err := t.loadSuspension(command.RunID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, session.ErrRunSuspensionNotFound
	}
	if saved.ID != command.SuspensionID {
		return nil, session.ErrRunSuspensionConflict
	}
	// An admitted successor owns the remaining work and its cancellation
	// reason. Return it before comparing the predecessor's historical reason.
	if continued, ok := row.meta.SuspensionResolution.(session.ContinuedSuspension); ok {
		return continued, nil
	}
	if row.meta.CancellationReason != "" && row.meta.CancellationReason != command.Reason {
		return nil, session.ErrRunCancellationConflict
	}
	if resolved, ok := row.meta.SuspensionResolution.(session.RelinquishedSuspension); ok {
		if resolved.Reason != command.Reason {
			return nil, session.ErrRunCancellationConflict
		}
		return resolved, nil
	}
	closed := session.RelinquishedSuspension{Reason: command.Reason, ResolvedAt: time.Now().UTC().Truncate(time.Millisecond)}
	row.meta.SuspensionResolution = closed
	row.meta.UpdatedAt = closed.ResolvedAt
	return closed, t.updateRun(row)
}

// append checks a lifecycle record and stores it, or returns the original ID
// of an exact retry.
func (t *txn) append(record *runlog.Event) (storage.AppendResult, error) {
	if err := t.checkAppend(record); err != nil {
		return storage.AppendResult{}, err
	}
	return t.appendChecked(record)
}

// appendOrdinary rejects new work after a run stops while preserving exact
// retries of records that were stored while the run was active.
func (t *txn) appendOrdinary(record *runlog.Event) (storage.AppendResult, error) {
	if err := t.checkAppend(record); err != nil {
		return storage.AppendResult{}, err
	}
	existing, err := t.recordByKey(record.RunID, record.EventKey)
	if err != nil {
		return storage.AppendResult{}, err
	}
	if existing != nil {
		return t.existingRecordResult(existing)
	}
	row, _, err := t.loadRun(record.RunID)
	if err != nil {
		return storage.AppendResult{}, err
	}
	if session.IsTerminalRunStatus(row.meta.Status) {
		return storage.AppendResult{}, session.ErrRunNotActive
	}
	return t.appendChecked(record)
}

// appendChecked assigns the next sequence to a record whose identity, owner,
// and retry key were already checked in this transaction.
func (t *txn) appendChecked(record *runlog.Event) (storage.AppendResult, error) {
	existing, err := t.recordByKey(record.RunID, record.EventKey)
	if err != nil {
		return storage.AppendResult{}, err
	}
	if existing != nil {
		return t.existingRecordResult(existing)
	}
	id, err := t.insertRecord(record)
	if err != nil {
		return storage.AppendResult{}, err
	}
	status, err := t.sessionStatus(record.SessionID)
	return storage.AppendResult{ID: id, Inserted: true, SessionStatus: status}, err
}

// checkAppend validates a record against its stored run, session and any
// record already stored under its key.
func (t *txn) checkAppend(record *runlog.Event) error {
	if err := storage.ValidateRunRecord(record); err != nil {
		return err
	}
	row, found, err := t.loadRun(record.RunID)
	if err != nil {
		return err
	}
	if !found {
		return session.ErrRunNotFound
	}
	if !reflect.DeepEqual(row.meta.Subject, record.Subject) || row.meta.SessionID != record.SessionID {
		return storage.ErrRunRecordOwnerMismatch
	}
	if record.SessionID != "" {
		if err := t.sessionOwnerExists(record.SessionID); err != nil {
			return err
		}
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

// mutableRun loads a run whose session has not been purged.
func (t *txn) mutableRun(runID string) (runRow, error) {
	row, found, err := t.loadRun(runID)
	if err != nil {
		return runRow{}, err
	}
	if !found {
		return runRow{}, session.ErrRunNotFound
	}
	if row.meta.SessionID != "" {
		purged, err := t.isPurged(row.meta.SessionID)
		if err != nil {
			return runRow{}, err
		}
		if purged {
			return runRow{}, session.ErrSessionPurged
		}
	}
	return row, nil
}

// sessionOwnerExists rejects a purged or missing session. Sessionless owners
// always exist.
func (t *txn) sessionOwnerExists(sessionID string) error {
	if sessionID == "" {
		return nil
	}
	purged, err := t.isPurged(sessionID)
	if err != nil {
		return err
	}
	if purged {
		return session.ErrSessionPurged
	}
	_, found, err := t.loadSession(sessionID)
	if err != nil {
		return err
	}
	if !found {
		return session.ErrSessionNotFound
	}
	return nil
}
