# CLI, MCP & Repository Reference

**[← Back to README](../README.md)** · **[← How Scantle Works](HOW_IT_WORKS.md)** · **[← Golden Path Architecture](ARCHITECTURE_PLAYBOOK.md)**

---

## Quickstart & MCP Configuration

### Prerequisites
- **Go 1.27.3+**, matching the repository's `go.mod` files, and the **Goa CLI** (`go install goa.design/goa/v3/cmd/goa@latest`)
- **macOS** (`sandbox-exec` is used by `verify/candidate` to isolate candidate builds and processes; non-macOS hosts fail closed with `ErrIsolationUnavailable`)
- **Temporal CLI** (`brew install temporal`) for running the end-to-end durable workflow crash and replay verification suites

### Start the Scantle Server

To enable feature-request analysis, set `ANTHROPIC_API_KEY` and set `SCANTLE_MODEL_NAME` to a model ID available to that key, then run:

```bash
export SCANTLE_AUTH_TOKEN="dev-secret-token"
go run ./cmd/scantle serve -addr 127.0.0.1:8090 \
  -principal operator@example.com \
  -model-provider anthropic -model-name "$SCANTLE_MODEL_NAME"
```

For OpenAI, use `-model-provider openai`, set `OPENAI_API_KEY`, and choose a model ID available to that key. Alternatively, use `-model-gateway-addr <host:port>` alone to call an existing model gateway; the gateway chooses the model. That connection uses plaintext gRPC and is intended for loopback or a trusted network.

Omitting all model flags starts the server without feature-request analysis. The `diff` tool, Golden Path resource, and `compiled_rule` rebuild response still work; `propose_change` returns `cp1 semantic analyzer is required`.

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

### Configure the Proposed Swarm Roster

To include agent assignments in an architecture preview, pass `-swarm-profiles ./swarm-profiles.json` when starting the server. Save a JSON array with these fields, replacing the model placeholders with IDs available to your coding agents:

```json
[
  {
    "id": "refund-builder",
    "provider": "anthropic",
    "model": "YOUR_ANTHROPIC_MODEL",
    "family": "claude",
    "authenticated": true,
    "capabilities": ["builder"]
  },
  {
    "id": "independent-review",
    "provider": "openai",
    "model": "YOUR_OPENAI_MODEL",
    "family": "gpt",
    "authenticated": true,
    "capabilities": ["architect", "eval_author", "reviewer"]
  }
]
```

These profiles describe coding agents separately from the CP1 analysis model. `authenticated: true` is the operator's assertion that the profile has usable credentials; the command does not test those credentials or discover installed coding agents. Keep credentials out of this file. Profiles from the same model family must use the same `family` value.

After confirming a Change Card, include explicit builder ownership in the `preview_architecture` request. For example, a builder for the orders service can supply this `builders` field alongside `change_id`, `request_revision`, and `design`:

```json
{
  "builders": [
    {
      "name": "orders",
      "paths": ["internal/orders/**"],
      "preferred_profile_id": "refund-builder"
    }
  ]
}
```

Paths are relative to the target repository and must cover the files assigned to that builder. With no eligible profiles, the architecture preview returns the evaluated design with an `unconfigured` roster. If eligible profiles lack a required role, it returns `missing_capability`. Once all roles are covered, omitting builder scopes returns `unconfigured` with `builder` listed in `missing_roles`.

A configured roster reports its actual `cross_family_review` and `cross_family_eval` values; family choices and capabilities determine whether those roles differ from every builder. The preview returns proposed assignments and manifest text; it does not launch agents or publish a Chorus manifest.

---

## MCP Tools & Resources

All tools and resources are served over MCP JSON-RPC at `POST /mcp` and over HTTP REST on the same listen address (authenticated via `Authorization: Bearer <token>`).

### MCP Tools

- **`propose_change`** (`POST /changes/propose`)
  - **Input:** `change_id`, `prompt`, `design_source?` → **Output:** `ChangeCardResult`
  - Analyzes a feature request at `CP1` using the embedded Golden Path prompt, returning concrete `examples`, `ambiguities`, `preserved_behaviors`, and a canonical `request_revision` digest.
- **`confirm_intent`** (`POST /changes/confirm`)
  - **Input:** `change_id`, `request_revision`, `answers`, `examples` → **Output:** `ChangeCardResult`
  - Locks the operator's ambiguity `answers` and confirmed `examples` at `CP1`, transitioning the Change Card to `confirmed`.
