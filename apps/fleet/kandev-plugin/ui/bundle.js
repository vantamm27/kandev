// kandev-fleet native UI: a Fleet page with seats, the dispatch queue, task
// stats, and handoff offers. Data comes from the plugin backend's actions,
// which call fleetd with the admin token kept in plugin config. Seat
// management uses an admin-only action; Kandev enforces it, and the page only
// hides the controls from members.
(function () {
  "use strict";

  var REFRESH_MS = 30000;

  var WAIT_REASONS = {
    no_seat: "owner has no live seat",
    labels: "no owner seat has the required labels",
    seats_full: "owner seats are busy or cooling down",
    quota: "not enough quota left",
    weekly_pacing: "weekly usage ahead of pace",
    unknown_priority: "unknown priority",
    no_assignee: "nobody is assigned; use Assign to me in the task's top bar",
  };

  var OUTCOMES = ["running", "launching", "released", "completed", "cancelled", "failed", "deleted", "launch_failed"];

  function pct(value) {
    return typeof value === "number" ? value.toFixed(0) + "%" : "-";
  }

  function stateLabel(seat) {
    if (seat.state === "cooldown_5h") return "cooldown (5h)";
    if (seat.state === "cooldown_week") return "cooldown (week)";
    return seat.state;
  }

  function duration(seconds) {
    if (typeof seconds !== "number") return "-";
    var m = Math.floor(seconds / 60);
    if (m < 1) return Math.round(seconds) + "s";
    if (m < 60) return m + "m";
    return Math.floor(m / 60) + "h " + (m % 60) + "m";
  }

  function when(iso) {
    if (!iso) return "-";
    var d = new Date(iso);
    return isNaN(d.getTime()) ? "-" : d.toLocaleString();
  }

  function shortId(id) {
    return id && id.length > 8 ? id.slice(0, 8) : id;
  }

  window.registerKandevPlugin("kandev-fleet", {
    initialize: function (registry, host) {
      var React = host.React;

      // host.jsx is React.createElement, which reads a third argument as a
      // child; this adapter takes the key there like the JSX runtime does.
      function jsx(type, props, key) {
        var p = Object.assign({}, props);
        if (key !== undefined) p.key = key;
        return host.jsx(type, p);
      }

      function activeWorkspaceId() {
        var state = host.store.getState();
        return (state.workspaces && state.workspaces.activeId) || undefined;
      }

      function viewerIsAdmin() {
        var auth = host.store.getState().auth;
        return Boolean(auth && auth.user && auth.user.role === "admin");
      }

      function invoke(key, body) {
        return host.api.invokeAction(key, { workspaceId: activeWorkspaceId(), body: body || {} });
      }

      var cellStyle = { padding: "4px 16px 4px 0", verticalAlign: "top" };
      var mutedStyle = { opacity: 0.7, fontSize: "12px" };

      function cell(content, key) {
        return jsx("td", { style: cellStyle, children: content }, key);
      }

      function Table(props) {
        var head = props.head.map(function (h) {
          return jsx("th", { style: Object.assign({ textAlign: "left" }, cellStyle), children: h }, h);
        });
        return jsx("table", {
          style: { borderCollapse: "collapse", fontSize: "14px" },
          children: [
            jsx("thead", { children: jsx("tr", { children: head }) }, "head"),
            jsx("tbody", { children: props.rows }, "body"),
          ],
        });
      }

      function Section(props) {
        return jsx("section", {
          style: { display: "flex", flexDirection: "column", gap: "8px" },
          children: [
            jsx("h2", { style: { fontSize: "16px", fontWeight: 600 }, children: props.title }, "title"),
            props.children,
          ],
        });
      }

      function Button(props) {
        return jsx("button", {
          type: "button",
          disabled: props.disabled,
          onClick: props.onClick,
          style: {
            marginRight: "6px",
            padding: "2px 8px",
            border: "1px solid currentColor",
            borderRadius: "4px",
            opacity: props.disabled ? 0.5 : 1,
            color: props.danger ? "#e5484d" : undefined,
          },
          children: props.label,
        });
      }

      // SeatActions offers the admin operations that fit the seat's state.
      // Force retire asks for a second click instead of a browser dialog.
      function SeatActions(props) {
        var seat = props.seat;
        var busy = props.busy === seat.id;
        function opButton(label, op) {
          return jsx(Button, { label: label, disabled: busy, onClick: function () { props.onOp(seat.id, op); } }, op);
        }
        if (seat.state !== "draining") return opButton("Drain", "drain");
        var armed = props.armed === seat.id;
        return jsx("span", {
          children: [
            opButton("Undrain", "undrain"),
            opButton("Retire", "retire"),
            jsx(
              Button,
              {
                label: armed ? "Confirm force retire" : "Force retire",
                danger: true,
                disabled: busy,
                onClick: function () {
                  if (armed) props.onOp(seat.id, "retire_force");
                  else props.onArm(seat.id);
                },
              },
              "force",
            ),
          ],
        });
      }

      function SeatsTable(props) {
        var head = ["Seat", "Owner", "Node", "State", "Free", "5h", "Week", "Last heartbeat"];
        if (props.isAdmin) head.push("Manage");
        var rows = props.seats.map(function (seat) {
          var cells = [
            cell(seat.id, "id"),
            cell(seat.owner_user_id, "owner"),
            cell(seat.ssh_user + "@" + seat.ssh_host, "host"),
            cell(stateLabel(seat), "state"),
            cell(seat.free_slots + "/" + seat.capacity, "free"),
            cell(pct(seat.quota_5h_pct), "q5"),
            cell(pct(seat.quota_week_pct), "qw"),
            cell(when(seat.last_heartbeat_at), "hb"),
          ];
          if (props.isAdmin) {
            cells.push(
              cell(
                jsx(SeatActions, { seat: seat, busy: props.busy, armed: props.armed, onOp: props.onOp, onArm: props.onArm }),
                "manage",
              ),
            );
          }
          return jsx("tr", { children: cells }, seat.id);
        });
        return jsx(Table, { head: head, rows: rows });
      }

      function QueueView(props) {
        var queue = props.queue || {};
        var queued = queue.queued || {};
        var total = Object.keys(queued).reduce(function (n, k) {
          return n + queued[k];
        }, 0);
        var summary = jsx(
          "p",
          {
            style: mutedStyle,
            children:
              total +
              " task(s) in the intake step" +
              (total ? " (" + Object.keys(queued).sort().map(function (k) { return k + ": " + queued[k]; }).join(", ") + ")" : "") +
              (queue.at ? ", as of " + when(queue.at) : ""),
          },
          "summary",
        );
        var waiting = queue.waiting || [];
        if (!waiting.length) {
          return jsx(React.Fragment, { children: [summary, jsx("p", { children: "Nothing is waiting." }, "none")] });
        }
        var rows = waiting.map(function (w) {
          return jsx(
            "tr",
            {
              children: [
                cell(w.title || shortId(w.task_id), "title"),
                cell(w.priority || "-", "priority"),
                cell(w.assignee_user_id || "-", "assignee"),
                cell(WAIT_REASONS[w.reason] || w.reason, "reason"),
              ],
            },
            w.task_id,
          );
        });
        return jsx(React.Fragment, {
          children: [summary, jsx(Table, { head: ["Task", "Priority", "Assignee", "Waiting because"], rows: rows }, "table")],
        });
      }

      function StatsTable(props) {
        var bySeat = props.bySeat || [];
        if (!bySeat.length) return jsx("p", { children: "No tasks launched yet." });
        // Known outcomes in lifecycle order, then any others fleetd reports.
        var seen = {};
        bySeat.forEach(function (s) {
          Object.keys(s.outcomes).forEach(function (o) {
            seen[o] = true;
          });
        });
        var used = OUTCOMES.filter(function (o) {
          return seen[o];
        }).concat(
          Object.keys(seen).filter(function (o) {
            return OUTCOMES.indexOf(o) < 0;
          }),
        );
        var rows = bySeat.map(function (s) {
          var total = 0;
          var cells = [cell(s.seat_id, "seat"), cell(s.owner_user_id, "owner")];
          used.forEach(function (o) {
            total += s.outcomes[o] || 0;
            cells.push(cell(s.outcomes[o] || 0, o));
          });
          cells.push(cell(total, "total"));
          return jsx("tr", { children: cells }, s.seat_id);
        });
        return jsx(Table, { head: ["Seat", "Owner"].concat(used, ["total"]), rows: rows });
      }

      function RecentTable(props) {
        var recent = props.recent || [];
        if (!recent.length) return jsx("p", { children: "No tasks in the last 7 days." });
        var rows = recent.map(function (t) {
          return jsx(
            "tr",
            {
              children: [
                cell(t.title || shortId(t.task_id), "title"),
                cell(t.seat_id, "seat"),
                cell(t.outcome + (t.last_error ? " (" + t.last_error + ")" : ""), "outcome"),
                cell(t.attempt, "attempt"),
                cell(when(t.claimed_at), "started"),
                cell(duration(t.duration_seconds), "duration"),
              ],
            },
            t.task_id,
          );
        });
        return jsx(Table, { head: ["Task", "Seat", "Outcome", "Attempt", "Started", "Run time"], rows: rows });
      }

      function HandoffList(props) {
        if (!props.handoffs.length) {
          return jsx("p", { children: "No tasks are waiting for another member." });
        }
        return jsx("ul", {
          children: props.handoffs.map(function (h) {
            var take =
              h.state === "offered" && props.canAccept
                ? jsx(
                    Button,
                    {
                      disabled: props.busy === h.id,
                      onClick: function () {
                        props.onAccept(h.id);
                      },
                      label: props.busy === h.id ? "Taking..." : "Take task",
                    },
                    "take",
                  )
                : null;
            var note = h.state === "exhausted" ? " (moved too often; needs a person)" : "";
            return jsx(
              "li",
              {
                children: [
                  jsx("span", { style: { marginRight: "8px" }, children: "Task " + h.task_id + " from " + h.from_seat_id + note }, "text"),
                  take,
                ],
              },
              h.id,
            );
          }),
        });
      }

      function FleetPage() {
        var loadedState = React.useState(null);
        var errorState = React.useState("");
        var busyState = React.useState("");
        var armedState = React.useState("");
        var isAdmin = viewerIsAdmin();

        var load = React.useCallback(function () {
          return Promise.all([invoke("fleet.overview"), invoke("fleet.tasks")])
            .then(function (results) {
              loadedState[1]({ overview: results[0], tasks: results[1] });
              errorState[1]("");
            })
            .catch(function (err) {
              errorState[1]((err && err.message) || "Could not load the fleet.");
            });
        }, []);

        React.useEffect(
          function () {
            load();
            var timer = setInterval(function () {
              if (!document.hidden) load();
            }, REFRESH_MS);
            return function () {
              clearInterval(timer);
            };
          },
          [load],
        );

        function run(id, promise, failure) {
          busyState[1](id);
          return promise
            .then(load)
            .catch(function (err) {
              errorState[1]((err && err.message) || failure);
            })
            .finally(function () {
              busyState[1]("");
              armedState[1]("");
            });
        }

        function accept(id) {
          run(id, invoke("fleet.handoff.accept", { handoffId: id }), "Could not take the task.");
        }

        function seatOp(id, op) {
          run(id, invoke("fleet.seat.manage", { seatId: id, op: op }), "Could not change the seat.");
        }

        var sections = [];
        if (errorState[0]) sections.push(jsx("p", { role: "alert", style: { color: "#e5484d" }, children: errorState[0] }, "error"));
        var loaded = loadedState[0];
        if (!loaded) {
          sections.push(jsx("p", { children: "Loading..." }, "loading"));
        } else {
          var data = loaded.overview;
          var tasks = loaded.tasks;
          sections.push(
            jsx(
              Section,
              {
                title: "Seats",
                children: jsx(SeatsTable, {
                  seats: data.seats || [],
                  isAdmin: isAdmin,
                  busy: busyState[0],
                  armed: armedState[0],
                  onOp: seatOp,
                  onArm: armedState[1],
                }),
              },
              "seats",
            ),
            jsx(Section, { title: "Queue", children: jsx(QueueView, { queue: tasks.queue }) }, "queue"),
            jsx(Section, { title: "Tasks per seat", children: jsx(StatsTable, { bySeat: tasks.by_seat }) }, "stats"),
            jsx(Section, { title: "Recent tasks (7 days)", children: jsx(RecentTable, { recent: tasks.recent }) }, "recent"),
            jsx(
              Section,
              {
                title: "Waiting for another member",
                children: [
                  jsx(
                    HandoffList,
                    { handoffs: data.handoffs || [], canAccept: data.can_accept, busy: busyState[0], onAccept: accept },
                    "list",
                  ),
                  data.can_accept
                    ? null
                    : jsx("p", { style: mutedStyle, children: "You need an online seat with a free slot to take a task." }, "hint"),
                ],
              },
              "handoffs",
            ),
          );
        }
        return jsx("main", {
          style: { padding: "16px", display: "flex", flexDirection: "column", gap: "24px", overflowX: "auto" },
          children: sections,
        });
      }

      registry.registerRoute("/fleet", FleetPage);
      registry.registerNavItem({ id: "fleet", label: "Fleet", path: "/fleet", icon: "users" });
    },
  });
})();
