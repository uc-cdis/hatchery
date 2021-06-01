# TL;DR

Hatchery has basic support for consuming apps specified in [dockstore docker-compose format](https://docs.dockstore.org/en/develop/getting-started/getting-started-with-services.html).


## Overview

[Dockstore](https://dockstore.org/) is a repository for sharing CWL and WDL workflow definitions as well as definitions (in various formats) for deploying scientific applications packages as docker images.


## Loading a Sample App

To load an application definition into hatchery:

* save the app in the commons manifest folder as `manifests/hatchery/name.yaml` - alongside `hatchery.json`
* add the application's metadata to the `more-configs` block in `hatchery.json`:
```
 jq -r '."more-configs"' < hatchery.json
[
  {
    "type": "dockstore-compose:1.0.0",
    "path": "/hatchery-more-configs/test-app.yaml",
    "name": "DockstoreTest"
  },
  {
    "type": "dockstore-compose:1.0.0",
    "path": "/hatchery-more-configs/notebook-app.yaml",
    "name": "DockstoreNotebook"
  }
]
```


### Example 1 - hello, world!

```
version: '3'
services:
   webapp:
      image: "python:3.8-buster"
      volumes:
         - ${DATA_VOLUME}/data:/usr/local/renci/data
      entrypoint: [ /bin/bash ]
      command:
        - "-c"
        - "cd /tmp && mkdir -p lw-workspace/proxy; echo '<html><body>Hello!</body></html>' > lw-workspace/proxy/index.html; /usr/bin/python3 -m http.server 8000"
      ports:
         - "${SERVICE_PORT}:8000"
      healthcheck:
        test: ["CMD", "curl", "-f", "http://localhost:8000/lw-workspace/proxy/index.html"]
        interval: 1m30s
        timeout: 10s
        retries: 3

   worker:
      image: "ubuntu:18.04"
      # avoid user-volume mount if not necessary
      volumes:
         - ${USER_VOLUME}/config/nginx-with-proxy.conf:/etc/nginx/conf.d/default.conf
      environment:
         - MONGO_URL=mongodb://localhost:27017/ohif
         - APP_CONFIG=/usr/share/nginx/html/app-config.js
      entrypoint: [ /bin/bash ]
      command: [ -c, 'while true; do echo "hello there!"; sleep 10; done;' ]
      deploy:
        resources:
          limits:
            cpus: '0.50'
            memory: 50M
          reservations:
            cpus: '0.25'
            memory: 20M
```


### Example 2 - jupyter notebook

```
version: '3'
user_uid: 1000
fs_gid: 100
services:
   webapp:
      image: "quay.io/occ_data/jupyternotebook:1.7.4"
      volumes:
         - ${DATA_VOLUME}/data:/data
      entrypoint:
        - "start-notebook.sh"
      command:
        - "--NotebookApp.base_url=/lw-workspace/proxy"
        - "--NotebookApp.password=''"
        - "--NotebookApp.token=''"
      ports:
         - "${SERVICE_PORT}:8888"
      healthcheck:
        test: ["CMD", "curl", "-f", "http://localhost:8888/lw-workspace/proxy/"]
        interval: 1m30s
        timeout: 10s
        retries: 3
      deploy:
        resources:
          limits:
            cpus: '0.50'
            memory: 256M
          reservations:
            cpus: '0.25'
            memory: 128M
```

## Idiosynchrasies

### Format limitations

* we assume `entrypoint` and `command` are both lists
* we assume `healthcheck` is a list, and require that the first entry is either `CMD` or `HTTP` where CMD has the normal docker-compose healthcheck CMD semantics, and HTTP emulates the [kubernetes liveness probe](https://kubernetes.io/docs/tasks/configure-pod-container/configure-liveness-readiness-startup-probes/) semantics and treats the second entry as the path to test, and the optional third entry as a port

### Mounting Workspace Volumes

We reserve path prefixes to support mounting user and data (fuse, read-only) data in a container's `volumes` block.

* `${USER_VOLUME}` mounts the per-user persistent storage folder
* `${DATA_VOLUME}` mounts the read-only `gen3-fuse` proxy to the commons objects referenced by the workspace manifest

There is also a possibility to tweak the User ID or filesystem GID to in order to let the user to have correct access permissions to mounted volumes. There are two config fields for this purpose, noted as below. They should be places in the first container's (root service) config YAML file (see `example 2`)

* `user_uid` the UID for the user in this container.
* `fs_gid` the GID for the filesystem mounts.

### Mounting Shared Memory Volume

There is also a reserved path prefixes to support mounting shared memory location in a container's `volumes` block.

* `${SHARED_MEMORY_VOLUME}` mounts the shared memory folder

### Networking

* one service must include a `port` mapping to port `${SERVICE_PORT}` - ex: `${SERVICE_PORT}:8000` - all external traffic is routed to that port
* the URL path of every HTTP request into an app has a prefix of `/lw-workspace/proxy/`
* the containers share the same `localhost` networking space, so two containers cannot
bind the same port, and different containers communicate with each other via `localhost:service-port`

### Container resources

Hatchery deploys an app as a kubernetes pod, so every container runs on the same host node.  The sum of the resources requested by every container in an app may not exceed the resources available on a single worker node.

## Resources

* [dockstore services docs](https://docs.dockstore.org/en/develop/getting-started/getting-started-with-services.html)
* Charles Overbeck's [Services from Dockstore to Gen3](https://docs.google.com/document/d/1JKOVW9Htl6GmgABi8EjsPItbPGurotWE-O88d-ot1ZI/edit#heading=h.d7m4aeu7te27)
* [BioData Catalyst Application Developer Guide](https://docs.google.com/document/d/1o3LdmjilnwtaNGmLML4Qq63pY1FfyMImVWpqRaI5ocE/edit)
* [dockstore services docs](https://docs.dockstore.org/en/develop/getting-started/getting-started-with-services.html)
* [compose-on-kubernetes github](https://github.com/docker/compose-on-kubernetes)
* [docker-compose](https://docs.docker.com/compose/)
