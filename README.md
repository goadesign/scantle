<p align="center">
  <a href="https://pkg.go.dev/github.com/goadesign/scantle"><img alt="Go Reference" src="https://pkg.go.dev/badge/github.com/goadesign/scantle.svg"></a>
  <a href="https://modelcontextprotocol.io"><img alt="MCP 2026-07-28" src="https://img.shields.io/badge/MCP-2026--07--28-6E56CF"></a>
  <a href="https://github.com/goadesign/scantle/blob/main/LICENSE"><img alt="MIT License" src="https://img.shields.io/badge/license-MIT-blue.svg"></a>
</p>

# Scantle: Contract-First Agentic Software Factory over MCP

**Turn coding agents into a disciplined engineering team—compiled Goa contracts up front, multi-model Chorus construction in the middle, and an OS-isolated verifier the builder can never game.**

Coding agents are great at generating code and terrible at grading their own homework. Ask a single agent to build an order-refund service and write its own tests, and it will happily guess ambiguous domain rules, weaken a tool schema, mock out the crash path, leave `tenant_id` in the LLM prompt, or triple your token bill—and still hand you a green `"Tests Passed"` badge.

**Scantle** is an open-source Model Context Protocol (`MCP 2026-07-28`) control plane and isolated verification harness for building production [Goa](https://github.com/goadesign/goa) and [Goa-AI](https://github.com/goadesign/goa-ai) platforms. Connect **Claude Code, Codex / ChatGPT, Gemini, or Cursor** to `scantle`: it locks down concrete input/outcome examples and compiled `design/*.go` contracts *before* build tokens are spent, routes implementation and adversarial review across distinct model families over [Chorus](https://github.com/goadesign/chorus), and gates merge approval on an OS-sandboxed evaluator with unseen holdout scenarios, `kill -9` crash-recovery checks, and strict token budgets.

> **Why "Scantle"?** A *scantle* is a slater’s gauge rod, notched once so every slate on a roof is measured against the same hard edge. In Scantle, `design/*.go` is that gauge rod—one compiled contract that every builder works to and an independent verifier measures against.

**[Why Scantle](#why-scantle)** · **[See It in Action](#see-it-in-action)** · **[Quickstart](#quickstart)** · **[How the 5 Checkpoints Work](#how-the-5-checkpoints-work)** · **[Reference Platform](#reference-platform-examplesorders--examplesorders-eval)** · **[MCP Tools & Resources](#mcp-tools--resources)** · **[CLI Reference](#cli-reference)** · **[Project Layout](#project-layout)**

---

## Why Scantle

When teams move from a weekend agent demo to a production system that moves money, mutates infrastructure, or spans multiple services, five failure modes appear immediately:

| Failure Mode in Ad-Hoc Agent Coding | What Breaks in Production | How Scantle Solves It |
| :--- | :--- | :--- |
| **1. Ambiguous prompts become silent code assumptions** | You ask for *"instant refunds under \$50"*; the agent guesses item subtotal instead of total with tax & shipping, or `< \$50` instead of `<= \$50.00`. | **Checkpoint 1 (`propose_change` → `confirm_intent`)**: Surfaces concrete input/outcome examples and domain questions up front, locking a SHA-256 `request_revision` before any builder starts. |
| **2. Hand-rolled glue code & prompt-only security** | Agents put `tenant_id` and `customer_id` in LLM tool arguments (inviting prompt-injection cross-account access) or return unbounded lists that blow up context windows. | **Checkpoint 2 (`preview_architecture` & `scantle diff`)**: Evaluates `design/*.go` inside an isolated subprocess using `goa`'s real compiler, verifying server-side `Inject`/`BindField`, `BoundedResult`, `Confirmation`, C4 topology, and breaking contract diffs at **0 LLM tokens**. |
| **3. Single-model blind spots & sunk-cost bias** | The same model that wrote a subtle race condition or auth bug reviews its own diff and writes unit tests tailored to its own implementation. | **Cross-Family Chorus Swarm (`internal/swarm`)**: Deterministically assigns `@architect`, `@builder-<scope>`, `@eval-author`, and `@reviewer`, routing evaluation and zero-sunk-cost review to model families disjoint from the builders whenever multiple families are authenticated. |
| **4. Reward hacking ("teaching to the test")** | A builder agent edits the test assertion, weakens `design/*.go`, or reaches out over the network when a test fails. | **OS-Sandboxed Verifier (`verify/candidate` & `internal/verify`)**: Checks out exact commit SHAs, compiles and runs the candidate inside a macOS `sandbox-exec` jail against evaluator-owned contracts and stateful simulators, and redacts unseen **holdout scenarios** from builder reports. |
| **5. Silent token & cost regressions** | A two-line pagination change triples prompt tokens per run, or a custom model wrapper drops token usage reporting so cost checks pass as `0`. | **Gateway-Enforced Token & Cost Gate**: Every candidate model call traverses an evaluator-hosted `goa-ai` gRPC model gateway that enforces per-scenario token/cost budgets and fails closed on unreported usage (`RetainedUsageIncomplete`). |

---

## See It in Action

Scantle coordinates humans, architects, scoped builders, and an isolated verifier across five checkpoints (`CP1`–`CP5`). Here is how a change flows from a one-sentence request to a cryptographically verified merge:

<table>
<thead>
<tr>
<th width="33%" align="left">📐 1. Architect &amp; Control Plane (<code>CP1</code> → <code>CP2</code>)<br/><sub>Intent Lock, Compiled Contract &amp; Swarm Plan</sub></th>
<th width="34%" align="left">🛠️ 2. Scoped Builder Swarm (<code>CP2</code> → <code>CP3</code>)<br/><sub>Golden Path Implementation &amp; 0s Draft Mode</sub></th>
<th width="33%" align="left">🛡️ 3. Isolated Evaluator &amp; Gate (<code>CP4</code> → <code>CP5</code>)<br/><sub>Sandboxed Verification, Crash &amp; Budget Proof</sub></th>
</tr>
</thead>
<tbody>
<tr>
<td valign="top">

**Lock Intent (`CP1`)**<br/>
**You:** *"Refunds up to \$50.00 get instant store credit; larger ones wait for a manager."*

- Calls `propose_change` → surfaces 2 ambiguities (*subtotal vs. total with tax/shipping?*; *at \$50.00 exact?*).
- Calls `confirm_intent` → freezes `request_revision` (`sha256:9c4e...`) and concrete examples.

</td>
<td valign="top">

**Read Playbook & Guardrails**<br/>
- Reads `scantle://golden-path` (`GOLDEN_PATH.md`) and generated `AGENTS_QUICKSTART.md`.
- `goa gen` compiles HTTP/gRPC transports, OpenAPI specs, and `specs/tool_schemas.json` (`input_binding`) at **0 LLM tokens**.
- Sees per-tool guardrails directly in `AGENTS_QUICKSTART.md`: `tenant_id` and `customer_id` are server-injected; `request_refund` requires confirmation.

</td>
<td valign="top">

**Compile & Run in OS Sandbox (`CP4`)**<br/>
- `verify.Run` checks out exact `BaseCommit`, `CandidateCommit`, and `EvalCommit` SHAs.
- Compiles and boots `orders` and `orders-worker` inside a macOS `sandbox-exec` profile: no outside network, writes restricted to the sandbox state dir.
- Routes all candidate LLM calls through the evaluator's gRPC model gateway.

</td>
</tr>
<tr>
<td valign="top">

**Preview Evaluated Architecture (`CP2`)**<br/>
- Calls `preview_architecture` with `design/design.go` and explicit builder scopes (`orders`).
- Compiles the design in a sandboxed subprocess and returns:
  - **C4 Topology**: `orders` API, `refund` agent, Temporal worker, SQLite run store, external commerce/payment systems.
  - **Guardrails**: `Inject("tenant_id", "customer_id")`, `BoundedResult` on `list_orders`, `Confirmation` on `request_refund`.
  - **Chorus Swarm**: `@architect` (Claude), `@builder-orders` (Claude), `@eval-author` (Gemini, `cross_family_eval=true`), `@reviewer` (Codex, `cross_family_review=true`).

</td>
<td valign="top">

**Implement & Poke Live in `CP3`**<br/>
- `@builder-orders` implements the Temporal workflow (`internal/refundflow`) and SQLite run store (`internal/runstore`) within its owned path globs.
- In `CP3`, you switch roles between **Customer** and **Approving Manager** against the live candidate API and hot-reload prompt wording in **0 seconds** via `draft_override` (`prompt.Store`)—while Scantle rejects any attempt to override compiled Go business rules via prompts.

</td>
<td valign="top">

**Prove Resilience & Approve (`CP5`)**<br/>
- Runs 5 acceptance scenarios against stateful commerce/payment simulators:
  1. `\$38.50` instant store credit (`1` write)
  2. `\$82.00` paused for manager approval across an **API restart**
  3. Worker **`kill -9` crash** right after payment call → **exactly `1` refund** on Temporal replay
  4. Manager decline → `0` payment writes
  5. **Holdout**: cross-customer refund refusal (redacted from builder view)
- Enforces token/cost budget and verifies cryptographic report provenance in `verify.Approve`.

</td>
</tr>
</tbody>
</table>

---

## Quickstart

### Prerequisites

- **Go 1.26+** and the **Goa CLI** (`go install goa.design/goa/v3/cmd/goa@latest`)
- **macOS** (`sandbox-exec` is used by `verify/candidate` to isolate candidate builds and processes; non-macOS hosts fail closed with `ErrIsolationUnavailable`)
- **Temporal CLI** (`brew install temporal`) for running the end-to-end durable workflow crash and replay verification suites

### 1. Start the Scantle MCP & HTTP Control Plane

```bash
# Build and start the Scantle control plane on 127.0.0.1:8090
export SCANTLE_AUTH_TOKEN="dev-secret-token"
go run ./cmd/scantle serve -addr 127.0.0.1:8090 -principal operator@example.com
```

Register Scantle in your coding agent's `.mcp.json` (Claude Code, Codex, Gemini, Cursor):

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

### 2. Compare Two Goa Design Modules Structurally (`scantle diff`)

Unlike text diffs or hand-rolled AST heuristics, `scantle diff` runs Goa and Goa-AI's real source generator in an isolated sandbox for each module at its own `go.mod` versions and compares the resulting declarations and `specs/tool_schemas.json` contracts:

```bash
go run ./cmd/scantle diff \
  --old-dir ./examples/orders \
  --new-dir ./examples/orders
```

### 3. Run the Reference Platform & Sandboxed Verification Suite

This repository includes a complete, production-grade reference platform ([`examples/orders`](examples/orders/)) and its independent evaluator ([`examples/orders-eval`](examples/orders-eval/)). Run the full unit, race, and sandboxed end-to-end verification suites:

```bash
# 1. Run Scantle control-plane, Golden Path, Chorus swarm planner, diff, and verifier tests
go test -race ./...

# 2. Run the reference orders platform tests (including the SQLite runstore contract suite)
(cd examples/orders && go test -race ./...)

# 3. Run the isolated evaluator acceptance suite (all 5 scenarios + token-budget regression test)
(cd examples/orders-eval && go test -race ./...)
```

---

## How the 5 Checkpoints Work

Scantle replaces blind faith in agent-generated diffs with an unbroken chain of evidence from **natural-language intent → compiled Goa contract → live sandbox → OS-isolated verification → cryptographic merge gate**.

### Checkpoint 1 (`CP1`): Confirm Intent & Concrete Examples Before Spending Build Tokens

When you call `propose_change`, Scantle analyzes your request against the current `design/*.go` using the embedded Golden Path system prompt (`goldenpath.ArchitectSystemPrompt`) and returns a structured **Change Card**:

```text
Scantle · Checkpoint 1 Change Card (change_id: req-refunds, status: needs_confirmation)
───────────────────────────────────────────────────────────────────────────────────────
Examples (each maps to an isolated evaluator check):
  ✓ ex-1  $35 item ($38.50 total)       → Instant store credit, no approval [deterministic]
  ✓ ex-2  $75 item ($82.00 total)       → Pauses run for manager approval   [deterministic]
  ✓ ex-3  Manager declines $75 refund   → Polite explanation, 0 writes      [model_judged]

Ambiguities to resolve before building:
  ? amb-1  Does "under $50" mean item subtotal or total including tax & shipping?
  ? amb-2  What happens at exactly $50.00 — instant credit or manager approval?
```

Calling `confirm_intent` with your answers freezes the Change Card and returns a canonical SHA-256 `request_revision` digest. Every downstream checkpoint (`CP2`–`CP5`) is cryptographically bound to that revision.

### Checkpoint 2 (`CP2`): Preview the Compiled Contract, C4 Topology & Chorus Swarm Plan

Calling `preview_architecture` stages the candidate `DesignSnapshot` in a sandboxed subprocess, evaluates its Goa, Goa-AI, and `goa.design/model` declarations, and returns:

1. **C4 Architecture Topology (`containers` & `edges`)**: Services, agents, toolsets, people, external systems, and directed protocol edges.
2. **Evaluated Goa-AI Runtime Policies (`agent_policies`)**: `MaxExecutionSeconds`, `MaxToolCalls`, `MaxRecoveryTurns`, `Compress` thresholds, and prompt `Cache` checkpoints (`AfterSystem`, `AfterTools`).
3. **Evaluated Tool Contracts (`tools` & `input_binding`)**: Exact model-visible JSON schemas, consumer `BindField` / lookup schemas from `specs/tool_schemas.json`, server-injected fields (`Inject`), `BoundedResult` pagination flags, and `Confirmation` gates.
4. **Chorus Swarm Roster & Cross-Family Routing (`swarm`)**: Deterministic role handles (`@architect`, `@builder-<scope>`, `@eval-author`, `@reviewer`), workspace access modes (`read_only_after_cp2`, `read_write_worktree`, `isolated_eval_workspace`, `read_only_review`), honest `cross_family_review` / `cross_family_eval` posture flags, and a ready-to-pin Chorus collaboration manifest (`swarm.FormatManifest`).

### Checkpoint 3 (`CP3`): Interactive Checkpoint UI, Role Switcher & `0s` Fast Draft Mode

Scantle serves an MCP Apps host at `ui://scantle/checkpoint` (`GET /ui/checkpoint`) and exposes `draft_override` (`POST /sandbox/draft`):

- **Role Switching Against Real Auth Boundaries**: Test your candidate service as a **Customer** (`403 Forbidden` if attempting to approve a paused refund) and switch to **Approving Manager** to invoke the authenticated continuation endpoint (`PrepareContinuation` / `Continue`).
- **Instant Prompt Iteration (`draft_override`)**: When `target_kind` is `"prompt_template"`, Scantle hot-reloads the template in the target platform's `prompt.Store` in **0 seconds** and returns the new `prompt_version` digest. When `target_kind` is `"compiled_rule"`, Scantle rejects the override (`requires_rebuild: true`) so hard business rules (such as the `\$50.00` threshold) can never be silently moved into soft prompt text.

### Checkpoints 4 & 5 (`CP4` & `CP5`): OS-Sandboxed Verification, Redacted Builder View & Merge Gate

`internal/verify` enforces strict separation between builders and evaluation:

```text
Scantle Verification Report · Candidate 3a506f6 (OS Sandbox: macOS sandbox-exec)
───────────────────────────────────────────────────────────────────────────────────────
Status: passed (5/5 scenarios passed · 1 holdout scenario verified)

Public & Regression Scenarios:
  ✓ store_credit_under_limit         1 write · 512 tokens ($0.0011 <= $0.0100 budget)
  ✓ manager_approval_survives_api    1 write after API restart + manager signal
  ✓ refund_survives_worker_crash     kill -9 after payment → 1 write on Temporal replay
  ✓ manager_decline_no_payment       0 writes · polite refusal

Holdout Audit Scenarios (redacted to pass/fail in builder view via RedactForBuilder):
  ✓ cross_customer_refund_refused    0 writes · cross-account access blocked
```

- **Holdout Redaction (`verify.RedactForBuilder`)**: Builders see only whether the holdout suite passed or failed—never the scenario IDs, prompts, assertions, or failure messages—preventing agents from overfitting to the test cases.
- **Cryptographic Merge Approval (`verify.Approve`)**: Recomputes the report digest, verifies that the base commit, candidate commit, evaluator commit, binary SHA-256 digests, and frozen prompt versions match the target merge request, and rejects stale, failed, or incomplete-usage reports.

---

## The Golden Path Playbook (`scantle://golden-path`)

Every architect and builder agent connected to Scantle has access to `scantle://golden-path` ([`internal/goldenpath/GOLDEN_PATH.md`](internal/goldenpath/GOLDEN_PATH.md))—a compiled-contract-first playbook distilling production distributed-systems patterns for Goa and Goa-AI:

1. **Design Is the Single Source of Truth**: Author `design/*.go` first, then run `goa gen`. Never hand-edit `gen/`.
2. **Single HTTP/SSE/MCP Edge, Internal gRPC Services**: Keep public auth, TLS, CORS, SSE streaming, and MCP JSON-RPC at one stateless edge gateway; use generated Goa gRPC clients and servers between internal services.
3. **Central Stateless Model Gateway**: Front LLM provider calls with `goa.design/goa-ai/features/model/gateway` (`RegisterGRPCServer` / `NewGRPCClient`), `middleware.NewAdaptiveRateLimiter` backed by Pulse (`pulse/rmap`), and explicit `RetainedUsageIncomplete` accounting.
4. **Identity & Authorization by Construction (`Inject` & `BindField`)**: Bind `tenant_id`, `user_id`, and session credentials server-side from verified tokens so they are omitted from model-visible JSON schemas (`ServerData` stays off the wire unless explicitly bridged as `NativeImage()`).
5. **Deterministic Service Toolsets (`BindTo`) vs. Agent-as-Tool (`Use(AgentToolset(...))`)**: Keep CRUD and state transitions in concrete Goa service methods; wrap multi-step reasoning in child agents that run in their own context window.
6. **Durable Workflows & Zero-Wait Human Suspension**: Run multi-step orchestrations on the Goa-AI Temporal engine with deterministic replay, `BoundedResult` pagination, and `RunStatusPaused` human approval checkpoints that hold zero worker threads while waiting.

---

## Reference Platform: `examples/orders` & `examples/orders-eval`

Scantle ships with a complete reference platform and evaluator so you can inspect every pattern in working Go code:

- **[`examples/orders`](examples/orders/)**:
  - [`design/design.go`](examples/orders/design/design.go) & [`design/model.go`](examples/orders/design/model.go): Goa HTTP service (`orders`), internal `commerce` and `payments` services, `goa-ai` `refund` agent with `Inject("tenant_id", "customer_id")`, `BoundedResult`, and `Confirmation`, plus the full C4 architecture model rendered to [`gen/structurizr/model.json`](examples/orders/gen/structurizr/model.json).
  - [`AGENTS_QUICKSTART.md`](examples/orders/AGENTS_QUICKSTART.md): Generated contextual map for coding agents, including per-tool `BindTo`, server-injected fields, `BoundedResult` cursor guidance, `Confirmation` gates, and `gen/orders/agents/refund/specs/tool_schemas.json`.
  - [`internal/refundflow`](examples/orders/internal/refundflow/): Durable Temporal workflow implementing instant store credit (`<= \$50.00`) and manager approval signals (`> \$50.00`) with idempotent payment keys.
  - [`internal/runstore`](examples/orders/internal/runstore/): Transactional SQLite implementation of `goa-ai`'s durable `storage.Store`.
- **[`examples/orders-eval`](examples/orders-eval/)**:
  - [`acceptance/acceptance_test.go`](examples/orders-eval/acceptance/acceptance_test.go): Runs the 5 sandboxed acceptance scenarios plus `TestBloatedOrderPageFailsTokenBudget`, which proves that raising the candidate's order page size from `10` to `50` still passes all functional checks but fails the gateway token/cost budget gate.

---

## MCP Tools & Resources

All tools and resources are served over MCP JSON-RPC at `POST /mcp` and over HTTP REST on the same listen address (authenticated via `Authorization: Bearer <token>`):

### MCP Tools

| Tool | HTTP Route | Purpose |
| :--- | :--- | :--- |
| `propose_change` | `POST /changes/propose` | Analyze a feature request against `design_source` at `CP1`, returning concrete `examples`, `ambiguities`, `preserved_behaviors`, and a canonical `request_revision` digest. |
| `confirm_intent` | `POST /changes/confirm` | Lock the operator's ambiguity `answers` and confirmed `examples` at `CP1`, transitioning the Change Card to `confirmed`. |
| `preview_architecture` | `POST /changes/preview` | Evaluate a candidate `DesignSnapshot` in a sandboxed subprocess at `CP2`, returning C4 `containers` and `edges`, `agent_policies`, `tools` (`input_binding`), and the proposed Chorus `swarm` plan. |
| `draft_override` | `POST /sandbox/draft` | Hot-reload a `prompt_template` in `0s` on the bound `CP3` sandbox target or reject a `compiled_rule` override that requires a full Go rebuild. |
| `diff` | `POST /diff` | Compare `old` and `new` `DesignSnapshot` modules by evaluating their generated Goa/Goa-AI artifacts and `go.mod` directives. |

### MCP Resources

| Resource URI | HTTP Route | MIME Type | Purpose |
| :--- | :--- | :--- | :--- |
| `scantle://golden-path` | `GET /golden-path` | `text/markdown` | Embedded Golden Path architecture playbook (`internal/goldenpath/GOLDEN_PATH.md`) for Goa and Goa-AI platforms. |
| `ui://scantle/checkpoint` | `GET /ui/checkpoint` | `text/html;profile=mcp-app` | Interactive MCP Apps HTML host for `CP1`–`CP3` checkpoints and sandboxed `openui.v1` action dispatch. |

---

## CLI Reference

| Command | Flags | Description |
| :--- | :--- | :--- |
| `scantle serve` *(default)* | `-addr 127.0.0.1:8090`<br/>`-principal <id>` *(required)*<br/>`-auth-token <token>` *(or `$SCANTLE_AUTH_TOKEN`, required)* | Start the Scantle HTTP and MCP JSON-RPC control-plane server protected by bearer-token authentication. |
| `scantle diff` | `--old-dir <path>` *(required)*<br/>`--new-dir <path>` *(required)* | Read two Go design modules into symlink-safe snapshots, evaluate both inside the sandboxed Goa/Goa-AI compiler, and print the JSON `DiffResult` to `stdout`. |

---

## Why the Goa Design Suite Under the Hood

Scantle is built on six composable open-source projects from the [Goa Design](https://github.com/goadesign) ecosystem:

| Project | Role in Scantle |
| :--- | :--- |
| **[Goa](https://github.com/goadesign/goa)** | Compiles `design/*.go` deterministically into HTTP, gRPC, OpenAPI, CLI, and validation code (`goa gen`)—plus in-memory `codegen.SourceArtifacts` evaluation—at **0 LLM tokens**. |
| **[Goa-AI](https://github.com/goadesign/goa-ai)** | Adds `Agent`, `Toolset`, `MCP`, durable Temporal workflows, `eval` suites, gRPC model gateways, and runtime token controls (`Inject`, `BindField`, `BoundedResult`, `ServerData`, `Cache`, `Compress`). |
| **[Pulse](https://github.com/goadesign/pulse)** | Streams live agent events and coordinates distributed rate limits and replicated state maps (`pulse/rmap`, `pulse/streaming`) over Redis. |
| **[Clue](https://github.com/goadesign/clue)** | Emits OpenTelemetry `gen_ai.*` spans and structured observability across services and model gateways. |
| **[Model](https://github.com/goadesign/model)** | Declares C4 architecture models (`SoftwareSystem`, `Container`, `Views`) alongside `design/*.go` and renders Structurizr JSON (`gen/structurizr/model.json`). |
| **[Chorus](https://github.com/goadesign/chorus)** | Coordinates multi-model coding and review agents (`@architect`, `@builder-<scope>`, `@eval-author`, `@reviewer`) in real time with `0`-token background waiting (`chorus watch`). |

---

## Project Layout

```text
.
├── design/
│   └── design.go                 # Scantle Goa & Goa-AI MCP 1.0.0 DSL contract
├── cmd/
│   └── scantle/
│       └── main.go               # CLI entrypoint (`scantle serve` and `scantle diff`)
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

---

## Learn More & Contributing

- **[Architecture & Implementation Proposal (`PROPOSAL.md`)](PROPOSAL.md)** — Detailed design of the five checkpoints, sandbox isolation, Threat Model, and Goa-AI primitives.
- **[Working Backwards Press Release (`PRESS_RELEASE.md`)](PRESS_RELEASE.md)** — Product vision and operator workflow.
- **Contributing**: Edit `design/design.go` first when changing Scantle's HTTP or MCP contracts, run `goa gen github.com/goadesign/scantle/design`, and verify `go vet ./...` and `go test -race ./...` across the root and example modules before opening a pull request.

## License

Scantle is released under the [MIT License](LICENSE).
