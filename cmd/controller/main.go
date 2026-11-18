package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/IdaemonI/labreserve/internal/config"
	"github.com/IdaemonI/labreserve/internal/diagnostics"
	"github.com/IdaemonI/labreserve/internal/httpapi"
	"github.com/IdaemonI/labreserve/internal/service"
	"github.com/IdaemonI/labreserve/internal/storage/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("controller stopped with error", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	check := flag.Bool("healthcheck", false, "check HTTP readiness and exit")
	address := flag.String("address", "http://127.0.0.1:8080/readyz", "healthcheck URL")
	flag.Parse()
	if *check {
		return healthcheck(*address)
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	poolConfig, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return errors.New("invalid DATABASE_URL configuration")
	}
	poolConfig.ConnConfig.ConnectTimeout = cfg.DatabaseTimeout
	poolConfig.ConnConfig.RuntimeParams["timezone"] = "UTC"
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return errors.New("initialize database pool")
	}
	defer pool.Close()
	pingCtx, cancel := context.WithTimeout(ctx, cfg.DatabaseTimeout)
	err = pool.Ping(pingCtx)
	cancel()
	if err != nil {
		return fmt.Errorf("database startup check: %w", err)
	}
	client, err := diagnostics.NewClient(cfg.GRPCTarget, cfg.DiagnosticsTimeout)
	if err != nil {
		return fmt.Errorf("initialize diagnostics client: %w", err)
	}
	defer func() {
		if err := client.Close(); err != nil {
			logger.Error("close diagnostics connection", "error", err)
		}
	}()
	s := service.New(postgres.New(pool), client, cfg.DatabaseTimeout)
	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           httpapi.New(s, logger, cfg.RequestTimeout),
		ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout:       cfg.RequestTimeout,
		WriteTimeout:      cfg.RequestTimeout + 2*time.Second,
		IdleTimeout:       time.Minute,
		MaxHeaderBytes:    16 << 10,
	}
	result := make(chan error, 1)
	go func() { result <- server.ListenAndServe() }()
	logger.Info("controller started", "http_addr", cfg.HTTPAddr)
	select {
	case err = <-result:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve HTTP: %w", err)
	case <-ctx.Done():
		logger.Info("controller shutting down")
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		// Closing active connections cancels their database and RPC contexts before
		// the deferred pool/connection closes can wait on them.
		closeErr := server.Close()
		return errors.Join(fmt.Errorf("HTTP graceful shutdown: %w", err), closeErr)
	}
	if err := <-result; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	logger.Info("controller stopped")
	return nil
}

func healthcheck(address string) error {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(address)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if _, err := io.Copy(io.Discard, io.LimitReader(resp.Body, 4096)); err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("readiness status %d", resp.StatusCode)
	}
	return nil
}
