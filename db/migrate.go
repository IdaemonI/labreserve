// Package db applies versioned PostgreSQL migrations embedded in the binary.
package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Migrate opens a connection, applies all pending migrations, and closes it.
// DATABASE_URL is never included in returned errors because it may hold secrets.
func Migrate(ctx context.Context, dsn string) error {
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		// Parse errors may contain the original DSN, including its password.
		return fmt.Errorf("invalid DATABASE_URL")
	}
	database := stdlib.OpenDB(*config)
	defer database.Close()
	if err := database.PingContext(ctx); err != nil {
		return fmt.Errorf("connect migration database: %w", err)
	}
	return Apply(ctx, database)
}

// Apply uses the caller's connection settings, including an isolated search_path.
// Each SQL migration runs inside a goose-managed transaction.
func Apply(ctx context.Context, database *sql.DB) error {
	files, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return fmt.Errorf("load embedded migrations: %w", err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, database, files)
	if err != nil {
		return fmt.Errorf("configure migrations: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}
