# Multi-stage build for ginkgo-cli QUIC server with Let's Encrypt (CertMagic)

FROM golang:1.24 AS builder
WORKDIR /src

# Pre-fetch modules
COPY go.mod go.sum ./
RUN go mod download

# Build binary
COPY . .
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags='-s -w' -o /out/ginkgo-cli ./cmd/ginkgo-cli


FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && \
    adduser -D -u 10001 appuser

USER appuser
WORKDIR /app
ENV XDG_CACHE_HOME=/data/cache

COPY --from=builder /out/ginkgo-cli /usr/local/bin/ginkgo-cli

# Persist CertMagic storage/cache
VOLUME ["/data"]

# QUIC (UDP) and HTTP-01 challenge (TCP) inside container
EXPOSE 7845/udp
EXPOSE 8080/tcp

# By default, listen on UDP :7845 and serve HTTP-01 on :8080.
# Map host port 80 -> 8080 (e.g., -p 80:8080) to satisfy ACME HTTP-01.
ENTRYPOINT ["/usr/local/bin/ginkgo-cli", "quic", "serve"]
CMD ["--addr", ":7845", "--http-addr", ":8080"]
