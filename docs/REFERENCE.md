# CLI, MCP & Repository Reference

**[← Back to README](../README.md)** · **[← How Scantle Works](HOW_IT_WORKS.md)** · **[← Golden Path Architecture](ARCHITECTURE_PLAYBOOK.md)**

---

## Quickstart & MCP Configuration

### Prerequisites
- **Go 1.26+** and the **Goa CLI** (`go install goa.design/goa/v3/cmd/goa@latest`)
- **macOS** (`sandbox-exec` is used by `verify/candidate` to isolate candidate builds and processes; non-macOS hosts fail closed with `ErrIsolationUnavailable`)
- **Temporal CLI** (`brew install temporal`) for running the end-to-end durable workflow crash and replay verification suites

### Start the Scantle Server

```bash
export SCANTLE_AUTH_TOKEN="dev-secret-token"
go run ./cmd/scantle serve -addr 127.0.0.1:8090 -principal operator@example.com
```

### Register Scantle in `.mcp.json`

Add Scantle to your project's `.mcp.json` (or global agent configuration in Claude Code, Codex, Gemini, or Cursor):

```json
{
  "mcpServers": {
    "scantle": {
      "type": "http",
      "url": "http://127.0.0.1:8090/mcp",
      "headers": {
        "Authorization": "Bearer dev-secret-token"
      }
    }
  }
}
```

---

## MCP Tools & Resources

All tools and resources are served over MCP JSON-RPC at `POST /mcp` and over HTTP REST on the same listen address (authenticated via `Authorization: Bearer <token>`).

### MCP Tools

| Tool | HTTP Route | Input Payload | Output Result | Purpose |
| :--- | :--- | :--- | :--- | :--- |
| `propose_change` | `POST /changes/propose` | `change_id`, `prompt`, `design_source?` | `ChangeCardResult` | Analyze a feature request at `CP1` using the embedded Golden Path prompt, returning concrete `examples`, `ambiguities`, `preserved_behaviors`, and a `request_revision` digest. |
| `confirm_intent` | `POST /changes/confirm` | `change_id`, `request_revision`, `answers`, `examples` | `ChangeCardResult` | Lock the operator's ambiguity `answers` and confirmed `examples` at `CP1`, transitioning the Change Card to `confirmed`. |
| `preview_architecture` | `POST /changes/preview` | `change_id`, `request_revision`, `design`, `builders?` | `ArchitecturePreviewResult` | Evaluate a candidate `DesignSnapshot` in a sandboxed subprocess at `CP2`, returning C4 `containers` and `edges`, `agent_policies`, `tools` (`input_binding`), and the proposed Chorus `swarm` plan. |
| `draft_override` | `POST /sandbox/draft` | `target_kind` (`prompt_template` \| `compiled_rule`), `prompt_id?`, `new_content` | `DraftOverrideResult` | Hot-reload a `prompt_template` in `0s` on the bound `CP3` sandbox target or reject a `compiled_rule` override that requires a full Go rebuild. |
| `diff` | `POST /diff` | `old` (`DesignSnapshot`), `new` (`DesignSnapshot`) | `DiffResult` | Compare two `DesignSnapshot` modules by evaluating their generated Goa/Goa-AI artifacts, tool schemas, and `go.mod` directives. |

### MCP Resources

| Resource URI | HTTP Route | MIME Type | Purpose |
| :--- | :--- | :--- | :--- |
| `scantle://golden-path` | `GET /golden-path` | `text/markdown` | Embedded Golden Path architecture playbook ([`internal/goldenpath/GOLDEN_PATH.md`](../internal/goldenpath/GOLDEN_PATH.md)) for Goa and Goa-AI platforms. |
| `ui://scantle/checkpoint` | `GET /ui/checkpoint` | `text/html;profile=mcp-app` | Interactive MCP Apps HTML host for `CP1`–`CP3` checkpoints and sandboxed `openui.v1` action dispatch. |

---

## CLI Reference

| Command | Flags | Description |
| :--- | :--- | :--- |
| `scantle serve` *(default)* | `-addr 127.0.0.1:8090`<br/>`-principal <id>` *(required)*<br/>`-auth-token <token>` *(or `$SCANTLE_AUTH_TOKEN`, required)* | Start the Scantle HTTP and MCP JSON-RPC control-plane server protected by bearer-token authentication. |
| `scantle diff` | `--old-dir <path>` *(required)*<br/>`--new-dir <path>` *(required)* | Read two Go design modules into symlink-safe snapshots, evaluate both inside the sandboxed Goa/Goa-AI compiler, and print the JSON `DiffResult` to `stdout`. |

