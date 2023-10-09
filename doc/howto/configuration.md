
# TL;DR

Hatchery is configured via the manifest file for the commons you are running. Hatchery contains two levels of configuration, one for the Hatchery service itself, and then an additional layer for each workspace.

## Example

An example manifest entry may look like

```
  "hatchery": {
    "user-namespace": "jupyter-pods",
    "sub-dir": "/lw-workspace",
    "sidecar": {
      "cpu-limit": "1.0",
      "memory-limit": "256Mi",
      "image": "quay.io/cdis/gen3fuse-sidecar:0.1.2",
      "env": {"NAMESPACE":"phillis", "HOSTNAME": "phillis.planx-pla.net"},
      "args": [],
      "command": ["su", "-c", "/home/jovyan/sidecarDockerrun.sh", "-s", "/bin/sh", "jovyan"],
      "lifecycle-pre-stop": ["su", "-c", "cd /data; for f in *; do fusermount -u $f; rm -rf $f; done", "-s", "/bin/sh", "jovyan"]
    },
    "containers": [{
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
      "nextflow": {
          "enabled": true,
          "job-image-whitelist": [
            "quay.io/cdis/*:*"
          ],
          "s3-bucket-whitelist": [
            "ngi-igenomes"
          ],
          "instance-ami": "ami-03392f075059ae3ba",
          "instance-type": "SPOT",
          "instance-min-vcpus": 0,
          "instance-max-vcpus": 9
      }
    }]
  }
```

* `user-namespace` is which namespace the pods will be deployed into.
* `sub-dir` is the path to Hatchery off the host domain, i.e. if the full domain path is `https://nci-crdc-demo.datacommons.io/lw-workspace` then `sub-dir` is `/lw-workspace`.
* `user-volume-size` the size of the user volume to be created. Applies to all containers because the user storage is the same across all of them.
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
    * `nextflow` is for configuration specific to Nextflow containers.
      * `enabled` is false by default; if true, automatically create AWS resources required to run Nextflow workflows in AWS Batch - see the [Nextflow workspaces](/doc/explanation/nextflow.md) documentation for more details.
      * `job-image-whitelist` are the only images that are allowed as Nextflow workflow containers. It supports wildcards `?` for a single character and `*` for multiple characters. Warning: setting the whitelist as an empty list allows all images!
      * `s3-bucket-whitelist` are public buckets that Nextflow jobs are allowed to get data objects from. Access to actions "s3:GetObject" and "s3:ListBucket" for `arn:aws:s3:::<bucket>` and `arn:aws:s3:::<bucket>/*` will be granted.
      * `instance-ami`, `instance-type` ("EC2", "SPOT", "FARGATE" or "FARGATE_SPOT"), `instance-min-vcpus` and `instance-max-vcpus` are AWS Batch Compute Environment settings.
