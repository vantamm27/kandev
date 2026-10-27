// Package dispatch starts queued Kandev tasks on their owners' seats. Each
// round reads the intake step's tasks from Kandev, plans with the schedule
// package, claims each planned task in the store, and sends session.launch
// on the seat's fixed executor profile.
package dispatch

import (
	"context"
	"errors"
	"fmt"
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
	ResetTaskEnvironment(ctx context.Context, taskID string) error
}

// Launcher starts and stops Kandev sessions.
type Launcher interface {
	LaunchSession(ctx context.Context, in kandev.LaunchSessionInput) (kandev.LaunchSessionResult, error)
	SetPrimarySession(ctx context.Context, sessionID string) error
	StopSession(ctx context.Context, sessionID, reason string) error
}

// Config selects the tasks fleetd dispatches.
type Config struct {
	WorkspaceID string
	// IntakeStepID is the workflow step where assigned tasks wait for a seat.
	IntakeStepID string
	// AgentProfileID is sent with every launch: a task created with
	// start_agent=false has no agent profile of its own. A task created with
	// this profile but not started also waits for a seat, in any step.
	AgentProfileID string
	MaxAttempts    int
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
	// WaitingTasks are the tasks the round could not start.
	WaitingTasks []WaitingTask
	// Titles maps each claimed task the round listed to its title.
	Titles map[string]string
}

// WaitingTask is a queued task the round could not start, and why.
type WaitingTask struct {
	TaskID         string `json:"task_id"`
	Title          string `json:"title"`
	Priority       string `json:"priority"`
	AssigneeUserID string `json:"assignee_user_id"`
	Reason         string `json:"reason"`
}

// LimitScanner runs a post-round scan (limit handoffs, checkpoint nudges).
type LimitScanner interface {
	Scan(ctx context.Context, now time.Time) (int, error)
}

// TaskTracker records the Kandev state of launched tasks from the round's
// claims and the workspace tasks the round listed.
type TaskTracker interface {
	Track(ctx context.Context, claims map[string]store.Claim, listed map[string]kandev.Task, now time.Time) error
}

// Dispatcher runs dispatch rounds.
type Dispatcher struct {
	cfg      Config
	store    *store.Store
	tasks    Tasks
	launcher Launcher
	log      *slog.Logger
	now      func() time.Time
	// Tracker, Nudges and Limits, when set, run in that order after each
	// round's launches.
	Tracker TaskTracker
	Nudges  LimitScanner
	Limits  LimitScanner

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
	claims, err := d.store.ListClaims(ctx)
	if err != nil {
		return err
	}
	queued, listed, err := d.intakeTasks(ctx, claims)
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
		t := listed[w.TaskID]
		stats.WaitingTasks = append(stats.WaitingTasks, WaitingTask{
			TaskID: w.TaskID, Title: t.Title, Priority: p, AssigneeUserID: t.AssigneeUserID, Reason: w.Reason,
		})
	}
	stats.Titles = make(map[string]string, len(claims))
	for id := range claims {
		if t, ok := listed[id]; ok {
			stats.Titles[id] = t.Title
		}
	}
	for _, a := range plan.Assignments {
		if d.launch(ctx, a, profiles[a.SeatID], priorities[a.TaskID], listed[a.TaskID]) {
			stats.Launched++
		} else {
			stats.Failed++
		}
	}
	d.postRound(ctx, claims, listed)
	d.mu.Lock()
	d.last = stats
	d.launched += int64(stats.Launched)
	d.failed += int64(stats.Failed)
	d.mu.Unlock()
	return nil
}

func (d *Dispatcher) postRound(ctx context.Context, claims map[string]store.Claim, listed map[string]kandev.Task) {
	if d.Tracker != nil {
		if err := d.Tracker.Track(ctx, claims, listed, d.now()); err != nil {
			d.log.Warn("task state tracking failed", "error", err)
		}
	}
	for _, scan := range []struct {
		name    string
		scanner LimitScanner
	}{{"checkpoint nudge", d.Nudges}, {"limit", d.Limits}} {
		if scan.scanner == nil {
			continue
		}
		if _, err := scan.scanner.Scan(ctx, d.now()); err != nil {
			d.log.Warn(scan.name+" scan failed", "error", err)
		}
	}
}

