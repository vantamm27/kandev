# Fleet

Registry and dispatcher for running Kandev tasks on a pool of Claude Code
subscription seats (one seat = one owner = one macOS user on a node). See
[the plan](../../docs/plans/fleet-subscription-pool/plan.md) and
[backlog](../../docs/plans/fleet-subscription-pool/backlog.md).

This is its own Go module so it builds and tests without the backend.

- `cmd/fleetd`: the service (registry API, heartbeats, dispatcher).
- `cmd/fleet-agent`: the node agent, run as each seat's macOS user.

## Register a seat (on the node, as the seat user)

Kandev runs the agent over non-interactive SSH, where macOS does not let
Claude Code read the login keychain. The seat owner therefore creates a
long-lived subscription token in their own session and exports it, with the
Homebrew `PATH`, from the shell startup file that SSH sessions read:

```sh
claude setup-token        # prints the token; keep it out of shared terminals
umask 077
cat >> ~/.zshenv <<'EOF'
export PATH="/opt/homebrew/bin:$PATH"
export CLAUDE_CODE_OAUTH_TOKEN="<token>"
EOF
fleet-agent preflight     # the ssh-env check confirms both
```

Kandev's SSH key must also be in the seat user's `~/.ssh/authorized_keys`,
preferably restricted with `from="<kandev host>"`.

On a Linux seat Claude Code keeps its login in a file, so the keychain step
does not apply; use `--systemd-unit ~/.config/systemd/user/fleet-agent.service`
instead of `--launch-agent`, and `sudo loginctl enable-linger <user>` so the
unit runs without a login session.
From another account (for example `ssm-user` in an SSM session), manage the
seat user's unit with `sudo systemctl --user -M <user>@ enable --now fleet-agent.service`.
Over SSH, bash runs Kandev's commands as a non-interactive non-login shell,
which reads `~/.bashrc` but not `~/.bash_profile`; an exported token belongs
at the top of `~/.bashrc`, before any early return for non-interactive shells.


```sh
FLEET_REGISTRATION_TOKEN=fleet_reg_... fleet-agent register \
  --fleetd http://fleetd.internal:8484 --ssh-host 10.0.0.5 \
  --labels macos --capacity 1 \
  --launch-agent ~/Library/LaunchAgents/com.kandev.fleet-agent.plist
```

The host key fingerprint is read from `/etc/ssh/ssh_host_ed25519_key.pub`
(`--host-key` to change) and matches `ssh-keygen -lf`. The registration is
saved to `~/.config/fleet-agent/config.json` with `0600` permissions; the
seat's HMAC secret is never printed.

## Run

```sh
make build
FLEET_DB_PATH=/var/lib/fleet/fleet.db \
FLEET_KANDEV_URL=http://kandev:38429 \
FLEET_KANDEV_TOKEN=kandev_pat_... \
./bin/fleetd
```

| Variable | Default | Meaning |
| --- | --- | --- |
| `FLEET_LISTEN_ADDR` | `:8484` | HTTP listen address. |
| `FLEET_DB_PATH` | `fleet.db` | SQLite file, created with `0600` permissions. |
| `FLEET_KANDEV_URL` | empty | Kandev base URL. |
| `FLEET_KANDEV_TOKEN` | empty | Personal access token of the fleet service user. |
| `FLEET_HEARTBEAT_TIMEOUT` | `90s` | A seat without a heartbeat for this long is offline. |
| `FLEET_SSH_IDENTITY_SOURCE` | empty | How Kandev authenticates to seats: `agent` or `file`. |
| `FLEET_SSH_IDENTITY_FILE` | empty | Private key path on the Kandev host when the source is `file`. |
| `FLEET_ADMIN_TOKEN` | empty | Bearer token for `/v1/admin`; empty disables the admin API. |
| `FLEET_METRICS_TOKEN` | empty | Bearer token required on `/metrics`; empty leaves it open (keep fleetd inside the VPN). |
| `FLEET_WORKSPACE_ID` | empty | Kandev workspace the dispatcher reads tasks from. |
| `FLEET_INTAKE_STEP_ID` | empty | Workflow steps where tasks wait for a seat, comma-separated (one per workflow); with this or the workspace empty, the dispatcher does not run. |
| `FLEET_AGENT_PROFILE_ID` | empty | Kandev agent profile dispatched sessions run; empty uses the workspace's default agent profile. |
| `FLEET_DISPATCH_INTERVAL` | `20s` | Time between dispatch rounds. |
| `FLEET_REVIEW_GRACE` | `5m` | How long a task stays in `REVIEW` before fleetd may stop its session to free the seat for a queued task. |

## Dispatching

Each round fleetd lists the workspace's tasks and picks those waiting for a
seat:

