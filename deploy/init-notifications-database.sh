#!/bin/sh
set -eu
export PGPASSWORD="$POSTGRES_PASSWORD"
psql -v ON_ERROR_STOP=1 -h notifications-postgres -U notifications_admin -d postgres \
 -v service_password="$NOTIFICATIONS_DB_PASSWORD" <<'SQL'
SELECT 'CREATE ROLE notifications_service LOGIN'
WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname='notifications_service') \gexec
ALTER ROLE notifications_service PASSWORD :'service_password';
SELECT 'CREATE DATABASE notifications OWNER notifications_service'
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname='notifications') \gexec
ALTER DATABASE notifications OWNER TO notifications_service;
REVOKE ALL ON DATABASE notifications FROM PUBLIC;
SQL
psql -v ON_ERROR_STOP=1 -h notifications-postgres -U notifications_admin -d notifications <<'SQL'
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
SQL
