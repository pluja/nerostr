# syntax=docker/dockerfile:1
# Static build (LMDB needs cgo); xx cross-compiles for the target platform natively.

FROM --platform=$BUILDPLATFORM tonistiigi/xx:1.9.0 AS xx

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
# hadolint ignore=DL3067
COPY --from=xx / /
RUN apk add --no-cache clang lld
ARG TARGETPLATFORM
RUN xx-apk add --no-cache musl-dev gcc
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY main.go ./
COPY internal ./internal
ARG VERSION=dev
ENV CGO_ENABLED=1
RUN xx-go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION} -linkmode external -extldflags -static" \
      -o /out/nerostr . \
 && xx-verify --static /out/nerostr \
 && mkdir -p /out/data

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /out/nerostr /nerostr
COPY --from=build --chown=1000:1000 /out/data /data
USER 1000:1000
ENV DATA_DIR=/data LISTEN_ADDR=:8080
VOLUME /data
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=10s --start-period=20s CMD ["/nerostr", "healthcheck"]
ENTRYPOINT ["/nerostr"]
