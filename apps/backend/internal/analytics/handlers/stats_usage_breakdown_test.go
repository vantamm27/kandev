package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kandev/kandev/internal/analytics/models"
)

// usageRepo returns a fixed number of usage rows and records the limit asked for.
type usageRepo struct {
	recordingRepo
	rows       int
	gotLimit   int
	gotStartOK bool
}

func (r *usageRepo) GetUsageBreakdown(
	_ context.Context, _ string, start *time.Time, limit int,
) ([]*models.UsageBreakdown, error) {
	r.gotLimit = limit
	r.gotStartOK = start != nil
	out := make([]*models.UsageBreakdown, 0, r.rows)
	for i := 0; i < r.rows; i++ {
		out = append(out, &models.UsageBreakdown{
			Day: "2026-10-20", ExecutorID: "exec-1", AssigneeUserID: "user-a", Model: "opus",
			EventCount: 2, TokensIn: 10, TokensCachedRead: 4, TokensOut: 3, TokensTotal: 17, CostSubcents: 5,
		})
	}
	return out, nil
}

func getUsageBreakdown(t *testing.T, repo *usageRepo, query string) usageBreakdownResponse {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	RegisterStatsRoutes(router, repo, newOwnerAuthorizer(), authzTestLogger(t))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/"+wsA+"/stats/usage-breakdown"+query, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body usageBreakdownResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body
}

func TestUsageBreakdownReturnsRowsWithoutMore(t *testing.T) {
	repo := &usageRepo{rows: 1}
	body := getUsageBreakdown(t, repo, "?range=month")

	if repo.gotLimit != usageBreakdownLimit+1 {
		t.Fatalf("limit = %d, want %d (one extra row to detect has_more)", repo.gotLimit, usageBreakdownLimit+1)
	}
	if !repo.gotStartOK {
		t.Fatal("range=month must pass a range start to the repository")
	}
	if body.HasMore || len(body.Usage) != 1 {
		t.Fatalf("has_more = %v, rows = %d, want false and 1", body.HasMore, len(body.Usage))
	}
	got := body.Usage[0]
	if got.Day != "2026-10-20" || got.ExecutorID != "exec-1" || got.AssigneeUserID != "user-a" ||
		got.Model != "opus" || got.EventCount != 2 || got.TokensIn != 10 || got.TokensCachedRead != 4 ||
		got.TokensOut != 3 || got.TokensTotal != 17 || got.CostSubcents != 5 {
		t.Fatalf("unexpected row: %+v", got)
	}
}

func TestUsageBreakdownTrimsExtraRowAndReportsMore(t *testing.T) {
	repo := &usageRepo{rows: usageBreakdownLimit + 1}
	body := getUsageBreakdown(t, repo, "")

	if !body.HasMore || len(body.Usage) != usageBreakdownLimit {
		t.Fatalf("has_more = %v, rows = %d, want true and %d", body.HasMore, len(body.Usage), usageBreakdownLimit)
	}
}
