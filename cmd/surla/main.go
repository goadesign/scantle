// Command surla runs the Surla MCP software factory control-plane server
// and CLI utilities (`surla serve`, `surla preview`, `surla diff`,
// `surla verify` and `surla approve`). Run `surla help` for usage.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"goa.design/goa-ai/features/model/anthropic"
	"goa.design/goa-ai/features/model/gateway"
	"goa.design/goa-ai/features/model/openai"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/goadesign/surla/internal/controlplane"
	"github.com/goadesign/surla/internal/designeval"
	"github.com/goadesign/surla/internal/diff"
	"github.com/goadesign/surla/internal/swarm"
	"github.com/goadesign/surla/internal/verify"
)

// serveConfig holds the optional `surla serve` flags that choose the CP1
// model and the swarm provider profiles. Zero values mean "not configured".
type serveConfig struct {
	// gatewayAddr is the host:port of a Goa-AI model gateway reached over
	// plaintext gRPC. It excludes provider and modelName.
	gatewayAddr string
	// provider is "anthropic" or "openai" for direct provider calls; it
	// requires modelName and the provider's API key environment variable.
	provider string
	// modelName is the provider model identifier used in direct mode.
	modelName string
	// swarmProfilesPath is the path of a JSON array of swarm.ProviderProfile.
	swarmProfilesPath string
}

// binaryFlags collects repeated `-binary name=pkg` flags for `surla verify`
// into verify.Plan.Binaries, rejecting malformed entries and repeated names.
type binaryFlags map[string]string

// artifactFlags collects repeated `-artifact name=sha256` flags for
// `surla approve` into verify.Binding.Artifacts: the digest of each candidate
// binary being promoted, measured by whoever runs the promotion.
type artifactFlags map[string]string

// approveSummary is what `surla approve` prints when the report matches the
// change being promoted.
type approveSummary struct {
	// ReportDigest identifies the approved report.
	ReportDigest string `json:"report_digest"`
	// Binding is the change the report was checked against.
	Binding verify.Binding `json:"binding"`
	// Counts covers every declared scenario, all of which passed.
	Counts verify.Counts `json:"counts"`
}

// usage is the text `surla`, `surla help`, `surla -h` and `surla --help`
// print. Each subcommand prints its own flags with `surla <subcommand> -h`.
const usage = `Surla checks changes to a Goa/Goa-AI service design before code is
written, and checks the built service against an evaluator suite before it is
promoted.

Usage:

  surla <command> [flags]

Commands:

  serve    run the Surla server that MCP clients (coding agents) connect to
  preview  print the architecture, agent policies and tool contracts a design
           module declares; needs no server and no model key
  diff     compare the designs in two module directories
  verify   build a candidate commit and run an evaluator suite against it,
           printing a verification report
  approve  check that a saved verification report passed and matches the
           change being promoted
  help     print this text, or "surla help <command>" for a command's flags
`

// apiKeyEnv maps each supported direct model provider to the environment
// variable that holds its API key. Keys are read only from the environment so
// they never appear in process listings or shell history.
var apiKeyEnv = map[string]string{
	"anthropic": "ANTHROPIC_API_KEY",
	"openai":    "OPENAI_API_KEY",
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "surla: %v\n", err)
		os.Exit(1)
	}
}

// run dispatches the surla subcommands. No arguments, `help`, `-h`, `-help`
// and `--help` print usage to stdout; `help <command>` and
// `<command> -h` print that command's flags to stderr (the flag package's
// output). A help request returns nil so the exit status is 0; an unknown
// command or a bad flag returns an error.
func run(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		_, err := fmt.Fprint(stdout, usage)
		return err
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "help", "-h", "-help", "--help":
		if sub == "help" && len(rest) == 1 {
			return run([]string{rest[0], "-h"}, stdout)
		}
		if len(rest) > 0 {
			return fmt.Errorf("unexpected arguments %q after %s", rest, sub)
		}
		_, err := fmt.Fprint(stdout, usage)
		return err
	}
	err := dispatch(sub, rest, stdout)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	return err
}

