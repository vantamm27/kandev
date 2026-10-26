// Package metrics renders fleet state in the Prometheus text exposition
// format (version 0.0.4) for VictoriaMetrics or Prometheus to scrape.
package metrics

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kandev/kandev/apps/fleet/internal/store"
)

// ContentType is the exposition format's media type.
const ContentType = "text/plain; version=0.0.4; charset=utf-8"

// allStates is every seat state, so a state with no seats still reports 0.
var allStates = []string{
	store.SeatRegistering, store.SeatOnline, store.SeatBusy, store.SeatCooldown5h,
	store.SeatCooldownWeek, store.SeatDraining, store.SeatOffline, store.SeatRetired,
}

// WriteSeats writes seat gauges. Retired seats only count toward
// fleet_seats; they get no per-seat series.
func WriteSeats(w io.Writer, seats []store.SeatSummary, now time.Time) error {
	e := &encoder{w: w}
	counts := make(map[string]int, len(allStates))
	for _, seat := range seats {
		counts[seat.State]++
	}
	e.help("fleet_seats", "Seats by state.")
	for _, state := range allStates {
		e.sample("fleet_seats", labels{{"state", state}}, float64(counts[state]))
	}

	live := make([]store.SeatSummary, 0, len(seats))
	for _, seat := range seats {
		if seat.State != store.SeatRetired {
			live = append(live, seat)
		}
	}
	sort.Slice(live, func(i, j int) bool { return live[i].ID < live[j].ID })

	e.help("fleet_seat_info", "Seat identity and current state (value is always 1).")
	for _, s := range live {
		e.sample("fleet_seat_info", labels{{"seat", s.ID}, {"owner", s.OwnerUserID}, {"host", s.SSHHost},
			{"user", s.SSHUser}, {"state", s.State}, {"limit", s.LimitReason}, {"agent_version", s.AgentVersion}}, 1)
	}
	perSeat := func(name, help string, value func(store.SeatSummary) (float64, bool)) {
		e.help(name, help)
		for _, s := range live {
			if v, ok := value(s); ok {
				e.sample(name, labels{{"seat", s.ID}, {"owner", s.OwnerUserID}}, v)
			}
		}
	}
	perSeat("fleet_seat_capacity", "Concurrent sessions the seat accepts.", func(s store.SeatSummary) (float64, bool) {
		return float64(s.Capacity), true
	})
	perSeat("fleet_seat_free_slots", "Free session slots at the last heartbeat.", func(s store.SeatSummary) (float64, bool) {
		return float64(s.FreeSlots), true
	})
	perSeat("fleet_seat_quota_5h_percent", "Reported 5-hour usage, percent.", func(s store.SeatSummary) (float64, bool) {
		return deref(s.Quota5hPct)
	})
	perSeat("fleet_seat_quota_week_percent", "Reported weekly usage, percent.", func(s store.SeatSummary) (float64, bool) {
		return deref(s.QuotaWeekPct)
	})
	perSeat("fleet_seat_limit_reset_seconds", "Seconds until the current usage limit resets.", func(s store.SeatSummary) (float64, bool) {
		return untilReset(s, now)
	})
	perSeat("fleet_seat_heartbeat_age_seconds", "Seconds since the seat's last heartbeat.", func(s store.SeatSummary) (float64, bool) {
		if s.LastHeartbeatAt == nil {
			return 0, false
		}
		return now.Sub(*s.LastHeartbeatAt).Seconds(), true
	})
	return e.err
}

func deref(p *float64) (float64, bool) {
	if p == nil {
		return 0, false
	}
	return *p, true
}

func untilReset(s store.SeatSummary, now time.Time) (float64, bool) {
	var reset *time.Time
	switch s.State {
	case store.SeatCooldown5h:
		reset = s.Reset5hAt
	case store.SeatCooldownWeek:
		reset = s.ResetWeekAt
	}
	if reset == nil {
		return 0, false
	}
	if d := reset.Sub(now).Seconds(); d > 0 {
		return d, true
	}
	return 0, true
}

type labels [][2]string

type encoder struct {
	w   io.Writer
	err error
}

func (e *encoder) printf(format string, args ...any) {
	if e.err == nil {
		_, e.err = fmt.Fprintf(e.w, format, args...)
	}
}

func (e *encoder) help(name, text string) {
	e.printf("# HELP %s %s\n# TYPE %s gauge\n", name, text, name)
}

func (e *encoder) sample(name string, ls labels, value float64) {
	parts := make([]string, 0, len(ls))
	for _, l := range ls {
		parts = append(parts, l[0]+`="`+escapeLabel(l[1])+`"`)
	}
	e.printf("%s{%s} %s\n", name, strings.Join(parts, ","), strconv.FormatFloat(value, 'g', -1, 64))
}

var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

func escapeLabel(v string) string { return labelEscaper.Replace(v) }

// DispatchStats is the dispatcher's view for metrics.
type DispatchStats struct {
	Queued    map[string]int
	Waiting   map[string]map[string]int
	Exhausted int
	Launched  int64
	Failed    int64
}

// WriteDispatch writes queue and dispatch metrics.
func WriteDispatch(w io.Writer, s DispatchStats) error {
	e := &encoder{w: w}
	e.help("fleet_queue_tasks", "Tasks waiting in the intake step at the last round, by priority.")
	for _, p := range sortedKeys(s.Queued) {
		e.sample("fleet_queue_tasks", labels{{"priority", p}}, float64(s.Queued[p]))
	}
	e.help("fleet_queue_waiting_tasks", "Tasks the last round could not start, by priority and reason.")
	for _, p := range sortedKeys(s.Waiting) {
		for _, r := range sortedKeys(s.Waiting[p]) {
			e.sample("fleet_queue_waiting_tasks", labels{{"priority", p}, {"reason", r}}, float64(s.Waiting[p][r]))
		}
	}
	e.printf("# HELP fleet_queue_exhausted_tasks Tasks that used up their launch attempts.\n# TYPE fleet_queue_exhausted_tasks gauge\nfleet_queue_exhausted_tasks %d\n", s.Exhausted)
	e.printf("# HELP fleet_dispatch_launches_total Sessions started since fleetd started.\n# TYPE fleet_dispatch_launches_total counter\nfleet_dispatch_launches_total %d\n", s.Launched)
	e.printf("# HELP fleet_dispatch_failures_total Failed launch attempts since fleetd started.\n# TYPE fleet_dispatch_failures_total counter\nfleet_dispatch_failures_total %d\n", s.Failed)
	return e.err
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
