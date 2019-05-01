FROM golang:1.12 as build-deps

WORKDIR /hatchery
ENV GOPATH=/hatchery

RUN go get -tags k8s.io/client-go/kubernetes \
    k8s.io/apimachinery/pkg/apis/meta/v1 \
    k8s.io/api/core/v1 \
    k8s.io/api/batch/v1 \
    k8s.io/client-go/tools/clientcmd \
    k8s.io/client-go/rest \
    github.com/golang/protobuf/proto \
    github.com/googleapis/gnostic/OpenAPIv2

COPY . /hatchery

# Populate git version info into the code
RUN echo "package handlers\n\nconst (" >src/handlers/gitversion.go \
    && COMMIT=`git rev-parse HEAD` && echo "    gitcommit=\"${COMMIT}\"" >>src/handlers/gitversion.go \
    && VERSION=`git describe --always --tags` && echo "    gitversion=\"${VERSION}\"" >>src/handlers/gitversion.go \
    && echo ")" >>src/handlers/gitversion.go


RUN echo $SHELL
RUN echo $GOPATH
RUN ls -al
RUN ls -al src/handlers
RUN go build -ldflags "-linkmode external -extldflags -static"

# Store only the resulting binary in the final image
# Resulting in significantly smaller docker image size
FROM scratch
COPY --from=build-deps /hatchery/hatchery /hatchery
CMD ["/hatchery"]
