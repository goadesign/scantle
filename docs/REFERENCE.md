# CLI, MCP & Repository Reference

**[← Back to README](../README.md)** · **[← How Surla Works](HOW_IT_WORKS.md)** · **[← Golden Path Architecture](ARCHITECTURE_PLAYBOOK.md)**

---

## Terminology at a Glance

| Term | Plain-English Meaning |
| :--- | :--- |
| **`CP1` – `CP5`** | Surla's five verification checkpoints: **CP1** Confirm Intent (`propose_change`, `confirm_intent`), **CP2** Preview Compiled Architecture & Swarm (`preview_architecture`, or local `surla preview`), **CP3** Fast Draft & Live Sandbox (`draft_override`), **CP4** Isolated Sandboxed Verification (`surla verify`), and **CP5** Promotion Check (`surla approve`, `approve_merge`). |
| **`Change Card`** | The versioned change plan (`ChangeCardResult`) created at `CP1`, holding concrete input/outcome examples, open domain ambiguity questions, confirmed answers, preserved contract behaviors, and optional `CP5` approval metadata. |
| **`request_revision`** | The SHA-256 hex digest of a Change Card's canonical content. `preview_architecture`, `surla verify`, `surla approve`, and `approve_merge` bind to `request_revision` so changing the agreed examples or answers automatically invalidates stale previews, reports, and approvals. When running standalone `surla verify` and `surla approve` on the CLI without a control-plane Change Card, pass any explicit revision string (such as a commit or label) consistently to both commands. |
| **`holdout`** | An unseen acceptance scenario (tagged `"holdout"` in the evaluator suite) whose name, inputs, checks, and logs are redacted to `"id": "holdout-N"` in `surla verify -builder-view` (`Report.ForBuilders()`) so coding agents cannot hardcode around the test cases. |
| **`input_binding`** | The compiled tool input contract (`ToolInputBindingItem`) returned in architecture previews, containing `args_schema`, `text_only_args_schema`, `lookup_args_schema`, `lookup_text_only_args_schema`, and `injected_fields` (the field names declared with `Inject(...)` in Goa-AI, which are omitted from the model-facing `args_schema`). |
| **`TargetUpdater` / `PromotionTarget`** | Optional Go interfaces in `controlplane.Options` bound when embedding the Surla control plane inside a custom host (`TargetUpdater` applies in-memory `CP3` prompt overrides; `PromotionTarget` loads `CP4` verification reports and measured promotion inputs for `approve_merge`). Standalone `surla serve` binds neither interface; use CLI `surla approve` for standalone report verification. |

---

## Quickstart & MCP Configuration

### Prerequisites
- **Go 1.27.3+**, matching the repository's `go.mod` files, and the **Goa CLI** (`go install goa.design/goa/v3/cmd/goa@latest`)
- **macOS** (`sandbox-exec` is used by `verify/candidate` to isolate candidate builds and processes; non-macOS hosts fail closed with `ErrIsolationUnavailable`)
- **Temporal CLI** (`brew install temporal`) for running the end-to-end durable workflow crash and replay verification suites

### Start the Surla Server

To enable feature-request analysis, set `ANTHROPIC_API_KEY` and set `SURLA_MODEL_NAME` to a model ID available to that key, then run:

```bash
export SURLA_AUTH_TOKEN="dev-secret-token"
go run ./cmd/surla serve -addr 127.0.0.1:8090 \
  -principal operator@example.com \
  -model-provider anthropic -model-name "$SURLA_MODEL_NAME"
```

For OpenAI, use `-model-provider openai`, set `OPENAI_API_KEY`, and choose a model ID available to that key. Alternatively, use `-model-gateway-addr <host:port>` alone to call an existing model gateway; the gateway chooses the model. That connection uses plaintext gRPC and is intended for loopback or a trusted network.

Omitting all model flags starts the server without feature-request analysis. The `diff` tool, Golden Path resource, and `compiled_rule` rebuild response still work; `propose_change` returns `cp1 semantic analyzer is required`.

### Register Surla in `.mcp.json`

Add Surla to your project's `.mcp.json` (or global agent configuration in Claude Code, Codex, Gemini, or Cursor):

