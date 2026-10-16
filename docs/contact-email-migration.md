# Canonical public contact cutover

The runtime accepts exactly five site-variable keys: `site.title`, `site.url`,
`site.companyName`, `contact.legalEmail` and `contact.email`. `contact_email` is
the only public contact setting; legal contact remains independent. Removed
support/privacy fields, keys, lookup aliases and context fallbacks are absent.
Their old Protobuf names and numbers are reserved. Older clients must reload.

This is a coordinated maintenance cutover. Package publication and the column
migration alone do not convert stored content. Do not start canonical-only
consumers until the live-content/history verification gate below succeeds.

## Prerequisites and shutdown

Publish the strict contracts, then common; pin the real published versions and
lockfiles in API, Web and Collab. Build the reviewed API release image, which
contains both `/app/backend` and `/app/site-variable-migrate`. Use that immutable
image digest for the maintenance Kubernetes Job; do not build or run ad hoc Go
source against production.

Apply geul-schema `20261016-contact-email-v1.sql`. Its transaction locks and backs
up settings before adding `contact_email` and initializing only NULL values from
trimmed nonempty support, then privacy, then an empty string. Existing canonical
values, including an explicit empty string, and prior backups are preserved.
The schema Job's Ready condition covers the column only, not content conversion.

Block content and settings mutations, stop API/MCP/automatic authoring writers
and delivery schedulers/workers, drain in-flight writes/checkpoints, and close
Collab rooms. Keep writers stopped until conversion and verification finish.
Take and verify a fresh external database backup after the drain. The
`--writers-stopped` acknowledgement does not stop services itself.

Inventory active immutable delivery runs before shutdown when possible. The
migration preflight counts old reserved contact tokens in
`email_delivery_run.render_snapshot`, including nested translations and layout
HTML. Scheduled/sending or unknown-status runs with those tokens block the
entire conversion before any backup-table or content write. Resolve them through
the delivery owner's normal drain or cancel/recreate operation; inspect recipient
claims and outcomes before doing so. Never update sealed snapshots, disable their
triggers, reset recipient states or re-send delivered mail to bypass this gate.
Terminal sent/failed/cancelled/skipped archives remain byte-for-byte unchanged and
are reported as `archives_preserved`; runtime aliases are not restored for them.

Queued/running translation jobs are also audited for removed contact keys/tokens
or predecessor catalog identities in the immutable request manifest and XLIFF.
Drain or cancel affected requests through the translation owner before cutover;
recreate them from the canonical source after the new API starts. The converter
does not rewrite provider handles, request artifacts, digests or archive data.
Existing queued request recovery plans must be preserved externally.

Other entity summaries (Post, Work, Series, Program Event) and site metadata
descriptions are read-only audited. Any removed contact token there blocks the
cutover before writes, because those owners are outside the reviewed mutation
allowlist. Resolve a reported owner through its own normal authoring contract
and retry; do not broaden an operator SQL replacement to bypass the check.

## Immutable-image maintenance Job

Configure `DATABASE_URL` through the deployment's existing Secret reference and
inherit the API's real `SITE_ORIGIN`/`CDN_URL` ConfigMap values for email
validation, without putting credentials in arguments or logs. Override the image command in
the one-time Job and run these steps in order:

```sh
/app/site-variable-migrate --contact-email --dry-run
/app/site-variable-migrate --contact-email --apply --writers-stopped
/app/site-variable-migrate --contact-email --verify
```

Only `--apply` writes. `--verify` is read-only, cannot be combined with apply,
and exits nonzero if any conversion owner or active delivery blocker remains.
Successful verification reports the pinned `target_catalog`,
`metadata_rows_blocking=0`, `translation_jobs_blocking=0`,
`delivery_runs_blocking=0` and `owners=0 apply=false`; the preserved archive count
may be nonzero. Logs contain
counts and owner/run identifiers, not content dumps or contact values. Require
both the Job's successful exit and its verification output before resuming apps.

The allowlist converts live content Block/locale JSON, menu rows/translations,
email layout/template HTML and subject text, and Page summaries. Contact atom
keys and reserved tokens are rewritten to `contact.email`, preserving order,
distinct occurrences, marks, links, CSS and private layout markers. Email
conversion also recognizes the case and brace whitespace accepted by the
email renderer; ordinary public text and atom keys retain their exact key rules.
Executable source fields and unrelated branding/literal addresses are not rewritten by
contact mode. The separate broader branding command also generates canonical
contact keys; it is not part of this cutover.

Historical `page_version`, `post_version` and `work_version.content_snapshot`
rows are converted as well. The offline converter accepts only these reviewed
source identities; every other noncurrent fingerprint fails closed:

