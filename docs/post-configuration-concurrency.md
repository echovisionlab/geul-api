# Post configuration concurrency

The manage API exposes a Post configuration revision so two editors cannot
silently overwrite each other's settings.

## Client contract

Read `configuration_revision` from the manage `Post` returned by Create, Get,
or a Post list. Include that exact lowercase, hyphenated UUID in
`UpdatePostRequest.expected_configuration_revision` on every update, including
slug-only updates and requests that would otherwise be no-ops.

`manage.v1.Post.revision` is the content-document revision used for title and
body collaboration. It is a separate token and must not be sent as
`expected_configuration_revision`. Post discovery results expose
`configuration_revision` for the same settings update contract.

The API acquires the existing Post root update lock, reloads the Post, and
rechecks live edit authorization before validating and comparing the revision.
An absent revision returns `FAILED_PRECONDITION` with a reload instruction. A
malformed or non-canonical UUID returns `INVALID_ARGUMENT`. A well-formed but
stale revision returns `ABORTED` with a reload instruction. Reload the Post and
apply the intended change to the current settings before retrying.

`UpdatePostResponse.configuration_revision` is the value persisted by
PostgreSQL and returned after the update. A no-op returns the current revision
without writing an audit record or changing `updated_at`.

## Revision boundary

PostgreSQL assigns an initial UUID when a Post is created. A trigger rotates it
only when one of these configuration fields changes: `slug`,
`comments_enabled`, `map_place_id`, or `document_layout`. Returning to an earlier
setting value still receives a new UUID, so a client holding an old token
cannot pass after an ABA change.

Lifecycle changes, source title or body edits, translations, featured media,
and other Post writes preserve the configuration revision. Their own timestamps
and content revisions remain separate. The API does not create or predict the
configuration UUID; it reads the persisted value after writing.

Apply the corresponding `geul-schema` migration before deploying this API.
