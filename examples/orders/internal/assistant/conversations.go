package assistant

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/goadesign/scantle/examples/orders/internal/orders"
	"github.com/google/uuid"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/runtime"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
)

type (
	// Conversations runs refund agent conversations on the Goa-AI runtime. It
	// implements orders.Conversations.
	Conversations struct {
		// rt reads run snapshots and saved suspensions.
		rt *runtime.Runtime
		// agent starts and continues refund agent runs.
		agent runtime.AgentClient
		// store is the host-owned durable runtime store.
		store Store
	}

	// Store is the host-owned runtime store: the worker-facing storage.Store
	// plus session creation, which the runtime leaves to the host.
	Store interface {
		storage.Store
		// CreateSession creates the session or returns the existing one.
		CreateSession(ctx context.Context, sessionID string, createdAt time.Time) (session.Session, error)
	}
)

const (
	// labelTenant is the run label holding the customer's tenant. The
	// generated tool code injects it into tool payloads as tenant_id.
	labelTenant = "tenant_id"
	// labelCustomer is the run label holding the customer ID. The generated
	// tool code injects it into tool payloads as customer_id.
	labelCustomer = "customer_id"

	// recordTimeout bounds how long Start waits for the runtime to record an
	// accepted run.
	recordTimeout = 15 * time.Second
	// recordPollInterval is how often Start checks for the recorded run.
	recordPollInterval = 25 * time.Millisecond
)

// NewConversations returns conversations backed by the runtime, the refund
// agent client, and the host-owned store.
func NewConversations(rt *runtime.Runtime, agent runtime.AgentClient, store Store) *Conversations {
	return &Conversations{rt: rt, agent: agent, store: store}
}

// Start starts a refund agent run in the customer's session. Tenant and
// customer become run labels, which the generated tools inject into every tool
// payload, so the model never supplies them.
func (c *Conversations) Start(ctx context.Context, tenantID, customerID, message string) (orders.RunState, error) {
	sessionID := sessionKey(tenantID, customerID)
	if _, err := c.store.CreateSession(ctx, sessionID, time.Now().UTC()); err != nil {
		return orders.RunState{}, fmt.Errorf("open session %s: %w", sessionID, err)
	}
	runID := newRunID()
	_, err := c.agent.Start(ctx, sessionID,
		[]*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: message}}}},
		runtime.WithRunID(runID),
		runtime.WithLabels(map[string]string{labelTenant: tenantID, labelCustomer: customerID}),
	)
	if err != nil {
		return orders.RunState{}, fmt.Errorf("start run: %w", err)
	}
	if err := c.awaitRecorded(ctx, runID); err != nil {
		return orders.RunState{}, err
	}
	return orders.RunState{RunID: runID, TenantID: tenantID, CustomerID: customerID, Status: "running"}, nil
}

