package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nikhil-ghind/hermes/internal/metrics"
	"github.com/nikhil-ghind/hermes/internal/service"
)

// fakeCacher implements Cacher for handler tests.
type fakeCacher struct {
	data    map[string][]byte
	metrics *metrics.Metrics
	failGet bool
}

func newFakeCacher() *fakeCacher {
	return &fakeCacher{data: make(map[string][]byte), metrics: metrics.New()}
}

func (f *fakeCacher) Get(_ context.Context, key string) ([]byte, error) {
	if f.failGet {
		return nil, io.ErrUnexpectedEOF
	}
	v, ok := f.data[key]
	if !ok {
		return nil, service.ErrNotFound
	}
	return v, nil
}

func (f *fakeCacher) Put(_ context.Context, key string, value []byte) error {
	f.data[key] = value
	return nil
}

func (f *fakeCacher) Delete(_ context.Context, key string) error {
	delete(f.data, key)
	return nil
}

func (f *fakeCacher) Metrics() *metrics.Metrics { return f.metrics }

func TestAPI_PutGetDelete(t *testing.T) {
	svc := newFakeCacher()
	h := New(svc)

	// PUT
	req := httptest.NewRequest(http.MethodPut, "/v1/cache/foo", strings.NewReader("bar"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNoContent, rec.Code)

	// GET
	req = httptest.NewRequest(http.MethodGet, "/v1/cache/foo", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "bar", rec.Body.String())

	// DELETE
	req = httptest.NewRequest(http.MethodDelete, "/v1/cache/foo", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNoContent, rec.Code)

	// GET after delete -> 404
	req = httptest.NewRequest(http.MethodGet, "/v1/cache/foo", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestAPI_GetBackingError(t *testing.T) {
	svc := newFakeCacher()
	svc.failGet = true
	h := New(svc)

	req := httptest.NewRequest(http.MethodGet, "/v1/cache/x", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadGateway, rec.Code)
}

func TestAPI_Health(t *testing.T) {
	svc := newFakeCacher()
	h := New(svc, okHealth{})

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "ok")
}

func TestAPI_HealthUnhealthy(t *testing.T) {
	svc := newFakeCacher()
	h := New(svc, badHealth{})

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestAPI_Metrics(t *testing.T) {
	svc := newFakeCacher()
	svc.metrics.L1Hits.Add(3)
	h := New(svc)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "\"l1_hits\":3")
}

type okHealth struct{}

func (okHealth) Ping(context.Context) error { return nil }

type badHealth struct{}

func (badHealth) Ping(context.Context) error { return io.ErrUnexpectedEOF }
