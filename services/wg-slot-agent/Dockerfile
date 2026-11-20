FROM --platform=$BUILDPLATFORM golang:1.26.9-alpine AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /src

COPY go.mod ./
COPY cmd/ cmd/
COPY internal/ internal/

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
    -ldflags="-w -s" \
    -o /bin/wg-slot-agent \
    ./cmd/wg-slot-agent

FROM alpine:3.24.3

RUN apk add --no-cache wireguard-tools-wg iproute2-minimal nftables

COPY --from=builder /bin/wg-slot-agent /usr/local/bin/wg-slot-agent

ENTRYPOINT ["/usr/local/bin/wg-slot-agent"]
CMD ["run"]
