//go:build ignore

// Command export_schemas_gen emits the Goa-generated tool ResultSchemas (from
// gen/surla/toolsets/surla) and MCP OutputSchemas (from gen/mcp_surla) as
// JSON on stdout so ui/checkpoint/build.mjs derives frontend TypeScript types
// and runtime JSON-Schema validators directly from the public generated APIs.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	genmcp "github.com/goadesign/surla/gen/mcp_surla"
	gensurlatoolset "github.com/goadesign/surla/gen/surla/toolsets/surla"
	"goa.design/goa-ai/runtime/agent/tools"
)

type exportedSchemas struct {
	Registry map[string]json.RawMessage `json:"registry"`
	MCP      map[string]json.RawMessage `json:"mcp"`
}

func main() {
	out := exportedSchemas{
		Registry: make(map[string]json.RawMessage),
		MCP:      make(map[string]json.RawMessage),
	}

	for _, decl := range gensurlatoolset.ToolSchemas() {
		if decl == nil || len(decl.ResultSchema) == 0 {
			fmt.Fprintf(os.Stderr, "empty ResultSchema on toolset declaration %+v\n", decl)
			os.Exit(1)
		}
		byName, ok := gensurlatoolset.ToolSchema(tools.Ident(decl.Name))
		if !ok || byName == nil || string(byName.ResultSchema) != string(decl.ResultSchema) {
			fmt.Fprintf(os.Stderr, "ToolSchema(%q) lookup mismatch with ToolSchemas()\n", decl.Name)
			os.Exit(1)
		}
		out.Registry[decl.Name] = json.RawMessage(decl.ResultSchema)
	}

	adapter := genmcp.NewMCPAdapter(nil, nil)
	listRes, err := adapter.ToolsList(context.Background(), &genmcp.ToolsListPayload{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "NewMCPAdapter(nil, nil).ToolsList: %v\n", err)
		os.Exit(1)
	}
	for _, tool := range listRes.Tools {
		if tool == nil || len(tool.OutputSchema) == 0 {
			continue
		}
		out.MCP[tool.Name] = tool.OutputSchema
	}

	enc := json.NewEncoder(os.Stdout)
	if err := enc.Encode(out); err != nil {
		fmt.Fprintf(os.Stderr, "encode exported schemas: %v\n", err)
		os.Exit(1)
	}
}