---

## Reference Platform: `examples/orders` & `examples/orders-eval`

Scantle ships with a complete reference platform and isolated evaluator so you can inspect and run every pattern locally:

### [`examples/orders`](../examples/orders/) — The Candidate Platform
- **[`design/design.go`](../examples/orders/design/design.go) & [`design/model.go`](../examples/orders/design/model.go)**: Declares the public `orders` HTTP service, internal `commerce` and `payments` services, the `goa-ai` `refund` agent (`Inject("tenant_id", "customer_id")`, `BoundedResult`, and `Confirmation`), and the C4 architecture model rendered to [`gen/structurizr/model.json`](../examples/orders/gen/structurizr/model.json).
- **[`AGENTS_QUICKSTART.md`](../examples/orders/AGENTS_QUICKSTART.md)**: Generated contextual map for coding agents, surfacing per-tool `BindTo` method links, server-injected fields, `BoundedResult` cursor guidance, `Confirmation` gates, and `gen/orders/agents/refund/specs/tool_schemas.json`.
- **[`internal/refundflow`](../examples/orders/internal/refundflow/)**: Durable Temporal workflow implementing instant store credit (`<= \$50.00` including tax and shipping) and manager approval signals (`> \$50.00`) with idempotent payment keys.
- **[`internal/runstore`](../examples/orders/internal/runstore/)**: Transactional SQLite implementation of `goa-ai`'s durable `storage.Store`.

### [`examples/orders-eval`](../examples/orders-eval/) — The Isolated Evaluator
- **[`acceptance/acceptance_test.go`](../examples/orders-eval/acceptance/acceptance_test.go)**: Compiles and boots the candidate inside `verify/candidate`'s macOS `sandbox-exec` jail against stateful commerce and payment simulators ([`sim/`](../examples/orders-eval/sim/)) and a scripted model gateway ([`scriptedmodel/`](../examples/orders-eval/scriptedmodel/)).
- Exercises five acceptance scenarios (instant store credit, manager approval across an API restart, Temporal worker `kill -9` crash recovery, manager decline, and a holdout cross-customer refund refusal) plus `TestBloatedOrderPageFailsTokenBudget`.

```bash
# Run all unit, race, and sandboxed acceptance suites across the repository
go test -race ./...
(cd examples/orders && go test -race ./...)
(cd examples/orders-eval && go test -race ./...)
```

---

## Project Layout

```text
.
├── design/
│   └── design.go                 # Scantle Goa & Goa-AI MCP 1.0.0 DSL contract
├── cmd/
│   └── scantle/
│       └── main.go               # CLI entrypoint (`scantle serve` and `scantle diff`)
├── docs/
│   ├── HOW_IT_WORKS.md           # The 5 checkpoints, Chorus routing & sandbox verification
│   ├── ARCHITECTURE_PLAYBOOK.md  # Golden Path rules & Goa Design suite overview
│   └── REFERENCE.md              # MCP tools/resources, CLI flags & repo layout
├── internal/
│   ├── controlplane/             # CP1–CP3 handlers, bearer auth, MCP Apps host, CP1 schema validator
│   ├── designeval/               # Sandboxed Goa/Goa-AI/Model subprocess evaluator & C4 extractor
│   ├── diff/                     # Structural declaration, tool schema, and go.mod version diff engine
│   ├── gitcapture/               # Read-only dirty git working-tree snapshot capture
│   ├── goldenpath/               # Embedded GOLDEN_PATH.md playbook & CP1 architect system prompt
│   ├── procgroup/                # Process-group lifecycle management for sandboxed child processes
│   ├── swarm/                    # Deterministic Chorus role assignment & cross-family model router
│   └── verify/                   # End-to-end worktree verifier, holdout redactor, and CP5 merge gate
├── verify/
│   └── candidate/                # macOS Seatbelt (`sandbox-exec`) build & runtime isolation jail
├── examples/
│   ├── orders/                   # Reference Goa + Goa-AI + Temporal + SQLite order-refund platform
│   └── orders-eval/              # Isolated evaluator harness, simulators, and 5 acceptance scenarios
├── gen/                          # Generated Goa HTTP, JSON-RPC, and MCP server/client packages
├── PROPOSAL.md                   # Full architecture & verification design specification
└── PRESS_RELEASE.md              # Working Backwards product vision
```
