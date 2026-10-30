<p align="center">
  <a href="https://pkg.go.dev/github.com/goadesign/scantle"><img alt="Go Reference" src="https://pkg.go.dev/badge/github.com/goadesign/scantle.svg"></a>
  <a href="https://modelcontextprotocol.io"><img alt="MCP" src="https://img.shields.io/badge/MCP-2026--07--28-6E56CF"></a>
  <a href="https://github.com/goadesign/scantle/blob/main/LICENSE"><img alt="MIT License" src="https://img.shields.io/badge/license-MIT-blue.svg"></a>
</p>

# Scantle

**An agentic software factory that builds, verifies, and evolves production services at a fraction of the token cost.**

Solo coding agents—and most multi-agent "team" wrappers—still ask LLMs to hand-write every line of HTTP routing, auth middleware, workflow plumbing, and tests from scratch. That works on day one and collapses by feature three: the codebase drifts into an inconsistent maze that burns more tokens on every edit, and the agent writing the code writes tests that rubber-stamp its own bugs.

**Scantle** pairs mixed-model agent teams (combining frontier reasoners and fast coding models across **Claude, Codex, and Gemini**) with a deterministic compiler stack. Because [Goa](https://github.com/goadesign/goa) generates transports, schemas, and validation at **0 LLM tokens**, and [Goa-AI](https://github.com/goadesign/goa-ai) provides built-in durable workflows, tool guardrails, and isolated evaluation, your agents only write compact contracts and pure domain logic—verified in an OS sandbox before you merge.

```text
You: "Add a refund assistant to our orders service. Refunds up to $50 get
      instant store credit; anything larger waits for a manager."

Scantle: Before I spin up the builders, two quick edge cases:
  1. Does "$50" mean item subtotal, or total including tax and shipping?
  2. What happens at exactly $50.00 — instant credit or manager approval?

You: "Total including tax and shipping, and $50.00 is instant."

Scantle: Locked 5 acceptance checks, compiled design/orders.go (0 tokens),
         and assigned the swarm across model families & classes:
  • Architect (Claude Opus)     → API, auth & workflow contract
  • Builder (Claude Sonnet)     → Refund workflow & state store
  • Test Author (Gemini Pro)    → Isolated acceptance & crash suite
  • Reviewer (Codex)            → Adversarial security & code review
```

---

## Why Engineers Use Scantle

### 1. 0-token code generation—agents write the contract, the compiler writes the plumbing
Why pay an LLM to hallucinate 3,000 lines of HTTP handlers, gRPC stubs, OpenAPI specs, MCP schemas, and JSON validators? In Scantle, the Architect agent expresses your API, agent tools, and security rules in a compact, strongly typed design (`design/*.go`). Goa deterministically compiles ~80% of the service at **0 LLM tokens** and **0 latency**—eliminating entire classes of glue-code bugs while slashing both build time and token spend.

### 2. A built-in Golden Path that stays cheap and effortless to evolve
Most AI-built codebases rot after a few weeks because every prompt invents a new folder structure, a new way to call LLMs, or a new auth pattern—forcing future agents to burn tens of thousands of tokens just reverse-engineering the repo. Scantle enforces an embedded **Golden Path architecture**:
- **Predictable layout & generated agent maps:** Every service follows the same clean structure and ships an auto-generated `AGENTS_QUICKSTART.md` map, so any coding agent can jump in months later and extend the platform with minimal context tokens.
- **Production patterns by default:** One HTTP/MCP edge gateway, internal gRPC services, a central LLM gateway with rate limiting and cost tracking, durable Temporal workflows, and server-injected `tenant_id`/`user_id` fields that the LLM can never spoof.

### 3. Mixed-model teams where the builder never grades its own homework
When configured with provider profiles (`-swarm-profiles`) and explicit builder path scopes, Scantle routes each role to the right model family and class—frontier reasoning models for architecture and security review, fast coding models for scoped implementation, and a **disjoint model family** for test authoring. Final verification (`internal/verify`) runs inside an **OS sandbox** (`verify/candidate`) the builder cannot touch:
- **Unseen holdout tests:** Hidden scenarios verify security and edge cases while [`Report.ForBuilders()`](internal/verify/report.go) strips holdout names, inputs, and logs from the builder's view, making "teaching to the test" impossible.
- **Real `kill -9` crash & budget proof:** The included [`examples/orders-eval`](examples/orders-eval/) reference suite kills the Temporal worker mid-transaction (`kill -9`), restarts the API during paused approvals, and fails the build when seeded payload bloat blows past the scenario's runtime LLM token budget (`go test -race -tags e2e`).

### 4. Fast draft overrides and live role-switched testing
Instead of reading a 2,000-line diff blindly, Scantle separates fast prompt tuning from compiled business rules and ships stateful dependency simulators in [`examples/orders-eval`](examples/orders-eval/):
- **Test across real auth boundaries:** In the reference platform, trigger a `\$54.00` refund as a **Customer**, verify that customer credentials cannot approve it (`403 Forbidden`), and switch to **Approving Manager** to approve or decline the paused workflow through the real HTTP API.
- **Iterate on prompts in 0 seconds:** When a host binds `Options.TargetUpdater`, `draft_override` (`POST /sandbox/draft`) hot-reloads prompt wording on the target's `prompt.Store` in **0 seconds**—while Scantle refuses any attempt to override compiled Go business rules (`target_kind: "compiled_rule"`, `requires_rebuild: true`; standalone `scantle serve` binds no `TargetUpdater`, so `prompt_template` overrides are rejected unless embedded). *(The `ui://scantle/checkpoint` MCP Apps resource currently serves the baseline HTML host shell; interactive role-switcher, replay, and OpenUI controls in the checkpoint web view are described in [`docs/HOW_IT_WORKS.md`](docs/HOW_IT_WORKS.md#checkpoint-3-cp3-fast-draft-overrides--live-sandbox-walkthrough).)*

> **Why the name "Scantle"?** A *scantle* is a slater’s gauge rod, notched once so every slate on a roof is measured against the same hard edge. In Scantle, `design/*.go` is that gauge rod—one compiled contract every builder works to, and an independent verifier measures against.

---

## Try It in 60 Seconds

Start the Scantle control plane, connect your coding assistant, and watch it evolve the included [`examples/orders`](examples/orders/) platform:

```bash
# 1. Start the Scantle MCP & HTTP control plane with a CP1 model provider
export SCANTLE_AUTH_TOKEN="dev-secret-token"
export ANTHROPIC_API_KEY="sk-ant-..."
go run ./cmd/scantle serve \
  -addr 127.0.0.1:8090 \
  -principal operator@example.com \
  -model-provider anthropic \
  -model-name claude-opus-4-6
```

Point **Claude Code, Codex, Gemini, or Cursor** at `http://127.0.0.1:8090/mcp` (see [`.mcp.json` setup](docs/REFERENCE.md#quickstart--mcp-configuration) and [`-swarm-profiles` configuration](docs/REFERENCE.md#configure-the-proposed-swarm-roster)) and prompt your agent:

> *"Using Scantle, propose a change to `examples/orders` so VIP customers get instant store-credit refunds up to \$150, then preview the compiled C4 architecture and Chorus swarm plan for builder scope `orders` (`internal/**`)."*

Your agent will create the Change Card (`propose_change`, calling `confirm_intent` only if open ambiguity questions yield `status: "needs_confirmation"`), compile the candidate Goa contract at **0 LLM tokens** (`preview_architecture`), and return the updated C4 topology, tool guardrails, and Chorus build roster (pass `-swarm-profiles <path.json>` on `scantle serve` to populate cross-family model assignments).

---

## Dive Deeper

- **[How Scantle Works (`docs/HOW_IT_WORKS.md`)](docs/HOW_IT_WORKS.md)** — Walk through the 5 checkpoints (`CP1`–`CP5`), cross-model Chorus routing, the live role-switching sandbox, and OS-isolated verification.
- **[Golden Path Architecture (`docs/ARCHITECTURE_PLAYBOOK.md`)](docs/ARCHITECTURE_PLAYBOOK.md)** — Explore the 7 Golden Path rules that keep Scantle platforms maintainable and see how the Goa Design suite powers them.
- **[CLI, MCP & Repo Reference (`docs/REFERENCE.md`)](docs/REFERENCE.md)** — Copy `.mcp.json` configuration, browse the MCP tools and CLI flags, and run the [`examples/orders`](examples/orders/) reference suite.
- **[Architecture Proposal (`PROPOSAL.md`)](PROPOSAL.md)** & **[Product Vision (`PRESS_RELEASE.md`)](PRESS_RELEASE.md)** — Read the full system design, threat model, and Working Backwards press release.

## License

Scantle is released under the [MIT License](LICENSE).