// intakeTasks lists the tasks waiting for a seat that fleetd may launch. A
// task waits when it is assigned and in the intake step, or when it was
// created with the fleet agent profile and not started. It also returns every
// workspace task it read.
func (d *Dispatcher) intakeTasks(ctx context.Context, claims map[string]store.Claim) ([]kandev.Task, map[string]kandev.Task, error) {
	var out []kandev.Task
	listed := map[string]kandev.Task{}
	for page := 1; page <= maxPages; page++ {
		res, err := d.tasks.ListWorkspaceTasks(ctx, d.cfg.WorkspaceID, kandev.ListTasksOptions{Page: page, PageSize: pageSize})
		if err != nil {
			return nil, nil, err
		}
		for _, t := range res.Tasks {
			listed[t.ID] = t
			if d.waitsForSeat(t) && launchable(t, claims) {
				out = append(out, t)
			}
		}
		if len(res.Tasks) < pageSize || page*pageSize >= res.Total {
			return out, listed, nil
		}
	}
	d.log.Warn("intake listing truncated", "pages", maxPages)
	return out, listed, nil
}

// waitsForSeat reports whether a task is fleetd's to run, whatever its
// session; launchable then decides whether its session allows a launch.
func (d *Dispatcher) waitsForSeat(t kandev.Task) bool {
	if t.WorkflowStepID == d.cfg.IntakeStepID && t.AssigneeUserID != "" {
		return true
	}
	return t.PrimaryAgentProfileID == d.cfg.AgentProfileID && !kandev.TaskDone(t.State)
}

// launchable reports whether a task's session state lets fleetd launch it. A
// failed session is retried only when fleetd's own launch created it. A
// session that never sent a prompt is one Kandev prepared when someone opened
// or created the task, unless fleetd's own launch is still in flight. Any
// other session belongs to a person or to a running launch.
func launchable(t kandev.Task, claims map[string]store.Claim) bool {
	if t.PrimarySessionID == nil {
		return true
	}
	c, ok := claims[t.ID]
	switch {
	case t.PrimarySessionIs(kandev.SessionStateFailed):
		return ok && c.State == store.ClaimFailed
	case t.PrimarySessionIs(kandev.SessionStateCreated):
		return !ok || c.State == store.ClaimFailed
	default:
		return false
	}
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

// start sends session.launch for a task, after taking over a session Kandev
// prepared on its own host.
func (d *Dispatcher) start(ctx context.Context, t kandev.Task, profileID, priority string) (kandev.LaunchSessionResult, error) {
	if t.PrimarySessionIs(kandev.SessionStateCreated) {
		if err := d.takeOver(ctx, t.ID, *t.PrimarySessionID); err != nil {
			return kandev.LaunchSessionResult{}, err
		}
	}
	res, err := d.launcher.LaunchSession(ctx, kandev.LaunchSessionInput{
		TaskID: t.ID, Intent: kandev.IntentStart, AgentProfileID: d.cfg.AgentProfileID,
		ExecutorProfileID: profileID, Priority: priority,
	})
	if err == nil && !res.Success {
		err = errors.New("session.launch returned success=false")
	}
	return res, err
}

// takeOver clears a session Kandev prepared on its own host. Its environment
// lives there, so a seat launch cannot reuse it: fleetd stops the session and
// resets the task environment, and the launch then builds a fresh one on the
// seat.
func (d *Dispatcher) takeOver(ctx context.Context, taskID, sessionID string) error {
	if err := d.launcher.StopSession(ctx, sessionID, "fleetd: running the task on a seat"); err != nil {
		// Resetting the environment below also ends the prepared agent.
		d.log.Warn("could not stop the prepared session", "task_id", taskID, "session_id", sessionID, "error", err)
	}
	if err := d.tasks.ResetTaskEnvironment(ctx, taskID); err != nil && !kandev.IsNotFound(err) {
		return fmt.Errorf("reset the prepared task environment: %w", err)
	}
	return nil
}

func (d *Dispatcher) backoff(attempt int) time.Duration {
	b := d.cfg.RetryBackoff
	for i := 1; i < attempt; i++ {
		b *= 2
	}
	return b
}

// launch claims and starts one assignment; it reports whether it launched.
func (d *Dispatcher) launch(ctx context.Context, a schedule.Assignment, profileID, priority string, t kandev.Task) bool {
	now := d.now()
	attempt, err := d.store.BeginClaim(ctx, a.TaskID, a.SeatID, now)
	if errors.Is(err, store.ErrAlreadyClaimed) {
		return false
	}
	if err != nil {
		d.log.Error("claim failed", "task_id", a.TaskID, "seat_id", a.SeatID, "error", err)
		return false
	}
	res, err := d.start(ctx, t, profileID, priority)
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
	// Kandev keeps the earlier session primary; the new one replaces it.
	if t.PrimarySessionID != nil && res.SessionID != "" {
		if err := d.launcher.SetPrimarySession(ctx, res.SessionID); err != nil {
			d.log.Warn("could not make the retried session primary", "task_id", a.TaskID, "session_id", res.SessionID, "error", err)
		}
	}
	return true
}
