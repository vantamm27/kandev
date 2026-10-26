# Fleet

Registry and dispatcher for running Kandev tasks on a pool of Claude Code
subscription seats (one seat = one owner = one macOS user on a node). See
[the plan](../../docs/plans/fleet-subscription-pool/plan.md) and
[backlog](../../docs/plans/fleet-subscription-pool/backlog.md).

This is its own Go module so it builds and tests without the backend.

- `cmd/fleetd`: the service (registry API, heartbeats, dispatcher).
- `cmd/fleet-agent`: the node agent, run as each seat's macOS user.

## Register a seat (on the node, as the seat user)

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

## API

| Endpoint | Auth | Purpose |
| --- | --- | --- |
| `GET /healthz` | none | `{"status":"ok"}` when the database answers. |
| `POST /v1/register` | one-time token in the body | Register a seat; creates its Kandev SSH executor and returns the seat's HMAC secret once. |
| `POST /v1/heartbeat` | HMAC (`X-Fleet-Seat`, `X-Fleet-Timestamp`, `X-Fleet-Signature`) | Report limit, quota, reset times, and free slots; returns the seat state. |

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
