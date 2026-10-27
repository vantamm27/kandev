package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/kandev/kandev/pkg/pluginsdk"
)

// Action keys declared in manifest.yaml.
const (
	actionOverview = "fleet.overview"
	actionAccept   = "fleet.handoff.accept"
	actionTasks    = "fleet.tasks"
	// actionManageSeat is admin-only in the manifest, so Kandev rejects
	// members before the plugin sees the request.
	actionManageSeat = "fleet.seat.manage"
)

// seatOps maps fleet.seat.manage operations to fleetd admin paths.
var seatOps = map[string]string{
	"drain":        "/drain",
	"undrain":      "/undrain",
	"retire":       "/retire",
	"retire_force": "/retire?force=true",
}

// fleetPlugin proxies member actions to fleetd's admin API.
type fleetPlugin struct {
	pluginsdk.UnimplementedPlugin
	config func(ctx context.Context) (fleetdConfig, error)
	http   *http.Client
}

type fleetdConfig struct {
	URL        string
	AdminToken string
}

var errNotConfigured = errors.New("set the fleetd URL and admin token in Settings > Plugins > Fleet")

func (p *fleetPlugin) hostConfig(ctx context.Context) (fleetdConfig, error) {
	host := p.Host()
	if host == nil {
		return fleetdConfig{}, errors.New("plugin host is not connected")
	}
	raw, err := host.GetConfig(ctx)
	if err != nil {
		return fleetdConfig{}, err
	}
	url, _ := raw["fleetd_url"].(string)
	token, _ := raw["admin_token"].(string)
	cfg := fleetdConfig{URL: strings.TrimRight(strings.TrimSpace(url), "/"), AdminToken: strings.TrimSpace(token)}
	if cfg.URL == "" || cfg.AdminToken == "" {
		return fleetdConfig{}, errNotConfigured
	}
	return cfg, nil
}

// seat and handoff are the fleetd fields the UI shows.
type seat struct {
	ID           string     `json:"id"`
	OwnerUserID  string     `json:"owner_user_id"`
	SSHHost      string     `json:"ssh_host"`
	SSHUser      string     `json:"ssh_user"`
	State        string     `json:"state"`
	LimitReason  string     `json:"limit_reason"`
	Capacity     int        `json:"capacity"`
	FreeSlots    int        `json:"free_slots"`
	Quota5hPct   *float64   `json:"quota_5h_pct,omitempty"`
	QuotaWeekPct *float64   `json:"quota_week_pct,omitempty"`
	Reset5hAt    *time.Time `json:"reset_5h_at,omitempty"`
	ResetWeekAt  *time.Time `json:"reset_week_at,omitempty"`
	// LastHeartbeatAt is when the seat's node last reported.
	LastHeartbeatAt *time.Time `json:"last_heartbeat_at,omitempty"`
}

type handoff struct {
	ID         string    `json:"id"`
	TaskID     string    `json:"task_id"`
	FromSeatID string    `json:"from_seat_id"`
	ToSeatID   string    `json:"to_seat_id,omitempty"`
	State      string    `json:"state"`
	Attempt    int       `json:"attempt"`
	CreatedAt  time.Time `json:"created_at"`
}

type overview struct {
	ActorID  string    `json:"actor_id"`
	Seats    []seat    `json:"seats"`
	Handoffs []handoff `json:"handoffs"`
	// CanAccept is true when the viewer owns an online seat with a free slot.
	CanAccept bool `json:"can_accept"`
}

