// Package config reads and validates the controller's environment settings.
package config

import (
	"fmt"
	"net"
	"os"
	"time"
)

type Config struct {
	DatabaseURL        string
	HTTPAddr           string
	GRPCTarget         string
	DatabaseTimeout    time.Duration
	DiagnosticsTimeout time.Duration
	RequestTimeout     time.Duration
	ShutdownTimeout    time.Duration
}

func Load() (Config, error) {
	c := Config{DatabaseURL: os.Getenv("DATABASE_URL"), HTTPAddr: value("HTTP_ADDR", ":8080"), GRPCTarget: value("GRPC_TARGET", "127.0.0.1:9090")}
	if c.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}
	if _, _, err := net.SplitHostPort(c.HTTPAddr); err != nil {
		return Config{}, fmt.Errorf("HTTP_ADDR must be host:port")
	}
	for _, item := range []struct {
		name, fallback string
		target         *time.Duration
	}{
		{"DATABASE_TIMEOUT", "2s", &c.DatabaseTimeout},
		{"DIAGNOSTICS_TIMEOUT", "1s", &c.DiagnosticsTimeout},
		{"HTTP_REQUEST_TIMEOUT", "5s", &c.RequestTimeout},
		{"SHUTDOWN_TIMEOUT", "10s", &c.ShutdownTimeout},
	} {
		d, err := time.ParseDuration(value(item.name, item.fallback))
		if err != nil || d <= 0 || d > time.Minute {
			return Config{}, fmt.Errorf("%s must be a positive duration no greater than 1m", item.name)
		}
		*item.target = d
	}
	return c, nil
}

func value(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
