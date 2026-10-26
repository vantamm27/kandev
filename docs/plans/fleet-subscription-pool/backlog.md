---
created: 2026-10-26
updated: 2026-10-26
plan: ./plan.md
---

# Fleet Backlog

Status values: `todo`, `in-progress`, `blocked`, `done`, `dropped`.
Kinds: `core` (Kandev fork patch), `plugin`, `agent` (fleet-agent binary),
`infra`, `spike`, `ops`.
Update the status and the Notes line whenever work stops, so the next session
can resume from this file alone.

## Phase 0: Foundations and spikes

| ID | Kind | Item | Status | Notes |
| --- | --- | --- | --- | --- |
| P0-01 | infra | Move Kandev to Postgres (RDS), enable PITR backups, document restore RTO. | todo | SQLite backups do not cover Postgres; use `pg_dump`/PITR. |
| P0-02 | infra | Enable auth (`KANDEV_FEATURES_AUTH`), create admin, create a service user and PAT for the fleet plugin/dispatcher. | todo | External `/mcp` is open when auth is disabled. |
| P0-03 | infra | Put agentgateway in front of Kandev `/mcp`: JWT auth, tool allowlist (agents never get `*_executor_profile_*`, `*_mcp_config_*`). | todo | |
| P0-04 | infra | Create a shared team workspace; grant members the collaborator role through an org unit. | todo | Required for layer-2 handoff; workspaces are per-user by default. |
| P0-05 | spike | S1: two macOS users on one Mac mini, each logged in to its own seat; Claude Code works over SSH (keychain vs `claude setup-token`). | todo | Decides the node provisioning recipe. |
| P0-06 | spike | S2: reliable remaining-quota signal (statusline JSON, `/usage`, error text). | todo | Without it the quota gate is reactive only. |
| P0-07 | spike | S3: launch a new session of an existing task on a different executor via `session.launch`. | todo | Not blocking; picks handoff step 1 vs step 2 (plan D8). |
| P0-08 | spike | S4: can a service user launch on executors whose `owner_user_id` is another user? | todo | If not, core patch C5. |
| P0-09 | spike | S5: what happens to running agent sessions when the Kandev backend restarts. | todo | Decides HA needs. |
| P0-10 | spike | S6: create dynamic profile `claude-<owner>` with a 120-minute reset wait; trigger a limit and observe resume. | todo | Validates plan D6. |
| P0-11 | ops | Send questions Q1 to Anthropic sales. | todo | See plan "Open questions". |

## Phase 1: Baseline telemetry

| ID | Kind | Item | Status | Notes |
| --- | --- | --- | --- | --- |
| C1 | core | MCP `create_task_kandev` accepts `priority` and `assignee_user_id` (alias `me` = caller). Service `CreateTaskRequest.AssigneeUserID` validated like an update. | done | Commit `1770e866` on `feature/fleet`. Files: `task/service/service_requests.go`, `service_tasks.go`, `mcp/handlers/{create_task_mode.go,handlers.go,create_task_assignee.go}`, `mcp/server/{server.go,handlers.go}`, tests `*create_task_assignee_test.go`, schema pin in `mcp/server/handlers_test.go`, docs `docs/public/automation-and-mcp.md`. MCP tests and lint pass. |
| C2 | core | Inject `OTEL_RESOURCE_ATTRIBUTES` per launch (`kandev.workspace_id`, `kandev.task_id`, `kandev.session_id`, `kandev.agent_profile_id`, `kandev.execution_profile_id`, `kandev.executor_type`), appended to any existing value. | done | Commit `170e3b17`. `agent/runtime/lifecycle/otel_resource_env.go` (+test), applied in all three branches of `buildEnvForExecution` (`manager_startup.go`), and added to the SSH remote env allowlist (`executor_ssh_operations.go`), because SSH forwards only allowlisted keys. Executor ID and assignee are not on `LaunchRequest`; seat identity comes from Claude Code's own account attributes, task-to-assignee joins via C4. Docs: `docs/public/agents-and-profiles.md`. |
| C4 | core | Export token usage grouped by day, executor, assignee, model: `GET /api/v1/workspaces/:id/stats/usage-breakdown`. | done | `analytics/repository/sqlite/usage_breakdown.go` (+SQLite test, env-gated Postgres parity test skipped locally), handler/DTO in `analytics/handlers`, `analytics/dto`. Authz covered by the existing all-stats-routes test. Docs in `agents-and-profiles.md`. A `/metrics` scrape endpoint was not added; Grafana can read this JSON (Infinity datasource) or a small exporter can poll it. |
| C6 | core | Fix `docs/WEBSOCKET_API.md`: document `session.launch`; mark `orchestrator.trigger/start/prompt/complete` retired (use `session.launch`, `message.add`, `task.state`/`task.move`). | done | Docs only; no test pins this file. Other parts of the file (`board.*`, `column.*`, `acp.*`, `agent.launch`) may also be stale; not verified. |
| P1-01 | infra | Managed settings for OTel on laptops and nodes (`source`, `team`, `seat`); prompt logging off. | todo | |
| P1-02 | infra | OTel collector: drop `session.id` from metrics; route events to the log store. | todo | Cardinality. |
| P1-03 | infra | Grafana dashboards: Seat (laptop vs node), Queue, Token per merged MR. | todo | |

