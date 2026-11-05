GHCR_REPOSITORY = ghcr.io/srizan10/hctvmtx

ghcr:
	$(eval VERSION := $(shell git rev-parse --short HEAD))

	echo $(GHCR_TOKEN) | docker login ghcr.io -u $(GHCR_ACTOR) --password-stdin

	docker buildx rm builder 2>/dev/null || true
	docker buildx create --name=builder

	docker build --builder=builder \
	-f docker/ffmpeg-rpi.Dockerfile . \
	--platform=linux/arm/v6,linux/arm/v7,linux/arm64 \
	-t $(GHCR_REPOSITORY):$(VERSION)-ffmpeg-rpi \
	-t $(GHCR_REPOSITORY):latest-ffmpeg-rpi \
	--push

	docker build --builder=builder \
	-f docker/rpi.Dockerfile . \
	--platform=linux/arm/v6,linux/arm/v7,linux/arm64 \
	-t $(GHCR_REPOSITORY):$(VERSION)-rpi \
	-t $(GHCR_REPOSITORY):latest-rpi \
	--push

	docker build --builder=builder \
	-f docker/ffmpeg.Dockerfile . \
	--platform=linux/amd64,linux/arm/v6,linux/arm/v7,linux/arm64 \
	-t $(GHCR_REPOSITORY):$(VERSION)-ffmpeg \
	-t $(GHCR_REPOSITORY):latest-ffmpeg \
	--push

	docker build --builder=builder \
	-f docker/standard.Dockerfile . \
	--platform=linux/amd64,linux/arm/v6,linux/arm/v7,linux/arm64 \
	-t $(GHCR_REPOSITORY):$(VERSION) \
	-t $(GHCR_REPOSITORY):latest \
	--push

	docker buildx rm builder

# MediaMTX with AAC to Opus transcoding for WebRTC (cgo, see docker/transcode.Dockerfile).
# amd64 only: building the codecs and cgo under emulation for other platforms takes too long.
ghcr-transcode:
	$(eval VERSION := $(shell git rev-parse --short HEAD))

	echo $(GHCR_TOKEN) | docker login ghcr.io -u $(GHCR_ACTOR) --password-stdin

	docker build -f docker/transcode.Dockerfile . \
	--platform=linux/amd64 \
	-t $(GHCR_REPOSITORY):$(VERSION)-transcode \
	-t $(GHCR_REPOSITORY):latest-transcode

	docker push $(GHCR_REPOSITORY):$(VERSION)-transcode
	docker push $(GHCR_REPOSITORY):latest-transcode