// HandleAction implements pluginsdk.ActionHandler.
func (p *fleetPlugin) HandleAction(ctx context.Context, req *pluginsdk.PluginActionRequest) (*pluginsdk.PluginActionResponse, error) {
	if req == nil {
		return nil, errors.New("missing action request")
	}
	cfg, err := p.config(ctx)
	if err != nil {
		return reply(http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
	}
	switch req.ActionKey {
	case actionOverview:
		return p.overview(ctx, cfg, req.Context.ActorID)
	case actionAccept:
		return p.accept(ctx, cfg, req.Context.ActorID, req.Body)
	case actionTasks:
		var out json.RawMessage
		if err := p.call(ctx, cfg, http.MethodGet, "/v1/admin/tasks", nil, &out); err != nil {
			return upstream(err)
		}
		return reply(http.StatusOK, out)
	case actionManageSeat:
		return p.manageSeat(ctx, cfg, req.Context.ActorID, req.Body)
	default:
		return reply(http.StatusNotFound, map[string]string{"error": "unknown action"})
	}
}

func (p *fleetPlugin) overview(ctx context.Context, cfg fleetdConfig, actor string) (*pluginsdk.PluginActionResponse, error) {
	seats, err := p.listSeats(ctx, cfg)
	if err != nil {
		return upstream(err)
	}
	var handoffs struct {
		Handoffs []handoff `json:"handoffs"`
	}
	if err := p.call(ctx, cfg, http.MethodGet, "/v1/admin/handoffs?state=offered,exhausted", nil, &handoffs); err != nil {
		return upstream(err)
	}
	_, canAccept := pickSeat(seats, actor)
	return reply(http.StatusOK, overview{ActorID: actor, Seats: seats, Handoffs: handoffs.Handoffs, CanAccept: canAccept})
}

func (p *fleetPlugin) accept(ctx context.Context, cfg fleetdConfig, actor string, body []byte) (*pluginsdk.PluginActionResponse, error) {
	var in struct {
		HandoffID string `json:"handoffId"`
	}
	if err := json.Unmarshal(body, &in); err != nil || in.HandoffID == "" {
		return reply(http.StatusBadRequest, map[string]string{"error": "handoffId is required"})
	}
	seats, err := p.listSeats(ctx, cfg)
	if err != nil {
		return upstream(err)
	}
	mine, ok := pickSeat(seats, actor)
	if !ok {
		return reply(http.StatusConflict, map[string]string{"error": "you have no online seat with a free slot"})
	}
	var result handoff
	payload := map[string]string{"seat_id": mine.ID}
	if err := p.call(ctx, cfg, http.MethodPost, "/v1/admin/handoffs/"+in.HandoffID+"/accept", payload, &result); err != nil {
		return fleetdFailure(err)
	}
	return reply(http.StatusOK, result)
}

func (p *fleetPlugin) manageSeat(ctx context.Context, cfg fleetdConfig, actor string, body []byte) (*pluginsdk.PluginActionResponse, error) {
	var in struct {
		SeatID string `json:"seatId"`
		Op     string `json:"op"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return reply(http.StatusBadRequest, map[string]string{"error": "invalid request"})
	}
	suffix, ok := seatOps[in.Op]
	if !ok || !seatIDPattern.MatchString(in.SeatID) {
		return reply(http.StatusBadRequest, map[string]string{"error": "seatId and a known op are required"})
	}
	var result map[string]string
	if err := p.call(ctx, cfg, http.MethodPost, "/v1/admin/seats/"+in.SeatID+suffix, nil, &result); err != nil {
		return fleetdFailure(err)
	}
	logger.Info("seat changed", "seat_id", in.SeatID, "op", in.Op, "actor", actor)
	return reply(http.StatusOK, result)
}

// seatIDPattern keeps the seat ID a single path segment.
var seatIDPattern = regexp.MustCompile(`^seat_[A-Za-z0-9]+$`)

// pickSeat chooses the actor's own online seat with the most free slots. The
// actor comes from Kandev's verified action context, never the request body,
// so a member can only take work onto their own seat.
func pickSeat(seats []seat, actor string) (seat, bool) {
	if actor == "" {
		return seat{}, false
	}
	var mine []seat
	for _, s := range seats {
		if s.OwnerUserID == actor && s.State == "online" && s.FreeSlots > 0 {
			mine = append(mine, s)
		}
	}
	if len(mine) == 0 {
		return seat{}, false
	}
	sort.Slice(mine, func(i, j int) bool {
		if mine[i].FreeSlots != mine[j].FreeSlots {
			return mine[i].FreeSlots > mine[j].FreeSlots
		}
		return mine[i].ID < mine[j].ID
	})
	return mine[0], true
}

func (p *fleetPlugin) listSeats(ctx context.Context, cfg fleetdConfig) ([]seat, error) {
	var out struct {
		Seats []seat `json:"seats"`
	}
	if err := p.call(ctx, cfg, http.MethodGet, "/v1/admin/seats", nil, &out); err != nil {
		return nil, err
	}
	live := out.Seats[:0]
	for _, s := range out.Seats {
		if s.State != "retired" {
			live = append(live, s)
		}
	}
	return live, nil
}

type statusError struct {
	status  int
	message string
}

func (e *statusError) Error() string { return fmt.Sprintf("fleetd: HTTP %d: %s", e.status, e.message) }

func (p *fleetPlugin) call(ctx context.Context, cfg fleetdConfig, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, cfg.URL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.AdminToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := p.http
	if client == nil {
		client = &http.Client{Timeout: 12 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512<<10))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		return &statusError{status: resp.StatusCode, message: e.Error}
	}
	return json.Unmarshal(raw, out)
}

func reply(status int, body any) (*pluginsdk.PluginActionResponse, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return &pluginsdk.PluginActionResponse{
		Status:  status,
		Body:    raw,
		Headers: map[string]string{"Content-Type": "application/json", "Cache-Control": "no-store"},
	}, nil
}

// fleetdFailure passes a fleetd rejection (4xx) to the browser, which explains
// what the member asked for, and hides anything else behind upstream.
func fleetdFailure(err error) (*pluginsdk.PluginActionResponse, error) {
	var se *statusError
	if errors.As(err, &se) && se.status < http.StatusInternalServerError {
		return reply(se.status, map[string]string{"error": se.message})
	}
	return upstream(err)
}

// logger writes to stderr; stdout carries the plugin handshake.
var logger = slog.New(slog.NewJSONHandler(os.Stderr, nil))

// upstream logs a fleetd failure and reports it without passing its details
// to the browser.
func upstream(err error) (*pluginsdk.PluginActionResponse, error) {
	logger.Warn("fleetd call failed", "error", err)
	return reply(http.StatusBadGateway, map[string]string{"error": "fleetd is unavailable"})
}
