// Command orders-worker executes refund workflows. It is a separate process so
// that crash tests can kill the process that calls the payment provider without
// stopping the orders API or the refund agent.
package main

import (
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"

	genpaymentsclient "github.com/goadesign/surla/examples/orders/gen/http/payments/client"
	genpayments "github.com/goadesign/surla/examples/orders/gen/payments"
	"github.com/goadesign/surla/examples/orders/internal/refundflow"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	goahttp "goa.design/goa/v3/http"
)

func main() {
	env := flag.String("env", "", `dependency environment: "sim" or "staging"`)
	paymentsURL := flag.String("payments-url", "", "base URL of the payment provider")
	temporalAddr := flag.String("temporal", "localhost:7233", "Temporal frontend address")
	queue := flag.String("refund-queue", "orders.refunds", "task queue of refund workflows")
	flag.Parse()
	if err := run(*env, *paymentsURL, *temporalAddr, *queue); err != nil {
		slog.Error("orders-worker stopped", "error", err)
		os.Exit(1)
	}
}

// run polls the refund task queue until interrupted.
func run(env, paymentsURL, temporalAddr, queue string) error {
	if env != "sim" && env != "staging" {
		return errors.New(`--env must be "sim" or "staging"`)
	}
	u, err := url.Parse(paymentsURL)
	if err != nil || u.Host == "" {
		return fmt.Errorf("--payments-url must be an absolute URL: %q", paymentsURL)
	}
	c := genpaymentsclient.NewClient(u.Scheme, u.Host, http.DefaultClient, goahttp.RequestEncoder, goahttp.ResponseDecoder, false)
	payments := genpayments.NewClient(c.Refund())

	temporal, err := client.Dial(client.Options{HostPort: temporalAddr})
	if err != nil {
		return fmt.Errorf("connect to temporal: %w", err)
	}
	defer temporal.Close()
	w := worker.New(temporal, queue, worker.Options{})
	refundflow.Register(w, refundflow.NewActivities(payments))
	slog.Info("orders-worker polling", "queue", queue, "env", env)
	return w.Run(worker.InterruptCh())
}
