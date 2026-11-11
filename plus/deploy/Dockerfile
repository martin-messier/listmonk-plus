# listmonk-plus: builds the fork from source (upstream's Dockerfile expects a
# prebuilt binary from goreleaser). Build from the repo root:
#   docker build -f plus/deploy/Dockerfile --build-arg VERSION=v7.0.0-beta.2 --build-arg COMMIT=$(git rev-parse --short HEAD) .

# 1. Admin frontend (Bun). Mirrors `bun run build` in static/admin.
FROM oven/bun:1.4.3 AS frontend
WORKDIR /src/static/admin
COPY static/admin/package.json static/admin/bun.lock ./
RUN bun install --frozen-lockfile
COPY static/admin/ ./
RUN mkdir -p ../public/static && bun run build

# 2. Go binary with all static assets packed in (mirrors `make build` + `make pack-bin`).
FROM golang:1.27.3-trixie AS backend
ARG VERSION=v7.0.0-beta.2
ARG COMMIT=unknown
WORKDIR /src
RUN go install github.com/knadh/stuffbin/stuffbin@v1.3.0
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=frontend /src/static/admin/dist ./static/admin/dist
COPY --from=frontend /src/static/public/static/altcha.umd.js ./static/public/static/altcha.umd.js
RUN CGO_ENABLED=0 go build -o listmonk \
      -ldflags="-s -w -X 'main.buildString=${VERSION} (#${COMMIT} listmonk-plus)' -X 'main.versionString=${VERSION}'" ./cmd \
 && stuffbin -a stuff -in listmonk -out listmonk \
      config.toml.sample schema.sql queries:/queries permissions.json \
      static/public:/public \
      static/admin/views:/admin/views static/admin/partials:/admin/partials \
      static/admin/dist:/admin/static static/admin/i18n.txt:/admin/i18n.txt \
      static/email-templates i18n:/i18n

# 3. Runtime image (same as upstream's).
FROM alpine:3.22
RUN apk --no-cache add ca-certificates tzdata shadow su-exec
WORKDIR /listmonk
COPY --from=backend /src/listmonk .
COPY config.toml.sample config.toml
COPY docker-entrypoint.sh /usr/local/bin/
RUN chmod +x /usr/local/bin/docker-entrypoint.sh
EXPOSE 9000
ENTRYPOINT ["docker-entrypoint.sh"]
CMD ["./listmonk"]
