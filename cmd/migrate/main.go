package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/IdaemonI/labreserve/db"
	"github.com/jackc/pgx/v5/pgconn"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if len(os.Args) > 2 || (len(os.Args) == 2 && os.Args[1] != "up") {
		logger.Error("usage: migrate [up]")
		os.Exit(2)
	}
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		logger.Error("DATABASE_URL is required")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if err := db.Migrate(ctx, dsn); err != nil {
		// Parsing/connection errors can include the DSN. A server SQLSTATE and
		// message identify migration failures without recording credentials.
		var serverError *pgconn.PgError
		if errors.As(err, &serverError) {
			logger.Error("migration failed", "sqlstate", serverError.Code, "message", serverError.Message)
		} else if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			logger.Error("migration canceled or timed out")
		} else {
			logger.Error("migration failed; check PostgreSQL availability, credentials and schema permissions")
		}
		os.Exit(1)
	}
	logger.Info("migrations applied")
}
