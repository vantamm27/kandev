package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kandev/kandev/apps/fleet/internal/seatstate"
)

// Quota probe defaults. The probe is the smallest Messages request that
// returns the subscription's unified rate-limit headers; count_tokens does
// not return them.
const (
	DefaultQuotaProbeInterval = 5 * time.Minute
	DefaultQuotaProbeURL      = "https://api.anthropic.com/v1/messages"
	DefaultQuotaProbeModel    = "claude-haiku-4-5"
	oauthBetaHeader           = "oauth-2025-04-20"
	anthropicAPIVersion       = "2023-06-01"
)

const (
	headerUnifiedPrefix = "Anthropic-Ratelimit-Unified-"
	unifiedRejected     = "rejected"
)

// QuotaProbe reads the seat's subscription usage from the rate-limit headers
// of a one-token request made with the seat's own login. The token never
// leaves the node.
type QuotaProbe struct {
	HTTP  *http.Client
	URL   string
	Model string
	Token func(ctx context.Context) (string, error)
}

// Probe sends one request and returns the usage it reports as of now.
func (p *QuotaProbe) Probe(ctx context.Context, now time.Time) (QuotaReport, error) {
	token, err := p.Token(ctx)
	if err != nil {
		return QuotaReport{}, err
	}
	body, err := json.Marshal(map[string]any{
		"model": p.Model, "max_tokens": 1,
		"messages": []map[string]string{{"role": "user", "content": "ok"}},
	})
	if err != nil {
		return QuotaReport{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.URL, bytes.NewReader(body))
	if err != nil {
		return QuotaReport{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Anthropic-Version", anthropicAPIVersion)
	req.Header.Set("Anthropic-Beta", oauthBetaHeader)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.HTTP.Do(req)
	if err != nil {
		return QuotaReport{}, fmt.Errorf("quota probe: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	report, ok := parseQuotaHeaders(resp.Header, now)
	if !ok {
		if resp.StatusCode != http.StatusOK {
			return QuotaReport{}, fmt.Errorf("quota probe: HTTP %d", resp.StatusCode)
		}
		// An API-billed token gets no subscription headers.
		return QuotaReport{}, errors.New("quota probe: response has no subscription rate-limit headers")
	}
	return report, nil
}

// parseQuotaHeaders turns Anthropic's unified rate-limit headers into a quota
// report. It reports false when the headers are absent.
func parseQuotaHeaders(h http.Header, now time.Time) (QuotaReport, bool) {
	get := func(name string) string { return strings.TrimSpace(h.Get(headerUnifiedPrefix + name)) }
	status := get("Status")
	pct5h, ok5h := utilizationPct(get("5h-Utilization"))
	pctWeek, okWeek := utilizationPct(get("7d-Utilization"))
	if status == "" && !ok5h && !okWeek {
		return QuotaReport{}, false
	}
	r := QuotaReport{UpdatedAt: now.UTC(), Reset5hAt: unixTime(get("5h-Reset")), ResetWeekAt: unixTime(get("7d-Reset"))}
	if ok5h {
		r.Quota5hPct = &pct5h
	}
	if okWeek {
		r.QuotaWeekPct = &pctWeek
	}
	if status == unifiedRejected {
		r.Limit = rejectedWindow(get("5h-Status"), get("7d-Status"), get("Representative-Claim"))
	}
	return r, true
}

// rejectedWindow names the window that blocks the seat. The weekly window wins
// because it outlasts the 5-hour one.
func rejectedWindow(status5h, statusWeek, claim string) string {
	switch {
	case statusWeek == unifiedRejected:
		return seatstate.LimitWeek
	case status5h == unifiedRejected:
		return seatstate.Limit5h
	case strings.HasPrefix(claim, "seven_day"):
		return seatstate.LimitWeek
	default:
		return seatstate.Limit5h
	}
}

// utilizationPct converts a 0..1 utilization to a percentage with two decimals.
func utilizationPct(raw string) (float64, bool) {
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || v < 0 {
		return 0, false
	}
	return math.Round(v*10000) / 100, true
}

func unixTime(raw string) *time.Time {
	secs, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || secs <= 0 {
		return nil
	}
	t := time.Unix(secs, 0).UTC()
	return &t
}

// WriteQuota atomically replaces the quota file with r.
func WriteQuota(path string, r QuotaReport) error {
	if err := writeJSONAtomic(path, r); err != nil {
		return fmt.Errorf("write quota file: %w", err)
	}
	return nil
}

// SeatToken returns the subscription token the seat's SSH sessions use: the
// CLAUDE_CODE_OAUTH_TOKEN exported by the login shell, or else the access
// token in Claude Code's credentials file.
func SeatToken(home, shell string) func(ctx context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		out, err := sshShellCommand(ctx, home, shell, `printf %s "${CLAUDE_CODE_OAUTH_TOKEN:-}"`).Output()
		if err == nil {
			if token := strings.TrimSpace(string(out)); token != "" {
				return token, nil
			}
		}
		return credentialsFileToken(filepath.Join(home, ".claude", ".credentials.json"))
	}
}

func credentialsFileToken(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", errors.New("no subscription login for the quota probe: CLAUDE_CODE_OAUTH_TOKEN is not exported and Claude Code's credentials file is unreadable")
	}
	var creds struct {
		ClaudeAiOauth struct {
			AccessToken string `json:"accessToken"`
		} `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(raw, &creds); err != nil || creds.ClaudeAiOauth.AccessToken == "" {
		return "", errors.New("no subscription login for the quota probe: Claude Code's credentials file has no access token")
	}
	return creds.ClaudeAiOauth.AccessToken, nil
}
