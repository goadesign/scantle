// Package diff tests structural Goa AST and go.mod directive comparison.
package diff

import (
	"strings"
	"testing"
)

func TestCompareReportsDeclarationsAndVersionDeltasWithoutBlanketCompatibilityClaim(t *testing.T) {
	oldDesign := `package design
import . "goa.design/goa/v3/dsl"
var _ = Service("orders", func() {
	Method("get_order", func() {
		Payload(String)
	})
})
`
	newDesign := `package design
import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)
var _ = Service("orders", func() {
	Method("get_order", func() {
		Payload(Int)
	})
	Method("approve_refund", func() {})
	Agent("refund_agent", "Handles order refunds", func() {
		Use("refund_tools", func() {
			Tool("issue_refund", "Issue a refund", func() {})
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
`
	newMod := `module github.com/goadesign/scantle/examples/orders

go 1.26.2

require (
	goa.design/goa/v3 v3.26.2
	goa.design/goa-ai v0.5.4
	goa.design/pulse v1.9.10
)

replace goa.design/goa-ai => ../goa-ai
`

	rep, err := Compare(oldDesign, newDesign, oldMod, newMod)
	if err != nil {
		t.Fatalf("Compare returned unexpected error: %v", err)
	}
	if !rep.HasChanges {
		t.Fatal("expected HasChanges to be true")
	}
	wantAdded := []string{
		"Service:orders/Agent:refund_agent",
		"Service:orders/Agent:refund_agent/Use:refund_tools",
		"Service:orders/Agent:refund_agent/Use:refund_tools/Tool:issue_refund",
		"Service:orders/Method:approve_refund",
	}
	if strings.Join(rep.AddedDeclarations, ",") != strings.Join(wantAdded, ",") {
		t.Fatalf("unexpected AddedDeclarations: got %v, want %v", rep.AddedDeclarations, wantAdded)
	}
	wantModified := []string{"Service:orders/Method:get_order"}
	if strings.Join(rep.ModifiedDeclarations, ",") != strings.Join(wantModified, ",") {
		t.Fatalf("unexpected ModifiedDeclarations: got %v, want %v", rep.ModifiedDeclarations, wantModified)
	}
	if len(rep.RemovedDeclarations) != 0 {
		t.Fatalf("expected no RemovedDeclarations, got %v", rep.RemovedDeclarations)
	}
	wantDeltas := []string{
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

func TestCompareDetectsMethodMovedBetweenServices(t *testing.T) {
	oldDesign := `package design
import . "goa.design/goa/v3/dsl"
var _ = Service("orders", func() {
	Method("get_order", func() {})
})
var _ = Service("billing", func() {})
`
	newDesign := `package design
import . "goa.design/goa/v3/dsl"
var _ = Service("orders", func() {})
var _ = Service("billing", func() {
	Method("get_order", func() {})
})
`
	rep, err := Compare(oldDesign, newDesign, "", "")
	if err != nil {
		t.Fatalf("Compare failed: %v", err)
	}
	if !rep.HasChanges {
		t.Fatal("expected moving Method between services to report HasChanges")
	}
	if strings.Join(rep.AddedDeclarations, ",") != "Service:billing/Method:get_order" {
		t.Fatalf("unexpected AddedDeclarations: %v", rep.AddedDeclarations)
	}
	if strings.Join(rep.RemovedDeclarations, ",") != "Service:orders/Method:get_order" {
		t.Fatalf("unexpected RemovedDeclarations: %v", rep.RemovedDeclarations)
	}
}
