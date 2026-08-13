# syntax=docker/dockerfile:1.7

FROM golang:1.24-bookworm AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd ./cmd
COPY internal ./internal
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
EXPOSE 8080
STOPSIGNAL SIGTERM
ENTRYPOINT ["/usr/local/bin/blackpearl"]

FROM runtime AS poc
COPY --from=fixture /fixture/blackpearl-poc.mp4 /opt/blackpearl/fixtures/blackpearl-poc.mp4
ENV BLACKPEARL_POC_SOURCE=/opt/blackpearl/fixtures/blackpearl-poc.mp4
