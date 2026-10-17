#!/bin/sh
set -eu
# Idempotent setup for new and existing Catalog volumes. Values are
# passed as psql variables and quoted with format(%I/%L), never interpolated SQL.
export PGPASSWORD="$POSTGRES_PASSWORD"
psql -v ON_ERROR_STOP=1 -h postgres -U "$POSTGRES_USER" -d postgres \
 -v catalog_db="$POSTGRES_DB" -v catalog_password="$CATALOG_DB_PASSWORD" \
 -v orders_password="$ORDERS_DB_PASSWORD" -v delivery_password="$DELIVERY_DB_PASSWORD" <<'SQL'
SELECT format('CREATE ROLE %I LOGIN', name)
FROM (VALUES ('catalog_service'), ('orders_service'), ('delivery_service')) AS roles(name)
WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname=name) \gexec
ALTER ROLE catalog_service PASSWORD :'catalog_password';
ALTER ROLE orders_service PASSWORD :'orders_password';
ALTER ROLE delivery_service PASSWORD :'delivery_password';
SELECT format('CREATE DATABASE %I OWNER catalog_service', :'catalog_db')
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname=:'catalog_db') \gexec
SELECT 'CREATE DATABASE orders OWNER orders_service'
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname='orders') \gexec
ALTER DATABASE :"catalog_db" OWNER TO catalog_service;
SELECT 'CREATE DATABASE delivery OWNER delivery_service'
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname='delivery') \gexec
ALTER DATABASE delivery OWNER TO delivery_service;
REVOKE ALL ON DATABASE delivery FROM PUBLIC;
ALTER DATABASE orders OWNER TO orders_service;
REVOKE ALL ON DATABASE :"catalog_db" FROM PUBLIC;
REVOKE ALL ON DATABASE orders FROM PUBLIC;
SQL
# Transfer ownership of existing Catalog tables created by the previous
# superuser-based Compose setup, so future migrations work under the service role.
psql -v ON_ERROR_STOP=1 -h postgres -U "$POSTGRES_USER" -d "$POSTGRES_DB" <<'SQL'
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
SELECT format('ALTER TABLE %I.%I OWNER TO catalog_service', schemaname, tablename)
FROM pg_tables WHERE schemaname='public' AND tableowner=current_user \gexec
SQL
psql -v ON_ERROR_STOP=1 -h postgres -U "$POSTGRES_USER" -d orders <<'SQL'
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
SQL

psql -v ON_ERROR_STOP=1 -h postgres -U "$POSTGRES_USER" -d delivery <<'SQL'
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
SQL
