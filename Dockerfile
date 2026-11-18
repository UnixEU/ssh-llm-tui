# syntax=docker/dockerfile:1

FROM golang:1.24-alpine AS build
WORKDIR /src
RUN apk add --no-cache ca-certificates git
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags="-s -w -X main.version=1.0.2" \
    -o /out/ssh-llm-tui ./cmd/ssh-llm-tui

# glibc, not Alpine. The NVIDIA container toolkit injects nvidia-smi and its
# libraries when NVIDIA_DRIVER_CAPABILITIES=utility, and those binaries need glibc.
FROM debian:bookworm-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 65532 app \
    && useradd --uid 65532 --gid app --create-home --home-dir /home/app app \
    && mkdir -p /data/hostkeys /config /tmp/uploads \
    && chown -R app:app /data /config /tmp/uploads
COPY --from=build /out/ssh-llm-tui /usr/local/bin/ssh-llm-tui
COPY config.example.yaml /config/config.yaml
USER 65532:65532
EXPOSE 2222
ENV CONFIG_PATH=/config/config.yaml \
    HOST_KEY_PATH=/data/hostkeys/ssh_host_ed25519_key \
    DMR_BASE_URL=http://host.docker.internal:12434 \
    AUTH_MODE=open
ENTRYPOINT ["/usr/local/bin/ssh-llm-tui"]
