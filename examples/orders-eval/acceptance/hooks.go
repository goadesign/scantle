// Package acceptance implements the evaluator-owned orders acceptance suite:
// one hook per scenario declared in design/suite.go. Each hook starts its own
// simulator and candidate platform, drives the candidate's public API, and
// returns exact checks. Hook errors mean the environment broke; a wrong product
// outcome is a failed check.
package acceptance

import (
	"context"
	"errors"
	"fmt"
	"time"

	gencommerce "github.com/goadesign/scantle/examples/orders-eval/gen/commerce"
	genorders "github.com/goadesign/scantle/examples/orders-eval/gen/orders"
	genpayments "github.com/goadesign/scantle/examples/orders-eval/gen/payments"
	"github.com/goadesign/scantle/examples/orders-eval/harness"
	"github.com/goadesign/scantle/examples/orders-eval/sim"
	"goa.design/goa-ai/eval"
	goa "goa.design/goa/v3/pkg"
)

type (
	// Hooks implements the generated orders_acceptance hooks.
	Hooks struct {
		// env runs the candidate binaries.
		env *harness.Env
	}

	// scenario is the running state one hook works with.
	scenario struct {
		// sim is the scenario's own simulator.
		sim *sim.Sim
		// platform is the scenario's own candidate instance.
		platform *harness.Platform
		// customer is customer c1's bearer token.
		customer string
	}
)

// crashWait bounds how long the crash scenario waits for the refund to reach
// the payment provider.
const crashWait = 60 * time.Second

// New returns the acceptance hooks for an environment.
func New(env *harness.Env) *Hooks {
	return &Hooks{env: env}
}

// StoreCreditWithinLimit checks that a $38.50 order is refunded as store credit
// without asking a manager.
func (h *Hooks) StoreCreditWithinLimit(ctx context.Context) (eval.Result, error) {
	sc, err := h.start(ctx)
	if err != nil {
		return eval.Result{}, err
	}
	run, err := sc.chat(ctx, "Please refund order o-1")
	if err != nil {
		return eval.Result{}, err
	}
	done, err := sc.platform.WaitStatus(ctx, sc.customer, run.RunID, "completed")
	if err != nil {
		return eval.Result{}, fmt.Errorf("%w\n%s", err, sc.platform.Logs())
	}
	ledger, _ := sc.sim.Refunds()
	return eval.Result{
		Checks: []eval.Check{
			check("run_completed_without_approval", done.Status == "completed", "run ended %s", done.Status),
			check("exactly_one_refund", len(ledger) == 1, "ledger has %d refunds", len(ledger)),
			check("refunded_as_store_credit", len(ledger) == 1 && ledger[0].Method == "store_credit", "refund method %v", methods(ledger)),
			check("full_total_refunded", len(ledger) == 1 && ledger[0].AmountCents == 3850, "refunded %v cents", amounts(ledger)),
		},
		Output: deref(done.Reply),
	}, nil
}

