// Package diagnostics implements the synthetic gRPC agent and its controller client.
package diagnostics

import (
	"context"
	"fmt"
	"time"

	pb "github.com/IdaemonI/labreserve/gen/diagnostics/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Server exposes deterministic diagnostics for the three seeded devices.
type Server struct {
	pb.UnimplementedDiagnosticsServer
	delay time.Duration
	mode  string
}

// NewServer accepts normal, error and unavailable modes. Only meter-c1 is delayed.
func NewServer(delay time.Duration, mode string) (*Server, error) {
	if delay < 0 {
		return nil, fmt.Errorf("simulator delay must not be negative")
	}
	switch mode {
	case "normal", "error", "unavailable":
	default:
		return nil, fmt.Errorf("unsupported simulator mode %q", mode)
	}
	return &Server{delay: delay, mode: mode}, nil
}

func (s *Server) InspectDevice(ctx context.Context, req *pb.InspectDeviceRequest) (*pb.InspectDeviceResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	id := req.GetDeviceId()
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "device_id is required")
	}
	var health pb.Health
	var code string
	switch id {
	case "sensor-a1", "meter-c1":
		health, code = pb.Health_HEALTH_OK, "NONE"
	case "adapter-b1":
		health, code = pb.Health_HEALTH_FAULT, "INTERFACE_ERROR"
	default:
		return nil, status.Error(codes.NotFound, "device not found")
	}
	if s.mode == "error" {
		return nil, status.Error(codes.Internal, "synthetic upstream error")
	}
	if s.mode == "unavailable" {
		return nil, status.Error(codes.Unavailable, "synthetic upstream unavailable")
	}
	if id == "meter-c1" && s.delay > 0 {
		timer := time.NewTimer(s.delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil, status.FromContextError(ctx.Err()).Err()
		case <-timer.C:
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	return &pb.InspectDeviceResponse{
		DeviceId: id, Health: health, DiagnosticCode: code,
		ObservedAt: timestamppb.New(time.Now().UTC()),
	}, nil
}
