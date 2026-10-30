# Golden Path

Rules for agents building services with Goa, Goa-AI, Model, Pulse and Clue. Every rule is a requirement unless marked "prefer". When a rule conflicts with a request, stop and say which rule and why.

## 0. Loop

1. Change `design/` first. Run `goa gen <module>/design`. Implement outside `gen/`. Build and test.
2. Never edit `gen/`. A compiler error in `gen/` means the design or the application code is wrong.
3. `goa example` writes scaffolding once and never updates it. Port design changes into existing implementations by hand.
4. Pin the `goa` command to the module version: `go run goa.design/goa/v3/cmd/goa gen …`.
5. After a signature change, regenerate, compile, and fix every caller the compiler reports. Then add behavior tests. Compiling proves types, not behavior.

## 1. Contract

- Describe every method once, independent of transport, then map it to `HTTP`, `GRPC` and `JSONRPC`. One method with one set of types serves every transport.
- Put structural validation in the design: `Required`, `MinLength`, `MaxLength`, `Pattern`, `Format`, `Minimum`, `Maximum`, `Enum`. Generated decoders enforce it. Never validate it again in handlers.
- Keep authorization and business rules in the service that owns them, not in the design.
- `Required` accepts `[]`. Add `MinLength(1)` to forbid an empty collection. Use `ArrayOfRequired` to forbid null elements.
- Never let nil and empty collections mean different operations. Model the operation explicitly.
- Mark a `OneOf` `Required` when the caller must choose a branch.
- Give gRPC fields stable, unique `Field(n, …)` numbers. Never renumber them.
- Give every list over unbounded data a bounded page: a default size, a maximum size, an opaque cursor, and a typed page result. Prefer cursors to offsets.
- Share a type with `Meta("struct:pkg:path", "<pkg>")`. Never write a second copy of a generated type.
- Adding a required result field breaks callers. Ship it with the code that always sets it. Never fix a broken contract by making the field optional.
- Removing an enum value, field or method breaks deployed callers. Migrate every caller first.
- URLs use plural resource nouns. The HTTP method names the action: `GET /orders`, never `/list-orders`.

## 2. Services

- Name systems and services after their domain capability. Use the same words in the design, code and docs.
- Edge services take external HTTP/SSE/MCP traffic, authenticate, start traces, and route. Domain services expose internal gRPC and own logic and state.
- Exactly one service owns each piece of durable state. Other services call its API and never read or write its tables, collections or keys.
- The service struct implements the generated interface. Pass every dependency into its constructor as an interface.
- Construct clients, connections and timeouts in `main` and pass them in. Runtime packages never take addresses or transport settings.
- Use one client package per downstream service, exposing only the methods you use, built on the generated client.
- Set a timeout on every outbound call. Pass `ctx` through every call.
- When work spans more than one service or transaction, finish it on the server in a durable workflow. Never depend on a client to retry for correctness.

## 3. Errors

- Declare every error a caller must tell apart with `Error("descriptive_name", …)` and a `Description`.
- Use `ErrorResult` unless the error needs extra fields. Mark errors with `Temporary()`, `Timeout()` and `Fault()`.
- Map each error per transport: `Response("name", StatusX)` in `HTTP`, `Response("name", CodeX)` in `GRPC`.
- Return errors with the generated `Make<Name>(err)` helpers. Map dependency errors to designed errors in the service layer.
- Wrap with `%w` and inspect with `errors.Is` or `errors.As`. Never discard an error with `_`.
- Never put internal detail in an error response. Record it on the trace.

## 4. Security

- Declare `Security(...)` at the API level and mark public methods explicitly with `NoSecurity()`.
- Implement the generated `Auther` methods: verify the credential, call `scheme.Validate(scopes)`, and return a context carrying the principal.
- Authenticate and authorize once, where a request or workflow enters the system. Never re-check inside internal loops.
- Deny by default. Check that the principal owns each resource it names.
- Use `JWTSecurity` only for JWTs. Use `BearerSecurity` for opaque tokens.
- Read secrets at runtime from a secret store and fail at startup if one is missing. Never log secrets, tokens or personal data.
- Never build a query from field names or operators the user controls.

## 5. Agents and tools

