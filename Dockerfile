# The bohux binary as an image. It runs the control plane by default (ADR-0030); the
# agent runs on the host, not here. Build from the repository root:
#   docker build -t bohux:dev --build-arg VERSION=dev .
# Generated code (gen/) is used when present in the context, else built from proto/
# with buf, which needs the Buf registry.

FROM bufbuild/buf:1.47.2 AS buf

FROM golang:1.24-bookworm AS build
COPY --from=buf /usr/local/bin/buf /usr/local/bin/buf
WORKDIR /src
COPY . .
# A vendor/ directory in the context is used as is (offline builds); otherwise modules
# are downloaded.
RUN [ -d vendor ] || go mod download
RUN if [ ! -d gen ]; then \
      go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.6 && \
      go install connectrpc.com/connect/cmd/protoc-gen-connect-go@v1.17.0 && \
      (test -f buf.lock || buf dep update) && buf generate; \
    fi
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X github.com/bohumix/bohux/internal/version.Version=${VERSION}" \
      -o /out/bohux ./cmd/bohux
# The data and backup directories, owned by the runtime user so a fresh named volume
# mounted there inherits that ownership.
RUN mkdir -p /out/data /out/backups

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/bohux /bohux
COPY --from=build --chown=65532:65532 /out/data /var/lib/bohux
COPY --from=build --chown=65532:65532 /out/backups /var/lib/bohux-backups
ENV BOHUX_LISTEN_ADDR=0.0.0.0:7400 \
    BOHUX_AGENT_LISTEN_ADDR=0.0.0.0:7443 \
    BOHUX_DATA_DIR=/var/lib/bohux
EXPOSE 7400 7443
USER 65532:65532
ENTRYPOINT ["/bohux"]
CMD ["controlplane"]
