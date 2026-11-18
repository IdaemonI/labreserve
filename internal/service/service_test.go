package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/IdaemonI/labreserve/internal/domain"
)

// These functions observe boundary calls; database invariants are tested using
// PostgreSQL in the integration suite rather than reproduced in a fake store.
type repositoryStub struct {
	create  func(context.Context, string, string) (domain.Reservation, error)
	exists  func(context.Context, string) (bool, error)
	release func(context.Context, string) error
	history func(context.Context, string, int, int) ([]domain.Reservation, error)
}

func (r repositoryStub) ListDevices(context.Context) ([]domain.Device, error) { return nil, nil }
func (r repositoryStub) Ping(context.Context) error                           { return nil }
func (r repositoryStub) CreateReservation(ctx context.Context, d, o string) (domain.Reservation, error) {
	return r.create(ctx, d, o)
}
func (r repositoryStub) DeviceExists(ctx context.Context, id string) (bool, error) {
	return r.exists(ctx, id)
}
func (r repositoryStub) ReleaseReservation(ctx context.Context, id string) error {
	return r.release(ctx, id)
}
func (r repositoryStub) History(ctx context.Context, id string, l, o int) ([]domain.Reservation, error) {
	return r.history(ctx, id, l, o)
}

type inspectorFunc func(context.Context, string) (domain.Diagnostic, error)

func (f inspectorFunc) Inspect(ctx context.Context, id string) (domain.Diagnostic, error) {
	return f(ctx, id)
}

func TestCreateValidationAndNormalization(t *testing.T) {
	for _, tc := range []struct{ name, device, owner string }{
		{"missing owner", "sensor-a1", ""}, {"whitespace", "sensor-a1", " \t\n "},
		{"long owner", "sensor-a1", strings.Repeat("я", 101)}, {"control", "sensor-a1", "a\x00b"},
		{"invalid utf8", "sensor-a1", string([]byte{0xff})}, {"missing device", "", "engineer"},
		{"spaces in device", " sensor-a1", "engineer"}, {"uppercase", "Sensor", "engineer"},
		{"leading hyphen", "-sensor", "engineer"}, {"long device", strings.Repeat("x", 65), "engineer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := repositoryStub{create: func(context.Context, string, string) (domain.Reservation, error) {
				t.Fatal("invalid input reached database")
				return domain.Reservation{}, nil
			}}
			_, err := New(repo, nil, time.Second).CreateReservation(context.Background(), tc.device, tc.owner)
			if !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	repo := repositoryStub{create: func(ctx context.Context, device, owner string) (domain.Reservation, error) {
		if device != "sensor-a1" || owner != "инженер" {
			t.Fatalf("received %q %q", device, owner)
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("database call has no deadline")
		}
		return domain.Reservation{Owner: owner}, nil
	}}
	if _, err := New(repo, nil, time.Second).CreateReservation(context.Background(), "sensor-a1", "  инженер  "); err != nil {
		t.Fatal(err)
	}
}

func TestDependencyErrorsArePreserved(t *testing.T) {
	for _, want := range []error{domain.ErrDeviceOccupied, domain.ErrDeviceNotFound, errors.New("unexpected database failure")} {
		repo := repositoryStub{create: func(context.Context, string, string) (domain.Reservation, error) { return domain.Reservation{}, want }}
		_, err := New(repo, nil, time.Second).CreateReservation(context.Background(), "sensor-a1", "engineer")
		if !errors.Is(err, want) {
			t.Fatalf("got %v, want %v", err, want)
		}
	}
}

func TestReleaseUUIDValidation(t *testing.T) {
	calls := 0
	repo := repositoryStub{release: func(ctx context.Context, id string) error {
		calls++
		if id != "f173de11-13e4-4cbe-9557-ffab003022ba" {
			t.Fatalf("unexpected canonical UUID %s", id)
		}
		return nil
	}}
	s := New(repo, nil, time.Second)
	for _, id := range []string{"bad", "f173de1113e44cbe9557ffab003022ba", "{f173de11-13e4-4cbe-9557-ffab003022ba}"} {
		if err := s.ReleaseReservation(context.Background(), id); !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("id %q error %v", id, err)
		}
	}
	if err := s.ReleaseReservation(context.Background(), "F173DE11-13E4-4CBE-9557-FFAB003022BA"); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("database calls = %d", calls)
	}
}

func TestDiagnosticsVerifiesDeviceBeforeRPC(t *testing.T) {
	for _, exists := range []bool{false, true} {
		t.Run(map[bool]string{false: "unknown", true: "known"}[exists], func(t *testing.T) {
			called := false
			repo := repositoryStub{exists: func(context.Context, string) (bool, error) { return exists, nil }}
			inspector := inspectorFunc(func(ctx context.Context, id string) (domain.Diagnostic, error) {
				called = true
				if ctx.Err() != nil {
					t.Fatal("database context cancellation leaked to RPC")
				}
				return domain.Diagnostic{Health: "FAULT"}, nil
			})
			result, err := New(repo, inspector, time.Second).Diagnostics(context.Background(), "adapter-b1")
			if !exists && !errors.Is(err, domain.ErrDeviceNotFound) {
				t.Fatalf("error %v", err)
			}
			if exists && (err != nil || result.Health != "FAULT") {
				t.Fatalf("result %v error %v", result, err)
			}
			if called != exists {
				t.Fatalf("RPC called = %v", called)
			}
		})
	}
}

func TestDatabaseDeadlineBoundsWait(t *testing.T) {
	repo := repositoryStub{create: func(ctx context.Context, _, _ string) (domain.Reservation, error) {
		<-ctx.Done()
		return domain.Reservation{}, ctx.Err()
	}}
	start := time.Now()
	_, err := New(repo, nil, 20*time.Millisecond).CreateReservation(context.Background(), "sensor-a1", "engineer")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("database deadline did not bound request")
	}
}

func TestHistoryRejectsPaginationBeforeDatabase(t *testing.T) {
	s := New(repositoryStub{}, nil, time.Second)
	for _, p := range [][2]int{{0, 0}, {101, 0}, {20, -1}, {20, 2147483648}} {
		_, err := s.History(context.Background(), "sensor-a1", p[0], p[1])
		if !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("pagination %v error %v", p, err)
		}
	}
}
