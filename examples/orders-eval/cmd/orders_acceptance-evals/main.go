// Command orders_acceptance-evals runs the evaluator-owned orders acceptance
// suite against built candidate binaries.
//
// It follows Scantle's suite command contract:
//
//   - --describe prints the suite declaration as JSON ({"id", "scenarios":
//     [{"id", "tags"}]}) and exits; it needs no artifacts.
//   - Otherwise every candidate binary is passed as --artifact name=path. The
//     suite pins each one, runs the selected scenarios, and writes the
//     eval.Report as JSON to standard output. Each scenario result records
//     the digests of the binaries it could execute.
//
// It exits 1 when the suite fails and 2 when it cannot run.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/goadesign/scantle/examples/orders-eval/acceptance"
	genevals "github.com/goadesign/scantle/examples/orders-eval/gen/evals/orders_acceptance"
	"github.com/goadesign/scantle/examples/orders-eval/harness"
	"github.com/goadesign/scantle/verify/candidate"
	"goa.design/goa-ai/eval"
)

type (
	// listFlag collects a repeatable string flag.
	listFlag []string

	// declaration is the suite description printed by --describe.
	declaration struct {
		// ID is the suite identifier.
		ID string `json:"id"`
		// Scenarios lists every declared scenario in declaration order.
		Scenarios []declaredScenario `json:"scenarios"`
	}

	// declaredScenario is one scenario of the declaration.
	declaredScenario struct {
		// ID is the scenario identifier.
		ID string `json:"id"`
		// Tags are the scenario's declared tags, such as "holdout".
		Tags []string `json:"tags"`
	}
)

func main() {
	var scenarios, tags, artifacts listFlag
	describe := flag.Bool("describe", false, "print the suite declaration as JSON and exit")
	concurrency := flag.Int("max-concurrency", 4, "maximum scenarios in flight")
	flag.Var(&artifacts, "artifact", "candidate binary as name=path (repeatable)")
	flag.Var(&scenarios, "scenario", "scenario to run (repeatable)")
	flag.Var(&tags, "tag", "run scenarios with this tag (repeatable)")
	flag.Parse()

	if *describe {
		if err := printDeclaration(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	report, err := run(ctx, artifacts, *concurrency, scenarios, tags)
	if report != nil {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if encErr := enc.Encode(report); encErr != nil {
			err = errors.Join(err, encErr)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if !report.Passed {
		os.Exit(1)
	}
}

// printDeclaration writes the suite declaration as JSON to standard output.
func printDeclaration() error {
	suite, err := genevals.New(&acceptance.Hooks{}, genevals.Inputs{})
	if err != nil {
		return err
	}
	d := declaration{ID: suite.ID}
	for _, s := range suite.Scenarios {
		d.Scenarios = append(d.Scenarios, declaredScenario{ID: s.ID, Tags: s.Tags})
	}
	return json.NewEncoder(os.Stdout).Encode(d)
}

// run pins the artifacts, prepares the environment and runs the selected
// scenarios.
func run(ctx context.Context, artifacts []string, concurrency int, scenarios, tags []string) (*eval.Report, error) {
	paths, err := parseArtifacts(artifacts)
	if err != nil {
		return nil, err
	}
	if len(scenarios) > 0 && len(tags) > 0 {
		return nil, errors.New("--scenario and --tag cannot be combined")
	}
	store, err := os.MkdirTemp("", "orders-eval-artifacts-*")
	if err != nil {
		return nil, fmt.Errorf("create artifact store: %w", err)
	}
	defer func() {
		if rmErr := candidate.RemoveStore(store); rmErr != nil {
			fmt.Fprintln(os.Stderr, rmErr)
		}
	}()
	orders, err := candidate.Pin(store, paths["orders"])
	if err != nil {
		return nil, err
	}
	worker, err := candidate.Pin(store, paths["orders-worker"])
	if err != nil {
		return nil, err
	}
	env, err := harness.NewEnv(orders, worker)
	if err != nil {
		return nil, err
	}
	suite, err := genevals.New(acceptance.New(env), genevals.Inputs{})
	if err != nil {
		return nil, err
	}
	runner, err := eval.NewRunner(nil, eval.RunnerConfig{MaxConcurrency: concurrency})
	if err != nil {
		return nil, err
	}
	var report eval.Report
	switch {
	case len(scenarios) > 0:
		report, err = runner.RunScenarios(ctx, suite, scenarios...)
	case len(tags) > 0:
		report, err = runner.RunTags(ctx, suite, tags...)
	default:
		report, err = runner.Run(ctx, suite)
	}
	return &report, err
}

// parseArtifacts maps name=path flags and requires exactly the orders and
// orders-worker binaries.
func parseArtifacts(flags []string) (map[string]string, error) {
	paths := map[string]string{}
	for _, f := range flags {
		name, path, ok := strings.Cut(f, "=")
		if !ok || name == "" || path == "" {
			return nil, fmt.Errorf("--artifact %q must be name=path", f)
		}
		if _, dup := paths[name]; dup {
			return nil, fmt.Errorf("--artifact %s given twice", name)
		}
		paths[name] = path
	}
	for _, name := range []string{"orders", "orders-worker"} {
		if paths[name] == "" {
			return nil, fmt.Errorf("--artifact %s=<path> is required", name)
		}
	}
	if len(paths) != 2 {
		return nil, errors.New("only the orders and orders-worker artifacts are accepted")
	}
	return paths, nil
}

// String returns the collected values.
func (l *listFlag) String() string {
	return strings.Join(*l, ",")
}

// Set appends one value.
func (l *listFlag) Set(v string) error {
	*l = append(*l, v)
	return nil
}
