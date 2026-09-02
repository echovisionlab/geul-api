.PHONY: deps tidy test test-integration test-integration-db test-integration-runner test-integration-required build run clean

INTEGRATION_SCHEMA_ROOT ?= ../geul-schema
INTEGRATION_POSTGRES_IMAGE ?= registry.dsub.io/echovisionlab/geul-postgres@sha256:41a2c6fb9e026ed327463e7662c92c5cc27e918bdaae6fa3447f45335d74494a
INTEGRATION_CDN_IMAGE ?= geul-cdn:integration

# Download dependencies.
deps:
	go mod download

tidy:
	go mod tidy

# Run the default unit and package tests. Integration tests are behind the
# integration build tag.
test:
	go test ./...

# Run every cataloged integration band with suite-owned PostgreSQL.
test-integration:
	GOWORK=off go run -tags=integration ./scripts/test/integration \
		--schema-root "$(INTEGRATION_SCHEMA_ROOT)" \
		--postgres-image "$(INTEGRATION_POSTGRES_IMAGE)" \
		--cdn-image "$(INTEGRATION_CDN_IMAGE)"


# Run only the database band.
test-integration-db:
	GOWORK=off go run -tags=integration ./scripts/test/integration \
		--band db \
		--schema-root "$(INTEGRATION_SCHEMA_ROOT)" \
		--postgres-image "$(INTEGRATION_POSTGRES_IMAGE)" \
		--cdn-image "$(INTEGRATION_CDN_IMAGE)"

test-integration-runner:
	GOWORK=off go test -count=1 -tags=integration ./scripts/test/integration

# Required local catalog: apply the reviewed current SQL to a fresh Postgres
# instance and validate the schema, without starting the full runtime/browser stack.
test-integration-required: test-integration-db

# Build the server.
build:
	go build -o bin/server ./cmd/server

# Run the server.
run: build
	./bin/server

# Clean build artifacts.
clean:
	rm -rf bin/
