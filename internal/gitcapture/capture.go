// Package gitcapture captures read-only working-tree snapshots of a Git
// repository and materializes them into isolated verification clones without
// mutating the user's checkout or index.
package gitcapture

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
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
		// TrackedPatch is the output of `git diff --binary --no-ext-diff --no-textconv <head>`.
		TrackedPatch []byte
		// Untracked lists captured untracked non-ignored entries in deterministic path order.
		Untracked []UntrackedFile
		// TreeDigest is the SHA-256 hex digest over HeadCommit, TrackedPatch, and Untracked.
		TreeDigest string
	}
)

// Capture inspects repoDir using read-only Git commands and returns a Snapshot
// of HEAD, tracked modifications against that exact resolved commit, and
// explicitly selected untracked files without modifying the working tree or Git
// index. When selectedPaths is empty, no untracked files are captured.
func Capture(ctx context.Context, repoDir string, selectedPaths ...string) (Snapshot, error) {
	normFilters, err := normalizeSelectedPaths(selectedPaths)
	if err != nil {
		return Snapshot{}, err
	}

	headBytes, err := runGitReadOnly(ctx, repoDir, "rev-parse", "HEAD")
	if err != nil {
		return Snapshot{}, fmt.Errorf("resolve HEAD: %w", err)
	}
	head := strings.TrimSpace(string(headBytes))

	patch, err := runGitReadOnly(
		ctx,
		repoDir,
		"-c",
		"diff.noprefix=false",
		"diff",
		"--no-color",
		"--binary",
		"--no-ext-diff",
		"--no-textconv",
		"--src-prefix=a/",
		"--dst-prefix=b/",
		head,
	)
	if err != nil {
		return Snapshot{}, fmt.Errorf("diff tracked files: %w", err)
	}

	var untracked []UntrackedFile
	if len(normFilters) > 0 {
		untrackedRaw, err := runGitReadOnly(ctx, repoDir, "ls-files", "--others", "--exclude-standard", "-z")
		if err != nil {
			return Snapshot{}, fmt.Errorf("list untracked files: %w", err)
		}
		relPaths := parseNullTerminatedPaths(untrackedRaw)
		untracked = make([]UntrackedFile, 0, len(relPaths))
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
func ApplyToClone(ctx context.Context, srcRepoDir, dstCloneDir string, snap Snapshot) error {
	if strings.TrimSpace(srcRepoDir) == "" || strings.TrimSpace(dstCloneDir) == "" {
		return errors.New("source repository and destination clone directories are required")
	}
	if strings.TrimSpace(snap.HeadCommit) == "" {
		return errors.New("snapshot HeadCommit is required")
	}
	if err := validateDisjointCloneDestination(srcRepoDir, dstCloneDir); err != nil {
		return err
	}
	expectedDigest := computeDigest(snap.HeadCommit, snap.TrackedPatch, snap.Untracked)
	if snap.TreeDigest != expectedDigest {
		return fmt.Errorf("snapshot TreeDigest mismatch: got %q, computed %q", snap.TreeDigest, expectedDigest)
	}

	if err := runGitCommand(ctx, srcRepoDir, nil, "clone", "--no-local", "--no-checkout", "--template=", srcRepoDir, dstCloneDir); err != nil {
		return fmt.Errorf("clone repository to %q: %w", dstCloneDir, err)
	}
	if err := runGitCommand(ctx, dstCloneDir, nil, "checkout", "--detach", snap.HeadCommit); err != nil {
		return fmt.Errorf("checkout head commit %q: %w", snap.HeadCommit, err)
	}
	if len(snap.TrackedPatch) > 0 {
		if err := runGitCommand(ctx, dstCloneDir, snap.TrackedPatch, "apply", "--binary", "--whitespace=nowarn", "-"); err != nil {
			return fmt.Errorf("apply tracked patch: %w", err)
		}
	}

	for _, u := range snap.Untracked {
		exactRel, err := validateRelativePath(u.Path)
		if err != nil {
			return err
		}
		dstPath, err := prepareUntrackedDestination(dstCloneDir, exactRel)
		if err != nil {
			return err
		}
		if u.IsSymlink {
			if err := os.Symlink(u.LinkTarget, dstPath); err != nil {
				return fmt.Errorf("create untracked symlink %q: %w", exactRel, err)
			}
			continue
		}
		perm := u.Mode.Perm()
		if perm == 0 {
			perm = 0o600
		}
		f, err := os.OpenFile(dstPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
		if err != nil {
			return fmt.Errorf("create untracked file %q: %w", exactRel, err)
		}
		_, writeErr := f.Write(u.Content)
		closeErr := f.Close()
		if writeErr != nil {
			return fmt.Errorf("write untracked file %q: %w", exactRel, writeErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close untracked file %q: %w", exactRel, closeErr)
		}
		if err := os.Chmod(dstPath, perm); err != nil {
			return fmt.Errorf("chmod untracked file %q: %w", exactRel, err)
		}
	}
	return nil
}

// validateDisjointCloneDestination resolves symlinks on srcRepoDir and the
// nearest existing ancestor of dstCloneDir and rejects any destination that
// overlaps with or is nested inside srcRepoDir (or vice versa).
func validateDisjointCloneDestination(srcRepoDir, dstCloneDir string) error {
	srcAbs, err := filepath.Abs(srcRepoDir)
	if err != nil {
		return fmt.Errorf("resolve source repository path: %w", err)
	}
	srcCanonical, err := filepath.EvalSymlinks(srcAbs)
	if err != nil {
		return fmt.Errorf("eval symlinks for source repository %q: %w", srcRepoDir, err)
	}
	dstCanonical, err := resolveCanonicalTargetPath(dstCloneDir)
	if err != nil {
		return fmt.Errorf("resolve destination clone path %q: %w", dstCloneDir, err)
	}
	sep := string(filepath.Separator)
	if dstCanonical == srcCanonical ||
		strings.HasPrefix(dstCanonical, srcCanonical+sep) ||
		strings.HasPrefix(srcCanonical, dstCanonical+sep) {
		return fmt.Errorf("destination clone directory %q overlaps with source repository %q", dstCloneDir, srcRepoDir)
	}
	return nil
}

// resolveCanonicalTargetPath resolves symlinks on the deepest existing ancestor
// of path and appends any not-yet-created suffix segments.
func resolveCanonicalTargetPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	var tail []string
	cur := abs
	for {
		resolved, err := filepath.EvalSymlinks(cur)
		if err == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, tail[i])
			}
			return resolved, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", fmt.Errorf("no existing ancestor for %q", abs)
		}
		tail = append(tail, filepath.Base(cur))
		cur = parent
	}
}

