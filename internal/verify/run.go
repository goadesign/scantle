package verify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/goadesign/scantle/internal/procgroup"
	"github.com/goadesign/scantle/verify/candidate"
	"goa.design/goa-ai/eval"
)

type (
	// Plan says what to verify: an exact candidate commit, the binaries to
	// build from it, and an exact evaluator suite commit.
	Plan struct {
		// CandidateRepo is the git repository holding the candidate.
		CandidateRepo string
		// CandidateCommit is the full commit ID to build, in the repository's
		// object format. Branch names and other revision expressions are
		// rejected.
		CandidateCommit string
		// CandidateModule is the Go module directory inside the candidate
		// repository that the binaries are built from, such as
		// "examples/orders" or ".". After symbolic links are resolved it must
		// still be inside the checkout.
		CandidateModule string
		// Binaries maps each artifact name to its package path inside
		// CandidateModule, such as "./cmd/orders". A name is the artifact ID
		// the suite receives as --artifact name=path: it must be non-empty,
		// not "." or "..", and contain no "/", "\\", "=" or NUL. Each package
		// path is "." or starts with "./", and after symbolic links are
		// resolved it must name a directory inside CandidateModule.
		Binaries map[string]string
		// SuiteRepo is the git repository holding the evaluator suite.
		SuiteRepo string
		// SuiteCommit is the full ID of the suite commit to run.
		SuiteCommit string
		// SuiteModule is the suite's Go module directory inside SuiteRepo.
		// Like CandidateModule, it must resolve to a directory inside the
		// checkout.
		SuiteModule string
		// SuiteCommand is the suite command's package path inside
		// SuiteModule, following the suite command contract. Like a candidate
		// package path, it is "." or starts with "./" and must resolve to a
		// directory inside SuiteModule.
		SuiteCommand string
		// RequestRevision identifies the confirmed request being verified.
		RequestRevision string
		// WorkDir is a private directory for clones, builds and evidence. It
		// must not be shared with the candidate's builder. It is required and
		// must be missing or empty, so everything a build can read under it was
		// written by this run; a relative path is made absolute against the
		// current directory.
		WorkDir string
		// ReportDir receives the report, named by its digest.
		ReportDir string
	}

	// declaration is the suite's --describe output.
	declaration struct {
		// ID is the suite identifier.
		ID string `json:"id"`
		// Environment is the dependency profile the suite runs with.
		Environment string `json:"environment"`
		// Scenarios lists every declared scenario.
		Scenarios []struct {
			// ID is the scenario identifier.
			ID string `json:"id"`
			// Tags are the scenario's declared tags.
			Tags []string `json:"tags"`
		} `json:"scenarios"`
	}
)

// commitID matches a full commit ID in either Git object format: 40 hex digits
// for SHA-1 repositories, 64 for SHA-256 ones. The checkout then requires HEAD
// to equal it, which only the full ID in the repository's own format can.
var commitID = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

const (
	// holdoutTag marks a scenario whose details builders must never see.
	holdoutTag = "holdout"
	// maxErrorExcerpt bounds how much of a failed command's output an error
	// message quotes; the full output stays in the evidence directory.
	maxErrorExcerpt = 4 << 10
	// suiteStopGrace is how long the suite's process group gets to clean up
	// after SIGTERM before it is killed.
	suiteStopGrace = 30 * time.Second
	// groupStopTimeout bounds waiting for a killed process group to empty.
	groupStopTimeout = 30 * time.Second
)

