# Gateway image. Build from the repo root:
#   docker build -f gateway/Dockerfile -t glasshouse-gateway .
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY gateway/ .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/gateway ./cmd/gateway

# docker:cli carries the docker CLI and the compose plugin the lab runner uses.
FROM docker:29-cli
COPY --from=build /out/gateway /usr/local/bin/gateway
# The lab template: the same compose file readers run locally, plus the
# hosted override.
COPY adapters/postgres/compose.yml gateway/lab.override.yml /etc/glasshouse/lab/
# Runs as root: whoever holds the dind socket is root on the lab daemon
# anyway, so a non-root user here would add no boundary.
ENTRYPOINT ["/usr/local/bin/gateway"]
