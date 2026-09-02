# Geul API

Geul API is a Go service for content, media, identity, and MCP operations.

## Quick start

Requires Go 1.26.6 and the runtime values described by
`internal/config.Config`. The trust boundary is configured explicitly with
`AUTH_HEADER_NAME` and `INTERNAL_SERVICE_HEADER_NAME`; invalid or missing
names fail startup closed. `SESSION_COOKIE_NAME` is the companion Identity/Ory
session-cookie contract.

```sh
go mod download
go test ./...
go build ./cmd/server
```

Build the production image locally:

```sh
docker build -t registry.dsub.io/echovisionlab/geul-api:0.1.0 .
```

The API uses the public Go modules
`github.com/echovisionlab/geul-event-contracts`,
`github.com/echovisionlab/geul-mediaauth`, and
`github.com/echovisionlab/geul-telemetry`. The companion TypeScript package
`@echovisionlab/geul-common` is not a Go dependency of this service.

## Integration tests

Unit and package tests run with `go test ./...`. Integration tests are
explicitly tagged and require a local reviewed schema checkout plus the
already available runtime images:

```sh
make test-integration
```

The harness never pulls images or applies production schema automatically.
Set `INTEGRATION_SCHEMA_ROOT`, `INTEGRATION_POSTGRES_IMAGE`, or
`INTEGRATION_CDN_IMAGE` to select exact local inputs.

## Compatibility

MCP protocol fields, database and event identifiers, persisted media/email
markers, and other public wire/storage identifiers retain their established
values. Display names and repository-owned branding use Geul.

## License

PolyForm Noncommercial License 1.0.0. See [LICENSE](LICENSE).