```json
{
  "mcpServers": {
    "surla": {
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
  - Analyzes a feature request at `CP1` using the embedded Golden Path prompt, returning concrete `examples`, `ambiguities`, `preserved_behaviors`, and a canonical `request_revision` digest. When the model also composed a decision view, the card carries `ui_composition` (`program`, `composed_for_revision`): it is presentation only, never hashed into `request_revision`, and dropped when `confirm_intent` changes answers or examples. When the request includes a `progressToken`, the tool sends MCP progress updates before and after the model call.
- **`confirm_intent`** (`POST /changes/confirm`)
  - **Input:** `change_id`, `request_revision`, `answers`, `examples` → **Output:** `ChangeCardResult`
  - Confirms a `needs_confirmation` Change Card or refines an already `confirmed` Change Card at `CP1` when called by the owning principal with the current `request_revision` and complete `answers` and `examples`. No-op submissions return the stored `request_revision` (preserving any recorded `approval`); changed answers or examples mint a new canonical `request_revision` digest and clear `approval`.
- **`get_change`** (`GET /changes/{change_id}`)
  - **Input:** `change_id` → **Output:** `ChangeCardResult`
  - Reads the stored `ChangeCardResult` (including any recorded `approval`) for `change_id` without mutating state (restricted to the owning principal) so a stale editor can fetch the latest saved card after a `request_revision` conflict.
- **`approve_merge`** (`POST /changes/approve`)
  - **Input:** `change_id`, `request_revision`, `candidate_commit`, `report_digest` → **Output:** `ChangeCardResult`
  - Records Checkpoint 5 (`CP5`) promotion approval on the owning principal's confirmed `ChangeCardResult` when `controlplane.Options.PromotionTarget` is bound. It loads the full `verify.Report` for `report_digest` and the current `PromotionInputs` (`SuiteID`, `SuiteCommit`, `Environment`, measured candidate binary `Artifacts`) for `candidate_commit` from `PromotionTarget`, runs `verify.Approve`, re-verifies that the card still has `request_revision`, and returns the updated `ChangeCardResult` with `approval` (`ApprovalItem`: `approved_by`, `report_digest`, `candidate_commit`, `suite_id`, `suite_commit`, `environment`, `artifacts`, `scenario_count`) populated in control-plane memory. Recording approval does not merge branches or deploy binaries. Standalone `surla serve` does not bind a `PromotionTarget`, so its `approve_merge` requests return `no promotion target is bound to this control plane`.
- **`preview_architecture`** (`POST /changes/preview`)
  - **Input:** `change_id`, `request_revision`, `design`, `builders?` → **Output:** `ArchitecturePreviewResult`
  - Evaluates a candidate `DesignSnapshot` in a sandboxed subprocess at `CP2`, returning C4 `containers` and `edges`, `agent_policies`, `tools` (`input_binding`), and the proposed `swarm` plan. When the request includes a `progressToken`, the tool sends MCP progress updates as design evaluation and roster planning start and finish.
- **`draft_override`** (`POST /sandbox/draft`)
  - **Input:** `target_kind` (`prompt_template` \| `compiled_rule`), `prompt_id?`, `new_content` → **Output:** `DraftOverrideResult`
  - Applies a `prompt_template` to a target configured through `controlplane.Options.TargetUpdater`, without rebuilding Go code, or reports that a `compiled_rule` requires a rebuild. The `serve` command does not bind a prompt updater, so its `prompt_template` requests return `no target prompt updater is bound to this control plane`.
- **`diff`** (`POST /diff`)
  - **Input:** `old` (`DesignSnapshot`), `new` (`DesignSnapshot`) → **Output:** `DiffResult`
  - Compares two `DesignSnapshot` modules by evaluating their generated Goa/Goa-AI artifacts, tool schemas, and `go.mod` directives, paired with `ToolUI("ui://surla/checkpoint")` for structural diff visualization.

### MCP Resources

- **`surla://golden-path`** (`text/markdown` via MCP `resources/read` on `POST /mcp`; JSON-encoded string on `GET /golden-path`)
  - Serves the embedded Golden Path architecture playbook ([`internal/goldenpath/GOLDEN_PATH.md`](../internal/goldenpath/GOLDEN_PATH.md)) for Goa and Goa-AI platforms.
