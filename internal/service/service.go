// Package service coordinates validation, persistence and read-only diagnostics.
package service

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/IdaemonI/labreserve/internal/domain"
	"github.com/google/uuid"
)

type Repository interface {
	ListDevices(context.Context) ([]domain.Device, error)
	DeviceExists(context.Context, string) (bool, error)
	CreateReservation(context.Context, string, string) (domain.Reservation, error)
	ReleaseReservation(context.Context, string) error
	History(context.Context, string, int, int) ([]domain.Reservation, error)
	Ping(context.Context) error
}

type Inspector interface {
	Inspect(context.Context, string) (domain.Diagnostic, error)
}

type Service struct {
	repo            Repository
	inspector       Inspector
	databaseTimeout time.Duration
}

func New(repo Repository, inspector Inspector, databaseTimeout time.Duration) *Service {
	return &Service{repo: repo, inspector: inspector, databaseTimeout: databaseTimeout}
}

func (s *Service) ListDevices(ctx context.Context) ([]domain.Device, error) {
	ctx, cancel := context.WithTimeout(ctx, s.databaseTimeout)
	defer cancel()
	return s.repo.ListDevices(ctx)
}

func (s *Service) CreateReservation(ctx context.Context, deviceID, owner string) (domain.Reservation, error) {
	if err := validateDeviceID(deviceID); err != nil {
		return domain.Reservation{}, err
	}
	owner = strings.TrimSpace(owner)
	if !utf8.ValidString(owner) || utf8.RuneCountInString(owner) < 1 || utf8.RuneCountInString(owner) > 100 || strings.ContainsFunc(owner, unicode.IsControl) {
		return domain.Reservation{}, fmt.Errorf("%w: owner must contain 1 to 100 characters without control characters", domain.ErrInvalidInput)
	}
	ctx, cancel := context.WithTimeout(ctx, s.databaseTimeout)
	defer cancel()
	return s.repo.CreateReservation(ctx, deviceID, owner)
}

func (s *Service) ReleaseReservation(ctx context.Context, id string) error {
	parsed, err := uuid.Parse(id)
	if err != nil || len(id) != 36 || !strings.EqualFold(parsed.String(), id) {
		return fmt.Errorf("%w: reservation id must be a hyphenated UUID", domain.ErrInvalidInput)
	}
	ctx, cancel := context.WithTimeout(ctx, s.databaseTimeout)
	defer cancel()
	return s.repo.ReleaseReservation(ctx, parsed.String())
}

func (s *Service) History(ctx context.Context, deviceID string, limit, offset int) (domain.History, error) {
	if err := validateDeviceID(deviceID); err != nil {
		return domain.History{}, err
	}
	if limit < 1 || limit > 100 || offset < 0 || int64(offset) > 2147483647 {
		return domain.History{}, fmt.Errorf("%w: limit must be 1..100 and offset 0..2147483647", domain.ErrInvalidInput)
	}
	ctx, cancel := context.WithTimeout(ctx, s.databaseTimeout)
	defer cancel()
	if err := s.ensureDevice(ctx, deviceID); err != nil {
		return domain.History{}, err
	}
	items, err := s.repo.History(ctx, deviceID, limit, offset)
	if err != nil {
		return domain.History{}, err
	}
	if items == nil {
		items = []domain.Reservation{}
	}
	return domain.History{Items: items, Limit: limit, Offset: offset}, nil
}

func (s *Service) Diagnostics(ctx context.Context, deviceID string) (domain.Diagnostic, error) {
	if err := validateDeviceID(deviceID); err != nil {
		return domain.Diagnostic{}, err
	}
	dbCtx, cancel := context.WithTimeout(ctx, s.databaseTimeout)
	err := s.ensureDevice(dbCtx, deviceID)
	cancel()
	if err != nil {
		return domain.Diagnostic{}, err
	}
	// No database transaction or connection is held while waiting for the agent.
	return s.inspector.Inspect(ctx, deviceID)
}

func (s *Service) Ready(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, s.databaseTimeout)
	defer cancel()
	return s.repo.Ping(ctx)
}

func (s *Service) ensureDevice(ctx context.Context, id string) error {
	exists, err := s.repo.DeviceExists(ctx, id)
	if err != nil {
		return err
	}
	if !exists {
		return domain.ErrDeviceNotFound
	}
	return nil
}

func validateDeviceID(id string) error {
	if len(id) < 1 || len(id) > 64 {
		return fmt.Errorf("%w: device id must contain 1 to 64 characters", domain.ErrInvalidInput)
	}
	for i, c := range id {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || (i > 0 && c == '-') {
			continue
		}
		return fmt.Errorf("%w: device id must match [a-z0-9][a-z0-9-]{0,63}", domain.ErrInvalidInput)
	}
	return nil
}
