// Package sweeper marks seats offline when their heartbeats stop.
package sweeper

import (
	"context"
	"log/slog"
	"time"
)

// Marker moves stale seats offline.
type Marker interface {
	MarkStaleSeatsOffline(ctx context.Context, cutoff, now time.Time) (int64, error)
}

// Sweeper runs Sweep on an interval until its context is cancelled.
type Sweeper struct {
	marker  Marker
	timeout time.Duration
	log     *slog.Logger
	now     func() time.Time
}

// New returns a sweeper that treats seats silent for longer than timeout as offline.
func New(marker Marker, timeout time.Duration, log *slog.Logger) *Sweeper {
	return &Sweeper{marker: marker, timeout: timeout, log: log, now: time.Now}
}

// Sweep marks stale seats offline once.
func (s *Sweeper) Sweep(ctx context.Context) {
	now := s.now()
	n, err := s.marker.MarkStaleSeatsOffline(ctx, now.Add(-s.timeout), now)
	if err != nil {
		s.log.Error("offline sweep failed", "error", err)
		return
	}
	if n > 0 {
		s.log.Warn("seats marked offline", "count", n, "heartbeat_timeout", s.timeout.String())
	}
}

// Run sweeps every timeout/3 and returns when ctx is done.
func (s *Sweeper) Run(ctx context.Context) {
	interval := s.timeout / 3
	if interval < time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.Sweep(ctx)
		}
	}
}
