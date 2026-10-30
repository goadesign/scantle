package runstore

// Preparation binds an original command, then one or more upload attempts
// append ordered initial-history and compiled-start records. Publication makes
// exactly one attempt per run immutable and usable. Every check and write runs
// in the write transaction shared with run starts and session purge.

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"

	genregistry "goa.design/goa-ai/registry/gen/registry"
	genrunterminal "goa.design/goa-ai/registry/gen/runterminal"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
)

// BeginRunPreparationOperation binds one invocation in the transaction used by
// seed publication and run starts. It performs no provider or history reads.
func (s *Store) BeginRunPreparationOperation(ctx context.Context, command storage.RunPreparationBegin) (genrunterminal.PreparationDecision, error) {
	if err := ctx.Err(); err != nil {
		return genrunterminal.PreparationDecision{}, err
	}
	if err := storage.ValidateRunPreparationBegin(command); err != nil {
		return genrunterminal.PreparationDecision{}, storage.NewContractError(err)
	}
	var decision genrunterminal.PreparationDecision
	err := s.write(ctx, func(t *txn) error {
		terminal, err := t.hasUnstartedTerminal(command.Operation.RunID)
		if err != nil {
			return err
		}
		if terminal {
			return session.ErrRunConflict
		}
		if err := t.sessionOwnerExists(command.Operation.SessionID); err != nil {
			return err
		}
		original, exists, err := t.loadBinding(command.Operation.RunID)
		if err != nil {
			return err
		}
		existing, runExists, err := t.loadRun(command.Operation.RunID)
		if err != nil {
			return err
		}
		if exists {
			if !reflect.DeepEqual(original, command) {
				return storage.ErrSeedConflict
			}
		} else {
			if runExists {
				return session.ErrRunConflict
			}
			if err := t.insertBinding(command); err != nil {
				return err
			}
		}
		// An existing Run's reason wins over later Session closure. The binding
		// remains saved when stopped so history publication can produce an
		// exact engine request whose first start records that cancellation.
		reason := existing.meta.CancellationReason
		if reason == "" && command.Operation.SessionID != "" {
			current, _, err := t.loadSession(command.Operation.SessionID)
			if err != nil {
				return err
			}
			if current.Status == session.StatusEnded {
				reason = run.CancellationReasonSessionEnded
			}
		}
		if reason != "" {
			decision = genrunterminal.PreparationDecision{State: genrunterminal.NewPreparationStateStop(genrunterminal.PreparationStateBranchStop(reason))}
			return nil
		}
		decision = genrunterminal.PreparationDecision{State: genrunterminal.NewPreparationStateProceed(&genrunterminal.PreparationProceed{})}
		return nil
	})
	return contractResult(decision, err)
}

// BeginRunSeed reserves the destination attempt and captures its source in
// the transaction used by lifecycle writes. Failed declarations reserve
// nothing.
func (s *Store) BeginRunSeed(ctx context.Context, declaration storage.SeedDeclaration) (storage.RunSeed, error) {
	if err := ctx.Err(); err != nil {
		return storage.RunSeed{}, err
	}
	if err := storage.ValidateSeedDeclaration(declaration); err != nil {
		return contractResult(storage.RunSeed{}, err)
	}
	var result storage.RunSeed
	err := s.write(ctx, func(t *txn) error {
		terminal, err := t.hasUnstartedTerminal(declaration.RunID)
		if err != nil {
			return err
		}
		if terminal {
			return session.ErrRunConflict
		}
		if err := t.sessionOwnerExists(declaration.SessionID); err != nil {
			return err
		}
		operation := storage.PreparationOperation{Subject: declaration.Subject, RunID: declaration.RunID, SessionID: declaration.SessionID, CommandID: declaration.CommandID}
		binding, exists, err := t.loadBinding(declaration.RunID)
		if err != nil {
			return err
		}
		if !exists || !reflect.DeepEqual(binding.Operation, operation) {
			return storage.ErrSeedConflict
		}
		existing, found, err := t.loadAttempt(declaration.RunID, declaration.AttemptID)
		if err != nil {
			return err
		}
		if found {
			if existing.abandoned {
				return storage.ErrPreparationAbandoned
			}
			if !storage.EqualSeedDeclarations(existing.seed.Declaration, declaration) {
				return storage.ErrSeedConflict
			}
			result = existing.seed
			result.Origin = binding.Origin
			return nil
		}
		_, accepted, err := t.publishedAttempt(declaration.RunID)
		if err != nil {
			return err
		}
		if accepted {
			return storage.ErrSeedConflict
		}
		_, runExists, err := t.loadRun(declaration.RunID)
		if err != nil {
			return err
		}
		if runExists {
			return session.ErrRunConflict
		}
		source, err := t.seedSource(declaration, binding.Origin)
		if err != nil {
			return err
		}
		attempt := attemptRow{
			seed:  storage.RunSeed{Declaration: declaration, Source: source, EndID: storage.EmptySeedEndID},
			endID: storage.EmptySeedEndID,
		}
		if err := t.insertAttempt(attempt); err != nil {
			return err
		}
		result = attempt.seed
		result.Origin = binding.Origin
		return nil
	})
	return contractResult(result, err)
}

