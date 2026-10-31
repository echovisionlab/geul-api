# Logging and tracing

Logs and traces leave the process through `internal/telemetry`. The normalizer
(`internal/telemetry/normalizing_handler.go`, wrapping the shared
`geul-telemetry` handler) normalizes keys, injects `request_id`, `trace_id` and
`span_id`, and drops keys on the shared denylist
(`geul-telemetry/policy/log-redaction.json`: `body`, `prompt`, `source_text`,
`member_id`, `provider_response_body` and others). Loki stores the JSON lines
under `service_name="api"`; Tempo stores the spans.

## What goes where

- **Typed System records** (`sharedtelemetry.New*Record`, `EmitSystem`) are a
  bounded wire contract, such as `translation.job.terminal` and
  `queue.delivery.failed`. Do not add diagnostics to them.
- **Failure records** carry the diagnosis: one line per failed operation, with
  the cause and the identifiers an operator needs to find the object without
  the code. The next section says which record that is.
- **Domain events** (`mail.*`, `queue.*`, `translation.provider.*` and other
  logs with an `event` attribute) describe something other than the failure
  that a caller receives, such as a provider callback or a skipped delivery.
  Dashboards select on their `domain`, `event` and `outcome`; never rename them.
- **Spans** carry bounded attributes only (`failure_reason`, `exception_type`,
  status class). When a check runs after the traced call, as response validation
  can, the failure record is the only record of its detail.

## Where a failure is recorded

A failure is logged once, by the record that ends the work it failed:

| Work | Record |
|---|---|
| Connect RPC | `request.completed` from `telemetry.AccessLogInterceptor` |
| Plain HTTP handler | `request.completed` from `telemetry.NewHTTPHandler` |
| MCP tool call | "MCP tool execution rejected" or "MCP tool execution failed" in `internal/mcp` |
| Queue message | "PGMQ handler failed" in the consumer, or "PostgreSQL signal handler failed" |
| Scheduler loop, reconciler, startup | the loop or component that handles the failure |

Code below these records returns the failure and does not log it. A Connect
handler returns its error; `errs.BoundaryInterceptor` classifies it, hides
server faults from the client, and hands the cause to the request record
through the `failurecause.Recorder` in the request context. A plain HTTP
handler records the cause itself before it writes the response:

```go
failurecause.RecorderFrom(r.Context()).Record(failurecause.Of(err))
```

`internal/architecture` fails the build on a deferred `errs.OperationLogLevel`
logger and on the old generic messages ("Operation failed", "content operation
failed", "RPC failed", "Media RPC failed", "Content document operation
rejected").

Log in place only when the code goes on: a degraded fallback that still
succeeds, a compensation or rollback failure beside the failure being
returned, a security event, or a result that the caller receives as success
(an AI document rejection returned as issues). Give that log a specific
message that names what happened and what the code did instead.

## Carrying the diagnosis on the error

The record that logs a failure renders the error with `failurecause.Message`
and reads the diagnosis attached anywhere in its chain:

- Cause: wrap with `%w` and context (`fmt.Errorf("lock identity state:
  %w", err)`). When the client must get a fixed answer, keep the cause behind
  it with `errs.WithCause(errs.NotFoundMsg("label not found"), err)`: the client
  sees the first error's code, message and details; `errors.Is`, `errors.As`
  and the record see the cause.
- Private values: `failurecause.WithPrivate(err, nickname)` attaches values
  the error text may contain and no log may keep. An error type can carry its
  own by implementing `failurecause.PrivateValuer`; `*auth.KratosHTTPError`
  does this for the upstream body. Request fields whose names mark them as
  secrets (token, password, email, code and the others in
  `errs.isPrivateField`) are redacted by the boundary without attaching them.
- Identifiers: `failurecause.WithAttrs(err, slog.String("page_id", id))`
  attaches bounded identifiers: entity type and ID, job or command ID, locale.
  The record adds them as attributes and never lets them replace one of its
  own fields. A handler whose every failure concerns one object defers it
  once:

  ```go
  defer failurecause.Annotate(&err, slog.String("page_id", req.Msg.GetPageId()))
  ```

  `failurecause.AnnotatePrivate` does the same for private values.
- Server faults behind a client answer: stored data the server cannot read,
  or a dependency failure deliberately answered as not found, still needs an
  operator. Mark the cause with `failurecause.ServerFault(err)`; the record
  then logs at Error whatever the status.
- Hidden context: an error whose text must stay fixed (because a client
  message or a lifecycle event uses it) implements
  `failurecause.OperationContext` to give the record its full text.

Private values and identifiers are found anywhere in the chain, through `%w`
wrapping and every branch of `errors.Join`.

A sink that logs a failure itself passes `failurecause.Args(err, private...)`,
which yields `failure_cause` and the identifier attributes.

## Fields

- The cause goes under `failure_cause` (`failurecause.Attr`, `Args` or a
  record). The normalizer reduces an `error` attribute to its type name, so
  never log a cause under `error`.
- Name the object: entity type and ID, job or command ID, locale. Failed jobs
  may be deleted, so the record has to identify them.
- `operation`, where a log names one, is snake_case `verb_object` for the
  domain operation (`reconcile_identity_states`, `load_share_link`), never a
  Go identifier. Attribute keys are snake_case.
- Validation failures log issue codes with a capped sample of identifiers and the
  total count, never the content. `core.IssuesLogAttr` (AI document operations)
  and the translation `issues=kind:count sample_ids=…` cause follow this shape.
- Values must be scalars. The shared handler drops every `slog.KindAny` value.
  The API normalizer keeps slices of scalars as a comma-joined string (50 items,
  1,024 characters) and scalar `fmt.Stringer` values as text; structs and maps
  are still dropped. Render anything else to a string or a count before logging.
- Never log document text, prompts, provider bodies, tokens or e-mail addresses.
  Converting a value does not bypass the key denylist.
- Log with the request or job context (`slog.*Context(ctx, ...)`) so the line
  has `request_id` and `trace_id`. A helper that has no context returns the
  problem to its caller instead of logging with `context.Background()`; only
  startup code without a request logs that way.

## Levels

- `request.completed` grades a failure by status: 4xx is Warn, 5xx is Error, and
  a cause marked `failurecause.ServerFault` is Error.
- A sink without a status uses `errs.OperationLogLevel(err)`: not found,
  invalid argument, unauthenticated and canceled are Debug; unknown, internal,
  unavailable, data loss, deadline exceeded and unimplemented are Error; the
  rest are Warn.
- Error means an operator should act. Cancellation during a normal shutdown is
  not an error (`telemetry.ShutdownLogLevel`). Telemetry flushes with its own
  bounded context because the run context is already canceled by then.
- Warn means the service handled something that should not happen, such as a
  trusted internal caller sending a malformed request ID. Client input is
  never Warn: a rejected request is already in its record.

## Request IDs

Request IDs are minted inside the cluster. The edge proxy removes a client's
`X-Request-Id`, and the API adopts a propagated ID only from a request that
passes the internal-service check. A trusted hop that forwards no ID, such as
Oathkeeper in front of `/mcp`, gets a new request context. A malformed
propagated ID is a Warn with the route and method of the caller.

## Finding a failure

- Loki: `{service_name="api"} | json | level="ERROR"`, then filter by `event`,
  `job_id`, `entity_id` or `request_id`. A failed request is
  `event="request.completed"` with `failure_cause` and the identifiers its
  error carried.
- Tempo: open the `trace_id` from the log line.
- The interactive AI document relay logs `mutation_id`. The editor collaboration
  service logs the same ID, and its own log holds the relay error that reaches
  the API only as `internal: internal error`.
