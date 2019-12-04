# Hatchery

Hatchery creates Kubernetes Pods for workspace services. Workspace services must expose HTTP servers. Ambassador is used to proxy user traffic through to their container workspace once it is launched by Hatchery.

## API Documentation

[OpenAPI documentation available here.](http://petstore.swagger.io/?url=https://raw.githubusercontent.com/uc-cdis/hatchery/master/openapis/openapi.yaml)

YAML file for the OpenAPI documentation is found in the `openapis` folder (in
the root directory); see the README in that folder for more details.

## Build and Test

See the [Dockerfile](./Dockerfile):

```
(
  go build -o bin/hatchery
  go test -v ./hatchery/
)
```

## Configuration

Hatchery is configured via the manifest file for the commons you are running. Hatchery contains two levels of configuration, one for the Hatchery service itself, and then an additional layer for each workspace.

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
      "user-volume-location": "/home/jovyan/pd"
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
    * `ready-probe` the path to use for the Kubernetes readiness probe.
    * `user-uid` the UID for the user in this container.
    * `fs-gid` the GID for the filesystem mounts.
    * `user-volume-location` the location where the user persistant storage should be mounted in this container.
    * `lifecycle-pre-stop` a string array as the container prestop command.
    * `lifecycle-post-start` a string array as the container poststart command.


### Configuration for R Studio

When running R Studio workspaces we recommend using

```
"env": {"DISABLE_AUTH": "true"}
```

to disable authentication as this is a single user container when run with Hatchery.

### Configuration for Jupyter Noteboboks

When running Jupyter Notebook workspaces we recommend using

```
"args": ["--NotebookApp.base_url=/lw-workspace/proxy/","--NotebookApp.password=''","--NotebookApp.token=''"],
"command": ["start-notebook.sh"],
"path-rewrite": "/lw-workspace/proxy/",
"ready-probe": "/lw-workspace/proxy/",
"user-uid": 1000,
"fs-gid": 100
```

to disable authentication as this is a single user container when run with Hatchery. This also sets the paths to be correct as well as the user IDs.

### Configuration for Galaxy

When running Galaxy workspaces we recommend using

```
"env": {"PROXY_PREFIX": "/lw-workspace/proxy/", "GALAXY_CONFIG_SINGLE_USER": "gen3@gen3.org"}
```

to disable authentication as this is a single user container when run with Hatchery.