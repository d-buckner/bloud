-- Creates the databases needed by the dev stack.
-- Mounted into the postgres container as /docker-entrypoint-initdb.d/init.sql.
-- The default "apps" user is created by POSTGRES_USER in compose.yml.

CREATE DATABASE authentik;
CREATE DATABASE bloud;
