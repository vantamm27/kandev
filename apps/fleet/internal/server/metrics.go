package server

import (
	"bytes"
	"context"
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/kandev/kandev/apps/fleet/internal/metrics"
	"github.com/kandev/kandev/apps/fleet/internal/store"
)

// SeatLister lists seats for metrics.
type SeatLister interface {
	ListSeats(ctx context.Context) ([]store.SeatSummary, error)
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if s.deps.Metrics == nil {
		http.NotFound(w, r)
		return
	}
	if s.deps.MetricsToken != "" {
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(s.deps.MetricsToken)) != 1 {
			writeError(w, http.StatusUnauthorized, "metrics token required")
			return
		}
	}
	seats, err := s.deps.Metrics.ListSeats(r.Context())
	if err != nil {
		s.deps.Log.Error("metrics: list seats failed", "error", err)
		http.Error(w, "metrics unavailable", http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	if err := metrics.WriteSeats(&buf, seats, s.now()); err != nil {
		http.Error(w, "metrics unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", metrics.ContentType)
	_, _ = w.Write(buf.Bytes())
}
