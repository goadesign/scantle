// Package diff tests structural evaluated Goa/Goa-AI design and go.mod
// directive comparison.
package diff

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/goadesign/scantle/internal/designeval"
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

func TestCompareDetectsTransportMappingsProtobufTagsMCPAnnotationsBoundedResultsAndPerConsumerCards(t *testing.T) {
	ctx := context.Background()
	oldDesign := `package design

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

var LookupArgs = Type("LookupArgs", func() {
	Field(1, "tenant_id", String)
	Field(2, "order_id", String)
	Field(3, "cursor", String)
	Required("tenant_id", "order_id")
})

var LookupResult = Type("LookupResult", func() {
	Field(1, "items", ArrayOf(String))
	Required("items")
})

var OrderTools = Toolset("order_tools", func() {
	Tool("list_orders", "List orders", func() {
		Args(LookupArgs)
		Return(LookupResult)
		BoundedResult(func() {
			ContinueWith("list_orders_page", "cursor")
			NextCursor("next_cursor")
		})
		Confirmation(func() {
			Title("A|B,C")
			PromptTemplate("D: {{ .order_id }}")
			DeniedResultTemplate("E")
		})
	})
	Tool("list_orders_page", "Continue listing orders", func() {
		Args(LookupArgs)
		Return(LookupResult)
		BoundedResult(func() {
			Cursor("cursor")
			NextCursor("next_cursor")
		})
	})
})

var _ = Service("orders", func() {
	JSONRPC(func() {
		POST("/mcp")
	})
	MCP("orders_mcp", "1.0.0")
	Method("get_order", func() {
		Payload(func() {
			Field(1, "id", String)
			Required("id")
		})
		Result(String)
		HTTP(func() {
			GET("/orders")
			Header("id:X-Order-ID")
			Response(StatusOK)
		})
		GRPC(func() {
			Response(CodeOK)
		})
		Tool("get_order", "Get order by ID", func() {
			ToolVisibility("model")
			ReadOnlyHint(true)
		})
	})
	Agent("tenant_agent", "Binds tenant_id", func() {
		Use(OrderTools, func() {
			BindField("tenant_id")
		})
		RunPolicy(func() {
			DefaultCaps(MaxToolCalls(4))
			TimeBudget("1m")
		})
	})
	Agent("order_agent", "Binds order_id", func() {
		Use(OrderTools, func() {
			BindInput("list_orders", "order_id")
		})
		RunPolicy(func() {
			DefaultCaps(MaxToolCalls(4))
			TimeBudget("1m")
		})
	})
})
`
	newDesign := `package design

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

var LookupArgs = Type("LookupArgs", func() {
	Field(1, "tenant_id", String)
	Field(2, "order_id", String)
	Field(3, "cursor", String)
	Required("tenant_id", "order_id")
})

var LookupResult = Type("LookupResult", func() {
	Field(1, "items", ArrayOf(String))
	Required("items")
})

var OrderTools = Toolset("order_tools", func() {
	Tool("list_orders", "List orders", func() {
		Args(LookupArgs)
		Return(LookupResult)
		BoundedResult(func() {
			ContinueWith("list_orders_page", "cursor")
			NextCursor("next_cursor")
		})
		Confirmation(func() {
			Title("A")
			PromptTemplate("B,C|D: {{ .order_id }}")
			DeniedResultTemplate("E")
		})
	})
	Tool("list_orders_page", "Continue listing orders", func() {
		Args(LookupArgs)
		Return(LookupResult)
		BoundedResult(func() {
			Cursor("cursor")
			NextCursor("next_cursor")
		})
	})
})

var _ = Service("orders", func() {
	JSONRPC(func() {
		POST("/mcp")
	})
	MCP("orders_mcp", "1.0.0")
	Method("get_order", func() {
		Payload(func() {
			Field(2, "id", String)
			Required("id")
		})
		Result(String)
		HTTP(func() {
			GET("/orders")
			Param("id:order_id")
			Response(StatusOK)
		})
		GRPC(func() {
			Response(CodeOK)
		})
		Tool("get_order", "Get order by ID", func() {
			ToolVisibility("app")
			DestructiveHint(true)
		})
	})
	Agent("tenant_agent", "Binds tenant_id", func() {
		Use(OrderTools, func() {
			BindField("tenant_id")
		})
		RunPolicy(func() {
			DefaultCaps(MaxToolCalls(4))
			TimeBudget("1m")
		})
	})
	Agent("order_agent", "Binds order_id", func() {
		Use(OrderTools, func() {
			BindInput("list_orders", "order_id")
		})
		RunPolicy(func() {
			DefaultCaps(MaxToolCalls(4))
			TimeBudget("1m")
		})
	})
})
`
	rep, err := Compare(ctx, oldDesign, newDesign, "", "")
	if err != nil {
		t.Fatalf("Compare returned unexpected error: %v", err)
	}
	for _, wantMod := range []string{
		"Service:orders/Method:get_order",
		"Service:orders/MCP:orders_mcp/Tool:get_order",
		"Toolset:order_tools/Tool:list_orders",
	} {
		if !slices.Contains(rep.ModifiedDeclarations, wantMod) {
			t.Fatalf("expected ModifiedDeclarations to contain %q, got %v", wantMod, rep.ModifiedDeclarations)
		}
	}

	evaluated, err := designeval.Evaluate(ctx, newDesign)
	if err != nil {
		t.Fatalf("Evaluate failed: %v", err)
	}
	var cardIDs []string
	for _, tc := range evaluated.Tools {
		cardIDs = append(cardIDs, tc.ID)
		if tc.ID == "order_tools.list_orders" {
			if strings.Contains(tc.ArgsSchema, `"cursor"`) {
				t.Fatalf("expected continuation cursor field to be hidden from initial tool args schema: %s", tc.ArgsSchema)
			}
			if !strings.Contains(tc.ReturnSchema, `"returned"`) || !strings.Contains(tc.ReturnSchema, `"truncated"`) {
				t.Fatalf("expected bounded-result fields in return schema: %s", tc.ReturnSchema)
			}
		}
		if tc.ID == "Service:orders/Agent:tenant_agent/order_tools.list_orders" {
			if strings.Contains(tc.ArgsSchema, `"tenant_id"`) || !strings.Contains(tc.ArgsSchema, `"order_id"`) {
				t.Fatalf("expected tenant_agent card to hide tenant_id and keep order_id: %s", tc.ArgsSchema)
			}
		}
		if tc.ID == "Service:orders/Agent:order_agent/order_tools.list_orders" {
			if strings.Contains(tc.ArgsSchema, `"order_id"`) || !strings.Contains(tc.ArgsSchema, `"tenant_id"`) {
				t.Fatalf("expected order_agent card to hide order_id and keep tenant_id: %s", tc.ArgsSchema)
			}
		}
	}
	for _, wantCard := range []string{
		"order_tools.list_orders",
		"Service:orders/Agent:tenant_agent/order_tools.list_orders",
		"Service:orders/Agent:order_agent/order_tools.list_orders",
	} {
		if !slices.Contains(cardIDs, wantCard) {
			t.Fatalf("expected tool cards to include %q without overwriting, got %v", wantCard, cardIDs)
		}
	}
}
