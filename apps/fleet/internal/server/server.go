// Package server exposes the fleetd HTTP API.
package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

// Pinger reports whether a dependency is healthy.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Server routes fleetd HTTP requests.
type Server struct {
	db  Pinger
	log *slog.Logger
	mux *http.ServeMux
}

const healthCheckTimeout = 2 * time.Second

// New builds the HTTP handler.
func New(db Pinger, log *slog.Logger) *Server {
	s := &Server{db: db, log: log, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /healthz", s.handleHealthz)
	return s
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), healthCheckTimeout)
	defer cancel()
	if err := s.db.Ping(ctx); err != nil {
		s.log.Warn("health check failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
