FROM golang:1.14 as build-deps

RUN apt-get update && apt-get install -y --no-install-recommends \
    vim

WORKDIR /hatchery

COPY . /hatchery

# Populate git version info into the code
RUN echo "package hatchery\n\nconst (" >hatchery/gitversion.go \
    && COMMIT=`git rev-parse HEAD` && echo "    gitcommit=\"${COMMIT}\"" >>hatchery/gitversion.go \
    && VERSION=`git describe --always --tags` && echo "    gitversion=\"${VERSION}\"" >>hatchery/gitversion.go \
    && echo ")" >>hatchery/gitversion.go

RUN echo $SHELL && ls -al && ls -al hatchery/
RUN go build -ldflags "-linkmode external -extldflags -static" -o bin/hatchery

# Store only the resulting binary in the final image
# Resulting in significantly smaller docker image size
#FROM scratch
#COPY --from=build-deps /hatchery/hatchery /hatchery

CMD ["/hatchery/bin/hatchery"]
