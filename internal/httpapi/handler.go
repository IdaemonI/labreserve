// Package httpapi provides the public JSON REST transport.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/IdaemonI/labreserve/internal/domain"
	"github.com/IdaemonI/labreserve/internal/service"
	"github.com/google/uuid"
	"google.golang.org/grpc/metadata"
)

const MaxBodyBytes int64 = 4096

type Handler struct {
	service        *service.Service
	logger         *slog.Logger
	requestTimeout time.Duration
}

func New(s *service.Service, logger *slog.Logger, requestTimeout time.Duration) http.Handler {
	h := &Handler{service: s, logger: logger, requestTimeout: requestTimeout}
	mux := http.NewServeMux()
	mux.HandleFunc("/devices", only(http.MethodGet, h.devices))
	mux.HandleFunc("/reservations", only(http.MethodPost, h.create))
	mux.HandleFunc("/reservations/{id}", only(http.MethodDelete, h.release))
	mux.HandleFunc("/devices/{id}/reservations", only(http.MethodGet, h.history))
	mux.HandleFunc("/devices/{id}/diagnostics", only(http.MethodGet, h.diagnostics))
	mux.HandleFunc("/healthz", only(http.MethodGet, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}))
	mux.HandleFunc("/readyz", only(http.MethodGet, h.ready))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "route_not_found", "Route not found")
	})
	return h.middleware(mux)
}

func only(method string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method {
			w.Header().Set("Allow", method)
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
			return
		}
		next(w, r)
	}
}

func (h *Handler) devices(w http.ResponseWriter, r *http.Request) {
	items, err := h.service.ListDevices(r.Context())
	if err != nil {
		h.failure(w, r, err)
		return
	}
	if items == nil {
		items = []domain.Device{}
	}
	writeJSON(w, http.StatusOK, items)
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxBodyBytes)
	var input struct {
		DeviceID string `json:"device_id"`
		Owner    string `json:"owner"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&input); err != nil {
		invalidJSON(w, err)
		return
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		invalidJSON(w, err)
		return
	}
	reservation, err := h.service.CreateReservation(r.Context(), input.DeviceID, input.Owner)
	if err != nil {
		h.failure(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, reservation)
}

func invalidJSON(w http.ResponseWriter, err error) {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		writeError(w, http.StatusRequestEntityTooLarge, "body_too_large", "Request body exceeds 4096 bytes")
		return
	}
	writeError(w, http.StatusBadRequest, "invalid_json", "Expected one JSON object with device_id and owner string fields")
}

func (h *Handler) release(w http.ResponseWriter, r *http.Request) {
	if err := h.service.ReleaseReservation(r.Context(), r.PathValue("id")); err != nil {
		h.failure(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) history(w http.ResponseWriter, r *http.Request) {
	limit, offset := 20, 0
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, 400, "invalid_pagination", "Invalid pagination query")
		return
	}
	for _, item := range []struct {
		name   string
		target *int
	}{{"limit", &limit}, {"offset", &offset}} {
		if v, ok := values[item.name]; ok {
			if len(v) != 1 {
				writeError(w, 400, "invalid_pagination", "Pagination parameters must occur once")
				return
			}
			n, parseErr := strconv.ParseInt(v[0], 10, 32)
			if parseErr != nil {
				writeError(w, 400, "invalid_pagination", "Pagination parameters must be integers")
				return
			}
			*item.target = int(n)
		}
	}
	result, err := h.service.History(r.Context(), r.PathValue("id"), limit, offset)
	if err != nil {
		h.failure(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) diagnostics(w http.ResponseWriter, r *http.Request) {
	result, err := h.service.Diagnostics(r.Context(), r.PathValue("id"))
	if err != nil {
		h.failure(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) ready(w http.ResponseWriter, r *http.Request) {
	if err := h.service.Ready(r.Context()); err != nil {
		h.logger.WarnContext(r.Context(), "database readiness failed", "request_id", requestID(r.Context()), "error", err)
		writeError(w, http.StatusServiceUnavailable, "not_ready", "Database is not ready")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (h *Handler) failure(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message := http.StatusInternalServerError, "internal_error", "Internal server error"
	switch {
	case errors.Is(err, domain.ErrInvalidInput):
		status, code, message = 400, "invalid_input", "Invalid request parameters"
	case errors.Is(err, domain.ErrDeviceNotFound):
		status, code, message = 404, "device_not_found", "Device not found"
	case errors.Is(err, domain.ErrReservationNotFound):
		status, code, message = 404, "reservation_not_found", "Reservation not found"
	case errors.Is(err, domain.ErrDeviceOccupied):
		status, code, message = 409, "device_occupied", "Device already has an active reservation"
	case errors.Is(err, domain.ErrUnavailable):
		status, code, message = 503, "diagnostics_unavailable", "Diagnostics service unavailable"
	case errors.Is(err, domain.ErrDeadline):
		status, code, message = 504, "diagnostics_timeout", "Diagnostics deadline exceeded"
	case errors.Is(err, domain.ErrUpstream):
		status, code, message = 502, "diagnostics_upstream_error", "Unexpected diagnostics service response"
	case errors.Is(err, context.DeadlineExceeded):
		status, code, message = 504, "dependency_timeout", "Dependency deadline exceeded"
	case errors.Is(err, context.Canceled):
		status, code, message = 408, "request_canceled", "Request canceled"
	}
	if status >= 500 || errors.Is(err, context.Canceled) {
		h.logger.ErrorContext(r.Context(), "request failed", "request_id", requestID(r.Context()), "method", r.Method, "path", r.URL.Path, "status", status, "error", err)
	}
	writeError(w, status, code, message)
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type errorResponse struct {
	Error errorDetail `json:"error"`
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorResponse{Error: errorDetail{Code: code, Message: message}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// Encoding the concrete response types above cannot fail; a write error means
	// the connection has closed and cannot be repaired with another HTTP response.
	_ = json.NewEncoder(w).Encode(value)
}

type requestIDKey struct{}

func requestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

type responseWriter struct {
	http.ResponseWriter
	status int
}

func (w *responseWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *responseWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

func (w *responseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (h *Handler) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		id := uuid.NewString()
		w.Header().Set("X-Request-ID", id)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		ctx, cancel := context.WithTimeout(context.WithValue(r.Context(), requestIDKey{}, id), h.requestTimeout)
		defer cancel()
		ctx = metadata.AppendToOutgoingContext(ctx, "x-request-id", id)
		capture := &responseWriter{ResponseWriter: w}
		defer func() {
			if recovered := recover(); recovered != nil {
				h.logger.ErrorContext(ctx, "http handler panic", "request_id", id, "panic", recovered)
				if capture.status == 0 {
					writeError(capture, 500, "internal_error", "Internal server error")
				}
			}
			h.logger.InfoContext(ctx, "http request", "request_id", id, "method", r.Method, "path", r.URL.Path, "status", capture.status, "duration", time.Since(start))
		}()
		next.ServeHTTP(capture, r.WithContext(ctx))
	})
}
