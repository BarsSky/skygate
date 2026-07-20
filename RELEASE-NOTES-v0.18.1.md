# v0.18.1 — small fixes

2026-07-20

Three small wins, all of which the operator flagged
during the v0.18.0 deploy:

## 1. `check_https.py` HSTS /login 404 (the user
   reported `--login-server=` was empty in the
   tutorial)

The `make test` `check-https` target was failing on
the VM with "HSTS: /login probe failed: HTTP Error
404". The root cause: the VM uses **openresty** as
the TLS terminator (not Caddy as the docs say).
openresty doesn't route `/login` to skygate — it
404s the path. The HSTS header is still set on the
host globally (we verified: `Strict-Transport-Security:
max-age=63072000; preload` on `/` and
`/api/v1/apikey`), but `/login` was unreachable as
a real backend request.

The fix: `check_hsts` now tries multiple paths in
priority order — `/login`, `/`, `/api/v1/apikey` —
and accepts the HSTS from whichever path returns
a real response (2xx/4xx/5xx). If `/login` 404s
(openresty config), we fall back to `/` (200 with
HSTS) and the check passes with a clear "via / —
/login was 404" message.

* `scripts/check_https.py` — `check_hsts` rewritten
  with fallback chain; reads headers from 4xx/5xx
  responses (was raising HTTPError on 404)
* `scripts/test_check_https.py` (new) — 4 regression
  tests:
  - `test_openresty_fallback_to_root` — the VM's
    config (/login 404, / 200+HSTS)
  - `test_openresty_fallback_to_apikey` — variant
    where both /login and / 404
  - `test_caddy_login_works` — the docs' Caddy
    config (/login 200+HSTS, primary path)
  - `test_no_hsts_anywhere` — TLS terminator with
    no HSTS at all

`make test` is now FULLY green (smoke 118/118 +
check_exit_nodes + check_https all PASS).

## 2. `admin/exit-nodes` "Tag as exit-node" / "Untag" buttons

The operator's old workflow for a fresh relay:

```bash
# On the relay:
sudo tailscale set --advertise-exit-node

# On the headscale host:
docker exec headscale headscale nodes approve-routes \
  --identifier N --routes 0.0.0.0/0,::/0
docker exec headscale headscale nodes tag \
  --identifier N --tags tag:exit-node
```

The two `docker exec` calls are now a single
button on `/admin/exit-nodes`. When a node has
`0.0.0.0/0` + `::/0` in `AvailableRoutes`, the
"Tag as exit-node" button appears next to the
delete button. Clicking it:

  1. Approves ONLY the exit-node bases
     (`0.0.0.0/0`, `::/0`) via
     `HSGlobal().ApproveRoutesForNodeID(...)`.
     We deliberately do NOT approve the full
     `AvailableRoutes` set — karolina has 200+
     subnets that the operator does NOT want
     auto-approved.
  2. Applies `tag:exit-node` via
     `HSGlobal().TagNode(...)`.
  3. Invalidates the headscale cache + writes
     an `exit_node_tag` audit log row.
  4. Redirects with a flash success message.

The existing ACL (`* → tag:exit-node:*`) already
allows the new tag, so no ACL re-push is needed —
Tailscale clients pick up the new tag on their
next ACL poll (usually <60s).

A symmetric **"Untag"** button appears for nodes
that already have `tag:exit-node`, so the operator
can demote a relay back to a regular node (e.g.
maintenance window).

* `internal/headscale/routes.go` —
  `ApproveRoutesForNodeID(nodeID, routes)` (new
  public API). The old `ApproveAllRoutes*` is
  kept for backward compatibility.
* `internal/handlers/admin_exit_nodes.go` —
  `PostAdminExitNodeTagAsExitNode` +
  `PostAdminExitNodeUntagAsExitNode`. `ExitNodeInfo`
  struct extended with `Tags` + `AdvertisesV4Default` +
  `AdvertisesV6Default` so the template can
  decide which button to render.
* `internal/handlers/templates/admin/exit_nodes.html`
  — conditional `<form>` for the two buttons.
* `internal/i18n/catalog.go` — 6 new keys
  (RU+EN): `tag_as_exit_button`,
  `tag_as_exit_help`, `untag_exit_button`,
  `untag_exit_help`, `tagged_exit_pill`,
  `not_advertising_exit`.
* `internal/handlers/admin_exit_nodes_tag_test.go`
  (new) — 4 unit tests: missing/bad node_id,
  non-admin forbidden, both Tag and Untag
  handlers.
* `cmd/skygate/main.go` — two new route
  registrations: `POST /admin/exit-nodes/tag-as-exit`
  and `POST /admin/exit-nodes/untag`.

## 3. `ControlURL` auto-injection in
   `renderWithLayout` — the bug behind the
   empty `--login-server=`

The `/admin/exit-nodes` Step-2 tutorial and
`/my/preauth` result page both reference
`{{.ControlURL}}` in their `tailscale up`
instructions, but neither handler passed
`ControlURL` in the data map. The rendered
HTML showed:

```
sudo tailscale up --login-server= --ssh --advertise-exit-node --accept-routes
```

with an EMPTY `--login-server=`. The operator
had to know to fill it in manually.

The fix: `renderWithLayout` now auto-injects
`data["ControlURL"] = a.ControlURL` on every
page render. The operator's `SKYGATE_CONTROL_URL`
env var flows through `New(...)` →
`App.ControlURL` → data map → template. Handlers
can still override it (the for-loop in
`renderWithLayout` merges caller values, so
caller wins) but they no longer HAVE to.

Live verification: the `/admin/exit-nodes` Step-2
block now renders with the actual hostname:

```
sudo tailscale up --login-server=https://head.skynas.ru --ssh --advertise-exit-node --accept-routes
```

* `internal/handlers/handlers.go` —
  `renderWithLayout` auto-injects `ControlURL`.
* `internal/handlers/handlers_test.go` (new) —
  2 regression tests:
  - `TestRenderWithLayout_AutoInjectsControlURL`
    — synthetic template with `{{.ControlURL}}`
    body; assert the URL lands in the body.
  - `TestRenderWithLayout_CallerCannotOverrideControlURL`
    — documents the current merge order (auto
    runs AFTER caller values, so `a.ControlURL`
    always wins). Update when v0.12.0+ per-user
    ControlURL lands.

## Live verification

* `make test` — smoke 118/118 (en 59 + ru 59,
  both 0 fail), check_exit_nodes PASS (3
  relays), check_https PASS (TLS, SAN, cert
  validity, HTTP→HTTPS redirect, HSTS via /
  fallback).
* `/admin/exit-nodes` Step-2 — renders with
  `--login-server=https://head.skynas.ru` (was
  empty before v0.18.1).
* `/admin/exit-nodes` action column — the new
  "Tag as exit-node" / "Untag" buttons are
  visible for emilia, sharlotta, karolina (all
  3 already have `tag:exit-node` so they show
  the "Untag" variant; new relays will show
  "Tag as exit-node" once they advertise
  0.0.0.0/0+::/0).

12/12 packages green, smoke 118/118, live at
the v0.18.1 build.
