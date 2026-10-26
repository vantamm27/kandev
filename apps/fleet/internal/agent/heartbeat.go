package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kandev/kandev/apps/fleet/internal/hmacauth"
	"github.com/kandev/kandev/apps/fleet/internal/seatstate"
)

// HeartbeatInterval is how often fleet-agent reports.
const HeartbeatInterval = 30 * time.Second

// Reporter sends signed heartbeats for one seat.
type Reporter struct {
	Config  Config
	Version string
	HTTP    *http.Client
	Slots   SlotCounter
	Log     *slog.Logger
	Now     func() time.Time
}

// SendOnce builds and sends one heartbeat and returns the seat state fleetd assigned.
func (r *Reporter) SendOnce(ctx context.Context) (string, error) {
	now := r.Now()
	quota, err := ReadQuota(r.Config.QuotaFile, now)
	if err != nil {
		r.Log.Warn("ignoring unreadable quota file", "path", r.Config.QuotaFile, "error", err)
		quota = QuotaReport{}
	}
	running, err := r.Slots.Running(ctx)
	if err != nil {
		return "", fmt.Errorf("count running sessions: %w", err)
	}
	body, err := json.Marshal(seatstate.Heartbeat{
		Limit: quota.Limit, Quota5hPct: quota.Quota5hPct, QuotaWeekPct: quota.QuotaWeekPct,
		Reset5hAt: quota.Reset5hAt, ResetWeekAt: quota.ResetWeekAt,
		FreeSlots: FreeSlots(r.Config.Capacity, running), AgentVersion: r.Version,
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(r.Config.FleetdURL, "/")+"/v1/heartbeat", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	hmacauth.SetHeaders(req, r.Config.SeatID, []byte(r.Config.HMACSecret), body, now)
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("heartbeat: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("heartbeat: HTTP %d: %s", resp.StatusCode, errorMessage(raw))
	}
	var out struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("heartbeat: decode response: %w", err)
	}
	return out.State, nil
}

// Run sends a heartbeat immediately and then every interval until ctx ends.
// Failures are logged and retried on the next tick.
func (r *Reporter) Run(ctx context.Context, interval time.Duration) {
	last := ""
	tick := func() {
		state, err := r.SendOnce(ctx)
		if err != nil {
			if ctx.Err() == nil {
				r.Log.Warn("heartbeat failed", "error", err)
			}
			return
		}
		if state != last {
			r.Log.Info("seat state", "seat_id", r.Config.SeatID, "state", state)
			last = state
		}
	}
	tick()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tick()
		}
	}
}
