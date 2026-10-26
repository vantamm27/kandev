// Package dispatch starts queued Kandev tasks on their owners' seats. Each
// round reads the intake step's tasks from Kandev, plans with the schedule
// package, claims each planned task in the store, and sends session.launch
// on the seat's fixed executor profile.
package dispatch

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/kandev/kandev/apps/fleet/internal/kandev"
	"github.com/kandev/kandev/apps/fleet/internal/metrics"
	"github.com/kandev/kandev/apps/fleet/internal/schedule"
	"github.com/kandev/kandev/apps/fleet/internal/store"
)

// Tasks reads Kandev tasks.
type Tasks interface {
	ListWorkspaceTasks(ctx context.Context, workspaceID string, opts kandev.ListTasksOptions) (kandev.TaskPage, error)
}

// Launcher starts Kandev sessions.
type Launcher interface {
	LaunchSession(ctx context.Context, in kandev.LaunchSessionInput) (kandev.LaunchSessionResult, error)
}

// Config selects the tasks fleetd dispatches.
type Config struct {
	WorkspaceID string
	// IntakeStepID is the workflow step where tasks wait for a seat. Tasks
	// there must be created with start_agent=false so Kandev does not start
	// them on its default executor.
	IntakeStepID string
	MaxAttempts  int
	// RetryBackoff is the wait after the first failed attempt; it doubles
	// for each further attempt.
	RetryBackoff time.Duration
	Policy       schedule.Policy
}

// Stats summarizes the last round for /metrics.
type Stats struct {
	At        time.Time
	Queued    map[string]int            // by priority
	Waiting   map[string]map[string]int // by priority, then reason
	Launched  int
	Failed    int
	Exhausted int // tasks that used up their attempts
}

// LimitScanner offers handoffs for tasks blocked by usage limits.
type LimitScanner interface {
	Scan(ctx context.Context, now time.Time) (int, error)
}

// Dispatcher runs dispatch rounds.
type Dispatcher struct {
	cfg      Config
	store    *store.Store
	tasks    Tasks
	launcher Launcher
	log      *slog.Logger
	now      func() time.Time
	// Limits, when set, runs after each round's launches.
	Limits LimitScanner

	mu       sync.Mutex
	last     Stats
	launched int64
	failed   int64
}

const (
	pageSize           = 100
	maxPages           = 50
	staleLaunchingAge  = 5 * time.Minute
	defaultMaxAttempts = 3
)

// New returns a dispatcher.
func New(cfg Config, st *store.Store, tasks Tasks, launcher Launcher, log *slog.Logger) *Dispatcher {
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = defaultMaxAttempts
	}
	if cfg.RetryBackoff <= 0 {
		cfg.RetryBackoff = time.Minute
	}
	if cfg.Policy.Classes == nil {
		cfg.Policy = schedule.DefaultPolicy()
	}
	return &Dispatcher{cfg: cfg, store: st, tasks: tasks, launcher: launcher, log: log, now: time.Now}
}

// Totals returns launches and failures since start.
func (d *Dispatcher) Totals() (launched, failed int64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.launched, d.failed
}

// MetricsStats reports the last round and totals for /metrics.
func (d *Dispatcher) MetricsStats() metrics.DispatchStats {
	d.mu.Lock()
	defer d.mu.Unlock()
	return metrics.DispatchStats{
		Queued: d.last.Queued, Waiting: d.last.Waiting, Exhausted: d.last.Exhausted,
		Launched: d.launched, Failed: d.failed,
	}
}

// LastRound returns the last round's stats.
func (d *Dispatcher) LastRound() Stats {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.last
}

