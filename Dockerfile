# syntax=docker/dockerfile:1
# Runtime image: dumpling binary + PostgreSQL 17 and MySQL 8.4 (Oracle) client tools.
# SQL Server needs no client tool (native BACKUP/RESTORE through the driver).

FROM node:22-bookworm-slim AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist web/dist
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/dumpling ./cmd/dumpling

FROM debian:bookworm-slim AS tools
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl gnupg \
 && install -d /usr/share/keyrings \
 && curl -fsSL https://www.postgresql.org/media/keys/ACCC4CF8.asc | gpg --dearmor -o /usr/share/keyrings/pgdg.gpg \
 && echo "deb [signed-by=/usr/share/keyrings/pgdg.gpg] https://apt.postgresql.org/pub/repos/apt bookworm-pgdg main" > /etc/apt/sources.list.d/pgdg.list \
 && curl -fsSL https://repo.mysql.com/RPM-GPG-KEY-mysql-2025 | gpg --dearmor -o /usr/share/keyrings/mysql.gpg \
 && echo "deb [signed-by=/usr/share/keyrings/mysql.gpg] http://repo.mysql.com/apt/debian bookworm mysql-8.4-lts" > /etc/apt/sources.list.d/mysql.list \
 && apt-get update && apt-get install -y --no-install-recommends postgresql-client-17 mysql-client \
 && apt-get purge -y curl gnupg && apt-get autoremove -y && rm -rf /var/lib/apt/lists/*

# Integration tests: `docker compose -f testdata/compose.yaml run --rm it`
FROM tools AS it
COPY --from=golang:1.26-bookworm /usr/local/go /usr/local/go
ENV PATH=/usr/local/go/bin:$PATH CGO_ENABLED=0
WORKDIR /src

FROM tools
COPY --from=build /out/dumpling /usr/local/bin/dumpling
RUN useradd --system --uid 10001 --home /data dumpling && install -d -o dumpling /data
USER dumpling
VOLUME /data
EXPOSE 9898
# Binds all interfaces inside the container: publish the port only behind TLS (--tls-cert/--tls-key) or a TLS reverse proxy.
ENTRYPOINT ["dumpling", "--data-dir", "/data", "--bind", "0.0.0.0:9898"]
