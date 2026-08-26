
# TL;DR

Hatchery is configured via the manifest file for the commons you are running. Hatchery contains two levels of configuration, one for the Hatchery service itself, and then an additional layer for each workspace.

## Example

An example manifest entry may look like

```
  "hatchery": {
    "user-namespace": "jupyter-pods",
    "sub-dir": "/lw-workspace",
    "user-volume-size": "10Gi",
    "hashed-usernames": false,
    "use-internal-services-url": false
    "prisma": {
      "enable": true,
      "console-address": ""
    },
    "pay-models-dynamodb-table": "dynamodb-table-name",
    "pay-models-dynamodb-arn": "arn:aws:iam::12345:role/other-role"
    "default-pay-model": {
      "workspace_type": "Trial Workspace",
      "local": true
    },
    "license-user-maps-dynamodb-table": "gen3-license-user-maps",
    "license-user-maps-global-seconday-index": "activeUsersIndex",
    "sidecar": {
      "cpu-limit": "1.0",
      "memory-limit": "256Mi",
      "image": "quay.io/cdis/gen3fuse-sidecar:0.1.2",
      "env": {"NAMESPACE":"phillis", "HOSTNAME": "phillis.planx-pla.net"},
      "args": [],
      "command": ["su", "-c", "/home/jovyan/sidecarDockerrun.sh", "-s", "/bin/sh", "jovyan"],
      "lifecycle-pre-stop": ["su", "-c", "cd /data; for f in *; do fusermount -u $f; rm -rf $f; done", "-s", "/bin/sh", "jovyan"]
    },
    "nextflow-global": {
      "s3-objects-expiration-days": 30,
      "sample-config-public-image": "",
      "imagebuilder-reader-role-arn": ""
    },
    "s3-config": {
      "bucketName": "workspace-software-s3-qa-gen3",
      "region": "us-east-1",
      "prefixBase": ""
    },
    "oidc-provider-arn": "arn:aws:iam::123456789012:oidc-provider/oidc.eks.us-east-1.amazonaws.com/id/EXAMPLED539D4633E53DE1B716D3041E",
    "containers": [
      {
        "target-port": 8888,
        "cpu-limit": "1.0",
        "memory-limit": "512Mi",
        "name": "Jupyter Notebook Bio Python",
        "image": "quay.io/occ_data/jupyternotebook:1.7.2",
        "env": {},
        "args": ["--NotebookApp.base_url=/lw-workspace/proxy/","--NotebookApp.password=''","--NotebookApp.token=''"],
        "command": ["start-notebook.sh"],
        "path-rewrite": "/lw-workspace/proxy/",
        "use-tls": "false",
        "ready-probe": "/lw-workspace/proxy/",
        "user-uid": 1000,
        "fs-gid": 100,
        "user-volume-location": "/home/jovyan/pd",
        "gen3-volume-location": "/home/jovyan/.gen3",
        "friends": [],
        "squashfs_mount": {
            "enabled": false,
            "source_sqsh": "/image/apps-current.sqsh",
            "expected_sha256": "",
            "mounter_image": "quay.io/cdis/ecs-ws-sidecar:master",
            "cache_size_limit": "20Gi",
            "pvc_claim_name": "software-library-pvc",
            "bucket_name": "",
            "region": "",
            "bucket_prefix": ""
        },
        "authz": {
            "version": 0.1,
            "or": [
                {"resource_paths": ["/workspace/jupyter-container"]},
                {"pay_models": ["Direct Pay", "None"]}
            ]
        },
        "nextflow": {
            "enabled": true,
            "job-image-whitelist": [
              "quay.io/cdis/*:*",
              "1234.ecr.aws/nextflow-approved/{{username}}:*"
            ],
            "s3-bucket-whitelist": [
              "ngi-igenomes"
            ],
            "compute-environment-type": "SPOT",
            "instance-ami": "ami-03392f075059ae3ba",
            "instance-ami-builder-arn": "arn:aws:imagebuilder:<region>:<account id>:image-pipeline/<pipeline name>",
            "instance-type": "optimal",
            "instance-min-vcpus": 0,
            "instance-max-vcpus": 9
        }
      },
      {
        "target-port": 8888,
        "cpu-limit": "1.0",
        "memory-limit": "2Gi",
        "name": "(Generic, Limited Gen3-licensed) Stata Notebook",
        "image": "quay.io/cdis/jupyter-pystata-gen3-licensed:master",
        "env": {
          "FRAME_ANCESTORS": "https://dev.planx-pla.net"
        },
        "license": {
          "enabled": true,
          "license-type": "STATA",
          "max-license-ids": 6,
          "g3auto-name": "license-g3auto",
          "g3auto-key": "license.txt",
          "file-path": "licence-path.txt",
          "workspace-flavor": "gen3-licensed"
        },
        "args": []
      }
    ],
    "more-configs": [
      {
        "type": "dockstore-compose:1.0.0",
        "path": "/hatchery-more-configs/test-app.yaml",
        "name": "Dockstore App"
      }
    ]
  }
```

