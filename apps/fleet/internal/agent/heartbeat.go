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
	"sync"
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
	// Probe, when set, rewrites Config.QuotaFile every ProbeEvery on its own
	// schedule, so a slow probe never delays a heartbeat.
	Probe      *QuotaProbe
	ProbeEvery time.Duration
}

// refreshQuota runs the quota probe once and records the result in the quota
// file the heartbeat reads. A failed probe leaves the old file, which
// ReadQuota stops trusting after QuotaMaxAge.
func (r *Reporter) refreshQuota(ctx context.Context) {
	report, err := r.Probe.Probe(ctx, r.Now())
	if err == nil {
		err = WriteQuota(r.Config.QuotaFile, report)
	}
	if err != nil && ctx.Err() == nil {
		r.Log.Warn("quota probe failed", "error", err)
	}
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
	if r.Probe != nil {
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			every(ctx, r.ProbeEvery, func() { r.refreshQuota(ctx) })
		}()
		defer wg.Wait()
	}
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
	every(ctx, interval, tick)
}

// every calls fn now and then every interval until ctx ends.
func every(ctx context.Context, interval time.Duration, fn func()) {
	fn()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			fn()
		}
	}
}
