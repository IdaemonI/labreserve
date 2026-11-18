package postgres

import (
	"errors"
	"fmt"
	"testing"

	"github.com/IdaemonI/labreserve/internal/domain"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestClassifyCreateError(t *testing.T) {
	tests := []struct {
		name       string
		code       string
		constraint string
		want       error
	}{
		{"active reservation conflict", "23505", "one_active_reservation_per_device", domain.ErrDeviceOccupied},
		{"unknown device", "23503", "reservations_device_id_fkey", domain.ErrDeviceNotFound},
		{"different unique constraint", "23505", "reservations_pkey", nil},
		{"same name different SQLSTATE", "23514", "one_active_reservation_per_device", nil},
		{"unrelated foreign key", "23503", "other_fkey", nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			original := fmt.Errorf("database operation: %w", &pgconn.PgError{Code: test.code, ConstraintName: test.constraint})
			got := classifyCreateError(original)
			if test.want == nil {
				if got != original {
					t.Fatalf("unrelated error was translated: %v", got)
				}
			} else if !errors.Is(got, test.want) {
				t.Fatalf("got %v, want %v", got, test.want)
			}
		})
	}
	original := errors.New("connection lost")
	if got := classifyCreateError(original); got != original {
		t.Fatalf("connection error translated: %v", got)
	}
}
