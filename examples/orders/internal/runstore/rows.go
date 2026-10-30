package runstore

// Row helpers translate between SQLite rows and the goa-ai value types. Every
// helper runs inside the caller's transaction and marks database errors as
// dbError; decoding failures of stored values are returned as plain errors.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"time"

	genregistry "goa.design/goa-ai/registry/gen/registry"
	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
)

type (
	// runRow is one stored run: its public metadata, the lifecycle record keys
	// that made each state visible, and the selected terminal result bytes.
	runRow struct {
		// meta is the RunMeta returned by LoadRun.
		meta session.RunMeta
		// keys names the records and request binding chosen by lifecycle writes.
		keys lifecycleKeys
		// terminalResult is the encoded selected OperationResult, or nil when
		// the terminal write selected no result or the run is not terminal.
		terminalResult []byte
	}

	// lifecycleKeys remembers which immutable record made each run state
	// visible. Exact retries must use these same keys.
	lifecycleKeys struct {
		// startKind records whether the engine or a synchronous callback owns
		// execution for the whole Run lifetime.
		startKind runStartKind
		// requestDigest binds an engine start to its accepted request. It is
		// all zero for synchronous starts.
		requestDigest [32]byte
		// start is the RunStarted record key.
		start string
		// parentLink is the parent's ChildRunLinked record key for child runs.
		parentLink string
		// cancellation is the cancellation-intent record key, when stored.
		cancellation string
		// suspension is the suspended record key, when stored.
		suspension string
		// terminal is the key of the record that closed the run.
		terminal string
	}

	// runStartKind records who owns execution for the whole Run lifetime.
	// Zero is invalid; missing metadata never implies a synchronous start.
	runStartKind int64
)

const (
	// engineRunStart marks a Run started by one of the four engine commands.
	engineRunStart runStartKind = iota + 1
	// synchronousRunStart marks a Run started by StartSynchronousRun.
	synchronousRunStart
)

const (
	// runColumns lists the runs columns read by scanRun, in scan order.
	runColumns = `run_id, session_id, parent_run_id, subject, origin, seed_end_id, status,
		start_outcome, started_at, updated_at, labels, cancellation_reason, resolution_kind,
		resolution_run_id, resolution_reason, resolution_at, start_kind, request_digest,
		start_key, parent_link_key, cancellation_key, suspension_key, terminal_key, terminal_result`
	// recordColumns lists the records columns read by scanRecord, in scan order.
	recordColumns = `seq, event_key, run_id, subject, session_id, turn_id, type, payload, timestamp`
	// resolutionContinued is the resolution_kind of a ContinuedSuspension.
	resolutionContinued = "continued"
	// resolutionRelinquished is the resolution_kind of a RelinquishedSuspension.
	resolutionRelinquished = "relinquished"
)

