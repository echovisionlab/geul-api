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
