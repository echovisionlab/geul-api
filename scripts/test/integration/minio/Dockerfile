# syntax=docker/dockerfile:1@sha256:ecfaec9ed6d810b56388c508f4121597bfbba70d41a6dfeee4d8cad5f295fc32

FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.24.2-alpine3.21@sha256:7772cb5322baa875edd74705556d08f0eeca7b9c4b5367754ce3f2f00041ccee AS builder

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

FROM --platform=$TARGETPLATFORM docker.io/library/alpine:3.21.3@sha256:a8560b36e8b8210634f77d9f7f9efd7ffa463e380b75e2e74aff4511df3ef88c

COPY --from=builder /out/minio /usr/bin/minio

EXPOSE 9000 9001

ENTRYPOINT ["/usr/bin/minio"]
CMD ["server", "/data"]