// AppendRunSeed accepts one bounded literal, literal part, declared source or
// compiled-start part exactly once. A lost response can be retried using the
// same key, position and bytes.
func (s *Store) AppendRunSeed(ctx context.Context, command storage.SeedAppend) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := storage.ValidateSeedAppend(command); err != nil {
		return contractResult("", err)
	}
	var id string
	err := s.write(ctx, func(t *txn) error {
		var err error
		id, err = t.appendSeed(command)
		return err
	})
	return contractResult(id, err)
}

// PublishRunSeed seals only the full accepted chain. A referenced source must
// occur exactly once and no literal may remain open. Only one attempt per run
// can be published.
func (s *Store) PublishRunSeed(ctx context.Context, publication storage.SeedPublication) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return contractError(s.write(ctx, func(t *txn) error {
		terminal, err := t.hasUnstartedTerminal(publication.RunID)
		if err != nil {
			return err
		}
		if terminal {
			return session.ErrRunConflict
		}
		state, err := t.preparation(publication.RunID, publication.AttemptID)
		if err != nil {
			return err
		}
		if err := t.sessionOwnerExists(state.seed.Declaration.SessionID); err != nil {
			return err
		}
		open, err := t.literalOpen(state)
		if err != nil {
			return err
		}
		if publication.SeedEndID != state.seed.EndID || publication.EndID != state.endID ||
			publication.PreparedBytes <= 0 || publication.PreparedBytes != state.preparedBytes ||
			(state.seed.Source != nil && !state.hasSource) || open {
			return storage.ErrSeedConflict
		}
		accepted, exists, err := t.publishedAttempt(publication.RunID)
		if err != nil {
			return err
		}
		if exists && accepted.seed.Declaration.AttemptID != publication.AttemptID {
			return storage.ErrSeedConflict
		}
		return t.publishAttempt(publication.RunID, publication.AttemptID)
	}))
}

// FindRunPreparation reads only the accepted value for the original command.
// Absence does not stop any upload that is still in progress.
func (s *Store) FindRunPreparation(ctx context.Context, operation storage.PreparationOperation) (storage.RunPreparation, bool, error) {
	if err := ctx.Err(); err != nil {
		return storage.RunPreparation{}, false, err
	}
	if err := storage.ValidatePreparationOperation(operation); err != nil {
		return storage.RunPreparation{}, false, storage.NewContractError(err)
	}
	var (
		result storage.RunPreparation
		found  bool
	)
	err := s.read(ctx, func(t *txn) error {
		var err error
		result, found, err = t.acceptedPreparation(operation)
		return err
	})
	if err != nil {
		_, err = contractResult(result, err)
		return storage.RunPreparation{}, false, err
	}
	return result, found, nil
}