## Phase 2: Registry and fleet-agent

| ID | Kind | Item | Status | Notes |
| --- | --- | --- | --- | --- |
| P2-01 | plugin | Scaffold `kandev-fleet` plugin (manifest, register in `plugin-registry/plugins.yaml`). | todo | Follow `docs/public/plugins-authoring.md`. |
| P2-02 | plugin | Registry action `register`: one-time token, host key from node, create executor (`ssh_*`, `ssh_host_fingerprint`) and profile, return per-seat HMAC secret. | todo | Executor never moves to another host (D3). |
| P2-03 | plugin | Heartbeat webhook (HMAC, 30 s): state, 5 h/weekly quota, reset time, free slots, agent version. | todo | |
| P2-04 | plugin | Fleet UI (read-only first): seats, state, quota, running tasks. | todo | |
| P2-05 | agent | `fleet-agent register --token --seat --labels`; installs a LaunchAgent for the seat's macOS user; preflight checks. | todo | |
| P2-06 | agent | `fleet-agent checkpoint`: commit and push the worktree to `refs/agent-wip/<task>` without the model. | todo | Used when quota is exhausted. |
| P2-07 | infra | Ansible/MDM recipe for Mac mini: users per seat, sshd, sleep off, FileVault policy. | todo | |
| P2-08 | infra | SSH CA (short-lived user certs, host certs) for Kandev -> node. | todo | |
| P2-09 | plugin | Drain / retire / rotate-token actions. | todo | |

## Phase 3: Quota-aware dispatcher and handoff

| ID | Kind | Item | Status | Notes |
| --- | --- | --- | --- | --- |
| C3 | core | Expose `quota_limited` code and `reset_at` to plugins (event payload or API). | todo | Classification already exists in `routingerr`. |
| C5 | core | Allow the fleet service account to dispatch on seat executors, if S4 shows it is blocked. | blocked | Waits on P0-08. |
| P3-01 | plugin | Dispatcher: owner match, priority + capped aging, quota gate, weekly pacing, launch via `session.launch`. | todo | Plan D10. |
| P3-02 | plugin | Retry budget, failed inbox, audit log. | todo | |
| P3-03 | plugin | Layer-2 handoff: banner, member suggestions, "Take task" action, launch on member's executor, chain resume -> continuation task -> fresh run, max 3 handoffs, record `handoff_path`. | todo | Plan D7, D8. |
| P3-04 | infra | Claude Code hooks (`Stop`, `PostToolUse`) auto-commit WIP; shared checkpoint prompt template. | todo | Plan D9. |
| P3-05 | plugin | Proactive checkpoint at >= 85% quota via `message_task_kandev`. | todo | |

## Phase 4: Optimization and scale-out

| ID | Kind | Item | Status | Notes |
| --- | --- | --- | --- | --- |
| P4-01 | plugin | Time-shift P2/P3 to off-hours; align the 5 h window to the workday. | todo | Only after Q1 answers unattended use. |
| P4-02 | infra | Model mix per agent profile (Sonnet default, Opus for planning, light subagent model). | todo | |
| P4-03 | infra | Finance dashboard: seat utilization, cost per merged MR per team, seat tier recommendations. | todo | |
| P4-04 | ops | Scale to about 10 Mac minis (2-4 seats each). | todo | |
| P4-05 | plugin | Feature flag to drop the owner constraint if Anthropic confirms pooling. | todo | Default off. |

## Log

- 2026-10-26: Architecture review, subscription-only decision, plugin approach,
  limit-handling layers decided. Branch `feature/fleet` created. C1 started.
- 2026-10-26: C1 implemented and tested. `go test ./internal/task/service/` has 37
  pre-existing failures on this Mac (worktree/archive/gitflow tests); the same 37
  fail on unmodified `main`, so they are environment-related, not caused by C1.
- 2026-10-26: C2 implemented and tested. `internal/agent/runtime/lifecycle` has
  pre-existing failures on this Mac (worktree/kubernetes/recovery tests,
  `TestBuildAuthMethodsIdentityAgentOverridesEnvironment`) and a flaky
  `TestSSHPassthroughResetReplacesChangedWorkspaceMCP` (fails about 1 in 10 on
  unmodified `main`); none are caused by C2.
- 2026-10-26: Pushed `feature/fleet` to github.com/vantamm27/kandev (commits
  9f642e47, 1770e866, 170e3b17). Next: C6 (docs fix), then Phase 0 spikes.
- 2026-10-26: The original checkout on /Volumes became unreadable (macOS
  "Operation not permitted"). Work continues in a fresh clone in the session
  scratchpad, pushed to the same `feature/fleet` branch; `git pull` the original
  checkout once access returns. C6 done.
- 2026-10-26: C4 done (usage-breakdown endpoint).