- Bind a tool to an existing method with `BindTo("Method")` or `BindTo("Service", "Method")`. Write an inline executor only to orchestrate several calls or for pure computation.
- Reuse service types in `Args` and `Return`. Describe when to use each tool, what it returns, and every field. Never maintain a second tool schema.
- Fill every value the model must not choose with `Inject(...)` from the authenticated request: tenant, user, session and run IDs, and credentials. The model never supplies identity or scope.
- In a hand-written executor, decode with the generated `Decode<Tool>(payload, meta, labels)`. The raw JSON codec leaves injected fields empty.
- Read run, session, turn and call IDs from `ToolCallMeta`, never from `context.Context`.
- Mark large, paged or truncated results with `BoundedResult()`. The service sets `Returned` and `Truncated` on every success, and sets `Total` only when it is known.
- Keep `Return` to domain fields. Never add `returned`, `total`, `truncated` or `next_cursor` to it.
- Prefer `BoundedResult(func() { ContinueWith("continue_x", "cursor"); NextCursor("next_cursor") })`. The runtime holds the cursor and the model calls the continuation with `{}`.
- Put data that is too large for the model, or meant for a UI, in `ServerData`, never in `Return`.
- Declare `Confirmation(...)` on tools that write, delete or command. Key the UI on the pending confirmation, never on a tool name.
- Return `planner.ToolFailure` with a `Recovery` (`RecoveryCorrectCall`, `RecoveryReplan` or `RecoveryFinish`). Never return a bare error or panic from a tool.
- Give every agent a `RunPolicy` with `DefaultCaps(MaxToolCalls(n), MaxRecoveryTurns(n))` and a `TimeBudget`, or a `Timing` block.
- Register every toolset and agent, then call `rt.Seal(ctx)` before the first run.
- Create the session in the host service before `Start`. Let the runtime own conversation history; keep no second copy.
- Mechanical decisions belong in code: identity, auth, IDs, cursors, pagination, valid transitions. Leave only semantic choices to the model.

## 6. Durability

- Agent turns and multi-step work run as durable workflows. Activities retry, so every side effect is idempotent. Key it by a stable ID such as the tool call ID or a domain key.
- Pass downstream calls an idempotency key derived from the domain, for example `refund/<tenant>/<order>`.
- Never retry a whole workflow. That repeats completed side effects.
- Store results larger than 1 MiB in application storage and return a typed reference.
- One host service owns the runtime store. Workers reach it through that service's typed API.
- Never set a Temporal `DataConverter`. The engine installs its own.
- Version workers with immutable build IDs and keep old versions running until their work drains.
- Ship an incompatible generated-contract change as one release: regenerate, drain old work, then deploy runtime, workers and callers together.

## 7. Events and streams

- Use events when several consumers need the same fact, or when the producer must not wait for consumers. Never use events for a step that must commit or fail atomically.
- Define event payloads as typed types in the design. Decide up front which events must never be lost.
- Delivery is at least once: make every handler idempotent, and acknowledge only after processing.
- Use Pulse readers when every consumer sees every event, sinks when each event goes to one consumer, and pools for throttled jobs that move off a dead worker. Bound streams with a maximum length.

## 8. Observability

- Instrument from the first commit: server middleware, client transports, span names, and consistent attributes.
- Create a span for each meaningful operation. Record state changes and results as span events, not log lines.
- On every failure path, including early returns, record the error and set the span's error status. Never set error status for an expected negative result.
- Log only startup, shutdown, background work outside a request, and unrecoverable errors. Never log in hot paths.
- Readiness checks critical dependencies with timeouts. Liveness checks only the process. Never expose pprof publicly.
- Alert on queries over truthful spans. Fix the alert's query; never filter or rewrite spans to quiet an alert.

## 9. Tests

- Unit tests cover pure logic: table-driven and deterministic, with no network or database.
- Every method has a component test for its success path and each designed error, with generated mocks and asserted calls.
- Integration tests use the generated clients, create and clean up their own state, and run in any order.
- Test conversions over every input variant, and assert the output, not just that no error occurred.
- Test agents with the in-memory engine. Fake models through `model.Provider`.
- Declare evals in the design with `Suite`, `Scenario` and `Input`. Check exact facts in code; send only claims that need judgment to a model judge. Never grade with regexes or keyword lists.
- Generation proves types only. Test authorization, idempotency and compatibility with deployed callers yourself.

## 10. Architecture model

- Keep the C4 model in the design as code, with `goa.design/model/dsl` (`Person`, `SoftwareSystem`, `Container`, `Uses`, `Views`). `goa gen` renders it.
- Each service and external dependency appears in the model with its real connections and technology. Update the model in the same change as the design.

## 11. Change and rollout

- Keep each change small and focused on one concern, and deployable. Update the design, code, model and docs together.
- Ship a deployable shell first, with the generated interface, stubbed results and instrumentation, and keep it deployable at every step.
- Pin every version; never use `latest`. Promote through each environment in order. Have a rollback before deploying.
- A change is done only when it runs in every target environment, not when it merges.

## Never

- Edit `gen/`.
- Duplicate a contract: no second schema, no copied generated type, no second validation pass.
- Let the model supply identity or scope.
- Read IDs from `context.Context`, or fall back silently to a default.
- Share durable state between services.
- Depend on client retries for correctness.
- Hide failures: no discarded errors, no unrecorded error paths, no filtered telemetry.
- Repeat auth checks inside an already authorized flow.

## Reference

`examples/orders` follows this path:
- `design/` holds the API, the toolset with `Inject`, `BindTo`, `BoundedResult`, `ContinueWith` and `Confirmation`, and the C4 model.
- `internal/refundflow` makes one idempotent refund per order.
- `internal/runstore` keeps the runtime store, owned by one service.