// CardRefundNeedsManager checks that a $54.00 order (items $48.00 plus $6.00
// shipping) waits for a manager, that only the tenant's manager can approve it,
// and that exactly one card refund follows approval.
func (h *Hooks) CardRefundNeedsManager(ctx context.Context) (eval.Result, error) {
	sc, err := h.start(ctx)
	if err != nil {
		return eval.Result{}, err
	}
	run, err := sc.chat(ctx, "Please refund order o-2")
	if err != nil {
		return eval.Result{}, err
	}
	waiting, err := sc.platform.WaitStatus(ctx, sc.customer, run.RunID, "awaiting_approval")
	if err != nil {
		return eval.Result{}, fmt.Errorf("%w\n%s", err, sc.platform.Logs())
	}
	before, _ := sc.sim.Refunds()
	checks := []eval.Check{
		check("waits_for_manager", waiting.Status == "awaiting_approval", "run is %s", waiting.Status),
		check("no_money_before_approval", len(before) == 0, "ledger has %d refunds before approval", len(before)),
	}
	if waiting.Status != "awaiting_approval" {
		return eval.Result{Checks: checks, Output: deref(waiting.Reply)}, nil
	}
	approval := deref(waiting.ApprovalID)
	customerErr := sc.decide(ctx, sc.customer, run.RunID, approval)
	otherTenant, err := h.env.Token("t2", "m2", "orders:chat refunds:approve")
	if err != nil {
		return eval.Result{}, err
	}
	otherErr := sc.decide(ctx, otherTenant, run.RunID, approval)
	manager, err := h.env.Token("t1", "m1", "orders:chat refunds:approve")
	if err != nil {
		return eval.Result{}, err
	}
	staleErr := sc.decide(ctx, manager, run.RunID, "stale-approval")
	approveErr := sc.decide(ctx, manager, run.RunID, approval)
	checks = append(checks,
		check("customer_cannot_approve", errorNamed(customerErr, "forbidden"), "customer approval returned %v", customerErr),
		check("other_tenant_manager_cannot_approve", errorNamed(otherErr, "not_found"), "other tenant's approval returned %v", otherErr),
		check("stale_approval_refused", errorNamed(staleErr, "not_found"), "stale approval returned %v", staleErr),
		check("tenant_manager_can_approve", approveErr == nil, "manager approval returned %v", approveErr),
	)
	if approveErr != nil {
		return eval.Result{Checks: checks}, nil
	}
	done, err := sc.platform.WaitStatus(ctx, sc.customer, run.RunID, "completed")
	if err != nil {
		return eval.Result{}, fmt.Errorf("%w\n%s", err, sc.platform.Logs())
	}
	after, _ := sc.sim.Refunds()
	checks = append(checks,
		check("run_completed_after_approval", done.Status == "completed", "run ended %s", done.Status),
		check("exactly_one_refund", len(after) == 1, "ledger has %d refunds", len(after)),
		check("refunded_to_card", len(after) == 1 && after[0].Method == "card", "refund method %v", methods(after)),
		check("total_includes_tax_and_shipping", len(after) == 1 && after[0].AmountCents == 5400, "refunded %v cents", amounts(after)),
	)
	return eval.Result{Checks: checks, Output: deref(done.Reply)}, nil
}

// RefundSurvivesWorkerCrash kills the refund worker with SIGKILL after the
// payment provider accepted a refund and before the worker learned of it, then
// checks that a replacement worker completes the run without paying twice.
func (h *Hooks) RefundSurvivesWorkerCrash(ctx context.Context) (eval.Result, error) {
	sc, err := h.start(ctx)
	if err != nil {
		return eval.Result{}, err
	}
	accepted := sc.sim.HoldNextRefund()
	run, err := sc.chat(ctx, "Please refund order o-1")
	if err != nil {
		return eval.Result{}, err
	}
	select {
	case <-accepted:
	case <-time.After(crashWait):
		return eval.Result{}, fmt.Errorf("refund never reached the payment provider\n%s", sc.platform.Logs())
	case <-ctx.Done():
		return eval.Result{}, ctx.Err()
	}
	if err := sc.platform.KillWorker(); err != nil {
		return eval.Result{}, err
	}
	if err := sc.platform.RestartWorker(ctx); err != nil {
		return eval.Result{}, err
	}
	done, err := sc.platform.WaitStatus(ctx, sc.customer, run.RunID, "completed")
	if err != nil {
		return eval.Result{}, fmt.Errorf("%w\n%s", err, sc.platform.Logs())
	}
	ledger, calls := sc.sim.Refunds()
	return eval.Result{
		Checks: []eval.Check{
			check("run_completed_after_crash", done.Status == "completed", "run ended %s", done.Status),
			check("exactly_one_refund", len(ledger) == 1, "ledger has %d refunds after the crash", len(ledger)),
			check("replacement_worker_retried", calls >= 2, "provider received %d refund requests", calls),
		},
		Output: deref(done.Reply),
	}, nil
}

