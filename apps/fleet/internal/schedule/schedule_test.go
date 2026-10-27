package schedule

import (
	"reflect"
	"testing"
	"time"
)

var now = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

func pct(v float64) *float64    { return &v }
func at(t time.Time) *time.Time { return &t }

func seat(id, owner string, free int) Seat {
	return Seat{ID: id, OwnerUserID: owner, State: "online", FreeSlots: free}
}

func task(id, owner, priority string, queuedAgo time.Duration) Task {
	return Task{ID: id, OwnerUserID: owner, Priority: priority, QueuedAt: now.Add(-queuedAgo)}
}

func assigned(p Plan) map[string]string {
	out := map[string]string{}
	for _, a := range p.Assignments {
		out[a.TaskID] = a.SeatID
	}
	return out
}

func waiting(p Plan) map[string]string {
	out := map[string]string{}
	for _, w := range p.Waiting {
		out[w.TaskID] = w.Reason
	}
	return out
}

func TestScoreAgingIsCappedBelowTheNextClass(t *testing.T) {
	p := DefaultPolicy()
	cases := []struct {
		priority string
		queued   time.Duration
		want     float64
	}{
		{PriorityCritical, 0, 10000},
		{PriorityCritical, 48 * time.Hour, 10000},
		{PriorityHigh, 30 * time.Minute, 1060},
		{PriorityHigh, 1000 * time.Hour, 3000},
		{PriorityMedium, 100 * time.Minute, 400},
		{PriorityMedium, 1000 * time.Hour, 1000},
		{PriorityLow, 1000 * time.Hour, 300},
		{PriorityLow, -time.Hour, 100}, // clock skew never lowers a score
	}
	for _, tc := range cases {
		if got := Score(p.Classes[tc.priority], now.Add(-tc.queued), now); got != tc.want {
			t.Fatalf("Score(%s, %s) = %v, want %v", tc.priority, tc.queued, got, tc.want)
		}
	}
	if Score(p.Classes[PriorityHigh], now.Add(-1000*time.Hour), now) >= p.Classes[PriorityCritical].Base {
		t.Fatal("high must never age past critical")
	}
}

func TestOrderingAndCapacity(t *testing.T) {
	tasks := []Task{
		task("low-old", "a", PriorityLow, 10*time.Hour), // 300
		task("med-new", "a", PriorityMedium, 0),         // 300, newer than low-old
		task("crit", "a", PriorityCritical, 0),          // 10000
		task("high", "a", PriorityHigh, 0),              // 1000
	}
	plan := Schedule(tasks, []Seat{seat("s1", "a", 2)}, now, DefaultPolicy())
	if got := assigned(plan); !reflect.DeepEqual(got, map[string]string{"crit": "s1", "high": "s1"}) {
		t.Fatalf("assigned = %v", got)
	}
	if got := waiting(plan); got["low-old"] != ReasonFull || got["med-new"] != ReasonFull {
		t.Fatalf("waiting = %v", got)
	}
	plan = Schedule(tasks, []Seat{seat("s1", "a", 3)}, now, DefaultPolicy())
	if _, ok := assigned(plan)["low-old"]; !ok {
		t.Fatal("on a score tie the older task goes first")
	}
}

func TestOwnerMatchAndLabels(t *testing.T) {
	seats := []Seat{seat("a1", "a", 1), {ID: "b1", OwnerUserID: "b", State: "online", FreeSlots: 1, Labels: []string{"macos", "xcode"}}}
	tasks := []Task{
		task("a-task", "a", PriorityHigh, 0),
		task("c-task", "c", PriorityCritical, 0),
		{ID: "b-ios", OwnerUserID: "b", Priority: PriorityHigh, QueuedAt: now, RequiredLabels: []string{"xcode"}},
		{ID: "a-ios", OwnerUserID: "a", Priority: PriorityHigh, QueuedAt: now, RequiredLabels: []string{"xcode"}},
	}
	plan := Schedule(tasks, seats, now, DefaultPolicy())
	if got := assigned(plan); !reflect.DeepEqual(got, map[string]string{"a-task": "a1", "b-ios": "b1"}) {
		t.Fatalf("assigned = %v", got)
	}
	if got := waiting(plan); got["c-task"] != ReasonNoSeat || got["a-ios"] != ReasonLabels {
		t.Fatalf("waiting = %v (a task never borrows another owner's seat)", got)
	}
}

func TestQuotaGateAndHeadOfLineBackfill(t *testing.T) {
	tight := Seat{ID: "a1", OwnerUserID: "a", State: "online", FreeSlots: 1, Quota5hPct: pct(95)}
	roomy := Seat{ID: "b1", OwnerUserID: "b", State: "online", FreeSlots: 1, QuotaWeekPct: pct(10)}
	tasks := []Task{task("a-crit", "a", PriorityCritical, 0), task("b-low", "b", PriorityLow, 0)}
	plan := Schedule(tasks, []Seat{tight, roomy}, now, DefaultPolicy())
	if waiting(plan)["a-crit"] != ReasonQuota {
		t.Fatalf("waiting = %v; 5%% left is below the 10%% estimate", waiting(plan))
	}
	if assigned(plan)["b-low"] != "b1" {
		t.Fatal("a blocked task must not hold back tasks behind it")
	}
	weekly := Seat{ID: "a1", OwnerUserID: "a", State: "online", FreeSlots: 1, QuotaWeekPct: pct(99)}
	if waiting(Schedule([]Task{task("t", "a", PriorityHigh, 0)}, []Seat{weekly}, now, DefaultPolicy()))["t"] != ReasonQuota {
		t.Fatal("1% weekly quota left is below the 2% estimate")
	}
}

