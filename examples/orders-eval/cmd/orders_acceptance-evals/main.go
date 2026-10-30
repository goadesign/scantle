// Command orders_acceptance-evals runs the evaluator-owned orders acceptance
// suite against built candidate binaries and writes the eval.Report as JSON to
// standard output. It exits 1 when the suite fails and 2 when it cannot run.
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

// listFlag collects a repeatable string flag.
type listFlag []string

func main() {
	var scenarios, tags listFlag
	ordersBin := flag.String("orders-bin", "", "candidate orders API binary")
	workerBin := flag.String("worker-bin", "", "candidate refund worker binary")
	concurrency := flag.Int("max-concurrency", 4, "maximum scenarios in flight")
	flag.Var(&scenarios, "scenario", "scenario to run (repeatable)")
	flag.Var(&tags, "tag", "run scenarios with this tag (repeatable)")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	report, err := run(ctx, *ordersBin, *workerBin, *concurrency, scenarios, tags)
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

// run validates flags, prepares the environment and runs the selected
// scenarios.
func run(ctx context.Context, ordersBin, workerBin string, concurrency int, scenarios, tags []string) (*eval.Report, error) {
	if ordersBin == "" || workerBin == "" {
		return nil, errors.New("--orders-bin and --worker-bin are required")
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
	orders, err := candidate.Pin(store, ordersBin)
	if err != nil {
		return nil, err
	}
	worker, err := candidate.Pin(store, workerBin)
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

// String returns the collected values.
func (l *listFlag) String() string {
	return strings.Join(*l, ",")
}

// Set appends one value.
func (l *listFlag) Set(v string) error {
	*l = append(*l, v)
	return nil
}
