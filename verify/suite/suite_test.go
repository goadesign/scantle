package suite

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/eval"

	"github.com/goadesign/surla/verify/candidate"
)

func TestRunFollowsTheSuiteCommandContract(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "app")
	require.NoError(t, os.WriteFile(bin, []byte("app binary"), 0o700))
	artifact := "--artifact=app=" + bin

	cases := []struct {
		name       string
		args       []string
		failing    bool
		wantCode   int
		wantRan    []string
		wantStderr string
	}{
		{name: "all scenarios pass", args: []string{artifact}, wantCode: ExitPassed, wantRan: []string{"smoke", "hidden"}},
		{name: "a scenario fails", args: []string{artifact}, failing: true, wantCode: ExitFailed, wantRan: []string{"smoke", "hidden"}},
		{name: "one scenario", args: []string{artifact, "--scenario", "smoke"}, wantCode: ExitPassed, wantRan: []string{"smoke"}},
		{name: "one tag", args: []string{artifact, "--tag", "holdout"}, wantCode: ExitPassed, wantRan: []string{"hidden"}},
		{name: "serial", args: []string{artifact, "--max-concurrency", "1"}, wantCode: ExitPassed, wantRan: []string{"smoke", "hidden"}},
		{name: "help", args: []string{"-h"}, wantCode: ExitPassed},
		{name: "missing artifact", args: nil, wantCode: ExitError, wantStderr: "--artifact app=<path> is required"},
		{name: "unknown artifact", args: []string{artifact, "--artifact", "other=" + bin}, wantCode: ExitError, wantStderr: "--artifact other is not a binary this suite runs (expected app)"},
		{name: "repeated artifact", args: []string{artifact, artifact}, wantCode: ExitError, wantStderr: "--artifact app given twice"},
		{name: "malformed artifact", args: []string{"--artifact", "app"}, wantCode: ExitError, wantStderr: `--artifact "app" must be name=path`},
		{name: "scenario and tag", args: []string{artifact, "--scenario", "smoke", "--tag", "holdout"}, wantCode: ExitError, wantStderr: "--scenario and --tag cannot be combined"},
		{name: "zero concurrency", args: []string{artifact, "--max-concurrency", "0"}, wantCode: ExitError, wantStderr: "concurrency must be greater than zero"},
		{name: "missing binary file", args: []string{"--artifact", "app=" + filepath.Join(t.TempDir(), "nope")}, wantCode: ExitError, wantStderr: "open "},
		{name: "positional argument", args: []string{artifact, "extra"}, wantCode: ExitError, wantStderr: `unexpected arguments ["extra"]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			t.Setenv("TMPDIR", tmp)
			defer assertEmptyDir(t, tmp)
			var stdout, stderr bytes.Buffer
			code := runCommand(t.Context(), tc.args, &stdout, &stderr, testCommand(tc.failing))
			assert.Equal(t, tc.wantCode, code, stderr.String())
			assert.Contains(t, stderr.String(), tc.wantStderr)
			if tc.wantRan == nil {
				assert.Empty(t, stdout.String())
				return
			}
			var report eval.Report
			require.NoError(t, json.Unmarshal(stdout.Bytes(), &report))
			var ran []string
			for _, s := range report.Scenarios {
				ran = append(ran, s.ID)
				require.NotNil(t, s.Result)
				assert.Equal(t, []eval.Artifact{{Name: "candidate:app", URI: "sha256:" + digestOf(t, bin)}}, s.Result.Artifacts)
			}
			assert.ElementsMatch(t, tc.wantRan, ran)
			assert.Equal(t, tc.wantCode == ExitPassed, report.Passed)
		})
	}
}

func TestRunReportsASuiteThatCannotBeBuilt(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "app")
	require.NoError(t, os.WriteFile(bin, []byte("app binary"), 0o700))
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	c := testCommand(false)
	c.Suite = func(map[string]candidate.Artifact) (eval.Suite, error) {
		return eval.Suite{}, errors.New("no simulator")
	}

	var stdout, stderr bytes.Buffer
	code := runCommand(t.Context(), []string{"--artifact", "app=" + bin}, &stdout, &stderr, c)
	assert.Equal(t, ExitError, code)
	assert.Empty(t, stdout.String())
	assert.Contains(t, stderr.String(), "no simulator")
	assertEmptyDir(t, tmp)
}

func TestRunFailsWhenTheArtifactStoreCannotBeRemoved(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "app")
	require.NoError(t, os.WriteFile(bin, []byte("app binary"), 0o700))
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	t.Cleanup(func() { require.NoError(t, os.Chmod(tmp, 0o700)) })
	c := testCommand(false)
	build := c.Suite
	c.Suite = func(pinned map[string]candidate.Artifact) (eval.Suite, error) {
		// The store already exists inside tmp; a read-only tmp keeps it from
		// being removed after the scenarios ran.
		require.NoError(t, os.Chmod(tmp, 0o500))
		return build(pinned)
	}

	var stdout, stderr bytes.Buffer
	code := runCommand(t.Context(), []string{"--artifact", "app=" + bin}, &stdout, &stderr, c)
	assert.Equal(t, ExitError, code, "a store left behind fails the run")
	assert.Contains(t, stderr.String(), "remove artifact store")
	var report eval.Report
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &report), "the truthful report is still printed")
	assert.True(t, report.Passed)
}

func TestRunDescribesTheSuiteWithoutBinaries(t *testing.T) {
	c := testCommand(false)
	c.Suite = func(map[string]candidate.Artifact) (eval.Suite, error) {
		panic("--describe must not build the runtime suite")
	}
	var stdout, stderr bytes.Buffer
	code := runCommand(t.Context(), []string{"--describe"}, &stdout, &stderr, c)
	require.Equal(t, ExitPassed, code, stderr.String())
	var d Declaration
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &d))
	assert.Equal(t, Declaration{
		ID:          "starter",
		Environment: "sim",
		Scenarios:   []DeclaredScenario{{ID: "smoke", Tags: []string{"smoke"}}, {ID: "hidden", Tags: []string{"holdout"}}},
	}, d)
}

// testCommand returns a suite running one binary named app with a smoke
// scenario and a holdout scenario, each recording the binary's evidence. With
// failing set, the holdout scenario fails.
func testCommand(failing bool) Command {
	return Command{
		Environment:    "sim",
		Binaries:       []string{"app"},
		MaxConcurrency: 2,
		Declare: func() (eval.Suite, error) {
			return eval.Suite{ID: "starter", Scenarios: []eval.Scenario{
				{ID: "smoke", Tags: []string{"smoke"}},
				{ID: "hidden", Tags: []string{"holdout"}},
			}}, nil
		},
		Suite: func(pinned map[string]candidate.Artifact) (eval.Suite, error) {
			scenario := func(id, tag string, passed bool) eval.Scenario {
				return eval.Scenario{ID: id, Tags: []string{tag}, Timeout: time.Minute, Run: func(context.Context) (eval.Result, error) {
					return eval.Result{
						Checks:    []eval.Check{check(passed)},
						Artifacts: []eval.Artifact{pinned["app"].Evidence("app")},
					}, nil
				}}
			}
			return eval.Suite{ID: "starter", Scenarios: []eval.Scenario{
				scenario("smoke", "smoke", true),
				scenario("hidden", "holdout", !failing),
			}}, nil
		},
	}
}

// check returns a deterministic check that passes or, with a diagnostic as
// failing checks require, fails.
func check(passed bool) eval.Check {
	if passed {
		return eval.Check{Name: "ok", Passed: true}
	}
	return eval.Check{Name: "ok", Diagnostic: "forced failure"}
}

// assertEmptyDir checks that dir, the TMPDIR of a run, holds nothing: the
// run removed its artifact store.
func assertEmptyDir(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries, "artifact store left in %s", dir)
}

// digestOf pins path into a scratch store and returns its digest, the value
// Evidence reports for it.
func digestOf(t *testing.T, path string) string {
	t.Helper()
	store := filepath.Join(t.TempDir(), "store")
	a, err := candidate.Pin(store, path)
	require.NoError(t, err)
	require.NoError(t, candidate.RemoveStore(store))
	return a.Digest()
}
