# Inspector image: static binary on a minimal base, running as a non-root user.
# The viewer shell is embedded in the binary, so the image is just the binary.
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/inspector ./cmd/inspector

FROM alpine:3.20
RUN adduser -D -u 10001 inspector
COPY --from=build /out/inspector /usr/local/bin/inspector
USER inspector
EXPOSE 8765
# -container lets the process bind 0.0.0.0 inside this container only. The
# compose file publishes the port on 127.0.0.1 of the host.
ENTRYPOINT ["/usr/local/bin/inspector", "-container"]
HEALTHCHECK --interval=5s --timeout=3s --retries=20 \
  CMD wget -qO- http://127.0.0.1:8765/health >/dev/null || exit 1