// scanRun decodes the columns listed by runColumns.
func scanRun(rows *sql.Rows) (runRow, error) {
	var (
		row                                   runRow
		subject, origin, labels, digest       []byte
		status, outcome, resolutionKind       string
		resolutionRunID, resolutionReason     string
		startedAt, updatedAt, resolvedAt      binaryTime
		startKind                             runStartKind
		terminalResult                        []byte
		startKey, linkKey, cancelKey, suspKey string
		terminalKey                           string
	)
	if err := rows.Scan(&row.meta.RunID, &row.meta.SessionID, &row.meta.ParentRunID, &subject, &origin,
		&row.meta.SeedEndID, &status, &outcome, &startedAt, &updatedAt, &labels, &row.meta.CancellationReason,
		&resolutionKind, &resolutionRunID, &resolutionReason, &resolvedAt, &startKind, &digest,
		&startKey, &linkKey, &cancelKey, &suspKey, &terminalKey, &terminalResult); err != nil {
		return runRow{}, &dbError{err: fmt.Errorf("runstore: scan run: %w", err)}
	}
	decodedSubject, err := genrunidentity.DecodeRunSubject(subject)
	if err != nil {
		return runRow{}, fmt.Errorf("stored run %q subject: %w", row.meta.RunID, err)
	}
	decodedOrigin, err := genregistry.DecodeExecutionInvocation(origin)
	if err != nil {
		return runRow{}, fmt.Errorf("stored run %q origin: %w", row.meta.RunID, err)
	}
	if labels != nil {
		if err := json.Unmarshal(labels, &row.meta.Labels); err != nil {
			return runRow{}, fmt.Errorf("stored run %q labels: %w", row.meta.RunID, err)
		}
	}
	if len(digest) != len(row.keys.requestDigest) {
		return runRow{}, fmt.Errorf("stored run %q has a %d-byte request digest", row.meta.RunID, len(digest))
	}
	row.meta.Subject = *decodedSubject
	row.meta.Origin = *decodedOrigin
	row.meta.Status = session.RunStatus(status)
	row.meta.StartOutcome = session.RunStartOutcome(outcome)
	row.meta.StartedAt = time.Time(startedAt)
	row.meta.UpdatedAt = time.Time(updatedAt)
	switch resolutionKind {
	case resolutionContinued:
		row.meta.SuspensionResolution = session.ContinuedSuspension{RunID: resolutionRunID}
	case resolutionRelinquished:
		row.meta.SuspensionResolution = session.RelinquishedSuspension{Reason: resolutionReason, ResolvedAt: time.Time(resolvedAt)}
	}
	row.keys = lifecycleKeys{
		startKind: startKind, start: startKey, parentLink: linkKey,
		cancellation: cancelKey, suspension: suspKey, terminal: terminalKey,
	}
	copy(row.keys.requestDigest[:], digest)
	row.terminalResult = terminalResult
	return row, nil
}

// scanRecord decodes the columns listed by recordColumns.
func scanRecord(rows *sql.Rows) (*runlog.Event, error) {
	var (
		record    runlog.Event
		seq       int64
		subject   []byte
		typ       string
		payload   []byte
		timestamp binaryTime
	)
	if err := rows.Scan(&seq, &record.EventKey, &record.RunID, &subject, &record.SessionID,
		&record.TurnID, &typ, &payload, &timestamp); err != nil {
		return nil, &dbError{err: fmt.Errorf("runstore: scan record: %w", err)}
	}
	decoded, err := genrunidentity.DecodeRunSubject(subject)
	if err != nil {
		return nil, fmt.Errorf("stored record %q subject: %w", record.EventKey, err)
	}
	record.ID = strconv.FormatInt(seq, 10)
	record.Subject = *decoded
	record.Type = runlog.Type(typ)
	record.Payload = payload
	record.Timestamp = time.Time(timestamp)
	return &record, nil
}

// encodeRunIdentity encodes the run's subject, original invocation and labels.
// The generated validators reject invalid UTF-8 before a run is stored, so the
// JSON codecs keep every accepted value exactly. Nil labels are stored as
// NULL so a read returns nil, not an empty map.
func encodeRunIdentity(meta session.RunMeta) (subject, origin, labels []byte, err error) {
	subject, err = genrunidentity.EncodeRunSubject(&meta.Subject)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("encode run subject: %w", err)
	}
	origin, err = genregistry.EncodeExecutionInvocation(&meta.Origin)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("encode run origin: %w", err)
	}
	if meta.Labels != nil {
		labels, err = json.Marshal(meta.Labels)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("encode run labels: %w", err)
		}
	}
	return subject, origin, labels, nil
}

// encodeResolution flattens a suspension resolution into its columns.
func encodeResolution(resolution session.SuspensionResolution) (kind, runID, reason string, at binaryTime) {
	switch value := resolution.(type) {
	case session.ContinuedSuspension:
		return resolutionContinued, value.RunID, "", binaryTime{}
	case session.RelinquishedSuspension:
		return resolutionRelinquished, "", value.Reason, binaryTime(value.ResolvedAt)
	default:
		return "", "", "", binaryTime{}
	}
}

// nullableBytes stores nil as NULL so absent values read back as nil.
func nullableBytes(value []byte) any {
	if value == nil {
		return nil
	}
	return value
}

