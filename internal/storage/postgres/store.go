// Package postgres persists devices and reservations using parameterized SQL.
package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/IdaemonI/labreserve/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

func (s *Store) ListDevices(ctx context.Context) ([]domain.Device, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT d.id, d.name, d.kind, r.id IS NOT NULL, r.id::text
		FROM devices d
		LEFT JOIN reservations r ON r.device_id = d.id AND r.released_at IS NULL
		ORDER BY d.id`)
	if err != nil {
		return nil, fmt.Errorf("list devices: %w", err)
	}
	defer rows.Close()
	devices := make([]domain.Device, 0)
	for rows.Next() {
		var device domain.Device
		if err := rows.Scan(&device.ID, &device.Name, &device.Kind, &device.Occupied, &device.ActiveReservationID); err != nil {
			return nil, fmt.Errorf("scan device: %w", err)
		}
		devices = append(devices, device)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read devices: %w", err)
	}
	return devices, nil
}

func (s *Store) DeviceExists(ctx context.Context, id string) (bool, error) {
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM devices WHERE id = $1)`, id).Scan(&exists); err != nil {
		return false, fmt.Errorf("find device: %w", err)
	}
	return exists, nil
}

func (s *Store) CreateReservation(ctx context.Context, deviceID, owner string) (domain.Reservation, error) {
	var reservation domain.Reservation
	id, err := uuid.NewRandom()
	if err != nil {
		return reservation, fmt.Errorf("generate reservation ID: %w", err)
	}
	err = s.pool.QueryRow(ctx, `
		INSERT INTO reservations (id, device_id, owner)
		VALUES ($1, $2, $3)
		RETURNING id::text, device_id, owner, created_at, released_at`,
		id.String(), deviceID, owner).Scan(
		&reservation.ID, &reservation.DeviceID, &reservation.Owner,
		&reservation.CreatedAt, &reservation.ReleasedAt)
	if err != nil {
		return domain.Reservation{}, fmt.Errorf("create reservation: %w", classifyCreateError(err))
	}
	reservation.CreatedAt = reservation.CreatedAt.UTC()
	return reservation, nil
}

// classifyCreateError translates only the two constraints with domain meaning.
// An unrelated unique constraint, connection error or syntax error stays internal.
func classifyCreateError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch {
		case pgErr.Code == "23505" && pgErr.ConstraintName == "one_active_reservation_per_device":
			return domain.ErrDeviceOccupied
		case pgErr.Code == "23503" && pgErr.ConstraintName == "reservations_device_id_fkey":
			return domain.ErrDeviceNotFound
		}
	}
	return err
}

// ReleaseReservation targets one immutable reservation ID. PostgreSQL serializes
// concurrent updates on that row, and COALESCE preserves the first release time.
func (s *Store) ReleaseReservation(ctx context.Context, id string) error {
	result, err := s.pool.Exec(ctx, `
		UPDATE reservations
		SET released_at = COALESCE(released_at, clock_timestamp())
		WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("release reservation: %w", err)
	}
	if result.RowsAffected() == 0 {
		return domain.ErrReservationNotFound
	}
	return nil
}

// History uses the same stable ordering as db/analysis/history.sql.
func (s *Store) History(ctx context.Context, deviceID string, limit, offset int) ([]domain.Reservation, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id::text, device_id, owner, created_at, released_at
		FROM reservations
		WHERE device_id = $1
		ORDER BY created_at DESC, reservations.id DESC
		LIMIT $2 OFFSET $3`, deviceID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list reservation history: %w", err)
	}
	defer rows.Close()
	items := make([]domain.Reservation, 0)
	for rows.Next() {
		var reservation domain.Reservation
		if err := rows.Scan(&reservation.ID, &reservation.DeviceID, &reservation.Owner,
			&reservation.CreatedAt, &reservation.ReleasedAt); err != nil {
			return nil, fmt.Errorf("scan reservation: %w", err)
		}
		reservation.CreatedAt = reservation.CreatedAt.UTC()
		if reservation.ReleasedAt != nil {
			timestamp := reservation.ReleasedAt.UTC()
			reservation.ReleasedAt = &timestamp
		}
		items = append(items, reservation)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read reservation history: %w", err)
	}
	return items, nil
}
