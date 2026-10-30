package runstore

// Seed row helpers translate preparation bindings, upload attempts and seed
// records between SQLite rows and the goa-ai storage types.

import (
	"bytes"
	"database/sql"
	"encoding/gob"
	"encoding/json"
	"fmt"
	"strconv"

	genregistry "goa.design/goa-ai/registry/gen/registry"
	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent/storage"
)

type (
	// attemptRow is one upload attempt: its frozen declaration and resolved
	// source, its accepted end positions and its publication state.
	attemptRow struct {
		// seed holds the declaration, the store-resolved source and the last
		// accepted history position. Origin is not stored here; seedResult
		// reads it from the operation binding.
		seed storage.RunSeed
		// endID is the last accepted record, including compiled-start parts.
		endID string
		// recordCount is the number of accepted records; positions are 1..n.
		recordCount int64
		// preparedBytes is the total size of accepted compiled-start parts.
		preparedBytes int64
		// published reports whether this attempt is the run's publication.
		published bool
		// abandoned reports whether settlement permanently closed the attempt.
		abandoned bool
		// hasSource reports whether the declared source record was appended.
		hasSource bool
	}
)

const (
	// attemptColumns lists the preparation_attempts columns read by
	// scanAttempt, in scan order.
	attemptColumns = `run_id, attempt_id, subject, session_id, command_id, kind, predecessor_run_id,
		source_run_id, source_end_id, exclude_reasoning, rendered_prompts, resolved_source,
		resolved_run_id, resolved_end_id, resolved_exclude_system, resolved_exclude_reasoning,
		seed_end_id, end_id, record_count, prepared_bytes, has_source, published, abandoned`
	// seedRecordColumns lists the seed_records columns read by
	// scanSeedRecord, in scan order.
	seedRecordColumns = `position, record_key, previous_id, messages, literal_data, literal_final,
		prefix_run_id, prefix_end_id, prefix_exclude_system, prefix_exclude_reasoning, prepared`
)

// sameSeedRecord reports whether an append retry carries exactly the stored
// record: the same key, position, predecessor and content.
func sameSeedRecord(stored, retry storage.SeedRecord) bool {
	if stored.ID != retry.ID || stored.Key != retry.Key || stored.PreviousID != retry.PreviousID ||
		!bytes.Equal(stored.Messages, retry.Messages) || !bytes.Equal(stored.Prepared, retry.Prepared) {
		return false
	}
	if (stored.LiteralPart == nil) != (retry.LiteralPart == nil) || (stored.Prefix == nil) != (retry.Prefix == nil) {
		return false
	}
	if stored.LiteralPart != nil && (stored.LiteralPart.Final != retry.LiteralPart.Final ||
		!bytes.Equal(stored.LiteralPart.Data, retry.LiteralPart.Data)) {
		return false
	}
	return stored.Prefix == nil || *stored.Prefix == *retry.Prefix
}

// nonEmptyBytes stores absent (empty) content as NULL.
func nonEmptyBytes(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return value
}

// hasUnstartedTerminal reports whether an execution that never started
// stored its terminal outcome under runID.
func (t *txn) hasUnstartedTerminal(runID string) (bool, error) {
	var id string
	return t.scanOne(`SELECT run_id FROM unstarted_terminals WHERE run_id = ?`, []any{runID}, &id)
}

