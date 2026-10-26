package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"github.com/kandev/kandev/apps/fleet/internal/seatstate"
)

// QuotaReport is the file a Claude Code hook or statusline script writes so
// fleet-agent can report the seat's usage. Every field is optional; a missing
// file means "unknown", which fleetd treats as no limit.
type QuotaReport struct {
	Limit        string     `json:"limit"`
	Quota5hPct   *float64   `json:"quota_5h_pct,omitempty"`
	QuotaWeekPct *float64   `json:"quota_week_pct,omitempty"`
	Reset5hAt    *time.Time `json:"reset_5h_at,omitempty"`
	ResetWeekAt  *time.Time `json:"reset_week_at,omitempty"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// QuotaMaxAge is how long quota percentages stay trusted without an update.
const QuotaMaxAge = 15 * time.Minute

// ReadQuota loads the report at path as seen at now. Percentages older than
// QuotaMaxAge are dropped. A reported limit is kept while its reset time is
// in the future, and kept for QuotaMaxAge when no reset time is known (the
// weekly notice has no date); after that the seat is assumed usable again.
func ReadQuota(path string, now time.Time) (QuotaReport, error) {
	if path == "" {
		return QuotaReport{}, nil
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return QuotaReport{}, nil
	}
	if err != nil {
		return QuotaReport{}, fmt.Errorf("read quota file: %w", err)
	}
	var r QuotaReport
	if err := json.Unmarshal(raw, &r); err != nil {
		return QuotaReport{}, fmt.Errorf("decode quota file: %w", err)
	}
	fresh := now.Sub(r.UpdatedAt) <= QuotaMaxAge
	if !fresh {
		r.Quota5hPct, r.QuotaWeekPct = nil, nil
	}
	r.Limit = effectiveLimit(r, now, fresh)
	return r, nil
}

func effectiveLimit(r QuotaReport, now time.Time, fresh bool) string {
	var reset *time.Time
	switch r.Limit {
	case seatstate.Limit5h:
		reset = r.Reset5hAt
	case seatstate.LimitWeek:
		reset = r.ResetWeekAt
	default:
		return seatstate.LimitNone
	}
	if reset != nil {
		if reset.After(now) {
			return r.Limit
		}
		return seatstate.LimitNone
	}
	if fresh {
		return r.Limit
	}
	return seatstate.LimitNone
}
