// Package gitcapture tests non-destructive dirty working-tree capture and
// isolated clone materialization.
package gitcapture

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCapturePreservesDirtyWorkingTreeModesSymlinksAndTrailingSpacePaths(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test Operator")

	trackedPath := filepath.Join(dir, "design.go")
	if err := os.WriteFile(trackedPath, []byte("package design\n// v1\n"), 0o600); err != nil {
		t.Fatalf("write tracked file: %v", err)
	}
	// Create a tracked symlink `scripts/link` pointing to `trackedPath` in the source checkout.
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o750); err != nil {
		t.Fatalf("mkdir scripts: %v", err)
	}
	trackedLink := filepath.Join(dir, "scripts", "link")
	if err := os.Symlink(trackedPath, trackedLink); err != nil {
		t.Fatalf("create tracked symlink: %v", err)
	}
	runGit(t, dir, "add", "design.go", "scripts/link")
	runGit(t, dir, "commit", "-m", "initial commit")

	dirtyContent := []byte("package design\n// dirty working tree edit\n")
	if err := os.WriteFile(trackedPath, dirtyContent, 0o600); err != nil {
		t.Fatalf("modify tracked file: %v", err)
	}

	// Create an executable untracked script, a dangling symlink, an untracked file
	// with a trailing space ("scripts/link ") next to tracked symlink "scripts/link",
	// and an unselected scratch file.
	scriptRel := "scripts/check.sh"
	scriptAbs := filepath.Join(dir, filepath.FromSlash(scriptRel))
	if err := os.WriteFile(scriptAbs, []byte("#!/bin/sh\necho ok\n"), 0o755); err != nil {
		t.Fatalf("write executable script: %v", err)
	}
	if err := os.Chmod(scriptAbs, 0o755); err != nil {
		t.Fatalf("chmod executable script: %v", err)
	}

	symlinkRel := "scripts/dangling-link"
	symlinkAbs := filepath.Join(dir, filepath.FromSlash(symlinkRel))
	if err := os.Symlink("../nonexistent-target", symlinkAbs); err != nil {
		t.Fatalf("create dangling symlink: %v", err)
	}

	trailingSpaceRel := "scripts/link "
	trailingSpaceAbs := filepath.Join(dir, "scripts", "link ")
	trailingSpaceContent := []byte("untracked file with trailing space must not write through scripts/link\n")
	if err := os.WriteFile(trailingSpaceAbs, trailingSpaceContent, 0o600); err != nil {
		t.Fatalf("write trailing-space file: %v", err)
	}

	scratchAbs := filepath.Join(dir, "unselected-secret.txt")
	if err := os.WriteFile(scratchAbs, []byte("do not capture"), 0o600); err != nil {
		t.Fatalf("write unselected file: %v", err)
	}

	statusBefore := runGit(t, dir, "status", "--porcelain=v1")
	indexBytesBefore, err := os.ReadFile(filepath.Join(dir, ".git", "index"))
	if err != nil {
		t.Fatalf("read .git/index before capture: %v", err)
	}

	// Empty selectedPaths must capture zero untracked files.
	emptySnap, err := Capture(ctx, dir)
	if err != nil {
		t.Fatalf("Capture with empty selectedPaths failed: %v", err)
	}
	if len(emptySnap.Untracked) != 0 {
		t.Fatalf("expected 0 untracked files when selectedPaths is empty, got %d", len(emptySnap.Untracked))
	}

	snap1, err := Capture(ctx, dir, "scripts")
	if err != nil {
		t.Fatalf("Capture(1) failed: %v", err)
	}
	snap2, err := Capture(ctx, dir, "scripts")
	if err != nil {
		t.Fatalf("Capture(2) failed: %v", err)
	}

	if snap1.TreeDigest == "" || snap1.TreeDigest != snap2.TreeDigest {
		t.Fatalf("expected deterministic TreeDigest, got %q vs %q", snap1.TreeDigest, snap2.TreeDigest)
	}
	if len(snap1.Untracked) != 3 {
		t.Fatalf("expected 3 selected untracked entries under scripts, got %+v", snap1.Untracked)
	}

	// Reject cloning into a subdirectory of the source checkout (directly or via symlink).
	if err := ApplyToClone(ctx, dir, filepath.Join(dir, "nested-clone"), snap1); err == nil {
		t.Fatal("expected ApplyToClone into a subdirectory of srcRepoDir to fail")
	}
	aliasParent := t.TempDir()
	symlinkToSrc := filepath.Join(aliasParent, "src-alias")
	if err := os.Symlink(dir, symlinkToSrc); err != nil {
		t.Fatalf("create symlink alias to srcRepoDir: %v", err)
	}
	if err := ApplyToClone(ctx, dir, filepath.Join(symlinkToSrc, "nested-via-symlink"), snap1); err == nil {
		t.Fatal("expected ApplyToClone into symlinked subdirectory of srcRepoDir to fail")
	}

	cloneDir := filepath.Join(t.TempDir(), "verify-clone")
	if err := ApplyToClone(ctx, dir, cloneDir, snap1); err != nil {
		t.Fatalf("ApplyToClone failed: %v", err)
	}

	// Verify tracked file in source checkout was NOT overwritten by "scripts/link ".
	sourceTrackedAfter, err := os.ReadFile(trackedPath)
	if err != nil || !bytes.Equal(sourceTrackedAfter, dirtyContent) {
		t.Fatalf("source checkout tracked file was mutated! got %q, want %q", string(sourceTrackedAfter), string(dirtyContent))
	}
	clonedTrailing, err := os.ReadFile(filepath.Join(cloneDir, filepath.FromSlash(trailingSpaceRel)))
	if err != nil || !bytes.Equal(clonedTrailing, trailingSpaceContent) {
		t.Fatalf("expected cloned %q to contain trailing-space content, got %q (err=%v)", trailingSpaceRel, string(clonedTrailing), err)
	}

	clonedScriptInfo, err := os.Lstat(filepath.Join(cloneDir, filepath.FromSlash(scriptRel)))
	if err != nil || clonedScriptInfo.Mode().Perm() != 0o755 {
		t.Fatalf("expected cloned script mode 0755, got %v (err=%v)", clonedScriptInfo, err)
	}

	clonedLinkTarget, err := os.Readlink(filepath.Join(cloneDir, filepath.FromSlash(symlinkRel)))
	if err != nil || clonedLinkTarget != "../nonexistent-target" {
		t.Fatalf("expected cloned dangling symlink target %q, got %q (err=%v)", "../nonexistent-target", clonedLinkTarget, err)
	}

	// Verify the source checkout status and .git/index bytes are untouched.
	statusAfter := runGit(t, dir, "status", "--porcelain=v1")
	if statusBefore != statusAfter {
		t.Fatalf("working tree status mutated: before=%q after=%q", statusBefore, statusAfter)
	}
	indexBytesAfter, err := os.ReadFile(filepath.Join(dir, ".git", "index"))
	if err != nil || !bytes.Equal(indexBytesBefore, indexBytesAfter) {
		t.Fatal(".git/index bytes mutated during Capture or ApplyToClone")
	}
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v (%s)", strings.Join(args, " "), err, string(out))
	}
	return string(out)
}
