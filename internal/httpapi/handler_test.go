package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/IdaemonI/labreserve/internal/domain"
	"github.com/IdaemonI/labreserve/internal/service"
	"github.com/google/uuid"
)

type repositoryStub struct {
	createErr, releaseErr, historyErr, pingErr error
	exists                                     bool
	createdOwner                               string
	devices                                    []domain.Device
}

func (r *repositoryStub) ListDevices(context.Context) ([]domain.Device, error) { return r.devices, nil }
func (r *repositoryStub) DeviceExists(context.Context, string) (bool, error)   { return r.exists, nil }
func (r *repositoryStub) CreateReservation(_ context.Context, device, owner string) (domain.Reservation, error) {
	r.createdOwner = owner
	return domain.Reservation{ID: "f173de11-13e4-4cbe-9557-ffab003022ba", DeviceID: device, Owner: owner, CreatedAt: time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)}, r.createErr
}
func (r *repositoryStub) ReleaseReservation(context.Context, string) error { return r.releaseErr }
func (r *repositoryStub) History(context.Context, string, int, int) ([]domain.Reservation, error) {
	return nil, r.historyErr
}
func (r *repositoryStub) Ping(context.Context) error { return r.pingErr }

type inspectorStub struct{ err error }

func (i inspectorStub) Inspect(_ context.Context, id string) (domain.Diagnostic, error) {
	return domain.Diagnostic{DeviceID: id, Health: "FAULT", DiagnosticCode: "INTERFACE_ERROR", ObservedAt: time.Now().UTC()}, i.err
}
func newTestHandler(repo *repositoryStub, diagErr error) http.Handler {
	return New(service.New(repo, inspectorStub{diagErr}, time.Second), slog.New(slog.NewJSONHandler(io.Discard, nil)), time.Second)
}
func request(handler http.Handler, method, path, body, contentType string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}
func assertError(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status %d, want %d; body %s", w.Code, status, w.Body.String())
	}
	var body errorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != code {
		t.Fatalf("code %q want %q", body.Error.Code, code)
	}
	if _, err := uuid.Parse(w.Header().Get("X-Request-ID")); err != nil {
		t.Fatalf("request id: %v", err)
	}
}

func TestCreateJSONAndValidation(t *testing.T) {
	for _, tc := range []struct {
		name, body, contentType string
		status                  int
		code                    string
	}{
		{"invalid JSON", "{", "application/json", 400, "invalid_json"},
		{"unknown field", `{"device_id":"sensor-a1","owner":"e","admin":true}`, "application/json", 400, "invalid_json"},
		{"trailing JSON", `{"device_id":"sensor-a1","owner":"e"} {}`, "application/json", 400, "invalid_json"},
		{"wrong type", `{"device_id":1,"owner":"e"}`, "application/json", 400, "invalid_json"},
		{"array", `[]`, "application/json", 400, "invalid_json"},
		{"null", `null`, "application/json", 400, "invalid_input"},
		{"empty owner", `{"device_id":"sensor-a1","owner":" \t "}`, "application/json", 400, "invalid_input"},
		{"missing device", `{"owner":"e"}`, "application/json", 400, "invalid_input"},
		{"missing media type", `{}`, "", 415, "unsupported_media_type"},
		{"text", `{}`, "text/plain", 415, "unsupported_media_type"},
		{"large body", `{"device_id":"sensor-a1","owner":"` + strings.Repeat("x", 5000) + `"}`, "application/json", 413, "body_too_large"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &repositoryStub{}
			w := request(newTestHandler(repo, nil), "POST", "/reservations", tc.body, tc.contentType)
			assertError(t, w, tc.status, tc.code)
			if repo.createdOwner != "" {
				t.Fatal("invalid request reached persistence")
			}
		})
	}
	repo := &repositoryStub{}
	w := request(newTestHandler(repo, nil), "POST", "/reservations", `{"device_id":"sensor-a1","owner":"  инженер  "}`, "application/json; charset=utf-8")
	if w.Code != 201 {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	var reservation domain.Reservation
	if err := json.Unmarshal(w.Body.Bytes(), &reservation); err != nil {
		t.Fatal(err)
	}
	if reservation.Owner != "инженер" || reservation.ReleasedAt != nil || reservation.CreatedAt.Location() != time.UTC {
		t.Fatalf("reservation %+v", reservation)
	}
}

