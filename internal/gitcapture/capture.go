// Package gitcapture captures read-only working-tree snapshots of a Git
// repository and materializes them into isolated verification clones without
// mutating the user's checkout or index.
package gitcapture

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

type (
	// UntrackedFile holds the relative path, mode bits, symlink target, and raw
	// bytes of an untracked entry in a working tree.
	UntrackedFile struct {
		// Path is the slash-normalized repository-relative file path.
		Path string
		// Mode is the file permission bits (or fs.ModeSymlink for symbolic links).
		Mode fs.FileMode
		// IsSymlink is true when the untracked entry is a symbolic link.
		IsSymlink bool
		// LinkTarget is the symbolic link target path when IsSymlink is true.
		LinkTarget string
		// Content is the exact file bytes observed at capture time for regular files.
		Content []byte
	}

	// Snapshot represents an immutable, read-only capture of a Git checkout.
	Snapshot struct {
		// HeadCommit is the resolved HEAD commit SHA.
		HeadCommit string
		// TrackedPatch is the output of `git diff --binary HEAD`.
		TrackedPatch []byte
		// Untracked lists captured untracked non-ignored entries in deterministic path order.
		Untracked []UntrackedFile
		// TreeDigest is the SHA-256 hex digest over HeadCommit, TrackedPatch, and Untracked.
		TreeDigest string
	}
)

// Capture inspects repoDir using read-only Git commands and returns a Snapshot
// of HEAD, tracked modifications, and untracked files without modifying the
// working tree or Git index. When selectedPaths is non-empty, only untracked
// files matching or nested under those repository-relative paths are captured.
func Capture(repoDir string, selectedPaths ...string) (Snapshot, error) {
	normFilters, err := normalizeSelectedPaths(selectedPaths)
	if err != nil {
		return Snapshot{}, err
	}

	headBytes, err := runGitReadOnly(repoDir, "rev-parse", "HEAD")
	if err != nil {
		return Snapshot{}, fmt.Errorf("resolve HEAD: %w", err)
	}
	head := strings.TrimSpace(string(headBytes))

	patch, err := runGitReadOnly(repoDir, "diff", "--binary", "HEAD")
	if err != nil {
		return Snapshot{}, fmt.Errorf("diff tracked files: %w", err)
	}

	untrackedRaw, err := runGitReadOnly(repoDir, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return Snapshot{}, fmt.Errorf("list untracked files: %w", err)
	}

	relPaths := parseNullTerminatedPaths(untrackedRaw)
	untracked := make([]UntrackedFile, 0, len(relPaths))
	for _, rel := range relPaths {
		if !matchesSelectedPaths(rel, normFilters) {
			continue
		}
		abs := filepath.Join(repoDir, filepath.FromSlash(rel))
		info, err := os.Lstat(abs)
		if err != nil {
			return Snapshot{}, fmt.Errorf("lstat untracked path %q: %w", rel, err)
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(abs)
			if err != nil {
				return Snapshot{}, fmt.Errorf("readlink untracked symlink %q: %w", rel, err)
			}
			untracked = append(untracked, UntrackedFile{
				Path:       rel,
				Mode:       fs.ModeSymlink,
				IsSymlink:  true,
				LinkTarget: target,
			})
		case info.Mode().IsRegular():
			data, err := os.ReadFile(abs)
			if err != nil {
				return Snapshot{}, fmt.Errorf("read untracked file %q: %w", rel, err)
			}
			untracked = append(untracked, UntrackedFile{
				Path:    rel,
				Mode:    info.Mode().Perm(),
				Content: data,
			})
		default:
			return Snapshot{}, fmt.Errorf("unsupported untracked file mode %v for %q", info.Mode(), rel)
		}
	}

	return Snapshot{
		HeadCommit:   head,
		TrackedPatch: patch,
		Untracked:    untracked,
		TreeDigest:   computeDigest(head, patch, untracked),
	}, nil
}

