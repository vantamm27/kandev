package sqlite

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"

	"github.com/kandev/kandev/internal/analytics/models"
	"github.com/kandev/kandev/internal/db"
	taskmodels "github.com/kandev/kandev/internal/task/models"
	tasksqlite "github.com/kandev/kandev/internal/task/repository/sqlite"
)

type usageFixture struct {
	t      *testing.T
	ctx    context.Context
	writer *sqlx.DB
	tasks  *tasksqlite.Repository
	stats  *Repository
	seq    int
}

func newUsageFixture(t *testing.T) *usageFixture {
	t.Helper()
	raw, err := db.OpenSQLite(filepath.Join(t.TempDir(), "usage.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, raw.Close()) })
	return newUsageFixtureOn(t, sqlx.NewDb(raw, "sqlite3"))
}

func newUsageFixtureOn(t *testing.T, writer *sqlx.DB) *usageFixture {
	t.Helper()
	tasks, err := tasksqlite.NewWithDB(writer, writer, nil)
	require.NoError(t, err)
	stats, err := NewWithDB(writer, writer)
	require.NoError(t, err)
	f := &usageFixture{t: t, ctx: context.Background(), writer: writer, tasks: tasks, stats: stats}
	for _, id := range []string{"main", "other"} {
		require.NoError(t, tasks.CreateWorkspace(f.ctx, &taskmodels.Workspace{ID: id, Name: id}))
		require.NoError(t, tasks.CreateWorkflow(f.ctx, &taskmodels.Workflow{ID: id + "-flow", WorkspaceID: id, Name: "Flow"}))
	}
	return f
}

func (f *usageFixture) task(id, workspaceID, assignee string, ephemeral bool) {
	f.t.Helper()
	task := &taskmodels.Task{ID: id, WorkspaceID: workspaceID, Title: id, AssigneeUserID: assignee, IsEphemeral: ephemeral}
	if !ephemeral {
		task.WorkflowID = workspaceID + "-flow"
	}
	require.NoError(f.t, f.tasks.CreateTask(f.ctx, task))
}

func (f *usageFixture) session(id, taskID, executorID string) {
	f.t.Helper()
	require.NoError(f.t, f.tasks.CreateTaskSession(f.ctx, &taskmodels.TaskSession{
		ID: id, TaskID: taskID, StartedAt: time.Now().UTC(), State: taskmodels.TaskSessionStateCompleted,
	}))
	_, err := f.writer.Exec(f.writer.Rebind(`UPDATE task_sessions SET executor_id = ? WHERE id = ?`), executorID, id)
	require.NoError(f.t, err)
}

func (f *usageFixture) event(taskID, sessionID, model string, at time.Time, in, cachedRead, out, cost int64) {
	f.t.Helper()
	f.seq++
	var session any
	if sessionID != "" {
		session = sessionID
	}
	_, err := f.writer.Exec(f.writer.Rebind(`
		INSERT INTO task_usage_events (
			usage_event_id, task_id, session_id, model, tokens_in, tokens_cached_read, tokens_out,
			tokens_total, cost_subcents, cost_source, contract_version, occurred_at, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'catalog', 1, ?, ?)`),
		fmt.Sprintf("evt-%d", f.seq), taskID, session, model, in, cachedRead, out,
		in+cachedRead+out, cost, at.UTC(), at.UTC())
	require.NoError(f.t, err)
}

func TestGetUsageBreakdownGroupsByDayExecutorAssigneeAndModel(t *testing.T) {
	assertUsageBreakdownGrouping(t, newUsageFixture(t))
}

func assertUsageBreakdownGrouping(t *testing.T, f *usageFixture) {
	t.Helper()
	f.task("assigned", "main", "user-a", false)
	f.task("unassigned", "main", "", false)
	f.task("quick-chat", "main", "user-a", true)
	f.task("foreign", "other", "user-a", false)
	f.session("s-a", "assigned", "exec-mini-1")
	f.session("s-b", "unassigned", "exec-mini-2")
	f.session("s-chat", "quick-chat", "exec-mini-1")
	f.session("s-foreign", "foreign", "exec-mini-1")

	day1 := time.Date(2026, 10, 20, 9, 30, 0, 123456789, time.UTC)
	day2 := day1.AddDate(0, 0, 1)
	f.event("assigned", "s-a", "opus", day1, 100, 50, 10, 7)
	f.event("assigned", "s-a", "opus", day1.Add(2*time.Hour), 200, 0, 20, 3)
	f.event("assigned", "s-a", "sonnet", day1, 5, 0, 1, 1)
	f.event("unassigned", "s-b", "opus", day2, 30, 0, 3, 2)
	f.event("unassigned", "", "opus", day2, 4, 0, 0, 0)
	f.event("quick-chat", "s-chat", "opus", day1, 999, 0, 0, 0)
	f.event("foreign", "s-foreign", "opus", day1, 999, 0, 0, 0)

	rows, err := f.stats.GetUsageBreakdown(f.ctx, "main", nil, 100)
	require.NoError(t, err)
	require.Equal(t, []*models.UsageBreakdown{
		{Day: "2026-10-21", ExecutorID: "exec-mini-2", Model: "opus", EventCount: 1, TokensIn: 30, TokensOut: 3, TokensTotal: 33, CostSubcents: 2},
		{Day: "2026-10-21", ExecutorID: "", Model: "opus", EventCount: 1, TokensIn: 4, TokensTotal: 4},
		{Day: "2026-10-20", ExecutorID: "exec-mini-1", AssigneeUserID: "user-a", Model: "opus", EventCount: 2,
			TokensIn: 300, TokensCachedRead: 50, TokensOut: 30, TokensTotal: 380, CostSubcents: 10},
		{Day: "2026-10-20", ExecutorID: "exec-mini-1", AssigneeUserID: "user-a", Model: "sonnet", EventCount: 1,
			TokensIn: 5, TokensOut: 1, TokensTotal: 6, CostSubcents: 1},
	}, rows)
}

func TestGetUsageBreakdownHonorsRangeStartAndLimit(t *testing.T) {
	f := newUsageFixture(t)
	f.task("assigned", "main", "user-a", false)
	f.session("s-a", "assigned", "exec-mini-1")
	old := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	recent := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	f.event("assigned", "s-a", "opus", old, 10, 0, 0, 0)
	f.event("assigned", "s-a", "opus", recent, 20, 0, 0, 0)
	f.event("assigned", "s-a", "sonnet", recent, 30, 0, 0, 0)

	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	rows, err := f.stats.GetUsageBreakdown(f.ctx, "main", &start, 100)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	for _, row := range rows {
		require.Equal(t, "2026-10-20", row.Day)
	}

	limited, err := f.stats.GetUsageBreakdown(f.ctx, "main", nil, 1)
	require.NoError(t, err)
	require.Len(t, limited, 1)
	require.Equal(t, "sonnet", limited[0].Model, "highest total first within the newest day")
}