// Run dispatches every interval until ctx ends.
func (d *Dispatcher) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := d.Round(ctx); err != nil && ctx.Err() == nil {
			d.log.Warn("dispatch round failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Round runs one dispatch round.
func (d *Dispatcher) Round(ctx context.Context) error {
	now := d.now()
	if n, err := d.store.FailStaleLaunching(ctx, now.Add(-staleLaunchingAge), now); err != nil {
		return err
	} else if n > 0 {
		d.log.Warn("recovered claims left launching", "count", n)
	}
	queued, err := d.intakeTasks(ctx)
	if err != nil {
		return err
	}
	claims, err := d.store.ListClaims(ctx)
	if err != nil {
		return err
	}
	seats, profiles, err := d.seats(ctx)
	if err != nil {
		return err
	}
	candidates, exhausted := d.candidates(queued, claims, now)
	plan := schedule.Schedule(candidates, seats, now, d.cfg.Policy)
	stats := Stats{At: now, Queued: map[string]int{}, Waiting: map[string]map[string]int{}, Exhausted: exhausted}
	priorities := make(map[string]string, len(candidates))
	for _, t := range candidates {
		priorities[t.ID] = t.Priority
		stats.Queued[t.Priority]++
	}
	for _, w := range plan.Waiting {
		p := priorities[w.TaskID]
		if stats.Waiting[p] == nil {
			stats.Waiting[p] = map[string]int{}
		}
		stats.Waiting[p][w.Reason]++
	}
	for _, a := range plan.Assignments {
		if d.launch(ctx, a, profiles[a.SeatID], priorities[a.TaskID]) {
			stats.Launched++
		} else {
			stats.Failed++
		}
	}
	if d.Limits != nil {
		if _, err := d.Limits.Scan(ctx, d.now()); err != nil {
			d.log.Warn("limit scan failed", "error", err)
		}
	}
	d.mu.Lock()
	d.last = stats
	d.launched += int64(stats.Launched)
	d.failed += int64(stats.Failed)
	d.mu.Unlock()
	return nil
}

// intakeTasks lists not-yet-started, assigned tasks in the intake step.
func (d *Dispatcher) intakeTasks(ctx context.Context) ([]kandev.Task, error) {
	var out []kandev.Task
	for page := 1; page <= maxPages; page++ {
		res, err := d.tasks.ListWorkspaceTasks(ctx, d.cfg.WorkspaceID, kandev.ListTasksOptions{Page: page, PageSize: pageSize})
		if err != nil {
			return nil, err
		}
		for _, t := range res.Tasks {
			if t.WorkflowStepID == d.cfg.IntakeStepID && t.AssigneeUserID != "" && t.PrimarySessionID == nil {
				out = append(out, t)
			}
		}
		if len(res.Tasks) < pageSize || page*pageSize >= res.Total {
			return out, nil
		}
	}
	d.log.Warn("intake listing truncated", "pages", maxPages)
	return out, nil
}

// seats returns schedulable seats, with free slots reduced by launches their
// heartbeats have not reported yet, and each seat's executor profile.
func (d *Dispatcher) seats(ctx context.Context) ([]schedule.Seat, map[string]string, error) {
	all, err := d.store.ListSeats(ctx)
	if err != nil {
		return nil, nil, err
	}
	pending, err := d.store.UnconfirmedLaunches(ctx)
	if err != nil {
		return nil, nil, err
	}
	seats := make([]schedule.Seat, 0, len(all))
	profiles := make(map[string]string, len(all))
	for _, s := range all {
		if s.State == store.SeatRetired {
			continue
		}
		free := s.FreeSlots - pending[s.ID]
		if free < 0 {
			free = 0
		}
		seats = append(seats, schedule.Seat{
			ID: s.ID, OwnerUserID: s.OwnerUserID, State: s.State, FreeSlots: free, Labels: s.Labels,
			Quota5hPct: s.Quota5hPct, QuotaWeekPct: s.QuotaWeekPct, ResetWeekAt: s.ResetWeekAt,
		})
		profiles[s.ID] = s.KandevExecutorProfileID
	}
	return seats, profiles, nil
}

// candidates drops tasks fleetd already launched, tasks still backing off,
// and tasks out of attempts.
func (d *Dispatcher) candidates(tasks []kandev.Task, claims map[string]store.Claim, now time.Time) ([]schedule.Task, int) {
	out := make([]schedule.Task, 0, len(tasks))
	exhausted := 0
	for _, t := range tasks {
		if c, ok := claims[t.ID]; ok {
			if c.State != store.ClaimFailed {
				continue
			}
			if c.Attempt >= d.cfg.MaxAttempts {
				exhausted++
				continue
			}
			if now.Before(c.UpdatedAt.Add(d.backoff(c.Attempt))) {
				continue
			}
		}
		priority := t.Priority
		if priority == "" {
			priority = schedule.PriorityMedium
		}
		out = append(out, schedule.Task{ID: t.ID, OwnerUserID: t.AssigneeUserID, Priority: priority, QueuedAt: t.CreatedAt})
	}
	return out, exhausted
}

func (d *Dispatcher) backoff(attempt int) time.Duration {
	b := d.cfg.RetryBackoff
	for i := 1; i < attempt; i++ {
		b *= 2
	}
	return b
}

// launch claims and starts one assignment; it reports whether it launched.
func (d *Dispatcher) launch(ctx context.Context, a schedule.Assignment, profileID, priority string) bool {
	now := d.now()
	attempt, err := d.store.BeginClaim(ctx, a.TaskID, a.SeatID, now)
	if errors.Is(err, store.ErrAlreadyClaimed) {
		return false
	}
	if err != nil {
		d.log.Error("claim failed", "task_id", a.TaskID, "seat_id", a.SeatID, "error", err)
		return false
	}
	res, err := d.launcher.LaunchSession(ctx, kandev.LaunchSessionInput{
		TaskID: a.TaskID, Intent: kandev.IntentStart, ExecutorProfileID: profileID, Priority: priority,
	})
	if err == nil && !res.Success {
		err = errors.New("session.launch returned success=false")
	}
	if err != nil {
		if ferr := d.store.FinishClaim(context.WithoutCancel(ctx), a.TaskID, store.ClaimFailed, "", err.Error(), d.now()); ferr != nil {
			d.log.Error("recording a failed launch failed", "task_id", a.TaskID, "error", ferr)
		}
		d.log.Warn("launch failed", "task_id", a.TaskID, "seat_id", a.SeatID, "attempt", attempt, "error", err)
		return false
	}
	if err := d.store.FinishClaim(context.WithoutCancel(ctx), a.TaskID, store.ClaimLaunched, res.SessionID, "", d.now()); err != nil {
		d.log.Error("recording a launch failed", "task_id", a.TaskID, "error", err)
	}
	d.log.Info("task launched", "task_id", a.TaskID, "seat_id", a.SeatID, "session_id", res.SessionID,
		"priority", priority, "score", a.Score, "attempt", attempt)
	return true
}
