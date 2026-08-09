package configurator

import (
	"context"
	"fmt"
	"log"

	"github.com/jackc/pgx/v5"
)

// EnsureDatabase creates the named database if it doesn't exist, grants all
// privileges to owner, and enables any requested extensions.
// socketDir is the unix socket directory (e.g. "/run/postgresql").
// By the time configurator PreStart runs, postgres is already available via
// the orchestrator's dependency ordering, so no polling is needed.
func EnsureDatabase(ctx context.Context, socketDir, owner, dbName string, extensions []string) error {
	connStr := fmt.Sprintf("user=%s host=%s dbname=postgres sslmode=disable", owner, socketDir)
	conn, err := pgx.Connect(ctx, connStr)
	if err != nil {
		return fmt.Errorf("connecting to postgres: %w", err)
	}
	defer conn.Close(ctx)

	var exists bool
	if err := conn.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname=$1)", dbName).Scan(&exists); err != nil {
		return fmt.Errorf("checking database existence: %w", err)
	}

	if !exists {
		if _, err := conn.Exec(ctx, fmt.Sprintf("CREATE DATABASE %q", dbName)); err != nil {
			return fmt.Errorf("creating database %q: %w", dbName, err)
		}
		log.Printf("postgres: created database %q", dbName)
	}

	if _, err := conn.Exec(ctx, fmt.Sprintf("GRANT ALL PRIVILEGES ON DATABASE %q TO %q", dbName, owner)); err != nil {
		return fmt.Errorf("granting privileges on %q: %w", dbName, err)
	}

	if len(extensions) == 0 {
		return nil
	}

	conn.Close(ctx)

	appConnStr := fmt.Sprintf("user=%s host=%s dbname=%s sslmode=disable", owner, socketDir, dbName)
	appConn, err := pgx.Connect(ctx, appConnStr)
	if err != nil {
		return fmt.Errorf("connecting to database %q: %w", dbName, err)
	}
	defer appConn.Close(ctx)

	for _, ext := range extensions {
		if _, err := appConn.Exec(ctx, fmt.Sprintf("CREATE EXTENSION IF NOT EXISTS %q", ext)); err != nil {
			return fmt.Errorf("enabling extension %q: %w", ext, err)
		}
		log.Printf("postgres: enabled extension %q in %q", ext, dbName)
	}

	return nil
}
