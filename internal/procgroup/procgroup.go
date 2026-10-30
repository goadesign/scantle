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

// Run runs cmd to completion as the leader of a new process group and returns
// only once every process in that group has exited. When cmd's context is
// canceled, sig goes to the whole group and grace bounds how long the command
// then gets to exit and flush its output. Whatever ended the command, every
// process left in the group is killed and Run waits up to stopTimeout for the
// group to empty.
//
// runErr is cmd's own result, as from exec.Cmd.Run. stopErr reports a group
// that did not empty; callers must then not release resources the remaining
// processes may use. cmd must come from exec.CommandContext.
func Run(cmd *exec.Cmd, sig syscall.Signal, grace, stopTimeout time.Duration) (runErr, stopErr error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, sig) }
	cmd.WaitDelay = grace
	runErr = cmd.Run()
	if cmd.Process != nil {
		stopErr = stop(cmd.Process.Pid, stopTimeout)
	}
	return runErr, stopErr
}

// stop kills every process remaining in the group led by pgid and waits, up to
// timeout, until the group no longer exists.
func stop(pgid int, timeout time.Duration) error {
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
