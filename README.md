# Scantle

> **Design preview — the experience we’re building.** Read [PROPOSAL.md](PROPOSAL.md) for the architecture and verification design, and [PRESS_RELEASE.md](PRESS_RELEASE.md) for the Working Backwards product vision.

You own an AI agent that issues refunds, modifies orders, or changes production configuration. Right after the first demo works, the real questions start:
- *What happens if a worker crashes right after the payment call and retries?*
- *Who approved that \$75 refund—and could the model have faked it?*
- *Did our agent interpret "\$50 limit" as item subtotal or total with tax and shipping?*
- *Why did a two-line tool change triple our LLM token bill?*

**Scantle** is an open-source, MCP-native software factory built on the **Goa Design suite** (`goa`, `goa-ai`, `pulse`, `clue`, `model`, and `chorus`). Connect the AI assistant you already use—**ChatGPT, Claude, Codex, Gemini, or Cursor**—to the `scantle` MCP server to bootstrap, verify, and evolve production agentic platforms without drowning in AI-generated glue code. *(A **scantle** is a slater’s gauge rod, marked once so every slate is measured against it; in Scantle, `design/*.go` is that rod—one design for the contracts and generated plumbing that every builder works to, with independent checks proving the resulting behavior.)*

---

## Who Scantle Is For (and Not For)

**Scantle is built for:**
- **Engineers who own evolving agent services in production**—especially workflows that take real actions (or long-running cross-service assistants with strict authorization, cost, and audit requirements) and must stay maintainable for years.
- **Product and domain owners** who work in ChatGPT or Claude and want to confirm concrete examples and poke a live sandbox *before* code is merged, rather than discovering requirements bugs in production.
- **Go / Goa teams first, and polyglot teams next:** You can start with a single generated Go agent service calling your existing Python, TypeScript, or Java services over HTTP, gRPC, or MCP. `goa gen` writes the transport, schema, MCP, and workflow plumbing; your team owns a fast, stateless Go binary and its domain logic.

**Scantle is not built for:**
- **Disposable chat demos or one-off prompt experiments** with no long-term operational ownership—a notebook or lightweight SDK is faster.
- **Teams where nobody will operate or debug a Go service in production** (PMs never touch Go, and `goa gen` writes the boilerplate, but your engineering team still owns and debugs the Go service).
- **Teams looking for a "zero-human" factory**—confirming intent and approving production merges are deliberate human checkpoints.

---

## Planned Chat Experience: Five Checkpoints That Build Real Confidence

Scantle never asks you to trust a vague `"Tests Passed"` badge. It gives you an unbroken chain from **your words → concrete examples → the compiled contract → a live sandbox → independent verification evidence**. When your request is already clear, intent and contract approval combine into a single step.

### 1. Ask for a capability in your AI assistant
> **You:** *"Create an order-support service with a refund agent. Refunds under \$50 get instant store credit; larger ones wait for a manager."*

### 2. Confirm what Scantle understood (`CP1` — before build tokens are spent)
```text
Scantle · Here's What I Understood (Request rev #1)
──────────────────────────────────────────────────────────────────────────────
Examples (each maps to an isolated evaluator check):
  ✓ $35 item ($38.50 total)       → Instant store credit, no approval [deterministic]
  ✓ $75 item ($82.00 total)       → Waits for manager approval        [deterministic]
  ✓ Manager declines $75 refund   → Customer sees reason + support    [model-judged]

Open questions to settle before building:
  ?1  Does "under $50" mean item subtotal or total with tax & shipping?
      (e.g., $48 item + $6 shipping = $54)
  ?2  What happens at exactly $50.00 — instant credit or manager approval?
```
> **You:** *"Total including tax and shipping, and up to \$50.00 is instant."*

