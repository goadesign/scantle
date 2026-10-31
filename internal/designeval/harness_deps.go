//go:build designeval_harness

// This file is never compiled into Scantle. It imports the generator packages
// the evaluator program (harnessMainHead) imports, so go mod tidy keeps their
// modules in Scantle's go.mod: evaluatorGoMod reads those requirements as the
// versions a design that does not require a generator module is evaluated
// with.
package designeval

import (
	_ "goa.design/goa-ai/codegen/agent"
	_ "goa.design/goa-ai/codegen/jsonschema"
	_ "goa.design/goa-ai/codegen/mcp"
	_ "goa.design/goa-ai/eval/expr"
	_ "goa.design/goa-ai/expr/agent"
	_ "goa.design/goa-ai/expr/mcp"
	_ "goa.design/goa/v3/codegen/generator"
	_ "goa.design/goa/v3/eval"
	_ "goa.design/goa/v3/expr"
	_ "goa.design/model/expr"
)