// awaitRecorded waits until the runtime has recorded the run it accepted.
//
// PROVISIONAL: this bounded wait covers the gap between engine acceptance and
// the workflow's first activity recording the run. It is not the intended
// contract (a timeout can leave an accepted workflow behind a failed HTTP
// call) and will be replaced by an owner-side signal once one is identified.
// The
// run's first workflow activity records it together with its labels; until
// then the API could neither show the run nor prove who owns it, so Start only
// returns a run ID that run_status can already authorize and report.
func (c *Conversations) awaitRecorded(ctx context.Context, runID string) error {
	ctx, cancel := context.WithTimeout(ctx, recordTimeout)
	defer cancel()
	tick := time.NewTicker(recordPollInterval)
	defer tick.Stop()
	for {
		_, err := c.store.LoadRun(ctx, runID)
		if err == nil {
			return nil
		}
		if !errors.Is(err, session.ErrRunNotFound) {
			return fmt.Errorf("load run %s: %w", runID, err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("run %s was not recorded within %s: %w", runID, recordTimeout, ctx.Err())
		case <-tick.C:
		}
	}
}

// Status returns the state of the conversation that runID belongs to. When the
// run was suspended and later continued, it follows the continuation chain and
// reports the newest run, so a caller polling the original run ID sees the
// outcome of the approved refund.
func (c *Conversations) Status(ctx context.Context, runID string) (orders.RunState, error) {
	for {
		meta, err := c.store.LoadRun(ctx, runID)
		if errors.Is(err, session.ErrRunNotFound) {
			return orders.RunState{}, orders.ErrRunNotFound
		}
		if err != nil {
			return orders.RunState{}, fmt.Errorf("load run %s: %w", runID, err)
		}
		next, ok := meta.SuspensionResolution.(session.ContinuedSuspension)
		if !ok {
			break
		}
		runID = next.RunID
	}
	snap, err := c.rt.GetRunSnapshot(ctx, runID)
	if err != nil {
		return orders.RunState{}, fmt.Errorf("snapshot run %s: %w", runID, err)
	}
	state := orders.RunState{
		RunID:      snap.RunID,
		TenantID:   snap.Labels[labelTenant],
		CustomerID: snap.Labels[labelCustomer],
	}
	switch snap.Status {
	case run.StatusCompleted:
		state.Status = "completed"
		state.Reply = snap.LastAssistantMessage
	case run.StatusFailed, run.StatusCanceled:
		state.Status = "failed"
	case run.StatusSuspended:
		return c.withPendingApproval(ctx, state)
	default:
		state.Status = "running"
	}
	return state, nil
}

// Decide continues a run waiting for a manager with the manager's decision.
// The continuation is a new run, in a new conversation turn of the same
// session, that inherits the saved work; the runtime executes the refund tool
// only when approved.
func (c *Conversations) Decide(ctx context.Context, state orders.RunState, approved bool, decidedBy string) (orders.RunState, error) {
	nextID := newRunID()
	prepared, err := c.agent.PrepareContinuation(ctx,
		sessionKey(state.TenantID, state.CustomerID), state.RunID, nextID, "turn-"+uuid.NewString(),
		&api.PendingInputResponse{Confirmation: &api.ConfirmationDecision{
			ID:          state.ApprovalID,
			Approved:    approved,
			RequestedBy: "manager:" + decidedBy,
		}},
		runtime.WorkflowOptions{},
	)
	if err != nil {
		return orders.RunState{}, fmt.Errorf("prepare continuation of %s: %w", state.RunID, err)
	}
	if _, err := c.agent.StartPrepared(ctx, prepared); err != nil {
		return orders.RunState{}, fmt.Errorf("start continuation of %s: %w", state.RunID, err)
	}
	return orders.RunState{RunID: nextID, TenantID: state.TenantID, CustomerID: state.CustomerID, Status: "running"}, nil
}

// withPendingApproval reports a suspended run waiting for a refund approval.
// The refund agent only suspends for request_refund confirmations; any other
// pending input breaks that contract.
func (c *Conversations) withPendingApproval(ctx context.Context, state orders.RunState) (orders.RunState, error) {
	suspension, err := c.rt.LoadRunSuspension(ctx, state.RunID)
	if err != nil {
		return orders.RunState{}, fmt.Errorf("load suspension of %s: %w", state.RunID, err)
	}
	if len(suspension.Pending) != 1 || suspension.Pending[0].Kind != api.PendingInputKindConfirmation {
		return orders.RunState{}, fmt.Errorf("run %s waits for unexpected input", state.RunID)
	}
	pending := suspension.Pending[0].Confirmation
	state.Status = "awaiting_approval"
	state.ApprovalID = pending.ID
	state.ApprovalPrompt = pending.Prompt
	return state, nil
}

// sessionKey identifies the conversation session of one customer.
func sessionKey(tenantID, customerID string) string {
	return tenantID + "/" + customerID
}

// newRunID returns a fresh run identifier.
func newRunID() string {
	return "run-" + uuid.NewString()
}
