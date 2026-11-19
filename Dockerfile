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
USER 65532:65532

ENTRYPOINT ["/manager"]