// SettleRunPreparation returns the accepted value or permanently closes the
// exact attempt in the transaction shared with publication. A missing attempt
// is recorded as abandoned so a late first begin cannot reopen it.
func (s *Store) SettleRunPreparation(ctx context.Context, attempt storage.PreparationAttempt) (storage.RunPreparation, bool, error) {
	if err := ctx.Err(); err != nil {
		return storage.RunPreparation{}, false, err
	}
	if err := storage.ValidatePreparationAttempt(attempt); err != nil {
		return storage.RunPreparation{}, false, storage.NewContractError(err)
	}
	var (
		result storage.RunPreparation
		found  bool
	)
	err := s.write(ctx, func(t *txn) error {
		var err error
		result, found, err = t.acceptedPreparation(attempt.Operation)
		if err != nil || found {
			return err
		}
		state, exists, err := t.loadAttempt(attempt.Operation.RunID, attempt.AttemptID)
		if err != nil {
			return err
		}
		if exists && !preparationMatches(state, attempt.Operation) {
			return storage.ErrSeedConflict
		}
		if !exists {
			return t.insertAttempt(attemptRow{
				seed: storage.RunSeed{Declaration: storage.SeedDeclaration{
					Subject: attempt.Operation.Subject, RunID: attempt.Operation.RunID,
					SessionID: attempt.Operation.SessionID, CommandID: attempt.Operation.CommandID,
					AttemptID: attempt.AttemptID,
				}},
				abandoned: true,
			})
		}
		state.abandoned = true
		return t.updateAttempt(state)
	})
	if err != nil {
		_, err = contractResult(result, err)
		return storage.RunPreparation{}, false, err
	}
	return result, found, nil
}

// LoadRunSeed returns an exact publication while its owning session exists.
func (s *Store) LoadRunSeed(ctx context.Context, runID, endID string) (storage.RunSeed, error) {
	if err := ctx.Err(); err != nil {
		return storage.RunSeed{}, err
	}
	var result storage.RunSeed
	err := s.read(ctx, func(t *txn) error {
		state, err := t.publishedSeed(runID, endID)
		if err != nil {
			return err
		}
		result, err = t.seedResult(state.seed)
		return err
	})
	return contractResult(result, err)
}

// ListRunSeedRecords returns at most 512 history records of one exact
// publication within the logical page budget.
func (s *Store) ListRunSeedRecords(ctx context.Context, runID, endID, afterID string, limit int) (storage.SeedPage, error) {
	if err := ctx.Err(); err != nil {
		return storage.SeedPage{}, err
	}
	if limit <= 0 {
		return contractResult(storage.SeedPage{}, storage.ErrInvalidTranscriptPosition)
	}
	var page storage.SeedPage
	err := s.read(ctx, func(t *txn) error {
		state, err := t.publishedSeed(runID, endID)
		if err != nil {
			return err
		}
		end, err := strconv.ParseInt(state.seed.EndID, 10, 64)
		if err != nil {
			return err
		}
		page, err = t.preparationPage(state, 0, end, afterID, limit)
		return err
	})
	return contractResult(page, err)
}

// ListRunPreparationRecords returns only the compiled-start suffix of the
// accepted body. History records are not read.
func (s *Store) ListRunPreparationRecords(ctx context.Context, runID, endID, afterID string, limit int) (storage.SeedPage, error) {
	if err := ctx.Err(); err != nil {
		return storage.SeedPage{}, err
	}
	if limit <= 0 {
		return contractResult(storage.SeedPage{}, storage.ErrInvalidTranscriptPosition)
	}
	var page storage.SeedPage
	err := s.read(ctx, func(t *txn) error {
		state, err := t.acceptedSeed(runID)
		if err != nil {
			return err
		}
		if state.endID != endID {
			return storage.ErrSeedConflict
		}
		start, err := strconv.ParseInt(state.seed.EndID, 10, 64)
		if err != nil {
			return err
		}
		page, err = t.preparationPage(state, start, state.recordCount, afterID, limit)
		return err
	})
	return contractResult(page, err)
}

// preparationMatches reports whether an attempt belongs to the operation.
func preparationMatches(state attemptRow, operation storage.PreparationOperation) bool {
	d := state.seed.Declaration
	return d.RunID == operation.RunID && reflect.DeepEqual(d.Subject, operation.Subject) &&
		d.SessionID == operation.SessionID && d.CommandID == operation.CommandID
}

