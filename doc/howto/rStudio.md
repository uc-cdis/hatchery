# TL;DR

R Studio is an Rlang notebook environment

## Configuration for R Studio

When running R Studio workspaces we recommend using

```
"env": {"DISABLE_AUTH": "true"}
```

to disable authentication as this is a single user container when run with Hatchery.

## Example

```
{
      "target-port": 8787,
      "cpu-limit": "1.0",
      "memory-limit": "1024Mi",
      "name": "R Studio",
      "image": "quay.io/cdis/rstudio:master",
      "env": {"DISABLE_AUTH": "true"},
      "args": [],
      "path-rewrite": "/",
      "use-tls": "false",
      "ready-probe": "/",
      "user-volume-location": "/home/rstudio/pd",
      "gen3-volume-location": "/home/jovyan/.gen3"
      "fs-gid": 100
    }
```
