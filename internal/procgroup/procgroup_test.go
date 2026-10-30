package procgroup

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCancelAndStopEndEveryChild(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	ctx, cancel := context.WithCancel(t.Context())
	// The shell ignores TERM, so only Stop's KILL ends it; its background
	// child records its PID and would otherwise outlive the shell.
	cmd := exec.CommandContext(ctx, "sh", "-c", `trap "" TERM; sleep 300 & echo $! > `+pidFile+`; wait`)
	Own(cmd, syscall.SIGTERM, 200*time.Millisecond)
	require.NoError(t, cmd.Start())
	child := waitForPID(t, pidFile)
	cancel()
	_ = cmd.Wait()
	require.NoError(t, syscall.Kill(child, 0), "before Stop the background child is still running")

	require.NoError(t, Stop(cmd.Process.Pid, 5*time.Second))
	assert.ErrorIs(t, syscall.Kill(child, 0), syscall.ESRCH, "Stop returns only once the child is gone")
}

func TestStopOnAnEmptyGroupSucceeds(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "true")
	Own(cmd, syscall.SIGTERM, time.Second)
	require.NoError(t, cmd.Run())
	assert.NoError(t, Stop(cmd.Process.Pid, time.Second))
}

// waitForPID reads the PID the test command writes.
func waitForPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(path)
		if err == nil && strings.TrimSpace(string(b)) != "" {
			pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
			require.NoError(t, err)
			return pid
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the command never recorded its child PID")
	return 0
}