// appendSeed applies one seed append to its open attempt.
func (t *txn) appendSeed(command storage.SeedAppend) (string, error) {
	state, err := t.preparation(command.RunID, command.AttemptID)
	if err != nil {
		return "", err
	}
	if _, service := state.seed.Declaration.Subject.Executor.AsServiceTool(); service && len(command.Record.Prepared) == 0 {
		return "", errors.New("service preparation cannot contain model history")
	}
	record := command.Record
	stored, exists, err := t.seedRecordByKey(command.RunID, command.AttemptID, record.Key)
	if err != nil {
		return "", err
	}
	if exists {
		record.ID = stored.ID
		if !sameSeedRecord(stored, record) {
			return "", storage.ErrSeedConflict
		}
		return stored.ID, nil
	}
	if state.published || record.PreviousID != state.endID {
		return "", storage.ErrSeedConflict
	}
	open, err := t.literalOpen(state)
	if err != nil {
		return "", err
	}
	if open && record.LiteralPart == nil {
		return "", storage.ErrSeedConflict
	}
	if state.preparedBytes > 0 && len(record.Prepared) == 0 {
		return "", storage.ErrSeedConflict
	}
	if err := t.sessionOwnerExists(state.seed.Declaration.SessionID); err != nil {
		return "", err
	}
	if record.Prefix != nil {
		if state.hasSource || !reflect.DeepEqual(record.Prefix, state.seed.Source) {
			return "", storage.ErrSeedConflict
		}
	}
	position := state.recordCount + 1
	record.ID = strconv.FormatInt(position, 10)
	if err := storage.ValidateSeedPageSize(storage.SeedPage{
		Records: []storage.SeedRecord{record}, NextCursor: record.ID,
	}); err != nil {
		return "", err
	}
	if err := t.insertSeedRecord(command.RunID, command.AttemptID, position, record); err != nil {
		return "", err
	}
	if record.Prefix != nil {
		state.hasSource = true
	}
	state.recordCount = position
	state.endID = record.ID
	if len(record.Prepared) > 0 {
		state.preparedBytes += int64(len(record.Prepared))
	} else {
		state.seed.EndID = record.ID
	}
	return record.ID, t.updateAttempt(state)
}

// preparationPage reads the accepted history or compiled-start range after
// afterID. Each continuation names the last returned record.
func (t *txn) preparationPage(state attemptRow, start, end int64, afterID string, limit int) (storage.SeedPage, error) {
	after := start
	if afterID != "" {
		parsed, err := strconv.ParseInt(afterID, 10, 64)
		if err != nil || parsed <= start || parsed > end || strconv.FormatInt(parsed, 10) != afterID {
			return storage.SeedPage{}, storage.ErrInvalidTranscriptPosition
		}
		after = parsed
	}
	limit = min(limit, transcriptPageMaxRecords)
	// Records are read one at a time; reading stops at the first record that
	// would overflow the page budget, so at most one record beyond the page
	// is read.
	var (
		page     storage.SeedPage
		full     bool
		emptyErr error
	)
	runID, attemptID := state.seed.Declaration.RunID, state.seed.Declaration.AttemptID
	err := t.seedRecords(runID, attemptID, after, end, limit, func(record storage.SeedRecord) bool {
		page.Records = append(page.Records, record)
		// Count the complete response, including a potential next cursor.
		page.NextCursor = record.ID
		if err := storage.ValidateSeedPageSize(page); err != nil {
			page.Records = page.Records[:len(page.Records)-1]
			if len(page.Records) == 0 {
				emptyErr = err
			}
			full = true
			return false
		}
		return true
	})
	if err != nil {
		return storage.SeedPage{}, err
	}
	if emptyErr != nil {
		return storage.SeedPage{}, emptyErr
	}
	page.NextCursor = ""
	if len(page.Records) == 0 {
		return page, nil
	}
	last := page.Records[len(page.Records)-1].ID
	if full {
		page.NextCursor = last
		return page, nil
	}
	if len(page.Records) == limit {
		lastPosition, err := strconv.ParseInt(last, 10, 64)
		if err != nil {
			return storage.SeedPage{}, err
		}
		var one int
		more, err := t.scanOne(`SELECT 1 FROM seed_records WHERE run_id = ? AND attempt_id = ? AND position > ? AND position <= ? LIMIT 1`,
			[]any{runID, attemptID, lastPosition, end}, &one)
		if err != nil {
			return storage.SeedPage{}, err
		}
		if more {
			page.NextCursor = last
		}
	}
	return page, nil
}

