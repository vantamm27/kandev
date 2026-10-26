package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/kandev/kandev/apps/fleet/internal/hmacauth"
	"github.com/kandev/kandev/apps/fleet/internal/seatstate"
	"github.com/kandev/kandev/apps/fleet/internal/store"
)

type heartbeatResponse struct {
	SeatID     string    `json:"seat_id"`
	State      string    `json:"state"`
	ServerTime time.Time `json:"server_time"`
}

func (s *Server) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBody))
	if err != nil {
		writeError(w, http.StatusBadRequest, "request body too large or unreadable")
		return
	}
	now := s.now()
	signed, err := hmacauth.Parse(r.Header)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	seat, err := s.deps.Seats.GetSeat(r.Context(), signed.SeatID)
	if errors.Is(err, store.ErrSeatNotFound) {
		writeError(w, http.StatusUnauthorized, "unknown seat")
		return
	}
	if err != nil {
		s.deps.Log.Error("heartbeat seat lookup failed", "seat_id", signed.SeatID, "error", err)
		writeError(w, http.StatusInternalServerError, "heartbeat failed")
		return
	}
	if err := signed.Verify([]byte(seat.HMACSecret), r.Method, r.URL.Path, body, now); err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	var hb seatstate.Heartbeat
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&hb); err != nil || !hb.Valid() {
		writeError(w, http.StatusBadRequest, "invalid heartbeat body")
		return
	}
	if hb.FreeSlots > seat.Capacity {
		hb.FreeSlots = seat.Capacity
	}
	next := seatstate.Next(seat.State, hb)
	err = s.deps.Seats.ApplyHeartbeat(r.Context(), seat.ID, store.HeartbeatUpdate{
		State: next, LimitReason: hb.Limit, Quota5hPct: hb.Quota5hPct, QuotaWeekPct: hb.QuotaWeekPct,
		Reset5hAt: hb.Reset5hAt, ResetWeekAt: hb.ResetWeekAt, FreeSlots: hb.FreeSlots,
		AgentVersion: hb.AgentVersion, SignedAt: signed.Timestamp,
	}, now)
	if errors.Is(err, store.ErrStaleSignature) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		s.deps.Log.Error("heartbeat update failed", "seat_id", seat.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "heartbeat failed")
		return
	}
	if next != seat.State {
		s.deps.Log.Info("seat state changed", "seat_id", seat.ID, "from", seat.State, "to", next, "limit", hb.Limit)
	}
	writeJSON(w, http.StatusOK, heartbeatResponse{SeatID: seat.ID, State: next, ServerTime: now.UTC()})
}
