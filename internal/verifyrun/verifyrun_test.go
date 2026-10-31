package verifyrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"goa.design/goa-ai/eval"

	"github.com/goadesign/surla/internal/verify"
)

// fakeModeEnv selects what the test executor's verification does: "pass"
// saves a passing report, "fail" fails at the build stage, and "hang" starts a
// child process in the run's group, records both PIDs and never returns.
const fakeModeEnv = "VERIFYRUN_TEST_FAKE"

// TestMain lets the test binary act as `surla verify-run supervise|execute`
// so the lifecycle runs in real processes.
func TestMain(m *testing.M) {
	if len(os.Args) == 4 && os.Args[1] == "verify-run" {
		os.Exit(helper(os.Args[2], os.Args[3]))
	}
	os.Exit(m.Run())
}

// helper runs one role of the test binary and returns its exit status.
func helper(role, dir string) int {
	var err error
	switch role {
	case "supervise":
		err = Supervise(dir, []string{os.Args[0]})
	case "execute":
		err = Execute(dir, fakeRun, os.Stdout, os.Stderr)
	default:
		err = fmt.Errorf("unknown role %q", role)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// fakeRun stands in for verify.Run according to fakeModeEnv.
func fakeRun(_ context.Context, plan verify.Plan) (verify.Report, error) {
	switch os.Getenv(fakeModeEnv) {
	case "pass":
		return savePassingReport(plan)
	case "fail":
		return verify.Report{}, &verify.StageError{Stage: verify.StageBuild, Summary: "The candidate did not build.",
			Err: errors.New("compile error in /private/secret/path")}
	case "hang":
		if err := os.MkdirAll(plan.WorkDir, 0o700); err != nil {
			return verify.Report{}, err
		}
		child := exec.Command("sleep", "300")
		if err := child.Start(); err != nil {
			return verify.Report{}, err
		}
		pids := fmt.Sprintf("%d %d", os.Getpid(), child.Process.Pid)
		if err := os.WriteFile(filepath.Join(filepath.Dir(plan.WorkDir), "pids"), []byte(pids), 0o600); err != nil {
			return verify.Report{}, err
		}
		select {}
	}
	return verify.Report{}, errors.New("no fake mode")
}

// savePassingReport assembles a passing report for plan and saves it where
// verify.Run would.
func savePassingReport(plan verify.Plan) (verify.Report, error) {
	binding := verify.Binding{
		CandidateCommit: plan.CandidateCommit, RequestRevision: plan.RequestRevision,
		SuiteID: "starter", SuiteCommit: plan.SuiteCommit, Environment: "sim",
		Artifacts: map[string]string{"app": strings.Repeat("d", 64)},
	}
	suite := verify.Suite{ID: "starter", Environment: "sim", Scenarios: []verify.Scenario{{ID: "smoke"}}}
	evidence := eval.Report{SuiteID: "starter", Scenarios: []eval.ScenarioReport{{
		ID: "smoke", StartedAt: time.Now(), Passed: true,
		Result: &eval.Result{
			Checks:    []eval.Check{{Name: "ok", Passed: true}},
			Artifacts: []eval.Artifact{{Name: "candidate:app", URI: "sha256:" + strings.Repeat("d", 64)}},
		},
	}}}
	report, err := verify.Assemble(binding, suite, evidence, 0)
	if err != nil {
		return verify.Report{}, err
	}
	b, err := json.Marshal(report)
	if err != nil {
		return verify.Report{}, err
	}
	return report, os.WriteFile(filepath.Join(plan.ReportDir, report.Digest+".json"), b, 0o600)
}

// start accepts a run in fake mode and returns its root, ID and supervisor
// PID.
func start(t *testing.T, mode string) (string, string, int) {
	t.Helper()
	t.Setenv(fakeModeEnv, mode)
	root, reports := t.TempDir(), t.TempDir()
	runID := NewRunID()
	pid, err := Start(root, Request{
		RunID: runID, Owner: "alice", ChangeID: "chg", AcceptedAt: time.Now().UTC(),
		Plan: verify.Plan{
			CandidateCommit: strings.Repeat("a", 40), RequestRevision: "rev-1",
			SuiteCommit: strings.Repeat("b", 40), ReportDir: reports,
		},
	}, []string{os.Args[0]})
	require.NoError(t, err)
	return root, runID, pid
}

// hangingPIDs waits up to 30s for a hanging run to record its executor and
// child PIDs.
func hangingPIDs(t *testing.T, root, runID string) (executor, child int) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		raw, err := os.ReadFile(filepath.Join(root, runID, "pids"))
		if err == nil {
			fields := strings.Fields(string(raw))
			require.Len(t, fields, 2)
			executor, err = strconv.Atoi(fields[0])
			require.NoError(t, err)
			child, err = strconv.Atoi(fields[1])
			require.NoError(t, err)
			return executor, child
		}
		require.True(t, time.Now().Before(deadline), "the hanging run never recorded its PIDs")
		time.Sleep(20 * time.Millisecond)
	}
}