// parseCursor converts a run or session record cursor to the last returned
// position. Empty starts at the beginning.
func parseCursor(cursor string) (int64, error) {
	if cursor == "" {
		return 0, nil
	}
	value, err := strconv.ParseInt(cursor, 10, 64)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("invalid cursor %q", cursor)
	}
	return value, nil
}

// sameEvent reports whether two records have the same immutable identity,
// payload and timestamp.
func sameEvent(left, right *runlog.Event) bool {
	return left.EventKey == right.EventKey && left.RunID == right.RunID &&
		reflect.DeepEqual(left.Subject, right.Subject) && left.SessionID == right.SessionID &&
		left.TurnID == right.TurnID && left.Type == right.Type &&
		left.Timestamp.Equal(right.Timestamp) && string(left.Payload) == string(right.Payload)
}

// exec runs one statement and marks driver errors as database failures.
func (t *txn) exec(query string, args ...any) (sql.Result, error) {
	result, err := t.tx.ExecContext(t.ctx, query, args...)
	if err != nil {
		return nil, &dbError{err: fmt.Errorf("runstore: exec: %w", err)}
	}
	return result, nil
}

// scanOne runs a single-row query. It reports false when no row matches.
func (t *txn) scanOne(query string, args []any, dest ...any) (bool, error) {
	err := t.tx.QueryRowContext(t.ctx, query, args...).Scan(dest...)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, &dbError{err: fmt.Errorf("runstore: query: %w", err)}
	}
	return true, nil
}

// isPurged reports whether sessionID was permanently removed.
func (t *txn) isPurged(sessionID string) (bool, error) {
	var id string
	return t.scanOne(`SELECT session_id FROM purged_sessions WHERE session_id = ?`, []any{sessionID}, &id)
}

// loadSession returns one stored session.
func (t *txn) loadSession(sessionID string) (session.Session, bool, error) {
	var (
		status    string
		createdAt binaryTime
		endedAt   []byte
	)
	found, err := t.scanOne(`SELECT status, created_at, ended_at FROM sessions WHERE session_id = ?`,
		[]any{sessionID}, &status, &createdAt, &endedAt)
	if err != nil || !found {
		return session.Session{}, found, err
	}
	result := session.Session{ID: sessionID, Status: session.SessionStatus(status), CreatedAt: time.Time(createdAt)}
	if endedAt != nil {
		var at binaryTime
		if err := at.Scan(endedAt); err != nil {
			return session.Session{}, false, fmt.Errorf("stored session %q end time: %w", sessionID, err)
		}
		ended := time.Time(at)
		result.EndedAt = &ended
	}
	return result, true, nil
}

// insertSession stores a new active session.
func (t *txn) insertSession(value session.Session) error {
	_, err := t.exec(`INSERT INTO sessions (session_id, status, created_at) VALUES (?, ?, ?)`,
		value.ID, string(value.Status), binaryTime(value.CreatedAt))
	return err
}

// endSession marks an active session ended at the given time.
func (t *txn) endSession(sessionID string, at time.Time) error {
	_, err := t.exec(`UPDATE sessions SET status = ?, ended_at = ? WHERE session_id = ?`,
		string(session.StatusEnded), binaryTime(at), sessionID)
	return err
}

// sessionStatus returns the owning session's status, or empty for sessionless
// records and sessions that no longer exist.
func (t *txn) sessionStatus(sessionID string) (session.SessionStatus, error) {
	if sessionID == "" {
		return "", nil
	}
	stored, _, err := t.loadSession(sessionID)
	return stored.Status, err
}

// sessionHasUnresolvedRuns reports whether the session owns a running run or
// a suspension without a permanent resolution.
func (t *txn) sessionHasUnresolvedRuns(sessionID string) (bool, error) {
	var id string
	return t.scanOne(`SELECT run_id FROM runs WHERE session_id = ? AND
		(status = ? OR (status = ? AND resolution_kind = '')) LIMIT 1`,
		[]any{sessionID, string(session.RunStatusRunning), string(session.RunStatusSuspended)}, &id)
}