- **`preview_architecture`** (`POST /changes/preview`)
  - **Input:** `change_id`, `request_revision`, `design`, `builders?` → **Output:** `ArchitecturePreviewResult`
  - Evaluates a candidate `DesignSnapshot` in a sandboxed subprocess at `CP2`, returning C4 `containers` and `edges`, `agent_policies`, `tools` (`input_binding`), and the proposed Chorus `swarm` plan.
- **`draft_override`** (`POST /sandbox/draft`)
  - **Input:** `target_kind` (`prompt_template` \| `compiled_rule`), `prompt_id?`, `new_content` → **Output:** `DraftOverrideResult`
  - Applies a `prompt_template` to a target configured through `controlplane.Options.TargetUpdater`, without rebuilding Go code, or reports that a `compiled_rule` requires a rebuild. The `serve` command does not bind a prompt updater, so its `prompt_template` requests return `no target prompt updater is bound to this control plane`.
- **`diff`** (`POST /diff`)
  - **Input:** `old` (`DesignSnapshot`), `new` (`DesignSnapshot`) → **Output:** `DiffResult`
  - Compares two `DesignSnapshot` modules by evaluating their generated Goa/Goa-AI artifacts, tool schemas, and `go.mod` directives.

### MCP Resources

- **`scantle://golden-path`** (`GET /golden-path`, `text/markdown`)
  - Serves the embedded Golden Path architecture playbook ([`internal/goldenpath/GOLDEN_PATH.md`](../internal/goldenpath/GOLDEN_PATH.md)) for Goa and Goa-AI platforms.
