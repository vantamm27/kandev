package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/kandev/kandev/apps/fleet/internal/handoff"
	"github.com/kandev/kandev/apps/fleet/internal/store"
)

// HandoffAcceptor moves a handoff's task to an accepting member's seat.
type HandoffAcceptor interface {
	Accept(ctx context.Context, handoffID, toSeatID string) (store.Handoff, error)
}

type acceptRequest struct {
	SeatID string `json:"seat_id"`
}

// handleAcceptHandoff records a member taking a handoff. Until the Kandev UI
// plugin (U1) calls it on the member's behalf, admins call it after the
// member agrees.
func (s *Server) handleAcceptHandoff(w http.ResponseWriter, r *http.Request) {
	if s.deps.Handoffs == nil {
		writeError(w, http.StatusNotFound, "handoffs are disabled")
		return
	}
	var req acceptRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.SeatID == "" {
		writeError(w, http.StatusBadRequest, "seat_id is required")
		return
	}
	h, err := s.deps.Handoffs.Accept(r.Context(), r.PathValue("id"), req.SeatID)
	if err != nil {
		status, message := acceptErrorStatus(err)
		if status >= http.StatusInternalServerError {
			s.deps.Log.Error("accept handoff failed", "handoff_id", r.PathValue("id"), "error", err)
		}
		writeError(w, status, message)
		return
	}
	writeJSON(w, http.StatusOK, h)
}

func acceptErrorStatus(err error) (int, string) {
	var kandevErr *handoff.KandevError
	switch {
	case errors.Is(err, store.ErrHandoffNotFound), errors.Is(err, store.ErrSeatNotFound):
		return http.StatusNotFound, err.Error()
	case errors.Is(err, store.ErrSeatStateConflict):
		return http.StatusConflict, "the handoff is no longer offered"
	case errors.Is(err, handoff.ErrSeatUnavailable):
		return http.StatusConflict, err.Error()
	case errors.Is(err, handoff.ErrSameOwner):
		return http.StatusBadRequest, err.Error()
	case errors.As(err, &kandevErr):
		return http.StatusBadGateway, kandevErr.Error()
	default:
		return http.StatusInternalServerError, "accept handoff failed"
	}
}
