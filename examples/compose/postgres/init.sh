#!/bin/sh
# Runs once on an empty PostgreSQL data directory.
set -eu

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<SQL
CREATE TABLE public.jobs (
    id         bigserial PRIMARY KEY,
    state      text NOT NULL CHECK (state IN ('queued', 'running', 'failed', 'done')),
    updated_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO public.jobs (state) VALUES
    ('queued'), ('queued'), ('running'), ('failed');

CREATE ROLE reader LOGIN PASSWORD '${APP_READER_PASSWORD}';
GRANT CONNECT ON DATABASE ${POSTGRES_DB} TO reader;
GRANT USAGE ON SCHEMA public TO reader;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO reader;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO reader;
SQL
