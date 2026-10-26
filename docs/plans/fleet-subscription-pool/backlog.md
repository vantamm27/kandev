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
| P0-05 | spike | S1: two macOS users on one Mac mini, each logged in to its own seat; Claude Code works over SSH (keychain vs `claude setup-token`). | in progress | 2026-10-27 on seat `seat_2be0ce995e932fa4` (Mac mini M4, user `tamvo`), reached over SSH from the Kandev container's key: login works; the non-interactive SSH `PATH` is `/usr/bin:/bin:/usr/sbin:/sbin` (no `/opt/homebrew/bin`); the login keychain cannot be read over SSH (`security ... -w` exits 36, "User interaction is not allowed"), so `claude -p` fails with "OAuth session expired and could not be refreshed" while the same command works in the GUI session. Verified recipe: the seat owner runs `claude setup-token` in their own session and exports `CLAUDE_CODE_OAUTH_TOKEN` plus the Homebrew `PATH` from `~/.zshenv` (mode 0600); afterwards `claude -p` over SSH from the Kandev key answers. `fleet-agent preflight` gained an `ssh-env` check for both. Second macOS user on the same Mac not tested yet. |
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
| F7 | agent | `/metrics` (Prometheus text): seats by state, quota %, queue depth by priority, dispatch wait histogram, handoffs by path. | done | Seat gauges + queue/dispatch metrics (`fleet_queue_tasks`, `fleet_queue_waiting_tasks{reason}`, `fleet_queue_exhausted_tasks`, `fleet_dispatch_launches_total`, `fleet_dispatch_failures_total`). A dispatch-wait histogram was replaced by per-reason waiting counts; handoff metrics come with F11. |
| P2-07 | infra | Ansible/MDM recipe for Mac mini: users per seat, sshd, sleep off, FileVault policy. | todo | Needs real Mac mini access. |
| DEP1 | ops | Deploy the new version to EC2 `10.8.0.238` (Kandev fork build + fleetd), after the implementation items. | in progress | Kit in `apps/fleet/deploy` (source-build Dockerfile over `ghcr.io/kdlbs/kandev:universal`, fleetd image, compose, runbook). Host: Amazon Linux 2023 x86_64, Kandev via compose at `/opt/kandev`, SQLite, auth and multi-tenancy on. 2026-10-26: images built on the host (`kandev-fleet:f3199109`, `fleetd:f3199109`); volume backed up to `~/kandev-backups/kandev-data-pre-fleet-20261026-172452.tgz`; compose switched from `kandev:pr2919` (backup `docker-compose.yml.bak-20261026-172452`); upstream migrations applied; `/health` ok with 0.98.0-fleet. fleetd running from `~/fleetd` (compose project `fleetd`, port 8484, reachable over the VPN) with a service PAT; `/healthz` ok, `/metrics` requires the metrics token; dispatcher off until `FLEET_WORKSPACE_ID`/`FLEET_INTAKE_STEP_ID` are set. `stats/usage-breakdown` answers 200 with a PAT. Rollback: restore the compose backup and the volume archive (upstream migrations ran). |
| P2-08 | infra | SSH CA (short-lived user certs, host certs) for Kandev -> node. | todo | Needs infra decision (Vault vs step-ca). |

## Phase 3: Quota-aware dispatcher and handoff

