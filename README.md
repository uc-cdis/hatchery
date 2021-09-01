# Hatchery

Hatchery creates Kubernetes Pods for workspace services. Workspace services must expose HTTP servers. Ambassador is used to proxy user traffic through to their container workspace once it is launched by Hatchery.

## Explanation
* [hatchery overview](doc/explanation/hatcheryOverview.md)
* [api](doc/explanation/hatcheryApi.md)
* [dockstore apps](doc/explanation/dockstore.md)

## How-to
* [dev-test](doc/howto/devTest.md)
* [gen3-fuse](doc/howto/fuseSidecar.md)
* [Jupyter Notebook](doc/howto/jupyterNotebook.md)
* [R Studio](doc/howto/rStudio.md)
* [Galaxy](doc/howto/galaxy.md)


## Tutorials

For this tutorial, we will be setting up a Microk8s based Kubernetes cluster,
on a single machine. Hatchery will be used to launch containers into a namespace
called `hatch-test`.

###Setting up Microk8s environment for testing

If you need a quick K8s environment for testing, Microk8s can be easily installed
on an Ubuntu based machine:
```
sudo snap install microk8s --classic --channel=1.21
```
If you are working on a machine with a small root disk that has a larger mounted
disk (like some virtual machines), you can remap the microk8s data directory with the command
```
sudo mount --bind /mnt/microk8s /var/snap/microk8s/common/
```

This can be made permanent by editing the `/etc/fstab`
```
/mnt/microk8s /var/snap/microk8s/common/ none defaults,bind 0 0
```

Next enable services that will be needed by Ambassador and Hatchery.
```
microk8s enable storage dns
```

Add a config file, so that hatchery can connect to the system
```
sudo microk8s config > ~/.kube/config
```

Alias kubectl to make things easy
```
alias kubectl=microk8s.kubectl
```

### Setting up ambassador

See the (Emissary getting started Guide)[https://www.getambassador.io/docs/emissary/latest/tutorials/getting-started/]. Setup Manual ambassador setup
```
kubectl apply -f https://www.getambassador.io/yaml/aes-crds.yaml && kubectl wait --for condition=established --timeout=90s crd -lproduct=aes && kubectl apply -f https://www.getambassador.io/yaml/aes.yaml && kubectl -n ambassador wait --for condition=available --timeout=90s deploy -lproduct=aes
```

Begin port forwarding to the service, so it can be connected to using `localhost`
as a base domain.
```
microk8s kubectl port-forward -n kube-system service/kubernetes-dashboard 10443:443
```


### Setting up K8s for hatchery

Start with the namespace creation. The `namespace.yaml` file
```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: hatch-test
  labels:
      name: development
```


Create a hatchery Config

Sample hatchery.yaml
```yaml
server-port: 8000
user-namespace: hatch-test
sub-dir : ""
host-domain: localhost
user-volume-size : 5G
sidecar:
  cpu-limit: "1.0"
  memory-limit: "256Mi"
  image: "quay.io/cdis/gen3fuse-sidecar:0.1.2"
  env:
    NAMESPACE: phillis
    HOSTNAME: phillis.planx-pla.net
  args: []
  command: ["su", "-c", "/home/jovyan/sidecarDockerrun.sh", "-s", "/bin/sh", "jovyan"]
  lifecycle-pre-stop: ["su", "-c", "cd /data; for f in *; do fusermount -u $f; rm -rf $f; done", "-s", "/bin/sh", "jovyan"]

service-mapper:
  ambassador-v2-mapper:
    host-domain: localhost

containers:
  - target-port: 8888
    cpu-limit: "1.0"
    memory-limit: "512Mi"
    name: "Jupyter Notebook Bio Python"
    image: "quay.io/occ_data/jupyternotebook:1.7.2"
    env: {}
    args: ["--NotebookApp.base_url=/lw-workspace/proxy/","--NotebookApp.password=''","--NotebookApp.token=''"]
    command: ["start-notebook.sh"]
    path-rewrite: "/lw-workspace/proxy/"
    use-tls: "false"
    ready-probe: "/lw-workspace/proxy/"
    user-uid: 1000
    fs-gid: 100
    user-volume-location: "/home/jovyan/pd"
    friends: []
```

### Build hatchery
```go
go build -o bin/ ./
```

### Start up hatchery
```
./bin/hatchery --config hatchery.yaml
```

### Get listing of apps
```bash
curl http://localhost:8000
```

Which returns:
```html
<html>
	<head>Gen3 Hatchery</head>
	<body><h1><a href="/launch?id=782b6d5f30a25b6ec3d24ea367a3a8b1">Launch Jupyter Notebook Bio Python - 1.0 CPU - 512Mi Memory</a></h1>
</body>
	</html>
```

Using the launch id, we can start an instance of the Jupyter notebook

```bash
curl -X POST -H "REMOTE_USER: test@test.com" "http://localhost:8000/launch?id=782b6d5f30a25b6ec3d24ea367a3a8b1"
```

Get the URL using the command
```bash
kubectl get mapping -n hatch-test
```
Which should return something like:
```
NAME                         SOURCE HOST                            SOURCE PREFIX   DEST SERVICE                 STATE   REASON
h-test-40test-2ecom-s   h-test-40test-2ecom-s.localhost   /               h-test-40test-2ecom-s           
```

In a web browser go to `http://h-test-40test-2ecom-s.localhost` to see the launched site.

## Reference
