//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/IdaemonI/labreserve/db"
	"github.com/IdaemonI/labreserve/internal/domain"
	"github.com/IdaemonI/labreserve/internal/testdb"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
)

func fixture(t *testing.T) (*Store, *pgxpool.Config) {
	t.Helper()
	pool := testdb.Open(t)
	return New(pool), pool.Config()
}

func TestIntegrationConcurrentReservation(t *testing.T) {
	store, _ := fixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	const attempts = 24
	start := make(chan struct{})
	results := make(chan error, attempts)
	var workers sync.WaitGroup
	for range attempts {
		workers.Go(func() {
			<-start
			_, err := store.CreateReservation(ctx, "sensor-a1", "concurrent-engineer")
			results <- err
		})
	}
	close(start)
	workers.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, domain.ErrDeviceOccupied):
			conflicts++
		default:
			t.Errorf("unexpected create error: %v", err)
		}
	}
	if successes != 1 || conflicts != attempts-1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
	var active int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM reservations WHERE device_id=$1 AND released_at IS NULL`, "sensor-a1").Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 1 {
		t.Fatalf("SQL active reservation count=%d, want 1", active)
	}
	devices, err := store.ListDevices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, device := range devices {
		if device.ID == "sensor-a1" && (!device.Occupied || device.ActiveReservationID == nil) {
			t.Fatalf("database-backed occupancy missing: %+v", device)
		}
	}
}

func TestIntegrationReleaseDoesNotAffectNewReservation(t *testing.T) {
	store, _ := fixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	first, err := store.CreateReservation(ctx, "sensor-a1", "first-engineer")
	if err != nil {
		t.Fatal(err)
	}
	// Synchronize the first releases while the row is still active. Concurrent
	// UPDATEs must all succeed, but only the first one assigns released_at.
	start := make(chan struct{})
	var workers sync.WaitGroup
	for range 12 {
		workers.Go(func() {
			<-start
			if err := store.ReleaseReservation(ctx, first.ID); err != nil {
				t.Errorf("concurrent first release: %v", err)
			}
		})
	}
	close(start)
	workers.Wait()
	var releasedAt time.Time
	if err := store.pool.QueryRow(ctx, `SELECT released_at FROM reservations WHERE id=$1`, first.ID).Scan(&releasedAt); err != nil {
		t.Fatal(err)
	}
	second, err := store.CreateReservation(ctx, "sensor-a1", "second-engineer")
	if err != nil {
		t.Fatal(err)
	}
	for range 12 {
		workers.Go(func() {
			if err := store.ReleaseReservation(ctx, first.ID); err != nil {
				t.Errorf("repeat release: %v", err)
			}
		})
	}
	workers.Wait()
	var stillReleasedAt time.Time
	if err := store.pool.QueryRow(ctx, `SELECT released_at FROM reservations WHERE id=$1`, first.ID).Scan(&stillReleasedAt); err != nil {
		t.Fatal(err)
	}
	if !releasedAt.Equal(stillReleasedAt) {
		t.Fatalf("repeat changed first release time from %v to %v", releasedAt, stillReleasedAt)
	}
	var activeID string
	if err := store.pool.QueryRow(ctx, `SELECT id::text FROM reservations WHERE device_id=$1 AND released_at IS NULL`, "sensor-a1").Scan(&activeID); err != nil {
		t.Fatal(err)
	}
	if activeID != second.ID {
		t.Fatalf("active ID %q, want %q", activeID, second.ID)
	}
	if err := store.ReleaseReservation(ctx, uuid.NewString()); !errors.Is(err, domain.ErrReservationNotFound) {
		t.Fatalf("unknown release: %v", err)
	}
}

func TestIntegrationPersistsAfterPoolReconnect(t *testing.T) {
	store, config := fixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	created, err := store.CreateReservation(ctx, "sensor-a1", "persistent-engineer")
	if err != nil {
		t.Fatal(err)
	}
	store.pool.Close()
	pool, err := pgxpool.NewWithConfig(ctx, config.Copy())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	items, err := New(pool).History(ctx, "sensor-a1", 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != created.ID || items[0].ReleasedAt != nil {
		t.Fatalf("reservation lost on reconnect: %+v", items)
	}
}

func TestIntegrationHistoryAndConstraints(t *testing.T) {
	store, _ := fixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := store.CreateReservation(ctx, "unknown-device", "engineer"); !errors.Is(err, domain.ErrDeviceNotFound) {
		t.Fatalf("unknown device error: %v", err)
	}
	for _, owner := range []string{"   ", "embedded\nnewline", "embedded\ttab", " leading-space"} {
		if _, err := store.CreateReservation(ctx, "sensor-a1", owner); err == nil || errors.Is(err, domain.ErrDeviceOccupied) {
			t.Fatalf("invalid owner %q was accepted or masked: %v", owner, err)
		}
	}
	timestamp := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	ids := []string{
		"00000000-0000-4000-8000-000000000001",
		"00000000-0000-4000-8000-000000000002",
		"00000000-0000-4000-8000-000000000003",
	}
	for _, id := range ids {
		if _, err := store.pool.Exec(ctx, `INSERT INTO reservations(id,device_id,owner,created_at,released_at) VALUES($1,$2,$3,$4,$4)`, id, "sensor-a1", "history-engineer", timestamp); err != nil {
			t.Fatal(err)
		}
	}
	items, err := store.History(ctx, "sensor-a1", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != ids[1] {
		t.Fatalf("unstable history or pagination: %+v", items)
	}
	exists, err := store.DeviceExists(ctx, "unknown-device")
	if err != nil || exists {
		t.Fatalf("unknown exists=%v error=%v", exists, err)
	}
	items, err = store.History(ctx, "adapter-b1", 20, 0)
	if err != nil || items == nil || len(items) != 0 {
		t.Fatalf("empty history must encode as []: %+v %v", items, err)
	}
}

func TestIntegrationMigrationsAreRepeatable(t *testing.T) {
	store, config := fixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	database := sql.OpenDB(stdlib.GetConnector(*config.ConnConfig))
	defer database.Close()
	if err := db.Apply(ctx, database); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM devices`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("repeated migrations produced %d devices", count)
	}
}

func TestIntegrationDatabaseContextCancellation(t *testing.T) {
	store, _ := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.ListDevices(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("database operation ignored cancellation: %v", err)
	}
	ctx, timeoutCancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer timeoutCancel()
	started := time.Now()
	_, err := store.pool.Exec(ctx, `SELECT pg_sleep(10)`)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("database query ignored deadline: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("database cancellation took %s", elapsed)
	}
}
