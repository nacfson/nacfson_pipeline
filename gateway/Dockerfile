# Multi-stage Dockerfile for Go Authentication Gateway
# Supports both linux/amd64 and linux/arm64 architectures

FROM --platform=$BUILDPLATFORM golang:1.22-alpine AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /src

# Copy go.mod
COPY go.mod ./
RUN go mod download

# Copy source code
COPY cmd/ cmd/
COPY internal/ internal/

# Build statically linked binary
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
    -ldflags="-w -s" \
    -o /bin/gateway \
    ./cmd/gateway

# Production runtime image: unprivileged non-root scratch image
FROM gcr.io/distroless/static:nonroot

WORKDIR /
COPY --from=builder /bin/gateway /gateway

USER 65532:65532

EXPOSE 8080

ENTRYPOINT ["/gateway"]
