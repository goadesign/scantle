// Package gitcapture captures read-only working-tree snapshots of a Git
// repository without mutating the user's checkout or index.
package gitcapture

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

type (
	//UntrackedFile holds the relative path and raw bytes of an untracked file in
	// a working tree.
	UntrackedFile struct {
		// Path is the slash-normalized repository-relative file path.
		Path string
		// Content is the exact file bytes observed at capture time.
		Content []byte
	}

	// Snapshot represents an immutable, read-only capture of a Git checkout.
	Snapshot struct {
		// HeadCommit is the resolved HEAD commit SHA.
		HeadCommit string
		// TrackedPatch is the output of `git diff --binary HEAD`.
		TrackedPatch []byte
		// Untracked lists untracked non-ignored files in deterministic path order.
		Untracked []UntrackedFile
		// TreeDigest is the SHA-256 hex digest over HeadCommit, TrackedPatch, and Untracked.
		TreeDigest string
	}
)

// Capture inspects repoDir using read-only Git commands and returns a Snapshot
// of HEAD, tracked modifications, and untracked files without modifying the
// working tree or Git index.
func Capture(repoDir string) (Snapshot, error) {
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
		abs := filepath.Join(repoDir, filepath.FromSlash(rel))
		data, err := os.ReadFile(abs)
		if err != nil {
			return Snapshot{}, fmt.Errorf("read untracked file %q: %w", rel, err)
		}
		untracked = append(untracked, UntrackedFile{
			Path:    rel,
			Content: data,
		})
	}

	return Snapshot{
		HeadCommit:   head,
		TrackedPatch: patch,
		Untracked:    untracked,
		TreeDigest:   computeDigest(head, patch, untracked),
	}, nil
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
// into a deterministic SHA-256 hex digest.
func computeDigest(head string, patch []byte, untracked []UntrackedFile) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "head:%s\n", head)
	_, _ = fmt.Fprintf(h, "patch:%d\n", len(patch))
	_, _ = h.Write(patch)
	for _, u := range untracked {
		_, _ = fmt.Fprintf(h, "\nuntracked:%s:%d\n", u.Path, len(u.Content))
		_, _ = h.Write(u.Content)
	}
	return hex.EncodeToString(h.Sum(nil))
}
