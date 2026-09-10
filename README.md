# Geul API

Geul API is a Go service for content, media, identity, and MCP operations.

## Quick start

Requires Go 1.26.6, Node 24.19.0, pnpm 11.22.0, FFmpeg/FFprobe, ImageMagick,
and the runtime values described by
`internal/config.Config`. The trust boundary is configured explicitly with
`AUTH_HEADER_NAME` and `INTERNAL_SERVICE_HEADER_NAME`; invalid or missing
names fail startup closed. `SESSION_COOKIE_NAME` is the companion Identity/Ory
session-cookie contract.

```sh
go mod download
make media-build
go test ./...
go build ./cmd/server
```

Build the production image locally:

```sh
docker build -t registry.dsub.io/echovisionlab/geul-api:0.1.0 .
```

The API uses the public Go modules
`github.com/echovisionlab/geul-event-contracts` and
`github.com/echovisionlab/geul-telemetry`. The companion TypeScript package
`@echovisionlab/geul-common` is not a Go dependency of this service.

## Included media services

CDN delivery, audio/video transcoding, waveform generation, mesh optimization,
and the original Node OG worker ship with the API. No separate Geul CDN,
Transcoder, Waveform, Asset Optimizer or OG image is needed.

- `internal/mediadelivery` retains CDN routes, signed URLs, Range handling and imgproxy requests.
- `internal/transcoding` and `internal/assetprocessing` retain the existing PGMQ consumers and processing code, retaining their own database pools and S3 adapters.
- `media/og` contains the original Node worker, dependencies, templates and fonts. The API starts it after binding HTTP, includes its health in readiness, and drains it before closing internal RPC.
- `media/asset-optimizer` retains the original npm lockfile and mesh script.

Image transformations still use imgproxy. Compose starts the existing engine
alongside the API; the deployment manifest puts that same container in the API
pod. It is a native dependency, with no replacement transformation implementation.

The API serves RPC on port 8000, private MCP on 8001, and the existing CDN/media
routes on 8002. OG health binds only to loopback on 3010. Existing CDN/media
hostnames and signed paths can keep pointing to the delivery listener.
Set `IMGPROXY_KEY` and `IMGPROXY_SALT` to the same hex secrets as imgproxy and
`CDN_IMGPROXY_URL` to its address (default `http://127.0.0.1:8080`).

For source execution after `make media-build`, also set:

```sh
export OG_WORKER_SCRIPT="$PWD/media/og/dist/index.js"
export GLTF_TRANSFORM_PATH="$PWD/media/asset-optimizer/node_modules/.bin/gltf-transform"
export PARTICLE_MESH_SCRIPT_PATH="$PWD/media/asset-optimizer/scripts/optimize-particle-mesh.mjs"
```

`MEDIA_AUDIO_WORKERS`, `MEDIA_VIDEO_WORKERS`, `MEDIA_WAVEFORM_WORKERS`, and
`OG_GENERATE_WORKERS` preserve production concurrency defaults (3, 3, 2, 12).
Keep the termination grace period above `OG_SHUTDOWN_TIMEOUT_MS` (120000 by
default) plus API cleanup time; Compose allows 140 seconds. Source revisions
and integration-only changes are recorded in [media/IMPORTS.md](media/IMPORTS.md).

## Integration tests

Build the matching Identity Kratos image before running integration tests:

```sh
docker build -f ../geul-identity/Dockerfile.kratos -t geul-identity-kratos:local ../geul-identity
```

An explicit test image can be selected with `GEUL_TEST_KRATOS_IMAGE`. Stock
Kratos omits the credential inventory required by account settings policy and
is not a substitute for this source-built runtime.


Unit and package tests run with `go test ./...`. Integration tests are
explicitly tagged and require a local reviewed schema checkout plus the
already available runtime images:

```sh
make test-integration
```

The harness never pulls images or applies production schema automatically.
Set `INTEGRATION_SCHEMA_ROOT` and `INTEGRATION_POSTGRES_IMAGE` to select exact local inputs.
Media delivery and processing run from this API checkout; imgproxy remains a
preinstalled native-engine image.

## Compatibility

MCP protocol fields, database and event identifiers, persisted media/email
markers, and other public wire/storage identifiers retain their established
values. Display names and repository-owned branding use Geul.

## License

PolyForm Noncommercial License 1.0.0. See [LICENSE](LICENSE).

## Internal personal access token verification

New and regenerated personal access tokens use `pat_<selector>.<secret>`.
Previously issued `geul_pat_` credentials remain accepted as compatibility
aliases. Replacing only their leading `geul_pat_` with `pat_` preserves the same
credential; it does not rotate the secret. Both spellings share the stored
selector/SHA-256 verifier and are invalidated together by regeneration or
deletion. No database table, column or stored verifier migration is required.

Set the optional `PAT_VERIFICATION_SECRET` (32–256 bytes) to enable
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
