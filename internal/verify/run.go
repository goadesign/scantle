package verify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"goa.design/goa-ai/eval"
)

type (
	// Plan says what to verify: an exact candidate commit, the binaries to
	// build from it, and an exact evaluator suite commit.
	Plan struct {
		// CandidateRepo is the git repository holding the candidate.
		CandidateRepo string
		// CandidateCommit is the exact commit to build.
		CandidateCommit string
		// CandidateModule is the Go module directory inside the candidate
		// repository that the binaries are built from.
		CandidateModule string
		// Binaries maps each artifact name to its package path inside
		// CandidateModule, such as "./cmd/orders".
		Binaries map[string]string
		// SuiteRepo is the git repository holding the evaluator suite.
		SuiteRepo string
		// SuiteCommit is the exact suite commit to run.
		SuiteCommit string
		// SuiteModule is the suite's Go module directory inside SuiteRepo.
		SuiteModule string
		// SuiteCommand is the suite command's package path inside
		// SuiteModule, following the suite command contract.
		SuiteCommand string
		// RequestRevision identifies the confirmed request being verified.
		RequestRevision string
		// Environment names the dependency profile, such as "sim".
		Environment string
		// ConfigDigest identifies the candidate's effective configuration.
		ConfigDigest string
		// WorkDir is a private directory for clones, builds and evidence. It
		// must not be shared with the candidate's builder.
		WorkDir string
		// ReportDir receives the report, named by its digest.
		ReportDir string
	}

	// declaration is the suite's --describe output.
	declaration struct {
		// ID is the suite identifier.
		ID string `json:"id"`
		// Scenarios lists every declared scenario.
		Scenarios []struct {
			// ID is the scenario identifier.
			ID string `json:"id"`
			// Tags are the scenario's declared tags.
			Tags []string `json:"tags"`
		} `json:"scenarios"`
	}
)

const (
	// holdoutTag marks a scenario whose details builders must never see.
	holdoutTag = "holdout"
	// maxErrorExcerpt bounds how much of a failed command's output an error
	// message quotes; the full output stays in the evidence directory.
	maxErrorExcerpt = 4 << 10
	// suiteStopGrace is how long the suite's process group gets to clean up
	// after SIGTERM before it is killed.
	suiteStopGrace = 30 * time.Second
)

