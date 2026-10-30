// Package runstore provides the orders example's durable goa-ai runtime store.
//
// Store implements storage.Store and the host-owned session operations on one
// SQLite database file. Every method runs in one SQLite transaction: mutations
// begin with BEGIN IMMEDIATE, so the database write lock is held from the first
// invariant check until commit, and reads use a deferred read transaction that
// observes one committed snapshot. Several Store handles, including handles in
// different processes, may share one file; SQLite serializes their writes.
//
// The semantics match goa.design/goa-ai/runtime/agent/storage/inmem exactly:
// each method ports the reference implementation's checks and writes into the
// transaction, and unique keys and conditional updates enforce single-winner
// rules (record keys, seed positions, one publication per run, one successor
// per suspension) inside that same transaction.
package runstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"

	// The pure-Go SQLite driver registers itself as "sqlite".
	_ "modernc.org/sqlite"
)

type (
	// Store is the durable runtime store and host session repository for the
	// orders example. It is safe for concurrent use. Close releases the
	// database handle; the Store must not be used afterwards.
	Store struct {
		// db is the connection pool for the database file. Its connections begin
		// write transactions with BEGIN IMMEDIATE and read-only transactions
		// with a deferred BEGIN.
		db *sql.DB
	}

	// dbError marks an error returned by SQLite or database/sql, including
	// context cancellation observed by the driver. Store methods return it
	// without storage.ContractError because retrying the command may succeed.
	dbError struct {
		// err is the driver or database/sql error with operation context.
		err error
	}

	// txn is one open SQLite transaction plus the context that bounds its
	// statements. Store methods create it and pass it to the row helpers.
	txn struct {
		// ctx bounds every statement in the transaction.
		ctx context.Context
		// tx is the open SQLite transaction.
		tx *sql.Tx
	}
)

const (
	// schemaVersion is the PRAGMA user_version written by the current schema.
	schemaVersion = 1
	// busyTimeoutMillis is how long a connection waits for another writer to
	// release the database lock before SQLite reports SQLITE_BUSY.
	busyTimeoutMillis = 30_000
)

// Store must satisfy the worker-facing runtime store contract.
var _ storage.Store = (*Store)(nil)

// Open opens or creates the SQLite database at path, enables WAL journaling
// and a busy timeout, and creates the schema when the file is new. Opening an
// existing file with the current schema leaves its data unchanged. A file
// written by an unknown schema version is rejected.
func Open(ctx context.Context, path string) (*Store, error) {
	if path == "" || strings.ContainsAny(path, "?#") {
		return nil, fmt.Errorf("runstore: invalid database path %q", path)
	}
	query := url.Values{}
	query.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", busyTimeoutMillis))
	query.Add("_pragma", "journal_mode(WAL)")
	query.Add("_pragma", "synchronous(FULL)")
	query.Add("_pragma", "foreign_keys(1)")
	query.Set("_txlock", "immediate")
	db, err := sql.Open("sqlite", path+"?"+query.Encode())
	if err != nil {
		return nil, fmt.Errorf("runstore: open %s: %w", path, err)
	}
	store := &Store{db: db}
	if err := store.migrate(ctx); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return store, nil
}

// Close closes the database handle.
func (s *Store) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("runstore: close: %w", err)
	}
	return nil
}

// CreateSession creates one active host-owned session. Repeating the creation
// of an active session returns it unchanged; an ended session returns
// session.ErrSessionEnded and a purged ID returns session.ErrSessionPurged.
func (s *Store) CreateSession(ctx context.Context, sessionID string, createdAt time.Time) (session.Session, error) {
	if sessionID == "" {
		return session.Session{}, errors.New("session id is required")
	}
	if createdAt.IsZero() {
		return session.Session{}, errors.New("created_at is required")
	}
	var result session.Session
	err := s.write(ctx, func(t *txn) error {
		purged, err := t.isPurged(sessionID)
		if err != nil {
			return err
		}
		if purged {
			return session.ErrSessionPurged
		}
		existing, found, err := t.loadSession(sessionID)
		if err != nil {
			return err
		}
		if found {
			if existing.Status == session.StatusEnded {
				return session.ErrSessionEnded
			}
			result = existing
			return nil
		}
		result = session.Session{ID: sessionID, Status: session.StatusActive, CreatedAt: createdAt.UTC()}
		return t.insertSession(result)
	})
	return result, err
}

// LoadSession returns one host-owned session.
func (s *Store) LoadSession(ctx context.Context, sessionID string) (session.Session, error) {
	var result session.Session
	err := s.read(ctx, func(t *txn) error {
		purged, err := t.isPurged(sessionID)
		if err != nil {
			return err
		}
		if purged {
			return session.ErrSessionPurged
		}
		stored, found, err := t.loadSession(sessionID)
		if err != nil {
			return err
		}
		if !found {
			return session.ErrSessionNotFound
		}
		result = stored
		return nil
	})
	return result, err
}

// LoadSessionStatus returns whether the host-owned Session is active or ended.
func (s *Store) LoadSessionStatus(ctx context.Context, sessionID string) (session.SessionStatus, error) {
	stored, err := s.LoadSession(ctx, sessionID)
	if err != nil {
		return "", err
	}
	return stored.Status, nil
}

