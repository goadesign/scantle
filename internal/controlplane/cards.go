// This file holds the control plane's Change Card store: one file per card
// under a directory that one process at a time may use.

package controlplane

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	gensurla "github.com/goadesign/surla/gen/surla"
)

type (
	// cardStore keeps every CP1 Change Card in its own file under one
	// directory, so cards outlive the process that wrote them. The files are
	// the only copy: each read decodes the file and each change replaces it.
	// One process at a time may use a directory; openCardStore holds an
	// exclusive lock on it until close. Callers hold ControlPlane.mu for
	// every call: the read lock for loads, the write lock for saves and close.
	cardStore struct {
		// dir holds one <hex sha256(change_id)>.json file per card.
		dir string
		// lock is the open lock file whose flock keeps other processes out.
		// close sets it to nil; the store then refuses to read or write, so
		// a request still running when the lock is released cannot write to
		// a directory another process may now hold.
		lock *os.File
	}

	// cardFile is the saved form of one card. Changing gensurla's
	// ChangeCardResult fields changes this format; saved cards that no longer
	// decode stop the calls that read them rather than being skipped.
	cardFile struct {
		// Card is the latest versioned Change Card.
		Card *gensurla.ChangeCardResult `json:"card"`
		// Owner is the authenticated principal that created the card.
		Owner string `json:"owner"`
		// DesignSource is the Goa design source inspected at CP1.
		DesignSource string `json:"design_source"`
	}
)

// stagingFile is the name, inside a cards directory, of the file each save
// writes before renaming it over the card's file. Card files end in ".json",
// so it is never read as a card.
const stagingFile = "pending"

// errCardsClosed reports a request that reached the card store after the
// control plane was closed.
var errCardsClosed = errors.New("control plane is closed")

// openCardStore creates dir (0700) when missing and takes its lock. It fails
// when another process already holds the lock.
func openCardStore(dir string) (*cardStore, error) {
	if dir == "" {
		return nil, errors.New("cards directory is required")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create cards directory: %w", err)
	}
	lock, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open cards directory lock: %w", err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			err = fmt.Errorf("cards directory %s is in use by another process", dir)
		} else {
			err = fmt.Errorf("lock cards directory: %w", err)
		}
		return nil, errors.Join(err, lock.Close())
	}
	return &cardStore{dir: dir, lock: lock}, nil
}

// syncDir flushes dir's entries so a completed rename survives a crash.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = d.Sync()
	return errors.Join(err, d.Close())
}

// close releases the directory lock. Closing a closed store does nothing.
func (s *cardStore) close() error {
	if s.lock == nil {
		return nil
	}
	err := s.lock.Close()
	s.lock = nil
	return err
}

// load returns the saved card for changeID, or false when none was saved. A
// card file that cannot be read or decoded is an error.
func (s *cardStore) load(changeID string) (storedChangeCard, bool, error) {
	if s.lock == nil {
		return storedChangeCard{}, false, errCardsClosed
	}
	data, err := os.ReadFile(s.path(changeID))
	if errors.Is(err, fs.ErrNotExist) {
		return storedChangeCard{}, false, nil
	}
	if err != nil {
		return storedChangeCard{}, false, fmt.Errorf("read change card %q: %w", changeID, err)
	}
	var f cardFile
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return storedChangeCard{}, false, fmt.Errorf("decode change card %q: %w", changeID, err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return storedChangeCard{}, false, fmt.Errorf("decode change card %q: trailing data", changeID)
	}
	if f.Card == nil || f.Card.ChangeID != changeID || f.Owner == "" {
		return storedChangeCard{}, false, fmt.Errorf("change card file for %q does not hold that card", changeID)
	}
	if f.Card.Status != StatusNeedsConfirmation && f.Card.Status != StatusConfirmed {
		return storedChangeCard{}, false, fmt.Errorf("change card %q has unknown status %q", changeID, f.Card.Status)
	}
	// request_revision is the digest of the card's content, owner and design
	// source, so recomputing it checks every field it covers.
	rev, err := computeCanonicalRevision(f.Card, f.Owner, f.DesignSource)
	if err != nil {
		return storedChangeCard{}, false, err
	}
	if rev != f.Card.RequestRevision {
		return storedChangeCard{}, false, fmt.Errorf("change card %q does not match its request_revision", changeID)
	}
	return storedChangeCard{card: f.Card, owner: f.Owner, designSource: f.DesignSource}, true, nil
}

// save replaces the saved card for record.card.ChangeID. It writes and syncs
// the card to the directory's one staging file, then renames that over the
// card's file. Only one save runs at a time in a directory (the directory
// lock and ControlPlane.mu), so one staging file suffices; a save that
// crashed leaves it behind, unread, and the next save overwrites it. An
// error before the rename leaves the previous card in place. The rename
// publishes the new card; an error syncing the directory after it means the
// new card is in place but may not survive a crash, and the error says so.
func (s *cardStore) save(record storedChangeCard) error {
	if s.lock == nil {
		return errCardsClosed
	}
	data, err := json.Marshal(cardFile{Card: record.card, Owner: record.owner, DesignSource: record.designSource})
	if err != nil {
		return fmt.Errorf("encode change card %q: %w", record.card.ChangeID, err)
	}
	staging := filepath.Join(s.dir, stagingFile)
	f, err := os.OpenFile(staging, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err == nil {
		_, err = f.Write(data)
		if err == nil {
			err = f.Sync()
		}
		err = errors.Join(err, f.Close())
	}
	if err == nil {
		err = os.Rename(staging, s.path(record.card.ChangeID))
	}
	if err != nil {
		return fmt.Errorf("save change card %q: %w", record.card.ChangeID, err)
	}
	if err := syncDir(s.dir); err != nil {
		return fmt.Errorf("change card %q is saved but may not survive a crash: %w", record.card.ChangeID, err)
	}
	return nil
}

// path names changeID's card file. Hashing the ID keeps any change_id inside
// the directory.
func (s *cardStore) path(changeID string) string {
	sum := sha256.Sum256([]byte(changeID))
	return filepath.Join(s.dir, hex.EncodeToString(sum[:])+".json")
}
