// Package diff tests structural Goa AST and go.mod version comparison.
package diff

import (
	"strings"
	"testing"
)

func TestCompareReportsDeclarationsAndVersionDeltasWithoutBlanketCompatibilityClaim(t *testing.T) {
	oldDesign := `package design
import . "goa.design/goa/v3/dsl"
var _ = Service("orders", func() {
	Method("get_order", func() {})
})
`
	newDesign := `package design
import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)
var _ = Service("orders", func() {
	Method("get_order", func() {})
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
`

	rep, err := Compare(oldDesign, newDesign, oldMod, newMod)
	if err != nil {
		t.Fatalf("Compare returned unexpected error: %v", err)
	}
	if !rep.HasChanges {
		t.Fatal("expected HasChanges to be true")
	}
	wantAdded := []string{"Agent:refund_agent", "Method:approve_refund", "Tool:issue_refund"}
	if strings.Join(rep.AddedDeclarations, ",") != strings.Join(wantAdded, ",") {
		t.Fatalf("unexpected AddedDeclarations: got %v, want %v", rep.AddedDeclarations, wantAdded)
	}
	if len(rep.RemovedDeclarations) != 0 {
		t.Fatalf("expected no RemovedDeclarations, got %v", rep.RemovedDeclarations)
	}
	wantDeltas := []string{
		"goa.design/goa-ai: v0.5.3 -> v0.5.4",
		"goa.design/goa/v3: v3.26.1 -> v3.26.2",
		"goa.design/pulse: added v1.9.10",
	}
	if strings.Join(rep.VersionDeltas, ",") != strings.Join(wantDeltas, ",") {
		t.Fatalf("unexpected VersionDeltas: got %v, want %v", rep.VersionDeltas, wantDeltas)
	}
	if strings.Contains(strings.ToLower(rep.Summary), "compatible") {
		t.Fatalf("summary must not emit a blanket compatibility claim: %q", rep.Summary)
	}
}
