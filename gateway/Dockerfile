FROM --platform=$BUILDPLATFORM golang:1.22-alpine AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /src

COPY go.mod ./
RUN go mod download

COPY cmd/ cmd/
COPY internal/ internal/

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
    -ldflags="-w -s" \
    -o /bin/gateway \
    ./cmd/gateway

FROM gcr.io/distroless/static:nonroot

WORKDIR /
COPY --from=builder /bin/gateway /gateway

USER 65532:65532

EXPOSE 8080

ENTRYPOINT ["/gateway"]
