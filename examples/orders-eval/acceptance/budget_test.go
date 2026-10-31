package acceptance

import (
	"errors"
	"testing"

	"github.com/goadesign/surla/examples/orders-eval/harness"
	"github.com/goadesign/surla/examples/orders-eval/scriptedmodel"
	"github.com/stretchr/testify/assert"
)

func TestJudgeBudget(t *testing.T) {
	b := budget{inputTokens: 100, outputTokens: 10, costNanoUSD: 1_000_000}
	usage := func(in, out int) harness.Usage {
		return harness.Usage{Calls: 2, ByModel: map[string]harness.Tokens{scriptedmodel.ModelID: {Input: in, Output: out}}}
	}
	cases := []struct {
		name   string
		usage  harness.Usage
		err    error
		passed bool
	}{
		{"within every bound", usage(100, 10), nil, true},
		{"input over budget", usage(101, 10), nil, false},
		{"output over budget", usage(100, 11), nil, false},
		{"cache writes count as input", harness.Usage{Calls: 1, ByModel: map[string]harness.Tokens{
			scriptedmodel.ModelID: {CacheWrite: 100},
		}}, nil, true},
		{"cache reads count as input", harness.Usage{Calls: 1, ByModel: map[string]harness.Tokens{
			scriptedmodel.ModelID: {Input: 60, CacheRead: 41},
		}}, nil, false},
		{"no call reached the gateway", harness.Usage{}, nil, false},
		{"a call did not report complete usage", harness.Usage{Calls: 2, Unreported: 1, ByModel: usage(1, 1).ByModel}, nil, false},
		{"model without a price", harness.Usage{Calls: 1, ByModel: map[string]harness.Tokens{"unknown": {Input: 1}}}, nil, false},
		{"usage not final", harness.Usage{}, errors.New("not final"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := judgeBudget(c.usage, c.err, b)
			assert.Equal(t, "within_token_budget", got.Name)
			assert.Equal(t, c.passed, got.Passed, got.Diagnostic)
		})
	}
	t.Run("cost alone can fail", func(t *testing.T) {
		// 100 cache-write tokens cost 375,000 nano-dollars; a 300,000 cap fails.
		got := judgeBudget(harness.Usage{Calls: 1, ByModel: map[string]harness.Tokens{
			scriptedmodel.ModelID: {CacheWrite: 100},
		}}, nil, budget{inputTokens: 100, outputTokens: 10, costNanoUSD: 300_000})
		assert.False(t, got.Passed, got.Diagnostic)
	})
}

func TestJudgeBudgetReportsObservedTotalsOfIncompleteUsage(t *testing.T) {
	got := judgeBudget(harness.Usage{Calls: 3, Unreported: 1, ByModel: map[string]harness.Tokens{
		scriptedmodel.ModelID: {Input: 40, Output: 4},
	}}, nil, budget{inputTokens: 100, outputTokens: 10, costNanoUSD: 1_000_000})
	assert.False(t, got.Passed, "totals within budget still fail when a call's usage is incomplete")
	assert.Contains(t, got.Diagnostic, "observed at least 40 input tokens")
}
