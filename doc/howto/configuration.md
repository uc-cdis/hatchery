
# TL;DR

Hatchery is configured via the manifest file for the commons you are running. Hatchery contains two levels of configuration, one for the Hatchery service itself, and then an additional layer for each workspace.

## Example

An example manifest entry may look like

```
  "hatchery": {
    "user-namespace": "jupyter-pods",
    "sub-dir": "/lw-workspace",
    "user-volume-size": "10Gi",
    "prisma": {
      "enable": true,
      "console-address": ""
    },
    "pay-models-dynamodb-table": "dynamodb-table-name",
    "default-pay-model": {
      "workspace_type": "Trial Workspace",
      "local": true
    },
    "sidecar": {
      "cpu-limit": "1.0",
      "memory-limit": "256Mi",
      "image": "quay.io/cdis/gen3fuse-sidecar:0.1.2",
      "env": {"NAMESPACE":"phillis", "HOSTNAME": "phillis.planx-pla.net"},
      "args": [],
      "command": ["su", "-c", "/home/jovyan/sidecarDockerrun.sh", "-s", "/bin/sh", "jovyan"],
      "lifecycle-pre-stop": ["su", "-c", "cd /data; for f in *; do fusermount -u $f; rm -rf $f; done", "-s", "/bin/sh", "jovyan"]
    },
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
* `user-volume-size` the size of the user volume to be created. Applies to all containers because the user storage is the same across all of them.
* `prisma`: TODO document
* `pay-models-dynamodb-table` is the name of the DynamoDB table where Hatchery can get users' pay model information
* `default-pay-model` is the pay model to fall back to when a user does not have a pay model set up in the `pay-models-dynamodb-table` table
* `sidecar` is the sidecar container launched in the same pod as each workspace container. In Gen3 this is used for the FUSE mount system to the manifests that the user has loaded in.
    * `cpu-limit` the CPU limit for the container matching Kubernetes resource spec.
    * `memory-limit` the memory limit for the container matching Kubernetes resource spec.
    * `image` the sidecar image path with tag.
    * `env` a dictionary of additional environment variables to pass to the container.
    * `args` the arguments to pass to the container.
    * `command` a string array as the command to run in the container overriding the default.
    * `lifecycle-pre-stop` a string array as the container prestop command.
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
* `more-configs`: see https://github.com/uc-cdis/hatchery/blob/master/doc/explanation/dockstore.md
