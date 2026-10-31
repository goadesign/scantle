package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/goadesign/surla/internal/controlplane"
	"github.com/goadesign/surla/internal/verify"
	"github.com/goadesign/surla/internal/verifyrun"
)

// verificationFile is the JSON form of the operator's CP4 configuration that
// `surla serve -verification-config` reads. Every field is required.
type verificationFile struct {
	// RunsDir holds one private directory per verification run.
	RunsDir string `json:"runs_dir"`
	// ReportDir receives every full report, named by its digest.
	ReportDir string `json:"report_dir"`
	// CandidateRepo is the git repository builders push candidates to.
	CandidateRepo string `json:"candidate_repo"`
	// CandidateModule is the Go module directory inside CandidateRepo.
	CandidateModule string `json:"candidate_module"`
	// Binaries maps each candidate binary name to its package path.
	Binaries map[string]string `json:"binaries"`
	// SuiteRepo is the git repository of the evaluator suite.
	SuiteRepo string `json:"suite_repo"`
	// SuiteCommit is the exact evaluator suite commit every run uses.
	SuiteCommit string `json:"suite_commit"`
	// SuiteModule is the suite's Go module directory inside SuiteRepo.
	SuiteModule string `json:"suite_module"`
	// SuiteCommand is the suite command's package path inside SuiteModule.
	SuiteCommand string `json:"suite_command"`
}

// runVerifyRun runs one internal role of a verification run started by
// verifyrun.Start: `surla verify-run supervise <dir>` or
// `surla verify-run execute <dir>`. Only verifyrun starts these; they are not
// listed in the usage text.
func runVerifyRun(args []string) error {
	if len(args) != 2 {
		return errors.New("verify-run takes a role (supervise or execute) and a run directory")
	}
	self, err := selfCommand()
	if err != nil {
		return err
	}
	switch role, dir := args[0], args[1]; role {
	case "supervise":
		return verifyrun.Supervise(dir, self)
	case "execute":
		return verifyrun.Execute(dir, verify.Run, os.Stdout, os.Stderr)
	default:
		return fmt.Errorf("unknown verify-run role %q", role)
	}
}

// runVerify builds the candidate commit in the OS sandbox and runs the
// evaluator suite commit against it, through the same supervised processes
// verify_change uses, with -work-dir as the runs directory. It waits for the
// run and writes the JSON report to stdout: the full report, or its builder
// view with -builder-view. The full report is also saved in -report-dir. A
// report that did not pass is still printed, and the command then fails so
// the exit status is 1. A run that ends without a report prints nothing and
// fails with where it stopped; the full error is in the run's executor.log.
// SIGINT and SIGTERM cancel the run.
func runVerify(args []string) error {
	plan, builderView, err := parseVerifyFlags(args)
	if err != nil {
		return err
	}
	self, err := selfCommand()
	if err != nil {
		return err
	}
	root := plan.WorkDir
	runID := verifyrun.NewRunID()
	supervisor, err := verifyrun.Start(root, verifyrun.Request{
		RunID: runID, Owner: "cli", AcceptedAt: time.Now().UTC(), Plan: plan,
	}, self)
	if err != nil {
		return err
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	go forwardCancel(signals, supervisor)
	if err := verifyrun.Wait(root, runID); err != nil {
		return err
	}
	_, state, err := verifyrun.Read(root, runID)
	if err != nil {
		return err
	}
	if state.Kind != verifyrun.StateCompleted {
		return fmt.Errorf("verification %s ended without a report at stage %s: %s (full output in %s)",
			state.Result.Failure, state.Result.Stage, state.Result.Summary, filepath.Join(root, runID, "executor.log"))
	}
	return writeVerifyReport(os.Stdout, state.Report, builderView)
}

// loadVerificationConfig strictly decodes the operator's verification
// configuration at path into a controlplane.VerificationConfig whose runs
// re-execute this surla executable. An empty path means verification is not
// configured.
func loadVerificationConfig(path string) (*controlplane.VerificationConfig, error) {
	if path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read -verification-config: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var f verificationFile
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("decode -verification-config %s: %w", path, err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("decode -verification-config %s: data after the configuration", path)
	}
	var missing []string
	for name, v := range map[string]string{
		"runs_dir": f.RunsDir, "report_dir": f.ReportDir, "candidate_repo": f.CandidateRepo,
		"candidate_module": f.CandidateModule, "suite_repo": f.SuiteRepo, "suite_commit": f.SuiteCommit,
		"suite_module": f.SuiteModule, "suite_command": f.SuiteCommand,
	} {
		if v == "" {
			missing = append(missing, name)
		}
	}
	if len(f.Binaries) == 0 {
		missing = append(missing, "binaries")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("-verification-config %s is missing %v", path, missing)
	}
	self, err := selfCommand()
	if err != nil {
		return nil, err
	}
	return &controlplane.VerificationConfig{
		RunsDir: f.RunsDir,
		Plan: verify.Plan{
			CandidateRepo: f.CandidateRepo, CandidateModule: f.CandidateModule, Binaries: f.Binaries,
			SuiteRepo: f.SuiteRepo, SuiteCommit: f.SuiteCommit, SuiteModule: f.SuiteModule,
			SuiteCommand: f.SuiteCommand, ReportDir: f.ReportDir,
		},
		Command: self,
	}, nil
}

// forwardCancel asks the run's supervisor to cancel the run when the CLI
// receives SIGINT or SIGTERM. The supervisor runs in its own session, so it
// does not receive the terminal's signals itself.
func forwardCancel(signals <-chan os.Signal, supervisor int) {
	if _, ok := <-signals; !ok {
		return
	}
	if err := syscall.Kill(supervisor, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		fmt.Fprintf(os.Stderr, "surla: cancel verification: %v\n", err)
	}
}

// selfCommand returns the argument list that runs this surla executable.
func selfCommand() ([]string, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate the surla executable: %w", err)
	}
	return []string{exe}, nil
}