### 3. Review the contract & approve the build (`CP2`)
```text
Scantle · Contract & Build Plan (Request rev #2, Base commit 8f41c02)
──────────────────────────────────────────────────────────────────────────────
Scope:    1 service (orders), 1 agent (refund), 1 human approval step
Contract: orders.Refund (tenant_id & user_id injected by server; hidden from LLM)
          BoundedResult on orders.List; provider-supported prompt caching enabled
Swarm:    @architect (Claude) · @builder-orders (Claude) · @eval-author (Gemini) · @reviewer (Codex, cross-family)
Planned:  10 evaluator checks · Est. build ~4 min / ~$0.40 budget
Map:      [Open C4 Architecture Diagram]   Diff: [View design/orders.go]
Approval: Sign in as operator to start build → http://localhost:9400/approve/req-2
```
- **Golden Path Architecture Playbook (`scantle://golden-path`):** Scantle guides the architect and builder agents with a compiled-contract-first Golden Path playbook (`internal/goldenpath/GOLDEN_PATH.md`)—separating the public HTTP/SSE/MCP edge gateway from internal Goa gRPC services, fronting multi-service LLM calls with a central stateless gRPC inference gateway (`goa-ai/features/model/gateway` + `middleware.NewAdaptiveRateLimiter` + `pulse`), binding caller identity server-side via `Inject`/`BindField`, keeping service methods concrete (`BindTo`) while wrapping semantic multi-step reasoning in child agents (`Use(AgentToolset(...))`), and persisting zero-wait human suspension checkpoints.
- **Role-Based Chorus Swarm & Cross-Family Model Routing:** When multiple coding-agent model families are configured on the server, Scantle deterministically assigns `@architect`, `@builder-<scope>`, `@eval-author`, and `@reviewer` so evaluation authoring and zero-sunk-cost adversarial review run on model families disjoint from the builders whenever eligible capabilities and pinned scope choices permit (`cross_family_review=true`, `cross_family_eval=true`; reporting `false` honestly when single-family or mixed-builder pinning shares a family), coordinated in real time over a pinned **Chorus** manifest while enforcing evaluator independence through OS process and workspace isolation.

