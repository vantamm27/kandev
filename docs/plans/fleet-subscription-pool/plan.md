---
created: 2026-10-26
status: in-progress
owner: vantamm27
branch: feature/fleet
backlog: ./backlog.md
---

# Implementation Plan: Subscription-Seat Fleet for Kandev

## Overview

Run Claude Code tasks on a fleet of Mac mini nodes, managed centrally from
Kandev, using only Claude Team subscription seats (no API keys). The fleet must
scale from 6 to 20-50 seats, give every task a clear priority, keep running when
one seat hits its usage limit, and report usage to management and finance.

Visual version of this plan (Vietnamese, with diagrams):
<https://claude.ai/artifact/WFTeDDQxNKU7JxnmN1rc7b>

## Context

- Team: about 30 developers. Infra: AWS EKS, GitLab CE (self-managed),
  VictoriaMetrics + Grafana, OpenVPN, agentgateway (internal MCP gateway).
- Today: 6 Mac minis, each logged in to one Claude Team seat, running Claude
  Code through Kandev's SSH executor. Kandev is the controller (board,
  workflow, live session, review, external MCP).

## Decisions

| # | Decision | Reason |
| --- | --- | --- |
| D1 | Subscription seats only. No API-key nodes, no LLM gateway pool. | API pricing is far above seat pricing for agentic coding. |
| D2 | 1 seat = 1 owner = 1 macOS user. One Mac mini hosts 2-4 seats. | Keeps the "owner logs in to their own seat" rule while cutting hardware about 3x. |
| D3 | Each seat is one fixed Kandev SSH executor for its lifetime. Never remap a slot to another host. | Kandev pins `ssh_host_fingerprint` per executor and task environments remember the host for resume. Remapping breaks host-key checks and resume. |
| D4 | No central collection of seat OAuth tokens and no proxy in front of Anthropic. | Claude Code terms forbid brokering login tokens. Each owner logs in on the node. |
| D5 | Fleet is a Kandev plugin (`kandev-fleet`) plus a small `fleet-agent` binary on each node. Core changes are small, tested, and upstreamable. | Upstream moves fast; plugins already get events, execution API, KV, webhooks, actions, and UI routes. |
| D6 | Limit handling, layer 1: one Kandev dynamic agent profile per owner. Candidates are only that owner's accounts. `quota_limited` policy waits for a trusted reset up to 120 minutes, then moves to the next candidate or stops. | Kandev already classifies Claude session/weekly limits and supports reset waits. |
| D7 | Limit handling, layer 2: hand the task to another member only after that member clicks "Take task". No automatic cross-seat assignment until Anthropic confirms pooling in writing. | Policy boundary on seat pooling. |
| D8 | Handoff goal is a Done task, not session preservation. Try in order: resume the same task on the new executor, then a continuation task from `agent-wip/<task>`, then a fresh run from `main`. Max 3 handoffs per task. | Simplest path that always finishes. |
| D9 | Every launch (first run, retry, handoff) uses the same checkpoint prompt template. WIP is pushed by hooks, not by the model. | A restart then behaves like a resume from the last checkpoint and costs no extra quota. |
| D10 | Dispatcher matches `task.assignee_user_id == seat.owner`. Priority uses Kandev's `critical/high/medium/low` as P0-P3 with capped aging. | Existing task fields; no schema change. |

Rejected: TeamClaude-style OAuth proxy pools (terms), GitLab Runner instead of
Kandev (loses live session and review), remapping `slot-N` via `~/.ssh/config`
(breaks D3), API-key overflow (D1).

## Target architecture

```
Dev laptop (Claude Code, /delegate) --MCP--> agentgateway --> Kandev /mcp
Kandev UI ------------------------------------------------> Kandev (fork, Postgres)
                                                            |  events / session.launch
                                                            v
                                               kandev-fleet plugin
                                               (registry, quota tracker, dispatcher,
                                                handoff policy, Fleet UI)
                                                            ^ heartbeat (HMAC)
Mac mini N: macOS user seat-x (owner X) -- fleet-agent, Claude Code, worktrees
            Kandev --SSH (cert, pinned host key)--> seat-x
Claude Code --> Anthropic (direct, owner login)
Claude Code --> GitLab (refs/agent-wip/*, push -o ci.skip)
OTel (laptop + node) --> collector --> VictoriaMetrics / log store --> Grafana
```

## What Kandev already provides (verified in code, commit b6263ea4)

Paths are relative to `apps/backend/`.

- Start a task on a chosen executor: WS action `session.launch`
  (`internal/orchestrator/handlers/handlers.go:45`, request in
  `internal/orchestrator/session_launch.go`) accepts `task_id`, `intent`,
  `executor_id`, `executor_profile_id`, `agent_profile_id`, `priority`,
  `prompt`. `docs/WEBSOCKET_API.md:1626` still documents a stale
  `orchestrator.start`.
- Create with executor: MCP `create_task_kandev` and `POST /tasks` accept
  `executor_profile_id`, `start_agent`, `external_id`, `blocked_by`.
- Task fields: `Priority` (critical/high/medium/low,
  `internal/task/models/task_priority.go`), `AssigneeUserID` (human assignee,
  set via update; spec `docs/specs/tasks/system-design/human-assignee.md`),
  `Labels` (JSON array), `Metadata`.
- Executors: `POST/PATCH/DELETE /executors`, `/executors/:id/profiles`
  (`internal/task/handlers/executor_handlers.go:35`,
  `executor_profile_handlers.go:55`). `Config` is a string map. Executors and
  profiles carry `owner_user_id` (`internal/task/repository/sqlite/executor_ownership.go`).
