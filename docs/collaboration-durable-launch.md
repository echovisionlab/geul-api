# Coordinated collaboration launch

This change is additive to contracts v0.5.0 and includes the pending collection-size
catalog change. Prepare one v0.6.0 release (or the next available minor version if
that version has been allocated). Do not publish an intermediate collection-only
or API-only contract build.

## Release order

1. Finish contracts lint, generation, catalog checks and package typechecks. Keep
   the generated Go and `packages/proto` TypeScript in the same release. Bump the
   root, `packages/proto`, `packages/event` and Release Please manifest to the
   same version, following the existing release workflow.
2. Release/tag contracts `v0.6.0`. That tag publishes the Go module
   `github.com/echovisionlab/geul-event-contracts@v0.6.0`. Verify the module proxy
   resolves it before building consumers.
3. The tag's existing Release workflow publishes
   `@echovisionlab/geul-proto@0.6.0`, then `@echovisionlab/geul-event@0.6.0`.
   Verify both npm versions are available and came from the same tag.
4. Upgrade geul-api's Go module and geul-web/editor-collab npm dependencies to
   these exact versions. Remove the temporary API collection and collaboration
   patches and the matching web collection patch together. Remove the wrapper's
   v0.5.0 guard/patch steps when switching to released contracts; until then run
   checks through `./scripts/go-with-collection-contract.sh` unchanged.
5. Before deploying the API, apply geul-schema's retry-safe
   `migrations/20261014-content-updated-outbox-v1.sql` using
   `psql -v ON_ERROR_STOP=1 -f ...`. Fresh installs already include the queue.
   No content backfill is needed. Existing Page incarnation and Post configuration
   revision migrations remain prerequisites, as documented in geul-schema.
6. Deploy the API, then the matching collab and web consumers in the coordinated
   launch window. Wire web SSR to the RPCs below before enabling first paint.
   Verify anonymous/unauthorized denial, archived Post read-only versus admin
   edit access, source/target parity with bootstrap, and committed events.
   Once web callers exist, record their actual entrypoints in the RPC inventory
   and restore its consumer audit status from `in_progress` to `complete`.

No release, tag, dependency version bump, deployment or migration execution is
performed by this implementation.

## Event delivery and recovery

Every content.updated producer uses the owning GORM transaction. The PostgreSQL
publisher sends a protobuf envelope to PGMQ `content.updated`, then calls
`pg_notify('content.updated', ...)` with the same envelope/message_id. Either
failure rolls back the domain mutation, queue row and notification. Rollback
produces no queue row or notification; commit produces one of each per event.
The queue retains events even when no listener is connected. Live NOTIFY remains
compatible with existing collab listeners.

Queue delivery/recovery is at least once, not end-to-end exactly once. Collab
is the only durable subscriber in the producer/AsyncAPI audit. It reads PGMQ
with a visibility timeout, locks and verifies the claimed row before handling,
and commits envelope-ID receipts with deletion after handling succeeds.
NOTIFY only wakes the queue drain; startup/reconnect and periodic polling recover
missed hints without invalidating unrelated rooms. Failed events remain queued.
The migration also creates the collab receipt table; rerun the retry-safe migration
if the earlier queue-only draft has already been applied. Retain receipts while
envelopes can be replayed, and monitor queue age/depth and handler errors.

A shared PGMQ queue is not broadcast. Cache/search or any future independent
subscriber needs its own queue via transactional producer fan-out or an outbox
relay; it must not compete with collab. No such consumers exist in this checkout,
so the API's single transactional enqueue/NOTIFY path remains unchanged.
Collab currently enforces a single room host with a session advisory lock before
WebSocket admission, retained through shutdown. Use a stop-before-start rollout;
an older NOTIFY-only binary does not participate in this lock. Additional room
hosts need durable fan-out to every host, not competing reads of local-room
invalidations. Lease/read-count fencing still protects concurrent queue readers.

All events carry an explicit source and the resulting `document_revision`.
Target edits retain the shared document revision and carry their new
`target_revision`; deletion carries `locale_exists=false` and no target token.
Metadata/media-only events carry the unchanged authoritative document revision.
Interactive AI signals commit with the mutation, even when its optional
post-commit interactive relay succeeds or fails. Collab should treat its same
revision as an echo and deduplicate the relay/event path.

## Coordinator recovery checks

The sandbox unit checks do not exercise sockets, Docker, or real PGMQ locks.
Before rollout, the coordinator must:

- Apply the migration twice to an existing database and verify fresh-schema
  parity, preserved queued messages, and SELECT/INSERT receipt plus queue
  read/delete permissions as the actual collab login role.
- Run `TestContentUpdatedSignalIsAtomicWithTransaction` in `internal/mq` with
  the repository's integration runner and reviewed schema. Verify commit and
  rollback have matching queue/NOTIFY envelope IDs.
- Use two database sessions: hold a consumer's message row lock beyond its
  30-second visibility timeout, verify another `pgmq.read` skips that row,
  and verify a stale `read_ct` cannot acknowledge a reclaimed delivery.
  Also deliver different transport rows sharing one envelope ID concurrently.
