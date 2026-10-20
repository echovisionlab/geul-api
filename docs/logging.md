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
- **Operation logs** carry the diagnosis: one line per failed operation, with the
  identifiers and the bounded detail an operator needs to find the object and the
  cause without the code.
- **Spans** carry bounded attributes only (`failure_reason`, `exception_type`,
  status class). When a check runs after the traced call, as response validation
  can, the operation log is the only record of its detail.

## Fields

- Pass causes with `failurecause.Attr(err)`. It writes a sanitized
  `failure_cause`; pass command-specific private values as its extra arguments.
- Name the object: entity type and ID, job or command ID, locale, operation.
  Failed jobs may be deleted, so the log line has to identify them.
- Validation failures log issue codes with a capped sample of identifiers and the
  total count, never the content. `core.IssuesLogAttr` (AI document operations)
  and the translation `issues=kind:count sample_ids=…` cause follow this shape.
- Values must be scalars. The shared handler drops every `slog.KindAny` value.
  The API normalizer keeps slices of scalars as a comma-joined string (50 items,
  1,024 characters) and scalar `fmt.Stringer` values as text; structs and maps
  are still dropped. Render anything else to a string or a count before logging.
- Never log document text, prompts, provider bodies, tokens or e-mail addresses.
  Converting a value does not bypass the key denylist.

## Levels

- Each request is recorded once by `request.completed`, with its status and
  cause: 4xx as Warn, 5xx as Error. Operation logs must not repeat expected
  client outcomes at Warn. Use `errs.OperationLogLevel(err)`: not found, invalid
  argument, unauthenticated and canceled are Debug; unknown, internal,
  unavailable, data loss and deadline exceeded are Error; the rest are Warn.
- Error means an operator should act. Cancellation during a normal shutdown is
  not an error (`telemetry.ShutdownLogLevel`). Telemetry flushes with its own
  bounded context because the run context is already canceled by then.
- Warn means the service handled something that should not happen, such as a
  trusted internal caller sending a malformed request ID.

## Request IDs

Request IDs are minted inside the cluster. The edge proxy removes a client's
`X-Request-Id`, and the API adopts a propagated ID only from a request that
passes the internal-service check. A trusted hop that forwards no ID, such as
Oathkeeper in front of `/mcp`, gets a new request context. A malformed
propagated ID is a Warn with the route and method of the caller.

## Finding a failure

- Loki: `{service_name="api"} | json | level="ERROR"`, then filter by `event`,
  `job_id`, `entity_id` or `request_id`.
- Tempo: open the `trace_id` from the log line.
- The interactive AI document relay logs `mutation_id`. The editor collaboration
  service logs the same ID, and its own log holds the relay error that reaches
  the API only as `internal: internal error`.
