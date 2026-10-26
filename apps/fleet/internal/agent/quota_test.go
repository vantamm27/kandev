package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

var qNow = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

func writeQuota(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "quota.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadQuota(t *testing.T) {
	for name, tc := range map[string]struct {
		body      string
		wantLimit string
		wantPct   bool
	}{
		"fresh no limit":             {`{"quota_5h_pct":40,"updated_at":"2026-10-26T11:55:00Z"}`, "", true},
		"5h limit before reset":      {`{"limit":"5h","reset_5h_at":"2026-10-26T13:00:00Z","updated_at":"2026-10-26T09:00:00Z"}`, "5h", false},
		"5h limit after reset":       {`{"limit":"5h","reset_5h_at":"2026-10-26T11:00:00Z","updated_at":"2026-10-26T10:00:00Z"}`, "", false},
		"weekly limit no date fresh": {`{"limit":"week","updated_at":"2026-10-26T11:50:00Z"}`, "week", false},
		"weekly limit no date stale": {`{"limit":"week","updated_at":"2026-10-26T10:00:00Z"}`, "", false},
		"stale pct dropped":          {`{"quota_week_pct":90,"updated_at":"2026-10-26T10:00:00Z"}`, "", false},
		"unknown limit ignored":      {`{"limit":"daily","updated_at":"2026-10-26T11:59:00Z"}`, "", false},
	} {
		t.Run(name, func(t *testing.T) {
			r, err := ReadQuota(writeQuota(t, tc.body), qNow)
			if err != nil {
				t.Fatalf("ReadQuota: %v", err)
			}
			if r.Limit != tc.wantLimit {
				t.Fatalf("limit = %q, want %q", r.Limit, tc.wantLimit)
			}
			if hasPct := r.Quota5hPct != nil || r.QuotaWeekPct != nil; hasPct != tc.wantPct {
				t.Fatalf("percentages present = %v, want %v", hasPct, tc.wantPct)
			}
		})
	}
}

func TestReadQuotaMissingAndBroken(t *testing.T) {
	if r, err := ReadQuota(filepath.Join(t.TempDir(), "none.json"), qNow); err != nil || r.Limit != "" {
		t.Fatalf("missing file = %+v, %v; want unknown", r, err)
	}
	if r, err := ReadQuota("", qNow); err != nil || r.Limit != "" {
		t.Fatalf("no path = %+v, %v", r, err)
	}
	if _, err := ReadQuota(writeQuota(t, `{not json`), qNow); err == nil {
		t.Fatal("broken file must error")
	}
}
