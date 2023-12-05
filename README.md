# Hatchery

Hatchery creates Kubernetes Pods for workspace services. Workspace services must expose HTTP servers. Ambassador is used to proxy user traffic through to their container workspace once it is launched by Hatchery.

## Documentation

### Explanation
* [Hatchery overview](doc/explanation/hatcheryOverview.md)
* [API documentation](http://petstore.swagger.io/?url=https://raw.githubusercontent.com/uc-cdis/hatchery/master/doc/openapi.yaml)
* [Configuring Dockstore apps](doc/explanation/dockstore.md)

### How-to
* [Hatchery configuration](doc/howto/configuration.md)
* [Quick start for local development](doc/howto/quick_start.md)
* [Run the tests locally](doc/howto/devTest.md)
* [Configuring gen3-fuse](doc/howto/fuseSidecar.md)
* Configuring workspaces:
    * [Jupyter](doc/howto/jupyterNotebook.md)
    * [RStudio](doc/howto/rStudio.md)
    * [Galaxy](doc/howto/galaxy.md)
    * [noNVC Firefox](doc/howto/noVNCFirefox.md)
