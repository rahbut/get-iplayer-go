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

# Build binary (statically linked for Alpine)
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-w -s -X main.BuildTime=${BUILD_TIME} -X main.Version=${VERSION}" -o get-iplayer-go ./cmd/get-iplayer-go/
# Stage 2: Create minimal runtime image
FROM alpine:3.19

# Install ffmpeg and ffprobe (only runtime dependencies)
RUN apk add --no-cache ffmpeg

# Copy binary from builder
COPY --from=builder /build/get-iplayer-go /usr/local/bin/get-iplayer-go

# Set working directory (app will create downloads/ subdirectory)
WORKDIR /app

# Expose port 7373
EXPOSE 7373

# Set default port
ENV PORT=7373

# Run web server by default
CMD ["get-iplayer-go", "web"]
