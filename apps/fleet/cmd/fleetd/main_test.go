package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kandev/kandev/apps/fleet/internal/config"
	"github.com/kandev/kandev/apps/fleet/internal/kandev"
	"github.com/kandev/kandev/apps/fleet/internal/store"
)

func TestTokenCreatePrintsUsableToken(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fleet.db")
	t.Setenv("FLEET_DB_PATH", dbPath)
	var out bytes.Buffer
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := run([]string{"token", "create", "--owner", "user-a", "--labels", "team:core, macos", "--ttl", "1h"}, &out, log); err != nil {
		t.Fatalf("run: %v", err)
	}
	raw := strings.TrimSpace(out.String())
	if !strings.HasPrefix(raw, "fleet_reg_") {
		t.Fatalf("output = %q", raw)
	}
	st, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = st.Close() }()
	tok, err := st.LookupToken(context.Background(), raw, time.Now())
	if err != nil || tok.OwnerUserID != "user-a" || strings.Join(tok.Labels, ",") != "team:core,macos" {
		t.Fatalf("token = %+v, %v", tok, err)
	}
	if _, err := st.LookupToken(context.Background(), raw, time.Now().Add(2*time.Hour)); err == nil {
		t.Fatal("token must expire after --ttl")
	}
}

func TestCommandErrors(t *testing.T) {
	t.Setenv("FLEET_DB_PATH", filepath.Join(t.TempDir(), "fleet.db"))
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	for name, args := range map[string][]string{
		"missing owner":        {"token", "create"},
		"bad ttl":              {"token", "create", "--owner", "u", "--ttl", "-1h"},
		"unknown command":      {"seats"},
		"serve without kandev": {"serve"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := run(args, io.Discard, log); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

type fakeWorkspaces struct {
	ws      kandev.Workspace
	err     error
	profile kandev.AgentProfile
}

func (f fakeWorkspaces) GetAgentProfile(_ context.Context, id string) (kandev.AgentProfile, error) {
	p := f.profile
	p.ID = id
	return p, nil
}

func (f fakeWorkspaces) GetWorkspace(context.Context, string) (kandev.Workspace, error) {
	return f.ws, f.err
}

func TestResolveAgentProfile(t *testing.T) {
	ctx := context.Background()
	cfg := config.Config{WorkspaceID: "ws-1"}
	if got, err := resolveAgentProfile(ctx, cfg, fakeWorkspaces{ws: kandev.Workspace{DefaultAgentProfileID: "ws-default"}}); err != nil || got != "ws-default" {
		t.Fatalf("workspace default = %q, %v", got, err)
	}
	cfg.AgentProfileID = "explicit"
	if got, err := resolveAgentProfile(ctx, cfg, fakeWorkspaces{err: errors.New("unreachable")}); err != nil || got != "explicit" {
		t.Fatalf("explicit setting must win without a lookup: %q, %v", got, err)
	}
	cfg.AgentProfileID = ""
	if _, err := resolveAgentProfile(ctx, cfg, fakeWorkspaces{}); err == nil {
		t.Fatal("a workspace without a default agent profile must be an error")
	}
	if _, err := resolveAgentProfile(ctx, cfg, fakeWorkspaces{err: errors.New("boom")}); err == nil {
		t.Fatal("a failed lookup must be an error")
	}
}

func TestResolveAgentProfileRejectsAPIBilledProfiles(t *testing.T) {
	ctx := context.Background()
	cfg := config.Config{WorkspaceID: "ws-1", AgentProfileID: "p-1"}
	for name, p := range map[string]kandev.AgentProfile{
		"api key env":     {EnvVars: []kandev.ProfileEnvVar{{Key: "ANTHROPIC_API_KEY"}}},
		"auth token env":  {EnvVars: []kandev.ProfileEnvVar{{Key: "ANTHROPIC_AUTH_TOKEN"}}},
		"base url env":    {EnvVars: []kandev.ProfileEnvVar{{Key: "ANTHROPIC_BASE_URL"}}},
		"provider url":    {ProviderBaseURL: "https://gateway"},
		"provider secret": {ProviderAPIKeySecretID: "secret-1"},
	} {
		if _, err := resolveAgentProfile(ctx, cfg, fakeWorkspaces{profile: p}); err == nil {
			t.Errorf("%s: a profile billed to an API key must be refused", name)
		}
	}
	ok := kandev.AgentProfile{EnvVars: []kandev.ProfileEnvVar{{Key: "CLAUDE_CODE_MAX_OUTPUT_TOKENS"}}}
	if got, err := resolveAgentProfile(ctx, cfg, fakeWorkspaces{profile: ok}); err != nil || got != "p-1" {
		t.Fatalf("subscription profile = %q, %v", got, err)
	}
}

type fakeSteps map[string]kandev.WorkflowStep

func (f fakeSteps) GetWorkflowStep(_ context.Context, id string) (kandev.WorkflowStep, error) {
	s, ok := f[id]
	if !ok {
		return kandev.WorkflowStep{}, &kandev.APIError{Status: 404, Message: "Step not found"}
	}
	return s, nil
}

func TestIntakeStepWarnings(t *testing.T) {
	auto := kandev.WorkflowStep{ID: "step-auto", Name: "In Progress",
		Events: kandev.StepEvents{OnEnter: []kandev.StepAction{{Type: kandev.OnEnterAutoStartAgent}}}}
	steps := fakeSteps{"step-ok": {ID: "step-ok", Name: "Backlog"}, "step-auto": auto}
	got := intakeStepWarnings(context.Background(), steps, []string{"step-ok", "step-auto", "step-gone"})
	if len(got) != 2 || !strings.Contains(got[0], "step-auto") || !strings.Contains(got[0], "auto_start_agent") || !strings.Contains(got[1], "step-gone") {
		t.Fatalf("warnings = %q", got)
	}
}
