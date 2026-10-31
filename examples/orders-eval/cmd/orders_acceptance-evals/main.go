// Command orders_acceptance-evals runs the evaluator-owned orders acceptance
// suite against built candidate binaries, following Surla's suite command
// contract (see github.com/goadesign/surla/verify/suite): --describe prints
// the declaration, and --artifact orders=path --artifact orders-worker=path
// runs the scenarios against those binaries and prints the eval.Report.
package main

import (
	"goa.design/goa-ai/eval"

	"github.com/goadesign/surla/examples/orders-eval/acceptance"
	genevals "github.com/goadesign/surla/examples/orders-eval/gen/evals/orders_acceptance"
	"github.com/goadesign/surla/examples/orders-eval/harness"
	"github.com/goadesign/surla/verify/candidate"
	"github.com/goadesign/surla/verify/suite"
)

func main() {
	suite.Main(suite.Command{
		Environment:    harness.Environment,
		Binaries:       []string{"orders", "orders-worker"},
		MaxConcurrency: 4,
		Suite:          newSuite,
	})
}

// newSuite returns the acceptance suite running the pinned orders API and
// refund worker. For --describe pinned is nil and the scenarios never run.
func newSuite(pinned map[string]candidate.Artifact) (eval.Suite, error) {
	env, err := harness.NewEnv(pinned["orders"], pinned["orders-worker"])
	if err != nil {
		return eval.Suite{}, err
	}
	return genevals.New(acceptance.New(env), genevals.Inputs{})
}
