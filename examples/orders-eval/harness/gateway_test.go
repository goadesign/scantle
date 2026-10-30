package harness

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"goa.design/goa-ai/runtime/agent/model"
)

func TestMeterRecordsEveryCallWithoutDoubleCounting(t *testing.T) {
	reported := func(in, out int) *model.Response {
		return &model.Response{Usage: model.TokenUsage{Model: "m", InputTokens: in, OutputTokens: out}, UsageReported: true}
	}
	m := &meter{}
	unary := m.unary(func(context.Context, *model.Request) (*model.Response, error) { return reported(10, 2), nil })
	_, _ = unary(t.Context(), &model.Request{})

	// The stream sends a usage chunk and returns the final response with the
	// same totals; only the final response counts.
	stream := m.stream(func(_ context.Context, _ *model.Request, send func(model.Chunk) error) (*model.Response, error) {
		_ = send(model.UsageChunk{Usage: model.TokenUsage{Model: "m", InputTokens: 7, OutputTokens: 1}})
		return reported(7, 1), nil
	})
	_, _ = stream(t.Context(), &model.Request{}, func(model.Chunk) error { return nil })

	unreported := m.unary(func(context.Context, *model.Request) (*model.Response, error) {
		return &model.Response{Usage: model.TokenUsage{Model: "m", InputTokens: 99}}, nil
	})
	_, _ = unreported(t.Context(), &model.Request{})

	failedUnknown := m.unary(func(context.Context, *model.Request) (*model.Response, error) {
		return nil, errors.New("provider unavailable")
	})
	_, _ = failedUnknown(t.Context(), &model.Request{})

	got := m.total()
	assert.Equal(t, 4, got.Calls)
	assert.Equal(t, 2, got.Unreported, "a response without reported usage and an error without usage are unreported, never zero")
	assert.Equal(t, Tokens{Input: 17, Output: 3}, got.ByModel["m"], "stream chunk usage is not added to the final total")
}

func TestPlatformUsageIsAnErrorUntilClosed(t *testing.T) {
	_, err := (&Platform{}).Usage()
	assert.Error(t, err)
}
