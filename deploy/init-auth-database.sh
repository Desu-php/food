#!/bin/sh
set -eu
export PGPASSWORD="$POSTGRES_PASSWORD"
psql -v ON_ERROR_STOP=1 -h auth-postgres -U auth_admin -d postgres -v service_password="$AUTH_DB_PASSWORD" <<'SQL'
SELECT 'CREATE ROLE auth_service LOGIN'
WHERE NOT EXISTS(SELECT FROM pg_roles WHERE rolname='auth_service') \gexec
ALTER ROLE auth_service PASSWORD :'service_password';
SELECT 'CREATE DATABASE auth OWNER auth_service'
WHERE NOT EXISTS(SELECT FROM pg_database WHERE datname='auth') \gexec
ALTER DATABASE auth OWNER TO auth_service;
REVOKE ALL ON DATABASE auth FROM PUBLIC;
SQL
psql -v ON_ERROR_STOP=1 -h auth-postgres -U auth_admin -d auth <<'SQL'
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
SQL
