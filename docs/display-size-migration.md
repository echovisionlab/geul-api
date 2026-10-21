# Display size cutover

Contracts 0.14.0 renames the shared block field `previewWidth` to `displaySize`.
The value and its 10–100 range are unchanged. Stored JSON has no `previewWidth`
read alias, so the API, Collab and Web that read the new catalog must not start
until live content and version history are converted.

## What converts

`/app/display-size-migrate` reads:

- every `content_block.shared_data` that holds `previewWidth`
- every Page, Post and Work version snapshot that holds `previewWidth` or was
  stored under a reviewed earlier catalog

It renames the key and keeps the value. Snapshots move to the target catalog
fingerprint. Each converted row must validate against the target catalog in
restore mode. A second comparison, independent of the converter, proves that
only the renamed keys (and a snapshot's fingerprint) changed. Unreviewed
fingerprints, rows holding both names and localized rows holding the shared
field stop the run before any write.

Apply runs one transaction per document and per version row. It locks the row,
copies its before image into `display_size_migration_backup` under a new
`run_id`, and rewrites it only if the row still matches the plan. A document
whose Blocks change gets a new `revision`, so editors still holding the old
document reload.

## Run

Stop Web, Collab and API, drain in-flight writes and take a fresh external
database backup. Then run these from the reviewed API release image, with
`DATABASE_URL` from the deployment's Secret reference:

```sh
/app/display-size-migrate
/app/display-size-migrate --apply --writers-stopped
/app/display-size-migrate --verify
```

Only `--apply` writes. `--verify` is read-only and exits nonzero while any row
still needs conversion. Logs contain counts and the run ID, not content. Start
the new API, Collab and Web in that order after verification succeeds.

To roll back, restore the rows recorded under the run ID in
`display_size_migration_backup` and start the previous images.
