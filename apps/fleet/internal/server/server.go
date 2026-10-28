// Package server exposes the fleetd HTTP API.
package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/kandev/kandev/apps/fleet/internal/registry"
	"github.com/kandev/kandev/apps/fleet/internal/store"
)

// Pinger reports whether a dependency is healthy.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Registrar registers seats.
type Registrar interface {
	Register(ctx context.Context, req registry.Request) (registry.Result, error)
}

// Seats reads and updates seats for signed node requests.
type Seats interface {
	GetSeat(ctx context.Context, id string) (store.Seat, error)
	ApplyHeartbeat(ctx context.Context, seatID string, u store.HeartbeatUpdate, now time.Time) error
}

// Deps are the server's collaborators.
type Deps struct {
	DB        Pinger
	Registrar Registrar
	Seats     Seats
	Admin     Admin
	// AdminToken guards /v1/admin; empty disables the admin API.
	AdminToken string
	// Metrics lists seats for /metrics; nil disables the endpoint.
	Metrics SeatLister
	// MetricsToken, when set, is required as a bearer token on /metrics.
	MetricsToken string
	// Dispatch adds queue and launch metrics when the dispatcher runs.
	Dispatch DispatchStatser
	// Handoffs accepts handoffs; nil disables the accept endpoint.
	Handoffs HandoffAcceptor
	// HandoffCounts adds handoff metrics.
	HandoffCounts HandoffCounter
	// TaskOutcomes adds per-task metrics and the admin task list.
	TaskOutcomes TaskOutcomeLister
	// Rounds adds the queue to the admin task list.
	Rounds RoundReader
	// Assigner gives new fleet tasks to their creators; nil disables it.
	Assigner TaskOwnerAssigner
	// WorkspaceID is the Kandev workspace the dispatcher serves.
	WorkspaceID string
	Log         *slog.Logger
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// Server routes fleetd HTTP requests.
type Server struct {
	deps Deps
	mux  *http.ServeMux
}

const (
	healthCheckTimeout = 2 * time.Second
	maxRequestBody     = 64 << 10
)

// New builds the HTTP handler.
func New(deps Deps) *Server {
	s := &Server{deps: deps, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /healthz", s.handleHealthz)
	s.mux.HandleFunc("POST /v1/register", s.handleRegister)
	s.mux.HandleFunc("POST /v1/heartbeat", s.handleHeartbeat)
	s.registerAdminRoutes()
	s.mux.HandleFunc("GET /metrics", s.handleMetrics)
	return s
}

func (s *Server) now() time.Time {
	if s.deps.Now != nil {
		return s.deps.Now()
	}
	return time.Now()
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), healthCheckTimeout)
	defer cancel()
	if err := s.deps.DB.Ping(ctx); err != nil {
		s.deps.Log.Warn("health check failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, out any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}
