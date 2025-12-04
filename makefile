# Updated Makefile for k8s-blk-pvc-backup-restore project

# Name of the binary to build
APP_NAME := restic-backup

# Directory where the final binary will be placed
BIN_DIR := bin

# Directory containing the main package source (main.go)
MAIN_DIR := cmd

# Variables for Docker image repository and tag
DOCKER_REPO ?= webberhuang/restic-accelerated
DOCKER_TAG ?= latest

# All supported architectures (Linux only for Docker compatibility)
LINUX_ARCHS := amd64 arm64

# Default target
all: build

# Build the Go binary by explicitly specifying the main source file.
build:
	@echo "=> Building $(APP_NAME)..."
	@mkdir -p $(BIN_DIR)
	@go build -o $(BIN_DIR)/$(APP_NAME) $(MAIN_DIR)/main.go

# Build binaries for all Linux architectures
build-all-platforms:
	@echo "=> Building binaries for Linux architectures: $(LINUX_ARCHS)"
	@mkdir -p $(BIN_DIR)
	@for arch in $(LINUX_ARCHS); do \
		echo ""; \
		echo "Building for Linux $$arch..."; \
		GOOS=linux GOARCH=$$arch go build -o $(BIN_DIR)/$(APP_NAME)-linux-$$arch $(MAIN_DIR)/main.go; \
		echo "✅ Built: $(BIN_DIR)/$(APP_NAME)-linux-$$arch"; \
	done
	@echo ""
	@echo "========================================"
	@echo "✅ All binaries built successfully!"
	@echo "========================================"
	@ls -lh $(BIN_DIR)

# Build Docker images for all architectures (includes binaries)
build-docker-all: build-all-platforms
	@echo "=> Building Docker images for all Linux architectures..."
	@cd accelerated-backup && $(MAKE) build-all-arch IMAGE_REPO=$(DOCKER_REPO) IMAGE_TAG=$(DOCKER_TAG)

# Push Docker images for all architectures and create manifest
push-docker-all: build-all-platforms
	@echo "=> Pushing Docker images for all Linux architectures..."
	@cd accelerated-backup && $(MAKE) push-manifest IMAGE_REPO=$(DOCKER_REPO) IMAGE_TAG=$(DOCKER_TAG)

# Release: Build all binaries and Docker images
release: push-docker-all
	@echo ""
	@echo "========================================"
	@echo "🎉 Release complete!"
	@echo "========================================"
	@echo ""
	@echo "📦 Standalone binaries built:"
	@for arch in $(LINUX_ARCHS); do \
		echo "  - $(BIN_DIR)/$(APP_NAME)-linux-$$arch"; \
	done
	@echo ""
	@echo "🐳 Docker images pushed:"
	@echo "  - $(DOCKER_REPO):$(DOCKER_TAG) (multi-arch: linux/amd64, linux/arm64)"

# Run go mod tidy to ensure dependencies are in sync.
mod-tidy:
	@echo "=> Tidying go.mod..."
	@go mod tidy

# Clean up generated files.
clean:
	@echo "=> Cleaning build artifacts..."
	@rm -rf $(BIN_DIR)

# Run tests for all packages.
test:
	@echo "=> Running tests..."
	@go test ./pkg/... ./accelerated-backup/... ./cmd/... -v

# Run the binary from the bin directory.
# Example usage: `make run ARGS="--mode=backup --pvc=foo"`
run:
	@echo "=> Running $(APP_NAME) with args: $(ARGS)"
	@$(BIN_DIR)/$(APP_NAME) $(ARGS)

# Build Docker image for accelerated-backup (single arch)
build-docker:
	@echo "=> Building Docker image for accelerated-backup..."
	@docker build -t $(DOCKER_REPO):$(DOCKER_TAG) ./accelerated-backup

# Push Docker image to repository (single arch)
push-docker:
	@echo "=> Pushing Docker image to repository $(DOCKER_REPO):$(DOCKER_TAG)..."
	@docker push $(DOCKER_REPO):$(DOCKER_TAG)

# Show help message for common targets.
help:
	@echo "Common targets:"
	@echo "  make build                - Build the binary for current platform"
	@echo "  make build-all-platforms  - Build binaries for all Linux architectures (amd64, arm64)"
	@echo "  make build-docker-all     - Build Docker images for all Linux architectures"
	@echo "  make push-docker-all      - Build and push Docker images with multi-arch manifest"
	@echo "  make release              - Build all binaries and push Docker images (recommended)"
	@echo "  make run                  - Run the binary (pass ARGS=\"...\" for flags)"
	@echo "  make clean                - Remove build artifacts"
	@echo "  make mod-tidy             - Tidy go.mod/go.sum"
	@echo "  make test                 - Run tests"
	@echo "  make build-docker         - Build Docker image for accelerated-backup (single arch)"
	@echo "  make push-docker          - Push Docker image to repository (single arch)"

.PHONY: all build build-all-platforms build-docker-all push-docker-all release mod-tidy clean test run build-docker push-docker help
