# Backup and Restore for KubeVirt VirtualMachines

This project provides tools to back up and restore KubeVirt VirtualMachines using Restic and MinIO.

## Project Structure

- **accelerated-backup/**: Contains accelerated I/O backup logic and related configurations.
  - `accelerated_io.go`: Core logic for accelerated backups.
  - `example/`: Example YAML files for Kubernetes jobs and configurations.
    - `backup-job.yaml`: Example backup job configuration.
    - `restore-job.yaml`: Example restore job configuration.
    - `lvm.yaml`: Logical Volume Manager configuration.
    - `minio-credentials.yaml`: MinIO credentials for S3 storage.
    - `restic-init-job.yaml`: Restic initialization job.
    - `restic-testing.yaml`: Testing configuration for Restic.

- **bin/**: Contains the compiled binary `restic-backup`.

- **cmd/**: Contains the main entry point for the application (`main.go`).

- **pkg/**: Contains reusable packages for backup, restore, Kubernetes interactions, and utilities.
  - `backup/`: Logic for handling backups.
  - `restore/`: Logic for handling restores.
  - `k8s/`: Kubernetes-specific utilities.
  - `logutil/`: Logging utilities.
  - `manifests/`: Manages Kubernetes manifests.
  - `find/`: Helper functions for finding and managing resources.
  - `vm/`: Logic for backing up and restoring KubeVirt VirtualMachines.

## Usage

### Command-Line Parameters

Common parameters for all modes:
- `-mode`: Operation mode (`find`, `vm-backup`, or `vm-restore`)
- `-namespace`: Kubernetes namespace (default: `backup`)
- `-kubeconfig`: Path to kubeconfig file (optional, uses default kubeconfig if not specified)
- `-awsid`: AWS_ACCESS_KEY_ID for Restic S3 storage
- `-awssecret`: AWS_SECRET_ACCESS_KEY for Restic S3 storage
- `-repository`: RESTIC_REPOSITORY value (e.g., `s3:http://endpoint:port/bucket`)
- `-password`: RESTIC_PASSWORD value

### VM Backup Mode

To back up a KubeVirt VirtualMachine (including all associated PVCs and secrets):

```bash
$ ./bin/restic-backup \
    -kubeconfig <PATH_TO_KUBECONFIG> \
    -awsid <AWS_ACCESS_KEY_ID> \
    -awssecret <AWS_SECRET_ACCESS_KEY> \
    -password <RESTIC_PASSWORD> \
    -repository s3:<S3_ENDPOINT>/<BUCKET_NAME> \
    -mode vm-backup \
    -vm <VM_NAME> \
    -vsc <VOLUME_SNAPSHOT_CLASS> \
    -namespace <NAMESPACE> \
    -backupname <BACKUP_NAME>
```

Example:

```bash
$ ./bin/restic-backup \
    -kubeconfig /home/webber/.kube/cluster-107-155.yaml \
    -awsid minioadmin \
    -awssecret minioadmin \
    -password abc \
    -repository s3:http://10.115.1.120:9000/restic-testing \
    -mode vm-backup \
    -vm vm1 \
    -vsc longhorn-snapshot \
    -namespace backup \
    -backupname vm1-b
```

This will:
- Backup all PVCs attached to the VM
- Backup all secrets referenced by the VM (e.g., cloud-init secrets)
- Save a sanitized VM manifest configuration
- Upload everything to the Restic repository tagged with the backup name

**Notes:** 
- The `-backupname` parameter serves as the unique identifier for this backup and will be used during restore.
- The `-kubeconfig` parameter is optional; if not provided, the default kubeconfig will be used.
- The repository will be automatically initialized if it doesn't exist.

### VM Restore Mode

To restore a KubeVirt VirtualMachine from backup:

```bash
$ ./bin/restic-backup \
    -kubeconfig <PATH_TO_KUBECONFIG> \
    -awsid <AWS_ACCESS_KEY_ID> \
    -awssecret <AWS_SECRET_ACCESS_KEY> \
    -password <RESTIC_PASSWORD> \
    -repository s3:<S3_ENDPOINT>/<BUCKET_NAME> \
    -mode vm-restore \
    -namespace <NAMESPACE> \
    -backupname <BACKUP_NAME> \
    -vm <NEW_VM_NAME>
```

Example:

```bash
$ ./bin/restic-backup \
    -kubeconfig /home/webber/.kube/cluster-107-155.yaml \
    -awsid minioadmin \
    -awssecret minioadmin \
    -password abc \
    -repository s3:http://10.115.1.120:9000/restic-testing \
    -mode vm-restore \
    -namespace backup \
    -backupname vm1-b \
    -vm vm1-restored
```

This will:
- Download the VM backup configuration from Restic using the backup name
- Restore all secrets with new names
- Create new PVCs and restore all volume data
- Update the VM manifest with new PVC and secret references
- Create the VirtualMachine resource

**Notes:** 
- If `-vm` is not specified, the VM will be restored with its original name.
- The `-kubeconfig` parameter is optional; if not provided, the default kubeconfig will be used.
- The repository must be initialized before performing a restore operation.

### Find Mode

To find snapshots by tag:

```bash
$ ./bin/restic-backup \
    -awsid <AWS_ACCESS_KEY_ID> \
    -awssecret <AWS_SECRET_ACCESS_KEY> \
    -password <RESTIC_PASSWORD> \
    -repository s3:<S3_ENDPOINT>/<BUCKET_NAME> \
    -mode find \
    -namespace <NAMESPACE> \
    -snapshot <SNAPSHOT_TAG>
```

**Note:** The repository must be initialized before performing a find operation.

## Example Configurations

Example YAML files for Kubernetes jobs and configurations can be found in the `accelerated-backup/example/` directory. These include:

- `backup-job.yaml`: Defines a Kubernetes job for performing backups.
- `restore-job.yaml`: Defines a Kubernetes job for restoring backups.
- `lvm.yaml`: Configuration for Logical Volume Manager (LVM).
- `minio-credentials.yaml`: Credentials for accessing MinIO S3 storage.
- `restic-init-job.yaml`: Initialization job for Restic.
- `restic-testing.yaml`: Testing configuration for Restic.

## Prerequisites

- Kubernetes cluster with PVCs.
- KubeVirt installed (for VM backup/restore operations).
- MinIO or compatible S3 storage.
- Restic installed and configured.
- VolumeSnapshot CRDs and a VolumeSnapshotClass configured.

**Note:** The Restic repository must be initialized before performing `find` or `vm-restore` operations. The tool will automatically initialize the repository during the first `vm-backup` operation if it doesn't exist.

## Building the Project

To build the project, use the `makefile` in the root directory:

```bash
$ make build
```

The compiled binary will be available in the `bin/` directory.

## Features

- **VM Backup/Restore**: Complete backup and restore of KubeVirt VirtualMachines including:
  - All attached PVCs with data
  - Referenced secrets (cloud-init, SSH keys, etc.)
  - Sanitized VM manifest
- **Accelerated I/O**: Uses optimized block-level I/O for improved performance
- **Snapshot-based**: Leverages Kubernetes VolumeSnapshots for consistent backups
- **Tag-based Organization**: Snapshots are tagged with namespace and snapshot name for easy discovery
- **Progress Tracking**: Real-time progress monitoring for backup/restore operations