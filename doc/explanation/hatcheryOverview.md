# TL;DR

Hatchery is a Gen3 service that launches web enabled tools onto a kubernetes cluster on a per-user basis.

## Architecture

```
reverse proxy - ambassador - user apps
      \ hatchery
```

TODO: make a diagram or steel one from Fantix

## Security

### VM Isolation

Jupyter node pool.


### Network policies

Metadata service and kubernetes API access denied.
Namespaces.
Identifying pod labels.

## User Data

EBS storage

## Gen3 Integration

* Workspace token service - authenticates users based on labels on the hatchery pods, and grants access tokens on request
* Fuse sidecar - implements user land file system that transparently exposes commons object store objects as files by managing the URL signing process under the hood
* Fence - for signing URL's
* Manifest service - manifest managment
* Portal integration
* Indexd - for resolving manifest entries

TODO - abstract underlying services from workspace.  Applications interact with the commons primarily through its public endpoint.

