# syntax=docker/dockerfile:1.28.0@sha256:bb22d9815c728170f72750f4e5b0d672e06176142e1d602c7e66c050100b7e5b

FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.27.3-alpine3.24@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb AS builder

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

FROM docker.io/library/alpine:3.24.3@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a

COPY --from=builder /out/minio /usr/bin/minio

EXPOSE 9000 9001

ENTRYPOINT ["/usr/bin/minio"]
CMD ["server", "/data"]
