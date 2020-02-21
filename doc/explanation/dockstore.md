# TL;DR

Hatchery has basic support for consuming apps specified in [dockstore docker-compose format](https://docs.dockstore.org/en/develop/getting-started/getting-started-with-services.html).

## Overview

[Dockstore](https://dockstore.org/) is a repository for sharing CWL and WDL workflow definitions as well as definitions (in various formats) for deploying scientific applications packages as docker images.

## Limiting Assumptions

* the compose service with a port 80 mapping is the one published to the load balancer
* volume mappings `source:destination` recognize 2 source volumes
    - `user-volume:` persists across reboots
    - `data-volume:` is the gen3-fuse volume
* we assume `entrypoint` and `command` are both lists
* we assume `healthcheck` is a list, and require that the first entry is either `CMD` or `HTTP` where CMD has the normal docker-compose healthcheck CMD semantics, and HTTP emulates the [kubernetes liveness probe](https://kubernetes.io/docs/tasks/configure-pod-container/configure-liveness-readiness-startup-probes/) semantics and treats the second entry as the path to test, and the optional third entry as a port

## Resources

* [dockstore services docs](https://docs.dockstore.org/en/develop/getting-started/getting-started-with-services.html)
* [compose-on-kubernetes github](https://github.com/docker/compose-on-kubernetes)