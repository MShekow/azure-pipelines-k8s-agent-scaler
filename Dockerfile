# syntax=docker/dockerfile:1
# Build the manager binary
FROM --platform=$BUILDPLATFORM golang:1.21 as builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.sum ./
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the go source
COPY cmd/main.go cmd/main.go
COPY api/ api/
COPY internal/ internal/

# Build
# the GOARCH has not a default value to allow the binary be built according to the host where the command
# was called. For example, if we call make docker-build in a local env which has the Apple Silicon M1 SO
# the docker BUILDPLATFORM arg will be linux/arm64 when for Apple x86 it will be linux/amd64. Therefore,
# by leaving it empty we can ensure that the container and binary shipped on it will have the same platform.
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o manager cmd/main.go

# use musl busybox since it's staticly compiled on all platforms
FROM busybox:musl AS busybox

# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/manager .

# Add a minimal shell that is only capable to delete and create files in "/home/nonroot"
# (minimal attack surface, for DEBUG_FILE_PATH)
COPY --from=busybox /bin/sh /bin/sh
COPY --from=busybox /bin/touch /bin/touch
COPY --from=busybox /bin/rm /bin/rm

USER 65532:65532

ENTRYPOINT ["/manager"]
