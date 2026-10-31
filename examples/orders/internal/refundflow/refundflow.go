// Package refundflow runs each order refund as a durable Temporal workflow so a
// refund completes exactly once even when the worker executing it crashes.
//
// The workflow ID and the payment provider's idempotency key are both derived
// from the tenant and order. Temporal guarantees at most one workflow per ID,
// and the provider returns the original receipt for a repeated key, so neither
// a retried activity nor a repeated request can refund an order twice.
package refundflow

import (
	"context"
	"errors"
	"fmt"
	"time"

	genpayments "github.com/goadesign/scantle/examples/orders/gen/payments"
	"github.com/goadesign/scantle/examples/orders/internal/orders"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

type (
	// Runner starts refund workflows and waits for their receipts. It
	// implements orders.RefundRunner.
	Runner struct {
		// temporal starts and reads refund workflows.
		temporal client.Client
		// taskQueue is the queue the refund worker polls.
		taskQueue string
	}

	// Activities holds the refund workflow's activities.
	Activities struct {
		// payments calls the payment provider.
		payments Refunder
	}

	// Refunder issues refunds at the payment provider. The generated payments
	// client satisfies it.
	Refunder interface {
		// Refund refunds an order once per idempotency key.
		Refund(ctx context.Context, p *genpayments.RefundPayload) (*genpayments.Receipt, error)
	}

	// Input is the refund workflow's argument.
	Input struct {
		// TenantID is the merchant tenant that owns the order.
		TenantID string
		// OrderID is the refunded order.
		OrderID string
		// AmountCents is the refunded amount in cents.
		AmountCents int64
		// Method is how the money is returned: "store_credit" or "card".
		Method string
		// IdempotencyKey identifies this refund at the payment provider.
		IdempotencyKey string
	}
)

const (
	// WorkflowName is the registered name of the refund workflow.
	WorkflowName = "orders.refund"
	// chargeActivityName is the registered name of the payment activity.
	chargeActivityName = "orders.refund.charge"
)

// NewRunner returns a Runner that starts refund workflows on taskQueue.
func NewRunner(c client.Client, taskQueue string) *Runner {
	return &Runner{temporal: c, taskQueue: taskQueue}
}

// NewActivities returns the refund activities backed by the payment provider.
func NewActivities(payments Refunder) *Activities {
	return &Activities{payments: payments}
}

// Register registers the refund workflow and its activity on a worker.
func Register(w worker.Worker, a *Activities) {
	w.RegisterWorkflowWithOptions(Workflow, workflow.RegisterOptions{Name: WorkflowName})
	w.RegisterActivityWithOptions(a.Charge, activity.RegisterOptions{Name: chargeActivityName})
}

// Refund runs the refund workflow for the order and returns its receipt. A
// refund already running for the order is joined; a refund that already
// completed returns its original receipt; a refund that failed may be retried.
func (r *Runner) Refund(ctx context.Context, refund orders.Refund) (*genpayments.Receipt, error) {
	id := refundKey(refund.TenantID, refund.OrderID)
	in := Input{
		TenantID:       refund.TenantID,
		OrderID:        refund.OrderID,
		AmountCents:    refund.AmountCents,
		Method:         refund.Method,
		IdempotencyKey: id,
	}
	run, err := r.temporal.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:                       id,
		TaskQueue:                r.taskQueue,
		WorkflowIDReusePolicy:    enumspb.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE_FAILED_ONLY,
		WorkflowIDConflictPolicy: enumspb.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING,
	}, WorkflowName, in)
	if err != nil {
		var started *serviceerror.WorkflowExecutionAlreadyStarted
		if !errors.As(err, &started) {
			return nil, fmt.Errorf("start refund workflow %s: %w", id, err)
		}
		run = r.temporal.GetWorkflow(ctx, id, "")
	}
	var receipt genpayments.Receipt
	if err := run.Get(ctx, &receipt); err != nil {
		return nil, fmt.Errorf("refund workflow %s: %w", id, err)
	}
	return &receipt, nil
}

// Workflow refunds one order by calling the payment provider through an
// activity that Temporal retries until it succeeds.
func Workflow(ctx workflow.Context, in Input) (*genpayments.Receipt, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2,
			MaximumInterval:    30 * time.Second,
		},
	})
	var receipt genpayments.Receipt
	if err := workflow.ExecuteActivity(ctx, chargeActivityName, in).Get(ctx, &receipt); err != nil {
		return nil, err
	}
	return &receipt, nil
}

// Charge asks the payment provider for the refund. Retrying it is safe: the
// provider returns the original receipt for a repeated idempotency key.
func (a *Activities) Charge(ctx context.Context, in Input) (*genpayments.Receipt, error) {
	receipt, err := a.payments.Refund(ctx, &genpayments.RefundPayload{
		TenantID:       in.TenantID,
		OrderID:        in.OrderID,
		AmountCents:    in.AmountCents,
		Method:         in.Method,
		IdempotencyKey: in.IdempotencyKey,
	})
	if err != nil {
		return nil, fmt.Errorf("payment provider refund for order %s: %w", in.OrderID, err)
	}
	return receipt, nil
}

// refundKey identifies the single refund an order may receive.
func refundKey(tenantID, orderID string) string {
	return "refund/" + tenantID + "/" + orderID
}
