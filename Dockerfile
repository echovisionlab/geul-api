# syntax=docker/dockerfile:1@sha256:ecfaec9ed6d810b56388c508f4121597bfbba70d41a6dfeee4d8cad5f295fc32

FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.27.1-alpine3.24@sha256:cf6fca6641884b8433441b2b0652976f975e1d0fdd26d177eaaf8596087f3125 AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /app

# Download public Go dependencies before copying the source tree.
COPY go.mod go.sum ./
RUN go mod download && go mod verify

# Copy source code.
COPY . .

# Build single production binary (includes API, worker, scheduler).
RUN GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} go build -mod=readonly -o backend ./cmd/server

# Final image
FROM docker.io/library/alpine:3.24@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b

RUN apk add --no-cache ca-certificates imagemagick rsvg-convert tzdata

WORKDIR /app

RUN mkdir -p /coverdata

COPY --from=builder /app/backend .
COPY --from=builder /app/assets ./assets

ENV MAGICK_CONFIGURE_PATH=/app/assets/imagemagick

EXPOSE 8000

CMD ["./backend"]
