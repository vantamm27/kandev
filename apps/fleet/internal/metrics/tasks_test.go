package metrics

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/kandev/kandev/apps/fleet/internal/store"
)

func TestWriteTasks(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	tasks := []store.TaskOutcome{
		{Claim: store.Claim{TaskID: "t-run", SeatID: "seat_a", Attempt: 1, State: store.ClaimLaunched,
			TaskState: "IN_PROGRESS", ClaimedAt: now.Add(-90 * time.Second)}, OwnerUserID: "u"},
		{Claim: store.Claim{TaskID: "t-review", SeatID: "seat_a", Attempt: 1, State: store.ClaimLaunched,
			TaskState: "REVIEW", ClaimedAt: now.Add(-time.Hour), FinishedAt: ptr(now.Add(-58 * time.Minute))}, OwnerUserID: "u"},
		{Claim: store.Claim{TaskID: "t-old", SeatID: "seat_a", Attempt: 1, State: store.ClaimLaunched,
			TaskState: "COMPLETED", ClaimedAt: now.Add(-72 * time.Hour), FinishedAt: ptr(now.Add(-71 * time.Hour))}, OwnerUserID: "u"},
		{Claim: store.Claim{TaskID: "t-fail", SeatID: "seat_b", Attempt: 3, State: store.ClaimFailed,
			ClaimedAt: now.Add(-time.Minute)}, OwnerUserID: "u"},
		{Claim: store.Claim{TaskID: "t-start", SeatID: "seat_b", Attempt: 1, State: store.ClaimLaunching,
			ClaimedAt: now.Add(-5 * time.Second)}, OwnerUserID: "u"},
	}
	var buf bytes.Buffer
	if err := WriteTasks(&buf, tasks, now); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		`fleet_tasks{seat="seat_a",owner="u",outcome="running"} 1`,
		`fleet_tasks{seat="seat_a",owner="u",outcome="review"} 1`,
		`fleet_tasks{seat="seat_a",owner="u",outcome="completed"} 1`,
		`fleet_tasks{seat="seat_b",owner="u",outcome="launch_failed"} 1`,
		`fleet_tasks{seat="seat_b",owner="u",outcome="launching"} 1`,
		`fleet_task_info{task="t-run",seat="seat_a",owner="u",state="IN_PROGRESS",outcome="running",attempt="1"} 1`,
		`fleet_task_info{task="t-fail",seat="seat_b",owner="u",state="",outcome="launch_failed",attempt="3"} 1`,
		`fleet_task_duration_seconds{task="t-run",seat="seat_a",owner="u"} 90`,
		`fleet_task_duration_seconds{task="t-review",seat="seat_a",owner="u"} 120`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, `task="t-old"`) {
		t.Error("a task finished before the window must not keep per-task series")
	}
	if strings.Contains(out, `fleet_task_duration_seconds{task="t-fail"`) {
		t.Error("a failed launch has no run time")
	}
	assertSampleLines(t, out)
}
