# Deploying the fork and fleetd on one Docker host

This runbook upgrades an existing Kandev Docker container to a source build of
this fork and starts fleetd beside it. Run every command on the host from a
checkout of the fork (`git clone -b feature/fleet https://github.com/vantamm27/kandev`).

## 1. Record the current container

```sh
docker ps --filter name=kandev --format '{{.Names}} {{.Image}} {{.Ports}}'
docker inspect kandev --format '{{json .Mounts}}'
docker inspect kandev --format '{{json .Config.Env}}'
```

Keep the output. It holds the image to roll back to, the published port, the
data volume, and any environment the new container must repeat. If the
container was started by Compose, use the Compose file instead of `docker run`
in step 4 and change only `image:`.

## 2. Build the images

```sh
docker build -f apps/fleet/deploy/Dockerfile.kandev \
  --build-arg KANDEV_COMMIT="$(git rev-parse --short HEAD)" \
  -t kandev-fleet:"$(git rev-parse --short HEAD)" .
docker build -f apps/fleet/deploy/Dockerfile.fleetd \
  -t fleetd:"$(git rev-parse --short HEAD)" .
```

`Dockerfile.kandev` overlays the built binaries on `ghcr.io/kdlbs/kandev:universal`
(`--build-arg BASE_IMAGE=...` to change it), so OS packages and the entrypoint
match upstream.

## 3. Back up the data volume

Stop Kandev first so the SQLite WAL is checkpointed into a consistent copy.

```sh
docker stop kandev
docker run --rm -v kandev-data:/data -v "$PWD":/backup debian:bookworm-slim \
  tar -czf /backup/kandev-data-$(date +%Y%m%d%H%M).tgz -C /data .
```

The archive contains the database and agent credentials. Store it with the
same care as a secret.

## 4. Swap the image

```sh
docker rename kandev kandev-old
docker run -d --name kandev --restart unless-stopped \
  -p <same port mapping as before> \
  -v kandev-data:/data \
  <same -e flags as before> \
  kandev-fleet:<sha>
docker logs -f kandev
```

## 5. Start fleetd

fleetd needs a Kandev personal access token. Sign in as the fleet service user
(an admin until spike S4 confirms the minimum role) and create one under
`Settings > Account > API Tokens`, then set `FLEET_KANDEV_TOKEN`.

```sh
cd apps/fleet/deploy
cp fleetd.env.example fleetd.env && chmod 600 fleetd.env   # then edit
FLEETD_IMAGE=fleetd:<sha> docker compose -p fleetd -f compose.fleetd.yml up -d
curl -fsS http://127.0.0.1:8484/healthz
```

Create a registration token for each seat:

```sh
docker compose -p fleetd -f compose.fleetd.yml exec fleetd fleetd token create --owner <kandev-user-id> --ttl 24h
```

## 6. Smoke test

- The Kandev UI opens and existing tasks and sessions are listed.
- `curl -fsS http://127.0.0.1:<port>/api/v1/workspaces/<id>/stats/usage-breakdown` returns JSON.
- `curl -fsS http://127.0.0.1:8484/metrics` lists `fleet_seats`.

## Rollback

```sh
docker stop kandev && docker rm kandev
docker rename kandev-old kandev && docker start kandev
```

The fleet patches add no Kandev migrations, but the fork tracks a newer
upstream than most installs, and upstream migrations may already have run on
the volume. If the old image refuses the database, also restore the step 3
archive into `kandev-data` before starting it.
Remove `kandev-old` after a few days of stable operation.
