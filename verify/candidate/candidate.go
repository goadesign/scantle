// Package candidate is the only place Scantle executes candidate code. Exec
// starts one digest-pinned candidate binary as a single sandboxed process: it
// sees only its explicit environment, reads and writes only its own state
// directory (plus the system libraries needed to load), connects only to the
// loopback ports it was assigned, listens only on its assigned port, and cannot
// create child processes. When the platform cannot enforce that sandbox, Exec
// fails with ErrIsolationUnavailable and never runs the candidate unsandboxed.
//
// Callers such as an evaluator suite keep their own process lifecycle (start,
// kill, restart); the sandbox applies only to the started process, never to the
// trusted caller.
package candidate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

type (
	// Artifact is one candidate binary pinned by the digest verify computed
	// when it built the candidate.
	Artifact struct {
		// Path is the absolute path of the binary.
		Path string
		// Digest is the lowercase hex SHA-256 of the binary's bytes.
		Digest string
	}

	// Spec describes how to run one candidate process.
	Spec struct {
		// Args are the command-line arguments after the binary name.
		Args []string
		// Env is the complete environment as KEY=VALUE entries; nothing is
		// inherited from the caller.
		Env []string
		// StateDir is the only directory the process may read and write. It
		// must exist and is also the process's HOME and TMPDIR.
		StateDir string
		// ConnectPorts are the loopback TCP ports the process may connect to.
		ConnectPorts []int
		// ListenPort is the loopback TCP port the process may listen on, or
		// zero when it must not listen.
		ListenPort int
		// LogFile receives the process's combined stdout and stderr. It is
		// owned by the caller and must lie outside StateDir so the candidate
		// cannot rewrite its own diagnostics. Exec creates or truncates it.
		LogFile string
	}

	// Process is a running sandboxed candidate process. Its output goes to
	// the caller's log file; the verifier keeps none of it in memory.
	Process struct {
		// cmd is the started command.
		cmd *exec.Cmd
		// logFile is the path of the process's output file.
		logFile string
		// done is closed after the process exited.
		done chan struct{}
		// waitErr is the process's exit error, valid after done is closed.
		waitErr error
	}
)

// sandboxExec is the macOS sandbox launcher. It applies the profile and then
// executes the candidate in place, so the candidate keeps the started PID.
const sandboxExec = "/usr/bin/sandbox-exec"

// ErrIsolationUnavailable reports that this platform cannot enforce the
// candidate sandbox, so the candidate must not run.
var ErrIsolationUnavailable = errors.New("candidate isolation is unavailable on this platform")

// ErrDigestMismatch reports that the binary on disk is not the pinned artifact.
var ErrDigestMismatch = errors.New("candidate binary does not match its pinned digest")

// NewArtifact pins the binary at path by its current SHA-256 digest.
func NewArtifact(path string) (Artifact, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return Artifact{}, fmt.Errorf("resolve %s: %w", path, err)
	}
	digest, err := fileDigest(abs)
	if err != nil {
		return Artifact{}, err
	}
	return Artifact{Path: abs, Digest: digest}, nil
}

// Exec verifies the artifact's digest and starts it in the sandbox described by
// spec. The process is killed when ctx ends.
func Exec(ctx context.Context, a Artifact, spec Spec) (*Process, error) {
	if runtime.GOOS != "darwin" {
		return nil, ErrIsolationUnavailable
	}
	if _, err := os.Stat(sandboxExec); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrIsolationUnavailable, err)
	}
	digest, err := fileDigest(a.Path)
	if err != nil {
		return nil, err
	}
	if digest != a.Digest {
		return nil, fmt.Errorf("%w: %s is %s, pinned %s", ErrDigestMismatch, a.Path, digest, a.Digest)
	}
	stateDir, err := filepath.EvalSymlinks(spec.StateDir)
	if err != nil {
		return nil, fmt.Errorf("resolve state directory: %w", err)
	}
	binary, err := filepath.EvalSymlinks(a.Path)
	if err != nil {
		return nil, fmt.Errorf("resolve binary: %w", err)
	}
	logPath, err := filepath.Abs(spec.LogFile)
	if err != nil || spec.LogFile == "" {
		return nil, fmt.Errorf("log file is required: %q", spec.LogFile)
	}
	if rel, err := filepath.Rel(stateDir, resolvedOrSelf(filepath.Dir(logPath))); err == nil && !strings.HasPrefix(rel, "..") {
		return nil, fmt.Errorf("log file %s must lie outside the candidate state directory", logPath)
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}
	profile := Profile(binary, stateDir, spec.ConnectPorts, spec.ListenPort)
	cmd := exec.CommandContext(ctx, sandboxExec, append([]string{"-p", profile, binary}, spec.Args...)...)
	cmd.Env = append([]string{"HOME=" + stateDir, "TMPDIR=" + stateDir}, spec.Env...)
	cmd.Dir = stateDir
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return nil, fmt.Errorf("start candidate %s: %w", binary, err)
	}
	p := &Process{cmd: cmd, logFile: logPath, done: make(chan struct{})}
	go func() {
		p.waitErr = cmd.Wait()
		_ = logFile.Close()
		close(p.done)
	}()
	return p, nil
}

