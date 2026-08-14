# syntax=docker/dockerfile:1.7

FROM oven/bun:1.3.11 AS web-deps
WORKDIR /web
COPY web/package.json web/bun.lock ./
RUN --mount=type=cache,target=/root/.bun/install/cache bun install --frozen-lockfile

FROM node:24-bookworm-slim AS web-builder
WORKDIR /web
ENV NEXT_TELEMETRY_DISABLED=1
COPY --from=web-deps /web/node_modules ./node_modules
COPY web/package.json ./package.json
COPY web/next.config.ts web/tsconfig.json web/vitest.config.ts web/vitest.setup.ts web/eslint.config.mjs ./
COPY web/src ./src
RUN node node_modules/next/dist/bin/next build

FROM golang:1.24-bookworm AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY web/assets.go ./web/assets.go
COPY --from=web-builder /web/out ./web/out
RUN --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/blackpearl ./cmd/blackpearl

FROM debian:bookworm-slim AS fixture
RUN apt-get update \
    && apt-get install -y --no-install-recommends ffmpeg \
    && rm -rf /var/lib/apt/lists/*
RUN mkdir -p /fixture \
    && ffmpeg -hide_banner -loglevel error -y \
       -f lavfi -i "testsrc2=size=1280x720:rate=24" \
       -f lavfi -i "sine=frequency=440:sample_rate=48000" \
       -t 8 -c:v libx264 -preset veryfast -crf 20 -pix_fmt yuv420p -c:a aac -b:a 128k \
       -movflags +faststart /fixture/blackpearl-poc.mp4

FROM debian:bookworm-slim AS runtime
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl fuse3 \
    && rm -rf /var/lib/apt/lists/*
COPY --from=builder /out/blackpearl /usr/local/bin/blackpearl
RUN mkdir -p /var/lib/blackpearl/cache /mnt/blackpearl
EXPOSE 8080 2049
STOPSIGNAL SIGTERM
ENTRYPOINT ["/usr/local/bin/blackpearl"]

FROM runtime AS poc
COPY --from=fixture /fixture/blackpearl-poc.mp4 /opt/blackpearl/fixtures/blackpearl-poc.mp4
ENV BLACKPEARL_POC_SOURCE=/opt/blackpearl/fixtures/blackpearl-poc.mp4

FROM nginx:1.29.5-alpine AS range-origin
COPY deploy/range-origin.conf /etc/nginx/nginx.conf
COPY --from=fixture /fixture/blackpearl-poc.mp4 /srv/media/blackpearl-poc.mp4
