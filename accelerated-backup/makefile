# If IMAGE_REPO is not specified via the environment, default it to webberhuang/restic-accelerated.
IMAGE_REPO ?= webberhuang/restic-accelerated
IMAGE_TAG := latest
TARGETARCH ?= amd64

# All Linux architectures supported (Docker only supports Linux containers)
LINUX_ARCHS := amd64 arm64

.PHONY: all build push build-all-arch push-all-arch push-manifest release

all:
	@echo "Available targets:"
	@echo "  build            - Build single-arch Docker image for $(TARGETARCH) with tag $(IMAGE_REPO):$(IMAGE_TAG)"
	@echo "  push             - Build and push single-arch Docker image"
	@echo "  build-all-arch   - Build Docker images for all architectures: $(LINUX_ARCHS)"
	@echo "  push-all-arch    - Build and push Docker images for all architectures: $(LINUX_ARCHS)"
	@echo "  push-manifest    - Build, push images and create multi-arch manifest (same tag for all architectures)"
	@echo "  release          - Build, push and create multi-arch manifest (recommended)"
	@echo ""
	@echo "Supported architectures (Linux only):"
	@echo "  - linux/amd64"
	@echo "  - linux/arm64"
	@echo ""
	@echo "Note: The restic-backup binaries must be pre-built in ../bin/ directory"

# Single-architecture build
build:
	@echo "Building Docker image for $(TARGETARCH)..."
	@echo "Checking for pre-built binary: ../bin/restic-backup-linux-$(TARGETARCH)"
	@if [ ! -f ../bin/restic-backup-linux-$(TARGETARCH) ]; then \
		echo "❌ Error: Binary ../bin/restic-backup-linux-$(TARGETARCH) not found!"; \
		echo "Please run 'make build-all-platforms' from the root directory first."; \
		exit 1; \
	fi
	docker build --build-arg TARGETARCH=$(TARGETARCH) -t $(IMAGE_REPO):$(IMAGE_TAG) -f Dockerfile ..

# Single-architecture push
push: build
	docker push $(IMAGE_REPO):$(IMAGE_TAG)

# Build all architectures separately
build-all-arch:
	@echo "Building Docker images for all architectures: $(LINUX_ARCHS)"
	@for arch in $(LINUX_ARCHS); do \
		echo ""; \
		echo "========================================"; \
		echo "Building Docker image for linux/$$arch..."; \
		echo "========================================"; \
		echo "Checking for pre-built binary: ../bin/restic-backup-linux-$$arch"; \
		if [ ! -f ../bin/restic-backup-linux-$$arch ]; then \
			echo "❌ Error: Binary ../bin/restic-backup-linux-$$arch not found!"; \
			echo "Please run 'make build-all-platforms' from the root directory first."; \
			exit 1; \
		fi; \
		docker build --build-arg TARGETARCH=$$arch -t $(IMAGE_REPO):$(IMAGE_TAG)-$$arch -f Dockerfile .. || exit 1; \
	done
	@echo ""
	@echo "✅ Build complete for all architectures!"
	@for arch in $(LINUX_ARCHS); do \
		echo "  - $(IMAGE_REPO):$(IMAGE_TAG)-$$arch"; \
	done

# Build and push all architectures separately
push-all-arch: build-all-arch
	@echo ""
	@echo "Pushing Docker images for all architectures: $(LINUX_ARCHS)"
	@for arch in $(LINUX_ARCHS); do \
		echo ""; \
		echo "========================================"; \
		echo "Pushing Docker image for linux/$$arch..."; \
		echo "========================================"; \
		docker push $(IMAGE_REPO):$(IMAGE_TAG)-$$arch || exit 1; \
	done
	@echo ""
	@echo "✅ Push complete for all architectures!"
	@for arch in $(LINUX_ARCHS); do \
		echo "  - $(IMAGE_REPO):$(IMAGE_TAG)-$$arch"; \
	done

# Create and push multi-arch manifest
push-manifest: push-all-arch
	@echo ""
	@echo "========================================";
	@echo "Creating multi-arch manifest for $(IMAGE_REPO):$(IMAGE_TAG)..."
	@echo "========================================";
	@# Build the manifest create command dynamically
	@MANIFEST_CMD="docker manifest create $(IMAGE_REPO):$(IMAGE_TAG)"; \
	for arch in $(LINUX_ARCHS); do \
		MANIFEST_CMD="$$MANIFEST_CMD --amend $(IMAGE_REPO):$(IMAGE_TAG)-$$arch"; \
	done; \
	eval $$MANIFEST_CMD
	@docker manifest push $(IMAGE_REPO):$(IMAGE_TAG)
	@echo ""
	@echo "✅ Multi-arch manifest pushed successfully!"
	@echo "You can now pull $(IMAGE_REPO):$(IMAGE_TAG) on any of these architectures:"
	@for arch in $(LINUX_ARCHS); do \
		echo "  - linux/$$arch"; \
	done

# Recommended: Build, push and create multi-arch manifest
release: push-manifest
	@echo ""
	@echo "========================================";
	@echo "🎉 Release complete: $(IMAGE_REPO):$(IMAGE_TAG)"
	@echo "========================================";
	@echo "Available architectures:"
	@for arch in $(LINUX_ARCHS); do \
		echo "  - linux/$$arch"; \
	done
	@echo ""
	@echo "Each Docker image contains:"
	@echo "  - restic-backup binary (architecture-specific)"
	@echo "  - accelerated_io binary (architecture-specific)"
	@echo "  - restic binary (architecture-specific)"
	@echo ""
	@echo "Pull the image with: docker pull $(IMAGE_REPO):$(IMAGE_TAG)"
