package server

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"

	"github.com/kandev/kandev/apps/fleet/internal/admin"
	"github.com/kandev/kandev/apps/fleet/internal/store"
)

// Admin administers seats.
type Admin interface {
	ListSeats(ctx context.Context) ([]store.SeatSummary, error)
	Drain(ctx context.Context, id string) error
	Undrain(ctx context.Context, id string) error
	Retire(ctx context.Context, id string, force bool) error
	RotateSecret(ctx context.Context, id string) (string, error)
}

func (s *Server) registerAdminRoutes() {
	s.mux.HandleFunc("GET /v1/admin/seats", s.adminOnly(s.handleListSeats))
	s.mux.HandleFunc("POST /v1/admin/seats/{id}/drain", s.adminOnly(s.handleSeatAction(func(ctx context.Context, id string, _ *http.Request) error {
		return s.deps.Admin.Drain(ctx, id)
	})))
	s.mux.HandleFunc("POST /v1/admin/seats/{id}/undrain", s.adminOnly(s.handleSeatAction(func(ctx context.Context, id string, _ *http.Request) error {
		return s.deps.Admin.Undrain(ctx, id)
	})))
	s.mux.HandleFunc("POST /v1/admin/seats/{id}/retire", s.adminOnly(s.handleSeatAction(func(ctx context.Context, id string, r *http.Request) error {
		return s.deps.Admin.Retire(ctx, id, r.URL.Query().Get("force") == "true")
	})))
	s.mux.HandleFunc("POST /v1/admin/seats/{id}/rotate-secret", s.adminOnly(s.handleRotateSecret))
}

// adminOnly requires `Authorization: Bearer <FLEET_ADMIN_TOKEN>`. With no
// admin token configured the admin API is disabled.
func (s *Server) adminOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.deps.AdminToken == "" || s.deps.Admin == nil {
			writeError(w, http.StatusNotFound, "admin API is disabled; set FLEET_ADMIN_TOKEN")
			return
		}
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(s.deps.AdminToken)) != 1 {
			writeError(w, http.StatusUnauthorized, "admin token required")
			return
		}
		next(w, r)
	}
}

func (s *Server) handleListSeats(w http.ResponseWriter, r *http.Request) {
	seats, err := s.deps.Admin.ListSeats(r.Context())
	if err != nil {
		s.deps.Log.Error("list seats failed", "error", err)
		writeError(w, http.StatusInternalServerError, "list seats failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"seats": seats})
}

func (s *Server) handleSeatAction(action func(ctx context.Context, id string, r *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if err := action(r.Context(), id, r); err != nil {
			s.writeAdminError(w, id, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"seat_id": id, "status": "ok"})
	}
}

func (s *Server) handleRotateSecret(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	secret, err := s.deps.Admin.RotateSecret(r.Context(), id)
	if err != nil {
		s.writeAdminError(w, id, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"seat_id": id, "hmac_secret": secret})
}

func (s *Server) writeAdminError(w http.ResponseWriter, id string, err error) {
	var kandevErr *admin.KandevError
	switch {
	case errors.Is(err, store.ErrSeatNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, store.ErrSeatStateConflict), errors.Is(err, admin.ErrSeatBusy):
		writeError(w, http.StatusConflict, err.Error())
	case errors.As(err, &kandevErr):
		writeError(w, http.StatusBadGateway, kandevErr.Error())
	default:
		s.deps.Log.Error("admin action failed", "seat_id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "admin action failed")
	}
}
