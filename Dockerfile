ARG AZLINUX_BASE_VERSION=hardened
FROM quay.io/cdis/golang:1.23-bookworm AS build-deps

ENV CGO_ENABLED=0
ENV GOOS=linux
ENV GOARCH=amd64

# Use Go toolchains (Go 1.21+ feature) to build with a newer Go toolchain
# Pick the Go 1.2x.x that is needed for the build.
ENV GOTOOLCHAIN=go1.26.0

WORKDIR $GOPATH/src/github.com/uc-cdis/hatchery/

COPY go.mod .
COPY go.sum .

RUN go mod download

COPY . .

RUN GITCOMMIT=$(git rev-parse HEAD) \
    GITVERSION=$(git describe --always --tags) \
    && go build \
    -ldflags="-X 'github.com/uc-cdis/hatchery/hatchery/version.GitCommit=${GITCOMMIT}' -X 'github.com/uc-cdis/hatchery/hatchery/version.GitVersion=${GITVERSION}'" \
    -o /hatchery


FROM quay.io/cdis/amazonlinux-base:${AZLINUX_BASE_VERSION}
USER gen3

ENV GOFIPS140=latest
COPY --from=build-deps /hatchery /hatchery
CMD ["/hatchery"]
