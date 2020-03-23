# TL;DR

Hatchery has the capacity to consume applications specified in a [docker-compose](https://docs.docker.com/compose/) like
yaml file - possibly copied from [Dockstore](https://dockstore.org).

## Loading a Sample App

To load the following application definition into hatchery:

* save the app in the commons manifest folder as `mannifests/hatchery/test-app.yaml` - alongside `hatchery.json`
* add the application's metadata to the `more-configs` block in `hatchery.json`:
```
 jq -r '."more-configs"' < hatchery.json 
[
  {
    "type": "dockstore-compose:1.0.0",
    "path": "/hatchery-more-configs/test-app.yaml",
    "name": "DockstoreTest"
  }
]
```


```
version: '3'
services:
   webapp:
      image: "python:3.8-buster"
      volumes:
         - data-volume/data:/usr/local/renci/data
      entrypoint: [ /bin/bash ]
      command:
        - "-c"
        - "cd /tmp; echo '<html><body>Hello!</body></html>' > index.html; /usr/bin/python3 -m http.server 8000"
      ports: 
         - 8000:80
      healthcheck:
        test: ["CMD", "curl", "-f", "http://localhost:8000"]
        interval: 1m30s
        timeout: 10s
        retries: 3
        start_period: 40s

   worker:
      image: "ubuntu:18.04"
      # avoid user-volume mount if not necessary
      volumes:
         - user-volume/config/nginx-with-proxy.conf:/etc/nginx/conf.d/default.conf
      environment:
         - MONGO_URL=mongodb://localhost:27017/ohif
         - APP_CONFIG=/usr/share/nginx/html/app-config.js
      entrypoint: [ /bin/bash ]
      command: [ -c, 'while true; do echo "hello there!"; sleep 10; done;' ]
      deploy:
        resources:
          limits:
            cpus: '0.50'
            memory: 50M
          reservations:
            cpus: '0.25'
            memory: 20M
```

## Mounting Workspace Volumes

We use reserved path prefixes to support mounting user and data (fuse) data in a container's `volumes` block.

* `user-volume/` mounts the per-user persistent storage folder
* `data-volume/` mounts the read-only `gen3-fuse` proxy to the commons objects referenced by the workspace manifest


## Resources

* [dockstore services docs](https://docs.dockstore.org/en/develop/getting-started/getting-started-with-services.html)
* Charles Overbeck's [Services from Dockstore to Gen3](https://docs.google.com/document/d/1JKOVW9Htl6GmgABi8EjsPItbPGurotWE-O88d-ot1ZI/edit#heading=h.d7m4aeu7te27)
* [BioData Catalyst Application Developer Guide](https://docs.google.com/document/d/1o3LdmjilnwtaNGmLML4Qq63pY1FfyMImVWpqRaI5ocE/edit)
