package metrics

import (
	"bufio"
	"bytes"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/kandev/kandev/apps/fleet/internal/store"
)

func ptr[T any](v T) *T { return &v }

var sampleLine = regexp.MustCompile(`^[a-z_][a-z0-9_]*\{([a-z_][a-z0-9_]*="(\\.|[^"\\])*",?)*\} -?[0-9.e+-]+$`)

func TestWriteSeats(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	seats := []store.SeatSummary{
		{ID: "seat_b", OwnerUserID: "user-b", SSHHost: "mini-1", SSHUser: "seat-b", State: store.SeatCooldown5h,
			LimitReason: "5h", Capacity: 1, Quota5hPct: ptr(100.0), Reset5hAt: ptr(now.Add(90 * time.Minute)),
			LastHeartbeatAt: ptr(now.Add(-20 * time.Second)), AgentVersion: `v1 "quoted"`},
		{ID: "seat_a", OwnerUserID: "user-a", SSHHost: "mini-1", SSHUser: "seat-a", State: store.SeatOnline,
			Capacity: 2, FreeSlots: 1, QuotaWeekPct: ptr(63.5)},
		{ID: "seat_old", OwnerUserID: "user-c", State: store.SeatRetired, Capacity: 1},
	}
	var buf bytes.Buffer
	if err := WriteSeats(&buf, seats, now); err != nil {
		t.Fatalf("WriteSeats: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		`fleet_seats{state="online"} 1`,
		`fleet_seats{state="cooldown_5h"} 1`,
		`fleet_seats{state="retired"} 1`,
		`fleet_seats{state="draining"} 0`,
		`fleet_seat_free_slots{seat="seat_a",owner="user-a"} 1`,
		`fleet_seat_capacity{seat="seat_a",owner="user-a"} 2`,
		`fleet_seat_quota_5h_percent{seat="seat_b",owner="user-b"} 100`,
		`fleet_seat_quota_week_percent{seat="seat_a",owner="user-a"} 63.5`,
		`fleet_seat_limit_reset_seconds{seat="seat_b",owner="user-b"} 5400`,
		`fleet_seat_heartbeat_age_seconds{seat="seat_b",owner="user-b"} 20`,
		`agent_version="v1 \"quoted\""`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, `seat="seat_old"`) {
		t.Fatal("retired seats must not get per-seat series")
	}
	if strings.Index(out, `seat="seat_a"`) > strings.Index(out, `seat="seat_b"`) {
		t.Fatal("per-seat series must be sorted by seat ID")
	}
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "# ") {
			continue
		}
		if !sampleLine.MatchString(line) {
			t.Fatalf("malformed sample line %q", line)
		}
	}
}

func TestWriteDispatch(t *testing.T) {
	var buf bytes.Buffer
	err := WriteDispatch(&buf, DispatchStats{
		Queued:    map[string]int{"high": 2, "low": 1},
		Waiting:   map[string]map[string]int{"low": {"weekly_pacing": 1}, "high": {"quota": 1}},
		Exhausted: 1, Launched: 7, Failed: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		`fleet_queue_tasks{priority="high"} 2`,
		`fleet_queue_waiting_tasks{priority="low",reason="weekly_pacing"} 1`,
		`fleet_queue_waiting_tasks{priority="high",reason="quota"} 1`,
		"fleet_queue_exhausted_tasks 1",
		"# TYPE fleet_dispatch_launches_total counter\nfleet_dispatch_launches_total 7",
		"fleet_dispatch_failures_total 2",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}
