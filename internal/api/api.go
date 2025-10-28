// Package api exposes the Hermes REST interface over the caching service.
//
// Routes:
//
//	GET    /v1/cache/{key} — read-through fetch
//	PUT    /v1/cache/{key} — write-through store (raw body is the value)
//	DELETE /v1/cache/{key} — write-through delete
//	GET    /healthz        — liveness/readiness probe
//	GET    /metrics        — JSON stats snapshot
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/nikhil-ghind/hermes/internal/metrics"
	"github.com/nikhil-ghind/hermes/internal/service"
)

// maxBodyBytes caps the size of a value that may be stored via PUT.
const maxBodyBytes = 4 << 20 // 4 MiB

// Cacher is the subset of the service used by the HTTP layer.
type Cacher interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Put(ctx context.Context, key string, value []byte) error
	Delete(ctx context.Context, key string) error
	Metrics() *metrics.Metrics
}

// Health is anything that can report readiness of backing dependencies.
type Health interface {
	Ping(ctx context.Context) error
}

// Handler wires the service to an http.Handler.
type Handler struct {
	svc    Cacher
	checks []Health
}

// New builds the chi router for the API. health checks (e.g. the Redis client)
// are consulted by /healthz.
func New(svc Cacher, checks ...Health) http.Handler {
	h := &Handler{svc: svc, checks: checks}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Second))

	r.Route("/v1/cache", func(r chi.Router) {
		r.Get("/{key}", h.handleGet)
		r.Put("/{key}", h.handlePut)
		r.Delete("/{key}", h.handleDelete)
	})

	r.Get("/healthz", h.handleHealth)
	r.Get("/metrics", h.handleMetrics)

	return r
}

func (h *Handler) handleGet(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	if key == "" {
		writeError(w, http.StatusBadRequest, "missing key")
		return
	}

	val, err := h.svc.Get(r.Context(), key)
	if errors.Is(err, service.ErrNotFound) {
		writeError(w, http.StatusNotFound, "key not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, "backing store error")
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(val)
}

func (h *Handler) handlePut(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	if key == "" {
		writeError(w, http.StatusBadRequest, "missing key")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "unable to read body")
		return
	}
	if len(body) > maxBodyBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "value too large")
		return
	}

	if err := h.svc.Put(r.Context(), key, body); err != nil {
		writeError(w, http.StatusBadGateway, "backing store error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) handleDelete(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	if key == "" {
		writeError(w, http.StatusBadRequest, "missing key")
		return
	}
	if err := h.svc.Delete(r.Context(), key); err != nil {
		writeError(w, http.StatusBadGateway, "backing store error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	for _, c := range h.checks {
		if c == nil {
			continue
		}
		if err := c.Ping(ctx); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"status": "unhealthy",
				"error":  err.Error(),
			})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) handleMetrics(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.svc.Metrics().Snapshot())
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
