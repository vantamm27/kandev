package server

import (
	"errors"
	"net/http"

	"github.com/kandev/kandev/apps/fleet/internal/registry"
	"github.com/kandev/kandev/apps/fleet/internal/store"
)

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req registry.Request
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.deps.Registrar.Register(r.Context(), req)
	if err != nil {
		status, message := registerErrorStatus(err)
		if status >= http.StatusInternalServerError {
			s.deps.Log.Error("seat registration failed", "ssh_host", req.SSHHost, "ssh_user", req.SSHUser, "error", err)
		}
		writeError(w, status, message)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

func registerErrorStatus(err error) (int, string) {
	var validation *registry.ValidationError
	var kandevErr *registry.KandevError
	switch {
	case errors.As(err, &validation):
		return http.StatusBadRequest, validation.Message
	case errors.Is(err, store.ErrTokenInvalid):
		return http.StatusUnauthorized, err.Error()
	case errors.Is(err, store.ErrSeatExists):
		return http.StatusConflict, err.Error()
	case errors.As(err, &kandevErr):
		return http.StatusBadGateway, "Kandev rejected the executor: " + kandevErr.Err.Error()
	default:
		return http.StatusInternalServerError, "registration failed"
	}
}
