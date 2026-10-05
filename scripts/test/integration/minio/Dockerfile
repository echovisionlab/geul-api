# syntax=docker/dockerfile:1.27.1@sha256:4edf897a3ffa55b89f906fc8cc78afdb3f1834cc9c7083565e611a8a7d5fe99e

FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.27.1-alpine3.24@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /src

RUN apk add --no-cache git && \
	git init && \
	git remote add origin https://github.com/minio/minio.git && \
	git fetch --depth=1 origin refs/tags/RELEASE.2025-04-22T22-12-26Z:refs/tags/RELEASE.2025-04-22T22-12-26Z && \
	git checkout --detach refs/tags/RELEASE.2025-04-22T22-12-26Z && \
	test "$(git rev-parse HEAD)" = 0d7408fc9969caf07de6a8c3a84f9fbb10a6739e

RUN set -eu; \
	ldflags="$(MINIO_RELEASE=RELEASE go run buildscripts/gen-ldflags.go)"; \
	mkdir -p /out; \
	CGO_ENABLED=0 GOOS="${TARGETOS:-linux}" GOARCH="${TARGETARCH:-amd64}" \
		go build -mod=readonly -tags kqueue -trimpath --ldflags "$ldflags" -o /out/minio .

FROM docker.io/library/alpine:3.24.2@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6

COPY --from=builder /out/minio /usr/bin/minio

EXPOSE 9000 9001

ENTRYPOINT ["/usr/bin/minio"]
CMD ["server", "/data"]
