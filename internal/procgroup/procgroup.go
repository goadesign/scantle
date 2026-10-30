// Package procgroup runs a command in its own process group and stops the
// whole group, so a command that starts children (go build starting the
// compiler, a suite starting candidates and Temporal) never leaves them
// running after it returns.
package procgroup

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"time"
)

// pollInterval is how often Stop checks whether the group has emptied.
const pollInterval = 50 * time.Millisecond

// Own makes cmd start in a new process group and, when its context is
// canceled, send sig to the whole group; grace bounds how long Wait then waits
// for the command's own exit and output. Call it before cmd.Start.
func Own(cmd *exec.Cmd, sig syscall.Signal, grace time.Duration) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, sig) }
	cmd.WaitDelay = grace
}

// Stop kills every process remaining in the group led by pgid and waits, up to
// timeout, until the group no longer exists. A group that still has processes
// after timeout is an error; callers must not release resources those
// processes use.
func Stop(pgid int, timeout time.Duration) error {
	if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("kill process group %d: %w", pgid, err)
	}
	deadline := time.Now().Add(timeout)
	for {
		err := syscall.Kill(-pgid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		if err != nil && !errors.Is(err, syscall.EPERM) {
			return fmt.Errorf("check process group %d: %w", pgid, err)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("process group %d still has processes after %s", pgid, timeout)
		}
		time.Sleep(pollInterval)
	}
}