- **`ui://surla/checkpoint`** (`text/html;profile=mcp-app` via MCP `resources/read` on `POST /mcp`; JSON-encoded string on `GET /ui/checkpoint`)
  - Serves the stateless MCP Apps checkpoint viewer (`internal/controlplane/checkpoint.html`, built from [`ui/checkpoint/`](../ui/checkpoint/)). It completes the `ui/initialize` → `ui/notifications/initialized` host handshake and renders results delivered by the host via `ui/notifications/tool-result`, without server-retained preview state: the `CP1` Change Card form (saving through `confirm_intent` and recovering with `get_change` via host `tools/call`, keeping unsaved edits across stale-revision conflicts, and rendering the `#surla-cp5-approval` promotion sign-off panel when `approval` is present on the card), the optional OpenUI decision view from `ui_composition` (labeled as a model-proposed illustration and sharing the form's draft), `CP2` architecture previews (a scrollable C4 graph above inspectable lists), unbound structural diffs, and `CP3` draft results. Interactive role-switching and replay controls, and `openui.v1` action dispatch, remain planned; see [`docs/HOW_IT_WORKS.md`](HOW_IT_WORKS.md#checkpoint-3-cp3-fast-draft-overrides--live-sandbox-walkthrough).

---

## CLI Reference

Running `surla`, `surla help`, `surla -h`, or `surla --help` prints top-level usage to `stdout` and exits `0`. Running `surla help <command>` or `surla <command> -h` prints that subcommand's flags to `stderr` and exits `0`.

### `surla preview`
Reads a Go design module on disk into a symlink-safe snapshot (`go.mod`, `go.sum`, and non-test `.go` files; run `go mod tidy` first if `go.sum` is missing), evaluates it inside the sandboxed Goa/Goa-AI/Model compiler (`designeval.Evaluate`) at **0 LLM tokens**, and prints the indented JSON `designeval.Result` (`system_name`, `declarations`, C4 `containers` and `edges`, `agent_policies`, and `tools`) to `stdout`. Needs no running server, no Change Card, and no LLM API key.
- `-dir <path>` — Root directory of the design module holding `go.mod`, `go.sum`, and `design/` (default `.`).

### `surla serve`
Starts the Surla HTTP and MCP JSON-RPC control-plane server protected by bearer-token authentication. Once the listener is bound, `surla serve` prints a startup summary to `stderr` showing the bound HTTP URL, the MCP endpoint (`http://<addr>/mcp`), and whether `propose_change` is off when no model is configured (the bearer token is never printed in startup logs or `-h` defaults).
- `-addr <host:port>` — Listen address (default `127.0.0.1:8090`).
- `-principal <id>` — Authenticated operator principal ID *(required)*.
- `-auth-token <token>` — Bearer token required on HTTP and MCP requests (default: `$SURLA_AUTH_TOKEN`, *required*).
- `-model-gateway-addr <host:port>` — Optional model gateway for CP1 analysis over plaintext gRPC. The gateway chooses the model; cannot be combined with `-model-provider` or `-model-name`.
- `-model-provider <anthropic|openai>` — Optional direct provider for CP1 analysis. Requires `-model-name` and the matching `ANTHROPIC_API_KEY` or `OPENAI_API_KEY` environment variable. Provider names must match exactly.
- `-model-name <model>` — Model ID for direct provider mode; required with `-model-provider` and rejected in gateway mode.
- `-swarm-profiles <path.json>` — Optional JSON array of configured coding-agent profiles for proposed CP2 assignments; see [Configure the Proposed Swarm Roster](#configure-the-proposed-swarm-roster).

Model and swarm-profile flags are independent. Incomplete or mixed model options stop startup with an error. A supplied profiles file must contain one JSON array: unknown fields, trailing JSON values, duplicate profile IDs, empty required metadata, and unknown capabilities stop startup. Omitted profiles leave the proposed roster unconfigured.

### `surla diff`
Reads two Go design modules into symlink-safe snapshots (`go.mod`, `go.sum`, and non-test `.go` files), evaluates both inside the sandboxed Goa/Goa-AI compiler at their own `go.mod` dependency versions, and prints the JSON `DiffResult` to `stdout`.
- `--old-dir <path>` — Root directory of the previous design module *(required)*.
- `--new-dir <path>` — Root directory of the candidate design module *(required)*.

### `surla verify`
Clones the committed candidate and evaluator suite into an isolated work directory, builds the candidate binaries (`WorkDir/candidate-bin/<name>`) and suite runner (`WorkDir/suite-bin/suite`) inside `verify/candidate`'s macOS `sandbox-exec` jail, runs the suite, writes the canonical `verify.Report` JSON to `<report-dir>/<digest>.json`, and prints the JSON report to `stdout` (setup or build errors exit non-zero without printing report JSON, while an assembled report with a failing scenario is written and printed before exiting non-zero).
- `-candidate-repo <path|url>` — Git repository holding the candidate *(required)*.
- `-candidate-commit <sha>` — Full commit ID of the candidate to build *(required)*.
- `-candidate-module <dir>` — Candidate Go module directory relative to `-candidate-repo` (default `.`).
- `-binary <name=package>` — Candidate binary artifact name and relative package path (`.` or a path beginning with `./`, such as `./cmd/orders`) inside `-candidate-module` (repeatable, at least one *required*; `<name>` must be a single path element).
- `-suite-repo <path|url>` — Git repository holding the evaluator suite *(required)*.
- `-suite-commit <sha>` — Full commit ID of the evaluator suite *(required)*.
- `-suite-module <dir>` — Suite Go module directory relative to `-suite-repo` (default `.`).
- `-suite-command <package>` — Suite command relative package path (`.` or a path beginning with `./`, such as `./cmd/orders_acceptance-evals`) inside `-suite-module` *(required)*.
- `-request-revision <digest>` — Confirmed `request_revision` digest bound into the verification report *(required)*.
- `-work-dir <path>` — Private working directory for clones, sandboxed builds, and evidence; must be missing or empty at run start and must not be shared with or readable by builder agents *(required)*.
- `-report-dir <path>` — Directory that receives the full canonical report as `<digest>.json`; must not be readable by builder agents when holdout privacy is required *(required)*.
- `-builder-view` — Optional boolean flag; when set, `stdout` prints `Report.ForBuilders()`, which redacts `"holdout"` scenarios to `"id": "holdout-N"` with scenario names, inputs, declarations, checks, and failure logs stripped (the full unredacted report is still written to `-report-dir`).

#### Evaluator Suite Command Contract (`-suite-command`)

`surla verify` compiles the Go package named by `-suite-command` inside the macOS build sandbox and invokes the resulting executable twice:

1. **Suite discovery (`--describe`):**
   - `surla verify` runs `<suite-bin> --describe` with no `--artifact` flags.
   - The command must write a single JSON declaration object to `stdout` and exit `0`:
     ```json
     {
       "id": "orders_acceptance",
       "environment": "sim",
       "scenarios": [
         {"id": "store_credit_within_limit", "tags": ["repair"]},
         {"id": "other_customers_order_refused", "tags": ["holdout"]}
       ]
     }
     ```
   - Scenarios whose `tags` contain `"holdout"` are marked `Holdout: true` in the assembled `verify.Report` and redacted in `Report.ForBuilders()` (`-builder-view`).
2. **Suite execution (`--artifact <name>=<path>`):**
   - `surla verify` runs `<suite-bin> --artifact <name>=<path> ...` passing one `--artifact` flag for every `-binary <name>=<package>` bound in the verification plan.
   - The suite pins each candidate binary into a private read-only store with [`candidate.Pin(store, path)`](../verify/candidate/candidate.go), starts candidate processes inside the runtime Seatbelt jail with [`candidate.Exec(ctx, pinned, spec)`](../verify/candidate/candidate.go), and attaches [`pinned.Evidence("<name>")`](../verify/candidate/candidate.go) to every scenario's `eval.Result.Artifacts`:
     ```go
     pinned, err := candidate.Pin(store, artifactPaths["orders"])
     // ...
     res.Artifacts = append(res.Artifacts, pinned.Evidence("orders"))
     ```
     `pinned.Evidence("orders")` produces `eval.Artifact{Name: "candidate:orders", URI: "sha256:<digest>"}`, which `surla verify` checks against the SHA-256 digest it measured when building `orders`. A scenario that omits a bound binary's evidence record or reports a different digest produces a failing `verify.Report` with scenario outcome `"harness_error"`.
   - The suite writes `goa.design/goa-ai/eval.Report` JSON to `stdout` and exits with:
     - **`0`** when all scenarios passed,
     - **`1`** when the suite ran and wrote a valid `eval.Report` JSON to `stdout` with one or more failed scenarios (`!report.Passed`), or
     - **`2` (or any other code)** when the suite could not run (invalid flags, harness setup failure, or crash before producing a report).
   - See [`examples/orders-eval/cmd/orders_acceptance-evals/main.go`](../examples/orders-eval/cmd/orders_acceptance-evals/main.go) and [`examples/orders-eval/acceptance/hooks.go`](../examples/orders-eval/acceptance/hooks.go) for the complete reference implementation.

### `surla approve`
Decodes a saved full `verify.Report` JSON file with `verify.DecodeReport`, checks it against explicit promotion flags with `verify.Approve`, and prints an indented JSON approval summary (`report_digest`, `binding`, `counts`) to `stdout` (exiting `0` when the report passed and matches every flag, or exiting `1` without stdout output if any flag is missing or the report is stale, failed, or unreadable). `surla approve` validates the report file only; it does not contact a control plane, record a Change Card approval, or merge/deploy.
- `-report <path>` — Path of the full verification report JSON written by `surla verify` *(required)*.
- `-candidate-commit <sha>` — Full commit ID of the candidate being promoted *(required)*.
- `-request-revision <digest>` — Current confirmed `request_revision` digest of the change *(required)*.
- `-suite-id <id>` — Evaluator suite identifier required for promotion *(required)*.
- `-suite-commit <sha>` — Full commit ID of the evaluator suite required for promotion *(required)*.
- `-environment <env>` — Dependency profile required for promotion, such as `sim` *(required)*.
- `-artifact <name=sha256>` — Candidate binary artifact name and 64-character lowercase hex SHA-256 digest measured for promotion (repeatable, at least one *required*; duplicate artifact names are rejected).

---

## Reference Platform: `examples/orders` & `examples/orders-eval`

Surla includes an order-refund service and an isolated evaluator for checking payment effects, manager approval, worker recovery, and token budgets:

### [`examples/orders`](../examples/orders/) — The Candidate Platform
- **[`design/`](../examples/orders/design/)**: Declares the public `orders` HTTP service, internal `commerce` and `payments` services, the `goa-ai` `refund` agent (`Inject("tenant_id", "customer_id")`, `BoundedResult`, and `Confirmation`), and the C4 architecture model rendered to [`gen/structurizr/model.json`](../examples/orders/gen/structurizr/model.json).
- **[`AGENTS_QUICKSTART.md`](../examples/orders/AGENTS_QUICKSTART.md)**: Generated contextual map for coding agents, surfacing per-tool `BindTo` method links, server-injected fields, `BoundedResult` cursor guidance, `Confirmation` gates, and `gen/orders/agents/refund/specs/tool_schemas.json`.
- **[`internal/orders`](../examples/orders/internal/orders/)**: The refund rule (`StoreCreditLimitCents` in `refunds.go`): an order total up to `\$50.00`, including tax and shipping, is refunded as instant store credit; a larger total is refunded to the card after a manager approves.
- **[`internal/assistant`](../examples/orders/internal/assistant/)**: Runs the `refund` agent and resumes a suspended run after the manager's decision (`PrepareContinuation`, then `StartPrepared`).
- **[`internal/refundflow`](../examples/orders/internal/refundflow/)**: Durable Temporal workflow that executes each refund exactly once, with workflow IDs and payment idempotency keys derived from the tenant and order.
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

See [`examples/orders/README.md`](../examples/orders/README.md) for a step-by-step walkthrough including `surla verify` (full operator report vs. `-builder-view`).

---

## Project Layout

```text
.
├── design/
│   └── design.go                 # Surla Goa & Goa-AI MCP 1.0.0 DSL contract
├── cmd/
│   └── surla/
│       └── main.go               # CLI entrypoint (`surla serve`, `surla preview`, `surla diff`, `surla verify`, and `surla approve`)
├── docs/
│   ├── HOW_IT_WORKS.md           # The 5 checkpoints, Chorus routing & independent verification
│   ├── ARCHITECTURE_PLAYBOOK.md  # Golden Path rules & Goa Design suite overview
│   └── REFERENCE.md              # MCP tools/resources, CLI flags & repo layout
├── internal/
│   ├── controlplane/             # CP1–CP3 & CP5 handlers, bearer auth, embedded checkpoint viewer, CP1 schema validator
│   ├── designeval/               # Sandboxed Goa/Goa-AI/Model subprocess evaluator & C4 extractor
│   ├── diff/                     # Structural declaration, tool schema, and go.mod version diff engine
│   ├── gitcapture/               # Read-only dirty git working-tree snapshot capture
│   ├── goldenpath/               # Embedded GOLDEN_PATH.md playbook & CP1 architect system prompt
│   ├── procgroup/                # Process-group lifecycle management for sandboxed child processes
│   ├── swarm/                    # Deterministic Chorus role assignment & cross-family model router
│   └── verify/                   # Worktree verifier (`surla verify`), holdout redactor, and promotion check (`surla approve`)
├── ui/
│   └── checkpoint/               # React + OpenUI checkpoint viewer source; build.mjs writes checkpoint.html, decision_catalog.md, and generated types
├── verify/
│   └── candidate/                # macOS Seatbelt (`sandbox-exec`) build & runtime isolation jail
├── examples/
│   ├── orders/                   # Reference Goa + Goa-AI + Temporal + SQLite order-refund platform
│   └── orders-eval/              # Isolated evaluator harness, simulators, and 5 acceptance scenarios
├── gen/                          # Generated Goa HTTP, JSON-RPC, and MCP server/client packages
├── PROPOSAL.md                   # Full architecture & verification design specification
└── PRESS_RELEASE.md              # Working Backwards product vision
```
