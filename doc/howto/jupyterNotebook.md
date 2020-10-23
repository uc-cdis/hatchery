# TL;DR

Jupyter notebook and jupyter lab are python notebook environments.  Note that CTDS maintains several jupyter Docker images in https://github.com/uc-cdis/containers/


## Jupyter Noteboboks

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

## Jupyter Lab

Jupyter lab is the successor to jupyter notebook.

## Example

```
{
      "target-port": 8888,
      "cpu-limit": "1.0",
      "memory-limit": "1024Mi",
      "name": "Jupyter Notebook Bio Python- Make your own",
      "image": "quay.io/occ_data/jupyternotebook:fix_niaid-test",
      "env": {"FRAME_ANCESTORS": "https://aids.niaiddata.org https://tb.niaiddata.org https://microbiome.niaiddata.org https://flu.niaiddata.org"},
      "args": ["--NotebookApp.base_url=/lw-workspace/proxy/","--NotebookApp.password=''","--NotebookApp.token=''"],
      "command": ["start-notebook.sh"],
      "path-rewrite": "/lw-workspace/proxy/",
      "use-tls": "false",
      "ready-probe": "/lw-workspace/proxy/",
      "lifecycle-post-start": ["/bin/sh","-c","export IAM=`whoami`; rm -rf /home/$IAM/pd/dockerHome; ln -s $(pwd) /home/$IAM/pd/dockerHome; mkdir -p /home/$IAM/.jupyter/custom; echo \"define(['base/js/namespace'], function(Jupyter){Jupyter._target = '_self';})\" >/home/$IAM/.jupyter/custom/custom.js; ln -s /data /home/$IAM/pd/; true"],
      "user-uid": 1000,
      "fs-gid": 100,
      "user-volume-location": "/home/jovyan/pd"
    },
  ```
