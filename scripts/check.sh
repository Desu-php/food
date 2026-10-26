#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
modules='platform contracts services/catalog services/orders services/delivery services/notifications services/auth'
files=$(gofmt -l platform contracts services)
if [ -n "$files" ]; then
  printf 'gofmt required:\n%s\n' "$files"
  exit 1
fi
go vet ./platform/... ./contracts/... ./services/catalog/... ./services/orders/... ./services/delivery/... ./services/notifications/... ./services/auth/...
go test ./platform/... ./contracts/... ./services/catalog/... ./services/orders/... ./services/delivery/... ./services/notifications/... ./services/auth/...
go test -race ./platform/... ./contracts/... ./services/catalog/... ./services/orders/... ./services/delivery/... ./services/notifications/... ./services/auth/...
for module in $modules; do
  (cd "$module" && GOWORK=off go build ./...)
done
docker compose --env-file .env.example -f deploy/compose.yaml config --quiet
docker compose -f deploy/compose.test.yaml config --quiet
AUTH_POSTGRES_PASSWORD=config-validation AUTH_DB_PASSWORD=config-validation \
  docker compose --env-file .env.example -f deploy/compose.yaml -f deploy/compose.auth.yaml config --quiet
