#!/bin/sh
# Applies and rolls back every migration in disposable schemas on the test DB.
set -eu
cd "$(dirname "$0")/.."
project=${1:-food-events-test}
for service in catalog orders delivery notifications; do
  schema="migration_check_$service"
  {
    printf 'CREATE SCHEMA %s; SET search_path TO %s;\n' "$schema" "$schema"
    for file in services/"$service"/migrations/*.up.sql; do cat "$file"; printf '\n'; done
    for file in $(find services/"$service"/migrations -name '*.down.sql' | sort -r); do cat "$file"; printf '\n'; done
    printf 'DROP SCHEMA %s;\n' "$schema"
  } | docker compose -p "$project" -f deploy/compose.test.yaml exec -T postgres psql -X -v ON_ERROR_STOP=1 -U test -d food_test >/dev/null
  printf 'Migration round trip passed: %s\n' "$service"
done
