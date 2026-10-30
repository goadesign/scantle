// Package gitcapture tests non-destructive dirty working-tree capture.
package gitcapture

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCapturePreservesDirtyWorkingTreeAndIndexByteForByte(t *testing.T) {
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
	untrackedPath := filepath.Join(dir, "notes.txt")
	untrackedContent := []byte("uncommitted operator scratch notes\n")
	if err := os.WriteFile(untrackedPath, untrackedContent, 0o600); err != nil {
		t.Fatalf("write untracked file: %v", err)
	}

	statusBefore := runGit(t, dir, "status", "--porcelain=v1")

	snap1, err := Capture(dir)
	if err != nil {
		t.Fatalf("Capture(1) failed: %v", err)
	}
	snap2, err := Capture(dir)
	if err != nil {
		t.Fatalf("Capture(2) failed: %v", err)
	}

	if snap1.TreeDigest == "" || snap1.TreeDigest != snap2.TreeDigest {
		t.Fatalf("expected deterministic TreeDigest, got %q vs %q", snap1.TreeDigest, snap2.TreeDigest)
	}
	if !strings.Contains(string(snap1.TrackedPatch), "dirty working tree edit") {
		t.Fatalf("expected TrackedPatch to contain dirty edit, got %q", string(snap1.TrackedPatch))
	}
	if len(snap1.Untracked) != 1 || snap1.Untracked[0].Path != "notes.txt" || string(snap1.Untracked[0].Content) != string(untrackedContent) {
		t.Fatalf("unexpected Untracked capture: %+v", snap1.Untracked)
	}

	statusAfter := runGit(t, dir, "status", "--porcelain=v1")
	if statusBefore != statusAfter {
		t.Fatalf("working tree status mutated: before=%q after=%q", statusBefore, statusAfter)
	}
	gotTracked, err := os.ReadFile(trackedPath)
	if err != nil || string(gotTracked) != string(dirtyContent) {
		t.Fatalf("tracked file mutated after Capture: %q (err=%v)", string(gotTracked), err)
	}
	gotUntracked, err := os.ReadFile(untrackedPath)
	if err != nil || string(gotUntracked) != string(untrackedContent) {
		t.Fatalf("untracked file mutated after Capture: %q (err=%v)", string(gotUntracked), err)
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
