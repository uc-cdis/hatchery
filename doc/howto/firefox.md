# TL;DR

Docker container for [Firefox](https://github.com/uc-cdis/docker-firefox)

## Configuration for Firefox

For the Firefox and noVNC solution, add the following boolean field to mount a shared memory volume.

```
"use-shared-memory": "true"
```

## Example

```
{
      "target-port": 5800,
      "cpu-limit": "1.0",
      "memory-limit": "1024Mi",
      "name": "Test VNC App with Shared Mem",
      "image": "quay.io/cdis/docker-firefox:master",
      "env": {
        "LANDING_URL": "http://localhost:8787/"
      },
      "args": [],
      "path-rewrite": "/",
      "use-tls": "false",
      "ready-probe": "/",
      "use-shared-memory": "true",
      "friends": [
        {
          "name": "rstudio",
          "image": "heliumdatastage/rstudio-server:1",
          "env": [
            {
              "name": "DISABLE_AUTH",
              "value": "true"
            }
          ],
          "resources": {
            "limits": {
              "cpu": "1.0",
              "memory": "512Mi"
            }
          },
          "ports": [
            {
              "containerPort": 8787
            }
          ]
        }
      ]
    }
```
