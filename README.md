# Backup and Restore for Harvester VirtualMachines

This project provides tools to back up and restore **Harvester VirtualMachines** using Restic with S3-compatible storage backends.

> **Note:** This tool is specifically designed for Harvester VMs, not generic KubeVirt VMs. It currently only supports **block mode PVCs** (volumeMode: Block).

## Key Features

- **Storage Provider Agnostic**: Works with any S3-compatible storage backend (MinIO, AWS S3, Ceph RGW, etc.)
- **CSI Driver Agnostic**: Supports any Kubernetes CSI driver with snapshot capabilities
- **VM Backup/Restore**: Complete backup and restore of Harvester VirtualMachines including:
  - All attached PVCs with data (block mode only)
  - Referenced secrets (cloud-init, SSH keys, etc.)
  - Sanitized VM manifest
- **Accelerated I/O**: Uses optimized block-level I/O for improved performance
- **Snapshot-based**: Leverages Kubernetes VolumeSnapshots for consistent backups
- **Tag-based Organization**: Snapshots are tagged with namespace and snapshot name for easy discovery
- **Progress Tracking**: Real-time progress monitoring for backup/restore operations

## Project Structure

- **accelerated-backup/**: Contains accelerated I/O backup logic and related configurations.
  - `accelerated_io.go`: Core logic for accelerated backups.
  - `example/`: Example YAML files for Kubernetes jobs and configurations.
    - `backup-job.yaml`: Example backup job configuration.
    - `restore-job.yaml`: Example restore job configuration.
    - `lvm.yaml`: Logical Volume Manager configuration.
    - `minio-credentials.yaml`: Example S3 credentials (for MinIO or any S3-compatible storage).
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
- `-mode`: Operation mode (`find`, `vm-backup`, `vm-restore`, or `cleanup`)
- `-namespace`: Kubernetes namespace (default: `backup`)
- `-kubeconfig`: Path to kubeconfig file (optional, uses default kubeconfig if not specified)
- `-awsid`: AWS_ACCESS_KEY_ID for S3-compatible storage (access key)
- `-awssecret`: AWS_SECRET_ACCESS_KEY for S3-compatible storage (secret key)
- `-repository`: RESTIC_REPOSITORY value (e.g., `s3:http://endpoint:port/bucket` or `s3:s3.amazonaws.com/bucket`)
- `-password`: RESTIC_PASSWORD value

### VM Backup Mode

To back up a Harvester VirtualMachine (including all associated PVCs and secrets):

> **Important:** Only block mode PVCs (volumeMode: Block) are currently supported. Filesystem mode PVCs are not supported at this time.

```bash
$ ./bin/restic-backup \
    -kubeconfig <PATH_TO_KUBECONFIG> \
    -awsid <S3_ACCESS_KEY_ID> \
    -awssecret <S3_SECRET_ACCESS_KEY> \
    -password <RESTIC_PASSWORD> \
    -repository s3:<S3_ENDPOINT>/<BUCKET_NAME> \
    -mode vm-backup \
    -vm <VM_NAME> \
    -vsc <CSI_DRIVER_TO_VSC_MAPPING> \
    -namespace <NAMESPACE> \
    -backupname <BACKUP_NAME>
```

Example (using MinIO):

```bash
$ ./bin/restic-backup \
    -kubeconfig /home/webber/.kube/cluster-107-155.yaml \
    -awsid minioadmin \
    -awssecret minioadmin \
    -password abc \
    -repository s3:http://10.115.1.120:9000/restic-testing \
    -mode vm-backup \
    -vm vm1 \
    -vsc "driver.longhorn.io=longhorn-snapshot,nfs.csi.k8s.io=csi-nfs-snapclass" \
    -namespace backup \
    -backupname vm1-b
```

Example (using AWS S3):

```bash
$ ./bin/restic-backup \
    -kubeconfig /home/webber/.kube/cluster.yaml \
    -awsid AKIAIOSFODNN7EXAMPLE \
    -awssecret wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY \
    -password abc \
    -repository s3:s3.amazonaws.com/my-backup-bucket \
    -mode vm-backup \
    -vm vm1 \
    -vsc "ebs.csi.aws.com=ebs-snapclass" \
    -namespace backup \
    -backupname vm1-b
```

This will:
- Automatically detect the CSI driver for each PVC attached to the VM
- Select the appropriate VolumeSnapshotClass based on the CSI driver mapping
- Backup all PVCs attached to the VM
- Backup all secrets referenced by the VM (e.g., cloud-init secrets)
- Save a sanitized VM manifest configuration
- Upload everything to the S3-compatible storage backend via Restic, tagged with the backup name

**Notes:** 
- The `-vsc` parameter specifies a mapping between CSI drivers and VolumeSnapshotClass names in the format: `driver1=class1,driver2=class2`
- The tool will automatically detect the CSI driver for each PVC by checking:
  1. The PersistentVolume's CSI driver field (most accurate)
  2. The PVC's `volume.kubernetes.io/storage-provisioner` annotation
  3. The StorageClass name (fallback)
- If a PVC uses a CSI driver not in the mapping, the backup will fail with a clear error message
- The `-backupname` parameter serves as the unique identifier for this backup and will be used during restore.
- The `-kubeconfig` parameter is optional; if not provided, the default kubeconfig will be used.
- The repository will be automatically initialized if it doesn't exist.

**Common CSI Driver Names:**
- Longhorn: `driver.longhorn.io`
- NFS CSI: `nfs.csi.k8s.io`
- Ceph RBD: `rbd.csi.ceph.com`
- AWS EBS: `ebs.csi.aws.com`
- Azure Disk: `disk.csi.azure.com`
- GCE PD: `pd.csi.storage.gke.io`

### VM Restore Mode

To restore a Harvester VirtualMachine from backup:

```bash
$ ./bin/restic-backup \
    -kubeconfig <PATH_TO_KUBECONFIG> \
    -awsid <S3_ACCESS_KEY_ID> \
    -awssecret <S3_SECRET_ACCESS_KEY> \
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
- Download the VM backup configuration from the S3-compatible storage via Restic using the backup name
- Restore all secrets with new names
- Create new PVCs and restore all volume data
- Update the VM manifest with new PVC and secret references
- Create the VirtualMachine resource

**Notes:** 
- If `-vm` is not specified, the VM will be restored with its original name.
- The `-kubeconfig` parameter is optional; if not provided, the default kubeconfig will be used.
- The repository must be initialized before performing a restore operation.

### Find Mode

The find mode supports two usage patterns:

#### 1. Find Detailed Backup Information

To retrieve detailed information about a specific backup (including VM config and all PVC backups):

```bash
$ ./bin/restic-backup \
    -awsid <S3_ACCESS_KEY_ID> \
    -awssecret <S3_SECRET_ACCESS_KEY> \
    -password <RESTIC_PASSWORD> \
    -repository s3:<S3_ENDPOINT>/<BUCKET_NAME> \
    -mode find \
    -namespace <NAMESPACE> \
    -backupname <BACKUP_NAME>
```

Example:

```bash
$ ./bin/restic-backup \
    -awsid minioadmin \
    -awssecret minioadmin \
    -password abc \
    -repository s3:http://10.115.1.120:9000/restic-testing \
    -mode find \
    -namespace backup \
    -backupname vm1-b
```

This will display:
- Backup name and namespace
- Backup timestamp
- Total backup size
- VM configuration details (snapshot ID, size, tags)
- All PVC backups with individual details (PVC name, snapshot ID, size, tags)

Example output:
```
✅ Backup Information:
📦 Backup Name: vm1-b
📁 Namespace: backup
🕐 Backup Time: 2025-12-02 14:30:45
💾 Total Size: 2048.50 MB

🖥️  VM Configuration:
   Snapshot ID: a1b2c3d4
   Time: 2025-12-02 14:30:45
   Size: 1.25 MB
   Tags: [ns=backup sn=vm1-b type=vm-config]

💿 PVC Backups (2):
   [1] PVC Name: vm1-disk-0
       Snapshot ID: e5f6g7h8
       Time: 2025-12-02 14:28:30
       Size: 1024.00 MB
       Tags: [ns=backup sn=vm1-b-pvc-vm1-disk-0]

   [2] PVC Name: vm1-disk-1
       Snapshot ID: i9j0k1l2
       Time: 2025-12-02 14:29:15
       Size: 1024.25 MB
       Tags: [ns=backup sn=vm1-b-pvc-vm1-disk-1]
```

#### 2. List All Snapshots or Filter by Tags

To list all snapshots in the repository or filter by specific tags:

```bash
$ ./bin/restic-backup \
    -awsid <S3_ACCESS_KEY_ID> \
    -awssecret <S3_SECRET_ACCESS_KEY> \
    -password <RESTIC_PASSWORD> \
    -repository s3:<S3_ENDPOINT>/<BUCKET_NAME> \
    -mode find \
    -namespace <NAMESPACE> \
    [-tag <TAG1>] [-tag <TAG2>] ...
```

Example (list all snapshots):

```bash
$ ./bin/restic-backup \
    -awsid minioadmin \
    -awssecret minioadmin \
    -password abc \
    -repository s3:http://10.115.1.120:9000/restic-testing \
    -mode find \
    -namespace backup
```

Example (filter by tags):

```bash
$ ./bin/restic-backup \
    -awsid minioadmin \
    -awssecret minioadmin \
    -password abc \
    -repository s3:http://10.115.1.120:9000/restic-testing \
    -mode find \
    -namespace backup \
    -tag type=vm-config
```

**Notes:** 
- When `-backupname` is specified, the tool displays detailed information about that specific backup.
- When `-backupname` is not specified, the tool lists all snapshots (optionally filtered by `-tag`).
- The `-tag` flag can be specified multiple times to filter by multiple tags.
- The repository must be initialized before performing a find operation.

### Cleanup Mode

To remove all resources created by a VM backup:

```bash
$ ./bin/restic-backup \
    -kubeconfig <PATH_TO_KUBECONFIG> \
    -awsid <S3_ACCESS_KEY_ID> \
    -awssecret <S3_SECRET_ACCESS_KEY> \
    -password <RESTIC_PASSWORD> \
    -repository s3:<S3_ENDPOINT>/<BUCKET_NAME> \
    -mode cleanup \
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
    -mode cleanup \
    -namespace backup \
    -backupname vm1-b
```

This will:
- Download the backup configuration to identify all resources
- Delete all PVC snapshots from Restic (tagged with `{backupName}-pvc-{pvcName}`)
- Delete the VM configuration snapshot from Restic (tagged with `type=vm-config`)
- Delete the local backup configuration file (`{backupName}.cfg`)

**Notes:** 
- The cleanup mode removes all backup data from Restic and cannot be undone.
- The `-kubeconfig` parameter is optional; if not provided, the default kubeconfig will be used.
- The repository must be initialized before performing a cleanup operation.

## Example Configurations

Example YAML files for Kubernetes jobs and configurations can be found in the `accelerated-backup/example/` directory. These include:

- `backup-job.yaml`: Defines a Kubernetes job for performing backups.
- `restore-job.yaml`: Defines a Kubernetes job for restoring backups.
- `lvm.yaml`: Configuration for Logical Volume Manager (LVM).
- `minio-credentials.yaml`: Example credentials for S3-compatible storage (works with MinIO, AWS S3, etc.).
- `restic-init-job.yaml`: Initialization job for Restic.
- `restic-testing.yaml`: Testing configuration for Restic.

## Prerequisites

- Kubernetes cluster with PVCs.
- KubeVirt installed (for VM backup/restore operations).
- Harvester cluster with VirtualMachines
- Block mode PVCs (volumeMode: Block) - filesystem mode PVCs are not currently supported
- S3-compatible storage backend (e.g., MinIO, AWS S3, Ceph RGW, Wasabi, Backblaze B2, etc.).
- Restic installed and configured.
- VolumeSnapshot CRDs and VolumeSnapshotClass(es) configured for your CSI driver(s).

**Note:** The Restic repository must be initialized before performing `find` or `vm-restore` operations. The tool will automatically initialize the repository during the first `vm-backup` operation if it doesn't exist.

## Building the Project

To build the project, use the `makefile` in the root directory:

```bash
$ make build
```

The compiled binary will be available in the `bin/` directory.

## Features

- **VM Backup/Restore**: Complete backup and restore of Harvester VirtualMachines including:
  - All attached PVCs with data (block mode only)
  - Referenced secrets (cloud-init, SSH keys, etc.)
  - Sanitized VM manifest
- **Accelerated I/O**: Uses optimized block-level I/O for improved performance
- **Snapshot-based**: Leverages Kubernetes VolumeSnapshots for consistent backups
- **Tag-based Organization**: Snapshots are tagged with namespace and snapshot name for easy discovery
- **Progress Tracking**: Real-time progress monitoring for backup/restore operations