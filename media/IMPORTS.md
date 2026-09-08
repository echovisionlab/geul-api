# Imported service sources

This integration moves the existing implementations; it does not replace their
processing engines, event contracts, retry rules, or persisted object layouts.

| Original repository | Revision | API location |
| --- | --- | --- |
| geul-cdn | `036eeb90fd86b76d30773832f9470907e3f8a356` | `internal/mediadelivery` |
| geul-transcoder | `f8a57dfa0c26eda5ce1e6b654920a1a643ee66d0` | `internal/transcoding` |
| geul-asset-optimizer | `7b3c498665608573fb6fd2f91d3c69336831e123` | `internal/assetprocessing + media/asset-optimizer` |
| geul-og | `e3eed6a5574a21928bc9b20babe58d3e69330881` | `media/og` |

## Integration changes

- `geul-cdn`: 33 Go files unchanged after import/package renaming and gofmt. Adapted files: `http_handler.go`.
- `geul-transcoder`: all 72 imported Go files unchanged after import/package renaming and gofmt.
- `geul-asset-optimizer`: 27 of 28 Go files unchanged after import/package renaming and gofmt; the fixture path in `optimizer/inspect_test.go` follows its new directory.
- All 76 imported OG source, test, template, font and build/dependency files are byte-for-byte copies. The API supervises its original Node entry point; queue and renderer code are untouched.
- All 10 imported Asset Optimizer Node/script files, including npm manifests and lockfile, are byte-for-byte copies.
- Each imported worker retains its original DB pool and S3 adapter. Transcoder and waveform retain separate pools and temporary directories. Resource sharing and deduplication are deferred.
- Worker imports use API package paths. The shared telemetry bootstrap remains owned by the API; each imported service retains its system-event helpers.
- Standalone Go mains/configuration are replaced only by API composition. CDN's HTTP handler is exported for registration. Source-test fixture paths are adjusted for the new directory layout.
- FFmpeg invocations and imgproxy signing/transform requests remain unchanged. imgproxy runs as the existing container, placed alongside API in the same deployment pod. Its upstream image was verified to have the same digest (`73c5dda13199745b0d2d00e4149d4a589c5b2e205dbf335ca05eca8276b54712`) as the existing mirror image; using upstream avoids a private registry credential in the public API pod.
- Separate delivery and loopback OG health listeners retain streaming/authentication behavior. Internal OG RPC remains authenticated with the existing service token.

Production cutover requires the new API image before the obsolete workload
manifests are removed. Once deployment and live behavior are verified, the four
original local and GitHub repositories are retired; this file records their
source revisions. Existing queue names,
media hostnames, signing secrets, buckets and object keys must remain unchanged.
The deployment worktree is a cutover draft until its API image digest is updated.

## Validation (2026-09-08)

- API unit/package suite passed; imported Go worker/config packages and composition checks passed after restoring independent worker resources.
- Original OG unit suite: 225 tests passed. Original OG MinIO integration: 2 passed. Original particle mesh script: 3 passed.
- API `internal/filemedia` integration package passed against fresh PostgreSQL, Kratos, SpiceDB, MinIO, imgproxy and the integrated source runtime. This includes upload, signed delivery, transcoding, waveform and injected failure paths.
- OG process supervision passed race-enabled tests for graceful result commit, unexpected exit and forced shutdown after deadline.
- Production image built and executed original Draco/WebP and particle mesh tools, FFmpeg HLS output, and Sharp with bundled OG fonts.
- The final Linux amd64 Docker image passed the full file/media integration package (190.349 seconds), including authenticated OG generation and CDN retrieval, video HLS and mesh optimization. OG retains UID/GID 1000 via its image launcher.
- Changed Go package vet checks passed. Deployment validation passed all 48 tests, nine Kustomize roots, the image catalog validator and Kubernetes server-side dry run. The rollout is split into preparation and traffic cutover revisions; imgproxy uses native sidecar shutdown ordering.

Two existing API test issues had to be corrected to run this boundary: duplicated
multipart helper declarations were removed in favor of the existing shared test
helper, and an Author-role assertion was aligned with the existing File.List
permission contract. No production authorization rule was changed.
The local runtime harness uses short `/tmp/geul-media-*` paths to stay below the
macOS Unix socket path limit; FFmpeg source is unchanged.

No production rollout has been performed. Deduplication, shared DB/S3 resources
and processing refactors are explicitly deferred to a subsequent change.
