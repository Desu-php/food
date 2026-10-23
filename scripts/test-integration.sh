#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
project=food-events-test
cleanup() { docker compose -p "$project" -f deploy/compose.test.yaml down -v; }
trap cleanup EXIT
docker compose -p "$project" -f deploy/compose.test.yaml up -d --wait --wait-timeout 180
./scripts/check-migrations.sh "$project"
export TEST_DATABASE_URL="postgres://test:test@localhost:${TEST_POSTGRES_PORT:-15432}/food_test?sslmode=disable"
export TEST_REDIS_URL="redis://localhost:${TEST_REDIS_PORT:-16379}/0"
export TEST_KAFKA_BROKERS="localhost:${TEST_KAFKA_PORT:-29092}"
go test -race -count=1 ./platform/... ./contracts/... ./services/catalog/... ./services/orders/... ./services/notifications/... ./services/delivery/... ./services/auth/...
go vet ./platform/... ./contracts/... ./services/catalog/... ./services/orders/... ./services/notifications/... ./services/delivery/... ./services/auth/...
