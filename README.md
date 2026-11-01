# Geul API

Geul API is a Go service for content, media, identity, and MCP operations.

## Quick start

Requires Go 1.27.3, Node 24.22.1, pnpm 12.14.0, FFmpeg/FFprobe, ImageMagick,
and the runtime values described by
`internal/config.Config`. The trust boundary is configured explicitly with
`AUTH_HEADER_NAME` and `INTERNAL_SERVICE_HEADER_NAME`. The internal header
default is `X-Internal-Service`, matching editor-collab. Invalid header names
and a missing auth header fail startup closed. `SESSION_COOKIE_NAME` is the companion Identity/Ory
session-cookie contract.

### Private dependencies

Authenticate before downloading dependencies. Export `NODE_AUTH_TOKEN` from
your credential manager with read access to the private GitHub npm packages.
`media/og/.npmrc` reads the token from the environment:

```ini
registry=https://registry.npmjs.org/
@echovisionlab:registry=https://npm.pkg.github.com
//npm.pkg.github.com/:_authToken=${NODE_AUTH_TOKEN}
```

The scope mapping applies to every `@echovisionlab` package. This repo consumes
`geul-event`, `geul-proto`, and `geul-telemetry`; it does not depend on `ionian`
or `audio-transcoder`. Keep those two packages on public npmjs. If either is
added here, use an explicit, versioned npmjs tarball URL in `package.json`
(and the lockfile) to bypass the scope mapping; no republication is required
with that approach. `.npmrc` cannot route individual packages within a scope.

The API also consumes the private Go modules
`github.com/echovisionlab/geul-event-contracts` and
`github.com/echovisionlab/geul-telemetry`. Authenticate Git HTTPS with a
credential helper that can read these repositories, then set:

```sh
export GOPRIVATE='github.com/echovisionlab/*'
```

This bypasses the public Go proxy and checksum database for the private modules.
Keep tokens out of tracked files and Docker build arguments.

```sh
go mod download
make media-build
make test
make build
```

Build the production image locally:

```sh
docker build \
  --secret id=private_deps_token,env=NODE_AUTH_TOKEN \
  -t registry.rawsector.com/echovisionlab/geul-api:0.1.0 .
```

For Docker builds, this token must read both the private repositories and npm
packages. BuildKit mounts it only during dependency operations; the Go Git
configuration lives in tmpfs. The runtime image needs no GitHub token.

Release image pushes to `registry.rawsector.com` go through the Tailscale tailnet
to the in-cluster HAProxy on the Service node, bypassing Cloudflare Tunnel's
100 MB request-body limit. Set repository secrets `TS_OAUTH_CLIENT_ID` and
`TS_OAUTH_SECRET`, and repository variable `REGISTRY_INTERNAL_ADDRESS` to the
Service node's tailnet IP address. The workflow maps the registry hostname in
both the runner and BuildKit container; Buildx uses host networking for the
tailnet route. Keep `HARBOR_REGISTRY_USERNAME` and `HARBOR_REGISTRY_PASSWORD`
for registry authentication.

### Operator cutover

1. Create one organization Actions secret, `PRIVATE_DEPS_TOKEN`, available to
   `geul-api` and the other consuming repositories. Use a GitHub PAT (classic)
   with `repo` and `read:packages`, an account with access to the dependency
   repositories/packages, and organization SSO authorization where required
   ([GitHub npm authentication requirements](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-npm-registry)).
   Store the same value under the same name in organization Dependabot secrets
   and grant this repository access there too. Dependabot has a separate secret
   store; its PR workflows also need that secret.
2. Publish the dependency versions to **private** GitHub Packages first. This
   repo currently pins `@echovisionlab/geul-event@0.4.1`,
   `@echovisionlab/geul-proto@0.4.1`, and
   `@echovisionlab/geul-telemetry@0.1.4`. Preserve the original npmjs tarball
   bytes so the committed integrity hashes still match. If repacking changes
   the bytes, publish a new version and update this consumer deliberately.
   Ensure `geul-common` is migrated for consumers that need it as well.