- **`ui://scantle/checkpoint`** (`GET /ui/checkpoint`, `text/html;profile=mcp-app`)
  - Serves the stateless MCP Apps checkpoint viewer (`internal/controlplane/checkpoint.html`). Completes the `ui/initialize` → `ui/notifications/initialized` host handshake and renders validated `ChangeCardResult` (`CP1`), `ArchitecturePreviewResult` (`CP2`), and `DraftOverrideResult` (`CP3`) payloads delivered by the host via `ui/notifications/tool-result` without server-retained preview state (interactive role-switching/replay controls and `openui.v1` action dispatch remain planned; see [`docs/HOW_IT_WORKS.md`](HOW_IT_WORKS.md#checkpoint-3-cp3-fast-draft-overrides--live-sandbox-walkthrough)).

---

## CLI Reference

### `scantle serve` *(default)*
Starts the Scantle HTTP and MCP JSON-RPC control-plane server protected by bearer-token authentication.
- `-addr <host:port>` — Listen address (default `127.0.0.1:8090`).
- `-principal <id>` — Authenticated operator principal ID *(required)*.
- `-auth-token <token>` — Bearer token required on HTTP and MCP requests (or `$SCANTLE_AUTH_TOKEN`, *required*).
- `-model-gateway-addr <host:port>` — Optional model gateway for CP1 analysis over plaintext gRPC. The gateway chooses the model; cannot be combined with `-model-provider` or `-model-name`.
- `-model-provider <anthropic|openai>` — Optional direct provider for CP1 analysis. Requires `-model-name` and the matching `ANTHROPIC_API_KEY` or `OPENAI_API_KEY` environment variable. Provider names must match exactly.
- `-model-name <model>` — Model ID for direct provider mode; required with `-model-provider` and rejected in gateway mode.
- `-swarm-profiles <path.json>` — Optional JSON array of configured coding-agent profiles for proposed CP2 assignments; see [Configure the Proposed Swarm Roster](#configure-the-proposed-swarm-roster).

Model and swarm-profile flags are independent. Incomplete or mixed model options stop startup with an error. A supplied profiles file must contain one JSON array: unknown fields, trailing JSON values, duplicate profile IDs, empty required metadata, and unknown capabilities stop startup. Omitted profiles leave the proposed roster unconfigured.

### `scantle diff`
Reads two Go design modules into symlink-safe snapshots, evaluates both inside the sandboxed Goa/Goa-AI compiler at their own `go.mod` dependency versions, and prints the JSON `DiffResult` to `stdout`.
- `--old-dir <path>` — Root directory of the previous design module *(required)*.
- `--new-dir <path>` — Root directory of the candidate design module *(required)*.

### `scantle verify`
Clones the committed candidate and evaluator suite into an isolated work directory, builds the candidate binaries (`WorkDir/candidate-bin/<name>`) and suite runner (`WorkDir/suite-bin/suite`) inside `verify/candidate`'s macOS `sandbox-exec` jail, runs the suite, writes the canonical `verify.Report` JSON to `<report-dir>/<digest>.json`, and prints the JSON report to `stdout` (setup or build errors exit non-zero without printing report JSON, while an assembled report with a failing scenario is written and printed before exiting non-zero).
- `-candidate-repo <path|url>` — Git repository holding the candidate *(required)*.
- `-candidate-commit <sha>` — Full commit ID of the candidate to build *(required)*.
- `-candidate-module <dir>` — Candidate Go module directory relative to `-candidate-repo` *(required)*.
- `-binary <name=package>` — Candidate binary artifact name and relative package path (`.` or `./...`) inside `-candidate-module` (repeatable, at least one *required*; `<name>` must be a single path element).
- `-suite-repo <path|url>` — Git repository holding the evaluator suite *(required)*.
- `-suite-commit <sha>` — Full commit ID of the evaluator suite *(required)*.
- `-suite-module <dir>` — Suite Go module directory relative to `-suite-repo` *(required)*.
- `-suite-command <package>` — Suite command relative package path (`.` or `./...`) inside `-suite-module` *(required)*.
- `-request-revision <digest>` — Confirmed `request_revision` digest bound into the verification report *(required)*.
- `-work-dir <path>` — Private working directory for clones, sandboxed builds, and evidence; must be missing or empty at run start and must not be shared with or readable by builder agents *(required)*.
- `-report-dir <path>` — Directory that receives the full canonical report as `<digest>.json`; must not be readable by builder agents when holdout privacy is required *(required)*.
- `-builder-view` — Optional boolean flag; when set, `stdout` prints `Report.ForBuilders()`, which redacts `"holdout"` scenarios to `"id": "holdout-N"` with scenario names, inputs, declarations, checks, and failure logs stripped (the full unredacted report is still written to `-report-dir`).

---

## Reference Platform: `examples/orders` & `examples/orders-eval`

Scantle includes an order-refund service and an isolated evaluator for checking payment effects, manager approval, worker recovery, and token budgets:

### [`examples/orders`](../examples/orders/) — The Candidate Platform
- **[`design/`](../examples/orders/design/)**: Declares the public `orders` HTTP service, internal `commerce` and `payments` services, the `goa-ai` `refund` agent (`Inject("tenant_id", "customer_id")`, `BoundedResult`, and `Confirmation`), and the C4 architecture model rendered to [`gen/structurizr/model.json`](../examples/orders/gen/structurizr/model.json).
- **[`AGENTS_QUICKSTART.md`](../examples/orders/AGENTS_QUICKSTART.md)**: Generated contextual map for coding agents, surfacing per-tool `BindTo` method links, server-injected fields, `BoundedResult` cursor guidance, `Confirmation` gates, and `gen/orders/agents/refund/specs/tool_schemas.json`.
- **[`internal/refundflow`](../examples/orders/internal/refundflow/)**: Durable Temporal workflow implementing instant store credit (`<= \$50.00` including tax and shipping) and manager approval signals (`> \$50.00`) with idempotent payment keys.
- **[`internal/runstore`](../examples/orders/internal/runstore/)**: Transactional SQLite implementation of `goa-ai`'s durable `storage.Store`.

### [`examples/orders-eval`](../examples/orders-eval/) — The Isolated Evaluator
- **[`acceptance/acceptance_test.go`](../examples/orders-eval/acceptance/acceptance_test.go)**: Compiles and boots the candidate inside `verify/candidate`'s macOS `sandbox-exec` jail against stateful commerce and payment simulators ([`sim/`](../examples/orders-eval/sim/)) and a scripted model gateway ([`scriptedmodel/`](../examples/orders-eval/scriptedmodel/)).
- Exercises five acceptance scenarios (`store_credit_within_limit`, `card_refund_needs_manager`, `refund_survives_worker_crash`, `pending_approval_survives_api_restart`, and the holdout `other_customers_order_refused`) plus `TestSeededPayloadBloatFailsTheBudget`.

Run the unit and race checks across all three modules:

```bash
go test -race ./...
(cd examples/orders && go test -race ./...)
(cd examples/orders-eval && go test -race ./...)
```

The end-to-end tests require the `e2e` build tag, macOS, and the Temporal CLI. Run the five acceptance scenarios and the seeded token-budget regression, then the verifier's complete `Run` → report → `Approve` test:

```bash
(cd examples/orders-eval && go test -race -tags e2e ./acceptance)
go test -race -tags e2e ./internal/verify
```

See [`examples/orders/README.md`](../examples/orders/README.md) for a step-by-step walkthrough including `scantle verify` (full operator report vs. `-builder-view`).

---

## Project Layout

```text
.
├── design/
│   └── design.go                 # Scantle Goa & Goa-AI MCP 1.0.0 DSL contract
├── cmd/
│   └── scantle/
│       └── main.go               # CLI entrypoint (`scantle serve`, `scantle diff`, and `scantle verify`)
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
