// Package gitcapture tests non-destructive dirty working-tree capture and
// isolated clone materialization.
package gitcapture

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCapturePreservesDirtyWorkingTreeModesSymlinksAndAppliesToIsolatedClone(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test Operator")

	trackedPath := filepath.Join(dir, "design.go")
	if err := os.WriteFile(trackedPath, []byte("package design\n// v1\n"), 0o600); err != nil {
		t.Fatalf("write tracked file: %v", err)
	}
	runGit(t, dir, "add", "design.go")
	runGit(t, dir, "commit", "-m", "initial commit")

	dirtyContent := []byte("package design\n// dirty working tree edit\n")
	if err := os.WriteFile(trackedPath, dirtyContent, 0o600); err != nil {
		t.Fatalf("modify tracked file: %v", err)
	}

	// Create an executable untracked script, a dangling symlink, and an unselected scratch file.
	scriptRel := "scripts/check.sh"
	scriptAbs := filepath.Join(dir, filepath.FromSlash(scriptRel))
	if err := os.MkdirAll(filepath.Dir(scriptAbs), 0o750); err != nil {
		t.Fatalf("mkdir scripts: %v", err)
	}
	scriptContent := []byte("#!/bin/sh\necho ok\n")
	if err := os.WriteFile(scriptAbs, scriptContent, 0o755); err != nil {
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

	scratchAbs := filepath.Join(dir, "unselected-secret.txt")
	if err := os.WriteFile(scratchAbs, []byte("do not capture"), 0o600); err != nil {
		t.Fatalf("write unselected file: %v", err)
	}

	statusBefore := runGit(t, dir, "status", "--porcelain=v1")

	snap1, err := Capture(dir, "scripts")
	if err != nil {
		t.Fatalf("Capture(1) failed: %v", err)
	}
	snap2, err := Capture(dir, "scripts")
	if err != nil {
		t.Fatalf("Capture(2) failed: %v", err)
	}

	if snap1.TreeDigest == "" || snap1.TreeDigest != snap2.TreeDigest {
		t.Fatalf("expected deterministic TreeDigest, got %q vs %q", snap1.TreeDigest, snap2.TreeDigest)
	}
	if !strings.Contains(string(snap1.TrackedPatch), "dirty working tree edit") {
		t.Fatalf("expected TrackedPatch to contain dirty edit, got %q", string(snap1.TrackedPatch))
	}
	if len(snap1.Untracked) != 2 {
		t.Fatalf("expected 2 selected untracked entries (excluding unselected-secret.txt), got %+v", snap1.Untracked)
	}

	// Materialize into an isolated verification clone and verify modes, symlinks, and tracked edits.
	cloneDir := filepath.Join(t.TempDir(), "verify-clone")
	if err := ApplyToClone(dir, cloneDir, snap1); err != nil {
		t.Fatalf("ApplyToClone failed: %v", err)
	}

	clonedTracked, err := os.ReadFile(filepath.Join(cloneDir, "design.go"))
	if err != nil || string(clonedTracked) != string(dirtyContent) {
		t.Fatalf("unexpected cloned tracked file: %q (err=%v)", string(clonedTracked), err)
	}

	clonedScriptInfo, err := os.Lstat(filepath.Join(cloneDir, filepath.FromSlash(scriptRel)))
	if err != nil {
		t.Fatalf("lstat cloned script: %v", err)
	}
	if clonedScriptInfo.Mode().Perm() != 0o755 {
		t.Fatalf("expected cloned script mode 0755, got %#o", clonedScriptInfo.Mode().Perm())
	}

	clonedLinkTarget, err := os.Readlink(filepath.Join(cloneDir, filepath.FromSlash(symlinkRel)))
	if err != nil || clonedLinkTarget != "../nonexistent-target" {
		t.Fatalf("expected cloned dangling symlink target %q, got %q (err=%v)", "../nonexistent-target", clonedLinkTarget, err)
	}

	if _, err := os.Lstat(filepath.Join(cloneDir, "unselected-secret.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected unselected-secret.txt to be absent from clone, got err=%v", err)
	}

	// Verify the source checkout and index are byte-for-byte untouched.
	statusAfter := runGit(t, dir, "status", "--porcelain=v1")
	if statusBefore != statusAfter {
		t.Fatalf("working tree status mutated: before=%q after=%q", statusBefore, statusAfter)
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