// loadBinding returns the original command bound to a destination run. A
// missing binding returns the zero value with false.
func (t *txn) loadBinding(runID string) (storage.RunPreparationBegin, bool, error) {
	var (
		subject, origin []byte
		result          storage.RunPreparationBegin
	)
	found, err := t.scanOne(`SELECT subject, session_id, command_id, origin FROM preparation_operations WHERE run_id = ?`,
		[]any{runID}, &subject, &result.Operation.SessionID, &result.Operation.CommandID, &origin)
	if err != nil || !found {
		return storage.RunPreparationBegin{}, found, err
	}
	decodedSubject, err := genrunidentity.DecodeRunSubject(subject)
	if err != nil {
		return storage.RunPreparationBegin{}, false, fmt.Errorf("stored preparation %q subject: %w", runID, err)
	}
	decodedOrigin, err := genregistry.DecodeExecutionInvocation(origin)
	if err != nil {
		return storage.RunPreparationBegin{}, false, fmt.Errorf("stored preparation %q origin: %w", runID, err)
	}
	result.Operation.Subject = *decodedSubject
	result.Operation.RunID = runID
	result.Origin = *decodedOrigin
	return result, true, nil
}

// insertBinding stores the first original command for a destination run.
func (t *txn) insertBinding(command storage.RunPreparationBegin) error {
	subject, err := genrunidentity.EncodeRunSubject(&command.Operation.Subject)
	if err != nil {
		return fmt.Errorf("encode preparation subject: %w", err)
	}
	origin, err := genregistry.EncodeExecutionInvocation(&command.Origin)
	if err != nil {
		return fmt.Errorf("encode preparation origin: %w", err)
	}
	_, err = t.exec(`INSERT INTO preparation_operations (run_id, subject, session_id, command_id, origin) VALUES (?, ?, ?, ?, ?)`,
		command.Operation.RunID, subject, command.Operation.SessionID, command.Operation.CommandID, origin)
	return err
}

// loadAttempt returns one upload attempt.
func (t *txn) loadAttempt(runID, attemptID string) (attemptRow, bool, error) {
	return t.queryAttempt(`SELECT `+attemptColumns+` FROM preparation_attempts WHERE run_id = ? AND attempt_id = ?`, runID, attemptID)
}

// publishedAttempt returns the run's published attempt.
func (t *txn) publishedAttempt(runID string) (attemptRow, bool, error) {
	return t.queryAttempt(`SELECT `+attemptColumns+` FROM preparation_attempts WHERE run_id = ? AND published = 1`, runID)
}

// queryAttempt decodes the single attempt selected by query.
func (t *txn) queryAttempt(query string, args ...any) (attemptRow, bool, error) {
	var (
		row                                       attemptRow
		d                                         = &row.seed.Declaration
		subject, prompts                          []byte
		kind                                      string
		excludeReasoning, resolved                bool
		resolvedRunID, resolvedEndID              string
		resolvedExcludeSystem, resolvedExcludeRsn bool
	)
	found, err := t.scanOne(query, args, &d.RunID, &d.AttemptID, &subject, &d.SessionID, &d.CommandID, &kind,
		&d.PredecessorRunID, &d.SourceRunID, &d.SourceEndID, &excludeReasoning, &prompts, &resolved,
		&resolvedRunID, &resolvedEndID, &resolvedExcludeSystem, &resolvedExcludeRsn,
		&row.seed.EndID, &row.endID, &row.recordCount, &row.preparedBytes, &row.hasSource, &row.published, &row.abandoned)
	if err != nil || !found {
		return attemptRow{}, found, err
	}
	decoded, err := genrunidentity.DecodeRunSubject(subject)
	if err != nil {
		return attemptRow{}, false, fmt.Errorf("stored preparation attempt %q subject: %w", d.AttemptID, err)
	}
	d.Subject = *decoded
	d.Kind = storage.SeedKind(kind)
	d.ExcludeReasoning = excludeReasoning
	if prompts != nil {
		if err := gob.NewDecoder(bytes.NewReader(prompts)).Decode(&d.RenderedPrompts); err != nil {
			return attemptRow{}, false, fmt.Errorf("stored preparation attempt %q prompts: %w", d.AttemptID, err)
		}
	}
	if resolved {
		row.seed.Source = &storage.HistoryPrefix{
			RunID: resolvedRunID, EndID: resolvedEndID,
			ExcludeSystem: resolvedExcludeSystem, ExcludeReasoning: resolvedExcludeRsn,
		}
	}
	return row, true, nil
}