// acceptedPreparation returns the published body for the original command.
func (t *txn) acceptedPreparation(operation storage.PreparationOperation) (storage.RunPreparation, bool, error) {
	if err := t.sessionOwnerExists(operation.SessionID); err != nil {
		return storage.RunPreparation{}, false, err
	}
	original, exists, err := t.loadBinding(operation.RunID)
	if err != nil {
		return storage.RunPreparation{}, false, err
	}
	if exists && !reflect.DeepEqual(original.Operation, operation) {
		return storage.RunPreparation{}, false, storage.ErrSeedConflict
	}
	state, found, err := t.publishedAttempt(operation.RunID)
	if err != nil || !found {
		return storage.RunPreparation{}, false, err
	}
	if !preparationMatches(state, operation) {
		return storage.RunPreparation{}, false, storage.ErrSeedConflict
	}
	seed, err := t.seedResult(state.seed)
	if err != nil {
		return storage.RunPreparation{}, false, err
	}
	return storage.RunPreparation{Seed: seed, EndID: state.endID, PreparedBytes: state.preparedBytes}, true, nil
}

// preparation loads an attempt that may still receive appends.
func (t *txn) preparation(runID, attemptID string) (attemptRow, error) {
	state, found, err := t.loadAttempt(runID, attemptID)
	if err != nil {
		return attemptRow{}, err
	}
	if !found {
		return attemptRow{}, storage.ErrSeedNotFound
	}
	if err := t.sessionOwnerExists(state.seed.Declaration.SessionID); err != nil {
		return attemptRow{}, err
	}
	if state.abandoned {
		return attemptRow{}, storage.ErrPreparationAbandoned
	}
	return state, nil
}

// seedSource proves that a reference belongs to the same subject and session
// and captures the successful completion position without caller inference.
func (t *txn) seedSource(d storage.SeedDeclaration, origin genregistry.ExecutionInvocation) (*storage.HistoryPrefix, error) {
	if d.Kind == storage.SeedLiteral {
		if d.PredecessorRunID != "" {
			predecessor, exists, err := t.loadRun(d.PredecessorRunID)
			if err != nil {
				return nil, err
			}
			if !exists {
				return nil, session.ErrRunNotFound
			}
			if predecessor.meta.Status != session.RunStatusSuspended || predecessor.meta.SessionID != d.SessionID ||
				!reflect.DeepEqual(predecessor.meta.Subject, d.Subject) || !reflect.DeepEqual(predecessor.meta.Origin, origin) {
				return nil, storage.ErrSeedConflict
			}
		}
		return nil, nil //nolint:nilnil // A literal seed has no source; RunSeed.Source is nil by contract.
	}
	source, ok, err := t.loadRun(d.SourceRunID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, session.ErrRunNotFound
	}
	if source.meta.SessionID != d.SessionID || !reflect.DeepEqual(source.meta.Subject, d.Subject) {
		return nil, storage.ErrRunRecordOwnerMismatch
	}
	expected := session.RunStatusCompleted
	if d.Kind == storage.SeedContinuation {
		if !reflect.DeepEqual(source.meta.Origin, origin) {
			return nil, storage.ErrSeedConflict
		}
		expected = session.RunStatusSuspended
	}
	if source.meta.Status != expected {
		return nil, fmt.Errorf("history source %q has status %q, want %q", d.SourceRunID, source.meta.Status, expected)
	}
	endID := d.SourceEndID
	if d.Kind == storage.SeedNextTurn {
		terminal, err := t.recordByKey(d.SourceRunID, source.keys.terminal)
		if err != nil {
			return nil, err
		}
		if terminal == nil {
			return nil, fmt.Errorf("completed history source %q has no terminal record", d.SourceRunID)
		}
		endID = terminal.ID
	} else {
		position, _, err := t.committedPosition(d.SourceRunID, endID)
		if err != nil {
			return nil, err
		}
		if position == 0 {
			return nil, storage.ErrInvalidTranscriptPosition
		}
	}
	return &storage.HistoryPrefix{
		RunID: d.SourceRunID, EndID: endID,
		ExcludeSystem: d.Kind == storage.SeedNextTurn, ExcludeReasoning: d.ExcludeReasoning,
	}, nil
}

