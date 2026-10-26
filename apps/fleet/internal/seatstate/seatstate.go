// Package seatstate decides a seat's state from its latest heartbeat. It is
// pure so every transition is table-tested.
package seatstate

import (
	"time"

	"github.com/kandev/kandev/apps/fleet/internal/store"
)

// Limit values a node reports when its seat hits a usage limit.
const (
	LimitNone = ""
	Limit5h   = "5h"
	LimitWeek = "week"
)

// Heartbeat is what a node reports.
type Heartbeat struct {
	// Limit is the usage limit the seat is currently blocked on, if any. The
	// node observes this from Claude Code; fleetd does not guess it.
	Limit        string     `json:"limit"`
	Quota5hPct   *float64   `json:"quota_5h_pct,omitempty"`
	QuotaWeekPct *float64   `json:"quota_week_pct,omitempty"`
	Reset5hAt    *time.Time `json:"reset_5h_at,omitempty"`
	ResetWeekAt  *time.Time `json:"reset_week_at,omitempty"`
	FreeSlots    int        `json:"free_slots"`
	AgentVersion string     `json:"agent_version"`
}

// Valid reports whether the heartbeat's fields are in range.
func (h Heartbeat) Valid() bool {
	switch h.Limit {
	case LimitNone, Limit5h, LimitWeek:
	default:
		return false
	}
	return h.FreeSlots >= 0 && pctInRange(h.Quota5hPct) && pctInRange(h.QuotaWeekPct)
}

func pctInRange(p *float64) bool { return p == nil || (*p >= 0 && *p <= 100) }

// Next returns the seat state after a heartbeat. Draining and retired are
// admin decisions a heartbeat never overrides; otherwise a reported limit
// wins over capacity, the weekly limit over the 5-hour one, and a seat with
// no free slot is busy.
func Next(current string, hb Heartbeat) string {
	switch current {
	case store.SeatDraining, store.SeatRetired:
		return current
	}
	switch {
	case hb.Limit == LimitWeek:
		return store.SeatCooldownWeek
	case hb.Limit == Limit5h:
		return store.SeatCooldown5h
	case hb.FreeSlots == 0:
		return store.SeatBusy
	default:
		return store.SeatOnline
	}
}
