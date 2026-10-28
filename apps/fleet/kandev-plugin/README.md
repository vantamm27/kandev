# kandev-fleet plugin

Adds a **Fleet** page to Kandev: seats with their state, free slots, and 5-hour
and weekly usage, and the tasks waiting for another member because their seat
hit a usage limit. A member with an online seat that has a free slot can
**Take task**; the plugin backend picks that member's own seat (from Kandev's
verified action identity, never the browser) and asks fleetd to move the task.

In the fleet workspace it also:

- shows each task's fleet status on its kanban card and in the chat top bar:
  queued and why (for example the owner's seats wait for a usage limit that
  resets at 14:20), running, in review, paused at the usage limit until the
  reset, seat freed, retrying, or needing attention;
- makes the member who creates a task its assignee, from Kandev's verified
  action identity, so the task can run on their seat, and reminds them in the
  create dialog to use **Create task**: **Start task** runs it on the Kandev
  host, which has no Claude login.

The plugin backend calls fleetd's admin API with the token in the plugin
config, so the token never reaches the browser.

## Build and install

```sh
make test            # unit tests, plugin-pack, and Kandev's installer checks
make package-host    # .build/kandev-fleet-<version>.tar.gz for this machine
curl -F "package=@.build/kandev-fleet-<version>.tar.gz" http://<kandev>/api/plugins/install
```

Then open **Settings > Plugins > Fleet** and set:

- **fleetd URL**, for example `http://fleetd.internal:8484`
- **fleetd admin token**: the `FLEET_ADMIN_TOKEN` configured on fleetd

Declared executables: `linux-amd64`, `linux-arm64`, `darwin-arm64`. Build the
archive on (or for) the platform Kandev runs on.

## Actions

| Key | Scope | Who | Does |
| --- | --- | --- | --- |
| `fleet.overview` | workspace | signed-in member | Seats (retired hidden), offered and exhausted handoffs, and whether the viewer can take one. |
| `fleet.task.status` | workspace | signed-in member | What fleetd does with each task (`GET /v1/admin/tasks/status`). |
| `fleet.task.created` | workspace | signed-in member | Body `{"taskId": "..."}`: make the member the assignee of a task they just created, if it is in the fleet workspace and unassigned. |
| `fleet.handoff.accept` | workspace | signed-in member | Body `{"handoffId": "..."}`: move the task onto the member's online seat with the most free slots. |