* `user-namespace` is which namespace the pods will be deployed into.
* `sub-dir` is the path to Hatchery off the host domain, i.e. if the full domain path is `https://nci-crdc-demo.datacommons.io/lw-workspace` then `sub-dir` is `/lw-workspace`.
* `hashed-usernames` if set to `true`, Hatchery hashes usernames before using them to generate Kubernetes resource names and related label/selector values, including pods, services, Ambassador mappings, and PVCs. This is useful when usernames may be too long or contain characters that are not safe for Kubernetes metadata, such as long OIDC `sub` values.
  **Warning:** changing this setting changes the generated Kubernetes resource names for users. Existing PVCs created under the previous naming scheme will not be reused automatically. Enabling this setting will cause new PVCs to be created for users, so previous persistent workspace drives must be migrated manually if they need to be preserved.
* `user-volume-size` the size of the user volume to be created. Applies to all containers because the user storage is the same across all of them.
* `use-internal-services-url` Use internal service URLs (http://fence-service/ and http://ambassador-service/) for communication with other services instead of using GEN3_ENDPOINT environmental variable
* `skip-node-selector` if set to `true`, will not set a node selector for the pods, which will be scheduled on any node. Useful for single-node clusters.
* `prisma`: TODO document
* `pay-models-dynamodb-table` is the name of the DynamoDB table where Hatchery can get users' pay model information
* `pay-models-dynamodb-arn` specify a cross-account role if the DynamoDB table is stored in another AWS account
* `default-pay-model` is the pay model to fall back to when a user does not have a pay model set up in the `pay-models-dynamodb-table` table
* `license-user-maps-dynamodb-table` is the optional table name if using dynamodb for managing user sessions of gen3-licensed workspaces.
* `license-user-maps-global-seconday-index` the global secondary index for active users in the license-user-maps table.
* `sidecar` is the sidecar container launched in the same pod as each workspace container. In Gen3 this is used for the FUSE mount system to the manifests that the user has loaded in.
    * `cpu-limit` the CPU limit for the container matching Kubernetes resource spec.
    * `memory-limit` the memory limit for the container matching Kubernetes resource spec.
    * `image` the sidecar image path with tag.
    * `env` a dictionary of additional environment variables to pass to the container.
    * `args` the arguments to pass to the container.
    * `command` a string array as the command to run in the container overriding the default.
    * `lifecycle-pre-stop` a string array as the container prestop command.
* `nextflow-global` is for global configuration specific to Nextflow containers.
    * `s3-objects-expiration-days` (int, default 30): objects created in S3 by Nextflow are deleted after the specified number of days.
    * `sample-config-public-image`: a publicly-accessible image that any user can pull to test Nextflow workflows. Will be mentioned in the auto-generated sample configuration and documentation when a user launches a Nextflow workspace.
    * `imagebuilder-reader-role-arn`: see the [nextflow-global.imagebuilder-reader-role-arn section](/doc/explanation/nextflow.md#nextflow-globalimagebuilder-reader-role-arn) of the Nextflow workspaces documentation.
* `containers` is the list of workspaces available to be run by this instance of Hatchery. Each container must be a single image and expose a web server.
    * `target-port` specifies the port that the container is exposing the webserver on.
    * `cpu-limit` the CPU limit for the container matching Kubernetes resource spec.
    * `memory-limit` the memory limit for the container matching Kubernetes resource spec.
    * `name` the display name for the workspace.
    * `image` the container image path with tag.
    * `env` a dictionary of additional environment variables to pass to the container.
    * `args` the arguments to pass to the container.
    * `command` a string array as the command to run in the container overriding the default.
    * `path-rewrite` the `rewrite` flag to be added as an annotation for Ambassador.
    * `use-tls` the `tls` flag to be added as an annotation for Ambassador.
    * `use-shared-memory` a boolean flag to mount a shared memory volume (for FireFox and noVNC)
    * `ready-probe` the path to use for the Kubernetes readiness probe.
    * `user-uid` the UID for the user in this container.
    * `fs-gid` the GID for the filesystem mounts.
    * `user-volume-location` the location where the user persistent storage should be mounted in this container.
    * `gen3-volume-location` the location where the user's API key file will be put into
    * `lifecycle-pre-stop` a string array as the container prestop command.
    * `lifecycle-post-start` a string array as the container poststart command.
    * `friends` is a list of kubernetes containers to deploy alongside the main container and the sidecar in the kubernetes pod.
    * `authz` describes access rules for this container. See the [Authorization documentation](/doc/explanation/authorization.md) for more details.
    * `nextflow` is for configuration specific to Nextflow containers. See the [Nextflow workspaces documentation](/doc/explanation/nextflow.md) for more details.
      * `enabled` is false by default; if true, automatically create AWS resources required to run Nextflow workflows in AWS Batch.
      * `job-image-whitelist` are the only images that are allowed as Nextflow workflow containers.
        * Supports wildcards `?` for a single character and `*` for multiple characters.
        * `{{username}}` can be used as a placeholder for the user's actual (escaped) username.
        * **Warning:** setting the whitelist as an empty list allows all images!
        * **Warning:** on the ECR side, tags are ignored and users are allowed access to the whole repo.
      * `s3-bucket-whitelist` are public buckets that Nextflow jobs are allowed to get data objects from. Access to actions "s3:GetObject" and "s3:ListBucket" for `arn:aws:s3:::<bucket>` and `arn:aws:s3:::<bucket>/*` will be granted.
      * `compute-environment-type` ("EC2", "SPOT", "FARGATE" or "FARGATE_SPOT"), `instance-ami`, `instance-type` ("optimal", "g4dn.xlarge"...), `instance-min-vcpus` and `instance-max-vcpus` are AWS Batch Compute Environment settings.
      * `instance-ami-builder-arn` is the ARN of an AWS image builder pipeline. The latest AMI built by this pipeline will be used. If `instance-ami` is specified, it overrides `instance-ami-builder-arn`.
    * `license` is for configuration specific to any gen3-licensed containers.
      * `enabled` set to `true` to enable management of license and user-sessions.
      * `license-type` name of the license type, eg `"STATA"`.
      * `max-license-ids` integer for maximum users of the license, eg `6`.
      * `g3auto-name` g3auto secret with license.
      * `g3auto-key` g3auto key for the secret, eg `"license_file.txt"`.
      * `file-path` container file-path where license should be copied.
      * `workspace-flavor` description of type of gen3-licensed container.
    * `squashfs_mount` mounts a shared, read-only software library into the workspace from a SquashFS image stored in S3. A privileged `apps-mounter` sidecar copies the `.sqsh` file to a local cache, loop-mounts it, and shares the mount with the workspace container at `/apps`.
      * `enabled` is false by default; if true, add the sidecar and the supporting volumes to this container's pod.
      * `source_sqsh` the path to the SquashFS file as seen by the sidecar (default `/image/apps-current.sqsh`). `/image` is the mounted S3 location, so this path is relative to `bucket_prefix` when one is set.
      * `expected_sha256` the SHA-256 digest of the SquashFS file. Strongly recommended: when it is empty, the sidecar logs a warning and skips digest verification.
      * `mounter_image` the sidecar image path with tag (default `quay.io/cdis/ecs-ws-sidecar:master`).
      * `cache_size_limit` the size limit of the local cache volume holding the copied SquashFS file (default `20Gi`). Must exceed the size of the image, or the pod is evicted mid-copy.
      * `pvc_claim_name` the name of the PersistentVolumeClaim providing the SquashFS file (default `software-library-pvc`). Hatchery creates this claim and its PersistentVolume on demand if they do not already exist, so the claim does not need to be provisioned ahead of time. The claim is shared by all workspace pods in `user-namespace`.
      * `bucket_name` and `region` optionally override the top-level `s3-config` values for this container. The region is used as the Mountpoint-S3 `stsRegion`, defaulting to `us-east-1`.
      * `bucket_prefix` the optional prefix ("directory") within the bucket that contains the SquashFS file, e.g. `"software-library/"`. It becomes the root of `/image` in the sidecar, so do not repeat it in `source_sqsh`.
      * **Note:** the bucket, the object, and the [Mountpoint for S3 CSI driver](https://github.com/awslabs/mountpoint-s3-csi-driver) must all already exist; Hatchery only creates the Kubernetes PV/PVC that reference them. A misconfiguration here does not fail the launch request: the PV/PVC are created successfully and the pod then fails to start, so check the pod events with `kubectl describe pod` to diagnose.
      * The volume is mounted with the pod's own credentials, so Hatchery automatically creates a per-user IAM role and an IRSA-annotated ServiceAccount granting read access to the bucket. This requires the top-level `oidc-provider-arn` to be set, and requires Hatchery's own AWS credentials to allow `iam:GetRole`, `iam:CreateRole` and `iam:PutRolePolicy`. The same role and ServiceAccount are shared with the `shared-workspace` feature, so the two can be enabled together.
* `s3-config` describes the S3 bucket used for mounting data into workspace pods, and provides the default bucket for any container using `squashfs_mount`.
    * `bucketName` the name of an existing S3 bucket, e.g. `"workspace-software-s3-qa-gen3"`.
    * `region` the region the bucket is in, e.g. `"us-east-1"`.
    * `prefixBase` an optional prefix within the bucket; defaults to `"<userName>/"` when empty.
* `oidc-provider-arn` the full ARN of the EKS cluster's OIDC provider, e.g. `"arn:aws:iam::123456789012:oidc-provider/oidc.eks.us-east-1.amazonaws.com/id/EXAMPLED539D4633E53DE1B716D3041E"`. Required by any feature that mounts S3 with the pod's own credentials (`squashfs_mount` and `shared-workspace`), because it is used to build the IRSA trust policy. For backward compatibility, `shared-workspace.oidc-provider-arn` is still honored when this is unset, but new configurations should set it here: one cluster has one OIDC provider, and both features share it. See [Finding the OIDC provider ARN](#finding-the-oidc-provider-arn) below.
* `more-configs`: see https://github.com/uc-cdis/hatchery/blob/master/doc/explanation/dockstore.md


## Finding the OIDC provider ARN

The `oidc-provider-arn` value is an IAM OIDC provider ARN built from the EKS
cluster's OIDC issuer URL. The ARN is the issuer URL with the `https://` prefix
removed, prefixed with `arn:aws:iam::<account-id>:oidc-provider/`.

The most reliable way to get it is to derive it from the cluster, since an
account usually has one provider per cluster and the ARNs are otherwise
indistinguishable:

```bash
CLUSTER=my-cluster   # e.g. devplanetv2

ISSUER=$(aws eks describe-cluster --name "$CLUSTER" \
  --query 'cluster.identity.oidc.issuer' --output text)
ACCOUNT=$(aws sts get-caller-identity --query Account --output text)

echo "arn:aws:iam::${ACCOUNT}:oidc-provider/${ISSUER#https://}"
```

Confirm the provider is actually registered in IAM — the command above only
builds a string, and a cluster can have an issuer URL with no corresponding IAM
provider, in which case IRSA silently fails to work:

```bash
aws iam get-open-id-connect-provider \
  --open-id-connect-provider-arn "<the ARN printed above>"
```

If that returns `NoSuchEntity`, the provider has not been created for the
cluster yet; see the [AWS IRSA setup documentation](https://docs.aws.amazon.com/eks/latest/userguide/enable-iam-roles-for-service-accounts.html).

To list the providers that do exist in the account:

```bash
aws iam list-open-id-connect-providers
```

If you do not have IAM read permissions but do have cluster access, the issuer
can also be read directly from the cluster, then combined with the account ID as
above:

```bash
kubectl get --raw /.well-known/openid-configuration | jq -r .issuer
```

**Note:** the ARN must be for the same AWS account the workspace pods run in,
because Hatchery creates the per-user IAM role in whatever account its own
credentials resolve to. A trust policy that references an OIDC provider in a
different account produces a role that cannot be assumed, and the mount fails
with a confusing error rather than a clear permissions one.

## Deployment

For production deployments the configuration json should be located at `/var/hatchery/hatchery.json`.

For local development the configuration json should be located in the hatchery source code project directory. The executable, `main.go`, would be invoked with the `-dev` argument, as described in the [quick_start.md](quick_start.md) document.
