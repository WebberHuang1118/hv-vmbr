# If IMAGE_REPO is not specified via the environment, default it to webberhuang/restic-accelerated.
IMAGE_REPO ?= webberhuang/restic-accelerated
IMAGE_TAG := latest
TARGETARCH ?= amd64

.PHONY: all build push release

all:
	@echo "Available targets:"
	@echo "  build   - Build the Docker image with tag $(IMAGE_REPO):$(IMAGE_TAG)"
	@echo "  push    - Push the Docker image to Docker Hub"
	@echo "  release - Build and push the Docker image"

build:
	docker build --build-arg TARGETARCH=$(TARGETARCH) -t $(IMAGE_REPO):$(IMAGE_TAG) .

push: build
	docker push $(IMAGE_REPO):$(IMAGE_TAG)

release: build push
	@echo "Release complete: $(IMAGE_REPO):$(IMAGE_TAG)"
