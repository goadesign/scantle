// Package verifyrun runs one Checkpoint 4 verification in processes that
// outlive the API that accepted it, and reads its state back from disk.
//
// Every run owns one directory under the runs root:
//
//	<root>/<run_id>/request.json  immutable: owner, Change Card revision,
//	                              candidate commit and the full verify.Plan
//	<root>/<run_id>/lock          held exclusively by the run's supervisor
//	<root>/<run_id>/result.json   terminal outcome, written by the supervisor
//	<root>/<run_id>/work/         verify.Run's work directory
//
// Start, called by the API, takes the lock before it writes request.json and
// hands the locked file to `surla verify-run supervise <dir>`, a new session
// that keeps the lock for its whole life. The supervisor starts
// `surla verify-run execute <dir>` as the leader of a new process group, so
// the builds, the evaluator suite and the candidates it runs all stay in that
// one group. When the executor ends, for any reason, the supervisor kills the
// group, waits until it is empty, and only then writes result.json. If the
// supervisor dies instead, the executor sees its control pipe close and kills
// its own group.
//
// Read classifies a run by the kernel lock alone: a held lock means the run is
// in progress; otherwise result.json is its outcome, and a missing result
// means the run's processes ended before it finished. Nothing else records a
// run's state.
package verifyrun

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"syscall"
	"time"

	"github.com/goadesign/surla/internal/procgroup"
	"github.com/goadesign/surla/internal/verify"
)

type (
	// Request is the immutable record of one accepted verification run.
	Request struct {
		// RunID identifies the run and names its directory.
		RunID string `json:"run_id"`
		// Owner is the authenticated principal that started the run; only it
		// may read the run.
		Owner string `json:"owner"`
		// ChangeID is the Change Card the run verifies.
		ChangeID string `json:"change_id"`
		// AcceptedAt is when the API accepted the run.
		AcceptedAt time.Time `json:"accepted_at"`
		// Plan is the complete verification plan: the operator's candidate
		// and suite configuration, the caller's candidate commit, the Change
		// Card revision, and the run's own work directory.
		Plan verify.Plan `json:"plan"`
	}

	// Result is the terminal outcome of a run, written once by its
	// supervisor and, for a run that finished, first by its executor.
	Result struct {
		// Kind says whether the run produced a report.
		Kind ResultKind `json:"kind"`
		// ReportDigest names the report under Plan.ReportDir when Kind is
		// ResultReport.
		ReportDigest string `json:"report_digest,omitempty"`
		// Failure says how a run without a report ended.
		Failure FailureKind `json:"failure,omitempty"`
		// Stage is where a run without a report stopped.
		Stage string `json:"stage,omitempty"`
		// Summary describes how a run without a report ended, without command
		// output.
		Summary string `json:"summary,omitempty"`
	}

	// ResultKind says whether a run produced a report.
	ResultKind string

	// FailureKind says how a run without a report ended.
	FailureKind string

	// State is a run's current state as Read classifies it.
	State struct {
		// Kind is the state.
		Kind StateKind
		// Report is the saved report when Kind is StateCompleted.
		Report verify.Report
		// Result is the terminal outcome when Kind is StateNotCompleted.
		Result Result
	}

	// StateKind is one of the three run states.
	StateKind string
)

const (
	// ResultReport is a run that produced a report.
	ResultReport ResultKind = "report"
	// ResultNoReport is a run that ended without one.
	ResultNoReport ResultKind = "no_report"

	// FailureExecution is a run whose verification step failed.
	FailureExecution FailureKind = "execution_failed"
	// FailureInterrupted is a run whose processes ended before it finished.
	FailureInterrupted FailureKind = "interrupted"

	// StageSupervise is the stage of a run interrupted outside verify.Run:
	// its executor or supervisor ended, or it was canceled.
	StageSupervise = "supervise"

	// StateRunning is a run still in progress.
	StateRunning StateKind = "running"
	// StateCompleted is a run that produced a report.
	StateCompleted StateKind = "completed"
	// StateNotCompleted is a run that ended without a report.
	StateNotCompleted StateKind = "not_completed"

	// requestFile, lockFile, resultFile and workDir are the entries of a
	// run directory.
	requestFile = "request.json"
	lockFile    = "lock"
	resultFile  = "result.json"
	workDir     = "work"

	// passedFD is the descriptor a supervisor and an executor receive from
	// their parent: the locked run file and the control pipe respectively.
	passedFD = 3

	// groupStopTimeout bounds how long the supervisor waits for the
	// executor's process group to empty after killing it.
	groupStopTimeout = 30 * time.Second
	// executorWaitDelay bounds how long the supervisor waits for the
	// executor's output after it exits.
	executorWaitDelay = 5 * time.Second
	// maxExecutorOutput bounds the executor's result line.
	maxExecutorOutput = 64 << 10

	// summaryExecutorDied, summarySupervisorDied and summaryCanceled
	// describe interrupted runs.
	summaryExecutorDied   = "The verification process ended before it finished."
	summarySupervisorDied = "The process supervising the verification ended before it finished."
	summaryCanceled       = "The verification was canceled."
)

