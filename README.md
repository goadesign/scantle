<p align="center">
  <a href="https://pkg.go.dev/github.com/goadesign/scantle"><img alt="Go Reference" src="https://pkg.go.dev/badge/github.com/goadesign/scantle.svg"></a>
  <a href="https://modelcontextprotocol.io"><img alt="MCP 2026-07-28" src="https://img.shields.io/badge/MCP-2026--07--28-6E56CF"></a>
  <a href="https://github.com/goadesign/scantle/blob/main/LICENSE"><img alt="MIT License" src="https://img.shields.io/badge/license-MIT-blue.svg"></a>
</p>

# Scantle

**Ship AI-built agentic systems you can actually trust in production.**

Any coding agent can generate a demo in five minutes. The hard part starts the moment that agent service has to issue real refunds, mutate customer records, or coordinate across internal APIs:
- *What happens if a worker crashes right after charging or refunding a card?*
- *Could a prompt injection trick the agent into refunding another customer’s order?*
- *Did the agent interpret "under \$50" as item subtotal or total with tax and shipping?*
- *Why did a two-line change just triple our LLM token bill?*

When the same coding agent writes both the implementation and the tests, it grades its own homework—silently baking its misunderstandings into the test suite and handing you a green `"Tests Passed"` badge.

**Scantle** is an open-source Model Context Protocol (MCP) software factory built on the [Goa Design](https://github.com/goadesign) suite. Connect the coding agents you already use—**Claude Code, Codex / ChatGPT, Gemini, or Cursor**—to Scantle to turn natural-language requirements into **compiled contracts, resilient distributed workflows, and independently verified Go services**.

> **Why "Scantle"?** A *scantle* is a slater’s gauge rod, notched once so every slate on a roof is measured against the same hard edge. In Scantle, your compiled design is that gauge rod—one contract every builder works to, and an isolated verifier measures against.

**[When to Use Scantle](#when-to-use-scantle)** · **[What You Get](#what-you-get)** · **[A 60-Second Taste](#a-60-second-taste)** · **[Try It Locally](#try-it-locally)** · **[Documentation](#documentation)**

---

## When to Use Scantle

**Use Scantle when:**
- **Your agents take real actions with real consequences**—moving money, changing account state, or running long-lived workflows that must survive process restarts and deploys.
- **You want AI speed without drowning in AI-generated glue code**—Goa compiles the HTTP/gRPC transports, OpenAPI specs, MCP schemas, and validation deterministically at **0 LLM tokens**, leaving only clean domain and workflow logic.
- **You want product owners and engineers looking at the same truth**—confirming concrete examples, poking a live sandbox with role switching, and reading an honest verification report before merging.

**Skip Scantle when:**
- You are hacking on a **disposable prototype, read-only chat demo, or one-off prompt experiment**—a notebook or lightweight SDK is faster when nothing needs to survive production at 3 AM.
- Your team wants a **"zero-human" black box**—Scantle is built around deliberate, high-leverage human checkpoints for confirming intent and approving merges.

---

## What You Get

### 1. Ambiguities Caught Before You Spend Build Tokens
Instead of letting an agent silently guess edge cases in code, Scantle turns your request into a small table of **concrete input/outcome examples** and surfaces open domain questions right in your chat (*"Does under \$50 include tax and shipping? What happens at exactly \$50.00?"*). Once you confirm, those examples become locked acceptance checks.

### 2. Golden Path Architecture by Construction
Scantle guides your agents with an embedded **Golden Path Playbook** (`scantle://golden-path`) and compiled [Goa](https://github.com/goadesign/goa) / [Goa-AI](https://github.com/goadesign/goa-ai) contracts:
- **Zero-trust identity binding:** Caller identity (`tenant_id`, `user_id`) is injected by the server from verified auth tokens and stripped from LLM tool schemas—making cross-account prompt injection impossible by construction.
- **Central LLM governance:** Model calls route through a stateless gRPC model gateway with adaptive rate limiting, prompt caching, and strict token/cost accounting.
- **Durable execution:** Multi-step actions run on Temporal workflows with idempotent side effects and zero-wait human approval pauses that survive server and worker restarts.

### 3. Multi-Model Swarms Where No Agent Grades Its Own Homework
When you have multiple coding agents installed (such as Claude, Codex, and Gemini), Scantle coordinates them over [Chorus](https://github.com/goadesign/chorus). It assigns scoped builders to explicit file paths and routes **test authoring** and **adversarial code review** to **different model families** from the builder—eliminating single-model blind spots and sunk-cost bias.

### 4. An Isolated Verifier the Builder Cannot Game
Before a change can merge, Scantle compiles and boots the candidate service inside an **OS sandbox** (`sandbox-exec`) that the builder agent cannot read or influence. The verifier runs your confirmed examples alongside **unseen holdout scenarios**, kills workers mid-flight (`kill -9`) to prove crash recovery, and fails the build if a change blows past your LLM token budget.

---

## A 60-Second Taste

Here is how a real feature flows through Scantle—from a one-sentence prompt to a verified merge:

1. **You ask:** *"Create an order-support service with a refund agent. Refunds under \$50 get instant store credit; larger ones wait for a manager."*
2. **You confirm the edge cases (`CP1`):** Scantle asks whether `\$50` includes tax and shipping. You reply: *"Total including tax and shipping, and up to \$50.00 is instant."*
3. **You approve the architecture & swarm (`CP2`):** Scantle compiles `design/design.go`, shows you the C4 architecture map and security guardrails (`tenant_id` & `customer_id` hidden from the model; `request_refund` gated by manager confirmation), and assigns `@builder-orders` (Claude), `@eval-author` (Gemini), and `@reviewer` (Codex).
4. **You poke the live sandbox (`CP3`):** You test a `\$54.00` refund as a **Customer**, switch roles in one click to **Approving Manager** to approve it, and tweak the agent's tone in **0 seconds** using Fast Draft Mode.
5. **You merge with proof (`CP4` & `CP5`):** Scantle's isolated evaluator proves all 5 scenarios pass—including surviving a `kill -9` worker crash right after the payment call, blocking a cross-customer holdout attack, and staying under the token budget.

👉 **[Walk through all 5 checkpoints in detail →](docs/HOW_IT_WORKS.md)**

---

## Try It Locally

This repository includes Scantle’s control plane along with a complete reference platform ([`examples/orders`](examples/orders/)) and its isolated evaluator ([`examples/orders-eval`](examples/orders-eval/)).

```bash
# 1. Start the Scantle MCP & HTTP control plane
export SCANTLE_AUTH_TOKEN="dev-secret-token"
go run ./cmd/scantle serve -addr 127.0.0.1:8090 -principal operator@example.com

# 2. Run a structural contract diff between two Goa design modules
go run ./cmd/scantle diff --old-dir ./examples/orders --new-dir ./examples/orders

# 3. Run the isolated evaluator against the reference orders platform
#    (Requires macOS + `brew install temporal` for sandboxed crash & replay checks)
(cd examples/orders-eval && go test -v -race ./acceptance)
```

To connect **Claude Code, Codex, Gemini, or Cursor** to the running server, add `http://127.0.0.1:8090/mcp` (with header `Authorization: Bearer dev-secret-token`) to your `.mcp.json`—see the **[CLI & MCP Reference](docs/REFERENCE.md#quickstart--mcp-configuration)**.

---

## Documentation

Scantle’s documentation is organized so you can dive as deep as you need:

| Guide | What You’ll Learn |
| :--- | :--- |
| **[How Scantle Works (`docs/HOW_IT_WORKS.md`)](docs/HOW_IT_WORKS.md)** | The 5 checkpoints (`CP1`–`CP5`), cross-family Chorus swarm routing, the live role-switching sandbox, and how OS-isolated verification prevents reward hacking. |
| **[Golden Path Architecture (`docs/ARCHITECTURE_PLAYBOOK.md`)](docs/ARCHITECTURE_PLAYBOOK.md)** | The production architecture Scantle builds (`scantle://golden-path`): edge vs. gRPC services, central model gateways, server-injected auth, durable Temporal workflows, and the Goa Design suite. |
| **[CLI, MCP & Repo Reference (`docs/REFERENCE.md`)](docs/REFERENCE.md)** | `.mcp.json` setup, MCP tools and resources, `scantle` CLI flags, the `examples/orders` reference platform, and the repository layout. |
| **[Architecture Proposal (`PROPOSAL.md`)](PROPOSAL.md)** | Full system specification, threat model, and verification invariants. |
| **[Working Backwards Press Release (`PRESS_RELEASE.md`)](PRESS_RELEASE.md)** | The operator narrative and product vision behind Scantle. |

## License

Scantle is released under the [MIT License](LICENSE).
