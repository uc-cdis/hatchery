# TL;DR

[Galaxy](https://galaxyproject.org/) is a multi-user environment for bioinformatics.

## Configuration

When running Galaxy workspaces we recommend using

```
"env": {"PROXY_PREFIX": "/lw-workspace/proxy/", "GALAXY_CONFIG_SINGLE_USER": "gen3@gen3.org"}
```

to disable authentication as this is a single user container when run with Hatchery.
