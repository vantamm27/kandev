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
| `FLEET_INTAKE_STEP_ID` | empty | Workflow step where tasks wait for a seat; with this or the workspace empty, the dispatcher does not run. |
| `FLEET_DISPATCH_INTERVAL` | `20s` | Time between dispatch rounds. |

## Dispatching

Each round fleetd lists the workspace's tasks and picks those that are in the
intake step, have a human assignee, and have no session yet. Create them with
`start_agent=false` (for example `create_task_kandev` with
`assignee_user_id: "me"`), otherwise Kandev starts them on its default
executor. The schedule package orders them (priority plus capped aging) and
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
- **Offers handoffs** for launched tasks on a seat at its weekly limit, or at
  its 5-hour limit with the reset unknown or more than 2 hours away. Shorter
  5-hour waits are left to the Kandev dynamic profile. Accept with
  `POST /v1/admin/handoffs/{id}/accept`.

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
| `GET /metrics` | `Bearer $FLEET_METRICS_TOKEN` when set | Prometheus text: `fleet_seats{state}`, and per live seat `fleet_seat_info`, `_capacity`, `_free_slots`, `_quota_5h_percent`, `_quota_week_percent`, `_limit_reset_seconds`, `_heartbeat_age_seconds` (labels `seat`, `owner`); with the dispatcher, `fleet_queue_tasks{priority}`, `fleet_queue_waiting_tasks{priority,reason}`, `fleet_queue_exhausted_tasks`, `fleet_dispatch_launches_total`, `fleet_dispatch_failures_total`, `fleet_handoffs{state}`. |

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
fleet-agent run                  # heartbeat every 30s until SIGTERM (the LaunchAgent runs this)
fleet-agent checkpoint --worktree <dir> --task <kandev-task-id>
```

`checkpoint` commits uncommitted work (hooks still run) and force-pushes HEAD to
`refs/agent-wip/<task>` with the `ci.skip` push option. It runs no model, so it
works while the seat is at its usage limit. Pass `--no-ci-skip` for a remote
that does not accept push options.

Free slots are capacity minus this user's `agentctl` processes (one per Kandev
session).

### Quota file

fleet-agent does not read Claude Code's quota itself. A Claude Code hook or
statusline script writes `~/.config/fleet-agent/quota.json` (path in the
config as `quota_file`), and every heartbeat reports it:

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
for 15 minutes after `updated_at` when no reset time is known (the weekly
notice has no date). Percentages older than 15 minutes are dropped. With no
file, the seat reports no limit. Which source fills this file reliably is
spike S2 in the backlog.

## Test

```sh
make test vet
```
