# syntax=docker/dockerfile:1
# Multi-arch build without qemu: the build stage always runs natively on the
# builder and Go cross-compiles for $TARGETARCH.
FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS build
ARG TARGETOS TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X main.version=$VERSION" \
    -o /out/glance-multi-calendar ./cmd/glance-multi-calendar
# Pre-create the data dir so the volume mount point is writable by the
# unprivileged user below (scratch has no shell to chown at runtime).
RUN mkdir -p /out/data

FROM scratch
# TLS roots for the Google API; the IANA tz database is embedded in the binary.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/glance-multi-calendar /glance-multi-calendar
COPY config.example.yml /config.yml
COPY --from=build --chown=65534:65534 /out/data /data
USER 65534:65534
EXPOSE 8080
VOLUME ["/data"]
ENTRYPOINT ["/glance-multi-calendar", "-config", "/config.yml"]