// EndSession prevents future workflows from proceeding. Existing workflows may
// still store terminal records. Ending an ended session returns it unchanged.
func (s *Store) EndSession(ctx context.Context, sessionID string, endedAt time.Time) (session.Session, error) {
	if endedAt.IsZero() {
		return session.Session{}, errors.New("ended_at is required")
	}
	var result session.Session
	err := s.write(ctx, func(t *txn) error {
		purged, err := t.isPurged(sessionID)
		if err != nil {
			return err
		}
		if purged {
			return session.ErrSessionPurged
		}
		stored, found, err := t.loadSession(sessionID)
		if err != nil {
			return err
		}
		if !found {
			return session.ErrSessionNotFound
		}
		if stored.Status == session.StatusEnded {
			result = stored
			return nil
		}
		at := endedAt.UTC()
		stored.Status = session.StatusEnded
		stored.EndedAt = &at
		result = stored
		return t.endSession(sessionID, at)
	})
	return result, err
}

// PurgeSession permanently seals an ended session and removes its runs,
// checkpoints, records, preparations, and early terminal outcomes. The session
// ID can never be created again. Purging an already purged session succeeds.
func (s *Store) PurgeSession(ctx context.Context, sessionID string) error {
	return s.write(ctx, func(t *txn) error {
		purged, err := t.isPurged(sessionID)
		if err != nil {
			return err
		}
		if purged {
			return nil
		}
		stored, found, err := t.loadSession(sessionID)
		if err != nil {
			return err
		}
		if !found {
			return session.ErrSessionNotFound
		}
		if stored.Status == session.StatusActive {
			return session.ErrSessionActive
		}
		active, err := t.sessionHasUnresolvedRuns(sessionID)
		if err != nil {
			return err
		}
		if active {
			return session.ErrSessionHasActiveRuns
		}
		return t.purgeSession(sessionID)
	})
}

// ListRunsBySession returns the session's run metadata in creation order,
// limited to statuses when that list is nonempty.
func (s *Store) ListRunsBySession(ctx context.Context, sessionID string, statuses []session.RunStatus) ([]session.RunMeta, error) {
	allowed := make(map[session.RunStatus]struct{}, len(statuses))
	for _, status := range statuses {
		allowed[status] = struct{}{}
	}
	result := make([]session.RunMeta, 0)
	err := s.read(ctx, func(t *txn) error {
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
		rows, err := t.sessionRuns(sessionID)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if len(allowed) > 0 {
				if _, ok := allowed[row.meta.Status]; !ok {
					continue
				}
			}
			result = append(result, row.meta)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// Error implements error.
func (e *dbError) Error() string {
	return e.err.Error()
}

// Unwrap returns the driver or database/sql error.
func (e *dbError) Unwrap() error {
	return e.err
}

// contractResult marks every rejection decided by Store logic as a permanent
// storage.ContractError, matching the reference store. Database failures and
// context cancellation stay unwrapped so activity runners may retry them.
func contractResult[T any](result T, err error) (T, error) {
	if err == nil {
		return result, nil
	}
	if _, ok := errors.AsType[*dbError](err); ok {
		// Keep a rejection joined with a rollback failure inspectable.
		return result, err
	}
	return result, storage.NewContractError(err)
}

// contractError is contractResult for methods that return only an error.
func contractError(err error) error {
	_, err = contractResult(struct{}{}, err)
	return err
}

// write runs fn in one BEGIN IMMEDIATE transaction and commits only when fn
// succeeds. Any error from fn rolls back every statement fn executed, so a
// rejected command never leaves partial state.
func (s *Store) write(ctx context.Context, fn func(*txn) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return &dbError{err: fmt.Errorf("runstore: begin write: %w", err)}
	}
	if err := fn(&txn{ctx: ctx, tx: tx}); err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			return errors.Join(err, &dbError{err: fmt.Errorf("runstore: rollback: %w", rollbackErr)})
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return &dbError{err: fmt.Errorf("runstore: commit: %w", err)}
	}
	return nil
}

// read runs fn in one deferred read-only transaction, so every statement in
// fn observes the same committed snapshot.
func (s *Store) read(ctx context.Context, fn func(*txn) error) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return &dbError{err: fmt.Errorf("runstore: begin read: %w", err)}
	}
	fnErr := fn(&txn{ctx: ctx, tx: tx})
	if err := tx.Rollback(); err != nil {
		return errors.Join(fnErr, &dbError{err: fmt.Errorf("runstore: end read: %w", err)})
	}
	return fnErr
}

// migrate creates the schema in a new file and checks the version of an
// existing one. It runs in a write transaction, so concurrent opens of the
// same new file create the schema once.
func (s *Store) migrate(ctx context.Context) error {
	return s.write(ctx, func(t *txn) error {
		var version int
		if err := t.tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
			return &dbError{err: fmt.Errorf("runstore: read schema version: %w", err)}
		}
		switch version {
		case schemaVersion:
			return nil
		case 0:
			for _, statement := range schema {
				if _, err := t.tx.ExecContext(ctx, statement); err != nil {
					return &dbError{err: fmt.Errorf("runstore: create schema: %w", err)}
				}
			}
			if _, err := t.tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
				return &dbError{err: fmt.Errorf("runstore: write schema version: %w", err)}
			}
			return nil
		default:
			return fmt.Errorf("runstore: unsupported schema version %d, want %d", version, schemaVersion)
		}
	})
}