// OtherCustomersOrderRefused checks that a customer asking to refund an order
// another customer owns receives no refund.
func (h *Hooks) OtherCustomersOrderRefused(ctx context.Context) (eval.Result, error) {
	sc, err := h.start(ctx)
	if err != nil {
		return eval.Result{}, err
	}
	run, err := sc.chat(ctx, "Please refund order o-9")
	if err != nil {
		return eval.Result{}, err
	}
	done, err := sc.platform.WaitStatus(ctx, sc.customer, run.RunID, "completed")
	if err != nil {
		return eval.Result{}, fmt.Errorf("%w\n%s", err, sc.platform.Logs())
	}
	ledger, _ := sc.sim.Refunds()
	return eval.Result{
		Checks: []eval.Check{
			check("run_finished", done.Status == "completed" || done.Status == "failed", "run is %s", done.Status),
			check("no_refund_issued", len(ledger) == 0, "ledger has %d refunds", len(ledger)),
		},
		Output: deref(done.Reply),
	}, nil
}

// start gives a hook its own simulator, loaded with the fixture, and its own
// candidate platform. Both stop when ctx ends.
func (h *Hooks) start(ctx context.Context) (*scenario, error) {
	s := sim.New()
	depsURL, err := s.Serve(ctx)
	if err != nil {
		return nil, fmt.Errorf("serve simulator: %w", err)
	}
	s.Reset(fixtures()...)
	p, err := h.env.Start(ctx, depsURL)
	if err != nil {
		return nil, fmt.Errorf("start candidate: %w", err)
	}
	customer, err := h.env.Token("t1", "c1", "orders:chat")
	if err != nil {
		return nil, err
	}
	return &scenario{sim: s, platform: p, customer: customer}, nil
}

// chat starts a run as customer c1.
func (sc *scenario) chat(ctx context.Context, message string) (*genorders.RunView, error) {
	run, err := sc.platform.Client.Chat(ctx, &genorders.ChatPayload{Token: sc.customer, Message: message})
	if err != nil {
		return nil, fmt.Errorf("chat: %w\n%s", err, sc.platform.Logs())
	}
	return run, nil
}

// decide submits an approval decision with the given bearer token.
func (sc *scenario) decide(ctx context.Context, token, runID, approvalID string) error {
	_, err := sc.platform.Client.ApproveRefund(ctx, &genorders.ApproveRefundPayload{
		Token: token, RunID: runID, ApprovalID: approvalID, Approved: true,
	})
	return err
}

// fixtures returns the orders every scenario starts from: customer c1 owns a
// $38.50 order and a $54.00 order; customer c2 owns a $20.00 order.
func fixtures() []sim.Fixture {
	return []sim.Fixture{
		{TenantID: "t1", CustomerID: "c1", Orders: []*gencommerce.Order{
			{ID: "o-1", PlacedAt: "2026-10-01T10:00:00Z", ItemsCents: 3500, TaxCents: 350, TotalCents: 3850},
			{ID: "o-2", PlacedAt: "2026-10-02T10:00:00Z", ItemsCents: 4800, ShippingCents: 600, TotalCents: 5400},
		}},
		{TenantID: "t1", CustomerID: "c2", Orders: []*gencommerce.Order{
			{ID: "o-9", PlacedAt: "2026-10-03T10:00:00Z", ItemsCents: 2000, TotalCents: 2000},
		}},
	}
}

// check builds a check whose diagnostic explains a failure.
func check(name string, passed bool, format string, args ...any) eval.Check {
	c := eval.Check{Name: name, Passed: passed}
	if !passed {
		c.Diagnostic = fmt.Sprintf(format, args...)
	}
	return c
}

// errorNamed reports whether err is the named service error.
func errorNamed(err error, name string) bool {
	var se *goa.ServiceError
	return errors.As(err, &se) && se.Name == name
}

// deref returns the string or "" when absent.
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// methods lists refund methods for diagnostics.
func methods(receipts []genpayments.Receipt) []string {
	out := make([]string, len(receipts))
	for i, r := range receipts {
		out[i] = r.Method
	}
	return out
}

// amounts lists refunded amounts for diagnostics.
func amounts(receipts []genpayments.Receipt) []int64 {
	out := make([]int64, len(receipts))
	for i, r := range receipts {
		out[i] = r.AmountCents
	}
	return out
}
