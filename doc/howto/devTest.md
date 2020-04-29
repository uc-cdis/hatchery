# TL;DR

Hatchery requires `golang 1.14+`.  It implements a golang module.


## Build and Test

See the [Dockerfile](../../Dockerfile):

Update dependencies with:
```
go get -u
```

Build and test with:
```
(
go build -o bin/hatchery && go test -v ./hatchery/
)
```