// purgeSession seals the session ID and deletes everything it owns.
func (t *txn) purgeSession(sessionID string) error {
	statements := []string{
		`INSERT INTO purged_sessions (session_id) VALUES (?)`,
		`DELETE FROM sessions WHERE session_id = ?`,
		`DELETE FROM records WHERE run_id IN (SELECT run_id FROM runs WHERE session_id = ?)`,
		`DELETE FROM suspensions WHERE run_id IN (SELECT run_id FROM runs WHERE session_id = ?)`,
		`DELETE FROM runs WHERE session_id = ?`,
		`DELETE FROM preparation_attempts WHERE session_id = ?`,
		`DELETE FROM preparation_operations WHERE session_id = ?`,
		`DELETE FROM unstarted_terminals WHERE session_id = ?`,
	}
	for _, statement := range statements {
		if _, err := t.exec(statement, sessionID); err != nil {
			return err
		}
	}
	return nil
}

// loadRun returns one stored run.
func (t *txn) loadRun(runID string) (runRow, bool, error) {
	rows, err := t.queryRuns(`SELECT `+runColumns+` FROM runs WHERE run_id = ?`, runID)
	if err != nil || len(rows) == 0 {
		return runRow{}, false, err
	}
	return rows[0], true, nil
}

// sessionRuns returns the session's runs in creation order.
func (t *txn) sessionRuns(sessionID string) ([]runRow, error) {
	return t.queryRuns(`SELECT `+runColumns+` FROM runs WHERE session_id = ? ORDER BY rowid`, sessionID)
}

// queryRuns decodes every run selected by query.
func (t *txn) queryRuns(query string, args ...any) ([]runRow, error) {
	rows, err := t.tx.QueryContext(t.ctx, query, args...)
	if err != nil {
		return nil, &dbError{err: fmt.Errorf("runstore: query runs: %w", err)}
	}
	defer rows.Close() //nolint:errcheck // rows.Err reports iteration failures.
	var result []runRow
	for rows.Next() {
		row, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, &dbError{err: fmt.Errorf("runstore: read runs: %w", err)}
	}
	return result, nil
}

// insertRun stores a new run. The primary key rejects a second run with the
// same ID inside the write transaction.
func (t *txn) insertRun(row runRow) error {
	subject, origin, labels, err := encodeRunIdentity(row.meta)
	if err != nil {
		return err
	}
	kind, runID, reason, at := encodeResolution(row.meta.SuspensionResolution)
	_, err = t.exec(`INSERT INTO runs (`+runColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.meta.RunID, row.meta.SessionID, row.meta.ParentRunID, subject, origin, row.meta.SeedEndID,
		string(row.meta.Status), string(row.meta.StartOutcome), binaryTime(row.meta.StartedAt),
		binaryTime(row.meta.UpdatedAt), labels, row.meta.CancellationReason, kind, runID, reason, at,
		int64(row.keys.startKind), row.keys.requestDigest[:], row.keys.start, row.keys.parentLink,
		row.keys.cancellation, row.keys.suspension, row.keys.terminal, nullableBytes(row.terminalResult))
	return err
}

// updateRun stores the mutable lifecycle state of an existing run: status,
// update time, cancellation reason, suspension resolution, record keys and the
// selected terminal result. Identity columns never change.
func (t *txn) updateRun(row runRow) error {
	kind, runID, reason, at := encodeResolution(row.meta.SuspensionResolution)
	_, err := t.exec(`UPDATE runs SET status = ?, updated_at = ?, cancellation_reason = ?,
		resolution_kind = ?, resolution_run_id = ?, resolution_reason = ?, resolution_at = ?,
		cancellation_key = ?, suspension_key = ?, terminal_key = ?, terminal_result = ?
		WHERE run_id = ?`,
		string(row.meta.Status), binaryTime(row.meta.UpdatedAt), row.meta.CancellationReason,
		kind, runID, reason, at, row.keys.cancellation, row.keys.suspension, row.keys.terminal,
		nullableBytes(row.terminalResult), row.meta.RunID)
	return err
}