| ID | Kind | Item | Status | Notes |
| --- | --- | --- | --- | --- |
| F8 | agent | Pure scheduling package: effective priority (base + capped aging), per-owner fair share, quota gate (`remaining < est_cost` excludes), weekly pacing, head-of-line backfill. Table-driven tests only, no I/O. | done | `apps/fleet/internal/schedule`. `DefaultPolicy`: base critical 10000 / high 1000 / medium 300 / low 100; aging 2, 1, 0.5 per minute capped so low reaches medium and medium reaches high, nothing passes critical. Quota gate uses an estimated 10 % of 5 h and 2 % of the week until baseline data exists. Pacing applies to medium (10 % slack) and low (0 %) only and needs a known weekly reset. Every waiting task gets a reason (`no_seat`, `labels`, `seats_full`, `quota`, `weekly_pacing`). Fair share across owners is not implemented: with owner matching each owner only uses their own seats; revisit with P4-05. |
| F9 | agent | Dispatcher loop: reconcile every 15-30 s; candidate tasks = start_agent=false in the configured intake step with an assignee; claim in store (idempotent), launch via `session.launch` on the owner's seat executor profile; retry budget; audit log. | done | `internal/dispatch`, `internal/store/claims.go`, `kandev.Launcher` (redials WS). Polls every `FLEET_DISPATCH_INTERVAL` (20 s); WS `task.subscribe` push is not used yet (polling is enough at this scale). Candidates: intake step + assignee + no primary session. Claims: UPSERT only over failed claims; stale "launching" claims recovered after 5 min; backoff 1 min doubling, 3 attempts. Launches not yet seen by a heartbeat hold their slot. Audit = structured log per launch. Not yet run against a real Kandev (needs DEP1 or a local instance). |
| C3 | core | Expose `quota_limited` code and `reset_at` on the session state event (or a REST read) so fleetd can move a seat to cooldown. | blocked | Deferred until spike S2 shows which path a real Claude limit takes. Kandev classifies provider errors in several places: `persistLastAgentError` (`orchestrator/event_handlers_agent.go:3527`, session metadata `last_agent_error`, `Code` = adapter `FailureCode`, not the routingerr code), `persistRetainedTurnFailureMessage` (`event_handlers_turn_failure.go:280`, message metadata `failure_code` + `provider_error.reset_at`), and dynamic-route evidence (`dynamic_evidence.go`). F10 works without it: seat limits come from the node quota file, task state from Kandev. |
| F10 | agent | Limit handling: on `quota_limited` mark seat cooldown with reset time; requeue the task per D6 (dynamic profile handles short waits) or offer handoff. | done | `internal/limits`, `internal/store/handoffs.go`, run after each dispatch round. A launched task on a seat in `cooldown_week`, or `cooldown_5h` with reset unknown or more than 2 h away, gets one `offered` handoff unless Kandev says it is REVIEW/COMPLETED/CANCELLED or deleted. Offers are withdrawn when the seat recovers. After 3 completed handoffs the record is `exhausted` for a person. `GET /v1/admin/handoffs?state=...` lists them. |
| F11 | agent | Handoff (layer 2): offer list (online seats with quota, same team), `POST /v1/handoffs/:id/accept` by member B, set assignee, launch chain resume -> continuation task from `agent-wip/<task>` -> fresh run, max 3, record `handoff_path`. | done | `internal/handoff`, `POST /v1/admin/handoffs/{id}/accept` (admin-gated until U1 calls it for the signed-in member). Path implemented: `resume` (new session of the same task on B's executor with the checkpoint prompt from D9). Rejects same-owner, busy/offline seats, and taken offers; a failed launch restores the assignee and marks the handoff failed so it is offered again. `fleet_handoffs{state}` metric. Suggested-member list is left to U1 (it can read `/v1/admin/seats`). |
| F11b | agent | Handoff fallbacks: continuation task from `refs/agent-wip/<task>` (needs a Kandev create-task call with the original repository and workflow), then a fresh run from `main`. | todo | Only needed if spike S3 shows Kandev refuses a new session of the same task on another executor. |
| F12 | agent | Proactive checkpoint at >= 85 % quota: send `message.add` asking the agent to update PROGRESS.md and push WIP. | done | `internal/nudge`, `kandev.Conn.AddMessage`, migration 3 (`dispatch_claims.nudged_at`). Online/busy seats at >= 85 % 5 h or weekly usage; once per task per 5 h; done tasks skipped; a failed send is retried. The message tells the agent the exact `git push ... refs/agent-wip/<task>` command, so it does not depend on P3-04 hooks. |
| P3-04 | infra | Claude Code hooks (`Stop`, `PostToolUse`) auto-commit WIP; shared checkpoint prompt template (template text can live in `apps/fleet/templates`). | todo | Hook install is per node (managed settings). |
| C5 | core | Allow the fleet service account to dispatch on seat executors, if S4 shows it is blocked. | blocked | Waits on P0-08. |
| U1 | plugin | Fleet UI plugin: seats page, queue, handoff banner + "Take task" calling fleetd through an authenticated plugin webhook. | done | `apps/fleet/kandev-plugin` (own Go module, `replace` to the backend for `pluginsdk`). Authenticated actions `fleet.overview` and `fleet.handoff.accept`; the seat is chosen from Kandev's verified `actorID`, never the browser. Admin token stays in plugin config. `/fleet` page (plain `host.jsx`, no build step). Tests: Kandev's manifest parser, `plugin-pack` + `pkgtar.Install`, backend action tests; `node --check` on the bundle. Not yet rendered in a live Kandev (needs DEP1 or a local instance); UI strings are English only. A per-task banner slot is not done. |

## Phase 3b: First live end-to-end run (2026-10-27)

Goal: a task created with `start_agent=false` in the intake step is picked up by fleetd and finished on a seat without a human starting it. Test bed on 10.8.0.238: workspace `fleet-test` (`639e31ff-f376-4a28-8ad3-47f1020f6c9f`), intake step Backlog (`97725d76-b10d-46ce-9df9-2b3f49575939`), seats `seat_2be0ce995e932fa4` (Mac) and `seat_9eb75930014476f8` (Linux).

| ID | Kind | Item | Status | Notes |
| --- | --- | --- | --- | --- |
| E1 | agent | Send an agent profile with dispatched launches. | done | Kandev rejected `session.launch` with "task has no agent_profile_id configured". `FLEET_AGENT_PROFILE_ID`, else the workspace default (commit 498208df). |
| E2 | agent | Pin the host key Kandev actually negotiates. | done | fleet-agent pins the ED25519 fingerprint, but Kandev's Go SSH client negotiates another host key type (ECDSA first in x/crypto's default order), so every launch fails with "host key changed". Measure the key with the same x/crypto client config Kandev uses; fix the two registered seats. Done: `register` probes the local sshd with x/crypto v0.57.0 defaults (ECDSA was chosen on both seats); `fleet-agent host-key` prints it; both seats' Kandev executors repinned (commit 85963c44). |
| E3 | agent | Retry tasks whose launch failed inside Kandev. | done | A failed launch leaves the task `FAILED` with a `FAILED` primary session, and the candidate filter ("no primary session") then never retries it. Treat a task whose fleet claim failed and whose primary session is `FAILED` as a retry candidate, within the attempt budget. Done (85963c44); verified live: task 2 relaunched on the Linux seat. |
| E4 | ops | Opening an intake-step task in the web UI prepares a session on the default executor. | todo | Seen on the first test task: the prepared session failed and blocked dispatch. Document for users; evaluate a guard (I1 would remove it). |
| E5 | ops | Provider repositories need the workspace's GitHub/GitLab integration even for public repos. | todo | "GitHub is not configured for this workspace". The live test uses a task without a repository until the integration is set up. |
| E7 | agent | Make the retried session primary. | done | Kandev keeps the failed session as primary after a new `session.launch`; fleetd sends `session.set_primary` after a retry. |
| E8 | ops | Dispatched sessions must not bill an API key. | in progress | The workspace default Claude profile (`vantamm27`) sets `ANTHROPIC_API_KEY` and `ANTHROPIC_BASE_URL`, so the first live session ran through the gateway instead of the seat's subscription and stalled ("Still waiting for the agent"); it was stopped. fleetd now refuses a profile that sets `ANTHROPIC_API_KEY`/`ANTHROPIC_AUTH_TOKEN`/`ANTHROPIC_BASE_URL` or a provider API. Agent-profile writes reject PATs by design (settings interlock), so a person must create a subscription profile (claude-acp, no env vars) in the web UI and make it the workspace default or `FLEET_AGENT_PROFILE_ID`. |
| E6 | test | Live run: dispatch, launch over SSH, agent turn completes, task leaves the intake step, slot is released. | todo | On both seats. |