// prepareUntrackedDestination ensures every ancestor directory under dstCloneDir
// is a real non-symlink directory and that the leaf path does not yet exist.
func prepareUntrackedDestination(dstCloneDir, exactRel string) (string, error) {
	segments := strings.Split(exactRel, "/")
	cur := dstCloneDir
	for i := 0; i < len(segments)-1; i++ {
		cur = filepath.Join(cur, segments[i])
		info, err := os.Lstat(cur)
		if errors.Is(err, os.ErrNotExist) {
			if mkErr := os.Mkdir(cur, 0o750); mkErr != nil {
				return "", fmt.Errorf("mkdir %q: %w", cur, mkErr)
			}
			continue
		}
		if err != nil {
			return "", fmt.Errorf("lstat ancestor %q: %w", cur, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", fmt.Errorf("refusing to traverse non-directory or symlink ancestor %q for %q", cur, exactRel)
		}
	}
	dstPath := filepath.Join(cur, segments[len(segments)-1])
	if _, err := os.Lstat(dstPath); err == nil {
		return "", fmt.Errorf("refusing to overwrite existing path or symlink at %q", exactRel)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("lstat destination %q: %w", exactRel, err)
	}
	return dstPath, nil
}

// runGitReadOnly executes a Git subcommand with --no-optional-locks and
// disabled hooks so the repository index and working tree are never touched.
func runGitReadOnly(ctx context.Context, repoDir string, args ...string) ([]byte, error) {
	fullArgs := append([]string{"--no-optional-locks", "-c", "core.hooksPath=/dev/null"}, args...)
	cmd := exec.CommandContext(ctx, "git", fullArgs...)
	cmd.Dir = repoDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// runGitCommand executes a Git command inside dir with hooks and template
// directories disabled and optional stdin bytes.
func runGitCommand(ctx context.Context, dir string, stdin []byte, args ...string) error {
	fullArgs := append([]string{"-c", "core.hooksPath=/dev/null", "-c", "init.templateDir="}, args...)
	cmd := exec.CommandContext(ctx, "git", fullArgs...)
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

// normalizeSelectedPaths validates repository-relative path filters without
// trimming valid filename whitespace.
func normalizeSelectedPaths(selected []string) ([]string, error) {
	if len(selected) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(selected))
	for _, raw := range selected {
		exact, err := validateRelativePath(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, exact)
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

// validateRelativePath verifies that raw is a non-empty repository-relative
// slash path with no empty, ".", or ".." segments, preserving exact whitespace.
func validateRelativePath(raw string) (string, error) {
	slashed := filepath.ToSlash(raw)
	if slashed == "" || strings.HasPrefix(slashed, "/") || strings.HasSuffix(slashed, "/") || strings.ContainsRune(slashed, 0) {
		return "", fmt.Errorf("invalid repository-relative path %q", raw)
	}
	for _, seg := range strings.Split(slashed, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", fmt.Errorf("path %q contains invalid segment %q", raw, seg)
		}
	}
	return slashed, nil
}

// matchesSelectedPaths reports whether rel matches one of filters (either exact
// path match or nested under a directory filter).
func matchesSelectedPaths(rel string, filters []string) bool {
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
	h.Write(fmt.Appendf(nil, "head:%s\npatch:%d\n", head, len(patch)))
	h.Write(patch)
	for _, u := range untracked {
		h.Write(fmt.Appendf(
			nil,
			"\nuntracked:%d:%s:mode=%#o:symlink=%t:target=%d:%s:len=%d\n",
			len(u.Path),
			u.Path,
			u.Mode,
			u.IsSymlink,
			len(u.LinkTarget),
			u.LinkTarget,
			len(u.Content),
		))
		h.Write(u.Content)
	}
	return hex.EncodeToString(h.Sum(nil))
}
