# Synchronized from operands.yaml with RELATED_IMAGE_FAR_MANAGER.
ARG FAR_IMAGE=quay.io/medik8s/fence-agents-remediation-operator:wao

# Inspect the existing FAR image during the WAO build. Do not execute agents.
FROM ${FAR_IMAGE} AS far-inventory
ARG FAR_IMAGE
RUN umask 022 \
    && mkdir -p /tmp/wao-far-inventory \
    && printf '%s\n' "${FAR_IMAGE}" > /tmp/wao-far-inventory/source-image \
    && LC_ALL=C ls -la /usr/sbin > /tmp/wao-far-inventory/usr-sbin.list \
    && LC_ALL=C ls -1A /usr/sbin > /tmp/wao-far-inventory/usr-sbin.names \
    && printf 'FAR inventory entries: ' \
    && wc -l < /tmp/wao-far-inventory/usr-sbin.names

# Build the manager binary
FROM quay.io/konveyor/builder:ubi9-latest AS builder

WORKDIR /workspace

ENV GOTOOLCHAIN=auto \
    GOMODCACHE=/go/pkg/mod \
    GOCACHE=/go/build-cache

# This layer is reusable while the dependency manifests stay unchanged.
COPY go.mod go.sum ./
RUN go version && go mod download

# Keep frequently changing build arguments after dependency downloads.
ARG TARGETOS
ARG TARGETARCH
ARG VERSION

COPY cmd/ cmd/
COPY api/ api/
COPY internal/ internal/
COPY version/ version/
COPY hack/ hack/
COPY Makefile ./

# Preserve the existing version-information behavior.
COPY .git/ .git/

RUN git config --global --add safe.directory /workspace

# Preserve compiled packages even when source changes invalidate this step.
RUN --mount=type=cache,id=wao-go-build,target=/go/build-cache \
    GOOS="${TARGETOS:-linux}" \
    GOARCH="${TARGETARCH:-$(go env GOARCH)}" \
    ./hack/build.sh

FROM registry.access.redhat.com/ubi9/ubi-micro:latest
WORKDIR /
COPY --from=builder /workspace/bin/manager .
COPY --from=far-inventory /tmp/wao-far-inventory/ /usr/share/wao/far-inventory/
USER 65532:65532

ENTRYPOINT ["/manager"]
