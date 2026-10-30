package runstore

// An execution's first failure or cancellation is stored when no Run exists.
// The write transaction orders it with preparation and Run creation; a stored
// terminal outcome prevents late work from publishing or starting.

import (
	"bytes"
	"context"
	"errors"
	"reflect"

	genrunterminal "goa.design/goa-ai/registry/gen/runterminal"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
)

// RecordUnstartedExecutionTerminal validates the execution owner's command,
// then stores its first outcome or returns the exact already stored decision.
func (s *Store) RecordUnstartedExecutionTerminal(ctx context.Context, command genrunterminal.UnstartedExecutionTerminal) (genrunterminal.UnstartedExecutionTerminalResult, error) {
	if err := ctx.Err(); err != nil {
		return genrunterminal.UnstartedExecutionTerminalResult{}, err
	}
	encoded, err := genrunterminal.EncodeUnstartedExecutionTerminal(&command)
	if err != nil {
		return genrunterminal.UnstartedExecutionTerminalResult{}, storage.NewContractError(err)
	}
	if bytes.Equal(command.RequestDigest, make([]byte, 32)) {
		return genrunterminal.UnstartedExecutionTerminalResult{}, storage.NewContractError(errors.New("accepted request digest is missing"))
	}
	selected, err := genrunterminal.DecodeUnstartedExecutionTerminal(encoded)
	if err != nil {
		return genrunterminal.UnstartedExecutionTerminalResult{}, err
	}
	sessionID := ""
	if selected.SessionID != nil {
		sessionID = *selected.SessionID
	}
	var result genrunterminal.UnstartedExecutionTerminalResult
	err = s.write(ctx, func(t *txn) error {
		var err error
		result, err = t.recordUnstartedTerminal(selected, sessionID, encoded)
		return err
	})
	return contractResult(result, err)
}

// recordUnstartedTerminal chooses between an existing Run, an earlier
// terminal outcome and storing this one.
func (t *txn) recordUnstartedTerminal(selected *genrunterminal.UnstartedExecutionTerminal, sessionID string, encoded []byte) (genrunterminal.UnstartedExecutionTerminalResult, error) {
	if err := t.sessionOwnerExists(sessionID); err != nil {
		return genrunterminal.UnstartedExecutionTerminalResult{}, err
	}
	existing, exists, err := t.loadRun(selected.RunID)
	if err != nil {
		return genrunterminal.UnstartedExecutionTerminalResult{}, err
	}
	if exists {
		meta := existing.meta
		if !reflect.DeepEqual(meta.Subject, *selected.Subject) || meta.SessionID != sessionID || !reflect.DeepEqual(meta.Origin, *selected.Origin) {
			return genrunterminal.UnstartedExecutionTerminalResult{}, session.ErrRunConflict
		}
		if err := checkStartDigest(existing, [32]byte(selected.RequestDigest)); err != nil {
			return genrunterminal.UnstartedExecutionTerminalResult{}, err
		}
		return genrunterminal.UnstartedExecutionTerminalResult{Result: genrunterminal.NewUnstartedExecutionTerminalDecisionStarted(&genrunterminal.StartedExecution{})}, nil
	}
	var saved []byte
	found, err := t.scanOne(`SELECT encoded FROM unstarted_terminals WHERE run_id = ?`, []any{selected.RunID}, &saved)
	if err != nil {
		return genrunterminal.UnstartedExecutionTerminalResult{}, err
	}
	if found {
		prior, err := genrunterminal.DecodeUnstartedExecutionTerminal(saved)
		if err != nil {
			return genrunterminal.UnstartedExecutionTerminalResult{}, err
		}
		if !reflect.DeepEqual(prior.Subject, selected.Subject) || !reflect.DeepEqual(prior.SessionID, selected.SessionID) ||
			!reflect.DeepEqual(prior.Origin, selected.Origin) || !bytes.Equal(prior.RequestDigest, selected.RequestDigest) {
			return genrunterminal.UnstartedExecutionTerminalResult{}, session.ErrRunConflict
		}
		if bytes.Equal(saved, encoded) {
			return genrunterminal.UnstartedExecutionTerminalResult{Result: genrunterminal.NewUnstartedExecutionTerminalDecisionReplayed(prior)}, nil
		}
		return genrunterminal.UnstartedExecutionTerminalResult{Result: genrunterminal.NewUnstartedExecutionTerminalDecisionExisting(prior)}, nil
	}
	binding, bound, err := t.loadBinding(selected.RunID)
	if err != nil {
		return genrunterminal.UnstartedExecutionTerminalResult{}, err
	}
	if bound {
		if !reflect.DeepEqual(binding.Operation.Subject, *selected.Subject) || binding.Operation.SessionID != sessionID || !reflect.DeepEqual(binding.Origin, *selected.Origin) {
			return genrunterminal.UnstartedExecutionTerminalResult{}, session.ErrRunConflict
		}
	} else {
		root, isRoot := selected.Origin.Invocation.AsRoot()
		if !isRoot || root.RunID != selected.RunID {
			return genrunterminal.UnstartedExecutionTerminalResult{}, storage.ErrSeedConflict
		}
	}
	seed, published, err := t.publishedAttempt(selected.RunID)
	if err != nil {
		return genrunterminal.UnstartedExecutionTerminalResult{}, err
	}
	if published && seed.seed.Declaration.PredecessorRunID != "" {
		d := seed.seed.Declaration
		predecessor, exists, err := t.loadRun(d.PredecessorRunID)
		if err != nil {
			return genrunterminal.UnstartedExecutionTerminalResult{}, err
		}
		if !exists || predecessor.meta.Status != session.RunStatusSuspended || predecessor.meta.SessionID != sessionID ||
			!reflect.DeepEqual(predecessor.meta.Subject, *selected.Subject) || !reflect.DeepEqual(predecessor.meta.Origin, *selected.Origin) {
			return genrunterminal.UnstartedExecutionTerminalResult{}, session.ErrRunConflict
		}
		// An execution that never started owns only its failure or
		// cancellation. Only a Run start can transfer the predecessor's saved
		// work to a successor.
	}
	stored, err := genrunterminal.EncodeUnstartedExecutionTerminal(selected)
	if err != nil {
		return genrunterminal.UnstartedExecutionTerminalResult{}, err
	}
	if _, err := t.exec(`INSERT INTO unstarted_terminals (run_id, session_id, encoded) VALUES (?, ?, ?)`,
		selected.RunID, sessionID, stored); err != nil {
		return genrunterminal.UnstartedExecutionTerminalResult{}, err
	}
	return genrunterminal.UnstartedExecutionTerminalResult{Result: genrunterminal.NewUnstartedExecutionTerminalDecisionStored(selected)}, nil
}