func TestDomainErrorsAndRedaction(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{domain.ErrDeviceOccupied, 409, "device_occupied"}, {domain.ErrDeviceNotFound, 404, "device_not_found"},
		{context.DeadlineExceeded, 504, "dependency_timeout"},
		{errors.New("secret_password SQL INSERT"), 500, "internal_error"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			w := request(newTestHandler(&repositoryStub{createErr: tc.err}, nil), "POST", "/reservations", `{"device_id":"sensor-a1","owner":"e"}`, "application/json")
			assertError(t, w, tc.status, tc.code)
			if strings.Contains(w.Body.String(), "secret_password") || strings.Contains(w.Body.String(), "SQL") {
				t.Fatal("internal error leaked")
			}
		})
	}
}

func TestReleaseAndRoutes(t *testing.T) {
	h := newTestHandler(&repositoryStub{}, nil)
	assertError(t, request(h, "DELETE", "/reservations/bad", "", ""), 400, "invalid_input")
	path := "/reservations/f173de11-13e4-4cbe-9557-ffab003022ba"
	for range 2 {
		w := request(h, "DELETE", path, "", "")
		if w.Code != 204 || w.Body.Len() != 0 {
			t.Fatalf("release %d %q", w.Code, w.Body.String())
		}
	}
	assertError(t, request(newTestHandler(&repositoryStub{releaseErr: domain.ErrReservationNotFound}, nil), "DELETE", path, "", ""), 404, "reservation_not_found")
	w := request(h, "POST", "/devices", "", "")
	assertError(t, w, 405, "method_not_allowed")
	if w.Header().Get("Allow") != "GET" {
		t.Fatal("missing Allow header")
	}
	assertError(t, request(h, "GET", "/unknown", "", ""), 404, "route_not_found")
}

func TestHistoryPagination(t *testing.T) {
	h := newTestHandler(&repositoryStub{exists: true}, nil)
	for _, query := range []string{"limit=no", "offset=no", "limit=", "limit=0", "limit=101", "offset=-1", "offset=2147483648", "limit=1&limit=2", "offset=%zz"} {
		t.Run(query, func(t *testing.T) {
			w := request(h, "GET", "/devices/sensor-a1/reservations?"+query, "", "")
			if w.Code != 400 {
				t.Fatalf("status %d body %s", w.Code, w.Body.String())
			}
		})
	}
	w := request(h, "GET", "/devices/sensor-a1/reservations", "", "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var result domain.History
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Limit != 20 || result.Offset != 0 || result.Items == nil || len(result.Items) != 0 {
		t.Fatalf("history %+v", result)
	}
	assertError(t, request(newTestHandler(&repositoryStub{}, nil), "GET", "/devices/unknown/reservations", "", ""), 404, "device_not_found")
}

func TestDiagnosticsStatusMappingAndIndependentReadiness(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{domain.ErrUnavailable, 503, "diagnostics_unavailable"}, {domain.ErrDeadline, 504, "diagnostics_timeout"},
		{domain.ErrUpstream, 502, "diagnostics_upstream_error"}, {domain.ErrDeviceNotFound, 404, "device_not_found"},
		{context.Canceled, 408, "request_canceled"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			h := newTestHandler(&repositoryStub{exists: true}, tc.err)
			assertError(t, request(h, "GET", "/devices/sensor-a1/diagnostics", "", ""), tc.status, tc.code)
			if w := request(h, "GET", "/readyz", "", ""); w.Code != 200 {
				t.Fatalf("readiness %d", w.Code)
			}
			if w := request(h, "POST", "/reservations", `{"device_id":"sensor-a1","owner":"e"}`, "application/json"); w.Code != 201 {
				t.Fatalf("reservation %d", w.Code)
			}
		})
	}
	h := newTestHandler(&repositoryStub{exists: true}, nil)
	w := request(h, "GET", "/devices/adapter-b1/diagnostics", "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"health":"FAULT"`) {
		t.Fatalf("fault response %d %s", w.Code, w.Body.String())
	}
	h = newTestHandler(&repositoryStub{pingErr: errors.New("DB unavailable")}, nil)
	assertError(t, request(h, "GET", "/readyz", "", ""), 503, "not_ready")
	if w := request(h, "GET", "/healthz", "", ""); w.Code != 200 {
		t.Fatalf("health %d", w.Code)
	}
}
