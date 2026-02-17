package main

import (
	"encoding/json"
	"net/http"
	"sync/atomic"
)

// healthHandler serves the /health endpoint with liveness/readiness status.
type healthHandler struct {
	ready atomic.Bool
}

// newHealthHandler creates a healthHandler that starts in the ready state.
func newHealthHandler() *healthHandler {
	h := &healthHandler{}
	h.ready.Store(true)
	return h
}

// setUnhealthy marks the handler as not ready (called during graceful shutdown).
func (h *healthHandler) setUnhealthy() {
	h.ready.Store(false)
}

// ServeHTTP returns 200 when ready or 503 when draining.
func (h *healthHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	status := "healthy"
	code := http.StatusOK
	if !h.ready.Load() {
		status = "draining"
		code = http.StatusServiceUnavailable
	}

	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": status})
}
