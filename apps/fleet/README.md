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

## Test

```sh
make test vet
```
