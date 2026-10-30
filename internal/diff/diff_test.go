// Package diff tests structural evaluated Goa/Goa-AI design and go.mod
// directive comparison.
package diff

import (
	"context"
	"slices"
	"strings"
	"testing"
)

func TestCompareReportsEvaluatedDeclarationsHelperChangesAndGoModDirectives(t *testing.T) {
	ctx := context.Background()
	oldDesign := `package design

import . "goa.design/goa/v3/dsl"

const maximumItems = 10

func orderPayload() {
	Attribute("limit", Int, func() {
		Maximum(maximumItems)
	})
}

var _ = Service("orders", func() {
	Method("get_order", func() {
		Payload(orderPayload)
	})
})
`
	newDesign := `package design

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

const maximumItems = 20

func orderPayload() {
	Attribute("limit", Int, func() {
		Maximum(maximumItems)
	})
}

var RefundTools = Toolset("refund_tools", func() {
	Tool("issue_refund", "Issue a refund", func() {})
})

var _ = Service("orders", func() {
	Method("get_order", func() {
		Payload(orderPayload)
	})
	Method("approve_refund", func() {})
	Agent("refund_agent", "Handles order refunds", func() {
		Use(RefundTools)
		RunPolicy(func() {
			DefaultCaps(MaxToolCalls(6))
			TimeBudget("2m")
		})
	})
})
`
	oldMod := `module github.com/goadesign/scantle/examples/orders

go 1.26.2

require (
	goa.design/goa/v3 v3.26.1
	goa.design/goa-ai v0.5.3
)

exclude (
	example.com/legacy v1.0.0
	example.com/legacy v1.1.0
)
`
	newMod := `module github.com/goadesign/scantle/examples/orders

go 1.26.2

require (
	goa.design/goa/v3 v3.26.2
	goa.design/goa-ai v0.5.4
	goa.design/pulse v1.9.10
)

replace goa.design/goa-ai => ../goa-ai

exclude example.com/legacy v1.1.0
`

	rep, err := Compare(ctx, oldDesign, newDesign, oldMod, newMod)
	if err != nil {
		t.Fatalf("Compare returned unexpected error: %v", err)
	}
	if !rep.HasChanges {
		t.Fatal("expected HasChanges to be true")
	}
	for _, want := range []string{
		"Service:orders/Agent:refund_agent",
		"Service:orders/Agent:refund_agent/Toolset:refund_tools/Tool:issue_refund",
		"Service:orders/Method:approve_refund",
	} {
		if !slices.Contains(rep.AddedDeclarations, want) {
			t.Fatalf("expected AddedDeclarations to contain %q, got %v", want, rep.AddedDeclarations)
		}
	}
	if !slices.Contains(rep.ModifiedDeclarations, "Service:orders/Method:get_order") {
		t.Fatalf("expected ModifiedDeclarations to contain Service:orders/Method:get_order when const maximumItems changed from 10 to 20, got %v", rep.ModifiedDeclarations)
	}
	wantDeltas := []string{
		"exclude example.com/legacy@v1.0.0: removed excluded",
		"goa.design/goa-ai: v0.5.3 -> v0.5.4",
		"goa.design/goa/v3: v3.26.1 -> v3.26.2",
		"goa.design/pulse: added v1.9.10",
		"replace goa.design/goa-ai: added ../goa-ai",
	}
	if strings.Join(rep.VersionDeltas, ",") != strings.Join(wantDeltas, ",") {
		t.Fatalf("unexpected VersionDeltas: got %v, want %v", rep.VersionDeltas, wantDeltas)
	}
	if strings.Contains(strings.ToLower(rep.Summary), "compatible") {
		t.Fatalf("summary must not emit a blanket compatibility claim: %q", rep.Summary)
	}
}