// Profile renders the sandbox profile for one candidate process. Paths must be
// absolute and already free of symlinks.
func Profile(binary, stateDir string, connectPorts []int, listenPort int) string {
	var b strings.Builder
	b.WriteString("(version 1)\n(deny default)\n")
	fmt.Fprintf(&b, "(allow process-exec (literal %s))\n", quote(binary))
	// The standard macOS top-level symlinks must be resolvable so callers can
	// use /var/... and /tmp/... paths; this reveals only that they exist.
	b.WriteString("(allow sysctl-read)\n(allow file-read-metadata (literal \"/var\") (literal \"/tmp\") (literal \"/etc\")")
	// Libraries such as SQLite stat every directory on the path to their
	// files; the state directory's ancestors are visible as metadata only.
	for dir := filepath.Dir(stateDir); dir != "/"; dir = filepath.Dir(dir) {
		fmt.Fprintf(&b, " (literal %s)", quote(dir))
	}
	b.WriteString(")\n")
	fmt.Fprintf(&b, "(allow file-read* (literal \"/\") (literal %s) (subpath \"/usr/lib\") (subpath \"/System\") "+
		"(subpath \"/private/var/db/dyld\") (subpath %s) (literal \"/dev/null\") (literal \"/dev/urandom\") (literal \"/dev/random\"))\n",
		quote(binary), quote(stateDir))
	fmt.Fprintf(&b, "(allow file-write* (subpath %s) (literal \"/dev/null\"))\n", quote(stateDir))
	for _, port := range connectPorts {
		fmt.Fprintf(&b, "(allow network-outbound (remote ip \"localhost:%d\"))\n", port)
	}
	if listenPort != 0 {
		fmt.Fprintf(&b, "(allow network-bind (local ip \"localhost:%d\"))\n", listenPort)
		fmt.Fprintf(&b, "(allow network-inbound (local ip \"localhost:%d\"))\n", listenPort)
	}
	return b.String()
}

// Kill sends SIGKILL to the candidate process and waits for it to exit. The
// sandbox forbids child processes, so the started PID is the whole process
// tree.
func (p *Process) Kill() error {
	select {
	case <-p.done:
		return nil
	default:
	}
	if err := p.cmd.Process.Signal(syscall.SIGKILL); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("kill candidate: %w", err)
	}
	<-p.done
	return nil
}

// Wait waits for the process to exit and returns its exit error.
func (p *Process) Wait() error {
	<-p.done
	return p.waitErr
}

// Done is closed once the process exited.
func (p *Process) Done() <-chan struct{} {
	return p.done
}

// Excerpt returns at most maxBytes from the end of the process's output so
// far, and whether earlier output was left out.
func (p *Process) Excerpt(maxBytes int64) (string, bool, error) {
	f, err := os.Open(p.logFile)
	if err != nil {
		return "", false, fmt.Errorf("open log file: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", false, fmt.Errorf("stat log file: %w", err)
	}
	start := max(info.Size()-maxBytes, 0)
	b := make([]byte, info.Size()-start)
	if _, err := f.ReadAt(b, start); err != nil && !errors.Is(err, io.EOF) {
		return "", false, fmt.Errorf("read log file: %w", err)
	}
	return string(b), start > 0, nil
}

// fileDigest returns the lowercase hex SHA-256 of the file's bytes.
func fileDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("hash %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// resolvedOrSelf resolves symlinks in path, or returns path unchanged when it
// does not exist yet.
func resolvedOrSelf(path string) string {
	if r, err := filepath.EvalSymlinks(path); err == nil {
		return r
	}
	return path
}

// quote renders s as a sandbox profile string literal.
func quote(s string) string {
	return strconv.Quote(s)
}
