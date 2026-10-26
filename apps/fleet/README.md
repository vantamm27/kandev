# Fleet

Registry and dispatcher for running Kandev tasks on a pool of Claude Code
subscription seats (one seat = one owner = one macOS user on a node). See
[the plan](../../docs/plans/fleet-subscription-pool/plan.md) and
[backlog](../../docs/plans/fleet-subscription-pool/backlog.md).

This is its own Go module so it builds and tests without the backend.

- `cmd/fleetd`: the service (registry API, heartbeats, dispatcher).
- `cmd/fleet-agent`: the node agent (planned, backlog F5).

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

`GET /healthz` returns `{"status":"ok"}` when the database answers.

## Test

```sh
make test vet
```
