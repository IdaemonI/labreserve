package diagnostics

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	pb "github.com/IdaemonI/labreserve/gen/diagnostics/v1"
	"github.com/IdaemonI/labreserve/internal/domain"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func transportClient(t *testing.T, agent pb.DiagnosticsServer, timeout time.Duration) *Client {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	pb.RegisterDiagnosticsServer(server, agent)
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	conn, err := grpc.NewClient("passthrough:///diagnostics-test", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }))
	if err != nil {
		t.Fatal(err)
	}
	conn.Connect()
	readyCtx, readyCancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer readyCancel()
	for state := conn.GetState(); state != connectivity.Ready; state = conn.GetState() {
		if !conn.WaitForStateChange(readyCtx, state) {
			conn.Close()
			server.Stop()
			t.Fatal("gRPC test transport did not become ready")
		}
	}
	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Errorf("close client: %v", err)
		}
		server.Stop()
		if err := <-serveErr; err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			t.Errorf("serve: %v", err)
		}
		if err := listener.Close(); err != nil {
			t.Errorf("close listener: %v", err)
		}
	})
	return &Client{conn: conn, rpc: pb.NewDiagnosticsClient(conn), timeout: timeout}
}

func TestAgentSnapshots(t *testing.T) {
	agent, err := NewServer(0, "normal")
	if err != nil {
		t.Fatal(err)
	}
	client := transportClient(t, agent, time.Second)
	for _, tc := range []struct{ id, health, code string }{
		{"sensor-a1", "OK", "NONE"},
		{"adapter-b1", "FAULT", "INTERFACE_ERROR"},
		{"meter-c1", "OK", "NONE"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			before := time.Now().UTC()
			snapshot, err := client.Inspect(t.Context(), tc.id)
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.DeviceID != tc.id || snapshot.Health != tc.health || snapshot.DiagnosticCode != tc.code {
				t.Fatalf("unexpected snapshot: %+v", snapshot)
			}
			if snapshot.ObservedAt.Before(before) || snapshot.ObservedAt.After(time.Now().UTC()) || snapshot.ObservedAt.Location() != time.UTC {
				t.Fatalf("invalid observed timestamp: %v", snapshot.ObservedAt)
			}
		})
	}
	if _, err := client.Inspect(t.Context(), "unknown"); !errors.Is(err, domain.ErrDeviceNotFound) {
		t.Fatalf("unknown device: got %v", err)
	}
	if _, err := agent.InspectDevice(t.Context(), &pb.InspectDeviceRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("empty ID: got %v", err)
	}
}

func TestAgentFailureModes(t *testing.T) {
	for _, tc := range []struct {
		mode string
		want error
	}{
		{"error", domain.ErrUpstream},
		{"unavailable", domain.ErrUnavailable},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			agent, err := NewServer(0, tc.mode)
			if err != nil {
				t.Fatal(err)
			}
			client := transportClient(t, agent, time.Second)
			if _, err := client.Inspect(t.Context(), "sensor-a1"); !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
		})
	}
}

type observedServer struct {
	pb.UnimplementedDiagnosticsServer
	agent    *Server
	started  chan struct{}
	finished chan struct{}
}

func (s *observedServer) InspectDevice(ctx context.Context, req *pb.InspectDeviceRequest) (*pb.InspectDeviceResponse, error) {
	close(s.started)
	defer close(s.finished)
	return s.agent.InspectDevice(ctx, req)
}

func await(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for gRPC handler")
	}
}

func TestDeadlineInterruptsDelayedAgent(t *testing.T) {
	agent, err := NewServer(time.Hour, "normal")
	if err != nil {
		t.Fatal(err)
	}
	observed := &observedServer{agent: agent, started: make(chan struct{}), finished: make(chan struct{})}
	client := transportClient(t, observed, 100*time.Millisecond)
	startedAt := time.Now()
	if _, err := client.Inspect(t.Context(), "meter-c1"); !errors.Is(err, domain.ErrDeadline) {
		t.Fatalf("want deadline, got %v", err)
	}
	await(t, observed.started)
	await(t, observed.finished)
	if elapsed := time.Since(startedAt); elapsed > 2*time.Second {
		t.Fatalf("deadline did not bound the call: %v", elapsed)
	}
}