// insertAttempt stores a new upload attempt.
func (t *txn) insertAttempt(row attemptRow) error {
	d := row.seed.Declaration
	subject, err := genrunidentity.EncodeRunSubject(&d.Subject)
	if err != nil {
		return fmt.Errorf("encode preparation attempt subject: %w", err)
	}
	// Gob keeps prompt strings byte for byte; JSON would replace invalid
	// UTF-8 and break exact declaration retries.
	var prompts []byte
	if len(d.RenderedPrompts) > 0 {
		if prompts, err = gobEncode(d.RenderedPrompts); err != nil {
			return fmt.Errorf("encode preparation attempt prompts: %w", err)
		}
	}
	source := row.seed.Source
	if source == nil {
		source = &storage.HistoryPrefix{}
	}
	_, err = t.exec(`INSERT INTO preparation_attempts (`+attemptColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.RunID, d.AttemptID, subject, d.SessionID, d.CommandID, string(d.Kind), d.PredecessorRunID,
		d.SourceRunID, d.SourceEndID, d.ExcludeReasoning, nullableBytes(prompts), row.seed.Source != nil,
		source.RunID, source.EndID, source.ExcludeSystem, source.ExcludeReasoning,
		row.seed.EndID, row.endID, row.recordCount, row.preparedBytes, row.hasSource, row.published, row.abandoned)
	return err
}

// updateAttempt stores an attempt's accepted positions and abandonment.
// Publication uses publishAttempt so the unique index decides the winner.
func (t *txn) updateAttempt(row attemptRow) error {
	_, err := t.exec(`UPDATE preparation_attempts SET seed_end_id = ?, end_id = ?, record_count = ?,
		prepared_bytes = ?, has_source = ?, abandoned = ? WHERE run_id = ? AND attempt_id = ?`,
		row.seed.EndID, row.endID, row.recordCount, row.preparedBytes, row.hasSource, row.abandoned,
		row.seed.Declaration.RunID, row.seed.Declaration.AttemptID)
	return err
}

// publishAttempt marks one attempt as the run's publication. The partial
// unique index rejects a second published attempt for the same run.
func (t *txn) publishAttempt(runID, attemptID string) error {
	_, err := t.exec(`UPDATE preparation_attempts SET published = 1 WHERE run_id = ? AND attempt_id = ?`, runID, attemptID)
	return err
}

// seedRecordByKey returns an attempt's record stored under an append key.
func (t *txn) seedRecordByKey(runID, attemptID, key string) (storage.SeedRecord, bool, error) {
	var (
		record storage.SeedRecord
		found  bool
	)
	err := t.eachSeedRecord(`SELECT `+seedRecordColumns+` FROM seed_records WHERE run_id = ? AND attempt_id = ? AND record_key = ?`,
		[]any{runID, attemptID, key}, func(stored storage.SeedRecord) bool {
			record, found = stored, true
			return false
		})
	return record, found, err
}

// seedRecords calls visit for each record with a position in (after, end],
// at most limit records, reading one row at a time. Iteration stops when
// visit returns false, so later rows are never read.
func (t *txn) seedRecords(runID, attemptID string, after, end int64, limit int, visit func(storage.SeedRecord) bool) error {
	return t.eachSeedRecord(`SELECT `+seedRecordColumns+` FROM seed_records
		WHERE run_id = ? AND attempt_id = ? AND position > ? AND position <= ? ORDER BY position LIMIT ?`,
		[]any{runID, attemptID, after, end, limit}, visit)
}

