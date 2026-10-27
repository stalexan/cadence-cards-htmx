# Build with Podman: podman build -t localhost/cadence-cards .
# Image names are fully qualified so Podman's short-name resolution never
# prompts or guesses a registry.

# --- build ---
FROM docker.io/library/golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Pure-Go SQLite (modernc.org/sqlite) makes CGO_ENABLED=0 possible: a static
# binary with no libc coupling to the runtime image.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/cadence ./cmd/cadence
# Fail the image build if unit tests fail.
RUN CGO_ENABLED=0 go test ./...

# --- runtime ---
FROM registry.fedoraproject.org/fedora:44
# curl is for the health check, which lives in the Quadlet unit
# (deploy/cadence-cards.container): Podman's default OCI image format has no
# HEALTHCHECK instruction, so one here would be dropped at build time.
RUN dnf -y install --setopt=install_weak_deps=False ca-certificates curl tzdata shadow-utils \
 && dnf clean all \
 && useradd --system --uid 10001 --create-home app \
 && mkdir -p /data && chown app:app /data
COPY --from=build /out/cadence /usr/local/bin/cadence
USER app
ENV PORT=3000 \
    DB_PATH=/data/cadence.db
VOLUME /data
EXPOSE 3000
ENTRYPOINT ["/usr/local/bin/cadence"]
