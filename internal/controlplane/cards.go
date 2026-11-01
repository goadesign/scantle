package controlplane

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
	// exclusive lock on it until close. Callers serialize read-check-write
	// sequences with ControlPlane.mu.
	cardStore struct {
		// dir holds one <hex sha256(change_id)>.json file per card.
		dir string
		// lock is the open lock file whose flock keeps other processes out.
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
		_ = lock.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("cards directory %s is in use by another process", dir)
		}
		return nil, fmt.Errorf("lock cards directory: %w", err)
	}
	return &cardStore{dir: dir, lock: lock}, nil
}

// close releases the directory lock.
func (s *cardStore) close() error {
	return s.lock.Close()
}

// load returns the saved card for changeID, or false when none was saved. A
// card file that cannot be read or decoded is an error.
func (s *cardStore) load(changeID string) (storedChangeCard, bool, error) {
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
	if dec.More() {
		return storedChangeCard{}, false, fmt.Errorf("decode change card %q: trailing data", changeID)
	}
	if f.Card == nil || f.Card.ChangeID != changeID || f.Owner == "" {
		return storedChangeCard{}, false, fmt.Errorf("change card file for %q does not hold that card", changeID)
	}
	return storedChangeCard{card: f.Card, owner: f.Owner, designSource: f.DesignSource}, true, nil
}

// save replaces the saved card for record.card.ChangeID. It writes and syncs
// a temporary file in the same directory and renames it over the old one, so
// a failed save leaves the previous card in place.
func (s *cardStore) save(record storedChangeCard) error {
	data, err := json.Marshal(cardFile{Card: record.card, Owner: record.owner, DesignSource: record.designSource})
	if err != nil {
		return fmt.Errorf("encode change card %q: %w", record.card.ChangeID, err)
	}
	tmp, err := os.CreateTemp(s.dir, "card-*.tmp")
	if err != nil {
		return fmt.Errorf("save change card %q: %w", record.card.ChangeID, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), s.path(record.card.ChangeID))
	}
	if err == nil {
		err = syncDir(s.dir)
	}
	if err != nil {
		return fmt.Errorf("save change card %q: %w", record.card.ChangeID, err)
	}
	return nil
}

// path names changeID's card file. Hashing the ID keeps any change_id inside
// the directory.
func (s *cardStore) path(changeID string) string {
	sum := sha256.Sum256([]byte(changeID))
	return filepath.Join(s.dir, hex.EncodeToString(sum[:])+".json")
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
