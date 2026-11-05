# MediaMTX with AAC to Opus transcoding for WebRTC readers (the aacopus build tag).
#
# The codecs come from FFmpeg (libavcodec) and libopus. Both are built from source,
# FFmpeg with nothing but the AAC decoder, and linked statically, so the result is
# a single static binary like the standard image: no codec libraries to install at
# runtime and no unused codecs to load.

#################################################################
FROM golang:1.26-alpine3.24 AS codecs

ARG FFMPEG_VERSION=8.1.3
ARG FFMPEG_SHA256=7138d28c96d9d3e3af4ee3d8cad72741f8ffb40da90c1112235dea3ecd3178a3
ARG OPUS_VERSION=1.6.1
ARG OPUS_SHA256=6ffcb593207be92584df15b32466ed64bbec99109f007c82205f0194572411a1

RUN apk add --no-cache build-base nasm pkgconf curl xz git

WORKDIR /src

RUN curl -fsSL -o opus.tar.gz "https://downloads.xiph.org/releases/opus/opus-${OPUS_VERSION}.tar.gz" \
	&& echo "${OPUS_SHA256}  opus.tar.gz" | sha256sum -c - \
	&& tar xf opus.tar.gz \
	&& cd "opus-${OPUS_VERSION}" \
	&& ./configure --prefix=/opt/codecs --enable-static --disable-shared \
		--disable-doc --disable-extra-programs \
	&& make -j"$(nproc)" install

RUN curl -fsSL -o ffmpeg.tar.xz "https://ffmpeg.org/releases/ffmpeg-${FFMPEG_VERSION}.tar.xz" \
	&& echo "${FFMPEG_SHA256}  ffmpeg.tar.xz" | sha256sum -c - \
	&& tar xf ffmpeg.tar.xz \
	&& cd "ffmpeg-${FFMPEG_VERSION}" \
	&& ./configure --prefix=/opt/codecs --enable-static --disable-shared \
		--disable-everything --disable-autodetect --disable-programs --disable-doc \
		--disable-network --disable-avdevice --disable-avformat --disable-avfilter \
		--disable-swscale --disable-swresample \
		--enable-decoder=aac \
	&& make -j"$(nproc)" install

#################################################################
FROM codecs AS build

WORKDIR /s

COPY go.mod go.sum ./
RUN go mod download

COPY . ./
RUN go generate ./...

ENV PKG_CONFIG_PATH=/opt/codecs/lib/pkgconfig
RUN CGO_ENABLED=1 go build -tags aacopus -ldflags '-s -w -extldflags "-static"' -o /mediamtx . \
	&& ! ldd /mediamtx 2>/dev/null

#################################################################
FROM scratch

COPY --from=build /mediamtx /
COPY mediamtx.yml LICENSE /

ENTRYPOINT [ "/mediamtx" ]
