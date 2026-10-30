# Golden Path Architecture & the Goa Design Suite

**[← Back to README](../README.md)** · **[← How Scantle Works](HOW_IT_WORKS.md)** · **[CLI & MCP Reference →](REFERENCE.md)**

When coding agents build a platform from scratch without architectural guardrails, they default to fragile shortcuts: every service exposes ad-hoc HTTP JSON routes, every package calls LLM provider SDKs directly with no central rate limiting or cost tracking, `tenant_id` is passed inside LLM prompt arguments, and multi-step workflows run in fragile in-memory goroutines that lose state on deploy.

Scantle embeds a single-source-of-truth **Golden Path Playbook** ([`internal/goldenpath/GOLDEN_PATH.md`](../internal/goldenpath/GOLDEN_PATH.md)) and serves it both as an MCP resource (`scantle://golden-path`) and as the foundational system prompt for Checkpoint 1 architectural analysis.

---

## The 7 Golden Path Rules

### 1. Design Is the Single Source of Truth (`design/*.go` → `goa gen`)
Every service, method, payload, error contract, agent, toolset, MCP resource, and C4 container is declared in `design/*.go` first. Running `goa gen` deterministically generates transport codecs, OpenAPI specs, gRPC protobufs, workflow registrations, and `AGENTS_QUICKSTART.md` at **0 LLM tokens**. Files under `gen/` are never edited by hand.

### 2. Single HTTP/SSE/MCP Edge Gateway, Internal gRPC Services
- **One Edge Service**: Expose a single stateless HTTP/SSE/MCP edge service that terminates TLS, validates authentication tokens (`JWTAuth`, `OAuth2`, `APIKeyAuth`), enforces CORS, and streams real-time updates to clients.
- **Internal gRPC Topology**: All internal microservices, domain backends, and model gateways communicate over generated Goa `gRPC` clients and servers with deadline propagation and strict schema validation.

### 3. Central Stateless LLM Model Gateway
No domain service or agent worker holds raw LLM provider API keys or calls external provider endpoints directly:
- **Gateway Service**: Front all model calls with [`goa.design/goa-ai/features/model/gateway`](https://github.com/goadesign/goa-ai) (`RegisterGRPCServer` and `NewGRPCClient`) over `Complete`, `Stream`, and `CountTokens`.
- **Distributed Token Rate Limiting**: Wrap the gateway with `middleware.NewAdaptiveRateLimiter` backed by a [Pulse](https://github.com/goadesign/pulse) replicated map (`pulse/rmap`) so token-per-minute budgets and AIMD backpressure are coordinated across replicas.
- **Fail-Closed Usage Attribution**: Emit OpenTelemetry `gen_ai.*` spans via [Clue](https://github.com/goadesign/clue) and record `TokenUsage` on every call. If a provider omits usage metadata, mark `RetainedUsageIncomplete = true` so budget gates fail closed rather than counting missing usage as zero.

### 4. Security & Identity by Construction (`Inject`, `BindField`, `ServerData`)
Never trust an LLM to supply or preserve security principals, tenant IDs, or credentials:
- **Edge Extraction & `Inject`**: Extract caller identity from verified tokens at the HTTP/MCP edge, store it in a typed Go context, and declare `Inject("tenant_id", "user_id")` on tools so those fields are stripped from model-visible JSON schemas and populated server-side by `ToolInterceptor.Inject`.
- **Consumer-Side `BindField`**: When an agent consumes a broader internal service method, use `BindField` to narrow the model-facing input schema to only the arguments the LLM is allowed to choose.
- **Off-Wire `ServerData`**: Keep audit metadata, citations, and UI artifacts in `ServerData` so they never bloat model context windows (only `ServerData` explicitly bridged via `NativeImage()` is attached to the model turn).

### 5. Deterministic Service Toolsets (`BindTo`) vs. Agent-as-Tool (`Use(AgentToolset(...))`)
- **Service Toolsets (`BindTo`)**: Keep CRUD operations, state transitions, and external API calls as deterministic Goa service methods bound via `BindTo`.
- **Agent-as-Tool (`Use(AgentToolset(...))`)**: When a sub-task requires multi-step reasoning over noisy data, encapsulate it in a dedicated child `Agent` (`Export`) and consume it via `Use(AgentToolset(...))` so the child runs in its own isolated context window and returns a bounded summary to the parent.
- **Generated Agent Guardrails (`AGENTS_QUICKSTART.md`)**: `goa gen` automatically documents every tool's `BindTo` target, server-injected fields, `BoundedResult` pagination, `Confirmation` gates, and `specs/tool_schemas.json` (`input_binding`) in `AGENTS_QUICKSTART.md`.

### 6. Durable Temporal Workflows & Zero-Wait Human Suspension
- **Deterministic Replay**: Multi-step agent runs execute on the Goa-AI Temporal engine (`features/runlog/mongo` or SQLite `runstore`), keeping workflow definitions pure and isolating I/O inside registered activities.
- **Zero-Wait Human Approvals**: High-consequence tools declare design-time `Confirmation(...)` or pause the workflow (`RunStatusPaused`), releasing all worker threads until an authorized principal invokes `PrepareContinuation` and `Continue` on the public API.
- **Payload & Context Bounds**: Paginate collection tools with `BoundedResult`, configure `RunPolicy` (`MaxToolCalls`, `TimeBudget`, `Cache`, `History` `Compress`), and keep tool/activity payloads well under Temporal's 2 MiB event-history limit (`< 1 MiB` for complete `goa-ai` envelopes).

### 7. Customizing Generated Artifacts via Goa Plugins (`codegen.RegisterPluginLast`)
When a platform needs custom generated files or wants to tailor artifacts produced by `goa` or `goa-ai`, register a Goa plugin via `codegen.RegisterPluginLast("my-plugin", "gen", nil, generate)` (`goa.design/goa/v3/codegen`). Your `GenerateFunc` receives the evaluated `eval.Root` roots and the complete `[]*codegen.File` list, allowing you to inspect or transform generated sections cleanly during `goa gen` without post-processing scripts.

---

## How the Goa Design Suite Fits Together

| Project | Role in a Scantle Platform |
| :--- | :--- |
| **[Goa](https://github.com/goadesign/goa)** | Compiles `design/*.go` into HTTP, gRPC, OpenAPI, CLI, and validation packages (`goa gen`)—plus in-memory `codegen.SourceArtifacts` for sandboxed design evaluation—at **0 LLM tokens**. |
| **[Goa-AI](https://github.com/goadesign/goa-ai)** | Adds `Agent`, `Toolset`, `MCP`, durable Temporal workflows, `eval` suites, gRPC model gateways, and runtime token controls (`Inject`, `BindField`, `BoundedResult`, `ServerData`, `Cache`, `Compress`). |
| **[Pulse](https://github.com/goadesign/pulse)** | Streams live agent events (`pulse/streaming`) and coordinates distributed rate limits and replicated state maps (`pulse/rmap`) over Redis. |
| **[Clue](https://github.com/goadesign/clue)** | Emits OpenTelemetry `gen_ai.*` spans, structured logs, and health checks across services and model gateways. |
| **[Model](https://github.com/goadesign/model)** | Declares C4 architecture models (`SoftwareSystem`, `Container`, `Views`) alongside `design/*.go` and renders Structurizr JSON (`gen/structurizr/model.json`). |
| **[Chorus](https://github.com/goadesign/chorus)** | Coordinates multi-model coding and review agents (`@architect`, `@builder-<scope>`, `@eval-author`, `@reviewer`) in real time with `0`-token background waiting (`chorus watch`). |
