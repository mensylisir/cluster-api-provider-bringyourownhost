FROM --platform=$BUILDPLATFORM golang:1.24.5 AS builder
# ENV GOPROXY=https://goproxy.cn,direct
ENV GODEBUG=netdns=go
ARG TARGETOS
ARG TARGETARCH
WORKDIR /workspace
COPY go.mod go.mod
COPY go.sum go.sum
RUN  go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -a -o manager main.go

FROM gcr.io/distroless/static:nonroot-${TARGETARCH}
WORKDIR /
COPY --from=builder /workspace/manager .
USER 65532:65532

ENTRYPOINT ["/manager"]