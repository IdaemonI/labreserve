package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	pb "github.com/IdaemonI/labreserve/gen/diagnostics/v1"
	"github.com/IdaemonI/labreserve/internal/diagnostics"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)
	address := flag.String("address", envDefault("GRPC_ADDR", ":9090"), "listen address, or healthcheck target")
	check := flag.Bool("healthcheck", false, "check the gRPC health service and exit")
	flag.Parse()
	if *check {
		if err := healthcheck(*address); err != nil {
			logger.Error("simulator healthcheck failed", "error", err)
			os.Exit(1)
		}
		return
	}
	if err := run(*address, logger); err != nil {
		logger.Error("simulator stopped with error", "error", err)
		os.Exit(1)
	}
}

func run(address string, logger *slog.Logger) error {
	delay, err := time.ParseDuration(envDefault("SIMULATOR_DELAY", "3s"))
	if err != nil {
		return fmt.Errorf("SIMULATOR_DELAY must be a Go duration: %w", err)
	}
	agent, err := diagnostics.NewServer(delay, envDefault("SIMULATOR_MODE", "normal"))
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("listen for gRPC: %w", err)
	}
	defer listener.Close()
	server := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		start := time.Now()
		response, err := handler(ctx, req)
		attrs := []any{"method", info.FullMethod, "duration_ms", time.Since(start).Milliseconds(), "grpc_code", status.Code(err).String()}
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			if ids := md.Get("x-request-id"); len(ids) > 0 {
				attrs = append(attrs, "request_id", ids[0])
			}
		}
		if err != nil {
			attrs = append(attrs, "error", err)
			logger.WarnContext(ctx, "gRPC request", attrs...)
		} else {
			logger.InfoContext(ctx, "gRPC request", attrs...)
		}
		return response, err
	}))
	pb.RegisterDiagnosticsServer(server, agent)
	healthServer := health.NewServer()
	healthpb.RegisterHealthServer(server, healthServer)
	healthServer.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	healthServer.SetServingStatus(pb.Diagnostics_ServiceDesc.ServiceName, healthpb.HealthCheckResponse_SERVING)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	logger.Info("simulator started", "address", address)
	select {
	case err := <-serveErr:
		server.Stop()
		if err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			return fmt.Errorf("serve gRPC: %w", err)
		}
	case <-ctx.Done():
		healthServer.Shutdown()
		done := make(chan struct{})
		go func() {
			server.GracefulStop()
			close(done)
		}()
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
			logger.Warn("forcing simulator shutdown after grace period")
			server.Stop()
			<-done
		}
	}
	logger.Info("simulator stopped")
	return nil
}

func healthcheck(address string) error {
	if strings.HasPrefix(address, ":") {
		address = "127.0.0.1" + address
	}
	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	response, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{Service: pb.Diagnostics_ServiceDesc.ServiceName})
	if err != nil {
		return err
	}
	if response.Status != healthpb.HealthCheckResponse_SERVING {
		return fmt.Errorf("gRPC health is %s", response.Status)
	}
	return nil
}

func envDefault(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}
