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
rows are converted as well. Only the four reviewed predecessor catalog
fingerprints are accepted by the offline converter; unknown fingerprints fail
closed. It updates the snapshot document to the reviewed strict catalog, then
validates the exact Page/Post/Work profile and source locale using generated
restore validation. Snapshot IDs, version numbers, contributors and timestamps
are preserved. Canonical hashes are derived from the stored snapshot bytes;
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
