# Stage 1: Build minimal ffmpeg/ffprobe — always runs on the native builder (no QEMU)
FROM --platform=$BUILDPLATFORM debian:bookworm-slim AS ffmpeg-builder

ARG FFMPEG_VERSION=8.1
ARG TARGETARCH

RUN apt-get update -qq && apt-get install -y --no-install-recommends \
    build-essential \
    ca-certificates \
    nasm \
    pkg-config \
    wget \
    xz-utils \
    # Cross-compiler for arm64 builds
    gcc-aarch64-linux-gnu \
    binutils-aarch64-linux-gnu \
    libc6-dev-arm64-cross \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /ffmpeg-src
RUN wget -q "https://ffmpeg.org/releases/ffmpeg-${FFMPEG_VERSION}.tar.xz" \
    && tar xf "ffmpeg-${FFMPEG_VERSION}.tar.xz" \
    && rm "ffmpeg-${FFMPEG_VERSION}.tar.xz"

WORKDIR /ffmpeg-src/ffmpeg-${FFMPEG_VERSION}
RUN set -e; \
    COMMON_FLAGS="--prefix=/ffmpeg-out \
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
        --enable-ffmpeg \
        --enable-ffprobe \
        --enable-demuxer=mpegts,mov,image2 \
        --enable-muxer=mov,mp4,image2 \
        --enable-protocol=file \
        --enable-decoder=mjpeg \
        --enable-encoder=mjpeg \
        --enable-parser=mjpeg \
        --enable-decoder=h264,aac,mp3 \
        --enable-bsf=aac_adtstoasc,h264_mp4toannexb"; \
    if [ "$TARGETARCH" = "arm64" ]; then \
        SYSROOT=/usr/aarch64-linux-gnu; \
        ./configure $COMMON_FLAGS \
            --arch=aarch64 \
            --cross-prefix=aarch64-linux-gnu- \
            --target-os=linux \
            --extra-cflags="--sysroot=${SYSROOT}" \
            --extra-ldflags="--sysroot=${SYSROOT} -static"; \
    else \
        ./configure $COMMON_FLAGS \
            --arch=x86_64 \
            --extra-ldflags="-static"; \
    fi \
    && make -j"$(nproc)" \
    && make install

# Stage 2: Build the Go binary — also runs natively via Go's built-in cross-compilation
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS builder

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ ./cmd/

ARG BUILD_TIME=dev
ARG VERSION=dev
ARG TARGETARCH

RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build \
    -ldflags="-w -s -X main.BuildTime=${BUILD_TIME} -X main.Version=${VERSION}" \
    -o get-iplayer-go ./cmd/get-iplayer-go/

# Stage 3: Minimal runtime image
FROM gcr.io/distroless/static-debian12

COPY --from=ffmpeg-builder /ffmpeg-out/bin/ffmpeg /usr/local/bin/ffmpeg
COPY --from=ffmpeg-builder /ffmpeg-out/bin/ffprobe /usr/local/bin/ffprobe
COPY --from=builder /build/get-iplayer-go /usr/local/bin/get-iplayer-go

WORKDIR /app
EXPOSE 7373
ENV PORT=7373
CMD ["get-iplayer-go", "web"]
