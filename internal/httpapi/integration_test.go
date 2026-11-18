//go:build integration

package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/IdaemonI/labreserve/gen/diagnostics/v1"
	"github.com/IdaemonI/labreserve/internal/diagnostics"
	"github.com/IdaemonI/labreserve/internal/domain"
	"github.com/IdaemonI/labreserve/internal/service"
	"github.com/IdaemonI/labreserve/internal/storage/postgres"
	"github.com/IdaemonI/labreserve/internal/testdb"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
)

func integrationHandler(pool *pgxpool.Pool, inspector service.Inspector) http.Handler {
	return New(service.New(postgres.New(pool), inspector, 2*time.Second), slog.New(slog.NewJSONHandler(io.Discard, nil)), 3*time.Second)
}

func apiCall(client *http.Client, method, url, body string) (int, []byte, error) {
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	return resp.StatusCode, data, err
}

func TestHTTPConcurrentBookingReleaseAndReconnect(t *testing.T) {
	pool := testdb.Open(t)
	server := httptest.NewServer(integrationHandler(pool, inspectorStub{}))
	t.Cleanup(server.Close)
	client := &http.Client{Timeout: 5 * time.Second}
	const attempts = 24
	type result struct {
		status int
		data   []byte
		err    error
	}
	results := make(chan result, attempts)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			status, data, err := apiCall(client, "POST", server.URL+"/reservations", fmt.Sprintf(`{"device_id":"sensor-a1","owner":"engineer-%d"}`, i))
			results <- result{status, data, err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	created, conflicts := 0, 0
	var first domain.Reservation
	for r := range results {
		if r.err != nil {
			t.Fatal(r.err)
		}
		switch r.status {
		case 201:
			created++
			if err := json.Unmarshal(r.data, &first); err != nil {
				t.Fatal(err)
			}
		case 409:
			conflicts++
		default:
			t.Fatalf("unexpected HTTP status %d: %s", r.status, r.data)
		}
	}
	if created != 1 || conflicts != attempts-1 {
		t.Fatalf("created=%d conflicts=%d", created, conflicts)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var active int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM reservations WHERE device_id=$1 AND released_at IS NULL`, "sensor-a1").Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 1 {
		t.Fatalf("SQL active count=%d", active)
	}
	status, data, err := apiCall(client, "GET", server.URL+"/devices", "")
	if err != nil || status != 200 {
		t.Fatalf("devices status=%d error=%v", status, err)
	}
	var devices []domain.Device
	if err := json.Unmarshal(data, &devices); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range devices {
		if d.ID == "sensor-a1" {
			found = d.Occupied && d.ActiveReservationID != nil && *d.ActiveReservationID == first.ID
		}
	}
	if !found {
		t.Fatal("device occupancy does not reflect SQL reservation")
	}
	if status, _, err := apiCall(client, "DELETE", server.URL+"/reservations/"+first.ID, ""); err != nil || status != 204 {
		t.Fatalf("release status=%d err=%v", status, err)
	}
	var releasedAt time.Time
	if err := pool.QueryRow(ctx, `SELECT released_at FROM reservations WHERE id=$1`, first.ID).Scan(&releasedAt); err != nil {
		t.Fatal(err)
	}
	status, data, err = apiCall(client, "POST", server.URL+"/reservations", `{"device_id":"sensor-a1","owner":"engineer-new"}`)
	if err != nil || status != 201 {
		t.Fatalf("second booking status=%d body=%s err=%v", status, data, err)
	}
	var second domain.Reservation
	if err := json.Unmarshal(data, &second); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if status, _, err := apiCall(client, "DELETE", server.URL+"/reservations/"+first.ID, ""); err != nil || status != 204 {
			t.Fatalf("repeat release status=%d err=%v", status, err)
		}
	}
	var after time.Time
	if err := pool.QueryRow(ctx, `SELECT released_at FROM reservations WHERE id=$1`, first.ID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if !releasedAt.Equal(after) {
		t.Fatal("repeat release changed original timestamp")
	}
	var stillActive bool
	if err := pool.QueryRow(ctx, `SELECT released_at IS NULL FROM reservations WHERE id=$1`, second.ID).Scan(&stillActive); err != nil {
		t.Fatal(err)
	}
	if !stillActive {
		t.Fatal("old release affected new reservation")
	}
	// Restart both the HTTP transport and the pool; no memory state survives.
	server.Close()
	config := pool.Config()
	pool.Close()
	reconnected, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reconnected.Close)
	restarted := httptest.NewServer(integrationHandler(reconnected, inspectorStub{}))
	t.Cleanup(restarted.Close)
	status, data, err = apiCall(client, "GET", restarted.URL+"/devices/sensor-a1/reservations?limit=1&offset=0", "")
	if err != nil || status != 200 {
		t.Fatalf("restart history status=%d error=%v body=%s", status, err, data)
	}
	var history domain.History
	if err := json.Unmarshal(data, &history); err != nil {
		t.Fatal(err)
	}
	if len(history.Items) != 1 || history.Items[0].ID != second.ID || history.Items[0].ReleasedAt != nil {
		t.Fatalf("persisted history=%+v", history)
	}
}

func TestHTTPDiagnosticsThroughRealGRPC(t *testing.T) {
	pool := testdb.Open(t)
	for _, tc := range []struct {
		name, mode, device string
		want               int
	}{
		{"healthy", "normal", "sensor-a1", 200}, {"fault", "normal", "adapter-b1", 200},
		{"unknown", "normal", "unknown", 404}, {"deadline", "normal", "meter-c1", 504},
		{"upstream error", "error", "sensor-a1", 502}, {"unavailable", "unavailable", "sensor-a1", 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			agent, err := diagnostics.NewServer(time.Hour, tc.mode)
			if err != nil {
				t.Fatal(err)
			}
			grpcServer := grpc.NewServer()
			pb.RegisterDiagnosticsServer(grpcServer, agent)
			go func() {
				if err := grpcServer.Serve(listener); err != nil {
					t.Errorf("gRPC serve: %v", err)
				}
			}()
			t.Cleanup(grpcServer.Stop)
			inspector, err := diagnostics.NewClient(listener.Addr().String(), 250*time.Millisecond)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := inspector.Close(); err != nil {
					t.Error(err)
				}
			})
			h := integrationHandler(pool, inspector)
			w := request(h, "GET", "/devices/"+tc.device+"/diagnostics", "", "")
			if w.Code != tc.want {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if tc.name == "fault" && !strings.Contains(w.Body.String(), `"health":"FAULT"`) {
				t.Fatalf("body %s", w.Body.String())
			}
			if tc.want >= 500 {
				w = request(h, "GET", "/readyz", "", "")
				if w.Code != 200 {
					t.Fatalf("gRPC failure broke readiness: %d", w.Code)
				}
			}
		})
	}
}