// ApplyToClone materializes snap into a fresh isolated Git clone at dstCloneDir
// cloned from srcRepoDir at snap.HeadCommit without mutating srcRepoDir.
func ApplyToClone(srcRepoDir, dstCloneDir string, snap Snapshot) error {
	if strings.TrimSpace(srcRepoDir) == "" || strings.TrimSpace(dstCloneDir) == "" {
		return errors.New("source repository and destination clone directories are required")
	}
	if strings.TrimSpace(snap.HeadCommit) == "" {
		return errors.New("snapshot HeadCommit is required")
	}
	expectedDigest := computeDigest(snap.HeadCommit, snap.TrackedPatch, snap.Untracked)
	if snap.TreeDigest != expectedDigest {
		return fmt.Errorf("snapshot TreeDigest mismatch: got %q, computed %q", snap.TreeDigest, expectedDigest)
	}

	if err := runGitCommand(srcRepoDir, nil, "clone", "--no-local", "--no-checkout", srcRepoDir, dstCloneDir); err != nil {
		return fmt.Errorf("clone repository to %q: %w", dstCloneDir, err)
	}
	if err := runGitCommand(dstCloneDir, nil, "checkout", "--detach", snap.HeadCommit); err != nil {
		return fmt.Errorf("checkout head commit %q: %w", snap.HeadCommit, err)
	}
	if len(snap.TrackedPatch) > 0 {
		if err := runGitCommand(dstCloneDir, snap.TrackedPatch, "apply", "--binary", "--whitespace=nowarn", "-"); err != nil {
			return fmt.Errorf("apply tracked patch: %w", err)
		}
	}

	for _, u := range snap.Untracked {
		cleanRel, err := validateRelativePath(u.Path)
		if err != nil {
			return err
		}
		dstPath := filepath.Join(dstCloneDir, filepath.FromSlash(cleanRel))
		if err := os.MkdirAll(filepath.Dir(dstPath), 0o750); err != nil {
			return fmt.Errorf("mkdir parent for %q: %w", cleanRel, err)
		}
		if u.IsSymlink {
			if err := os.Symlink(u.LinkTarget, dstPath); err != nil {
				return fmt.Errorf("create untracked symlink %q: %w", cleanRel, err)
			}
			continue
		}
		perm := u.Mode.Perm()
		if perm == 0 {
			perm = 0o600
		}
		if err := os.WriteFile(dstPath, u.Content, perm); err != nil {
			return fmt.Errorf("write untracked file %q: %w", cleanRel, err)
		}
		if err := os.Chmod(dstPath, perm); err != nil {
			return fmt.Errorf("chmod untracked file %q: %w", cleanRel, err)
		}
	}
	return nil
}

// runGitReadOnly executes a Git subcommand with --no-optional-locks so the
// repository index and working tree are never touched.
func runGitReadOnly(repoDir string, args ...string) ([]byte, error) {
	fullArgs := append([]string{"--no-optional-locks"}, args...)
	cmd := exec.Command("git", fullArgs...)
	cmd.Dir = repoDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// runGitCommand executes a Git command inside dir with optional stdin bytes.
func runGitCommand(dir string, stdin []byte, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if len(stdin) > 0 {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// normalizeSelectedPaths validates and normalizes repository-relative path filters.
func normalizeSelectedPaths(selected []string) ([]string, error) {
	if len(selected) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(selected))
	for _, raw := range selected {
		clean, err := validateRelativePath(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, clean)
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

// validateRelativePath verifies that raw is a clean, non-empty repository-relative slash path.
func validateRelativePath(raw string) (string, error) {
	slashed := strings.TrimSpace(filepath.ToSlash(raw))
	if slashed == "" || strings.HasPrefix(slashed, "/") {
		return "", fmt.Errorf("invalid repository-relative path %q", raw)
	}
	clean := path.Clean(slashed)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("path %q escapes repository root", raw)
	}
	return clean, nil
}

// matchesSelectedPaths reports whether rel matches one of filters (or any path when filters is empty).
func matchesSelectedPaths(rel string, filters []string) bool {
	if len(filters) == 0 {
		return true
	}
	for _, f := range filters {
		if rel == f || strings.HasPrefix(rel, f+"/") {
			return true
		}
	}
	return false
}

// parseNullTerminatedPaths splits `-z` Git output into sorted slash paths.
func parseNullTerminatedPaths(raw []byte) []string {
	if len(raw) == 0 {
		return nil
	}
	parts := bytes.Split(raw, []byte{0})
	paths := make([]string, 0, len(parts))
	for _, p := range parts {
		if len(p) == 0 {
			continue
		}
		paths = append(paths, filepath.ToSlash(string(p)))
	}
	slices.Sort(paths)
	return paths
}

// computeDigest hashes the HEAD commit, tracked diff patch, and untracked files
// (including file mode bits and symlink targets) into a deterministic SHA-256 hex digest.
func computeDigest(head string, patch []byte, untracked []UntrackedFile) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "head:%s\n", head)
	_, _ = fmt.Fprintf(h, "patch:%d\n", len(patch))
	_, _ = h.Write(patch)
	for _, u := range untracked {
		_, _ = fmt.Fprintf(h, "\nuntracked:%s:mode=%#o:symlink=%t:target=%s:len=%d\n", u.Path, u.Mode, u.IsSymlink, u.LinkTarget, len(u.Content))
		_, _ = h.Write(u.Content)
	}
	return hex.EncodeToString(h.Sum(nil))
}