// dispatch runs the subcommand sub with its arguments. A `-h` flag makes the
// subcommand's flag parsing print its flags and return flag.ErrHelp.
func dispatch(sub string, args []string, stdout io.Writer) error {
	switch sub {
	case "serve":
		return runServe(args)
	case "preview":
		return runPreview(args, stdout)
	case "diff":
		return runDiff(args)
	case "verify":
		return runVerify(args)
	case "approve":
		return runApprove(args, stdout)
	default:
		return fmt.Errorf("unknown command %q; run \"surla help\" to list the commands", sub)
	}
}

// runServe starts the Surla HTTP and MCP JSON-RPC server protected by a
// verified bearer-token authenticator.
func runServe(args []string) (err error) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := fs.String("addr", "127.0.0.1:8090", "HTTP and MCP listen address")
	principalID := fs.String("principal", "", "Authenticated operator principal ID")
	authToken := fs.String("auth-token", os.Getenv("SURLA_AUTH_TOKEN"), "Bearer token required on HTTP and MCP requests")
	var cfg serveConfig
	fs.StringVar(&cfg.gatewayAddr, "model-gateway-addr", "", "CP1 model gateway host:port (plaintext gRPC; excludes -model-provider and -model-name)")
	fs.StringVar(&cfg.provider, "model-provider", "", "CP1 model provider: anthropic or openai (key from ANTHROPIC_API_KEY or OPENAI_API_KEY)")
	fs.StringVar(&cfg.modelName, "model-name", "", "CP1 model name for -model-provider")
	fs.StringVar(&cfg.swarmProfilesPath, "swarm-profiles", "", "JSON file listing the coding-agent provider profiles used for CP2 swarm plans")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*principalID) == "" || strings.TrimSpace(*authToken) == "" {
		return errors.New("serve requires -principal and -auth-token (or SURLA_AUTH_TOKEN)")
	}
	opts, closeModel, err := serveOptions(cfg, os.Getenv)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, closeModel()) }()
	auth := controlplane.NewTokenAuthenticator(map[string]controlplane.Principal{
		strings.TrimSpace(*authToken): {ID: strings.TrimSpace(*principalID)},
	})
	cp := controlplane.New(opts)
	srv := &http.Server{
		Handler:           controlplane.NewHTTPHandler(cp, auth),
		ReadHeaderTimeout: 10 * time.Second,
	}
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	writeServeStatus(os.Stderr, ln.Addr(), opts)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutCtx)
	}
}

// writeServeStatus tells the operator of `surla serve`, once the listener is
// bound, where MCP clients connect and which optional features are off. addr is
// the bound address, so port 0 reports the port actually assigned. The bearer
// token never appears.
func writeServeStatus(w io.Writer, addr net.Addr, opts controlplane.Options) {
	fmt.Fprintf(w, "surla: serving on http://%s\n", addr)
	fmt.Fprintf(w, "surla: connect MCP clients to http://%s/mcp with the bearer token\n", addr)
	if opts.Analyzer == nil {
		fmt.Fprintln(w, "surla: propose_change is off: no model is configured to analyze change requests"+
			" (set -model-provider and -model-name, or -model-gateway-addr)")
	}
	if len(opts.SwarmProfiles) == 0 {
		fmt.Fprintln(w, "surla: swarm plans are off: no coding-agent profiles are configured (set -swarm-profiles)")
	}
}

// runPreview reads the design module in -dir (default: the current directory)
// with readModuleSnapshot, evaluates it with designeval.Evaluate, and writes
// the result to w as indented JSON: the system name, declarations, C4
// containers and edges, agent policies and tool contracts. It records nothing
// and calls no model. SIGINT and SIGTERM cancel the evaluation.
func runPreview(args []string, w io.Writer) error {
	fs := flag.NewFlagSet("preview", flag.ContinueOnError)
	dir := fs.String("dir", ".", "root directory of the design module (holding go.mod, go.sum and design/)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments %q", fs.Args())
	}
	files, err := readModuleSnapshot(*dir)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	res, err := designeval.Evaluate(ctx, files)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(res)
}