// assertGone waits up to 10s for pid to stop existing.
func assertGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		require.True(t, time.Now().Before(deadline), "process %d outlived the run", pid)
		time.Sleep(20 * time.Millisecond)
	}
}

func TestRunThatProducesAReportReadsAsCompleted(t *testing.T) {
	root, runID, _ := start(t, "pass")
	require.NoError(t, Wait(root, runID))
	req, state, err := Read(root, runID)
	require.NoError(t, err)
	assert.Equal(t, "alice", req.Owner)
	assert.Equal(t, filepath.Join(root, runID, "work"), req.Plan.WorkDir)
	require.Equal(t, StateCompleted, state.Kind)
	assert.True(t, state.Report.Passed)
	assert.NoDirExists(t, filepath.Join(root, runID, "work"), "a run with a report releases its work directory")
}

func TestFailedStepReadsAsExecutionFailedWithoutItsOutput(t *testing.T) {
	root, runID, _ := start(t, "fail")
	require.NoError(t, Wait(root, runID))
	_, state, err := Read(root, runID)
	require.NoError(t, err)
	assert.Equal(t, State{Kind: StateNotCompleted, Result: Result{
		Kind: ResultNoReport, Failure: FailureExecution, Stage: "build", Summary: "The candidate did not build.",
	}}, state)
	saved, err := os.ReadFile(filepath.Join(root, runID, "result.json"))
	require.NoError(t, err)
	assert.NotContains(t, string(saved), "/private/secret/path")
	evidence, err := os.ReadFile(filepath.Join(root, runID, "executor.log"))
	require.NoError(t, err)
	assert.Contains(t, string(evidence), "/private/secret/path", "the full error stays with the evaluator")
}

func TestKilledExecutorIsInterruptedAndLeavesNoProcesses(t *testing.T) {
	root, runID, _ := start(t, "hang")
	executor, child := hangingPIDs(t, root, runID)
	_, state, err := Read(root, runID)
	require.NoError(t, err)
	assert.Equal(t, StateRunning, state.Kind)

	require.NoError(t, syscall.Kill(executor, syscall.SIGKILL))
	require.NoError(t, Wait(root, runID))
	assertGone(t, child)
	_, state, err = Read(root, runID)
	require.NoError(t, err)
	assert.Equal(t, interrupted(summaryExecutorDied), state.Result)
	assert.DirExists(t, filepath.Join(root, runID, "work"), "an interrupted run keeps its work directory as evidence")
}

func TestKilledSupervisorStopsTheRunAndLeavesNoProcesses(t *testing.T) {
	root, runID, supervisor := start(t, "hang")
	executor, child := hangingPIDs(t, root, runID)

	require.NoError(t, syscall.Kill(supervisor, syscall.SIGKILL))
	require.NoError(t, Wait(root, runID))
	assertGone(t, executor)
	assertGone(t, child)
	_, state, err := Read(root, runID)
	require.NoError(t, err)
	assert.Equal(t, interrupted(summarySupervisorDied), state.Result)
}

func TestCanceledRunIsInterruptedAndLeavesNoProcesses(t *testing.T) {
	root, runID, supervisor := start(t, "hang")
	executor, child := hangingPIDs(t, root, runID)

	require.NoError(t, syscall.Kill(supervisor, syscall.SIGTERM))
	require.NoError(t, Wait(root, runID))
	assertGone(t, executor)
	assertGone(t, child)
	_, state, err := Read(root, runID)
	require.NoError(t, err)
	assert.Equal(t, interrupted(summaryCanceled), state.Result)
}

func TestReadAndStartRejectInvalidRuns(t *testing.T) {
	root := t.TempDir()
	_, _, err := Read(root, "../escape")
	assert.ErrorContains(t, err, "is not 32 lowercase hex characters")
	_, _, err = Read(root, NewRunID())
	assert.ErrorContains(t, err, "read run request")

	runID := NewRunID()
	require.NoError(t, os.MkdirAll(filepath.Join(root, runID), 0o700))
	_, err = Start(root, Request{RunID: runID}, []string{os.Args[0]})
	assert.ErrorContains(t, err, "create run directory", "a run directory is never reused")
}
