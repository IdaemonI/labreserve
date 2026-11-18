// Package domain defines reservation records and errors shared across adapters.
package domain

import (
	"errors"
	"time"
)

var (
	ErrDeviceNotFound      = errors.New("device not found")
	ErrReservationNotFound = errors.New("reservation not found")
	ErrDeviceOccupied      = errors.New("device already has an active reservation")
	ErrInvalidInput        = errors.New("invalid input")
	ErrUnavailable         = errors.New("diagnostics unavailable")
	ErrDeadline            = errors.New("diagnostics deadline exceeded")
	ErrUpstream            = errors.New("unexpected diagnostics response")
)

type Device struct {
	ID                  string  `json:"id"`
	Name                string  `json:"name"`
	Kind                string  `json:"kind"`
	Occupied            bool    `json:"occupied"`
	ActiveReservationID *string `json:"active_reservation_id"`
}

type Reservation struct {
	ID         string     `json:"id"`
	DeviceID   string     `json:"device_id"`
	Owner      string     `json:"owner"`
	CreatedAt  time.Time  `json:"created_at"`
	ReleasedAt *time.Time `json:"released_at"`
}

type History struct {
	Items  []Reservation `json:"items"`
	Limit  int           `json:"limit"`
	Offset int           `json:"offset"`
}

type Diagnostic struct {
	DeviceID       string    `json:"device_id"`
	Health         string    `json:"health"`
	DiagnosticCode string    `json:"diagnostic_code"`
	ObservedAt     time.Time `json:"observed_at"`
}
