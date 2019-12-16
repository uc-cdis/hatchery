# TL;DR

Hatchery provides explicit configuration for the
[gen3-fuse](https://github.com/uc-cdis/gen3-fuse/) sidecar.

## Overview

The fuse sidecar exposes commons objects as files via the magic of a user space file system driver that transparently manages interactions with manifests, indexd, fence, and signed URL's.

## Configuration

ex:
```
$ jq -r .sidecar < hatchery.json 
{
  "cpu-limit": "0.8",
  "memory-limit": "256Mi",
  "image": "quay.io/cdis/gen3fuse-sidecar:0.1.3-mickey",
  "env": {
    "NAMESPACE": "default",
    "HOSTNAME": "vpodc.org"
  },
  "args": [],
  "command": [
    "/bin/bash",
    "/sidecarDockerrun.sh"
  ],
  "lifecycle-pre-stop": [
    "su",
    "-c",
    "echo test",
    "-s",
    "/bin/sh",
    "root"
  ]
}

```