// acceptedSeed loads the run's published attempt while its owner exists.
func (t *txn) acceptedSeed(runID string) (attemptRow, error) {
	state, found, err := t.publishedAttempt(runID)
	if err != nil {
		return attemptRow{}, err
	}
	if !found {
		return attemptRow{}, storage.ErrSeedNotFound
	}
	if err := t.sessionOwnerExists(state.seed.Declaration.SessionID); err != nil {
		return attemptRow{}, err
	}
	return state, nil
}

// publishedSeed loads the run's publication when its history ends at endID.
func (t *txn) publishedSeed(runID, endID string) (attemptRow, error) {
	state, err := t.acceptedSeed(runID)
	if err != nil {
		return attemptRow{}, err
	}
	if endID != state.seed.EndID {
		return attemptRow{}, storage.ErrSeedConflict
	}
	return state, nil
}

// validateStartSeed rejects any start that does not attach the exact
// published initial history reserved for this subject and session.
func (t *txn) validateStartSeed(start session.RunStart) error {
	terminal, err := t.hasUnstartedTerminal(start.RunID)
	if err != nil {
		return err
	}
	if terminal {
		return session.ErrRunConflict
	}
	state, err := t.publishedSeed(start.RunID, start.SeedEndID)
	if err != nil {
		return err
	}
	d := state.seed.Declaration
	binding, _, err := t.loadBinding(start.RunID)
	if err != nil {
		return err
	}
	origin := binding.Origin
	if err := storage.ValidateSeedDeclaration(d); err != nil {
		return err
	}
	if start.ParentRunID == "" && start.PredecessorRunID == "" {
		root, selected := origin.Invocation.AsRoot()
		if !selected || root.RunID != start.RunID {
			return storage.ErrSeedConflict
		}
	}
	if !reflect.DeepEqual(d.Subject, start.Subject) || d.SessionID != start.SessionID {
		return storage.ErrRunRecordOwnerMismatch
	}
	if d.PredecessorRunID != start.PredecessorRunID {
		return storage.ErrSeedConflict
	}
	if _, service := start.Subject.Executor.AsServiceTool(); service && (d.Kind != storage.SeedLiteral || state.seed.EndID != storage.EmptySeedEndID) {
		return storage.ErrSeedConflict
	}
	if start.PredecessorRunID != "" {
		predecessor, exists, err := t.loadRun(start.PredecessorRunID)
		if err != nil {
			return err
		}
		if !exists {
			return session.ErrRunNotFound
		}
		if !reflect.DeepEqual(predecessor.meta.Origin, origin) {
			return storage.ErrSeedConflict
		}
	}
	return nil
}

// seedResult returns seed metadata with the invocation saved for its
// operation. Accepted seeds always have a matching preparation binding.
func (t *txn) seedResult(seed storage.RunSeed) (storage.RunSeed, error) {
	binding, _, err := t.loadBinding(seed.Declaration.RunID)
	seed.Origin = binding.Origin
	return seed, err
}

// literalOpen reports whether the attempt's last accepted record is a
// nonfinal literal part, which only the next part may follow.
func (t *txn) literalOpen(state attemptRow) (bool, error) {
	if state.recordCount == 0 {
		return false, nil
	}
	var open bool
	_, err := t.scanOne(`SELECT literal_data IS NOT NULL AND literal_final = 0 FROM seed_records
		WHERE run_id = ? AND attempt_id = ? AND position = ?`,
		[]any{state.seed.Declaration.RunID, state.seed.Declaration.AttemptID, state.recordCount}, &open)
	return open, err
}