// Run clones the candidate at its exact commit and builds its binaries, then
// clones the suite at its exact commit, asks it for its declaration, runs it
// against the built binaries, and returns the CP4 report bound to everything
// that was tested. The report is also written to ReportDir as <digest>.json.
//
// The candidate is built before the suite is checked out, with cgo disabled
// and a minimal environment, so building candidate source cannot read the
// protected suite or verifier credentials. Every command's output goes to
// files under WorkDir/evidence; errors quote only a bounded excerpt.
//
// A suite that produces no decodable eval.Report is an error. Otherwise the
// report keeps the suite's evidence and exit status: a suite that ran and
// failed, or that exited abnormally after writing partial results, yields a
// report that does not pass.
func Run(ctx context.Context, p Plan) (Report, error) {
	evidenceDir := filepath.Join(p.WorkDir, "evidence")
	binDir := filepath.Join(p.WorkDir, "bin")
	for _, dir := range []string{evidenceDir, binDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return Report{}, fmt.Errorf("create %s: %w", dir, err)
		}
	}
	r := &runner{evidenceDir: evidenceDir}
	binding := Binding{
		CandidateCommit: p.CandidateCommit,
		RequestRevision: p.RequestRevision,
		SuiteCommit:     p.SuiteCommit,
		Artifacts:       map[string]string{},
		Environment:     p.Environment,
		ConfigDigest:    p.ConfigDigest,
	}

	candidateDir := filepath.Join(p.WorkDir, "candidate")
	if err := r.checkout(ctx, "candidate", p.CandidateRepo, p.CandidateCommit, candidateDir); err != nil {
		return Report{}, err
	}
	buildEnv, err := candidateBuildEnv(p.WorkDir)
	if err != nil {
		return Report{}, err
	}
	var artifactFlags []string
	for _, name := range slices.Sorted(maps.Keys(p.Binaries)) {
		out := filepath.Join(binDir, name)
		if err := r.run(ctx, "build-"+name, filepath.Join(candidateDir, p.CandidateModule), buildEnv,
			"go", "build", "-trimpath", "-o", out, p.Binaries[name]); err != nil {
			return Report{}, fmt.Errorf("build candidate %s: %w", name, err)
		}
		digest, err := sha256File(out)
		if err != nil {
			return Report{}, err
		}
		binding.Artifacts[name] = digest
		artifactFlags = append(artifactFlags, "--artifact", name+"="+out)
	}

	suiteDir := filepath.Join(p.WorkDir, "suite")
	if err := r.checkout(ctx, "suite", p.SuiteRepo, p.SuiteCommit, suiteDir); err != nil {
		return Report{}, err
	}
	suiteBin := filepath.Join(binDir, "suite")
	if err := r.run(ctx, "build-suite", filepath.Join(suiteDir, p.SuiteModule), nil,
		"go", "build", "-o", suiteBin, p.SuiteCommand); err != nil {
		return Report{}, fmt.Errorf("build suite: %w", err)
	}
	suite, err := r.describe(ctx, suiteBin)
	if err != nil {
		return Report{}, err
	}
	binding.SuiteID = suite.ID
	evidence, exit, err := r.runSuite(ctx, suiteBin, artifactFlags)
	if err != nil {
		return Report{}, err
	}
	report, err := Assemble(binding, suite, evidence, exit)
	if err != nil {
		return Report{}, err
	}
	if err := writeReport(p.ReportDir, report); err != nil {
		return Report{}, err
	}
	return report, nil
}

// runner runs the verifier's commands with file-backed output.
type runner struct {
	// evidenceDir receives every command's stdout and stderr.
	evidenceDir string
}

// checkout clones repo into dir and checks out commit, detached. The source
// repository is only read; clone hooks and templates are disabled.
func (r *runner) checkout(ctx context.Context, label, repo, commit, dir string) error {
	if err := r.run(ctx, "clone-"+label, "", nil, "git", "-c", "core.hooksPath=/dev/null",
		"clone", "--quiet", "--no-checkout", "--template=", repo, dir); err != nil {
		return fmt.Errorf("check out %s: %w", label, err)
	}
	if err := r.run(ctx, "checkout-"+label, dir, nil, "git", "-c", "core.hooksPath=/dev/null",
		"-c", "advice.detachedHead=false", "checkout", "--quiet", "--detach", commit); err != nil {
		return fmt.Errorf("check out %s: %w", label, err)
	}
	return nil
}

// describe runs the suite's --describe and converts it into the declared
// scenario list.
func (r *runner) describe(ctx context.Context, suiteBin string) (Suite, error) {
	if err := r.run(ctx, "describe", "", nil, suiteBin, "--describe"); err != nil {
		return Suite{}, fmt.Errorf("describe suite: %w", err)
	}
	var d declaration
	if err := decodeFile(filepath.Join(r.evidenceDir, "describe.stdout"), &d); err != nil {
		return Suite{}, fmt.Errorf("decode suite declaration: %w", err)
	}
	s := Suite{ID: d.ID}
	for _, sc := range d.Scenarios {
		s.Scenarios = append(s.Scenarios, Scenario{ID: sc.ID, Holdout: slices.Contains(sc.Tags, holdoutTag)})
	}
	return s, nil
}