func TestCancellationPropagatesToAgent(t *testing.T) {
	agent, err := NewServer(time.Hour, "normal")
	if err != nil {
		t.Fatal(err)
	}
	observed := &observedServer{agent: agent, started: make(chan struct{}), finished: make(chan struct{})}
	client := transportClient(t, observed, time.Hour)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := client.Inspect(ctx, "meter-c1")
		result <- err
	}()
	await(t, observed.started)
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("want cancellation, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("client did not stop on cancellation")
	}
	await(t, observed.finished)
}

type responseServer struct {
	pb.UnimplementedDiagnosticsServer
	response *pb.InspectDeviceResponse
	err      error
}

func (s *responseServer) InspectDevice(context.Context, *pb.InspectDeviceRequest) (*pb.InspectDeviceResponse, error) {
	return s.response, s.err
}

func TestClientRejectsMalformedSnapshots(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*pb.InspectDeviceResponse)
	}{
		{"wrong device", func(r *pb.InspectDeviceResponse) { r.DeviceId = "adapter-b1" }},
		{"missing timestamp", func(r *pb.InspectDeviceResponse) { r.ObservedAt = nil }},
		{"invalid timestamp", func(r *pb.InspectDeviceResponse) { r.ObservedAt.Seconds = 253402300800 }},
		{"unspecified health", func(r *pb.InspectDeviceResponse) { r.Health = pb.Health_HEALTH_UNSPECIFIED }},
		{"unknown health", func(r *pb.InspectDeviceResponse) { r.Health = pb.Health(99) }},
		{"empty diagnostic code", func(r *pb.InspectDeviceResponse) { r.DiagnosticCode = " " }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := &pb.InspectDeviceResponse{DeviceId: "sensor-a1", Health: pb.Health_HEALTH_OK, DiagnosticCode: "NONE", ObservedAt: timestamppb.Now()}
			tc.mutate(response)
			client := transportClient(t, &responseServer{response: response}, time.Second)
			if _, err := client.Inspect(t.Context(), "sensor-a1"); !errors.Is(err, domain.ErrUpstream) {
				t.Fatalf("malformed response accepted: %v", err)
			}
		})
	}
}

func TestClientClassifiesStatuses(t *testing.T) {
	for _, tc := range []struct {
		code codes.Code
		want error
	}{
		{codes.Unavailable, domain.ErrUnavailable},
		{codes.DeadlineExceeded, domain.ErrDeadline},
		{codes.NotFound, domain.ErrDeviceNotFound},
		{codes.Canceled, domain.ErrUpstream},
		{codes.Internal, domain.ErrUpstream},
		{codes.PermissionDenied, domain.ErrUpstream},
	} {
		t.Run(tc.code.String(), func(t *testing.T) {
			client := transportClient(t, &responseServer{err: status.Error(tc.code, "synthetic")}, time.Second)
			if _, err := client.Inspect(t.Context(), "sensor-a1"); !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
		})
	}
}

func TestUnavailableTransport(t *testing.T) {
	conn, err := grpc.NewClient("passthrough:///unavailable", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return nil, io.EOF }))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := &Client{conn: conn, rpc: pb.NewDiagnosticsClient(conn), timeout: time.Second}
	if _, err := client.Inspect(t.Context(), "sensor-a1"); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("want unavailable, got %v", err)
	}
}

func TestConfigurationValidation(t *testing.T) {
	if _, err := NewServer(-time.Second, "normal"); err == nil {
		t.Fatal("negative delay accepted")
	}
	if _, err := NewServer(0, "unknown"); err == nil {
		t.Fatal("unknown mode accepted")
	}
	if _, err := NewClient("", time.Second); err == nil {
		t.Fatal("empty target accepted")
	}
	if _, err := NewClient("localhost:9090", 0); err == nil {
		t.Fatal("zero timeout accepted")
	}
}
