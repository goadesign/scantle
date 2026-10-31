package designeval

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/mod/modfile"
)

// reservedDir is the snapshot directory the evaluator owns; snapshots must not
// use it.
const reservedDir = "surla_designeval"

// validateSnapshot checks a design snapshot and returns its parsed go.mod.
//
// A snapshot is the exact files of one Go module: every path is clean,
// slash-separated and relative to the module root, names a .go file, the root
// go.mod or the root go.sum, and stays out of the reserved evaluator
// directory. go.mod, go.sum and at least one design/*.go file are required.
// go.mod must declare a module path and may not replace a module with a local
// directory, because the snapshot is the only source the evaluator reads.
func validateSnapshot(files map[string]string) (*modfile.File, error) {
	designFiles := 0
	for _, name := range slices.Sorted(maps.Keys(files)) {
		if err := validatePath(name); err != nil {
			return nil, err
		}
		if path.Dir(name) == "design" && strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") {
			designFiles++
		}
	}
	for _, required := range []string{"go.mod", "go.sum"} {
		if _, ok := files[required]; !ok {
			return nil, fmt.Errorf("design snapshot has no %s", required)
		}
	}
	if designFiles == 0 {
		return nil, errors.New("design snapshot has no design/*.go file")
	}
	mod, err := modfile.Parse("go.mod", []byte(files["go.mod"]), nil)
	if err != nil {
		return nil, fmt.Errorf("parse snapshot go.mod: %w", err)
	}
	if mod.Module == nil || mod.Module.Mod.Path == "" {
		return nil, errors.New("snapshot go.mod declares no module path")
	}
	for _, r := range mod.Replace {
		if r.New.Version == "" {
			return nil, fmt.Errorf("snapshot go.mod replaces %s with local directory %q; only versioned replacements are supported",
				r.Old.Path, r.New.Path)
		}
	}
	return mod, nil
}

// validatePath enforces the snapshot path rules for one file.
func validatePath(name string) error {
	switch {
	case name == "" || path.IsAbs(name) || strings.Contains(name, `\`) || path.Clean(name) != name:
		return fmt.Errorf("snapshot path %q is not a clean relative path", name)
	case name == ".." || strings.HasPrefix(name, "../"):
		return fmt.Errorf("snapshot path %q leaves the module", name)
	case name == reservedDir || strings.HasPrefix(name, reservedDir+"/"):
		return fmt.Errorf("snapshot path %q uses the evaluator's reserved directory", name)
	case name == "go.mod" || name == "go.sum":
		return nil
	case path.Base(name) == "go.mod" || path.Base(name) == "go.sum":
		return fmt.Errorf("snapshot path %q is a nested module file; a snapshot holds one module", name)
	case !strings.HasSuffix(name, ".go"):
		return fmt.Errorf("snapshot path %q is not a .go, go.mod or go.sum file", name)
	}
	return nil
}

// stageSnapshot writes exactly the snapshot's bytes under dir, which must be
// a new empty directory. Each file is created exclusively, so nothing that
// already exists, such as a symbolic link, is followed or overwritten; no
// host path named by the snapshot is ever read.
func stageSnapshot(dir string, files map[string]string) error {
	for _, name := range slices.Sorted(maps.Keys(files)) {
		dst := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
			return fmt.Errorf("stage %s: %w", name, err)
		}
		f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return fmt.Errorf("stage %s: %w", name, err)
		}
		_, writeErr := f.WriteString(files[name])
		if closeErr := f.Close(); writeErr != nil || closeErr != nil {
			return fmt.Errorf("stage %s: %w", name, errors.Join(writeErr, closeErr))
		}
	}
	return nil
}

// SnapshotDigest returns the SHA-256 of a snapshot's canonical encoding: each
// path and its content in path order, each length-prefixed, so two snapshots
// have the same digest only when they hold exactly the same files.
func SnapshotDigest(files map[string]string) string {
	h := sha256.New()
	for _, name := range slices.Sorted(maps.Keys(files)) {
		for _, part := range []string{name, files[name]} {
			_, _ = fmt.Fprintf(h, "%d:", len(part))
			_, _ = io.WriteString(h, part)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}
