package dispatch

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kandev/kandev/apps/fleet/internal/kandev"
	"github.com/kandev/kandev/apps/fleet/internal/store"
)

type recordingTracker struct{ order *[]string }

func (r recordingTracker) Track(_ context.Context, _ map[string]store.Claim, listed map[string]kandev.Task, _ time.Time) error {
	*r.order = append(*r.order, "tracker")
	if _, ok := listed["t-a"]; !ok {
		*r.order = append(*r.order, "tracker missed the listed task")
	}
	return nil
}

type recordingScanner struct {
	name  string
	order *[]string
	err   error
}

func (r recordingScanner) Scan(context.Context, time.Time) (int, error) {
	*r.order = append(*r.order, r.name)
	return 0, r.err
}

func TestRoundTracksTasksFirstAndRunsEveryScan(t *testing.T) {
	_, tasks, _, d, _ := newFixture(t, seatFor("a1", "user-a", 1))
	tasks.tasks = []kandev.Task{intake("t-a", "user-a", "")}
	var order []string
	d.Tracker = recordingTracker{order: &order}
	d.Nudges = recordingScanner{name: "nudges", order: &order, err: errors.New("kandev down")}
	d.Limits = recordingScanner{name: "limits", order: &order}
	if err := d.Round(context.Background()); err != nil {
		t.Fatalf("Round: %v", err)
	}
	if strings.Join(order, ",") != "tracker,nudges,limits" {
		t.Fatalf("post-round order = %v; the tracker runs first with the round's listing, then every scan even after a failure", order)
	}
}
