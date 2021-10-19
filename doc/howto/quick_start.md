# Quick start for local development

- Create a configuration file at `./hatchery.json` with basic configuration:

```
{
    "user-namespace": "jupyter-pods",
    "sub-dir": "/lw-workspace",
    "user-volume-size": "10Gi"
}
```

- Insteall [nodemon](https://nodemon.io/)

- Run Hatchery:

`export GEN3_ENDPOINT=qa-heal.planx-pla.net; export GEN3_VPCID=qaplanetv1; nodemon --exec go run main.go -config ./hatchery.json --signal SIGTERM`

The API is exposed at http://0.0.0.0:8000.