| Fingerprint                                                        | Evidence                                                             |
| ------------------------------------------------------------------ | -------------------------------------------------------------------- |
| `1b0b80933dd4e3a92f66d8f85d25c915d74410ee47bb6cf6ec3adffcfe964bc8` | Contracts initial catalog, commit `5a24205`                          |
| `e037d77d859734c7c716372434602edfe8872d521e8f0f9b9e28e12827635335` | Contracts Mermaid catalog, commit `68d7cd7`                          |
| `de190638c6014cc2ac80efce5f747b30d20cc024f3d0820c87ba6c1f1a56e16e` | Contracts embed catalog, commit `26fec00`                            |
| `2db3b07d50a139a670f68e3cd045063d20475a102acbee7852066b1929b7172b` | Contracts durable collaboration catalog, commit `cdae513`            |
| `7ee996e5a23685d3b981268c3dd6608d8f006f5b62dbec5fb35e1b4d0532cf49` | Contracts site-variable catalog, commit `cb2755f`                    |
| `ffc51f7192e406fe25fcb5383428be94e74a52db83c666b550ca38d4db88dc85` | Contracts unified contact catalog, commit `3004be1`                  |
| `5afc20ddc15f7c6099873f33dae6557c5f263e668f12e0babb0186f051a52977` | Observed stored identity; per-snapshot current-shape validation only |
| `0bff2608f4dfcb86193ffbb4ef9da9ad27a9c5ed9812e7dfe15a6711ef873235` | Observed stored identity; per-snapshot current-shape validation only |
| `17a1f44809c2a0c3acc168ae6ad756b00a7d980ccca546881fd3b43b3fbbc401` | Observed stored identity; per-snapshot current-shape validation only |

The last three identities have no recovered historical schema in the available
split-repository Git history. Their admission does not declare general support
for those old schemas. Each individual payload must decode without unknown
fields and pass the complete current generated restore validator for its exact
Page/Post/Work profile and source locale. There is no structural field repair,
default injection, field removal or runtime fallback. A separate recursive
before/after comparison permits only the catalog replacement and exact contact
key/token substitutions. It rejects changes to any other value, object key,
array order, identity, style or number. JSON numbers retain their precision.

The entire inventory is converted and validated before the first backup-table
or content write. Operators can validate a private JSON export of rows shaped
as `{table, id, snapshot}` without committing it or logging authored values:

```sh
GEUL_CONTACT_HISTORY_CORPUS=/private/path/history.json \
  go test ./internal/sitevariablemigrate -run '^TestContactPrivateHistoryCorpus$' -count=1 -v
```

The diagnostic logs row indexes, fingerprints and structural failure categories,
checks idempotence, and exits nonzero if any row cannot be converted. Its separate
target-shape probe grants no migration permission. A successful private-corpus
check does not replace the final immutable-image dry-run and verification while
writers are stopped. Snapshot IDs, version numbers, contributors and timestamps
remain unchanged. Canonical hashes are derived from the stored snapshot bytes;
there is no separate stored hash to leave stale. Runtime decoders never upgrade
old catalog identities or accept old keys.

Each changed owner has a transaction, locked rows, current before-images in
`site_variable_migration_backup` keyed by a fresh reported `run_id`, and compare
predicates that reject stale plans. Existing runs' backups are retained. Live
content documents advance their revision; legal projections are regenerated
with their own before-images. Menu and layout changes retain their existing
root/document or locale-graph locks. Version snapshot rows use their own
before-image and JSON comparison; they do not edit the current live document.
These database fences do not replace the required writer and room shutdown.

Email validation failures roll back and report the owner. Other failures stop
the run; earlier committed owners remain backed up. Keep apps stopped, resolve
the failure and retry. Completed owners are unchanged on retry. A zero exit from
apply alone is insufficient: the final `--verify` must also succeed, including
after any reported skipped owner.

## Resume and rollback

After backup/count/revision checks and a successful verification Job, start the
matching API and confirm readiness, then Collab and Web with the same strict
catalog fingerprint. Check contact/legal settings, representative live content,
version restore and email preview. Browsers must reload the new client. Preserve
unsaved browser journals for explicit recovery; old catalog journals must not be
silently replayed or deleted. Do not reopen writers on an older binary that can
write removed keys or old setting columns.

For rollback, stop writers again and restore the external database backup or a
reviewed migration run's complete owner before-images under the same locking and
revision discipline. Restore derived legal projections and document revisions
with their owner, not isolated child rows. Use a matching application version
for that restored catalog. The command records backups and does not provide an
automatic rollback operation. Immutable delivery archives are never rewritten.
