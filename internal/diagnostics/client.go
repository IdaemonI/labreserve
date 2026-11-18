package diagnostics

import (
	"context"
	"fmt"
	"strings"
	"time"

	pb "github.com/IdaemonI/labreserve/gen/diagnostics/v1"
	"github.com/IdaemonI/labreserve/internal/domain"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

type Client struct {
	conn    *grpc.ClientConn
	rpc     pb.DiagnosticsClient
	timeout time.Duration
}

// NewClient reuses a single connection. Plaintext is for the local teaching demo.
// A successful constructor does not imply upstream availability; each RPC is bounded.
func NewClient(target string, timeout time.Duration) (*Client, error) {
	if strings.TrimSpace(target) == "" || timeout <= 0 {
		return nil, fmt.Errorf("diagnostics target and positive timeout are required")
	}
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("create diagnostics connection: %w", err)
	}
	return &Client{conn: conn, rpc: pb.NewDiagnosticsClient(conn), timeout: timeout}, nil
}

func (c *Client) Close() error { return c.conn.Close() }

func (c *Client) Inspect(ctx context.Context, id string) (domain.Diagnostic, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	response, err := c.rpc.InspectDevice(ctx, &pb.InspectDeviceRequest{DeviceId: id})
	if err != nil {
		// Canceled is meaningful as a caller cancellation only when our context
		// was actually canceled. A live caller must not receive HTTP 408 merely
		// because an upstream handler independently returned gRPC Canceled.
		if ctxErr := ctx.Err(); ctxErr != nil {
			cause := ctxErr
			if ctxErr == context.DeadlineExceeded {
				cause = domain.ErrDeadline
			}
			return domain.Diagnostic{}, fmt.Errorf("inspect device: %w: %v", cause, err)
		}
		return domain.Diagnostic{}, classifyError(err)
	}
	if response == nil || response.GetDeviceId() != id || response.ObservedAt == nil ||
		response.ObservedAt.CheckValid() != nil || strings.TrimSpace(response.DiagnosticCode) == "" ||
		len(response.DiagnosticCode) > 128 {
		return domain.Diagnostic{}, fmt.Errorf("%w: invalid snapshot", domain.ErrUpstream)
	}
	var health string
	switch response.GetHealth() {
	case pb.Health_HEALTH_OK:
		health = "OK"
	case pb.Health_HEALTH_FAULT:
		health = "FAULT"
	default:
		return domain.Diagnostic{}, fmt.Errorf("%w: unspecified or unknown health", domain.ErrUpstream)
	}
	return domain.Diagnostic{
		DeviceID: id, Health: health, DiagnosticCode: response.DiagnosticCode,
		ObservedAt: response.ObservedAt.AsTime().UTC(),
	}, nil
}

func classifyError(err error) error {
	var cause error
	switch status.Code(err) {
	case codes.NotFound:
		cause = domain.ErrDeviceNotFound
	case codes.Unavailable:
		cause = domain.ErrUnavailable
	case codes.DeadlineExceeded:
		cause = domain.ErrDeadline
	default:
		cause = domain.ErrUpstream
	}
	return fmt.Errorf("inspect device: %w: %v", cause, err)
}
