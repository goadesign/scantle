// Package assistant runs the refund agent: its planner, its system prompt, and
// the conversations the orders API starts, inspects, and continues.
package assistant

import (
	"context"
	"errors"
	"fmt"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/prompt"
)

// Planner asks the configured model for the refund agent's next step. It owns
// no side effects: tools run through the runtime, so retrying a planner
// activity is safe.
type Planner struct {
	// modelID selects the model client registered with the runtime.
	modelID string
}

const (
	// SystemPromptID identifies the refund agent's system prompt in the
	// runtime prompt registry. Prompt store overrides of this ID change the
	// agent's wording without rebuilding the service.
	SystemPromptID prompt.Ident = "orders.refund.system"

	// systemPromptVersion is the version of the baseline prompt below.
	systemPromptVersion = "v1"

	// systemPrompt is the baseline instruction for the refund agent. Business
	// rules such as the $50.00 limit are enforced by the tools; the prompt
	// only helps the model pick the right tool the first time.
	systemPrompt = `You help a customer get refunds for their own orders.
Use list_orders to find the order the customer means. Refund each order at most once.
An order total includes tax and shipping. If that total is $50.00 or less, use issue_store_credit.
If it is over $50.00, use request_refund; a manager must approve it before the money moves, so tell the customer the refund is waiting for approval.
Never promise a refund before a tool result confirms it. If a tool refuses, explain the reason to the customer in plain words.`
)

// SystemPrompt returns the baseline prompt spec to register with the runtime
// prompt registry.
func SystemPrompt() prompt.PromptSpec {
	return prompt.PromptSpec{
		ID:          SystemPromptID,
		AgentID:     "orders.refund",
		Role:        prompt.PromptRoleSystem,
		Description: "Instructions for the refund assistant.",
		Template:    systemPrompt,
		Version:     systemPromptVersion,
	}
}

// NewPlanner returns a planner that uses the runtime model client registered
// under modelID.
func NewPlanner(modelID string) *Planner {
	return &Planner{modelID: modelID}
}

// PlanStart decides the first step of a run.
func (p *Planner) PlanStart(ctx context.Context, in *planner.PlanInput) (*planner.PlanResult, error) {
	return p.plan(ctx, in.Agent, in.Messages, false)
}

// PlanResume decides the next step after tool results. When the runtime is
// finalizing the run, it asks for a final answer without tools.
func (p *Planner) PlanResume(ctx context.Context, in *planner.PlanResumeInput) (*planner.PlanResult, error) {
	return p.plan(ctx, in.Agent, in.Messages, in.Finalize != nil)
}

// plan renders the system prompt, sends the conversation and the advertised
// tools to the model, and returns either the requested tool calls or the
// final answer.
func (p *Planner) plan(ctx context.Context, agent planner.PlannerContext, messages []*model.Message, finalize bool) (*planner.PlanResult, error) {
	system, err := agent.RenderPrompt(ctx, SystemPromptID, nil)
	if err != nil {
		return nil, fmt.Errorf("render system prompt: %w", err)
	}
	client, ok := agent.PlannerModelClient(p.modelID)
	if !ok {
		return nil, fmt.Errorf("model %q is not configured", p.modelID)
	}
	req := &model.Request{
		ModelClass: model.ModelClassHighReasoning,
		PromptRefs: []prompt.PromptRef{system.Ref},
		Messages: append([]*model.Message{{
			Role:  model.ConversationRoleSystem,
			Parts: []model.Part{model.TextPart{Text: system.Text}},
		}}, messages...),
		Stream: true,
	}
	if !finalize {
		req.Tools = agent.AdvertisedToolDefinitions()
	}
	sum, err := client.Stream(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("model step: %w", err)
	}
	if len(sum.ToolCalls) > 0 {
		if finalize {
			return nil, errors.New("model requested tools while the run is finalizing")
		}
		return &planner.PlanResult{ToolCalls: sum.ToolCalls}, nil
	}
	return &planner.PlanResult{FinalResponse: sum.FinalResponse()}, nil
}