- tasks in an intake step,
- tasks created with the fleet agent profile (`FLEET_AGENT_PROFILE_ID`) and not
  started, in any step. In the web UI: pick that profile, choose **Create
  without starting agent**, then **Assign to me** in the task's top bar, and
- released tasks (see [Custom workflows](#custom-workflows)) whose workflow
  went on, pinned to the seat that holds their environment, and
- tasks whose launch or session failed (see [Stops and retries](#stops-and-retries)).

A task waits only if it has no session, or its only session is fleetd's own
failed launch, or a session that never sent a prompt. The web UI prepares such
a session on Kandev's own host when a task is created or opened; fleetd stops
it and resets the task environment before launching on a seat. A fleet task
without an assignee waits with reason `no_assignee`, since Kandev does not
record who created a task and fleetd only runs work on its owner's seats. The
Kandev plugin makes the member who creates a task in the fleet workspace its
assignee (see [the plugin](kandev-plugin/README.md)).
Create tasks through the API with `start_agent=false` (for example
`create_task_kandev` with `assignee_user_id: "me"`), otherwise Kandev starts
them on its default executor. The schedule package orders them (priority plus capped aging) and
assigns each to one of the assignee's own seats that is online with a free
slot, has the required labels, enough quota, and room in its weekly pacing.
fleetd claims the task, then sends `session.launch` with the seat's executor
profile. A failed launch is retried with backoff (1 min, doubling) up to 3
attempts; a claim left "launching" by a crash is retried after 5 minutes.
Launches the seat's heartbeat has not reported yet still hold their slot.

After each round fleetd also:

- **Nudges** running tasks on a seat whose reported 5-hour or weekly usage is
  at least 85 %: a `message.add` asks the agent to update PROGRESS.md, commit,
  and push to `refs/agent-wip/<task>`. At most once per task per 5 hours.
- **Offers handoffs** for tasks that still need a seat at its weekly limit, or
  at its 5-hour limit with the reset unknown or more than 2 hours away:
  launched tasks that are not done, tasks paused at the limit, and tasks whose
  session failed there. A task in `REVIEW` for a person is not offered.
  Shorter 5-hour waits end with the automatic resume below. Accept with
  `POST /v1/admin/handoffs/{id}/accept`.

Waiting reasons are `no_assignee`, `unknown_priority`, `no_seat`, `labels`,
`quota_cooldown` (the owner's seats wait for their usage limit to reset),
`seats_full` (they are busy), `quota` (less left than a task's estimated
cost), and `weekly_pacing`.

## Stops and retries

When a launched task changes state, fleetd decides what the stop means:

- **Usage limit.** A task that reaches `REVIEW` with the seat's usage-limit
  report ("You've hit your session limit", "usage limit reached", and the
  like) among its session's newest agent messages is `paused`. Its session
  keeps the seat. Once the seat's heartbeat shows the limit reset (the seat is
  online again), fleetd sends the session `message.add` with a request to
  continue where it stopped, in the same conversation. A paused task that a
  person continues is no longer paused.
- **Session failure.** A task that becomes `FAILED`, or reaches `REVIEW` with
  its session `FAILED`, goes back to the dispatcher on the same seat, with the
  same backoff and attempt budget as a failed launch (3 attempts). The new
  session runs the task's current workflow step in the same task directory,
  but starts a fresh conversation. After the last attempt the task stays
  `exhausted` until a person acts.
- A task that is `COMPLETED`, `CANCELLED`, or deleted is done: fleetd stops
  its session.

## Custom workflows

Kandev sets a task to `REVIEW` after every agent turn, not only at the end of
its workflow, and a move to another step keeps the task's live session. fleetd
therefore keeps a session through `REVIEW`, so a person can reply, or move the
task to the next agent step, and the work goes on in the same session on the
same seat.

A session in `REVIEW` still holds its seat. When a queued task of the same
owner waits with `seats_full`, fleetd stops the session of the task that has
been in `REVIEW` longest, at least `FLEET_REVIEW_GRACE`, one per waiting task,
and marks its claim `released`. The grace keeps a session whose step moves on
by itself (`on_turn_complete`) after passing `REVIEW`.

A released task needs its seat again when it leaves `REVIEW`, or when it moves
to another step with `auto_start_agent`. If Kandev started a live session for
it on the seat's executor, fleetd adopts that session, also while the task
stays in `REVIEW`.

**Resume.** The stopped session shows Kandev's **Resume** and **Start fresh
session** buttons. Both start the same session on the executor it ran on, the
seat, so Resume continues the same conversation there; a message to the
stopped session is refused until then. They run even when the seat is busy or
at its usage limit, since Kandev does not ask fleetd. fleetd adopts the
revived session and can free the seat again later. A new session from the
task's **+** tab uses the executor chosen in its dialog, not the seat. Otherwise fleetd continues the task in its stopped seat session, once the
seat has a free slot: it stops live sessions Kandev started for the task on
another executor and sends `session.launch` with intent `workflow_step` for
the seat session and the task's current step. Kandev resumes that session on
the seat and sends the step's prompt in the same conversation, so the agent
keeps its context across steps. For a move into an `auto_start_agent` step,
Kandev first tries the agent on its own host, which fails with
"Authentication required"; that failed session stays listed in the task. If
the seat session cannot continue, fleetd resets the task environment and
launches a new session on the seat with the step's prompt, which starts a
fresh conversation; agents should still commit or push their work at the end
of each step.

**Resume.** The stopped session shows Kandev's **Resume** and **Start fresh
session** buttons. Both start the same session on the executor it ran on, the
seat, so Resume continues the same conversation there; a message to the
stopped session is refused until then. They run even when the seat is busy or
at its usage limit, since Kandev does not ask fleetd. fleetd adopts the
revived session and can free the seat again later. A new session from the
task's **+** tab uses the executor chosen in its dialog, not the seat. Otherwise fleetd launches a new session on the
seat the task was released from, with the step's prompt. For a move into an
`auto_start_agent` step, Kandev first starts the agent on its own host, which
fails with "Authentication required" and builds the task environment there;
fleetd stops that session and resets the task environment before launching
on the seat. The failed host session stays listed in the task. The new
session starts a fresh conversation; files from the earlier session stay on
the seat but are not reattached, so agents should commit or push their work
at the end of each step.

Rules for a workflow that fleetd runs:

- The intake step must not have `auto_start_agent`; Kandev would start the
  task on its own executor first. fleetd logs a warning at startup when it
  does.
- Later steps may use `auto_start_agent`, `on_turn_complete` moves, and
  human gates.
- Put each workflow's intake step in `FLEET_INTAKE_STEP_ID`, or create its
  tasks with the fleet agent profile.

## Monitoring

After each dispatch round fleetd reads the Kandev state of every launched task
it has not yet seen end (`COMPLETED`, `CANCELLED`, or deleted) or fail, and
records it with the time it ended. When a task ends fleetd sends
`session.stop` for its session once: a session waiting for input keeps
agentctl running and the seat full. The stopped session is `CANCELLED` and
Kandev does not resume it; its transcript and the task directory on the seat
stay. `/metrics` reports:

| Metric | Labels | Meaning |
| --- | --- | --- |
| `fleet_tasks` | `seat`, `owner`, `outcome` | Tasks launched since the fleet database was created. `outcome` is `launching`, `running` (including a task in `REVIEW` that keeps its session), `paused` (at a usage limit), `released`, `launch_failed`, `failed` (its session failed mid-run; it is retried), or the ended Kandev state in lower case (`completed`, `cancelled`, `deleted`). |
| `fleet_task_info` | `task`, `seat`, `owner`, `state`, `outcome`, `attempt` | One series (value 1) per task running now or finished in the last 24 h; `state` is the last Kandev task state. |
| `fleet_task_duration_seconds` | `task`, `seat`, `owner` | Launch to finish, or launch to now while running. |

`deploy/monitoring` holds a vmagent scrape job (`scrape-fleetd.yml`), vmalert
rules (`alerts-fleetd.yml`: missing heartbeats, quota at 85 %, cooldown, no
usable seat, waiting tasks, launch failures, long tasks), and a Grafana
dashboard (`grafana-fleet-dashboard.json`, import it and pick the
VictoriaMetrics data source).

## API

| Endpoint | Auth | Purpose |
| --- | --- | --- |
| `GET /healthz` | none | `{"status":"ok"}` when the database answers. |
| `POST /v1/register` | one-time token in the body | Register a seat; creates its Kandev SSH executor and returns the seat's HMAC secret once. |
| `POST /v1/heartbeat` | HMAC (`X-Fleet-Seat`, `X-Fleet-Timestamp`, `X-Fleet-Signature`) | Report limit, quota, reset times, and free slots; returns the seat state. |
| `GET /v1/admin/seats` | `Bearer $FLEET_ADMIN_TOKEN` | List seats (no secrets). |
| `POST /v1/admin/seats/{id}/drain` | admin | Stop new work on a seat; running sessions continue. |
| `POST /v1/admin/seats/{id}/undrain` | admin | Return a draining seat to service. |
| `POST /v1/admin/seats/{id}/retire[?force=true]` | admin | Delete the seat's Kandev executor and retire it. Without `force` the seat must be draining with every slot free. The endpoint can then be registered again. |
| `POST /v1/admin/seats/{id}/rotate-secret` | admin | Issue a new HMAC secret (shown once); put it in the node's config. |
| `GET /v1/admin/handoffs[?state=offered,...]` | admin | List handoff offers for tasks blocked by a seat's limit. |
| `POST /v1/admin/handoffs/{id}/accept` | admin | Body `{"seat_id": "..."}`: the member owning that seat takes the task; fleetd reassigns it and starts a new session of the same task on their seat with a checkpoint prompt. On failure the original assignee is restored and the handoff is offered again. |
| `GET /v1/admin/tasks/status` | admin | `{"workspace_id", "at", "tasks": {"<task id>": {"status", "reason", "seat_id", "reset_at", "attempt", "max_attempts", "retry_at", "last_error"}}}` as of the last round. `status` is `waiting` (with `reason`), `starting`, `running`, `review`, `paused`, `released`, `retrying`, or `exhausted`. The Kandev plugin shows it on task cards. |
| `POST /v1/admin/tasks/{id}/assign-creator` | admin | Body `{"user_id": "..."}`: assign an unassigned task in the fleet workspace to that user. `result` is `assigned`, `already_assigned`, or `not_fleet_workspace`. |
| `GET /metrics` | `Bearer $FLEET_METRICS_TOKEN` when set | Prometheus text: `fleet_seats{state}`, and per live seat `fleet_seat_info`, `_capacity`, `_free_slots`, `_quota_5h_percent`, `_quota_week_percent`, `_limit_reset_seconds`, `_heartbeat_age_seconds` (labels `seat`, `owner`); with the dispatcher, `fleet_queue_tasks{priority}`, `fleet_queue_waiting_tasks{priority,reason}`, `fleet_queue_exhausted_tasks`, `fleet_dispatch_launches_total`, `fleet_dispatch_failures_total`, `fleet_handoffs{state}`, `fleet_tasks{seat,owner,outcome}`, and for tasks running or finished in the last 24 h `fleet_task_info{task,seat,owner,state,outcome,attempt}` and `fleet_task_duration_seconds{task,seat,owner}`. See [Monitoring](#monitoring). |

Issue a registration token for a seat owner:

```sh
./bin/fleetd token create --owner <kandev-user-id> --labels team:payments --ttl 24h
```

Heartbeat signature: `hex(HMAC-SHA256(secret, "<unix seconds>\n<METHOD>\n<path>\n<hex sha256(body)>"))`.
The timestamp must be within 5 minutes of the server clock and newer than the
seat's last accepted one, so a captured request cannot be replayed. Seats
silent for `FLEET_HEARTBEAT_TIMEOUT` go offline; draining and retired seats
keep their state.

## Run the agent

```sh
fleet-agent preflight            # claude, git, sshd, 20 GiB free disk, sleep disabled (macOS)
fleet-agent run                  # heartbeat every 30s and a quota probe every 5m until SIGTERM
fleet-agent checkpoint --worktree <dir> --task <kandev-task-id>
```

`checkpoint` commits uncommitted work (hooks still run) and force-pushes HEAD to
`refs/agent-wip/<task>` with the `ci.skip` push option. It runs no model, so it
works while the seat is at its usage limit. Pass `--no-ci-skip` for a remote
that does not accept push options.

Free slots are capacity minus this user's `agentctl` processes (one per Kandev
session) that run an agent, that is a child other than a shell, or that started
less than 2 minutes ago. Kandev also restarts a finished task's `agentctl` when
someone views its files or terminal; with only shells under it, it holds no slot.

### Quota

`fleet-agent run` reads the seat's subscription usage every 5 minutes
(`--quota-probe-interval`, `0` disables it). It sends one Messages request
(`--quota-probe-model`, default `claude-haiku-4-5`, `max_tokens: 1`) with the
seat's own login and reads Anthropic's `anthropic-ratelimit-unified-*`
response headers: 5-hour and 7-day utilization, their reset times, and
whether the subscription is `rejected`. Each probe costs about 9 tokens of the
seat's own quota. `count_tokens` does not return these headers, and the
`/api/oauth/usage` endpoint needs the `user:profile` scope that
`claude setup-token` tokens lack.

The login is the `CLAUDE_CODE_OAUTH_TOKEN` the login shell exports to SSH
sessions, read the way sshd runs a command, or else the access token in
`~/.claude/.credentials.json`. It stays on the node: fleet-agent sends only the
percentages and reset times to fleetd.

The probe writes `~/.config/fleet-agent/quota.json` (path in the config as
`quota_file`), and every heartbeat reports it. Another tool may write the same
file when the probe is disabled:

```json
{
  "limit": "5h",
  "quota_5h_pct": 100,
  "quota_week_pct": 63.5,
  "reset_5h_at": "2026-10-26T14:00:00Z",
  "updated_at": "2026-10-26T11:58:00Z"
}
```

`limit` is `""`, `"5h"`, or `"week"`. A limit holds until its reset time, or
for 15 minutes after `updated_at` when no reset time is known. Percentages
older than 15 minutes are dropped. With no file, the seat reports no limit.

## Test

```sh
make test vet
```
