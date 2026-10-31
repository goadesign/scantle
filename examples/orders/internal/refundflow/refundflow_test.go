package refundflow

import (
	"context"
	"errors"
	"testing"

	genpayments "github.com/goadesign/surla/examples/orders/gen/payments"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
)

// flakyPayments fails its first call after recording it, the way a worker
// that crashes after the provider accepted the refund looks to the provider.
type flakyPayments struct {
	// keys records the idempotency key of every call.
	keys []string
}

func TestWorkflowRetriesWithTheSameIdempotencyKey(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	payments := &flakyPayments{}
	acts := NewActivities(payments)
	env.RegisterActivityWithOptions(acts.Charge, activity.RegisterOptions{Name: chargeActivityName})

	in := Input{TenantID: "t1", OrderID: "o-5400", AmountCents: 5400, Method: "card", IdempotencyKey: refundKey("t1", "o-5400")}
	env.ExecuteWorkflow(Workflow, in)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	var receipt genpayments.Receipt
	require.NoError(t, env.GetWorkflowResult(&receipt))
	assert.Equal(t, int64(5400), receipt.AmountCents)
	assert.Equal(t, []string{"refund/t1/o-5400", "refund/t1/o-5400"}, payments.keys,
		"the retry must reuse the key so the provider can return the original receipt")
}

// Refund fails the first call and succeeds afterwards.
func (f *flakyPayments) Refund(_ context.Context, p *genpayments.RefundPayload) (*genpayments.Receipt, error) {
	f.keys = append(f.keys, p.IdempotencyKey)
	if len(f.keys) == 1 {
		return nil, errors.New("connection reset after the provider accepted the refund")
	}
	return &genpayments.Receipt{ReceiptID: "r-1", OrderID: p.OrderID, AmountCents: p.AmountCents, Method: p.Method}, nil
}
