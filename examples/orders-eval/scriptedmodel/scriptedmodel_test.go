package scriptedmodel

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/model"
)

func TestStreamReportsUsageThatGrowsWithTheRequest(t *testing.T) {
	usage := func(text string) model.TokenUsage {
		t.Helper()
		s, err := New().Stream(t.Context(), &model.Request{Messages: []*model.Message{{
			Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: text}},
		}}})
		require.NoError(t, err)
		for {
			if _, err := s.Recv(); err != nil {
				break
			}
		}
		resp := s.Response()
		require.True(t, resp.HasUsage(), "usage is always explicitly reported")
		assert.Equal(t, ModelID, resp.Usage.Model)
		assert.Equal(t, resp.Usage.InputTokens+resp.Usage.OutputTokens, resp.Usage.TotalTokens)
		return resp.Usage
	}
	short, long := usage("refund"), usage("refund order please, I have been waiting a long time for this")
	assert.Greater(t, long.InputTokens, short.InputTokens)
	assert.Equal(t, usage("refund"), short, "the count is deterministic")
}

func TestStreamRejectsAnOrderRequestWithoutTools(t *testing.T) {
	_, err := New().Stream(t.Context(), &model.Request{Messages: []*model.Message{{
		Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "Please refund order o-1"}},
	}}})
	assert.ErrorContains(t, err, "advertises no tools")
}