// claimSuccessor resolves an unresolved suspension in favor of successorID.
// The conditional update makes the first successor permanent: it reports
// session.ErrRunConflict when another resolution already exists.
func (t *txn) claimSuccessor(predecessorID, successorID string) error {
	result, err := t.exec(`UPDATE runs SET resolution_kind = ?, resolution_run_id = ?, updated_at = ?
		WHERE run_id = ? AND resolution_kind = ''`,
		resolutionContinued, successorID, binaryTime(time.Now().UTC()), predecessorID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return &dbError{err: fmt.Errorf("runstore: claim successor: %w", err)}
	}
	if changed != 1 {
		return session.ErrRunConflict
	}
	return nil
}

// loadSuspension returns one run's stored checkpoint.
func (t *txn) loadSuspension(runID string) (session.RunSuspension, bool, error) {
	var value session.RunSuspension
	found, err := t.scanOne(`SELECT suspension_id, data FROM suspensions WHERE run_id = ?`,
		[]any{runID}, &value.ID, &value.Data)
	return value, found, err
}

// insertSuspension stores a run's first checkpoint.
func (t *txn) insertSuspension(runID string, value session.RunSuspension) error {
	_, err := t.exec(`INSERT INTO suspensions (run_id, suspension_id, data) VALUES (?, ?, ?)`,
		runID, value.ID, value.Data)
	return err
}

// recordByKey returns the record stored under one run's event key.
func (t *txn) recordByKey(runID, key string) (*runlog.Event, error) {
	records, err := t.queryRecords(`SELECT `+recordColumns+` FROM records WHERE run_id = ? AND event_key = ?`, runID, key)
	if err != nil || len(records) == 0 {
		return nil, err
	}
	return records[0], nil
}

// queryRecords decodes every record selected by query.
func (t *txn) queryRecords(query string, args ...any) ([]*runlog.Event, error) {
	rows, err := t.tx.QueryContext(t.ctx, query, args...)
	if err != nil {
		return nil, &dbError{err: fmt.Errorf("runstore: query records: %w", err)}
	}
	defer rows.Close() //nolint:errcheck // rows.Err reports iteration failures.
	var result []*runlog.Event
	for rows.Next() {
		record, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, record)
	}
	if err := rows.Err(); err != nil {
		return nil, &dbError{err: fmt.Errorf("runstore: read records: %w", err)}
	}
	return result, nil
}

// insertRecord assigns the next run-scoped ID and, for sessionful records,
// the next session commit position, then stores the record. The record's
// identity, owner and retry key were checked by the caller in this
// transaction; the unique keys still reject a duplicate key or position.
func (t *txn) insertRecord(record *runlog.Event) (string, error) {
	var seq int64
	if _, err := t.scanOne(`SELECT COALESCE(MAX(seq), 0) + 1 FROM records WHERE run_id = ?`,
		[]any{record.RunID}, &seq); err != nil {
		return "", err
	}
	var sessionSeq sql.NullInt64
	if record.SessionID != "" {
		if _, err := t.scanOne(`SELECT COALESCE(MAX(session_seq), 0) + 1 FROM records WHERE session_id = ? AND session_seq IS NOT NULL`,
			[]any{record.SessionID}, &sessionSeq); err != nil {
			return "", err
		}
	}
	subject, err := genrunidentity.EncodeRunSubject(&record.Subject)
	if err != nil {
		return "", fmt.Errorf("encode record subject: %w", err)
	}
	if _, err := t.exec(`INSERT INTO records (run_id, seq, event_key, session_id, session_seq, subject, turn_id, type, payload, payload_bytes, timestamp)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.RunID, seq, record.EventKey, record.SessionID, sessionSeq, subject, record.TurnID,
		string(record.Type), []byte(record.Payload), len(record.Payload), binaryTime(record.Timestamp)); err != nil {
		return "", err
	}
	return strconv.FormatInt(seq, 10), nil
}

// existingRecordResult returns the original record identity for an exact retry
// without writing another history entry.
func (t *txn) existingRecordResult(existing *runlog.Event) (storage.AppendResult, error) {
	status, err := t.sessionStatus(existing.SessionID)
	return storage.AppendResult{ID: existing.ID, SessionStatus: status}, err
}