## Ideas

| ID | Kind | Item | Status | Notes |
| --- | --- | --- | --- | --- |
| I1 | plugin | Evaluate a Kandev executor provider (`executor_providers` in the plugin manifest) backed by fleetd, so Kandev asks fleetd for a seat at launch instead of fleetd launching onto fixed SSH executors. | todo | Seen in `cmd/plugin-fixture`; would remove the intake-step polling. Large change; only after the current design runs in production. |

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
- 2026-10-26: F9 done (dispatcher) and F7 completed with queue metrics. Fixed a test helper that hung on a second WS dial; tests now run with -timeout 5m.
- 2026-10-26: F10 done (handoff offers for limit-blocked tasks). C3 deferred to after S2 with the code paths recorded.
- 2026-10-26: F11 done (accept handoff, resume path). F11b split out pending S3.
- 2026-10-26: F12 done (checkpoint nudges).
- 2026-10-26: U1 done (Fleet UI plugin). All code-only backlog items are done; the rest need infra, spikes, or user decisions.
- 2026-10-26: DEP1 Kandev fork and fleetd deployed on 10.8.0.238; dispatcher off until workspace and intake step are chosen.
- 2026-10-27: S1 partial: keychain is unreadable over SSH; seats need `claude setup-token` and `~/.zshenv` (verified on seat 1; preflight `ssh-env` check added).
- 2026-10-27: second seat `seat_9eb75930014476f8` on Linux worker 10.0.28.229 (`ec2-user`, label `linux`), formerly Kandev executor `worker-1` on an API key (removed, backups kept). Needed: SG rule TCP 8484 from the worker to the Kandev host, token exported from `~/.bashrc`, systemd user unit with linger.
- 2026-10-27: first live dispatch runs: E1 fixed; E2-E5 found; continuing toward E6.
- 2026-10-27: E2, E3, E7 done; first session dispatched to the Linux seat. E8 found: the workspace's Claude profile bills an API key through the gateway; fleetd now refuses such profiles.
