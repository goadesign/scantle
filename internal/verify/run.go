package verify

import (
	"bytes"
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
	"strings"

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
		// WorkDir is a private directory for clones, builds and output. It
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

// holdoutTag marks a scenario whose details builders must never see.
const holdoutTag = "holdout"

// Run clones the candidate and the suite at their exact commits, builds the
// candidate binaries, asks the suite for its declaration, runs it against the
// built binaries, and returns the CP4 report bound to everything that was
// tested. The report is also written to ReportDir as <digest>.json.
//
// A suite that cannot run, or whose output is not an eval.Report, is an error;
// a suite that runs but fails yields a report with Passed false.
func Run(ctx context.Context, p Plan) (Report, error) {
	candidateDir := filepath.Join(p.WorkDir, "candidate")
	suiteDir := filepath.Join(p.WorkDir, "suite")
	binDir := filepath.Join(p.WorkDir, "bin")
	if err := checkout(ctx, p.CandidateRepo, p.CandidateCommit, candidateDir); err != nil {
		return Report{}, fmt.Errorf("check out candidate: %w", err)
	}
	if err := checkout(ctx, p.SuiteRepo, p.SuiteCommit, suiteDir); err != nil {
		return Report{}, fmt.Errorf("check out suite: %w", err)
	}
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		return Report{}, fmt.Errorf("create build directory: %w", err)
	}
	binding := Binding{
		CandidateCommit: p.CandidateCommit,
		RequestRevision: p.RequestRevision,
		SuiteCommit:     p.SuiteCommit,
		Artifacts:       map[string]string{},
		Environment:     p.Environment,
		ConfigDigest:    p.ConfigDigest,
	}
	var artifactFlags []string
	for _, name := range slices.Sorted(maps.Keys(p.Binaries)) {
		out := filepath.Join(binDir, name)
		if err := goBuild(ctx, filepath.Join(candidateDir, p.CandidateModule), p.Binaries[name], out); err != nil {
			return Report{}, fmt.Errorf("build candidate %s: %w", name, err)
		}
		digest, err := sha256File(out)
		if err != nil {
			return Report{}, err
		}
		binding.Artifacts[name] = digest
		artifactFlags = append(artifactFlags, "--artifact", name+"="+out)
	}
	suiteBin := filepath.Join(binDir, "suite")
	if err := goBuild(ctx, filepath.Join(suiteDir, p.SuiteModule), p.SuiteCommand, suiteBin); err != nil {
		return Report{}, fmt.Errorf("build suite: %w", err)
	}
	suite, err := describe(ctx, suiteBin)
	if err != nil {
		return Report{}, err
	}
	binding.SuiteID = suite.ID
	evalReport, err := runSuite(ctx, suiteBin, p.WorkDir, artifactFlags)
	if err != nil {
		return Report{}, err
	}
	report, err := Assemble(binding, suite, evalReport)
	if err != nil {
		return Report{}, err
	}
	if err := writeReport(p.ReportDir, report); err != nil {
		return Report{}, err
	}
	return report, nil
}

// checkout clones repo into dir and checks out commit, detached. The source
// repository is only read.
func checkout(ctx context.Context, repo, commit, dir string) error {
	if _, err := command(ctx, "", "git", "clone", "--quiet", "--no-checkout", repo, dir); err != nil {
		return err
	}
	_, err := command(ctx, dir, "git", "-c", "advice.detachedHead=false", "checkout", "--quiet", "--detach", commit)
	return err
}

// goBuild builds one package of the module at dir into out.
func goBuild(ctx context.Context, dir, pkg, out string) error {
	_, err := command(ctx, dir, "go", "build", "-o", out, pkg)
	return err
}

// describe runs the suite's --describe and converts it into the declared
// scenario list.
func describe(ctx context.Context, suiteBin string) (Suite, error) {
	out, err := command(ctx, "", suiteBin, "--describe")
	if err != nil {
		return Suite{}, fmt.Errorf("describe suite: %w", err)
	}
	var d declaration
	if err := json.Unmarshal(out, &d); err != nil {
		return Suite{}, fmt.Errorf("decode suite declaration: %w", err)
	}
	s := Suite{ID: d.ID}
	for _, sc := range d.Scenarios {
		s.Scenarios = append(s.Scenarios, Scenario{ID: sc.ID, Holdout: slices.Contains(sc.Tags, holdoutTag)})
	}
	return s, nil
}

// runSuite runs the suite against the artifacts and decodes its eval.Report
// from standard output, which goes to a file rather than memory. Exit status 1
// means the suite ran and failed; any other non-zero status means it could not
// run.
func runSuite(ctx context.Context, suiteBin, workDir string, artifactFlags []string) (eval.Report, error) {
	outPath := filepath.Join(workDir, "suite-report.json")
	errPath := filepath.Join(workDir, "suite-stderr.log")
	stdout, err := os.Create(outPath)
	if err != nil {
		return eval.Report{}, fmt.Errorf("create suite output: %w", err)
	}
	defer stdout.Close()
	stderr, err := os.Create(errPath)
	if err != nil {
		return eval.Report{}, fmt.Errorf("create suite log: %w", err)
	}
	defer stderr.Close()
	cmd := exec.CommandContext(ctx, suiteBin, artifactFlags...)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	runErr := cmd.Run()
	var exitErr *exec.ExitError
	if runErr != nil && !(errors.As(runErr, &exitErr) && exitErr.ExitCode() == 1) {
		return eval.Report{}, fmt.Errorf("run suite (stderr in %s): %w", errPath, runErr)
	}
	f, err := os.Open(outPath)
	if err != nil {
		return eval.Report{}, fmt.Errorf("open suite output: %w", err)
	}
	defer f.Close()
	var r eval.Report
	if err := json.NewDecoder(f).Decode(&r); err != nil {
		return eval.Report{}, fmt.Errorf("decode suite report: %w", err)
	}
	return r, nil
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

// command runs name with args in dir and returns its standard output,
// including its standard error in the error when it fails.
func command(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
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
