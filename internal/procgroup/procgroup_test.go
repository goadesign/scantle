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

func TestRunEndsEveryChildOnCancel(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	ctx, cancel := context.WithCancel(t.Context())
	// The shell ignores TERM, so only Run's final KILL ends it; its background
	// child records its PID and would otherwise outlive the shell.
	cmd := exec.CommandContext(ctx, "sh", "-c", `trap "" TERM; sleep 300 & echo $! > `+pidFile+`; wait`)
	type result struct{ runErr, stopErr error }
	done := make(chan result, 1)
	go func() {
		runErr, stopErr := Run(cmd, syscall.SIGTERM, 200*time.Millisecond, 5*time.Second)
		done <- result{runErr, stopErr}
	}()
	child := waitForPID(t, pidFile)
	cancel()
	res := <-done
	assert.Error(t, res.runErr, "the canceled command reports how it ended")
	require.NoError(t, res.stopErr)
	assert.ErrorIs(t, syscall.Kill(child, 0), syscall.ESRCH, "Run returns only once the TERM-ignoring child is gone")
}

func TestRunOfACleanCommandSucceeds(t *testing.T) {
	runErr, stopErr := Run(exec.CommandContext(t.Context(), "true"), syscall.SIGTERM, time.Second, time.Second)
	assert.NoError(t, runErr)
	assert.NoError(t, stopErr)
}

func TestRunWithoutStartingReportsTheStartError(t *testing.T) {
	runErr, stopErr := Run(exec.CommandContext(t.Context(), "/nonexistent/binary"), syscall.SIGTERM, time.Second, time.Second)
	assert.Error(t, runErr)
	assert.NoError(t, stopErr, "nothing started, so nothing is left to stop")
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