3. Grant the token's account read access to each package, and link packages to
   their source repositories with private access/inheritance configured.
   Package visibility and access must be checked separately from repository
   visibility. In each publishing repo, use its own `GITHUB_TOKEN` with
   `permissions: packages: write`, GitHub Packages `publishConfig`, and no npmjs
   provenance/trusted-publishing flags. This repo publishes only a Harbor image;
   both bundled Node projects remain `private: true` and release-please remains
   a Go release.
4. Before enabling frozen installs against the new registry, resolve the three
   pinned packages against GitHub Packages on an authorized machine:

   ```sh
   pnpm --dir media/og update --lockfile-only --force \
     @echovisionlab/geul-event@0.4.1 \
     @echovisionlab/geul-proto@0.4.1 \
     @echovisionlab/geul-telemetry@0.1.4
   ```

   Review and commit the resulting lockfile: each of those entries must have
   the actual GitHub Packages `dist.tarball` URL and matching integrity.
   The current npmjs lock entries have only integrity hashes; a registry switch
   alone can make frozen installs construct invalid GitHub tarball URLs
   ([pnpm registry-switch issue](https://github.com/pnpm/pnpm/issues/7579)).
   Do not guess the URLs or remove integrity checks. This step requires the
   packages to exist and is a prerequisite to the cutover.

5. Retain `HARBOR_REGISTRY_USERNAME` and `HARBOR_REGISTRY_PASSWORD` with push
   access to the `echovisionlab` project at `registry.rawsector.com`. Developers
   and deployment hosts must run `docker login registry.rawsector.com` with
   pull credentials before pulling API or PostgreSQL images. Kubernetes needs
   a matching `imagePullSecret` on the consuming workloads. Existing image
   digests are unchanged; confirm they exist at this registry before cutover.
6. Merge the consumer configuration and refreshed lockfile after dependency
   publication, then switch the geul repositories to private. Verify CI and a
   release build with empty dependency caches on an authorized runner. Private
   fork PRs do not receive Actions secrets; use trusted same-repository branches
   for checks requiring private dependencies. Enable Actions to create release
   pull requests if the organization currently disallows it.

Collection blocks support the shared `itemSize` enum (`s`, `m`, `l`, default
`m`), released in contracts v0.6.0. See
[the coordinated launch steps](docs/collaboration-durable-launch.md).

Page source-room batches may mutate shared block structure and explicit source
locale values. Deleting a shared block removes its descendant blocks and their
locale overlays atomically, including target translations. Target locales in
the resulting `changed_locales` acknowledgement describe that cascade; only
explicit locale mutations in the request determine source-room write authority.

Page target CAS also binds `page_translation.incarnation_id`, assigned by
PostgreSQL for each locale row lifetime. Apply the schema upgrade documented in
`geul-schema` before deploying this API; a deleted/recreated locale cannot reuse
an old room's token even with identical timestamps. Internal target conflicts
return a typed `TARGET_REVISION_CHANGED` detail, and no-op metadata writes do
not report changed locales or advance their revision.

A provider translation job whose target has since become the Page source is
rejected before any source write. Jobs targeting a locale that remains a target
may still survive source-locale switching; promotion must not give old target
jobs authority to overwrite canonical source content.

## Included media services

CDN delivery, audio/video transcoding, waveform generation, mesh optimization,
and the Node OG worker ship with the API.

- `internal/mediadelivery` provides CDN routes, signed URLs, Range handling and imgproxy requests.
- `internal/transcoding` and `internal/assetprocessing` run the PGMQ consumers with their own database pools and S3 adapters.
- `media/og` contains the Node worker, dependencies, templates and fonts. The API starts it after binding HTTP, includes its health in readiness, and drains it before closing internal RPC.
- `media/asset-optimizer` contains the mesh script and its npm dependencies.

Image transformations use imgproxy. Compose runs it alongside the API;
Kubernetes runs it as a sidecar in the API Pod.

The API serves RPC on port 8000, private MCP on 8001, and the existing CDN/media
routes on 8002. OG health binds only to loopback on 3010. Existing CDN/media
hostnames and signed paths can keep pointing to the delivery listener.
Set `IMGPROXY_KEY` and `IMGPROXY_SALT` to the same hex secrets as imgproxy and
`CDN_IMGPROXY_URL` to its address (default `http://127.0.0.1:8080`).

### MCP menu discovery

`menu_list` returns authorized Menu UUIDs, management names, and source locales,
with bounded `limit`/`offset` pagination. `menu_locations_get` returns only the
four menu assignments from Site Settings: header, secondary, footer, and avatar
dropdown. Unassigned locations are `null`; menu names do not imply placement.
Both tools retain their owning services' existing permissions. The location
reader requires administrator Site Settings view permission and does not expose
other site settings.

Use the selected Menu UUID as `d` with `p=menu` in
`document_open`, `document_read`, `document_catalog`, and `document_apply`.
Read the source locale, current revisions, and stable menu item handles before
editing. These discovery tools do not create menus or change site assignments.

`CLOUDFLARE_CACHE_PURGE_ENABLED` defaults to `true`, requiring
`CLOUDFLARE_ZONE_ID` and `CLOUDFLARE_API_TOKEN` for public asset deletion.
Set it to `false` when CDN/media DNS records point directly to the origin
without Cloudflare proxy caching. Credentials are then optional, and cleanup
still deletes objects and finalizes the asset lifecycle without a Cloudflare
request. Canonical asset-prefix validation and delivery cache headers remain
in effect. Disable purging only after traffic no longer uses Cloudflare's cache.

For source execution after `make media-build`, also set:

```sh
export OG_WORKER_SCRIPT="$PWD/media/og/dist/index.js"
export GLTF_TRANSFORM_PATH="$PWD/media/asset-optimizer/scripts/asset-transform.mjs"
export PARTICLE_MESH_SCRIPT_PATH="$PWD/media/asset-optimizer/scripts/optimize-particle-mesh.mjs"
```

`MEDIA_AUDIO_WORKERS`, `MEDIA_VIDEO_WORKERS`, `MEDIA_WAVEFORM_WORKERS`, and
`OG_GENERATE_WORKERS` preserve production concurrency defaults (3, 3, 2, 12).
Keep the termination grace period above `OG_SHUTDOWN_TIMEOUT_MS` (120000 by
default) plus API cleanup time; Compose allows 140 seconds.

## MCP management outputs

`work_credit_group_update` and `work_credit_update` return the current resource
values after a successful update. Their output schemas omit `changed`: the owning
API does not report whether persistence changed, so an equal-value update must
not be counted as a write. Clients should use the output schemas returned by
`tools/list`; the MCP server implementation version is `15`.

Administrator document workflows include settings reads, typed content and
metadata edits, publication, withdrawal, and existing version and File controls.
`post_settings_get` returns the settings revision required by
`post_settings_update`; `page_settings_get` returns the Page layout and display
settings. Page layout edits use `document_metadata_update` with the current
source-document revision. `post_unpublish` moves a published Post, or an archived
Post managed by an administrator, directly to draft without republishing it.

Program Event management reuses its owning create, settings, publish, archive,
and delete APIs. Event types, series, and Labels have reference lists; existing
document tools edit the event body, source title, and locale summary. Event
archive retains the archived lifecycle rather than moving to draft.
The media tools read, upsert, remove, and reorder its native role-specific media
collection. Upserting an existing event/role/File edits its alt and caption;
omitting either value clears it. An unchanged reorder is a no-op.

Release tools cover settings, draft/publication, artwork, slug checks, and the
seven native relation setters. Read `release_relations_get` first and pass the
exact observed arrays back to the relevant setter. Setters return native
`success`; a fresh relation read is a separate operation. Track tools cover
creation, settings, credits, deletion, and complete release ordering. Track
publication follows its Release. `genre_list`, `style_list`, and `format_list`
resolve music references.

`file_upload` imports a ChatGPT attachment directly into the File library without
a Post or Page association. Its top-level `file` field uses OpenAI's
[`openai/fileParams` contract](https://developers.openai.com/plugins/reference#_meta-fields-on-tool-descriptor):
`download_url`, opaque ChatGPT `file_id`, and optional `file_name`/`mime_type`.
The server streams the download through the existing File authority, validates
the actual bytes, and returns a File UUID. The ChatGPT file ID is never a
File selector. Keep the same UUID `correlation_id` when retrying an upload.
Use `kind=general` for library storage or a supported media kind for its existing
processing flow. The signed source URL is not returned to the model.

`file_list` browses and searches files and folders. File Manager tools rename and
move files, create/rename/move/delete folders, and inspect file deletion impacts.
`file_delete` returns native accepted and rejected IDs: acceptance schedules
durable deletion; references prevent deletion. Folder deletion preserves the
native all-or-nothing reference check.

`document_file_add`, `document_file_replace`, and `document_file_remove` place
and remove existing files in Post/Page bodies. Image MIME files render as images
through the native File Block. Add can set its caption atomically;
`document_file_caption_update` edits or clears the localized caption using the
exact document revision. Replacement preserves the caption. Removing a Block
keeps the reusable File in the library. Page placements require a rich-text
parent Block. Attachment download policy remains a separate explicit action.

`file_transfer` accepts `k=track_audio` and `track_id` to use the existing Track
audio attachment authority. Copy the current `audio_original_file_id` from
`track_list` into `expected_current_file_id`; omit it only when no audio exists.
Keep the returned scoped handle through status and completion. Direct audio
completion still requires browser-prepared media and its
`client_media_bundle_id`. Remote media imports require a UUID `correlation_id`
that remains unchanged across retries.

Map Place tools cover administrator listing, single/batch reads, creation,
settings, and deletion. Map Theme tools cover discovery, resolution, creation,
copying, deletion, default selection, and full snapshot editing with its actual
revision. Snapshot edits use the current request actor's fresh permission,
revision check, and audit transaction. Neither Map domain has a publication
lifecycle. `form_list` and `post_series_list` resolve Page block dependencies.

`member_admin_list` and `member_admin_get` reuse the administrator Member APIs,
including live authorization and personal-data access auditing. Their compact
account projection excludes provider identifiers and authentication credentials.
It includes the avatar asset ID; `member_tag_list` resolves its tag IDs without
returning individual Member data.

`site_settings_get` reads administrator settings and source File bindings;
`site_settings_update` sends only supplied keys to the same atomic
`SiteSettingService.SetManySettings` operation as the admin UI, retaining its
validation, fresh administrator permission check, attachment handling and audit.
Ready PNG/SVG images imported into the File library can be assigned to the light
and dark logo slots; email branding accepts PNG. Site Settings reuses their
existing `image` assets and immutable source File IDs, without copying bytes or
changing asset kinds. Public settings and OG snapshots resolve the same binding.
Dedicated `logo` and `email_image` uploads remain supported.
`mail_adapter_list` returns safe sender/provider metadata without access keys,
passwords, usernames or tokens. `mail_adapter_sender_update` changes sender
name/address and optionally activation/priority through the existing Mail Adapter
update transaction, fresh admin permission, audit and cache invalidation.
Credentials are preserved server-side under the row lock and never accepted or
returned by these tools. Sender changes retain admin provider verification and
may send a verification email to the sender address.

`document_catalog` reads an authorized document's supported kinds, typed fields,
relations, and File ownership; domain defaults and constraints remain with the
owning compiler.

## Go package boundaries

`internal/llm` owns provider-neutral requests, sessions, and bounded failures.
`internal/llm/providers` owns Gemini SDK and OpenAI-compatible HTTP adapters;
provider construction imports that leaf package. Translation validation and
placeholder policies have no provider I/O or logging. The AI adapter reports
private parsing diagnostics in the calling request context while returning the
same bounded invalid-response error.

The integration runner keeps resource composition in `main.go`, serial band
policy in `band.go`, protected lease file I/O in `lease.go`, and child-process
ownership in `process.go`. A band consumes only the backend reset interface.
After cancellation, it finishes bounded cleanup and starts no further packages.
Test-only fixtures and `TestMain` belong in `_test.go` files unless they are
shared deliberately through `internal/testutil`.

## Tests

CI runs unit and package tests with `go test ./...`, media-tool tests, `go vet`,
golangci-lint (`.golangci.yml`, on both the default and `integration` builds),
the server build, and integration tests. The required `Validate` check waits for
all of them. Run the linter locally with `golangci-lint run ./...` and
`golangci-lint run --build-tags=integration ./...`.
Input validation, authorization decisions, state
transitions, and adapter mappings belong in unit tests. Reuse the owning
service's tests when an adapter only delegates to it.

Use integration tests for PostgreSQL constraints, transaction rollback, and
storage or external-service behavior that an in-process test cannot exercise.
They use the `integration` build tag. The local database suite needs only a
reviewed schema checkout and the pinned PostgreSQL image:

```sh
make test-integration-db
```

Pull requests run the cataloged integration packages whose test binaries import
a package the pull request changed (`--changed-from <base>`). Changes the import
graph cannot attribute, such as `go.mod`, the runner itself, assets outside a
package, or `.github/workflows/ci.yml` (which pins the schema, images and sibling
revisions), run the whole catalog; documentation and other workflow changes run
none. Packages whose tests start `cmd/server` also run when its dependencies
change. The
nightly schedule and a manual `CI` dispatch run the whole catalog. CI pins the
schema, PostgreSQL image, and the sibling Identity, editor-collab, Common,
Contracts and Telemetry revisions in `.github/workflows/ci.yml`. To preview a
selection locally:

```sh
GOWORK=off go run -tags=integration ./scripts/test/integration --changed-from origin/main --list
```

Set `INTEGRATION_SCHEMA_ROOT` and `INTEGRATION_POSTGRES_IMAGE` to select exact
local inputs. The runner starts its own PostgreSQL container and creates
disposable databases.

Every integration test owns its state, so tests and packages run concurrently
and nothing is reset between them. The runner starts one set of containers
(PostgreSQL, Kratos, SpiceDB, Oathkeeper, MinIO, imgproxy) for the whole run and
discards them at the end. Each test then gets:

- its own PostgreSQL database, cloned from the schema template in about 0.1 s
  (`SetupOryStack`, `PrepareOryIntegrationTest`, `StartBackendIntegrationStack`,
  `SetupAppPostgres`);
- its own SpiceDB datastore (SpiceDB `serve-testing` isolates datastores by
  bearer token) with the generated schema and role graph;
- its own S3 buckets when it uses a backend or runtime stack.

Kratos is the one shared service. In an Ory or backend stack database, the
`kratos` schema is a postgres_fdw view of the shared Kratos database, so tests
must use unique identity IDs and emails, assert only on identities they created,
and never update or delete `kratos.*` rows they do not own. postgres_fdw does
not support `ON CONFLICT ... DO UPDATE` on those tables. The foreign server uses
remote estimates so joins such as identity fences lock only the joined rows.

postgres_fdw runs remote transactions at REPEATABLE READ, so a test that locks
or re-reads `kratos.*` rows inside a transaction while another connection
changes them (identity and session fences, revocation ordering) would see a
serialization failure where production waits. Such tests use
`SetupOryStackWithLocalKratos`: its database holds a private copy of the kratos
schema with real tables. The Kratos API does not see that copy, and
`CreateUser` seeds identities with SQL there.

Every top-level test in an integration-tagged file starts with `t.Parallel()`;
`scripts/test/integration` enforces this. A test that must replace
process-global state may opt out with a `// integration:serial <reason>` doc
comment. `--jobs` (default 4) bounds both package and test concurrency. The
runtime band (`cmd/backend-integration-stack`, `cmd/server/runtimetest`,
`internal/filemedia/runtimetest`, `internal/testutil`) still runs one test at a time
because its fixtures start shared application processes and register the single
Kratos hook upstream.

For in-process SQLite fixtures, use `internal/testutil/sqlitetest.Open` with the
test's SQLite driver. It allocates a fresh shared-memory database per invocation
and closes its connection pool with `t.Cleanup`. A fixed name, including
`t.Name()`, can retain rows across `go test -count` runs in the same process.

Existing native system tests are available locally for changes to the relevant
service boundary. For a focused regression, pass its package and test name:

```sh
make test-integration-package PKG=./internal/page \
  RUN='^TestPageAIDocumentLayoutExactMutationIntegration$$'
```

The native runner currently requires pinned runtime images, the sibling
Identity, collaboration, Common, Contracts, and Telemetry checkouts, their
frozen-lockfile Node dependencies, media assets, FFmpeg/FFprobe, and `cwebp`.
Prepare the matching Kratos image with
`docker build -f ../geul-identity/Dockerfile.kratos -t geul-identity-kratos:local ../geul-identity`,
or select one with `GEUL_TEST_KRATOS_IMAGE`. Stock Kratos lacks the required
settings inventory. The runner does not pull images automatically. These
system tests are not a second full-stack CI gate for every API change.

The runner resolves test assets from the API module root, including when built
with `-trimpath`. SpiceDB uses the canonical sibling
`../geul-identity/config/spicedb/schema.generated.zed`; no copy under API's
`infra/` is needed. Prepare the matching Identity checkout and its generated
artifacts before running. The runner checks the reviewed schema, Identity
configs, `geul-editor-collab`, `geul-common`, `geul-event-contracts`, and
`geul-telemetry` checkouts and all selected local images before starting containers.
The `db` band needs only `schema.sql` and its PostgreSQL image.

The default MinIO image is Chainguard's build, pinned by digest, because the
upstream `minio/minio` images are no longer pullable from Docker Hub. Override
it with `--minio-image` or `GEUL_INTEGRATION_MINIO_IMAGE` when needed.

`--minio-image` overrides `GEUL_INTEGRATION_MINIO_IMAGE` for both the shared
backend and child test processes. MinIO readiness does not require a shell in
the image, and its disposable `/data` mount supports non-root images. This
local override does not change the production image pin. Pre-pull or build the
other required images for the Docker daemon's native architecture as well;
missing-image diagnostics identify the exact image.

## License

PolyForm Noncommercial License 1.0.0. See [LICENSE](LICENSE).

## Internal personal access token verification

Personal access tokens use only `pat_<selector>.<secret>` for issuance,
regeneration and verification. The database stores the selector and SHA-256
secret verifier.

The legacy Worker verification endpoint and its dedicated credential are
deprecated and retained for migration. Set the optional
`PAT_VERIFICATION_SECRET` (32–256 bytes) to enable
`POST /internal/auth/personal-access-token/verify` on the main listener. An
unset secret leaves the route unregistered. This dedicated credential is
independent of internal RPC, session, OAuth and media signing secrets. Keep
the route outside the public Oathkeeper RPC policy.

Trusted callers authenticate using HTTP Basic (`pat-verifier` and the
verification secret), provide a Member personal access token in `X-API-Key`,
and send an empty body with no query parameters. The API delegates to the
existing Member PAT service on every request. Regeneration and deletion
invalidate the previous bearer for subsequent checks.

A successful response is `200 {"member_id":"..."}`. This proves only the
credential's current Member identity; the consuming service owns resource
authorization, quotas, rate limits and cache policy. No product-specific
permission or MCP authorization is granted by this endpoint.

Invalid credentials return 401, invalid transport 400, saturation 503 with
`Retry-After: 1`, and dependency failures 503. All responses use `no-store`.
At most eight verification calls run concurrently, each with a two-second
deadline. Raw bearers, verifiers and incoming identity headers are neither
returned nor logged. Do not cache verification responses.

### Oathkeeper PAT session check

`GET /internal/oathkeeper/pat` is registered only on the existing private
gateway listener, `MCP_PRIVATE_PORT` (default **8001**). It is absent from the
main `PORT` listener (typically 8000), even when legacy verification is enabled.
Oathkeeper sends the original `Authorization: Bearer pat_<selector>.<secret>`
and the existing internal-service credential: `TOKEN_SIGNING_SECRET` in the
header named by `INTERNAL_SERVICE_HEADER_NAME` (default `X-Internal-Service`).
The caller credential uses the same constant-time comparison as internal RPCs;
PAT verifiers use the existing constant-time SHA-256 comparison.

Success returns `200` JSON:

```json
{
  "subject": "<member id>",
  "extra": { "member_id": "<member id>", "token_id": "<selector>" }
}
```

Missing, malformed or invalid credentials return `401 {"error":"Unauthorized"}`.
Dependency failures or invalid stored principals return
`503 {"error":"Service Unavailable"}`. Saturation also returns 503 with
`Retry-After: 1`; wrong methods return JSON 405 with `Allow: GET`. Other paths
are unregistered. Checks require an empty
body and no query parameters. Every handler response sets `Cache-Control:
no-store`. There is no authentication cache, and each call has the existing
two-second deadline and eight-call concurrency bound. Failures use
`failurecause.Attr`, with the PAT and internal credential supplied as private
values rather than log fields.

Both verification endpoints share the Member PAT service. Its final current
verifier check now also requires a live, onboarded Member linked in both
directions to an active, unbanned Kratos Identity. This eligibility check also
applies to the retained legacy endpoint. Regeneration/deletion invalidates the
old bearer immediately for subsequent checks. The current PAT model has no
expiry timestamp: these credentials are non-expiring until replaced/deleted.
No schema change is introduced.

Deployment must allow the Oathkeeper workload to reach the API's private port
through the internal service/NetworkPolicy. Keep that port internal, with no
HAProxy/Tunnel/public Oathkeeper rule exposing this callback. Configure the
OpenDiscogs access rule to use only `bearer_token` for authentication and route
its upstream to `open-discogs-api`. Keep the internal caller header out of
client-controlled forwarding. The authenticator's `additional_headers` injects
the deployment-owned value only into the private check request.

Example global authenticator configuration (merge into the existing config;
render the actual internal service URL, port, header name and secret from the
deployment secret store, without checking the rendered credential into Git):

```yaml
authenticators:
  bearer_token:
    enabled: true
    config:
      check_session_url: http://geul-api:8001/internal/oathkeeper/pat
      token_from:
        header: Authorization
      prefix: pat_
      preserve_path: true
      preserve_query: true
      force_method: GET
      forward_http_headers: [Authorization]
      additional_headers:
        X-Internal-Service: "<rendered TOKEN_SIGNING_SECRET>"
      subject_from: subject
      extra_from: extra
```

On the OpenDiscogs access rule, set `authenticators: [{handler: bearer_token}]`
without anonymous or cookie-session fallback. `subject_from: subject` is
explicit because Oathkeeper v26.2.0 otherwise defaults bearer-token subjects to
`sub`. The prefix filters credentials before the API performs full validation.
The path, query, method and header options follow the
[Ory authenticator contract](https://www.ory.com/docs/oathkeeper/pipeline/authn)
and [v26.2.0 implementation](https://github.com/ory/oathkeeper/blob/v26.2.0/pipeline/authn/authenticator_bearer_token.go).
Do not add a cache around the callback. Once the Worker has been retired and
no other callers need the legacy endpoint, unset `PAT_VERIFICATION_SECRET`;
its endpoint and credential remain in code for this migration.

### HTTP admission responses

Quota rejection uses 429 with `Retry-After` in whole seconds. Server concurrency
saturation uses 503 with `Retry-After`; it is not a Member usage quota. The trusted
PAT verifier challenges gateway credentials with HTTP Basic; public consumers
own their Bearer challenges and authorization policies.

The login-code reservation transaction supplies `RateLimit-Policy` and
`RateLimit` (IETF draft-ietf-httpapi-ratelimit-headers-11, not a published RFC).
`auth-code-ip` counts outstanding reservations for the normalized caller IP over
600 seconds. `r` is available reservations after this request; `t=600` is the
effective rolling window, not an absolute reset timestamp. Failed delivery may
release a reservation after the response snapshot. Cooldown, address and global
budgets also apply; their activity counters are private. `Retry-After` is the
maximum wait across rejected budgets and takes precedence over `t`.

Unified authentication responses preserve admission headers, and browser CORS
exposes them. OAuth, Connect and Kratos error payloads retain their existing
protocol contracts. Cookie-session routes do not accept PATs and must not
advertise Bearer authentication merely to share a response format.

## Concurrent content and delivery contracts

Public contact settings use only `contact_email` and the `contact.email` content
variable; legal contact remains separate. Removed support/privacy settings and
content keys are rejected.

Contracts 0.14.0 renames the shared block field `previewWidth` to `displaySize`
with no read alias.

Post configuration writes use `configuration_revision` as an optimistic
concurrency token. Manage clients read it from `manage.v1.Post` and send it as
`expected_configuration_revision` on every `UpdatePost` call. The API compares
it against the row reloaded under the Post root lock after rechecking live edit
authorization. Stale writes return `ABORTED`; reload the Post before retrying.
Only changes to slug, comments, map place, or document layout rotate this token.
Lifecycle and content writes leave it unchanged. Apply the matching
`geul-schema` migration before starting this API. See
[`docs/post-configuration-concurrency.md`](docs/post-configuration-concurrency.md)
for the full contract.

Post and Work public detail reads authorize the locked root and load localized
metadata and body in one repeatable-read transaction. Content writers lock the
root before the document. A read started before unpublish may return that
published snapshot, but it cannot combine its published status with a later
draft body. Work also captures its featured-image source, credit groups, credits,
and client associations in that transaction before resolving public identity
and ready assets. Draft and share-link access retain their existing authorization.
Delayed translations cannot write into a locale promoted to the current source
unless the job itself targets that current source.

Work, Program Event, and Program Event Type public lists reuse or batch their
loaded relations. The [public-read query measurements](docs/public-read-query-budget.md)
record the same-fixture before/after counts and remaining resolver costs.

Map Theme collaboration uses exactly the locale-neutral `und` locale. Other
collaboration resources retain exact supported-locale validation. Program Event
updates validate the combined time/location patch against the root returned
under `FOR UPDATE`; TBA and ONLINE locations clear map-place IDs.

Campaign workers claim pending recipients before contacting a provider. Claims
have UUID owners and ten-minute leases; the application deadline is at most five
minutes and respects the caller's shorter deadline. Terminal writes and releases
are fenced by the current owner. Recovery ignores active claims. Locks follow
layout, campaign, run, then recipient order. The deployment's versioned
`email-delivery-claims-v1` migration must complete before this API is started.
Provider acceptance followed by a process crash or failed terminal database write
still has an at-least-once delivery ambiguity; the lease prevents immediate
parallel delivery but does not make the provider and database atomic.

Metadata AI jobs use a three-minute attempt lease and timestamp-fenced results.
Queue delivery retries up to eight times with exponential backoff. A minute
reconciler checks jobs unchanged for ten minutes, and republishes only when no
active PGMQ row has the job's stable message ID. A running attempt must also be
stale. Enqueue and recovery timestamps commit together; canceled handlers get
at most five seconds to persist their fenced failure state.

Queue handlers create one cancelable or timeout context per delivery. A local
same-condition diagnostic of 10,000 completed timed deliveries retained 10,000
children with the former overwritten cancel function and zero with the single
constructor. This measures retained context registrations rather than production
RSS or request latency.
