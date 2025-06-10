# TL;DR

Docker container for [noNVC Firefox](https://github.com/uc-cdis/docker-firefox)

## Configuration for noVNC Firefox

For the noVNC Firefox solution, we need to mount a shared memory volume for the Firefox container.

Add the following config to the main container's manifest

```
"use-shared-memory": "true"
```

List the Firefox container as one of the friend containers in the `friends` array, and add the following configs into it

```
"volumeMounts": [{
  "name": "dshm",
  "mountPath": "/dev/shm"
}],
"securityContext": {
  "privileged": true
},
"ports": [{
  "containerPort": 5800
}]
```

In the main container's manifest, specify

```
"target-port": 5800
```

## Example

```
{
  "target-port": 5800,
  "cpu-limit": "1.0",
  "memory-limit": "512Mi",
  "name": "Jupyter Notebook Bio Python (Lab Edition) thru noVNC",
  "image": "quay.io/cdis/jupyter-notebook:1.0.2",
  "env": {
    "NAMESPACE": "qa-mickey"
  },
  "args": [
    "--NotebookApp.base_url=/",
    "--NotebookApp.default_url=/lab",
    "--NotebookApp.password=''",
    "--NotebookApp.token=''"
  ],
  "command": [
    "start-notebook.sh"
  ],
  "path-rewrite": "/",
  "use-tls": "false",
  "ready-probe": "/",
  "lifecycle-post-start": [
    "/bin/sh",
    "-c",
    "export IAM=`whoami`; rm -rf /home/$IAM/pd/dockerHome; ln -s $(pwd) /home/$IAM/pd/dockerHome; mkdir -p /home/$IAM/.jupyter/custom; echo \"define(['base/js/namespace'], function(Jupyter){Jupyter._target = '_self';})\" >/home/$IAM/.jupyter/custom/custom.js; ln -s /data /home/$IAM/; true"
  ],
  "user-volume-location": "/home/jovyan/pd",
  "gen3-volume-location": "/home/jovyan/.gen3"
  "use-shared-memory": "true",
  "friends": [
    {
      "name": "firefox",
      "image": "quay.io/cdis/docker-firefox:v1.13.0_ctds.1.0.0",
      "volumeMounts": [{
        "name": "dshm",
        "mountPath": "/dev/shm"
      }],
      "securityContext": {
        "privileged": true
      },
      "env": [
        {
          "name": "LANDING_URL",
          "value": "http://localhost:8888/"
        }
      ],
      "resources": {
        "limits": {
          "cpu": "1.0",
          "memory": "3Gi"
        }
      },
      "ports": [{
        "containerPort": 5800
      }]
    }
  ]
}
```

See more examples in [qa-mickey hatchery config](https://github.com/uc-cdis/gitops-qa/blob/master/qa-mickey.planx-pla.net/manifests/hatchery/hatchery.json)
