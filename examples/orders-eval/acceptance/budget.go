package acceptance

import (
	"maps"
	"slices"

	"github.com/goadesign/surla/examples/orders-eval/harness"
	"github.com/goadesign/surla/examples/orders-eval/scriptedmodel"
	"goa.design/goa-ai/eval"
)

type (
	// budget is the most model usage one scenario may spend across every
	// model call its candidate makes (parent, child and continuation runs).
	budget struct {
		// inputTokens bounds input tokens, cache reads and writes included.
		inputTokens int
		// outputTokens bounds output tokens.
		outputTokens int
		// costNanoUSD bounds the estimated cost in billionths of a dollar.
		costNanoUSD int64
	}

	// price is what one model charges per token, in billionths of a dollar.
	price struct {
		// input is the price of an uncached input token.
		input int64
		// output is the price of an output token.
		output int64
		// cacheRead is the price of an input token read from the cache.
		cacheRead int64
		// cacheWrite is the price of an input token written to the cache
		// with the provider's default five-minute lifetime.
		cacheWrite int64
	}
)

// prices lists the models the evaluator can price. It is a fixture price
// profile for the simulated environment: the scripted model is priced like a
// mid-size hosted model ($3 in, $15 out per million tokens) so budgets read in
// realistic dollars, but neither its token counts nor these prices are a real
// provider's tokens or bill. A model missing here cannot be priced, which
// fails the budget check.
var prices = map[string]price{
	scriptedmodel.ModelID: {input: 3_000, output: 15_000, cacheRead: 300, cacheWrite: 3_750},
}

// Scenario budgets: the reference candidate's measured usage plus about 20%
// for legitimate wording changes. A refund conversation (list the orders, call
// one refund tool, reply) measured 2,563 input and 105 output tokens
// ($0.009264); a refused one (list the orders, reply) measured 1,457 input and
// 67 output tokens ($0.005376). Sending the model 40 unneeded orders costs a
// refund conversation 4,193 input tokens and fails its budget.
var (
	// refundBudget bounds the scenarios that issue or wait for a refund.
	refundBudget = budget{inputTokens: 3_100, outputTokens: 130, costNanoUSD: 11_200_000}
	// refusalBudget bounds the scenario that refuses another customer's order.
	refusalBudget = budget{inputTokens: 1_750, outputTokens: 85, costNanoUSD: 6_500_000}
)

// withinBudget checks the platform's measured model usage against b. It must
// run after the platform closed, when the usage is final. Usage that is not
// final, any call whose usage was not reported, and any model without a price
// fail the check, because the spend cannot then be proven within budget.
func withinBudget(p *harness.Platform, b budget) eval.Check {
	usage, err := p.Usage()
	return judgeBudget(usage, err, b)
}

// judgeBudget is withinBudget over an already read measurement.
func judgeBudget(usage harness.Usage, err error, b budget) eval.Check {
	const name = "within_token_budget"
	if err != nil {
		return check(name, false, "%v", err)
	}
	if usage.Calls == 0 {
		return check(name, false, "the candidate made no model call through the evaluator's gateway")
	}
	var in, out int
	var cost int64
	for _, model := range slices.Sorted(maps.Keys(usage.ByModel)) {
		t := usage.ByModel[model]
		pr, ok := prices[model]
		if !ok {
			return check(name, false, "model %q has no price", model)
		}
		in += t.Input + t.CacheRead + t.CacheWrite
		out += t.Output
		cost += int64(t.Input)*pr.input + int64(t.Output)*pr.output +
			int64(t.CacheRead)*pr.cacheRead + int64(t.CacheWrite)*pr.cacheWrite
	}
	if usage.Unreported > 0 {
		return check(name, false, "%d of %d model calls did not report complete usage; observed at least "+
			"%d input tokens (budget %d), %d output tokens (budget %d), $%.6f (budget $%.6f)",
			usage.Unreported, usage.Calls, in, b.inputTokens, out, b.outputTokens, dollars(cost), dollars(b.costNanoUSD))
	}
	return check(name, in <= b.inputTokens && out <= b.outputTokens && cost <= b.costNanoUSD,
		"%d calls used %d input tokens (budget %d), %d output tokens (budget %d), $%.6f (budget $%.6f)",
		usage.Calls, in, b.inputTokens, out, b.outputTokens, dollars(cost), dollars(b.costNanoUSD))
}

// dollars converts billionths of a dollar to dollars for display.
func dollars(nano int64) float64 {
	return float64(nano) / 1e9
}