func TestStatesThatDoNotTakeWork(t *testing.T) {
	for _, state := range []string{"busy", "cooldown_5h", "cooldown_week", "draining", "offline", "retired"} {
		s := Seat{ID: "a1", OwnerUserID: "a", State: state, FreeSlots: 1}
		plan := Schedule([]Task{task("t", "a", PriorityCritical, 0)}, []Seat{s}, now, DefaultPolicy())
		if len(plan.Assignments) != 0 || waiting(plan)["t"] != ReasonFull {
			t.Fatalf("state %s took work: %+v", state, plan)
		}
	}
}

func TestWeeklyPacing(t *testing.T) {
	// Two days into the week: even spend is 28.6%.
	reset := now.Add(5 * 24 * time.Hour)
	mk := func(used float64) Seat {
		return Seat{ID: "a1", OwnerUserID: "a", State: "online", FreeSlots: 4, QuotaWeekPct: pct(used), ResetWeekAt: at(reset)}
	}
	tasks := []Task{
		task("crit", "a", PriorityCritical, 0), task("high", "a", PriorityHigh, 0),
		task("med", "a", PriorityMedium, 0), task("low", "a", PriorityLow, 0),
	}
	cases := map[float64]map[string]string{
		20: {"crit": "", "high": "", "med": "", "low": ""},           // under pace: all run
		33: {"crit": "", "high": "", "med": "", "low": ReasonPacing}, // within medium's 10% slack
		45: {"crit": "", "high": "", "med": ReasonPacing, "low": ReasonPacing},
	}
	for used, want := range cases {
		plan := Schedule(tasks, []Seat{mk(used)}, now, DefaultPolicy())
		w := waiting(plan)
		for id, reason := range want {
			if w[id] != reason {
				t.Fatalf("used %.0f%%: %s waiting reason = %q, want %q (plan %+v)", used, id, w[id], reason, plan)
			}
		}
	}
	noWindow := Seat{ID: "a1", OwnerUserID: "a", State: "online", FreeSlots: 1, QuotaWeekPct: pct(80)}
	if len(Schedule([]Task{task("low", "a", PriorityLow, 0)}, []Seat{noWindow}, now, DefaultPolicy()).Assignments) != 1 {
		t.Fatal("pacing needs a known week window; without one it never blocks")
	}
}

func TestSeatChoicePrefersFreeSlotsThenLowerUsage(t *testing.T) {
	seats := []Seat{
		{ID: "s-busyish", OwnerUserID: "a", State: "online", FreeSlots: 1, QuotaWeekPct: pct(5)},
		{ID: "s-roomy", OwnerUserID: "a", State: "online", FreeSlots: 2, QuotaWeekPct: pct(50)},
	}
	if got := assigned(Schedule([]Task{task("t", "a", PriorityHigh, 0)}, seats, now, DefaultPolicy()))["t"]; got != "s-roomy" {
		t.Fatalf("picked %s, want the seat with more free slots", got)
	}
	seats[1].FreeSlots = 1
	if got := assigned(Schedule([]Task{task("t", "a", PriorityHigh, 0)}, seats, now, DefaultPolicy()))["t"]; got != "s-busyish" {
		t.Fatalf("picked %s, want the seat with lower weekly usage", got)
	}
}

func TestUnknownPriorityWaits(t *testing.T) {
	plan := Schedule([]Task{task("t", "a", "urgent", 0)}, []Seat{seat("s", "a", 1)}, now, DefaultPolicy())
	if waiting(plan)["t"] != ReasonPriority || len(plan.Assignments) != 0 {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestUnassignedTaskWaits(t *testing.T) {
	plan := Schedule([]Task{task("t", "", PriorityHigh, 0)}, []Seat{seat("s", "a", 1)}, now, DefaultPolicy())
	if waiting(plan)["t"] != ReasonNoAssignee || len(plan.Assignments) != 0 {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestPinnedTaskRunsOnlyOnItsSeat(t *testing.T) {
	pinned := task("t", "a", PriorityHigh, 0)
	pinned.SeatID = "s-env"
	seats := []Seat{seat("s-env", "a", 1), seat("s-roomy", "a", 4)}
	if got := assigned(Schedule([]Task{pinned}, seats, now, DefaultPolicy()))["t"]; got != "s-env" {
		t.Fatalf("pinned task assigned to %q, want s-env", got)
	}
	seats[0].FreeSlots = 0
	plan := Schedule([]Task{pinned}, seats, now, DefaultPolicy())
	if len(plan.Assignments) != 0 || waiting(plan)["t"] != ReasonFull {
		t.Fatalf("plan = %+v; a pinned task waits for its own seat", plan)
	}
}
