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

Fleet service lives in `apps/fleet` (module `github.com/kandev/kandev/fleet`):
`cmd/fleetd` (service), `cmd/fleet-agent` (node), `internal/...` packages.

| ID | Kind | Item | Status | Notes |
| --- | --- | --- | --- | --- |
| F1 | agent | Scaffold `apps/fleet`: go.mod, `cmd/fleetd` with config (env), HTTP server with `/healthz`, SQLite store with migrations (`seats`, `registration_tokens`, `dispatch_claims`, `handoffs`), Makefile target, README. | done | `make test vet build` pass with `-race`; golangci-lint (backend config) 0 issues; smoke run: `/healthz` 200, DB created `0600`, clean SIGTERM shutdown. CI: `.github/workflows/fleet-tests.yml` (push to `feature/fleet`/main, PRs touching `apps/fleet`). |
| F2 | agent | Kandev client package: REST (create/patch/delete executor + profile, list/get tasks, update task assignee) and WS (`session.launch`, `task.subscribe`) with PAT auth; httptest-based client tests that pin request shapes. | done | `apps/fleet/internal/kandev`. WS auth uses `Authorization: Bearer` (the global auth middleware resolves PATs before `/ws`; `?token=` also works). Notifications are a bounded channel that drops the oldest, so callers must reconcile from REST. Shapes are copied from backend handlers; there is no cross-module contract test, so re-check them when rebasing upstream. Tests pass 20x under `-race`. |
| F3 | agent | Registration: `fleetd token create --owner <user> --labels --ttl` (one-time, SHA-256 hashed at rest); `POST /v1/register` creates the Kandev SSH executor (pinned `ssh_host_fingerprint`) + profile, then consumes the token and stores the seat in one transaction; returns a per-seat HMAC secret once. | done | `apps/fleet/internal/registry`, `internal/store/{tokens,seats}.go`, `internal/server/register.go`. A failed final step deletes the created executor, so the token stays usable. `team:`/`owner:` labels are admin-only (token), never node-claimed. Empty `prepare_script` makes Kandev use its default SSH prepare script. E2E smoke against a fake Kandev: 201, token reuse 401. Executor `owner_user_id` = seat owner; whether the service PAT may create executors for another user is spike S4. |
| F4 | agent | Heartbeat: `POST /v1/heartbeat` (HMAC + timestamp window), payload = limit (`""`/`5h`/`week`), quota 5h/weekly %, reset times, free slots, agent version. Seat state machine with an offline sweeper. | done | `internal/hmacauth` (shared with fleet-agent), `internal/seatstate` (pure `Next`), `internal/store/heartbeats.go`, `internal/server/heartbeat.go`, `internal/sweeper`. Replays rejected by a per-seat monotonic signed timestamp (409). Free slots capped at capacity. The node reports `limit`; fleetd does not infer it from %. Migration 2 adds `limit_reason`, `last_signed_at`. |
| F5 | agent | `fleet-agent` binary: `register` (writes config + LaunchAgent plist for the seat's macOS user), `run` (heartbeat loop), `preflight` (claude login, sshd, disk, sleep off), `checkpoint <worktree>` (commit + push `refs/agent-wip/<task>` without the model). | done | `apps/fleet/cmd/fleet-agent`, `internal/agent`. Heartbeat reporter tested against the real fleetd handler + store. Quota comes from a JSON file a Claude Code hook/statusline writes (format in `apps/fleet/README.md`); filling it reliably is spike S2. Slots = capacity minus this user's `agentctl` processes. `preflight` passes on the dev Mac. `checkpoint` tested against a bare remote with push options. The preflight `claude` check only proves the binary exists, not that the seat is logged in (S1). |
| F6 | agent | Admin endpoints: list seats, drain, undrain, retire (deletes Kandev executor after drain), rotate seat secret. | done | `internal/admin`, `internal/server/admin.go`, `FLEET_ADMIN_TOKEN` (empty disables). Retire needs draining + all slots free unless `?force=true`; a Kandev 404 still retires, other Kandev errors leave the seat unchanged (502). Endpoint uniqueness is now a partial index over non-retired seats (migration 1 revised before first deploy), so a retired endpoint can be re-registered. |
| F7 | agent | `/metrics` (Prometheus text): seats by state, quota %, queue depth by priority, dispatch wait histogram, handoffs by path. | in-progress | Seat gauges done (`internal/metrics`, optional `FLEET_METRICS_TOKEN`): `fleet_seats{state}`, per-seat info/capacity/free slots/quota/limit reset/heartbeat age; retired seats get no per-seat series. Queue, dispatch-wait, and handoff metrics land with F9/F11. |
| P2-07 | infra | Ansible/MDM recipe for Mac mini: users per seat, sshd, sleep off, FileVault policy. | todo | Needs real Mac mini access. |
| DEP1 | ops | Deploy the new version to EC2 `10.8.0.238` (Kandev fork build + fleetd), after the implementation items. | todo | Needs from the user: SSH access to the host, how Kandev runs there today (binary/systemd, Docker, k8s), DB (SQLite or Postgres), whether fleetd runs on the same host, and a maintenance window. Plan: back up the DB, build from `feature/fleet`, deploy, smoke-test `/healthz`, MCP `create_task_kandev` with priority, and `stats/usage-breakdown`, keep the old binary for rollback. |
| P2-08 | infra | SSH CA (short-lived user certs, host certs) for Kandev -> node. | todo | Needs infra decision (Vault vs step-ca). |

## Phase 3: Quota-aware dispatcher and handoff

| ID | Kind | Item | Status | Notes |
| --- | --- | --- | --- | --- |
| F8 | agent | Pure scheduling package: effective priority (base + capped aging), per-owner fair share, quota gate (`remaining < est_cost` excludes), weekly pacing, head-of-line backfill. Table-driven tests only, no I/O. | done | `apps/fleet/internal/schedule`. `DefaultPolicy`: base critical 10000 / high 1000 / medium 300 / low 100; aging 2, 1, 0.5 per minute capped so low reaches medium and medium reaches high, nothing passes critical. Quota gate uses an estimated 10 % of 5 h and 2 % of the week until baseline data exists. Pacing applies to medium (10 % slack) and low (0 %) only and needs a known weekly reset. Every waiting task gets a reason (`no_seat`, `labels`, `seats_full`, `quota`, `weekly_pacing`). Fair share across owners is not implemented: with owner matching each owner only uses their own seats; revisit with P4-05. |
| F9 | agent | Dispatcher loop: reconcile every 15-30 s plus `task.subscribe` events; candidate tasks = start_agent=false in the configured intake step with an assignee; claim in store (idempotent), launch via `session.launch` on the owner's seat executor profile; retry budget; audit log. | todo | Covers old P3-01, P3-02. |
| C3 | core | Expose `quota_limited` code and `reset_at` on the session state event (or a REST read) so fleetd can move a seat to cooldown. | todo | Classification exists in `routingerr`; find where the session failure reason is persisted. |
| F10 | agent | Limit handling: on `quota_limited` mark seat cooldown with reset time; requeue the task per D6 (dynamic profile handles short waits) or offer handoff. | todo | Depends on C3. |
| F11 | agent | Handoff (layer 2): offer list (online seats with quota, same team), `POST /v1/handoffs/:id/accept` by member B, set assignee, launch chain resume -> continuation task from `agent-wip/<task>` -> fresh run, max 3, record `handoff_path`. | todo | Plan D7, D8. Covers old P3-03. |
| F12 | agent | Proactive checkpoint at >= 85 % quota: send `message.add` asking the agent to update PROGRESS.md and push WIP. | todo | Covers old P3-05. |
| P3-04 | infra | Claude Code hooks (`Stop`, `PostToolUse`) auto-commit WIP; shared checkpoint prompt template (template text can live in `apps/fleet/templates`). | todo | Hook install is per node (managed settings). |
| C5 | core | Allow the fleet service account to dispatch on seat executors, if S4 shows it is blocked. | blocked | Waits on P0-08. |
| U1 | plugin | Fleet UI plugin: seats page, queue, handoff banner + "Take task" calling fleetd through an authenticated plugin webhook. | todo | After F6/F11. |

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
- 2026-10-26: D5 revised (standalone fleet service instead of plugin, see plan).
  Phase 2-3 broken down into F1-F12 and U1.
- 2026-10-26: F1 done (fleet module scaffold + CI workflow).
- 2026-10-26: F2 done (Kandev REST + WS client).
- 2026-10-26: F3 done (seat registration). User asked to deploy to EC2 10.8.0.238 after implementation (DEP1).
- 2026-10-26: F4 done (signed heartbeats, seat states, offline sweeper).
- 2026-10-26: F5a done (fleet-agent register + LaunchAgent).
- 2026-10-26: F5 done (fleet-agent run/preflight/checkpoint).
- 2026-10-26: F6 done (seat admin API).
- 2026-10-26: F7 seat metrics done; queue/dispatch/handoff metrics deferred to F9/F11.
- 2026-10-26: F8 done (pure scheduler).
