// kandev-fleet native UI: a Fleet page listing seats and handoff offers.
// Data comes from the plugin backend's authenticated actions, which call
// fleetd with the admin token kept in plugin config.
(function () {
  "use strict";

  var REFRESH_MS = 30000;

  function pct(value) {
    return typeof value === "number" ? value.toFixed(0) + "%" : "-";
  }

  function stateLabel(seat) {
    if (seat.state === "cooldown_5h") return "cooldown (5h)";
    if (seat.state === "cooldown_week") return "cooldown (week)";
    return seat.state;
  }

  window.registerKandevPlugin("kandev-fleet", {
    initialize: function (registry, host) {
      var React = host.React;
      var jsx = host.jsx;

      function activeWorkspaceId() {
        var state = host.store.getState();
        return (state.workspaces && state.workspaces.activeId) || undefined;
      }

      function invoke(key, body) {
        return host.api.invokeAction(key, { workspaceId: activeWorkspaceId(), body: body || {} });
      }

      function cell(content, key) {
        return jsx("td", { style: { padding: "4px 12px 4px 0" }, children: content }, key);
      }

      function SeatsTable(props) {
        var rows = props.seats.map(function (seat) {
          return jsx(
            "tr",
            {
              children: [
                cell(seat.id, "id"),
                cell(seat.owner_user_id, "owner"),
                cell(seat.ssh_user + "@" + seat.ssh_host, "host"),
                cell(stateLabel(seat), "state"),
                cell(seat.free_slots + "/" + seat.capacity, "slots"),
                cell(pct(seat.quota_5h_pct), "q5h"),
                cell(pct(seat.quota_week_pct), "qweek"),
              ],
            },
            seat.id,
          );
        });
        var head = ["Seat", "Owner", "Node", "State", "Free", "5h", "Week"].map(function (title) {
          return jsx("th", { style: { textAlign: "left", paddingRight: "12px" }, children: title }, title);
        });
        return jsx("table", {
          children: [
            jsx("thead", { children: jsx("tr", { children: head }) }, "head"),
            jsx("tbody", { children: rows }, "body"),
          ],
        });
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
                    "button",
                    {
                      type: "button",
                      disabled: props.busy === h.id,
                      onClick: function () {
                        props.onAccept(h.id);
                      },
                      style: { marginLeft: "8px" },
                      children: props.busy === h.id ? "Taking..." : "Take task",
                    },
                    "take",
                  )
                : null;
            var note = h.state === "exhausted" ? " (moved too often; needs a person)" : "";
            return jsx(
              "li",
              {
                children: [
                  jsx("span", { children: "Task " + h.task_id + " from " + h.from_seat_id + note }, "text"),
                  take,
                ],
              },
              h.id,
            );
          }),
        });
      }

      function FleetPage() {
        var dataState = React.useState(null);
        var errorState = React.useState("");
        var busyState = React.useState("");
        var data = dataState[0];

        var load = React.useCallback(function () {
          return invoke("fleet.overview")
            .then(function (result) {
              dataState[1](result);
              errorState[1]("");
            })
            .catch(function (err) {
              errorState[1]((err && err.message) || "Could not load the fleet.");
            });
        }, []);

        React.useEffect(
          function () {
            load();
            var timer = setInterval(load, REFRESH_MS);
            return function () {
              clearInterval(timer);
            };
          },
          [load],
        );

        function accept(id) {
          busyState[1](id);
          invoke("fleet.handoff.accept", { handoffId: id })
            .then(load)
            .catch(function (err) {
              errorState[1]((err && err.message) || "Could not take the task.");
            })
            .finally(function () {
              busyState[1]("");
            });
        }

        return jsx("main", {
          style: { padding: "16px", display: "flex", flexDirection: "column", gap: "16px" },
          children: [
            jsx("h1", { style: { fontSize: "20px" }, children: "Fleet" }, "title"),
            errorState[0] ? jsx("p", { role: "alert", children: errorState[0] }, "error") : null,
            data
              ? jsx(React.Fragment, {
                  children: [
                    jsx("h2", { style: { fontSize: "16px" }, children: "Waiting for another member" }, "h-title"),
                    jsx(
                      HandoffList,
                      { handoffs: data.handoffs || [], canAccept: data.can_accept, busy: busyState[0], onAccept: accept },
                      "handoffs",
                    ),
                    data.can_accept
                      ? null
                      : jsx("p", { children: "You need an online seat with a free slot to take a task." }, "hint"),
                    jsx("h2", { style: { fontSize: "16px" }, children: "Seats" }, "s-title"),
                    jsx(SeatsTable, { seats: data.seats || [] }, "seats"),
                  ],
                }, "data")
              : jsx("p", { children: "Loading..." }, "loading"),
          ],
        });
      }

      registry.registerRoute("/fleet", FleetPage);
      registry.registerNavItem({ id: "fleet", label: "Fleet", path: "/fleet", icon: "users" });
    },
  });
})();