var (
	// runIDPattern is the form of a run ID, which also names its directory.
	runIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
	// digestPattern is the form of a report digest.
	digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// NewRunID returns a new random run ID.
func NewRunID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return hex.EncodeToString(b)
}

// Start accepts req under root and starts its supervisor, then returns the
// supervisor's PID without waiting for the run. command is the argument list
// that runs this program, such as the path of the surla executable; Start
// appends "verify-run supervise <dir>". The run directory must not exist.
// req.Plan.WorkDir is set to the run's work directory.
func Start(root string, req Request, command []string) (int, error) {
	if !runIDPattern.MatchString(req.RunID) {
		return 0, fmt.Errorf("run ID %q is not 32 lowercase hex characters", req.RunID)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return 0, fmt.Errorf("create runs directory: %w", err)
	}
	dir := filepath.Join(root, req.RunID)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return 0, fmt.Errorf("create run directory: %w", err)
	}
	// The lock is taken before request.json exists and is passed, still
	// held, to the supervisor, so no reader ever sees an accepted run that
	// nobody holds.
	lock, err := os.OpenFile(filepath.Join(dir, lockFile), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return 0, fmt.Errorf("create run lock: %w", err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return 0, fmt.Errorf("lock run: %w", err)
	}
	req.Plan.WorkDir = filepath.Join(dir, workDir)
	if err := writeJSON(dir, requestFile, req); err != nil {
		return 0, err
	}
	logFile, err := os.OpenFile(filepath.Join(dir, "supervisor.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return 0, fmt.Errorf("create supervisor log: %w", err)
	}
	defer logFile.Close()
	cmd := exec.Command(command[0], append(command[1:], "verify-run", "supervise", dir)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.ExtraFiles = []*os.File{lock}
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("start verification supervisor: %w", err)
	}
	go reap(cmd)
	return cmd.Process.Pid, nil
}

// Supervise runs as `surla verify-run supervise <dir>`. It holds the run lock
// it inherited on descriptor 3, starts the executor as the leader of a new
// process group with the read end of a control pipe on its descriptor 3, and
// waits for it. SIGTERM or SIGINT cancels the run. When the executor ends, the
// group is killed and emptied before result.json is written: the executor's
// own result when it reported one, and otherwise an interrupted outcome. A
// run that produced a report has its work directory removed; any other run
// keeps it as evidence. command is the argument list that runs this program.
func Supervise(dir string, command []string) error {
	syscall.CloseOnExec(passedFD)
	lock := os.NewFile(passedFD, lockFile)
	defer lock.Close()

	control, keep, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("create control pipe: %w", err)
	}
	defer keep.Close()
	var out bytes.Buffer
	logFile, err := os.OpenFile(filepath.Join(dir, "executor.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("create executor log: %w", err)
	}
	defer logFile.Close()
	cmd := exec.Command(command[0], append(command[1:], "verify-run", "execute", dir)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.ExtraFiles = []*os.File{control}
	cmd.Stdout = &limitedWriter{w: &out, n: maxExecutorOutput}
	cmd.Stderr = logFile
	cmd.WaitDelay = executorWaitDelay
	if err := cmd.Start(); err != nil {
		control.Close()
		return errors.Join(fmt.Errorf("start verification executor: %w", err),
			writeJSON(dir, resultFile, interrupted(summaryExecutorDied)))
	}
	control.Close()
	pgid := cmd.Process.Pid

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(signals)
	canceled := make(chan struct{})
	go func() {
		if _, ok := <-signals; ok {
			close(canceled)
			stopGroup(pgid)
		}
	}()

	waitErr := cmd.Wait()
	// Whatever ended the executor, nothing it started may outlive the run.
	if err := procgroup.Stop(pgid, groupStopTimeout); err != nil {
		return fmt.Errorf("stop verification processes; result not written: %w", err)
	}
	result := interrupted(summaryExecutorDied)
	select {
	case <-canceled:
		result = interrupted(summaryCanceled)
	default:
		if waitErr == nil {
			if r, err := decodeResult(out.Bytes()); err == nil {
				result = r
			}
		}
	}
	if err := writeJSON(dir, resultFile, result); err != nil {
		return err
	}
	if result.Kind == ResultReport {
		return os.RemoveAll(filepath.Join(dir, workDir))
	}
	return nil
}

// Execute runs as `surla verify-run execute <dir>`, the leader of the run's
// process group. It watches the control pipe on descriptor 3 and kills its
// whole group when the supervisor dies; otherwise it runs the plan saved in
// request.json with run and writes the run's Result as JSON to out. The full
// error of a failed step goes to errLog, which stays with the evaluator.
func Execute(dir string, run func(context.Context, verify.Plan) (verify.Report, error), out, errLog io.Writer) error {
	syscall.CloseOnExec(passedFD)
	control := os.NewFile(passedFD, "control")
	go watchSupervisor(control)

	req, err := readRequest(dir)
	if err != nil {
		return err
	}
	report, err := run(context.Background(), req.Plan)
	result := Result{Kind: ResultReport, ReportDigest: report.Digest}
	if err != nil {
		var stage *verify.StageError
		if !errors.As(err, &stage) {
			return fmt.Errorf("verification failed without a stage: %w", err)
		}
		fmt.Fprintln(errLog, err)
		result = Result{Kind: ResultNoReport, Failure: FailureExecution, Stage: string(stage.Stage), Summary: stage.Summary}
	}
	return json.NewEncoder(out).Encode(result)
}

// Read returns the run's request and current state. It takes a shared lock on
// the run before it looks for result.json, so a run that finishes meanwhile is
// never reported as interrupted. A missing run is an error.
func Read(root, runID string) (Request, State, error) {
	if !runIDPattern.MatchString(runID) {
		return Request{}, State{}, fmt.Errorf("run ID %q is not 32 lowercase hex characters", runID)
	}
	dir := filepath.Join(root, runID)
	req, err := readRequest(dir)
	if err != nil {
		return Request{}, State{}, err
	}
	lock, err := os.Open(filepath.Join(dir, lockFile))
	if err != nil {
		return Request{}, State{}, fmt.Errorf("open run lock: %w", err)
	}
	defer lock.Close()
	switch err := syscall.Flock(int(lock.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); {
	case errors.Is(err, syscall.EWOULDBLOCK):
		return req, State{Kind: StateRunning}, nil
	case err != nil:
		return Request{}, State{}, fmt.Errorf("lock run for reading: %w", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, resultFile))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return req, State{Kind: StateNotCompleted, Result: interrupted(summarySupervisorDied)}, nil
	case err != nil:
		return Request{}, State{}, fmt.Errorf("read run result: %w", err)
	}
	result, err := decodeResult(raw)
	if err != nil {
		return Request{}, State{}, err
	}
	if result.Kind == ResultNoReport {
		return req, State{Kind: StateNotCompleted, Result: result}, nil
	}
	f, err := os.Open(filepath.Join(req.Plan.ReportDir, result.ReportDigest+".json"))
	if err != nil {
		return Request{}, State{}, fmt.Errorf("open verification report: %w", err)
	}
	defer f.Close()
	report, err := verify.DecodeReport(f)
	if err != nil {
		return Request{}, State{}, err
	}
	if report.Digest != result.ReportDigest {
		return Request{}, State{}, fmt.Errorf("report %s has digest %s", result.ReportDigest, report.Digest)
	}
	return req, State{Kind: StateCompleted, Report: report}, nil
}

// Wait blocks until the run's supervisor has finished, by taking a shared
// lock on the run.
func Wait(root, runID string) error {
	lock, err := os.Open(filepath.Join(root, runID, lockFile))
	if err != nil {
		return fmt.Errorf("open run lock: %w", err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_SH); err != nil {
		return fmt.Errorf("wait for run: %w", err)
	}
	return nil
}

// limitedWriter keeps at most n bytes written to it and drops the rest.
type limitedWriter struct {
	// w receives the kept bytes.
	w io.Writer
	// n is how many more bytes may be kept.
	n int
}

// Write keeps what fits and reports every byte as written, so the writer
// never blocks or fails the executor.
func (l *limitedWriter) Write(p []byte) (int, error) {
	keep := p[:min(len(p), l.n)]
	l.n -= len(keep)
	if _, err := l.w.Write(keep); err != nil {
		return 0, err
	}
	return len(p), nil
}

// interrupted returns the outcome of a run whose processes ended before it
// finished.
func interrupted(summary string) Result {
	return Result{Kind: ResultNoReport, Failure: FailureInterrupted, Stage: StageSupervise, Summary: summary}
}

// decodeResult strictly decodes one Result and checks that its fields match
// its kind.
func decodeResult(raw []byte) (Result, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var r Result
	if err := dec.Decode(&r); err != nil {
		return Result{}, fmt.Errorf("decode run result: %w", err)
	}
	switch {
	case r.Kind == ResultReport && digestPattern.MatchString(r.ReportDigest) &&
		r.Failure == "" && r.Stage == "" && r.Summary == "":
		return r, nil
	case r.Kind == ResultNoReport && r.ReportDigest == "" &&
		(r.Failure == FailureExecution || r.Failure == FailureInterrupted) && r.Stage != "" && r.Summary != "":
		return r, nil
	}
	return Result{}, fmt.Errorf("run result %s is inconsistent", raw)
}

// readRequest strictly decodes the run's request.json.
func readRequest(dir string) (Request, error) {
	raw, err := os.ReadFile(filepath.Join(dir, requestFile))
	if err != nil {
		return Request{}, fmt.Errorf("read run request: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var req Request
	if err := dec.Decode(&req); err != nil {
		return Request{}, fmt.Errorf("decode run request: %w", err)
	}
	return req, nil
}

// writeJSON writes v to dir/name atomically: a temporary file is written,
// synced and renamed, and the directory is synced.
func writeJSON(dir, name string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("encode %s: %w", name, err)
	}
	tmp, err := os.CreateTemp(dir, name+".*")
	if err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	if _, err := tmp.Write(b); err != nil {
		return errors.Join(fmt.Errorf("write %s: %w", name, err), tmp.Close(), os.Remove(tmp.Name()))
	}
	if err := errors.Join(tmp.Sync(), tmp.Close()); err != nil {
		return errors.Join(fmt.Errorf("write %s: %w", name, err), os.Remove(tmp.Name()))
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, name)); err != nil {
		return errors.Join(fmt.Errorf("write %s: %w", name, err), os.Remove(tmp.Name()))
	}
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("sync %s: %w", dir, err)
	}
	return errors.Join(d.Sync(), d.Close())
}

// stopGroup kills the executor's process group to cancel the run; Supervise
// then waits for it to empty.
func stopGroup(pgid int) {
	if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		fmt.Fprintf(os.Stderr, "cancel verification: kill process group %d: %v\n", pgid, err)
	}
}

// watchSupervisor reads the control pipe until the supervisor's end closes,
// which happens only when the supervisor exits, and then kills the executor's
// whole process group, the executor included.
func watchSupervisor(control *os.File) {
	if _, err := io.Copy(io.Discard, control); err != nil {
		fmt.Fprintf(os.Stderr, "read control pipe: %v\n", err)
	}
	if err := syscall.Kill(0, syscall.SIGKILL); err != nil {
		fmt.Fprintf(os.Stderr, "kill verification process group: %v\n", err)
	}
}

// reap waits for the supervisor so it does not linger as a zombie of the API
// process. Its exit status is not needed: the run's outcome is result.json.
func reap(cmd *exec.Cmd) {
	if err := cmd.Wait(); err != nil {
		fmt.Fprintf(os.Stderr, "verification supervisor %d: %v\n", cmd.Process.Pid, err)
	}
}
