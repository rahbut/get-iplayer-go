# Stage 1: Build minimal ffmpeg/ffprobe (MPEG-TS + MP4/M4A mux/demux + JPEG only)
FROM alpine:3.21 AS ffmpeg-builder

ARG FFMPEG_VERSION=8.1

RUN apk add --no-cache \
    build-base \
    nasm \
    pkgconf \
    wget \
    xz

WORKDIR /ffmpeg-src
RUN wget -q "https://ffmpeg.org/releases/ffmpeg-${FFMPEG_VERSION}.tar.xz" \
    && tar xf "ffmpeg-${FFMPEG_VERSION}.tar.xz" \
    && rm "ffmpeg-${FFMPEG_VERSION}.tar.xz"

WORKDIR /ffmpeg-src/ffmpeg-${FFMPEG_VERSION}
RUN ./configure \
    --prefix=/ffmpeg-out \
    --disable-everything \
    --disable-network \
    --disable-autodetect \
    --disable-doc \
    --disable-htmlpages \
    --disable-manpages \
    --disable-podpages \
    --disable-txtpages \
    --disable-debug \
    --enable-small \
    --enable-static \
    --disable-shared \
    --extra-ldflags="-static" \
    --enable-ffmpeg \
    --enable-ffprobe \
    # Container formats
    --enable-demuxer=mpegts,mov,image2 \
    --enable-muxer=mov,image2 \
    # Protocols
    --enable-protocol=file \
    # JPEG support (for thumbnail embedding)
    --enable-decoder=mjpeg \
    --enable-encoder=mjpeg \
    --enable-parser=mjpeg \
    # Passthrough decoders (stream copy still needs these registered for probing)
    --enable-decoder=h264,aac,mp3 \
    --enable-bsf=aac_adtstoasc,h264_mp4toannexb \
    && make -j"$(nproc)" \
    && make install

# Stage 2: Build the Go binary
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

# Stage 3: Minimal runtime image
FROM gcr.io/distroless/static-debian12

# Copy minimal ffmpeg and ffprobe
COPY --from=ffmpeg-builder /ffmpeg-out/bin/ffmpeg /usr/local/bin/ffmpeg
COPY --from=ffmpeg-builder /ffmpeg-out/bin/ffprobe /usr/local/bin/ffprobe

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
