# syntax=docker/dockerfile:1.27.1@sha256:4edf897a3ffa55b89f906fc8cc78afdb3f1834cc9c7083565e611a8a7d5fe99e

FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.27.2-alpine3.24@sha256:f92b6ef800e499660581efdabdf25d9d817a9d124eaf900924f0504e7e27e12d AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /app

ENV GOPRIVATE=github.com/echovisionlab/*
RUN apk add --no-cache git ca-certificates

# Keep the authenticated Git rewrite in tmpfs, outside all image layers.
COPY go.mod go.sum ./
RUN --mount=type=secret,id=private_deps_token,required=true \
    --mount=type=tmpfs,target=/root \
    git config --global url."https://x-access-token:$(cat /run/secrets/private_deps_token)@github.com/".insteadOf "https://github.com/" \
    && go mod download && go mod verify

# Copy source code.
COPY . .

# Build single production binary (includes API, worker, scheduler).
RUN GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} go build -mod=readonly -o backend ./cmd/server

FROM docker.io/library/node:24.21.0-alpine@sha256:ebfe2f90462722a7a4de65e91990e97fe0d401c70e0e762c5b53302f905ec1c1 AS og-builder
WORKDIR /app/media/og
RUN npm install --global pnpm@12.9.1
COPY media/og/package.json media/og/pnpm-lock.yaml media/og/pnpm-workspace.yaml media/og/.npmrc ./
# pnpm ignores environment-backed auth in the repository .npmrc; pass it as pnpm config.
RUN --mount=type=secret,id=private_deps_token,required=true \
    env "pnpm_config_//npm.pkg.github.com/:_authToken=$(cat /run/secrets/private_deps_token)" \
    pnpm install --frozen-lockfile
COPY media/og/ ./
RUN --mount=type=secret,id=private_deps_token,required=true \
    env "pnpm_config_//npm.pkg.github.com/:_authToken=$(cat /run/secrets/private_deps_token)" \
    sh -c 'pnpm typecheck && pnpm build && pnpm prune --prod'

FROM docker.io/library/node:24.21.0-alpine@sha256:ebfe2f90462722a7a4de65e91990e97fe0d401c70e0e762c5b53302f905ec1c1 AS mesh-builder
WORKDIR /app/media/asset-optimizer
COPY media/asset-optimizer/package.json media/asset-optimizer/package-lock.json ./
RUN npm ci --omit=dev

# A single API image owns delivery, durable consumers and native tools.
FROM docker.io/library/node:24.21.0-alpine@sha256:ebfe2f90462722a7a4de65e91990e97fe0d401c70e0e762c5b53302f905ec1c1

RUN apk add --no-cache su-exec ca-certificates imagemagick rsvg-convert tzdata ffmpeg mesa-va-gallium font-noto-arabic font-noto-thai

WORKDIR /app

RUN mkdir -p /coverdata

COPY --from=builder /app/backend .
COPY --from=builder /app/assets ./assets
COPY --from=og-builder /app/media/og/node_modules ./media/og/node_modules
COPY --from=og-builder /app/media/og/dist ./media/og/dist
COPY media/og/assets ./media/og/assets
COPY media/og/package.json media/og/LICENSE.md media/og/THIRD_PARTY_NOTICES.md ./media/og/
COPY media/og/THIRD_PARTY_LICENSES ./media/og/THIRD_PARTY_LICENSES
COPY --from=mesh-builder /app/media/asset-optimizer/node_modules ./media/asset-optimizer/node_modules
COPY media/asset-optimizer/package.json media/asset-optimizer/LICENSE.md ./media/asset-optimizer/
COPY media/asset-optimizer/scripts/optimize-particle-mesh.mjs ./media/asset-optimizer/scripts/optimize-particle-mesh.mjs
COPY --chmod=755 media/asset-optimizer/scripts/asset-transform.mjs ./media/asset-optimizer/scripts/asset-transform.mjs

COPY --chmod=755 media/og-node /usr/local/bin/geul-og-node
ENV OG_NODE_BINARY_PATH=/usr/local/bin/geul-og-node

ENV MAGICK_CONFIGURE_PATH=/app/assets/imagemagick

EXPOSE 8000 8002

CMD ["./backend"]
