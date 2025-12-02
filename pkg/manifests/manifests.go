package manifests

// MinioCredentials (optional). This secret uses fixed naming.
const MinioCredentials = `
apiVersion: v1
kind: Secret
metadata:
  name: minio-credentials
  namespace: {{NAMESPACE}}
type: Opaque
stringData:
  AWS_ACCESS_KEY_ID: "minioadmin"
  AWS_SECRET_ACCESS_KEY: "minioadmin"
  RESTIC_REPOSITORY: "s3:http://192.188.0.56:9000/restic-testing"
  RESTIC_PASSWORD: "abc"
`

// ResticCheckJob checks if the repository is initialized.
const ResticCheckJob = `
apiVersion: batch/v1
kind: Job
metadata:
  name: {{NAME}}
  namespace: {{NAMESPACE}}
spec:
  backoffLimit: 0
  ttlSecondsAfterFinished: 30
  template:
    spec:
      restartPolicy: Never
      containers:
      - name: restic-check
        image: webberhuang/restic-accelerated:latest
        imagePullPolicy: IfNotPresent
        command: ["/bin/sh", "-c"]
        args:
          - export AWS_ACCESS_KEY_ID={{AWS_ACCESS_KEY_ID}} && export AWS_SECRET_ACCESS_KEY={{AWS_SECRET_ACCESS_KEY}} && export RESTIC_REPOSITORY={{RESTIC_REPOSITORY}} && export RESTIC_PASSWORD={{RESTIC_PASSWORD}} && restic snapshots > /dev/null 2>&1
`

// ResticInitJob initializes the repository.
const ResticInitJob = `
apiVersion: batch/v1
kind: Job
metadata:
  name: {{NAME}}
  namespace: {{NAMESPACE}}
spec:
  backoffLimit: 0
  ttlSecondsAfterFinished: 30
  template:
    spec:
      restartPolicy: Never
      containers:
      - name: restic-init
        image: webberhuang/restic-accelerated:latest
        imagePullPolicy: IfNotPresent
        command: ["/bin/sh", "-c"]
        args:
          - export AWS_ACCESS_KEY_ID={{AWS_ACCESS_KEY_ID}} && export AWS_SECRET_ACCESS_KEY={{AWS_SECRET_ACCESS_KEY}} && export RESTIC_REPOSITORY={{RESTIC_REPOSITORY}} && export RESTIC_PASSWORD={{RESTIC_PASSWORD}} && restic init
`

// VolumeSnapshot creates a snapshot from a given PVC.
const VolumeSnapshot = `
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshot
metadata:
  name: {{NAME}}
  namespace: {{NAMESPACE}}
spec:
  volumeSnapshotClassName: {{VOLUME_SNAPSHOT_CLASSNAME}}
  source:
    persistentVolumeClaimName: {{PVC_NAME}}
`

// PVCClone creates a new PVC from a VolumeSnapshot.
const PVCClone = `
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: {{NAME}}
  namespace: {{NAMESPACE}}
spec:
  accessModes:
  - ReadWriteOnce
  volumeMode: {{VOLUME_MODE}}
  storageClassName: {{STORAGE_CLASS}}
  resources:
    requests:
      storage: {{STORAGE_SIZE}}
  dataSource:
    name: {{VOLUME_SNAPSHOT_NAME}}
    kind: VolumeSnapshot
    apiGroup: snapshot.storage.k8s.io
`

// BackupJob defines the backup job.
const BackupJob = `
apiVersion: batch/v1
kind: Job
metadata:
  name: {{NAME}}
  namespace: {{NAMESPACE}}
spec:
  backoffLimit: 0
  ttlSecondsAfterFinished: 30
  template:
    spec:
      restartPolicy: Never
      containers:
      - name: backup
        image: webberhuang/restic-accelerated:latest
        imagePullPolicy: IfNotPresent
        command: ["/bin/sh", "-c"]
        args:
          - export AWS_ACCESS_KEY_ID={{AWS_ACCESS_KEY_ID}} && export AWS_SECRET_ACCESS_KEY={{AWS_SECRET_ACCESS_KEY}} && export RESTIC_REPOSITORY={{RESTIC_REPOSITORY}} && export RESTIC_PASSWORD={{RESTIC_PASSWORD}} && /usr/local/bin/accelerated_io -device /dev/{{PVC_NAME}} -mode=read | restic -q backup --stdin --stdin-filename {{PV_NAME}} --tag=ns={{NAMESPACE}},sn={{SNAPSHOT_NAME}}
        volumeDevices:
        - name: vol1
          devicePath: /dev/{{PVC_NAME}}
      volumes:
      - name: vol1
        persistentVolumeClaim:
          claimName: {{PVC_NAME}}
`

// RestoreJob defines the restore job.
const RestoreJob = `
apiVersion: batch/v1
kind: Job
metadata:
  name: {{NAME}}
  namespace: {{NAMESPACE}}
spec:
  backoffLimit: 0
  ttlSecondsAfterFinished: 30
  template:
    spec:
      restartPolicy: Never
      containers:
      - name: restore
        image: webberhuang/restic-accelerated:latest
        imagePullPolicy: IfNotPresent
        command: ["/bin/sh", "-c"]
        args:
          - export AWS_ACCESS_KEY_ID={{AWS_ACCESS_KEY_ID}} && export AWS_SECRET_ACCESS_KEY={{AWS_SECRET_ACCESS_KEY}} && export RESTIC_REPOSITORY={{RESTIC_REPOSITORY}} && export RESTIC_PASSWORD={{RESTIC_PASSWORD}} && restic -v=2 dump {{SNAPSHOT_ID}} {{PV_NAME}} | /usr/local/bin/accelerated_io -device /dev/{{PVC_NAME}} -mode=write
        volumeDevices:
        - name: vol2
          devicePath: /dev/{{PVC_NAME}}
      volumes:
      - name: vol2
        persistentVolumeClaim:
          claimName: {{PVC_NAME}}
`

// FindJob defines the job to execute "restic snapshots" with optional tag filtering and JSON output.
const FindJob = `
apiVersion: batch/v1
kind: Job
metadata:
  name: {{NAME}}
  namespace: {{NAMESPACE}}
spec:
  backoffLimit: 0
  ttlSecondsAfterFinished: 60
  template:
    spec:
      restartPolicy: Never
      containers:
      - name: find
        image: webberhuang/restic-accelerated:latest
        imagePullPolicy: IfNotPresent
        command: ["/bin/sh", "-c"]
        args:
          - |
            export AWS_ACCESS_KEY_ID={{AWS_ACCESS_KEY_ID}}
            export AWS_SECRET_ACCESS_KEY={{AWS_SECRET_ACCESS_KEY}}
            export RESTIC_REPOSITORY={{RESTIC_REPOSITORY}}
            export RESTIC_PASSWORD={{RESTIC_PASSWORD}}
            if [ -n "{{TAG_FILTER}}" ]; then
              restic snapshots --tag={{TAG_FILTER}} --json
            else
              restic snapshots --json
            fi
`
