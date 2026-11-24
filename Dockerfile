ARG AZLINUX_BASE_VERSION=hardened
FROM quay.io/cdis/amazonlinux-base:${AZLINUX_BASE_VERSION} AS base

FROM --platform=$BUILDPLATFORM quay.io/cdis/amazonlinux-base:${AZLINUX_BASE_VERSION} AS build
ARG BUILDARCH
ARG TARGETOS
ARG TARGETARCH
ARG GO_VERSION=1.26.4
ARG GO_SHA256_AMD64=1153d3d50e0ac764b447adfe05c2bcf08e889d42a02e0fe0259bd47f6733ad7f
ARG GO_SHA256_ARM64=ef758ae7c6cf9267c9c0ef080b8965f453d89ab2d25d9eb22de4405925238768

RUN dnf install -y --nodocs --setopt=install_weak_deps=False git tar gzip \
    && dnf clean all

RUN case "${BUILDARCH}" in \
        amd64) sha="${GO_SHA256_AMD64}" ;; \
        arm64) sha="${GO_SHA256_ARM64}" ;; \
        *) echo "unsupported BUILDARCH=${BUILDARCH}" >&2; exit 1 ;; \
    esac \
    && curl -fsSL -o /tmp/go.tgz "https://go.dev/dl/go${GO_VERSION}.linux-${BUILDARCH}.tar.gz" \
    && echo "${sha}  /tmp/go.tgz" | sha256sum -c - \
    && tar -C /usr/local -xzf /tmp/go.tgz \
    && rm /tmp/go.tgz

ENV PATH="/usr/local/go/bin:${PATH}" \
    CGO_ENABLED=0 \
    GOOS=${TARGETOS} \
    GOARCH=${TARGETARCH} \
    GOTOOLCHAIN=local \
    GOFLAGS=-mod=readonly \
    GOFIPS140=v1.0.0

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download && go mod verify

COPY . .
RUN GITCOMMIT=$(git rev-parse HEAD) \
    && GITVERSION=$(git describe --always --tags) \
    && go build -trimpath \
        -ldflags="-s -w -X 'github.com/uc-cdis/hatchery/hatchery/version.GitCommit=${GITCOMMIT}' -X 'github.com/uc-cdis/hatchery/hatchery/version.GitVersion=${GITVERSION}'" \
        -o /out/hatchery

FROM base AS runtime-files
RUN mkdir -p /var/hatchery && rm -f /home/gen3/.bash*

FROM scratch
COPY --from=runtime-files /etc/passwd /etc/group /etc/
COPY --from=runtime-files /etc/pki /etc/pki
COPY --from=runtime-files /etc/ssl /etc/ssl
COPY --from=runtime-files /etc/crypto-policies /etc/crypto-policies
COPY --from=runtime-files /etc/localtime /etc/localtime
COPY --from=runtime-files /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=runtime-files /tmp /tmp
COPY --from=runtime-files /var/hatchery /var/hatchery
COPY --from=runtime-files /home/gen3 /home/gen3
COPY --from=build /out/hatchery /hatchery

ENV LC_ALL=C.UTF-8 LANG=C.UTF-8
# gen3, numeric so Kubernetes can enforce runAsNonRoot
USER 1000:1000
CMD ["/hatchery"]