- Disconnect only LISTEN, mutate one resident entity/locale, then reconnect.
  Confirm queue drain and `external_update` (or typed `conflict`) reload only
  affected rooms; unrelated resident and loading rooms must stay usable.
  Suppress NOTIFY entirely and verify fallback polling still handles the event.
- Kill collab after handling but before deletion/receipt commit. Restart and
  verify redelivery, canonical bootstrap, no lost accepted edits, and eventual
  deletion. Repeat with a transient flush failure and failed acknowledgement.
- Start a second collab host and verify rejection before WebSocket admission.
  Verify the owner lock lasts through graceful room shutdown and owner-session
  loss triggers fatal shutdown. Stop the old NOTIFY-only binary before rollout.

## Editor first paint

- `api.manage.v1.PostService/GetPostEditorDocument`
  accepts `GetPostEditorDocumentRequest { post_id, locale }`.
- `api.manage.v1.PageService/GetPageEditorDocument`
  accepts `GetPageEditorDocumentRequest { page_id, locale }`.

Both responses have `document`, `document_revision`, `locale`, `locale_exists`,
`target_revision` (optional), `source_locale`, `block_media`, `editable`, `title`
and `summary`. Page also has `document_layout`. Post's `document` is
`api.content.v1.LocalizedRichTextDocument`; Page's is
`api.content.v1.LocalizedPageDocument`, preserving its mixed Page block tree.
TS clients use `getPostEditorDocument({ postId, locale })` and
`getPageEditorDocument({ pageId, locale })`; response field names are camelCase.

These session-authenticated reads call the same authorized materialization used
by `LoadPostBlockDocument` / `LoadPageBlockDocument`. Post requires edit, or
view_archived for archived read-only access; archived editability additionally
requires edit_archived. Page requires edit. Authorization denial is NOT_FOUND.
The exact canonical locale is required; missing targets use source fallback
with `locale_exists=false`. Paint the returned document read-only until the
matching collab room is ready; do not seed Yjs from SSR data. This implementation
adds the API contract and handlers only, with no geul-web UI changes.

## Conflict and internal header contracts

`api.intra.v1.CollaborationConflictDetail` adds optional
`current_document_revision`, `current_target_revision`, and
`current_target_exists`. Tokens are captured from authoritative locked state.
A deleted target has `current_target_exists=false` and no target token.
Serialization aborts that cannot observe authoritative state are retryable
ABORTED errors, not fabricated revision conflicts.

`INTERNAL_SERVICE_HEADER_NAME` defaults to `X-Internal-Service`, matching collab.
It remains configurable and validated. Deploy collab with the same name if the
API override is used; collab's current hardcoded client requires the default.

## Coordinated cross-repository checklist

One launch ships every repository below together. Nothing here is executed by the
implementation; each step needs an explicit go-ahead.

1. **geul-event-contracts** — release the minor version above with: editor-document
   RPCs, `CollaborationConflictDetail` fields, content.updated queue constants and
   `ContentUpdatedEvent.origin` (field 12). Verify the Go module proxy and both npm
   packages resolve from the same tag.
2. **geul-common** — release with the `./collaboration/protocol` export (v3 envelope,
   `room.access_changed`, `retryable` protocol errors, body digest,
   `encodeRequestRejection`, `entity.changed.origin`). Bump the package version.
3. **Consumers** — bump `@echovisionlab/geul-proto`, `@echovisionlab/geul-event` and
   `@echovisionlab/geul-common` in geul-web and geul-editor-collab; bump the Go module
   in geul-api. Remove: collab's temporary `tsconfig.json` paths/rootDir and
   `vitest.config.ts` alias; web's collection patch registration; the API wrapper's
   v0.5.0 guard and patches. Collab's default `tsc` fails on `ContentUpdatedEvent.origin`
   until this step.
4. **geul-schema** — apply `migrations/20261014-content-updated-outbox-v1.sql` before
   the API (see step 5 above).
5. **Deploy** — API (adds `Geul-Expected-Member` and `Geul-Client-Origin` interceptors,
   policy content.updated), then collab (`Recreate`; downgrades editors to view in place
   via `room.access_changed`) and web together. Update image digests in deployment.
6. **Web clients after deploy** — unsaved-edit journals live in the IndexedDB databases
   `geul-block-room-journal` and `geul-document-room-journal` (records bound to the
   member); tab backups use the `geul:editor-save-recovery:v2:<member>:` session storage
   prefix. Earlier unscoped v1 tab backups are ignored.
7. **MinIO** — `minio/minio` on Docker Hub denies pulls, so the pinned
   `deployment/infrastructure/data/minio.yaml` image cannot be re-pulled on a new node.
   Preferred: copy the currently running image by digest into `registry.dsub.io` and pin
   that (same binary, no data migration). A different image (for example Chainguard)
   runs as a non-root user and needs a reviewed data-volume ownership change first.
8. **Bundle check** — run web `perf:bundle` against the released packages.
