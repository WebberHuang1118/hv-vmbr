# Multi-architecture Docker build
# The restic-backup binary must be pre-built before running docker build

# Stage 1: Build the accelerated_io binary
FROM golang:1.21 as accelerated_io-builder

# Use build arguments to support multi-architecture builds
ARG TARGETARCH=amd64

WORKDIR /app
COPY accelerated-backup/accelerated_io.go .
RUN go mod init accelerated_io && go mod tidy

# Build for the target architecture
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -o /accelerated_io accelerated_io.go

# Stage 2: Create the final minimal image
FROM debian:bullseye

# Use build arguments to support multi-architecture builds
ARG TARGETARCH=amd64
ARG RESTIC_VERSION=0.17.3
ENV TARGETARCH=${TARGETARCH}

# Install necessary dependencies
RUN apt-get update && apt-get install -y ca-certificates wget bzip2 && rm -rf /var/lib/apt/lists/*

# Download and install Restic binary from the official release
RUN echo "TARGETARCH is: ${TARGETARCH}" && \
    case "$TARGETARCH" in \
      "amd64") ARCH="amd64" ;; \
      "arm64") ARCH="arm64" ;; \
      *) echo "Unsupported architecture: $TARGETARCH"; exit 1 ;; \
    esac && \
    echo "Downloading from: https://github.com/restic/restic/releases/download/v${RESTIC_VERSION}/restic_${RESTIC_VERSION}_linux_${ARCH}.bz2" && \
    wget -q -O restic.bz2 "https://github.com/restic/restic/releases/download/v${RESTIC_VERSION}/restic_${RESTIC_VERSION}_linux_${ARCH}.bz2" && \
    bzip2 -d restic.bz2 && \
    chmod +x restic && \
    mv restic /usr/local/bin/restic

# Copy the accelerated_io binary from the builder stage
COPY --from=accelerated_io-builder /accelerated_io /usr/local/bin/accelerated_io
RUN chmod +x /usr/local/bin/accelerated_io

# Copy the pre-built restic-backup binary for the target architecture
COPY bin/restic-backup-linux-${TARGETARCH} /usr/local/bin/restic-backup
RUN chmod +x /usr/local/bin/restic-backup

CMD ["/bin/sh"]
