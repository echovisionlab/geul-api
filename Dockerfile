# syntax=docker/dockerfile:1@sha256:ecfaec9ed6d810b56388c508f4121597bfbba70d41a6dfeee4d8cad5f295fc32

FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.27.0-alpine3.24@sha256:4c9fe60190a2a3350ddc51de80d0224b8a6698d12bdfc999fee45ea9d6c46dbc AS builder

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
