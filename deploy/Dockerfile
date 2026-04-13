# Stage 1: Build the Go binary
FROM golang:1.25-alpine AS builder

WORKDIR /build

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY cmd/ ./cmd/

# Accept build arguments
ARG BUILD_TIME=dev
ARG VERSION=dev
ARG TARGETARCH

# Build binary (statically linked, target architecture injected by buildx)
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -ldflags="-w -s -X main.BuildTime=${BUILD_TIME} -X main.Version=${VERSION}" -o get-iplayer-go ./cmd/get-iplayer-go/

# Stage 2: Pull static ffmpeg/ffprobe binaries (multi-arch)
FROM mwader/static-ffmpeg:8.1 AS ffmpeg

# Stage 3: Minimal runtime image
FROM gcr.io/distroless/static-debian12

# Copy ffmpeg and ffprobe
COPY --from=ffmpeg /ffmpeg /usr/local/bin/ffmpeg
COPY --from=ffmpeg /ffprobe /usr/local/bin/ffprobe

# Copy application binary
COPY --from=builder /build/get-iplayer-go /usr/local/bin/get-iplayer-go

# Set working directory (app will create downloads/ subdirectory)
WORKDIR /app

# Expose port 7373
EXPOSE 7373

# Set default port
ENV PORT=7373

# Run web server by default
CMD ["get-iplayer-go", "web"]
