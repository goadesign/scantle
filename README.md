<p align="center">
  <a href="https://pkg.go.dev/github.com/goadesign/scantle"><img alt="Go Reference" src="https://pkg.go.dev/badge/github.com/goadesign/scantle.svg"></a>
  <a href="https://modelcontextprotocol.io"><img alt="MCP" src="https://img.shields.io/badge/MCP-2026--07--28-6E56CF"></a>
  <a href="https://github.com/goadesign/scantle/blob/main/LICENSE"><img alt="MIT License" src="https://img.shields.io/badge/license-MIT-blue.svg"></a>
</p>

# Scantle

**An agentic software factory that turns coding assistants into a disciplined engineering team.**

Today’s coding agents are fast solo sprinters: ask one to build a feature, and it will happily write the API, the business logic, and the tests in one big loop. That works for prototypes, and falls apart in production—because a single agent guesses ambiguous requirements, dumps thousands of lines of brittle glue code into your repo, and writes tests that rubber-stamp its own bugs.

**Scantle** connects to the AI assistants you already use (**Claude Code, Codex, Gemini, Cursor**) and orchestrates them like a real engineering organization. You describe what you need, settle the edge cases up front, and let Scantle coordinate specialized agents—an **Architect**, **Builders**, an independent **Test Author**, and a **Reviewer**—to ship services that are crash-tested, security-checked, and proven in an isolated sandbox before you ever see a merge button.

```text
You: "Add a refund assistant to our orders service. Refunds up to $50 get
      instant store credit; anything larger waits for a manager."

Scantle: Before I spin up the builders, two quick edge cases:
  1. Does "$50" mean item subtotal, or total including tax and shipping?
  2. What happens at exactly $50.00 — instant credit or manager approval?

You: "Total including tax and shipping, and $50.00 is instant."

Scantle: Locked 5 acceptance checks and assigned the team:
  • Architect (Claude)    → Compiles the API & workflow contract
  • Builder (Claude)      → Implements the refund workflow & state store
  • Test Author (Gemini)  → Writes the isolated acceptance & crash suite
  • Reviewer (Codex)      → Runs adversarial security & code review
```

---

## Why Engineers Use Scantle

### 1. The agent that writes the code never grades its own homework
When the same AI writes both the code and the tests, it subconsciously cheats—tailoring tests to its own implementation or weakening an assertion when a test fails. Scantle enforces a strict separation of powers:
- **Cross-model verification:** When you have multiple models configured, Scantle assigns test authoring and code review to **different model families** from the builder (for example, Claude builds while Gemini writes the evaluation suite and Codex reviews), eliminating single-model blind spots.
- **OS-sandboxed evaluation with hidden holdouts:** Candidate code is compiled and tested inside an isolated OS sandbox that the builder agent cannot read or edit, including **unseen holdout scenarios** whose details are hidden from the builder so it can never "teach to the test."

### 2. Zero AI-generated glue code—only contracts and domain logic
Most AI coding tools flood your repository with hand-rolled HTTP handlers, JSON serializers, retry loops, and OpenAPI boilerplate that your team has to maintain forever. Scantle is **contract-first**:
- The Architect agent defines your API, agent tools, and security rules in a compact, strongly typed design file (`design/*.go`).
- A deterministic compiler ([Goa](https://github.com/goadesign/goa) and [Goa-AI](https://github.com/goadesign/goa-ai)) generates all HTTP/gRPC transports, schemas, client SDKs, and validation at **0 LLM tokens**—so builders only write clean domain and workflow logic.

### 3. Production safety, crash recovery, and cost control built in
Agents working in Scantle follow an embedded **Golden Path** for distributed systems instead of inventing fragile shortcuts:
- **Prompt-injection-proof identity:** Sensitive fields like `tenant_id` and `customer_id` are injected server-side from verified auth tokens and stripped from LLM tool schemas, making it impossible for a model to access another customer's data.
- **Real `kill -9` crash proof:** Scantle doesn't just run unit tests. Its verifier starts your service, kills the worker process (`kill -9`) right after a payment call, restarts the API while a manager approval is paused, and proves that durable workflows recover with **zero double-refunds**.
- **Hard token & cost budgets:** Every model call routes through a central gateway that measures token usage and fails the build if a change silently bloats your LLM bill.

### 4. Poke the live system yourself instead of reading a 2,000-line diff
Before you approve a merge, Scantle boots the candidate build in an interactive sandbox backed by stateful dependency simulators:
- **Switch roles in one click:** Test a `\$54.00` refund as a **Customer**, then switch to **Approving Manager** to approve or decline the paused request through the real API.
- **Iterate in 0 seconds:** Tweak an agent's prompt wording with instant hot-reload (while Scantle blocks any attempt to sneak hard business rules into soft prompts), or turn any live chat turn into a permanent regression test with one click.

> **Why the name "Scantle"?** A *scantle* is a slater’s gauge rod, notched once so every slate on a roof is measured against the same hard edge. In Scantle, your compiled contract is that gauge rod—one specification every builder works to, and an independent verifier measures against.

---

## Try It in 60 Seconds

This repository includes Scantle along with a complete reference order-refund platform ([`examples/orders`](examples/orders/)) and its isolated verifier ([`examples/orders-eval`](examples/orders-eval/)).

```bash
# 1. Run the isolated verifier against the reference orders platform
#    Proves instant credit, manager approval across API restart, kill -9 worker
#    crash recovery, cross-customer holdout refusal, and token-budget enforcement.
#    (Requires Go 1.26+, macOS, and `brew install temporal`)
(cd examples/orders-eval && go test -v -race ./acceptance)

# 2. Start the Scantle MCP & HTTP control plane for your coding agents
export SCANTLE_AUTH_TOKEN="dev-secret-token"
go run ./cmd/scantle serve -addr 127.0.0.1:8090 -principal operator@example.com
```

---

## Dive Deeper

- **[How Scantle Works (`docs/HOW_IT_WORKS.md`)](docs/HOW_IT_WORKS.md)** — See how a change moves through the 5 checkpoints (`CP1`–`CP5`), cross-model swarm routing, the live sandbox, and OS-isolated verification.
- **[Golden Path Architecture (`docs/ARCHITECTURE_PLAYBOOK.md`)](docs/ARCHITECTURE_PLAYBOOK.md)** — Learn the distributed-systems architecture Scantle builds and how the open-source Goa ecosystem powers it under the hood.
- **[CLI, MCP & Repo Reference (`docs/REFERENCE.md`)](docs/REFERENCE.md)** — Connect Claude, Codex, Gemini, or Cursor via `.mcp.json`, browse the MCP tools and CLI flags, and explore the repository layout.
- **[Architecture Proposal (`PROPOSAL.md`)](PROPOSAL.md)** & **[Product Vision (`PRESS_RELEASE.md`)](PRESS_RELEASE.md)** — Read the full system design, threat model, and Working Backwards press release.

## License

Scantle is released under the [MIT License](LICENSE).
