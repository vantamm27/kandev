package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/kandev/kandev/internal/analytics/models"
	"github.com/kandev/kandev/internal/db/dialect"
)

// GetUsageBreakdown sums recorded token usage per UTC day, session executor,
// task human assignee, and model, newest day first. Usage from ephemeral and
// automation-run tasks is excluded, matching the other workspace stats.
func (r *Repository) GetUsageBreakdown(
	ctx context.Context,
	workspaceID string,
	start *time.Time,
	limit int,
) (items []*models.UsageBreakdown, err error) {
	parentCtx := ctx
	operationCtx, release, err := r.beginAnalyticsOperation(parentCtx)
	if err != nil {
		return nil, err
	}
	defer func() {
		err = normalizeAnalyticsOperationError(parentCtx, operationCtx, err)
		release()
	}()
	ctx = operationCtx

	startArg := rangeStartArg(start)
	drv := r.ro.DriverName()
	// Positional GROUP BY/ORDER BY: PostgreSQL resolves a bare name that
	// matches an input column (executor_id) to that column, not the alias.
	query := fmt.Sprintf(`
		SELECT
			%s AS day,
			COALESCE(s.executor_id, '') AS executor_id,
			COALESCE(t.assignee_user_id, '') AS assignee_user_id,
			e.model,
			COUNT(*) AS event_count,
			COALESCE(SUM(e.tokens_in), 0) AS tokens_in,
			COALESCE(SUM(e.tokens_cached_read), 0) AS tokens_cached_read,
			COALESCE(SUM(e.tokens_cached_write), 0) AS tokens_cached_write,
			COALESCE(SUM(e.tokens_out), 0) AS tokens_out,
			COALESCE(SUM(e.tokens_total), 0) AS tokens_total,
			COALESCE(SUM(e.cost_subcents), 0) AS cost_subcents
		FROM task_usage_events e
		JOIN tasks t ON t.id = e.task_id
		LEFT JOIN task_sessions s ON s.id = e.session_id
		WHERE t.workspace_id = ? AND t.is_ephemeral = 0`+andNotAutomationOriginT+` AND `+rangeStartPredicate(drv, "e.occurred_at")+`
		GROUP BY 1, 2, 3, 4
		ORDER BY 1 DESC, 10 DESC, 2 ASC, 3 ASC, 4 ASC
		LIMIT ?
	`, dialect.DateText(drv, "e.occurred_at"))

	rows, err := r.ro.QueryContext(ctx, r.ro.Rebind(query), workspaceID, startArg, startArg, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var results []*models.UsageBreakdown
	for rows.Next() {
		var row models.UsageBreakdown
		if err := rows.Scan(
			&row.Day, &row.ExecutorID, &row.AssigneeUserID, &row.Model, &row.EventCount,
			&row.TokensIn, &row.TokensCachedRead, &row.TokensCachedWrite, &row.TokensOut,
			&row.TokensTotal, &row.CostSubcents,
		); err != nil {
			return nil, err
		}
		results = append(results, &row)
	}
	return results, rows.Err()
}