// Run clones the candidate at its exact commit and builds its binaries into
// WorkDir/candidate-bin, then clones the suite at its exact commit, builds it
// into WorkDir/suite-bin, asks it for its declaration, runs it against the
// built binaries, and returns the CP4 report bound to everything that was
// tested. The report is also written to ReportDir as <digest>.json.
//
// Both builds go through candidate.Build: an OS sandbox in which the build
// reads nothing but its source tree, the toolchain, the module cache and its
// own directories under WorkDir, writes nothing but those directories, and has
// no network. The candidate's source tree is its module; the suite's is its
// whole checkout, so it may replace sibling modules there. A committed link or
// replace directive can reach only those trees: the bound commit, the trusted
// toolchain and checksum-verified modules, and directories this run created
// empty, because Run refuses a WorkDir that already has contents. The suite is
// checked out only after the candidate is built. Every command's
// output goes to files under WorkDir/evidence; errors quote only a bounded
// excerpt.
//
// A suite that produces no decodable eval.Report is an error. Otherwise the
// report keeps the suite's evidence and exit status: a suite that ran and
// failed, or that exited abnormally after writing partial results, yields a
// report that does not pass.
func Run(ctx context.Context, p Plan) (Report, error) {
	for name, commit := range map[string]string{"candidate": p.CandidateCommit, "suite": p.SuiteCommit} {
		if !commitID.MatchString(commit) {
			return Report{}, fmt.Errorf("%s commit %q is not a full commit ID", name, commit)
		}
	}
	for name, pkg := range p.Binaries {
		if err := validateArtifactName(name); err != nil {
			return Report{}, err
		}
		if err := validatePackagePath(pkg); err != nil {
			return Report{}, fmt.Errorf("binary %q: %w", name, err)
		}
	}
	if err := validatePackagePath(p.SuiteCommand); err != nil {
		return Report{}, fmt.Errorf("suite command: %w", err)
	}
	if p.WorkDir == "" {
		return Report{}, errors.New("work directory is required")
	}
	workDir, err := filepath.Abs(p.WorkDir)
	if err != nil {
		return Report{}, fmt.Errorf("work directory: %w", err)
	}
	p.WorkDir = workDir
	switch entries, err := os.ReadDir(p.WorkDir); {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return Report{}, fmt.Errorf("work directory: %w", err)
	case len(entries) > 0:
		return Report{}, fmt.Errorf("work directory %s must be missing or empty", p.WorkDir)
	}
	evidenceDir := filepath.Join(p.WorkDir, "evidence")
	candidateBinDir := filepath.Join(p.WorkDir, "candidate-bin")
	suiteBinDir := filepath.Join(p.WorkDir, "suite-bin")
	for _, dir := range []string{evidenceDir, candidateBinDir, suiteBinDir} {
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
	}

	candidateDir := filepath.Join(p.WorkDir, "candidate")
	if err := r.checkout(ctx, "candidate", p.CandidateRepo, p.CandidateCommit, candidateDir); err != nil {
		return Report{}, err
	}
	moduleDir, err := resolveInside(candidateDir, p.CandidateModule)
	if err != nil {
		return Report{}, fmt.Errorf("candidate module %q: %w", p.CandidateModule, err)
	}
	for name, pkg := range p.Binaries {
		if _, err := resolveInside(moduleDir, pkg); err != nil {
			return Report{}, fmt.Errorf("binary %q package %q: %w", name, pkg, err)
		}
	}
	toolchain, err := r.prepareBuild(ctx, "candidate", moduleDir, p.WorkDir)
	if err != nil {
		return Report{}, err
	}
	var artifactFlags []string
	for _, name := range slices.Sorted(maps.Keys(p.Binaries)) {
		out := filepath.Join(candidateBinDir, name)
		if err := candidate.Build(ctx, candidate.BuildSpec{
			SourceDir: moduleDir, ModuleDir: moduleDir, Package: p.Binaries[name], Out: out,
			GOROOT: toolchain.goroot, ModCache: toolchain.modCache, WorkDir: toolchain.workDir,
			LogFile: filepath.Join(r.evidenceDir, "build-"+name+".log"),
		}); err != nil {
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
	suiteModuleDir, err := resolveInside(suiteDir, p.SuiteModule)
	if err != nil {
		return Report{}, fmt.Errorf("suite module %q: %w", p.SuiteModule, err)
	}
	if _, err := resolveInside(suiteModuleDir, p.SuiteCommand); err != nil {
		return Report{}, fmt.Errorf("suite command %q: %w", p.SuiteCommand, err)
	}
	suiteToolchain, err := r.prepareBuild(ctx, "suite", suiteModuleDir, p.WorkDir)
	if err != nil {
		return Report{}, err
	}
	// The suite builds in the same sandbox as the candidate, readable only
	// within its own checkout: its module may replace sibling modules there,
	// but no link or replace can pull in source outside SuiteCommit.
	suiteBin := filepath.Join(suiteBinDir, "suite")
	if err := candidate.Build(ctx, candidate.BuildSpec{
		SourceDir: suiteDir, ModuleDir: suiteModuleDir, Package: p.SuiteCommand, Out: suiteBin,
		GOROOT: suiteToolchain.goroot, ModCache: suiteToolchain.modCache, WorkDir: suiteToolchain.workDir,
		LogFile: filepath.Join(r.evidenceDir, "build-suite.log"),
	}); err != nil {
		return Report{}, fmt.Errorf("build suite: %w", err)
	}
	suite, err := r.describe(ctx, suiteBin)
	if err != nil {
		return Report{}, err
	}
	binding.SuiteID, binding.Environment = suite.ID, suite.Environment
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

// resolveInside resolves rel, a path relative to the directory root, following
// every symbolic link in root and rel, and returns the resolved path. It fails
// when the path does not exist or is not root itself or inside it, so a
// committed link cannot point a build at source outside the commit the report
// names.
func resolveInside(root, rel string) (string, error) {
	base, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", root, err)
	}
	dir, err := filepath.EvalSymlinks(filepath.Join(base, rel))
	if err != nil {
		return "", err
	}
	inner, err := filepath.Rel(base, dir)
	if err != nil || !filepath.IsLocal(inner) {
		return "", fmt.Errorf("resolves to %s, outside %s", dir, base)
	}
	return dir, nil
}

// validatePackagePath rejects a Go package argument that is not "." or a
// "./"-relative directory, such as a standard library import path or a go
// build flag, before it reaches `go build`.
func validatePackagePath(pkg string) error {
	if pkg != "." && !strings.HasPrefix(pkg, "./") {
		return fmt.Errorf("package path %q must be \".\" or start with \"./\"", pkg)
	}
	return nil
}

// validateArtifactName rejects a binary name that cannot serve both as the
// artifact ID passed to the suite as --artifact name=path and as one file name
// inside WorkDir/candidate-bin and WorkDir/evidence.
func validateArtifactName(name string) error {
	switch {
	case name == "", name == ".", name == "..":
		return fmt.Errorf("binary name %q is not a valid artifact name", name)
	case strings.ContainsAny(name, "/\\=\x00"):
		return fmt.Errorf("binary name %q must not contain /, \\, = or NUL", name)
	}
	return nil
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
		"-c", "advice.detachedHead=false", "checkout", "--quiet", "--detach", commit+"^{commit}"); err != nil {
		return fmt.Errorf("check out %s: %w", label, err)
	}
	if err := r.run(ctx, "head-"+label, dir, nil, "git", "rev-parse", "HEAD"); err != nil {
		return fmt.Errorf("check out %s: %w", label, err)
	}
	head, err := os.ReadFile(filepath.Join(r.evidenceDir, "head-"+label+".stdout"))
	if err != nil {
		return fmt.Errorf("read %s head: %w", label, err)
	}
	if got := strings.TrimSpace(string(head)); got != commit {
		return fmt.Errorf("check out %s: HEAD is %s, want %s", label, got, commit)
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
	s := Suite{ID: d.ID, Environment: d.Environment}
	for _, sc := range d.Scenarios {
		s.Scenarios = append(s.Scenarios, Scenario{ID: sc.ID, Holdout: slices.Contains(sc.Tags, holdoutTag)})
	}
	return s, nil
}

// runSuite runs the suite in its own process group and returns its eval.Report
// and exit status. Cancellation sends SIGTERM to the whole group, so the
// suite's candidate and Temporal processes stop with it. After the suite
// process ends, every process left in its group is killed and runSuite waits
// until the group is empty before returning; a group that does not empty in
// time is an error and its state is not released. A report is decoded
// whatever the outcome; a canceled or abnormal end gets exit status -1 and
// never passes. Only a missing or undecodable report is an error.
func (r *runner) runSuite(ctx context.Context, suiteBin string, artifactFlags []string) (eval.Report, int, error) {
	stdout, stderr, err := r.outputs("suite")
	if err != nil {
		return eval.Report{}, 0, err
	}
	defer stdout.Close()
	defer stderr.Close()
	cmd := exec.CommandContext(ctx, suiteBin, artifactFlags...)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	runErr, stopErr := procgroup.Run(cmd, syscall.SIGTERM, suiteStopGrace, groupStopTimeout)
	if stopErr != nil {
		return eval.Report{}, 0, fmt.Errorf("stop suite processes: %w", stopErr)
	}
	var report eval.Report
	decodeErr := decodeFile(filepath.Join(r.evidenceDir, "suite.stdout"), &report)
	exit := 0
	var exitErr *exec.ExitError
	switch {
	case runErr == nil:
	case errors.As(runErr, &exitErr):
		exit = exitErr.ExitCode()
	default:
		// Canceled or otherwise not a clean exit: whatever the suite reported
		// is kept, and the verification can never pass.
		exit = -1
	}
	if decodeErr != nil {
		return eval.Report{}, 0, fmt.Errorf("suite ended (%v) without a decodable report: %w%s", runErr, decodeErr, r.excerpt("suite"))
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

// buildToolchain is what a sandboxed candidate build runs with.
type buildToolchain struct {
	// goroot is the resolved toolchain the candidate module selects.
	goroot string
	// modCache is the module cache holding the candidate's dependencies.
	modCache string
	// workDir is the build's private HOME, TMPDIR and GOCACHE.
	workDir string
}

// prepareBuild resolves the toolchain the module at moduleDir selects and
// downloads its module dependencies, outside the sandbox, for the build named
// label ("candidate" or "suite"). These steps fetch checksum-verified modules
// and official toolchains and compile or execute no checked-out code; the
// sandboxed build afterwards runs with no network. Evidence files and the
// build home are named by label so the two builds never share them.
func (r *runner) prepareBuild(ctx context.Context, label, moduleDir, workDir string) (buildToolchain, error) {
	if err := r.run(ctx, "go-mod-download-"+label, moduleDir, nil, "go", "mod", "download"); err != nil {
		return buildToolchain{}, fmt.Errorf("download %s modules: %w", label, err)
	}
	if err := r.run(ctx, "go-env-"+label, moduleDir, nil, "go", "env", "GOROOT", "GOMODCACHE"); err != nil {
		return buildToolchain{}, fmt.Errorf("resolve %s toolchain: %w", label, err)
	}
	out, err := os.ReadFile(filepath.Join(r.evidenceDir, "go-env-"+label+".stdout"))
	if err != nil {
		return buildToolchain{}, fmt.Errorf("read toolchain: %w", err)
	}
	lines := strings.Fields(string(out))
	if len(lines) != 2 {
		return buildToolchain{}, fmt.Errorf("unexpected go env output %q", out)
	}
	t := buildToolchain{goroot: lines[0], modCache: lines[1], workDir: filepath.Join(workDir, "build-home-"+label)}
	if err := os.MkdirAll(t.workDir, 0o700); err != nil {
		return buildToolchain{}, fmt.Errorf("create build home: %w", err)
	}
	return t, nil
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
