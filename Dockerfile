FROM golang:1.24 AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags='-s -w' -o /out/ginkgo-cli ./cmd/ginkgo-cli


FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 appuser

USER appuser
WORKDIR /app
ENV XDG_CACHE_HOME=/data/cache
ENV GINKGO_DATA_DIR=/data
ENV GINKGO_HTTP_ADDR=":8080"

COPY --from=builder /out/ginkgo-cli /usr/local/bin/ginkgo-cli
VOLUME ["/data"]
EXPOSE 8080/tcp
ENTRYPOINT ["/usr/local/bin/ginkgo-cli", "server"]
