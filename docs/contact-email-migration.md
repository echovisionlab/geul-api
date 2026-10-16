# Unified public contact email

`site_settings.contact_email` owns the public contact address. `legal_email`
remains separate. New content uses `contact.email`. Existing
`contact.supportEmail` and `contact.privacyEmail` atoms and reserved text tokens
remain accepted, including in historical snapshots and restores, and resolve to
the canonical address. An explicitly cleared canonical value stays empty; reads
never fall back to old database columns.

Management and manifest responses expose the new field and retain both old wire
fields as read aliases. Existing setting RPC aliases write the canonical column;
a batch with different canonical/alias values is rejected before mutation. New
Web and MCP authoring exposes only the canonical setting/key. Legal settings,
legacy column values and saved history are preserved.

## Schema and package prerequisites

Apply geul-schema `migrations/20261016-contact-email-v1.sql` before deploying
this API. Its transaction locks and backs up the settings table, adds the new
column and fills only NULL canonical values from trimmed nonempty support,
then privacy, then an empty string. Retry preserves existing canonical values,
including an explicit empty string, and does not overwrite prior backups.

Publish the new contracts, then common, and update API/Web/Collab consumers to
the same content catalog fingerprint before allowing new atoms to be authored.
Schema migration and package publication do not rewrite content. Existing atoms
and tokens already render the canonical value through aliases, so the normal
rolling release requires no content rewrite, writer shutdown or room drain.
The optional cleanup below is a separate maintenance operation. Do not remove
legacy keys or wire numbers after converting live content: saved history and
old clients can still contain them.

Pause contact-setting edits between schema backfill and completion of the API
replacement: an older API binary can still write the legacy columns without
updating the new canonical column. Reload old administration forms after the
new Web release; conflicting old support/privacy inputs are rejected rather
than choosing one silently. This narrow settings cutover does not require
stopping content writers or Collab rooms.

## Optional offline cleanup of live content

Physical conversion is optional; it is unnecessary for settings or rendering to
use the unified address. The contact conversion is an offline maintenance
operation. It does not publish
`content.updated` events or close resident rooms. Before apply, block all content
writes, stop API/MCP/automatic authoring writers, drain pending checkpoints, and
close/stop Collab rooms so no resident room can save an old snapshot. Keep these
writers stopped through verification. The command's `--writers-stopped` flag
acknowledges this operational prerequisite; it does not stop services itself.

Take and verify an external database backup first. Use the release's migration
binary with an explicitly configured `DATABASE_URL`; keep credentials in the
operator's existing secret mechanism. Inspect the dry-run counts:

```sh
site-variable-migrate --contact-email --dry-run
site-variable-migrate --contact-email --apply --writers-stopped
site-variable-migrate --contact-email --dry-run
```

Only `--apply` enables writes. The final dry run should report zero changes for
converted owners. Review any skipped owner or error before restarting writers.
Email validation failures roll back and report that owner; database errors or
concurrent-change conflicts stop the run. A partially completed run can be
retried: completed owners are unchanged on the next pass.

The contact mode rewrites exact legacy reserved tokens and rich-text atom keys
in live content Block/locale rows, menu rows/translations, email layout/template
translations and Page summaries. It preserves atom order, duplicate instances,
marks, links and legal keys. It does not convert literal addresses or branding,
backfill layout markers, rewrite executable source fields, delete snapshots or
modify legacy settings. Omit `--contact-email` only when intentionally running
the separate, broader legacy branding/layout migration.

Each changed owner runs in a transaction with before-image rows in
`site_variable_migration_backup`, keyed by reported `run_id`; subsequent runs
keep prior backups. Row locks and comparisons against the planned values reject
stale plans. Content documents receive a new revision; legal projections are
regenerated with their backup contract. Menu and email layout owners also use
their existing root/document or locale-graph locks. These database fences do
not replace the writer/room shutdown prerequisite.

After checking counts, backups, legal projections and revisions, restart the
matching API/Web/Collab releases and verify current contact/legal settings,
public content and email rendering. Unsaved browser journals must remain
available for explicit recovery; do not erase them as part of migration. Old
revisions must reload/reconcile through the normal conflict flow. Historical
restores can reintroduce accepted legacy keys; read aliases continue to work
and the conversion can be rerun during a later maintenance window.

For rollback, stop writers again and use the external backup or the identified
run's owner before-images under the same owner and revision discipline. Do not
bulk overwrite content rows while rooms are active or revert only a child row
without its document revision and derived legal projections. This command
provides backups, not an automatic rollback command.
