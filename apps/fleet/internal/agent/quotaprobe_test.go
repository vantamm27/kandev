package agent

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/kandev/kandev/apps/fleet/internal/seatstate"
)

func unifiedHeaders(kv map[string]string) http.Header {
	h := http.Header{}
	for k, v := range kv {
		h.Set(headerUnifiedPrefix+k, v)
	}
	return h
}

func TestParseQuotaHeaders(t *testing.T) {
	reset5h := qNow.Add(2 * time.Hour).Unix()
	resetWeek := qNow.Add(72 * time.Hour).Unix()
	r, ok := parseQuotaHeaders(unifiedHeaders(map[string]string{
		"Status": "allowed", "5h-Utilization": "0.56", "7d-Utilization": "0.03",
		"5h-Reset": itoa(reset5h), "7d-Reset": itoa(resetWeek),
	}), qNow)
	if !ok || r.Limit != seatstate.LimitNone || r.Quota5hPct == nil || *r.Quota5hPct != 56 ||
		r.QuotaWeekPct == nil || *r.QuotaWeekPct != 3 || r.Reset5hAt.Unix() != reset5h ||
		r.ResetWeekAt.Unix() != resetWeek || !r.UpdatedAt.Equal(qNow) {
		t.Fatalf("allowed report = %+v, ok %v", r, ok)
	}
	if _, ok := parseQuotaHeaders(http.Header{}, qNow); ok {
		t.Fatal("a response without unified headers must not produce a report")
	}
}

func TestParseQuotaHeadersRejectedWindow(t *testing.T) {
	for name, tc := range map[string]struct {
		headers map[string]string
		want    string
	}{
		"5h status":          {map[string]string{"Status": "rejected", "5h-Status": "rejected", "7d-Status": "allowed"}, seatstate.Limit5h},
		"weekly status wins": {map[string]string{"Status": "rejected", "5h-Status": "rejected", "7d-Status": "rejected"}, seatstate.LimitWeek},
		"weekly claim":       {map[string]string{"Status": "rejected", "Representative-Claim": "seven_day_opus"}, seatstate.LimitWeek},
		"unknown window":     {map[string]string{"Status": "rejected"}, seatstate.Limit5h},
		"warning is allowed": {map[string]string{"Status": "allowed_warning", "5h-Utilization": "0.9"}, seatstate.LimitNone},
	} {
		r, ok := parseQuotaHeaders(unifiedHeaders(tc.headers), qNow)
		if !ok || r.Limit != tc.want {
			t.Errorf("%s: limit = %q (ok %v), want %q", name, r.Limit, ok, tc.want)
		}
	}
}

func TestQuotaProbeSendsSeatLoginAndReadsRejectedResponse(t *testing.T) {
	var gotAuth, gotBeta string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotBeta = r.Header.Get("Authorization"), r.Header.Get("Anthropic-Beta")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set(headerUnifiedPrefix+"Status", "rejected")
		w.Header().Set(headerUnifiedPrefix+"5h-Status", "rejected")
		w.Header().Set(headerUnifiedPrefix+"5h-Utilization", "1.0")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	p := &QuotaProbe{HTTP: srv.Client(), URL: srv.URL, Model: "m",
		Token: func(context.Context) (string, error) { return "seat-token", nil }}
	r, err := p.Probe(context.Background(), qNow)
	if err != nil || r.Limit != seatstate.Limit5h || *r.Quota5hPct != 100 {
		t.Fatalf("probe = %+v, %v", r, err)
	}
	if gotAuth != "Bearer seat-token" || gotBeta != oauthBetaHeader || gotBody["model"] != "m" || gotBody["max_tokens"] != float64(1) {
		t.Fatalf("request auth %q beta %q body %v", gotAuth, gotBeta, gotBody)
	}
}

func TestQuotaProbeErrors(t *testing.T) {
	for name, status := range map[string]int{"no headers": http.StatusOK, "unauthorized": http.StatusUnauthorized} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
		p := &QuotaProbe{HTTP: srv.Client(), URL: srv.URL,
			Token: func(context.Context) (string, error) { return "t", nil }}
		if _, err := p.Probe(context.Background(), qNow); err == nil {
			t.Errorf("%s: expected an error", name)
		}
		srv.Close()
	}
}

func TestWriteQuotaRoundTripsThroughReadQuota(t *testing.T) {
	path := filepath.Join(t.TempDir(), "q", "quota.json")
	pct := 42.5
	reset := qNow.Add(time.Hour)
	if err := WriteQuota(path, QuotaReport{Limit: seatstate.Limit5h, Quota5hPct: &pct, Reset5hAt: &reset, UpdatedAt: qNow}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("quota file mode = %v, %v", info, err)
	}
	r, err := ReadQuota(path, qNow.Add(time.Minute))
	if err != nil || r.Limit != seatstate.Limit5h || *r.Quota5hPct != pct {
		t.Fatalf("read back = %+v, %v", r, err)
	}
}

func TestSeatTokenPrefersShellExportThenCredentialsFile(t *testing.T) {
	home := t.TempDir()
	shell := filepath.Join(home, "fake-shell")
	if err := os.WriteFile(shell, []byte("#!/bin/sh\nprintf 'from-shell'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got, err := SeatToken(home, shell)(context.Background()); err != nil || got != "from-shell" {
		t.Fatalf("shell token = %q, %v", got, err)
	}
	silent := filepath.Join(home, "silent-shell")
	if err := os.WriteFile(silent, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := SeatToken(home, silent)(context.Background()); err == nil {
		t.Fatal("no export and no credentials file must be an error")
	}
	creds := filepath.Join(home, ".claude", ".credentials.json")
	if err := os.MkdirAll(filepath.Dir(creds), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(creds, []byte(`{"claudeAiOauth":{"accessToken":"from-file"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := SeatToken(home, silent)(context.Background()); err != nil || got != "from-file" {
		t.Fatalf("credentials file token = %q, %v", got, err)
	}
}

func TestRefreshQuotaWritesTheFileAndKeepsItOnFailure(t *testing.T) {
	fail := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if fail {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set(headerUnifiedPrefix+"Status", "allowed")
		w.Header().Set(headerUnifiedPrefix+"5h-Utilization", "0.25")
	}))
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "quota.json")
	r := &Reporter{
		Config: Config{QuotaFile: path}, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now: func() time.Time { return qNow },
		Probe: &QuotaProbe{HTTP: srv.Client(), URL: srv.URL,
			Token: func(context.Context) (string, error) { return "t", nil }},
	}
	r.refreshQuota(context.Background())
	fail = true
	r.refreshQuota(context.Background())
	q, err := ReadQuota(path, qNow)
	if err != nil || q.Quota5hPct == nil || *q.Quota5hPct != 25 {
		t.Fatalf("quota file = %+v, %v; a failed probe must keep the last report", q, err)
	}
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }
