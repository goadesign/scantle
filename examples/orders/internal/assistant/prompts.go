package assistant

import (
	"context"
	"fmt"

	"github.com/goadesign/surla/examples/orders/internal/orders"
	"goa.design/goa-ai/runtime/agent/prompt"
)

// Prompts changes the refund agent's prompts while the service runs. It
// implements orders.Prompts. The overrides live in the service's own prompt
// store, the one the runtime reads when the planner renders a prompt, so the
// next model request uses the new text.
type Prompts struct {
	// registry holds the built-in prompt specs the planner renders.
	registry *prompt.Registry
	// store is the runtime's prompt store; Override writes to it.
	store prompt.Store
}

// NewPrompts returns prompts that edit the specs registered in registry by
// writing overrides to store. store must be the prompt store the runtime that
// owns registry was built with.
func NewPrompts(registry *prompt.Registry, store prompt.Store) *Prompts {
	return &Prompts{registry: registry, store: store}
}

// Override makes template the text of the registered prompt promptID for every
// tenant and returns "sha256:" followed by the template's hex SHA-256, the
// version model requests then carry. It renders template exactly as the
// planner will, with the registered spec's functions and no template data,
// before saving it, so a template that does not parse or execute is rejected
// with orders.ErrInvalidPrompt and the text in use stays the same. An
// unregistered promptID fails with orders.ErrPromptNotFound.
func (p *Prompts) Override(ctx context.Context, promptID, template string) (string, error) {
	spec, ok := p.spec(prompt.Ident(promptID))
	if !ok {
		return "", fmt.Errorf("%w: %s", orders.ErrPromptNotFound, promptID)
	}
	spec.Template = template
	check := prompt.NewRegistry(nil)
	if err := check.Register(spec); err != nil {
		return "", fmt.Errorf("%w: %w", orders.ErrInvalidPrompt, err)
	}
	if _, err := check.Render(ctx, spec.ID, prompt.Scope{}, nil); err != nil {
		return "", fmt.Errorf("%w: %w", orders.ErrInvalidPrompt, err)
	}
	if err := p.store.Set(ctx, spec.ID, prompt.Scope{}, template, nil); err != nil {
		return "", fmt.Errorf("save prompt override: %w", err)
	}
	return prompt.VersionFromTemplate(template), nil
}

// spec returns the registered spec with the given ID.
func (p *Prompts) spec(id prompt.Ident) (prompt.PromptSpec, bool) {
	for _, spec := range p.registry.List() {
		if spec.ID == id {
			return spec, true
		}
	}
	return prompt.PromptSpec{}, false
}