- SSH reachability poller: default 60 s, range 15-3600 s,
  `executors.sshReachabilityIntervalSeconds`; event
  `executor.reachability.changed`.
- Concurrency ceilings: `KANDEV_MAX_CONCURRENT_SESSIONS`, per agent profile
  `max_concurrent_sessions`, workflow-step WIP limits.
- Event bus subjects: `task.created/updated/state_changed/moved/deleted`,
  `task_session.state_changed`, `session.usage_updated`. External clients can
  subscribe over `/ws`.
- Agent env injection: `buildEnvForExecution`
  (`internal/agent/runtime/lifecycle/manager_startup.go:517-590`) sets
  `KANDEV_TASK_ID`, `KANDEV_SESSION_ID`, and others at about line 557.
- Token usage per turn: `task_usage_events` (tokens in/out/cache, model, cost).
- Limit detection: `internal/agent/runtime/routingerr/rules.go:55-57` classifies
  Claude "session limit" and "weekly limit" as `quota_limited`;
  `resethint.go` parses the reset time (5 h limit only; weekly has no date).
- Dynamic agent profiles with ordered candidates, reset waits, retries, and
  fallback that keeps the task and worktree
  (`docs/decisions/2026-08-13-dynamic-agent-profile-routing.md`,
  `2026-08-17-provider-error-classes-and-policies.md`). Fixed profiles on
  Kanban stay manual recovery after a quota failure.
- Context continuation (`continue_from_history`): 64 KiB snapshot built from
  Kandev's SQL conversation (`docs/specs/agents/system-design/harness-session-continuity.md`).
  Only a user action authorizes it; queue dispatch never does.
- Workspace roles owner/collaborator/viewer and org units
  (`internal/authz/roles.go`). Workspaces are per-user by default.
- External MCP auth: PAT required unless auth is disabled
  (`internal/backendapp/helpers.go:2769`). Auth is off by default; enable with
  `KANDEV_FEATURES_AUTH`.
- Plugins: separate process; events, `api_read`/`api_write` (incl.
  execution), KV, secrets, webhooks, actions, UI routes
  (`docs/public/plugins-authoring.md`, `internal/plugins/manifest/manifest.go`).
  Plugins must not touch databases or `internal/...` packages.

## Phases

Each phase ends on a measurable exit criterion, not on a date. Dates are targets.

### Phase 0: Foundations and spikes (target 2026-10-27 to 2026-11-07)

Exit: one Mac mini runs two seats; Kandev launches tasks on each; written
answers for spikes S1-S5.

### Phase 1: Baseline telemetry (target 2026-11-10 to 2026-11-21)

Exit: every seat reports usage split by laptop and node; two weeks of baseline
data before any pacing threshold is set.

### Phase 2: Registry and fleet-agent (target 2026-11-24 to 2026-12-12)

Exit: a new seat is added in under 15 minutes without opening Kandev's executor
settings; draining a seat loses no task.

### Phase 3: Quota-aware dispatcher and handoff (target 2026-12-15 to 2027-01-16)

Exit: no task starts on a seat in cooldown; P0 waits under 5 minutes (p95) when
a seat has quota; cooldown hours per seat drop against baseline.

### Phase 4: Optimization and scale-out (from 2027-01-19)

Exit: average seat utilization rises against baseline; finance reads cost per
merged MR per team monthly.

The task-level backlog with status lives in [backlog.md](./backlog.md).

## Risks

- Unattended use terms: unclear whether the Team plan allows unattended Claude
  Code runs on shared machines at night. Ask sales before investing in
  time-shifting.
- Quota visibility: without a reliable remaining-quota signal, the quota gate
  only reacts after a limit is hit.
- Fork drift: rebase weekly, keep core patches small, upstream C1/C2/C4 early.
- Plugin limits: if KV/webhooks are not enough, split the plugin into a
  standalone service behind the same fleet-agent API.
- Mac mini reboots: FileVault and macOS updates stall nodes at the login
  screen. Needs MDM, `fdesetup authrestart`, and an offline alert.
- Owner laptop usage shares the seat quota with the node; pacing must sum both.

## Open questions

- Q1 (Anthropic sales): may member B's seat run member A's task when B
  explicitly accepts it? Is there an Enterprise option for org-level pooled
  usage? Is unattended night use on shared hardware allowed?
- Q2 (OpenAI terms): may a Codex/ChatGPT subscription be a dynamic-profile
  candidate on a shared node?
- Q3: does `KANDEV_MAX_CONCURRENT_SESSIONS` interact badly with the plugin's
  own admission? (Plan: plugin owns admission; ceiling = total slots + 2.)
- Q4: can a plugin service account launch on executors owned by other users
  (`owner_user_id`)? Spike S4.
- Q5: can one task start a new session on a different executor? Spike S3.
  Not blocking (D8).

## Resume guide

1. `cd /Volumes/tamvo/workspaces/tamvo/kandev-vantamm27 && git checkout feature/fleet`
2. Go 1.26.0 is not installed system-wide. A copy lives in the session
   scratchpad; if it is gone, install with `brew install go` or download
   `https://go.dev/dl/go1.26.0.darwin-arm64.tar.gz` and put its `bin` on `PATH`.
3. Open [backlog.md](./backlog.md), pick the first item marked `in-progress`
   or the first `todo` in the current phase, and read its "Notes" line.
4. Backend checks: `make -C apps/backend test` (or targeted
   `go test -trimpath ./internal/<pkg>/...`), and
   `bash scripts/lint-go-changed <changed .go files>` from the repo root.
5. Commits: Conventional Commits, header at most 100 characters; run `gofmt`
   on staged Go files first.
