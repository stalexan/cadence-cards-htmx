# --- build ---
FROM golang:1.26 AS build
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
FROM ubuntu:resolute
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl tzdata \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --system --uid 10001 --create-home app \
 && mkdir -p /data && chown app:app /data
COPY --from=build /out/cadence /usr/local/bin/cadence
USER app
ENV PORT=3000 \
    DB_PATH=/data/cadence.db
VOLUME /data
EXPOSE 3000
HEALTHCHECK --interval=30s --timeout=10s --retries=3 --start-period=15s \
  CMD curl -fsS http://localhost:3000/api/health || exit 1
ENTRYPOINT ["/usr/local/bin/cadence"]