// runSuite runs the suite in its own process group and returns its eval.Report
// and exit status. Cancellation sends SIGTERM to the whole group, so the
// suite's candidate and Temporal processes stop with it, and kills the group
// after a grace period. A report is decoded whatever the exit status; only a
// missing or undecodable report is an error.
func (r *runner) runSuite(ctx context.Context, suiteBin string, artifactFlags []string) (eval.Report, int, error) {
	stdout, stderr, err := r.outputs("suite")
	if err != nil {
		return eval.Report{}, 0, err
	}
	defer stdout.Close()
	defer stderr.Close()
	cmd := exec.CommandContext(ctx, suiteBin, artifactFlags...)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = suiteStopGrace
	runErr := cmd.Run()
	if cmd.Process != nil {
		// Reap anything the suite left in its group, whatever its exit.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	exit := 0
	var exitErr *exec.ExitError
	switch {
	case runErr == nil:
	case errors.As(runErr, &exitErr):
		exit = exitErr.ExitCode()
	default:
		return eval.Report{}, 0, fmt.Errorf("run suite: %w%s", runErr, r.excerpt("suite"))
	}
	var report eval.Report
	if err := decodeFile(filepath.Join(r.evidenceDir, "suite.stdout"), &report); err != nil {
		return eval.Report{}, 0, fmt.Errorf("suite exited %d without a decodable report: %w%s", exit, err, r.excerpt("suite"))
	}
	return report, exit, nil
}

// run runs one command with its stdout and stderr in evidence files named by
// label. env, when non-nil, replaces the inherited environment.
func (r *runner) run(ctx context.Context, label, dir string, env []string, name string, args ...string) error {
	stdout, stderr, err := r.outputs(label)
	if err != nil {
		return err
	}
	defer stdout.Close()
	defer stderr.Close()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w%s", label, err, r.excerpt(label))
	}
	return nil
}

// outputs creates the evidence files for one command.
func (r *runner) outputs(label string) (*os.File, *os.File, error) {
	stdout, err := os.Create(filepath.Join(r.evidenceDir, label+".stdout"))
	if err != nil {
		return nil, nil, fmt.Errorf("create evidence file: %w", err)
	}
	stderr, err := os.Create(filepath.Join(r.evidenceDir, label+".stderr"))
	if err != nil {
		_ = stdout.Close()
		return nil, nil, fmt.Errorf("create evidence file: %w", err)
	}
	return stdout, stderr, nil
}

// excerpt returns the end of a command's stderr for an error message, noting
// truncation and where the full output is.
func (r *runner) excerpt(label string) string {
	path := filepath.Join(r.evidenceDir, label+".stderr")
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() == 0 {
		return ""
	}
	start := max(info.Size()-maxErrorExcerpt, 0)
	b := make([]byte, info.Size()-start)
	if _, err := f.ReadAt(b, start); err != nil && !errors.Is(err, io.EOF) {
		return ""
	}
	note := ""
	if start > 0 {
		note = " (last " + strconv.Itoa(maxErrorExcerpt) + " bytes)"
	}
	return "\nstderr" + note + ", full output in " + path + ":\n" + strings.TrimSpace(string(b))
}

// candidateBuildEnv returns the minimal environment for building candidate
// source: no cgo (so no C preprocessing of arbitrary files), read-only module
// resolution, and private caches, with nothing else inherited.
func candidateBuildEnv(workDir string) ([]string, error) {
	modCache, err := exec.Command("go", "env", "GOMODCACHE").Output()
	if err != nil {
		return nil, fmt.Errorf("locate module cache: %w", err)
	}
	home := filepath.Join(workDir, "build-home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		return nil, fmt.Errorf("create build home: %w", err)
	}
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"GOCACHE=" + filepath.Join(home, "gocache"),
		"GOMODCACHE=" + strings.TrimSpace(string(modCache)),
		"CGO_ENABLED=0",
		"GOFLAGS=-mod=readonly",
	}, nil
}

// decodeFile decodes one JSON value from a file.
func decodeFile(path string, v any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewDecoder(f).Decode(v)
}

// writeReport saves the report as <ReportDir>/<digest>.json.
func writeReport(dir string, r Report) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create report directory: %w", err)
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("encode report: %w", err)
	}
	return os.WriteFile(filepath.Join(dir, r.Digest+".json"), b, 0o600)
}

// sha256File returns the lowercase hex SHA-256 of a file.
func sha256File(path string) (string, error) {
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