// seedRecordSizes returns at most limit record positions and encoded JSON
// sizes in (after, end], without reading record content.
func (t *txn) seedRecordSizes(runID, attemptID string, after, end int64, limit int) ([]recordSize, error) {
	rows, err := t.tx.QueryContext(t.ctx, `SELECT position, json_bytes FROM seed_records
		WHERE run_id = ? AND attempt_id = ? AND position > ? AND position <= ? ORDER BY position LIMIT ?`,
		runID, attemptID, after, end, limit)
	if err != nil {
		return nil, &dbError{err: fmt.Errorf("runstore: query seed record sizes: %w", err)}
	}
	return scanSizes(rows)
}

// eachSeedRecord decodes the seed records selected by query one row at a
// time and passes each to visit until it returns false.
func (t *txn) eachSeedRecord(query string, args []any, visit func(storage.SeedRecord) bool) error {
	rows, err := t.tx.QueryContext(t.ctx, query, args...)
	if err != nil {
		return &dbError{err: fmt.Errorf("runstore: query seed records: %w", err)}
	}
	defer rows.Close() //nolint:errcheck // rows.Err reports iteration failures.
	for rows.Next() {
		var (
			record                       storage.SeedRecord
			position                     int64
			messages, literal, prepared  []byte
			literalFinal                 sql.NullBool
			prefixRunID, prefixEndID     sql.NullString
			excludeSystem, excludeReason sql.NullBool
		)
		if err := rows.Scan(&position, &record.Key, &record.PreviousID, &messages, &literal, &literalFinal,
			&prefixRunID, &prefixEndID, &excludeSystem, &excludeReason, &prepared); err != nil {
			return &dbError{err: fmt.Errorf("runstore: scan seed record: %w", err)}
		}
		record.ID = strconv.FormatInt(position, 10)
		record.Messages = messages
		record.Prepared = prepared
		if literal != nil {
			record.LiteralPart = &storage.LiteralPart{Data: literal, Final: literalFinal.Bool}
		}
		if prefixRunID.Valid {
			record.Prefix = &storage.HistoryPrefix{
				RunID: prefixRunID.String, EndID: prefixEndID.String,
				ExcludeSystem: excludeSystem.Bool, ExcludeReasoning: excludeReason.Bool,
			}
		}
		if !visit(record) {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return &dbError{err: fmt.Errorf("runstore: read seed records: %w", err)}
	}
	return nil
}

// insertSeedRecord stores one accepted record at position. The primary key
// and the unique append key make each position and key single-winner.
func (t *txn) insertSeedRecord(runID, attemptID string, position int64, record storage.SeedRecord) error {
	// Measure the record exactly as reads return it: empty content reads
	// back as nil.
	returned := record
	returned.Messages, returned.Prepared = nil, nil
	if len(record.Messages) > 0 {
		returned.Messages = record.Messages
	}
	if len(record.Prepared) > 0 {
		returned.Prepared = record.Prepared
	}
	encoded, err := json.Marshal(returned)
	if err != nil {
		return fmt.Errorf("encode seed record: %w", err)
	}
	var (
		literal, literalFinal, prefixRunID, prefixEndID any
		excludeSystem, excludeReasoning                 any
	)
	if record.LiteralPart != nil {
		literal, literalFinal = record.LiteralPart.Data, record.LiteralPart.Final
	}
	if record.Prefix != nil {
		prefixRunID, prefixEndID = record.Prefix.RunID, record.Prefix.EndID
		excludeSystem, excludeReasoning = record.Prefix.ExcludeSystem, record.Prefix.ExcludeReasoning
	}
	_, err = t.exec(`INSERT INTO seed_records (run_id, attempt_id, position, record_key, previous_id, messages,
		literal_data, literal_final, prefix_run_id, prefix_end_id, prefix_exclude_system, prefix_exclude_reasoning, prepared, json_bytes)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		runID, attemptID, position, record.Key, record.PreviousID, nonEmptyBytes(record.Messages),
		literal, literalFinal, prefixRunID, prefixEndID, excludeSystem, excludeReasoning, nonEmptyBytes(record.Prepared), len(encoded))
	return err
}