// runDiff compares two design modules on disk, each read into a snapshot with
// readModuleSnapshot, and writes the structured diff JSON report to stdout.
func runDiff(args []string) error {
	fs := flag.NewFlagSet("diff", flag.ContinueOnError)
	oldDir := fs.String("old-dir", "", "root directory of the previous design module (holding go.mod and design/)")
	newDir := fs.String("new-dir", "", "root directory of the candidate design module (holding go.mod and design/)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *oldDir == "" || *newDir == "" {
		return errors.New("--old-dir and --new-dir are required")
	}
	oldFiles, err := readModuleSnapshot(*oldDir)
	if err != nil {
		return err
	}
	newFiles, err := readModuleSnapshot(*newDir)
	if err != nil {
		return err
	}
	rep, err := diff.Compare(context.Background(), oldFiles, newFiles)
	if err != nil {
		return err
	}
	res := &gensurla.DiffResult{
		HasChanges:           rep.HasChanges,
		AddedDeclarations:    rep.AddedDeclarations,
		RemovedDeclarations:  rep.RemovedDeclarations,
		ModifiedDeclarations: rep.ModifiedDeclarations,
		VersionDeltas:        rep.VersionDeltas,
		Summary:              rep.Summary,
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(res)
}

// runVerify builds the candidate commit in the OS sandbox, runs the evaluator
// suite commit against it with verify.Run, and writes the JSON report to
// stdout: the full report, or its builder view with -builder-view. verify.Run
// also writes the full report to -report-dir. A report that did not pass is
// still printed, and the command then fails so the exit status is 1. When
// verify.Run itself fails, nothing is printed and its error is returned.
// SIGINT and SIGTERM cancel the run.
func runVerify(args []string) error {
	plan, builderView, err := parseVerifyFlags(args)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	report, err := verify.Run(ctx, plan)
	if err != nil {
		return err
	}
	return writeVerifyReport(os.Stdout, report, builderView)
}

// writeVerifyReport writes report to w as indented JSON, or its builder view
// when builderView is set, and then returns an error when the report did not
// pass so `surla verify` exits 1 after printing it.
func writeVerifyReport(w io.Writer, report verify.Report, builderView bool) error {
	var out any = report
	if builderView {
		out = report.ForBuilders()
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return err
	}
	if !report.Passed {
		return fmt.Errorf("verification did not pass (report %s)", report.Digest)
	}
	return nil
}

// runApprove checks, with verify.Approve, that the saved report at -report
// passed and describes exactly the change given by the other flags, and
// prints an approveSummary to w. It saves nothing: no Change Card approval is
// recorded and nothing is merged. A report that is stale, did not pass, or
// cannot be read prints nothing and returns the error, so the exit status is 1.
func runApprove(args []string, w io.Writer) error {
	reportPath, current, err := parseApproveFlags(args)
	if err != nil {
		return err
	}
	f, err := os.Open(reportPath)
	if err != nil {
		return fmt.Errorf("open report: %w", err)
	}
	defer f.Close()
	report, err := verify.DecodeReport(f)
	if err != nil {
		return err
	}
	if err := verify.Approve(current, report); err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(approveSummary{ReportDigest: report.Digest, Binding: current, Counts: report.Counts})
}

// parseApproveFlags turns `surla approve` arguments into the report path and
// the binding of the change being promoted. Every flag is required and must
// be given explicitly, because each one is a fact the report is compared with.
func parseApproveFlags(args []string) (string, verify.Binding, error) {
	fs := flag.NewFlagSet("approve", flag.ContinueOnError)
	artifacts := artifactFlags{}
	var reportPath string
	var b verify.Binding
	fs.StringVar(&reportPath, "report", "", "path of the full verification report written by surla verify")
	fs.StringVar(&b.CandidateCommit, "candidate-commit", "", "full commit ID of the candidate being promoted")
	fs.StringVar(&b.RequestRevision, "request-revision", "", "current request_revision of the confirmed change")
	fs.StringVar(&b.SuiteID, "suite-id", "", "evaluator suite the promotion requires")
	fs.StringVar(&b.SuiteCommit, "suite-commit", "", "evaluator suite commit the promotion requires")
	fs.StringVar(&b.Environment, "environment", "", "dependency profile the promotion requires, such as sim")
	fs.Var(artifacts, "artifact", "candidate binary being promoted as name=sha256 (repeatable)")
	if err := fs.Parse(args); err != nil {
		return "", verify.Binding{}, err
	}
	if fs.NArg() > 0 {
		return "", verify.Binding{}, fmt.Errorf("unexpected arguments %q", fs.Args())
	}
	required := []struct {
		flag  string
		value string
	}{
		{"-report", reportPath},
		{"-candidate-commit", b.CandidateCommit},
		{"-request-revision", b.RequestRevision},
		{"-suite-id", b.SuiteID},
		{"-suite-commit", b.SuiteCommit},
		{"-environment", b.Environment},
	}
	var missing []string
	for _, r := range required {
		if r.value == "" {
			missing = append(missing, r.flag)
		}
	}
	if len(artifacts) == 0 {
		missing = append(missing, "-artifact")
	}
	if len(missing) > 0 {
		return "", verify.Binding{}, fmt.Errorf("approve requires %s", strings.Join(missing, ", "))
	}
	b.Artifacts = artifacts
	return reportPath, b, nil
}

// String renders the collected artifacts as sorted name=sha256 pairs.
func (a artifactFlags) String() string {
	pairs := make([]string, 0, len(a))
	for name, digest := range a {
		pairs = append(pairs, name+"="+digest)
	}
	slices.Sort(pairs)
	return strings.Join(pairs, ",")
}

// Set records one name=sha256 entry. The name must be non-empty and appear
// only once, and the digest must be 64 lowercase hex characters, the form
// surla verify records.
func (a artifactFlags) Set(v string) error {
	name, digest, ok := strings.Cut(v, "=")
	if !ok || name == "" || !isSHA256Hex(digest) {
		return fmt.Errorf("artifact %q must be name=<64 lowercase hex sha256>", v)
	}
	if _, dup := a[name]; dup {
		return fmt.Errorf("artifact name %q is repeated", name)
	}
	a[name] = digest
	return nil
}

// String renders the collected binaries as sorted name=pkg pairs.
func (b binaryFlags) String() string {
	pairs := make([]string, 0, len(b))
	for name, pkg := range b {
		pairs = append(pairs, name+"="+pkg)
	}
	slices.Sort(pairs)
	return strings.Join(pairs, ",")
}

// Set records one name=pkg entry. Both parts must be non-empty, and a name may
// appear only once.
func (b binaryFlags) Set(v string) error {
	name, pkg, ok := strings.Cut(v, "=")
	if !ok || name == "" || pkg == "" {
		return fmt.Errorf("binary %q must be name=package", v)
	}
	if _, dup := b[name]; dup {
		return fmt.Errorf("binary name %q is repeated", name)
	}
	b[name] = pkg
	return nil
}

// parseVerifyFlags turns `surla verify` arguments into a verify.Plan and
// reports whether the builder view was requested. -candidate-module and
// -suite-module default to ".", the repository root; every other plan field is
// required and must be given explicitly. Commit IDs and repositories are
// checked by verify.Run.
func parseVerifyFlags(args []string) (verify.Plan, bool, error) {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	binaries := binaryFlags{}
	var p verify.Plan
	fs.StringVar(&p.CandidateRepo, "candidate-repo", "", "git repository holding the candidate")
	fs.StringVar(&p.CandidateCommit, "candidate-commit", "", "full commit ID of the candidate to build")
	fs.StringVar(&p.CandidateModule, "candidate-module", ".", "candidate Go module directory inside -candidate-repo")
	fs.Var(binaries, "binary", "candidate binary as name=package inside -candidate-module (repeatable)")
	fs.StringVar(&p.SuiteRepo, "suite-repo", "", "git repository holding the evaluator suite")
	fs.StringVar(&p.SuiteCommit, "suite-commit", "", "full commit ID of the evaluator suite")
	fs.StringVar(&p.SuiteModule, "suite-module", ".", "suite Go module directory inside -suite-repo")
	fs.StringVar(&p.SuiteCommand, "suite-command", "", "suite command package inside -suite-module")
	fs.StringVar(&p.RequestRevision, "request-revision", "", "request_revision of the confirmed change being verified")
	fs.StringVar(&p.WorkDir, "work-dir", "", "private directory for clones, builds and evidence; keep it away from builders")
	fs.StringVar(&p.ReportDir, "report-dir", "", "directory that receives the full report as <digest>.json")
	builderView := fs.Bool("builder-view", false, "print the builder view, which hides holdout scenario details, instead of the full report")
	if err := fs.Parse(args); err != nil {
		return verify.Plan{}, false, err
	}
	if fs.NArg() > 0 {
		return verify.Plan{}, false, fmt.Errorf("unexpected arguments %q", fs.Args())
	}
	required := []struct {
		flag  string
		value string
	}{
		{"-candidate-repo", p.CandidateRepo},
		{"-candidate-commit", p.CandidateCommit},
		{"-suite-repo", p.SuiteRepo},
		{"-suite-commit", p.SuiteCommit},
		{"-suite-command", p.SuiteCommand},
		{"-request-revision", p.RequestRevision},
		{"-work-dir", p.WorkDir},
		{"-report-dir", p.ReportDir},
	}
	var missing []string
	for _, r := range required {
		if r.value == "" {
			missing = append(missing, r.flag)
		}
	}
	if len(binaries) == 0 {
		missing = append(missing, "-binary")
	}
	if len(missing) > 0 {
		return verify.Plan{}, false, fmt.Errorf("verify requires %s", strings.Join(missing, ", "))
	}
	p.Binaries = binaries
	return p, *builderView, nil
}

// isSHA256Hex reports whether s is a lowercase hex SHA-256 digest.
func isSHA256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// readModuleSnapshot reads the module rooted at dir into a design snapshot:
// its go.mod, go.sum and every non-test .go file, keyed by slash-separated
// path relative to dir. Hidden, vendor and testdata directories and nested
// modules are skipped. A symbolic link anywhere in the module is an error, and
// files are opened without following links, so the snapshot holds only files
// that really are inside dir.
func readModuleSnapshot(dir string) (map[string]string, error) {
	root, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	files := map[string]string{}
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symbolic link; a design snapshot holds only regular files", p)
		}
		if d.IsDir() {
			name := d.Name()
			switch {
			case rel == ".":
				return nil
			case strings.HasPrefix(name, ".") || name == "vendor" || name == "testdata":
				return filepath.SkipDir
			}
			if _, err := os.Lstat(filepath.Join(p, "go.mod")); err == nil {
				return filepath.SkipDir
			}
			return nil
		}
		slash := filepath.ToSlash(rel)
		keep := slash == "go.mod" || slash == "go.sum" ||
			(strings.HasSuffix(slash, ".go") && !strings.HasSuffix(slash, "_test.go"))
		if !keep || !d.Type().IsRegular() {
			return nil
		}
		f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
		if err != nil {
			return fmt.Errorf("read %s: %w", p, err)
		}
		defer f.Close()
		b, err := io.ReadAll(f)
		if err != nil {
			return fmt.Errorf("read %s: %w", p, err)
		}
		files[slash] = string(b)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

// serveOptions builds the control-plane Options for `surla serve` from cfg.
// getenv supplies provider API keys. The returned close function releases the
// model gateway connection, if any, and returns nil when no connection was
// opened. With no model flags the Analyzer stays nil, and with no
// -swarm-profiles the profiles stay nil, so the server still answers diff,
// golden-path and compiled_rule requests without credentials.
func serveOptions(cfg serveConfig, getenv func(string) string) (controlplane.Options, func() error, error) {
	profiles, err := loadSwarmProfiles(cfg.swarmProfilesPath)
	if err != nil {
		return controlplane.Options{}, nil, err
	}
	analyzer, closeModel, err := newCP1Analyzer(cfg, getenv)
	if err != nil {
		return controlplane.Options{}, nil, err
	}
	return controlplane.Options{Analyzer: analyzer, SwarmProfiles: profiles}, closeModel, nil
}

// newCP1Analyzer returns the CP1 analyzer selected by cfg: a model gateway
// client when gatewayAddr is set, a direct provider client when provider and
// modelName are set, or nil when neither is set. Mixed or partial model flags
// and a missing provider API key are errors; key values never appear in them.
func newCP1Analyzer(cfg serveConfig, getenv func(string) string) (controlplane.CP1Analyzer, func() error, error) {
	noop := func() error { return nil }
	switch {
	case cfg.gatewayAddr != "" && (cfg.provider != "" || cfg.modelName != ""):
		return nil, nil, errors.New("-model-gateway-addr cannot be combined with -model-provider or -model-name; the gateway chooses the model")
	case cfg.gatewayAddr != "":
		conn, err := grpc.NewClient(cfg.gatewayAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			return nil, nil, fmt.Errorf("connect model gateway %q: %w", cfg.gatewayAddr, err)
		}
		client, err := gateway.NewGRPCClient(conn)
		if err != nil {
			return nil, nil, errors.Join(fmt.Errorf("model gateway client: %w", err), conn.Close())
		}
		return controlplane.NewModelCP1Analyzer(client), conn.Close, nil
	case cfg.provider == "" && cfg.modelName == "":
		return nil, noop, nil
	case cfg.provider == "" || cfg.modelName == "":
		return nil, nil, errors.New("-model-provider and -model-name must be set together")
	}
	env, ok := apiKeyEnv[cfg.provider]
	if !ok {
		return nil, nil, fmt.Errorf("unsupported -model-provider %q (expected \"anthropic\" or \"openai\")", cfg.provider)
	}
	key := getenv(env)
	if key == "" {
		return nil, nil, fmt.Errorf("-model-provider %s requires %s", cfg.provider, env)
	}
	newClient := anthropic.NewFromAPIKey
	if cfg.provider == "openai" {
		newClient = openai.NewFromAPIKey
	}
	client, err := newClient(key, cfg.modelName)
	if err != nil {
		return nil, nil, fmt.Errorf("%s model client: %w", cfg.provider, err)
	}
	return controlplane.NewModelCP1Analyzer(client), noop, nil
}

// loadSwarmProfiles decodes the JSON array of swarm.ProviderProfile at path,
// rejecting null, unknown fields and trailing data, then rejects malformed
// entries (duplicate IDs, empty required fields, unknown capabilities) with
// swarm.Plan's own profile checks. Plan is given no builders, so only its
// configuration error matters here; whether the roster is ready is decided per
// CP2 request. An empty path means no profiles are configured.
func loadSwarmProfiles(path string) ([]swarm.ProviderProfile, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read -swarm-profiles: %w", err)
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	var profiles *[]swarm.ProviderProfile
	if err := dec.Decode(&profiles); err != nil {
		return nil, fmt.Errorf("decode -swarm-profiles %s: %w", path, err)
	}
	if profiles == nil {
		return nil, fmt.Errorf("decode -swarm-profiles %s: expected a JSON array, got null", path)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("decode -swarm-profiles %s: trailing data after the profile array", path)
	}
	if _, err := swarm.Plan(swarm.Request{Profiles: *profiles}); err != nil {
		return nil, fmt.Errorf("-swarm-profiles %s: %w", path, err)
	}
	return *profiles, nil
}
