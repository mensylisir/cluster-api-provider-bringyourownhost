# Build the manager binary
ARG GO_VERSION=1.25.5
FROM golang:${GO_VERSION} as builder

ARG TARGETARCH

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# Use GODEBUG to disable IPv6 to avoid DNS resolution issues
RUN GODEBUG=netdns=go go mod download

# Copy the go source
COPY main.go main.go
COPY apis/ apis/
COPY controllers/ controllers/
COPY installer/ installer/
COPY common/ common/

# Build - support both amd64 and arm64
RUN case "${TARGETARCH}" in \
    amd64) GOARCH=amd64 ;; \
    arm64) GOARCH=arm64 ;; \
    *) GOARCH=amd64 ;; \
    esac && \
    CGO_ENABLED=0 GOOS=linux go build -a -o manager main.go

# Use distroless as minimal base image to package the manager binary
FROM gcr.io/distroless/static:nonroot-${TARGETARCH}
WORKDIR /
COPY --from=builder /workspace/manager .
USER 65532:65532

ENTRYPOINT ["/manager"]