### 4. Try the real candidate yourself (`CP3` — Live Sandbox, Role Switcher & Fast Draft Mode)
While the change waits for your merge decision, Scantle boots the exact candidate build (`commit a1b2c3f`) against isolated, stateful dependency simulators (or a `--env=staging` profile):
```text
Scantle Sandbox · Candidate a1b2c3f · [ENV: SIMULATED PAYMENTS v1.2 — NO REAL MONEY MOVES]
Role: [Customer ▾] (Switch to [Approving Manager] to approve/decline paused refunds)
──────────────────────────────────────────────────────────────────────────────
One-click replay of your examples (runs a fresh isolated sandbox execution):
  [▶ Replay: $48 + $6 shipping ($54) → Waits for manager]
  [▶ Replay: $50.00 total            → Instant store credit]
  [▶ Replay: $75 declined by manager → Customer explanation]
Or type any prompt to poke the agent live...        [+ Make This an Example] [⚡ Draft Tweak]
```
- **Switch roles in one click (via the service's public API):** Test a `\$54` refund as the **Customer**, then switch to **Approving Manager** (using evaluator-issued `sim` principals or scoped `staging` principals) to call the service's authenticated approval endpoint (`PrepareContinuation` / `Continue`)—including verifying that a `Customer` principal cannot self-approve.
- **Preview your agent's UI live (`ToolUI` MCP Apps + optional OpenUI starter):** If your tools ship `ui://` MCP App UIs—including an optional, replaceable **OpenUI** ([`thesysdev/openui`](https://github.com/thesysdev/openui)) viewer starter with typed Goa data/action bindings—`CP3` renders them live in the sandbox right beside the raw `Result`/`ServerData` JSON inspector, with zero UI framework lock-in in `goa-ai` or `scantle` core.
- **Turn any live turn into a permanent test:** Spot a weird edge case while chatting? Click **`[+ Make This an Example]`**, confirm what *should* have happened, and Scantle's isolated evaluator adds it to your regression suite.
- **Iterate in seconds with Draft Mode (`⚡ Draft Tweak`):** Tweak prompt wording (0s reload via `prompt.Store`, bound by version reference at promotion) or a threshold (fast single-service `go build` + instant re-run of your `CP1` examples, badged `Draft Experiment`), then promote all your tweaks through one full `CP4`/`CP5` verification run.

### 5. See what was checked, by whom, and what was *not* tested (`CP4` & `CP5`)
```text
Scantle Evaluator Report · Candidate a1b2c3f (Independent of builder workspace)
──────────────────────────────────────────────────────────────────────────────
Required Checks: 10 required — 10 passed, 0 failed, 0 skipped, 0 not run, 0 errors

Your Examples (Request rev #2):
  ✓ $38.50 total → Instant credit (1 write)                        [trace ↗]
  ✓ $50.00 total → Instant credit (1 write)                        [trace ↗]
  ✓ $54.00 total ($48 + $6) → Paused for manager approval          [trace ↗]
  ✓ $75.00 declined → Polite refusal, 0 payment writes             [trace ↗]

Safety, Crash & Token Budget Checks:
  ✓ Worker killed (kill -9) right after payment call → 1 refund    [trace ↗]
  ✓ Duplicate request sent twice → 1 refund (idempotency verified) [trace ↗]
  ✓ In-flight manager approval survives service upgrade            [replay ↗]
  ✓ Unseen holdout audit scenarios → 2/2 passed
  ✓ Runtime cost: 3.1k tokens ($0.004) vs. 4.0k budget ( parent + child + summary )

Not Tested in This Run:
  • Live payment gateway API (verified against stateful simulator v1.2 only)
  • Concurrency / load above 10 req/s

Merge Gate (CP5):
  Sign in as authorized operator to merge commit a1b2c3f → http://localhost:9400/merge/a1b2c3f
```

---

## Why the Goa Design Suite Under the Hood

| Project | What It Does in Scantle |
| :--- | :--- |
| **[Goa](https://github.com/goadesign/goa)** | Compiles `design/*.go` deterministically into HTTP, gRPC, OpenAPI, and validation code (`goa gen`) at **0 LLM tokens**. |
| **[Goa-AI](https://github.com/goadesign/goa-ai)** | Adds `Agent`, `Toolset`, `MCP`, durable workflows, `eval` suites, and runtime token controls (`Inject`, `BoundedResult`, `ServerData`, `Cache`). |
| **[Pulse](https://github.com/goadesign/pulse)** | Streams live agent events and coordinates distributed tool routing and rate limits over Redis. |
| **[Clue](https://github.com/goadesign/clue)** | Emits OpenTelemetry `gen_ai.*` spans and generates typed dependency mocks (`cmg`) for stateful simulation. |
| **[Model](https://github.com/goadesign/model)** | Keeps C4 architecture diagrams (`SoftwareSystem`, `Container`) in code and verified against service names. |
| **[Chorus](https://github.com/goadesign/chorus)** | Coordinates coding and review agents (Claude, Codex, Gemini) in real time with `0`-token background waiting (`chorus watch`). |

## Run Locally with Zero Daemons, Deploy Anywhere

Every generated platform keeps domain and planner code behind narrow Go interfaces, so only `cmd/<service>/main.go` changes across environments:
1. **Tier 0 (Instant Laptop Binary):** Target runs as a single Go binary in memory with zero Docker containers, Redis, or Temporal required for the target service.
2. **Tier 1 (Local Durable Stack):** Supervised local Temporal + Redis + durable store backed by `.scantle/state/` on disk for real `kill -9` crash and replay verification.
3. **Tier 2 (Production Anywhere):** Stateless Linux containers connecting to your production Temporal, Redis, database, OTLP backend, and LLM provider on any cloud or on-prem Kubernetes.

---

## Learn More
- **[Architecture & Implementation Proposal (`PROPOSAL.md`)](PROPOSAL.md)**
- **[Working Backwards Press Release (`PRESS_RELEASE.md`)](PRESS_RELEASE.md)**
