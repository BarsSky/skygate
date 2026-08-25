# Skygate Backlog — abandoned / blocked / in-progress work

**Last updated**: 2026-08-25 (B172 login 'next'-redirect fix SHIPPED in v1.5.2-alpha1 — closes the OIDC handshake gap that hid the welcome-page-after-login symptom; B171 comprehensive device-delete with ACL regen SHIPPED 2026-08-25)
**Maintainer**: Mavis (skygate)
**Purpose**: Single source of truth for features that exist in the
codebase as abandoned stubs, plans that live in dead branches,
or work that the operator wants done but is blocked on something
external. This file is referenced by `AGENTS.md` and is what
Mavis (or any future AI assistant) should read before proposing
work — to avoid re-litigating old decisions and to know what
the operator's stated intent is.

If you (operator) want a feature from this file worked on, say
"do N" where N is the priority number. If you want a feature
moved up or down, just say so.

---

## Priority 9 — v1.5.0 UX gaps (added 2026-08-24)

Operator 2026-08-24 review surfaced 5 small UX gaps in the v1.5.0
release. None are blocking, but together they make the
non-engineer user journey awkward. Each is a small B-check
(half-day to 1.5 days), all independent of each other, so the
operator can pick any order.

### B162 — Device delete from /my/devices (+ session terminate)

**Status**: SHIPPED 2026-08-24 (commit 2ef776e, v1.5.1-alpha1) — see commit 2ef776e for the full diff
**Effort**: ~0.5-1 day
**Symptom**: there is a Renew button per row (B160) but no
Delete. If a user wants to remove a lost phone or a decommissioned
laptop, they have to log in to headscale manually via
`headscale nodes delete -i <id>` + clear `node_owner_map`
+ remove the auto-generated `tag:dev-<user>-<device>` ACL
references in the policy. The user-visible effect is "phantom
devices" that stay on /my/devices until the operator manually
intervenes.

**Scope**:
- New handler `PostMyDeviceDelete` in `internal/feature/my/devices.go`
  (mirror of `PostMyDeviceRenew` pattern).
- New route `POST /my/devices/{id}/delete` in `cmd/skygate/main.go`.
- New template form: per-row "Delete" button (next to Renew) with
  `onsubmit="return confirm('{{t "devices.delete_confirm"}}')"`.
- New i18n keys (RU + EN):
  - `devices.delete` — "Удалить" / "Delete"
  - `devices.delete_title` — "Удалить устройство из headscale"
  - `devices.delete_confirm` — "Удалить устройство %s из headscale?
    Tailscale-клиент сразу потеряет доступ к tailnet. Действие
    необратимо."
  - `devices.delete_ok` — "Устройство %s удалено"
  - `devices.delete_err_404` — "Устройство не найдено или не
    принадлежит вашему аккаунту"
  - `devices.delete_err_failed` — "Не удалось удалить устройство: %s"
- B-check contract: handler present, route registered, i18n
  keys present, test scopes cross-user attempt to 404, test
  handles "node no longer exists" gRPC error → 410 Gone (mirrors
  B160.1).
- The handler must also clean up `node_owner_map` row for
  this node id (otherwise the snapshot branch in
  `GetMyDevices` will re-show the row).
- Audit log: `device_deleted node_id=N hostname=H`.

**Reusable lesson** (from B160 + B160.1): the same "no longer
exists in NodeStore" gRPC error pattern applies. Match it
in the delete handler and return 410 Gone + "refresh the
page" message instead of leaking the raw gRPC text.

### B163 — System tests: full FAIL output is not visible

**Status**: SHIPPED 2026-08-24 (commit 2ef776e, v1.5.1-alpha1) — see commit 2ef776e for the full diff
**Effort**: ~0.5-1 day
**Symptom**: `/admin/system_tests` shows the FAIL reason but
truncates / formats it badly. The current template has
`<small><code>{{.Output}}</code></small>` which renders
inline, single-line, no whitespace handling. If a test
returns a multi-line error (e.g. "SQL error: column X does
not exist\n  at line 5\n  at line 6\n..."), the user sees
a wall of text in a tiny inline element that wraps
unpredictably. The operator has reported "I can't tell what
the test actually failed on".

**Scope**:
- Edit `internal/handlers/templates/admin/system_tests.html`
  line ~301: replace `<small><code>{{.Output}}</code></small>`
  with a `<details><summary>{{.Name}} details</summary><pre>{{.Output}}</pre></details>`
  collapsible block.
- Style: `pre` with `white-space: pre-wrap; word-wrap: break-word;
  max-height: 300px; overflow: auto; font-size: 12px;
  background: var(--bg-elev); padding: 8px; border-radius: 4px`.
- The collapsible is **open by default** for FAIL rows
  (so the operator sees the reason immediately), **closed
  by default** for PASS/SKIP rows.
- Add a small "copy" button next to the output (clipboard
  API, plain JS) so the operator can paste it into a
  Telegram message when asking for help.
- B-check contract: template has the new `<details>` markup
  around `{{.Output}}`, CSS rules for `pre` in the test
  section are present, copy button is rendered.

**Reusable lesson**: any template that displays a multi-line
error message should use `<pre>` (preserves whitespace) and
be visually distinguished from regular content. The
`<small><code>` pattern is a recurring code smell.

### B164 — DERP server init on new host (SSH-based auto-config)

**Status**: SHIPPED 2026-08-24 (commit 2ef776e, v1.5.1-alpha1) — see commit 2ef776e for the full diff
**Effort**: ~1.5-2 days
**Symptom**: `/admin/derp/relays` has CRUD for adding
**existing** DERP relays (you paste the hostname + region
metadata). But there's no "set up a new DERP relay on a
fresh host" flow. The operator has to:
1. SSH to the new host manually
2. Install Go (derper is a Go binary)
3. `go install tailscale.com/cmd/derper@latest`
4. Generate or place cert
5. Configure systemd unit
6. Open firewall for DERP port
7. Add the relay in `/admin/derp/relays`

That's 7 manual steps per DERP relay. The operator wants
one-click "Initialize on this host" with the same SSH
access model that exit-nodes use.

**Scope**:
- New page `/admin/derp/relays/init` with a form:
  - `hostname` (the relay's public hostname, e.g.
    `derp-fra-1.example.com`)
  - `region_id` (1-999, unique in the tailnet)
  - `region_code` (3-letter code: `fra`, `ams`, ...)
  - `region_name` (display name)
  - `ssh_user` (default `root`)
  - `ssh_target` (e.g. `root@198.51.100.10:22` or
    `root@100.64.0.7:22` for Tailscale access)
  - `ssh_key_path` (default `/root/.ssh/id_ed25519`)
  - `derp_port` (default `443` for HTTPS derper)
  - `stun_port` (default `3478`)
  - `sort_order` (1 = primary, 2 = secondary, ...)
  - `verify_tls` (checkbox, default ON — verify the
    derper cert on first connection)
- New handler `PostAdminDerpRelaysInit` that:
  1. Validates form fields.
  2. SSHes to `ssh_target` using the key.
  3. Runs `deploy/derp-init.sh` (new) on the remote host:
     - Installs Go 1.23+ if missing
     - `go install tailscale.com/cmd/derper@latest`
     - Generates self-signed cert if none provided
     - Configures systemd unit `derper.service`
     - Enables + starts the service
     - Verifies it's listening on derp_port
  4. On success, inserts a `derp_relays` row via the
     existing `db.UpsertDerpRelay` helper.
  5. Writes audit log `derp_init hostname=X region_id=Y
     result=ok|failed detail="..."`.
- B-check contract: handler present, route registered,
  template renders form, i18n keys (10+) present, deploy
  script syntax-checked, SSH connection test (mock) passes.
- **Note**: the actual SSH client is the same as the
  exit-nodes path — for v1, the handler shells out to
  `bash deploy/derp-init.sh <host> <port> <key>` (same
  pattern as `headscale.provision.go`'s
  `BootstrapScriptPath`); we don't need to bring in
  `golang.org/x/crypto/ssh` for v1.

**Reusable lesson**: the headscale-bootstrap.sh pattern
(provision.go + bash script) is the canonical "do something
on a remote host" primitive. Use it instead of writing
custom SSH client code.

### B165 — /my/devices registration form: stable layout + better hints

**Status**: SHIPPED 2026-08-24 (commit 2ef776e, v1.5.1-alpha1) — see commit 2ef776e for the full diff
**Effort**: ~1-1.5 days
**Symptom**: the "Add new device" form on `/my/devices`
has the OS tiles + custom TTL + reusable checkbox, but:
1. The fields shift on different screen widths (no grid
   layout, floats on inline-flex).
2. The hints are tiny and use `text-muted` gray — operator
   reported "the hints don't tell me anything actionable".
3. No example for SSH-key generation for users who want
   to use this device as a Linux exit-node / subnet-router.
4. The "Custom TTL" input + unit dropdown are on the same
   row but the labels are not visually grouped with the
   input — looks like two unrelated fields.

**Scope**:
- Restructure the form into a `<div class="form-grid">`
  with 2 columns on desktop, 1 on mobile.
- Add a dedicated help section (collapsible `<details>`)
  with:
  - "How to register a Linux server that will be an
    exit-node / subnet-router" step-by-step.
  - SSH key generation example:
    ```bash
    ssh-keygen -t ed25519 -N "" -f ~/.ssh/id_ed25519
    cat ~/.ssh/id_ed25519.pub >> ~/.ssh/authorized_keys
    chmod 600 ~/.ssh/id_ed25519
    ```
  - Tailscale command:
    ```bash
    sudo tailscale up --login-server=https://head.example.com \
      --authkey=<preauth-key-from-skygate> \
      --advertise-exit-node  # if exit-node
      --advertise-routes=10.0.0.0/24  # if subnet-router
    ```
  - Per-OS quick-ref: Android (Settings → Use custom
    coordination server), iOS (same), Windows (Tailscale
    GUI → top-right menu → Use custom coordination server).
- Style: use `form-grid` class (already exists in
  `static/css/themes.css`); add `form-hint-strong` for
  important hints; use `<kbd>` for key names in examples.
- B-check contract: template has the new `<details>`
  block + form-grid class + 4 new i18n keys (`reg.linux_*`,
  `reg.os_quickref_*`) + the SSH example block.

**Reusable lesson**: any form with "do this on the server
side too" should have a `details` block with the
server-side commands inline. Operators don't want to
context-switch to docs while registering a device.

### B166 — B160 e2e + system tests

**Status**: SHIPPED 2026-08-24 (commit 2ef776e, v1.5.1-alpha1) — see commit 2ef776e for the full diff
**Effort**: ~0.5-1 day
**Symptom**: B160 (device renewal) shipped with unit tests
but no e2e test that exercises the full flow on a real
headscale. The operator is concerned the unit tests
could miss a regression in the full chain (handler →
headscale gRPC → response → audit log → redirect).
Also: no system test for renew. The system test for
device health (in `internal/feature/admin/system_tests.go`)
doesn't cover renewal at all.

**Scope**:
- Add a system test `headscale.device_renew` to the
  TestRegistry in `system_tests.go` that:
  1. Picks the first non-tagged device belonging to a
     known user.
  2. Calls `headscale.ExtendNodeExpiry` with
     `now + 30d` directly (the same call the renew
     handler makes).
  3. Verifies the new expiry is in the expected range
     ([now+29d, now+31d]).
  4. Restores the original expiry (so the test is
     idempotent).
- Add a system test `headscale.device_delete` that:
  1. Lists all devices for the test user.
  2. Creates a test device via `headscale.RegisterNode`
     with a known tag.
  3. Calls `headscale.DeleteNode(id)`.
  4. Verifies the device is gone from
     `headscale.ListAllNodes`.
  5. Verifies the row is removed from `node_owner_map`.
- Document these tests in `scripts/check_b166.sh` with
  the standard contract layout (handler presence +
  test presence + 4 i18n keys).

**Reusable lesson**: system tests for "external system
calls" should be idempotent (restore state after the
test) so they can run on the production VM without
manual cleanup. The expirewatch goroutine is a good
model — it reads + writes + restores in a transaction
or with a rollback.

---

## Phase 3 B93+B111 completion (SHIPPED 2026-08-13, v1.3.11 + v1.3.12)

**Status**: SHIPPED + DEPLOYED to live VM (build `v1.3.11-2-g4a4899d`).
All 5 Phase-3 nodes re-tagged to `tag:dev-infra-*` in headscale,
re-attributed to `infra` user in `node_owner_map`, B111 catch-alls
`* → tag:dev-infra-X` active in live policy. svyatoslava portal
user removed (5/5 left in `portal_users`).

The B93 infra user (introduced 2026-07-12) was incomplete —
isInfraNode only matched `skygate-host-*` hostname prefix or
`tag:dev-infra-*` exact tag, missing all 4 relay VPSs (emilia,
karolina, sharlotta) and the 2nd-host candidate (svyatoslava-1)
that had `tag:exit-node` but no `tag:dev-infra-*`. Operator
needed skygate-host-1 (Telegram bot) to reach all 4 exit nodes
but the per-device mesh in the `infra` bucket was empty (only 1
node: skygate-host-1). B111 completed B93 with:

  1. `isInfraNode` rule 3: any node tagged `tag:exit-node` is
     infra-class. Catches all 4 relay VPSs + svyatoslava-1.
  2. `BackfillInfra` changes from `INSERT OR IGNORE` to active
     `UPDATE` — re-attributes user-portal nodes (skyadmin,
     michail, guest, daniil, svyatoslava) to `infra` when
     isInfraNode matches.
  3. New helper `getInfraExitNodeTags` in
     `internal/acl/acl_perdevice.go` — filters skygate
     (`skygate-host-*` prefix), returns sorted exit tags.
  4. Both `GenerateACLForPlane` + `GenerateACLWithViaForPlane`
     emit `* → tag:dev-infra-<exit>` catch-alls (preserves
     pre-B93 public access to the relay VPSs).

Phase 3 deployment steps (committed by Mavis, operator runbook
in `docs/B111-INFRA-RETAG-RUNBOOK.md`):

  1. Update headscale policy — add 4 `tagOwners` for
     `tag:dev-infra-{emilia,karolina,sharlotta,svyatoslava-1}`
     (catch-22: tagOwners must exist BEFORE
     `headscale nodes tag --force` accepts the new tag).
  2. `headscale nodes tag --force -i <ID> -t <NEW_TAGS>` for
     5 nodes (skygate-host-1, emilia, karolina, sharlotta,
     svyatoslava-1). Server-side change, tailscale clients
     pick up new tags on netmap sync (~10s).
  3. `UPDATE node_owner_map` (5 rows) via psql on the primary
     `172.17.0.1:5000` (NOT `localhost:5432` — read-only
     replica). All 5 → username='infra', new tags.
  4. Trigger skygate policy re-apply: `POST /admin/exit-rules/
     reapply` with admin cookie (Python+urllib from inside
     skygate container — busybox wget doesn't support cookies).
  5. Verify: ping 4 exit nodes from skygate-host-1 (all
     reachable: emilia 51ms, karolina 143ms, sharlotta
     166ms, svyatoslava-1 5ms).
  6. Delete `svyatoslava` portal user (id=11) + headscale
     user (id=84) — both via CASCADE on the portal_users
     row + `headscale users destroy --identifier 84 --force`.

Key gotcha (operator trap from earlier attempts): `tailscale up`
on an alive node is a no-op (returns RC=0, doesn't change
state). The actual re-auth requires `tailscale up --force-reauth
--reset` (for the local state) OR server-side `headscale nodes
tag --force` (for the headscale-side tags). The latter is the
correct path for tag changes — it's instant and doesn't require
restarting the tailscaled on the node.

Snapshot for rollback at `/tmp/b111_phase3_full_20260813_163219/`
(policy.json, headscale_nodes.json, node_owner_map.tsv, skygate-
host-1.state) + `/tmp/rollback_nom.sql` (DB rollback).

---

## Priority 12 — v1.5.2 live OIDC e2e on a public hostname (SHIPPED 2026-08-24, B168)

### B168 — setup-skygate-public.sh + deploy/snippets/nginx-skygate-oidc.conf

**Status**: SHIPPED 2026-08-24 (commit `7d4c91f7`, v1.5.2-alpha1)
**Effort**: ~0.5 day
**What was delivered**: closes the operator side of the B167-B168 pair. Pre-B168 the operator could not test the OIDC flow because `SKYGATE_OIDC_ISSUER` was a placeholder (`https://skygate.example.com` doesn't resolve) and there was no public reverse-proxy for the skygate container. B168 ships 2 files + 1 B-check + 1 verify_pre_deploy row:
- `deploy/snippets/nginx-skygate-oidc.conf` (~9.2 KB) — nginx server block for `skygate.skynas.ru:443`. 5 location blocks (discovery + jwks + /oidc/ + /admin/oidc + /admin/oidc/sync) all proxy to the skygate container on port 8080. Sets `X-Forwarded-Proto` (skygate OIDC code uses it to render https:// in the issuer claim). 3 TLS options documented (certbot / existing pipeline / Tailscale cert).
- `deploy/scripts/setup-skygate-public.sh` (~10.4 KB) — 5-step setup script the operator runs on the skygate VM after DNS + nginx are in place. 1: validate the new issuer URL is reachable. 2: update `SKYGATE_OIDC_ISSUER` + `SKYGATE_OIDC_REDIRECT_URIS` in .env with `.pre-setup-public.YYYYMMDDHHMMSS` backup (idempotent). 3: restart skygate. 4: wait for /healthz + verify the discovery doc reports the new issuer (round-trip check). 5: reuses `deploy/oidc-sync.sh` (B167) in docker mode to push the new headscale.conf + restart headscale. Writes an `oidc_setup` audit row.
- 19 B-check contracts in `scripts/check_b168.sh` (ALL PASS): source-contract (5 locations + X-Forwarded-Proto + 5 steps + B167 reuse) + idempotency + safety (idempotent, validates before .env, verifies after restart, .env backup) + audit log row.

**Operator action (after deploy)**:
1. Add DNS A-record on reg.ru: `skygate.skynas.ru` → `<skygate-vm-public-ip>` (same IP as head.skynas.ru)
2. Paste `deploy/snippets/nginx-skygate-oidc.conf` into the fronting nginx + `nginx -s reload`
3. On the skygate VM: `bash deploy/scripts/setup-skygate-public.sh`
4. Install Tailscale on a test device, custom coord server = `https://head.skynas.ru`, log in
5. The Tailscale client redirects to `https://skygate.skynas.ru/oidc/authorize` → `/login` → `/oidc/callback` → device registered

---

## Priority 11 — v1.5.0 HA runbooks batch (SHIPPED 2026-08-24, B151 + B152 + B153)

### B151 — init-headplane.sh (Phase 8: auto-apply headplane API key on fresh deploy)

**Status**: SHIPPED 2026-08-24 (commit `91999d68`, v1.5.2-alpha1)
**Effort**: ~0.5 day
**What was delivered**: pre-B151 the operator had to (1) generate a headscale API key via `docker exec`, (2) paste it into .env as `HEADPLANE_HEADSCALE__API_KEY`, (3) re-run `deploy.sh` + restart headplane. B151 collapses this to `bash scripts/init-headplane.sh`. 2 modes (bundled + external headplane), 6-step bundled flow with idempotent NEEDS_KEY gate, getenv/setenv helpers consistent with `deploy/lib/env.sh`. 20 B-check contracts in `scripts/check_b151.sh` (ALL PASS).

### B152 — bootstrap_standby.sh (Phase 7: provision a new skygate-standby node)

**Status**: SHIPPED 2026-08-24 (commit `91999d68`, v1.5.2-alpha1)
**Effort**: ~0.5 day
**What was delivered**: pre-B152 provisioning a new standby was a multi-hour manual runbook (clone repo, copy .env, set SKYGATE_HA_ROLE=standby, start containers, wait, verify). B152 is a single `bash scripts/bootstrap_standby.sh` on the new VM. S3-pulls the skygate binary + headscale config from the primary, starts the docker-compose stack with role=standby, verifies `/healthz` + `ha_chain` registration, writes `ha.bootstrap` audit row. Idempotent. 18 B-check contracts in `scripts/check_b152.sh` (ALL PASS).

### B153 — dr_drill.sh (Phase 9: live DR drill runbook)

**Status**: SHIPPED 2026-08-24 (commit `91999d68`, v1.5.2-alpha1)
**Effort**: ~0.5 day
**What was delivered**: pre-B153 the operator had no structured way to verify the HA chain actually works under failure. B153 is a 5-step live DR drill (verify version match + kill active + verify failover within 60s + verify no-flap rejoin + optional kill-both). 3 operator flags (`--yes` unattended, `--skip-regapi-check`, `--skip-kill-both`). Polls `/readyz` for the B145 role banner, NEVER uses `docker compose down -v` (no data destruction). 18 B-check contracts in `scripts/check_b153.sh` (ALL PASS).

**Operator action (after deploy)**:
1. Wait for the operator to provision svyatoslava-1
2. Operator runs `scripts/bootstrap_standby.sh` on the new VM
3. Operator schedules a maintenance window + runs `scripts/dr_drill.sh`
4. After the drill passes, tag `v1.5.0` (Phase 10)
5. reg.ru IP whitelist still blocking B146 (Phase 2)

---

## Priority 10 — v1.5.2 OIDC config auto-sync (SHIPPED 2026-08-24, B167)

### B167 — OIDC config auto-sync (full Option C)

**Status**: SHIPPED 2026-08-24 (commit `0c6875a`, v1.5.2-alpha1)
**Effort**: ~2 days
**What was delivered**: closes the operator-side OIDC loop. Pre-B167
the operator had to:
  1. Visit /admin/oidc to see the 4 must-match values
  2. Hand-copy the headscale.conf `oidc:` block to /etc/headscale/config.yaml
  3. `docker restart headscale` (or `systemctl restart headscale`,
     or `kubectl rollout restart deploy/headscale`)
  4. Hope headscale's /health came back OK

B167 collapses this to 1 click on /admin/oidc/sync. 6 restart modes
(auto / docker / systemd / k8s / api / manual) + a 7th (download)
that just renders the generated YAML for copy-paste. Plus a
boot-time auto-sync via `SKYGATE_OIDC_AUTOSYNC=true` for the
"deploy skygate with the OIDC env vars set and want headscale to
pick up the config on the same boot" case.

**Files**: 11 changed, +2030 lines (commit `0c6875a`):
- `deploy/oidc-sync.sh` (10-step, ~290 lines) — bash workhorse
- `internal/oidc/sync.go` (~330 lines) — Go wrapper
- `internal/oidc/sync_test.go` (5 unit tests)
- `internal/feature/admin/oidc_sync.go` (~270 lines) — admin handler
- `internal/handlers/templates/admin/oidc_sync.html` (~210 lines)
- `internal/i18n/catalog_admin.go` — 55 oidc_sync.* keys in RU + 55 in EN
- `internal/handlers/templates/layout.html` — nav.oidc_sync sidebar sub-link
- `internal/i18n/catalog_common.go` — nav.oidc_sync key in RU + EN
- `cmd/skygate/main.go` — GET + POST /admin/oidc/sync routes + boot-time auto-sync
- `scripts/check_b167.sh` (38 contracts: source + live-script + live-route)
- `scripts/verify_pre_deploy.sh` — registered B167 in the runner

**Operator action**: visit /admin/oidc/sync, click "Sync now"
(with mode=auto to use the auto-detected runner), confirm the
result on the page. Or set `SKYGATE_OIDC_AUTOSYNC=true` in the
skygate .env for a boot-time auto-sync (no manual click needed
on every restart).

**B167.1 (rolled into B167)**: the generated `oidc:` block must NOT
include `strip_email_domain` (removed in headscale 0.23+). A
regression would crash headscale 0.29.x at startup. The B167
B-check pins this as a regression guard.

---

## Priority 13 — v1.5.2 expired-row sub-classification hint on /my/devices (SHIPPED 2026-08-25, B170)

### B170 — /my/devices expired-row sub-classification hint

**Status**: SHIPPED 2026-08-25 (commit `7d90af2f`, v1.5.2-alpha1)
**Effort**: ~0.5 day
**What was delivered**: operator 2026-08-25 observed that a device force-expired by headscale (admin action, or the user running `tailscale logout`) shows up on /my/devices with the same red "Истёк" pill as a device whose TTL ran out naturally while offline. The two cases have very different root causes, so the operator wants a one-line hint that disambiguates without SSH'ing into the VM and running `headscale nodes list`. B170 ships:

- `parseLastSeenAndClassify` helper in `internal/feature/my/devices.go` — uses `|LastSeen − Expiry|` with a 5-min threshold (absolute delta so a future-dated `LastSeen` from headscale clock skew is still classified correctly). Returns `(time.Time, string)` where the string is one of `no_activity` / `near_expiry` / `while_offline`.
- `ExpiryHint` + `LastSeenTime` fields on `myNodeRow` (the row struct used by `/my/devices`).
- 3-way `{{if eq .ExpiryHint}}` chain under the existing `.ExpiryWarning` badge in `internal/handlers/templates/user/devices.html` — small muted caption, NOT a separate pill, so the visual hierarchy keeps the red pill as the primary signal.
- 4 new i18n keys (`devices.expired_hint_title` + `_no_activity` + `_near_expiry` + `_while_offline`) in RU + EN.
- 4 unit tests in `internal/feature/my/devices_b170_test.go` — pins the 3 hint categories + the 5-min boundary (inclusive on both sides) + the Nano-precision regression guard (a 5min+0.5s delta must classify as `while_offline`, not round to whole seconds).
- 28 contracts in `scripts/check_b170.sh` (ALL PASS).
- `scripts/verify_pre_deploy.sh` — B170 registered in the runner.

**Heuristic summary**:

| LastSeen vs Expiry | Hint | Most likely cause |
|---|---|---|
| `LastSeen == ""` (or unparseable) | `no_activity` | Orphan / snapshot-only / admin force-removed |
| `|LastSeen − Expiry| ≤ 5 min` | `near_expiry` | `tailscale logout` (expiry set to "now" on headscale side) |
| `|LastSeen − Expiry| > 5 min` | `while_offline` | TTL ran out while offline, OR admin force-expired a long-idle device |

**Verified locally**: `go build ./...` clean, `go vet ./...` clean, `go test ./internal/feature/my/...` PASS, `bash scripts/check_b170.sh` 28/28 PASS.

---

## Priority 14 — v1.5.2 comprehensive device-delete with ACL regen (SHIPPED 2026-08-25, B171)

### B171 — comprehensive device-delete with ACL regen (user + admin)

**Status**: SHIPPED 2026-08-25 (commit `45ab8ff9`, v1.5.2-alpha1)
**Effort**: ~1 day
**What was delivered**: closes the operator-observed gap from 2026-08-25: "кнопка удалить устройство отсуствует у пользователя... администратор также по кнопке очистит не только из skygate (из таблиц БД) но и из headscale и headplane. забирая на себя управлоение политиками и тегами, корректно подчищая и перегенерировывая acl". Pre-B171 the per-row Delete buttons on `/my/devices` (B162, v1.5.1) and `/admin/devices` (B169, v1.5.2) only cleaned three things: headscale (gRPC DeleteNode), `node_owner_map`, and `device_exit_node_prefs`. The `device_rules` table was left with orphaned rows pointing at the now-deleted device, and the ACL policy in headscale was left with `tag:dev-<user>-<device>` references that no longer existed. The next ACL regen would either skip the orphans (policy out of sync with `/my/exit-rules`) or include them and crash headscale's `SetPolicy` with a 400. B171 ships:
- `internal/devicedelete/devicedelete.go` (NEW, 327 lines) — the shared coordinator that does `node_owner_map` + `device_exit_node_prefs` + `device_rules` + ACL regen + cache invalidate + audit in one call. Both `PostMyDeviceDelete` (user scope) and `PostAdminDeviceDelete` (admin scope) call this helper, so the cleanup logic is identical for both paths.
- `db.DeleteRulesByDeviceID` + `qDeleteRulesByDeviceID` (NEW) — the SQL primitive that cleans every orphaned `device_rules` row in one query (no per-rule click required, no race window).
- `db.DeleteNodeOwnerByNodeTagCounted` (NEW) — the row-counted variant of the existing helper (returns `int64` instead of just `error`) so the audit row can include the count.
- `PostMyDeviceDelete` (B162 rewire) — now calls `devicedelete.Delete` + passes `deleted_rules=N` + `acl_err=...` in the redirect. The 410 Gone path (headscale already removed the node) still runs the cleanup so the local DB is consistent even when the deletion was triggered by a parallel admin action.
- `PostAdminDeviceDelete` (B169 rewire) — same coordinator + `ok_rules=N` + `acl_err=...` in the redirect. The 404 / exit-node-error / 410 Gone special cases are preserved.
- `/my/devices` template — Delete button moved OUTSIDE the `{{if .ExpiryUnix}}` block. The operator can now delete their own exit-nodes / subnet-routers / no-expiry devices too (the 'button is missing for my tagged device' symptom that drove the request).
- `/admin/devices` template — `FlashOkRules` + `FlashACLErr` extensions render the rules count + ACL error inline. The B169 `ok=/err=` flash pattern is preserved for backwards compat.
- 2 new i18n keys RU + EN (`devices.delete_acl_rules_cleaned` + `devices.delete_acl_err`) in `catalog_my.go`.
- The audit row emitted by `devicedelete.Delete` includes the explicit headplane note (`headplane: read-only view, will refresh on next UI load`) so the operator can confirm the full cleanup with one audit query. Headplane is read-only from headscale's API; no separate call is needed — the next headplane page load (~30s) shows the deletion automatically.
- `scripts/check_b171.sh` (NEW, 35 contracts) + fixes to `scripts/check_b162.sh` and `check_b169.sh` (the B162/B169 checks previously grepped for literal `db.DeleteNodeOwnerByNodeTag` / `InvalidateCache` / `'device_deleted'` calls inside the handler; after the B171 rewire those calls live in `devicedelete.Delete`, so the checks now accept either the direct call OR the `devicedelete.Delete` path).

**Verified locally**: `go build ./...` clean, `go vet ./...` clean, `go test ./...` (38 packages) all PASS, `bash scripts/check_b162.sh` all 26 PASS, `bash scripts/check_b169.sh` all 19 PASS, `bash scripts/check_b170.sh` all 28 PASS, `bash scripts/check_b171.sh` all 35 PASS, `make verify-pre` 156 PASS / 14 pre-existing FAIL (B95/B134-B137/B147/B159-B161/B163-B166, all from earlier v1.3.20-v1.5.0 batches, not caused by B171).

---

## Priority 15 — v1.5.2 OIDC login `next`-redirect fix (SHIPPED 2026-08-25, B172)

### B172 — login `next`-redirect fix (OIDC handshake survives the login round-trip)

**Status**: SHIPPED 2026-08-25 (commit `40f8c81b`, v1.5.2-alpha1)
**Effort**: ~0.5 day
**What was delivered**: closes the operator-observed gap from 2026-08-25: "когда попробовал залогинится в headscale через head.skynas.ru перенесло на логин в skygate, после входа в skygate открылась страница приветствия и все. устройство не добавлено и больше ничего непроисходит". Pre-B172 `PostLogin` in `internal/feature/auth/service.go` always redirected to `/dashboard`, ignoring the `next` query param that `/oidc/authorize` sets via `/login?next=...` (the full OIDC authorize URL with `client_id` + `state` + `code_challenge`). Pre-B172 the login form also had no hidden `next` input, so the OIDC handshake died silently after the user typed their password — the operator saw the welcome page and headscale's `/oidc/callback` was never reached so the device never got registered. B172 ships:
- `PostLogin` now reads + validates + honours the `next` form field. Was hard-coded to `/dashboard`, killing the OIDC flow silently. The new `http.Redirect(w, r, next, http.StatusFound)` line at the end of the function uses the validated `next` variable.
- New `safeNextRedirect` helper (the open-redirect defense). Accepts: empty (default to `/dashboard`, backwards-compat) / relative path starting with a single `/` / absolute URL with the same host as the request. Rejects: protocol-relative URLs (e.g. `//evil.com/path`) / absolute URLs with a different host / non-http(s) schemes (`javascript:`, `data:`, `file:`) / malformed URLs.
- `GetLogin` reads the `next` query string param and passes it to the template as `Next`.
- `login.html` renders a hidden `<input type="hidden" name="next" value="{{.Next}}">` inside the form. Go's `html/template` auto-escapes so a hostile `next` value can't break out of the attribute.
- The B161.4 e2e test in `internal/oidc/e2e_test.go` is extended with a new STEP 4 that wires a mock `/login` handler into the test mux and walks the full authorize → `/login?next=...` → `POST /login` → re-run `/oidc/authorize` flow. If `PostLogin` ever drops the `next` again, the e2e test fails at the "login POST: 302 → /oidc/authorize" assertion. The test also asserts the session cookie is set on the post-login redirect + the hidden `next` input is in the form + the post-login redirect preserves the `state` + `client_id` + `code_challenge` params.
- 18 unit tests in `service_b172_test.go` (`TestSafeNextRedirect` + `TestSafeNextRedirect_EmptyHost`) covering the 5 case categories of `safeNextRedirect` (empty/relative/protocol-relative/different-host/same-host). Pins the open-redirect defense so a future refactor can't silently weaken it.
- 24 B-check contracts in `scripts/check_b172.sh` (source contract + security contract + e2e contract + smoke contract).

**Root cause analysis**: the bug went undetected in the B161.4 e2e test because STEP 4 was a "pre-populate an auth code (simulating successful login)" shortcut that bypassed the `/login` round-trip entirely. B172 closes that gap by walking the actual login form via a mock handler.

**Verified locally**: `go build ./...` clean, `go vet ./...` clean, `go test ./...` (38 packages) all PASS, `go test ./internal/feature/auth/... -v` PASS (TestSafeNextRedirect 17 subtests + TestSafeNextRedirect_EmptyHost 6 subtests), `go test ./internal/oidc/... -v` PASS (TestE2E_HeadscaleClientFlow now includes STEP 4 that walks the `/login` round-trip via mock handler), `bash scripts/check_b172.sh` 24/24 PASS, `make verify-pre` 157 PASS / 13 pre-existing FAIL (B95/B134-B137/B147/B159-B161/B163-B166, all from earlier v1.3.20-v1.5.0 batches, not caused by B172).

**Live verified on VM** (build `v1.5.0-alpha1-42-g40f8c81`): the `/oidc/authorize` handler returns 302 to `/login?next=...` with all OIDC params preserved; the login form contains the hidden `next` input with the OIDC URL.

---

## Priority 16 — v1.5.2 login form submit loading-state (SHIPPED 2026-08-25, B173)

### B173 — login form submit loading-state (the user can see when the form is processing)

**Status**: SHIPPED 2026-08-25 (commit `6b1c241e`, v1.5.2-alpha1)
**Effort**: ~0.3 day
**What was delivered**: closes the operator-observed gap from 2026-08-25: "теперь при переходе страница логина всегда обновляется если написать пароль и тем самым его сбрасывает от чего нельзя залогиниться" — the page was re-rendering in <100ms with no visual feedback after the user hit Enter, so the user saw "the page refreshed and my password is gone" without any explanation. Pre-B173 the form had no JS: the user typed their password, hit Enter, and the page would re-render with no progress indicator. If credentials were wrong (typo, wrong keyboard layout, caps lock) the form re-rendered with an error message; if they were right the form would redirect to the OIDC URL. Either way the user saw "the page refreshed and my password is gone" with no explanation. The OIDC path (B172) makes the round-trip even more confusing — the user types, hits Enter, and the page silently transitions to headscale's `/oidc/callback` flow. B173 ships:
- `login.html` has a JS `onsubmit` handler wrapped in an IIFE + try/catch (a JS error falls through to the normal form submit, so the loading state is progressive enhancement, NOT a hard dependency). The handler:
  1. Calls `e.target.checkValidity()` BEFORE entering the loading state. If a required field is empty or invalid, the browser's native "this field is required" tooltip still shows — the loading state only kicks in for valid forms about to be POSTed.
  2. Sets `username` + `password` to `readOnly = true` so the user can't type more characters while the request is in flight (avoids "I typed more after Enter and it didn't submit" confusion).
  3. Disables the submit button (`btn.disabled = true`).
  4. Swaps the button label from `Войти` (RU) / `Sign in` (EN) to `Вход...` / `Signing in...` by toggling two `<span>` elements (`#login-btn-idle` + `#login-btn-loading`) via `style.display`. The loading span also contains a `<i class="fa-solid fa-spinner fa-spin">` for a CSS-only spinning indicator.
- CSS in `static/css/themes.css`:
  - `button:disabled { opacity: .6; cursor: wait; }` — the button dims and the cursor changes to a wait indicator, so the user sees the button is "stuck" (and not broken) during the in-flight request.
  - `input:read-only { background: var(--bg-elev); color: var(--text-muted); cursor: not-allowed; }` — the read-only inputs get a muted background + not-allowed cursor, reinforcing the "you can't edit this right now" state.
- New i18n key `login.submitting` in RU + EN (`internal/i18n/catalog_common.go`): RU `"Вход..."`, EN `"Signing in..."`.
- The form has `id="login-form"` + `id="login-submit"` so the JS handler can target them without re-querying. The button contains TWO `<span>` children (`.login-btn-idle` and `.login-btn-loading`) so the JS swap is a single `style.display` toggle on each.
- 12 B-check contracts in `scripts/check_b173.sh` (template structure + i18n key in both RU + EN + JS handler presence + CSS rules + verify-pre integration).

**The B172 + B173 combination is the end-to-end OIDC login UX**: B172 preserves the `next` parameter through the login POST (so the OIDC handshake survives); B173 makes the form submit observable so the user doesn't think the page just refreshed and lost their password. Together they close the two most-confusing symptoms of the OIDC login flow on a slow connection.

**Verified locally**: `go build ./...` clean, `go vet ./...` clean, `go test ./...` (38 packages) all PASS, `bash scripts/check_b173.sh` 12/12 PASS, `make verify-pre` 158 PASS / 13 pre-existing FAIL (B95/B134-B137/B147/B159-B161/B163-B166, all from earlier v1.3.20-v1.5.0 batches, not caused by B173).

**Live verified on VM** (build `v1.5.0-alpha1-44-g6b1c241`): `GET /login?next=/foo` returns HTML with `id="login-form"`, `id="login-submit"`, `<span class="login-btn-idle"><i class="fa-solid fa-arrow-right"></i>Войти</span>`, `<span class="login-btn-loading" style="display:none"><i class="fa-solid fa-spinner fa-spin"></i><span class="login-btn-loading-text">Вход...</span></span>`. EN locale (via `lang=en` cookie) renders `Sign in` + `Signing in...`. The full B172 + B173 chain is also live-verified: `/oidc/authorize?client_id=headscale&...` (no session) → 302 → `/login?next=%2Foidc%2Fauthorize%3F...` → HTML contains the loading state markup AND the `next` hidden input with the full OIDC URL.

---

## Priority 17 — v1.5.2 full-page loading overlay (SHIPPED 2026-08-25, B173.1)

### B173.1 — full-page loading overlay (catches password-manager auto-submit)

**Status**: SHIPPED 2026-08-25 (commit `9bbb750c`, v1.5.2-alpha1)
**Effort**: ~0.3 day
**What was delivered**: closes the operator-observed gap from 2026-08-25 (right after B173 shipped): "все равно рефрешь при вставке пароля из запомненых на странице логина" — the B173 button-only loading state was invisible when a password manager auto-submitted the form via `form.submit()` (which bypasses submit event listeners entirely) or when the page navigated away so fast the user never saw the button swap. The B173 IIFE only listened for the `submit` event, which is NOT fired when `HTMLFormElement.submit()` is called programmatically. Password managers (1Password, LastPass, Bitwarden, Chrome's built-in manager, Firefox's built-in manager) use `form.submit()` to auto-submit the form after auto-filling credentials, which bypasses the B173 handler entirely. B173.1 adds 3 layers of defense:
- **Full-page loading overlay** — a `position:fixed; z-index:9999` semi-transparent overlay (`rgba(0,0,0,0.55)` + `backdrop-filter: blur(2px)`) covering the entire viewport with a centered card containing a `fa-spinner fa-spin` and the same "Вход..." / "Signing in..." text. The overlay is the "you can't miss it" visual feedback that the form is processing — far more visible than the B173 button-only swap. The CSS includes a `login-loading-fadein` keyframe animation (120ms ease-out) so the overlay fades in smoothly rather than popping in.
- **`f.submit` method override** — the IIFE captures the native `HTMLFormElement.submit()` method via `var nativeSubmit = f.submit` and replaces it with a wrapper that calls `showLoading()` then defers the actual submit by 60ms via `setTimeout`. This catches programmatic submits from password managers. The 60ms delay ensures the browser has a chance to render the overlay before navigation starts. The override wraps the original call in a try/catch so a JS error in the wrapper falls through to the normal form behavior.
- **`pagehide` / `visibilitychange` / `beforeunload` listeners** — the IIFE registers 3 last-resort listeners that show the overlay whenever the page is being navigated away from, regardless of how the navigation was triggered. This catches cases where (a) the password manager bypasses our submit handler entirely, (b) the browser navigates before our event listener runs, or (c) some browser extension triggers a form submit via an unexpected path. The `visibilitychange` listener specifically checks `document.hidden` to avoid showing the overlay when the user just switches tabs (which would be annoying).
- **Single `showLoading()` function** — the IIFE consolidates all the "show the loading state" logic into a single `showLoading()` function that's called from all 5 detection paths (submit event + form.submit override + pagehide + visibilitychange + beforeunload). The function uses a `submitting` flag to prevent double-execution (e.g. when the submit event fires AND the form.submit override runs in the same tick).
- 6 new contracts in `scripts/check_b173.sh` contract D (overlay element + overlay card content + overlay CSS rules + form.submit override + 3 nav listeners + showLoading function).

**Why B173 wasn't enough**: the B173 IIFE only listened for the `submit` event. The `submit` event fires for explicit submits (Enter in input, button click, `form.requestSubmit()`) but does NOT fire when `HTMLFormElement.submit()` is called programmatically. Some password managers use `form.requestSubmit()` (which fires the event), but many use `form.submit()` directly to skip browser validation. The B173.1 `form.submit` method override catches the latter case, and the `pagehide` / `visibilitychange` / `beforeunload` listeners are the last-resort catch-all for any navigation path we missed.

**Verified locally**: `go build ./...` clean, `go vet ./...` clean, `bash scripts/check_b173.sh` 18/18 PASS (12 original B173 + 6 new B173.1).

**Live verified on VM** (build `v1.5.0-alpha1-46-g9bbb750`): `GET /login` returns HTML with all B173.1 markers — `#login-loading-overlay` element + `.login-loading-card` with `<i class="fa-solid fa-spinner fa-spin"></i>` + `<div class="login-loading-text">Вход...</div>`, CSS `position:fixed` + `z-index:9999`, JS IIFE with `f.submit = function(){ showLoading(); ... setTimeout(function(){ nativeSubmit.apply(self, args); }, 60); }` + 3 nav listeners (`pagehide` + `visibilitychange` + `beforeunload`) + `function showLoading` + `overlay.classList.add('visible')`.

---

## Priority 18 — v1.5.2 OIDC readSession uses auth.ParseJWT (SHIPPED 2026-08-25, B174)

### B174 — OIDC session JWT parsing fix (closes the "password reset on login" loop)

**Status**: SHIPPED 2026-08-25 (commit `794b9c68`, v1.5.2-alpha1)
**Effort**: ~0.3 day
**What was delivered**: closes the operator-observed gap from 2026-08-25 (after B172 + B173 + B173.1 shipped): "все равно сбрасывает, после того как браузер предлагает использовать сохраненный пароль до того как вносил правки по поводу next все отрабатывала" — the B173.1 full-page overlay didn't help because the password was being cleared AFTER the form submitted (not during). **Root cause analysis**: `PostLogin` in `internal/feature/auth/service.go:188-195` sets the `skygate_session` cookie to an HS256 JWT (via `auth.IssueJWT`), but the pre-B174 OIDC `readSession` in `internal/oidc/authorize.go:255-287` tried to parse the cookie as a colon-separated `<uid>:<username>:<email>:<expires_unix>` string — a format `PostLogin` NEVER wrote. `readSession` ALWAYS returned nil → the OIDC handler ALWAYS redirected back to `/login?next=...` → the user saw the login page re-render with an empty password (the "сбрасывает" symptom). Pre-B172 the user thought "it worked" because `PostLogin` hard-coded `/dashboard` (the B172 bug) — the user never went back through `/oidc/authorize`, so the broken `readSession` was never exercised. B172 fixed the redirect, which EXPOSED the latent OIDC `readSession` bug. B174 ships:
- `oidc.Service` gets a `JWTSecret string` field (the same secret `feature/auth` uses) + a `UserLookup func(userID int64) (username, email string, err error)` callback that maps the JWT-claim `uid` → DB-side `username + email` (the JWT doesn't carry email; the OIDC `id_token` + `/userinfo` endpoints need it).
- `readSession` delegates to `auth.ParseJWT` (the same helper `feature/auth.PostLogin` uses) to verify the HMAC signature + extract the `uid` + `usr` claims. The pre-B174 colon-split is GONE (dead `parseInt64` helper deleted). If `UserLookup` returns an error (e.g. the user was deleted from `portal_users` after the JWT was issued), `readSession` returns nil — a stale cookie with a valid signature but no live user must NOT proceed to `/oidc/authorize` (otherwise headscale would create a session for a deleted skygate account).
- `cmd/skygate/main.go` passes `app.JWTSecret` to `oidcsvc.NewService` + wires `oidcSvc.UserLookup` via `db.GetUserNameByID` (returns `username + "@skygate.local"` for the email since the `portal_users` table has no email column — a B174.1+ would add one).
- The B161.4 e2e test now issues a REAL JWT (via `auth.IssueJWT`) instead of a mock string. The pre-B174 test bypassed the broken `readSession` with a `X-Test-Session-Cookie-Present` header — that workaround is GONE in B174. The test now asserts `/oidc/authorize` post-login → `https://head.test/oidc/callback?code=...&state=...` (NOT a bounce to `/login`).
- `oidc.NewService` signature extended from 5 to 6 params (added `jwtSecret string`). The 6th param is REQUIRED — a future refactor that drops it will fail the B174 B-check.
- 22 contracts in `scripts/check_b174.sh` (source contract: `JWTSecret` + `UserLookup` fields + `auth.ParseJWT` call + dead `parseInt64` deleted + UserLookup-error handling; wiring contract: `main.go` passes `app.JWTSecret` + sets `UserLookup` + `oidc.NewService` accepts 6 params; test contract: e2e uses real JWT + no mock header + asserts callback URL + `authorize_b174_test.go` exists; regression contract: `TestReadSession_PreB174FormatRejected` pins the pre-B174 colon-separated cookie as REJECTED — an attacker can't forge a session by setting `"1:alice:alice@example.com:9999999999"` as the cookie value; 7 subtests for `TestReadSession_ParsesJWT` covering valid-JWT, no-cookie, empty-cookie, invalid-JWT, expired-JWT, UserLookup-nil, UserLookup-error; build contract: `go build` + `go vet` + `go test ./internal/oidc/...`).

**Why B173 + B173.1 weren't enough**: the B173.1 full-page overlay was a great UX fix (the user could finally SEE that the form was processing) but it didn't address the actual bug. The overlay would show "Вход..." for a moment, then the page would navigate to `/login?next=...` (re-rendered with an empty password), and the user would think "the password was reset on submit". The root cause was a pre-existing OIDC-side bug that the B172 redirect fix EXPOSED — `readSession` couldn't parse the JWT cookie that `PostLogin` was already setting correctly.

**Why the B161.4 e2e test didn't catch it pre-B174**: the test used a `X-Test-Session-Cookie-Present` header to make the OIDC handler recognize a session (the production `readSession` couldn't parse the real JWT cookie). The e2e test PASSED but the production code was BROKEN — a classic case of a test that exercises a test-only path instead of the real production path. B174 removes the test-only header and issues a real JWT, so the test now exercises the same code path the user's browser does.

**Verified locally**: `go build ./...` clean, `go vet ./...` clean, `go test ./...` (38 packages) all PASS, `bash scripts/check_b174.sh` 22/22 PASS.

**Live verified on VM** (build `v1.5.0-alpha1-48-g794b9c6`): full OIDC flow works end-to-end — `/oidc/authorize?client_id=headscale&...` (no session) → 302 → `/login?next=...` → POST `/login` (with `skyadmin` credentials) → 302 → `/oidc/authorize?...` (with the `skygate_session` JWT cookie that PostLogin just set) → **302 → `https://head.skynas.ru/oidc/callback?code=<43 chars>&state=b174-live-test-xyz`** (NOT a bounce back to `/login`). The pre-B174 bug is closed: the user logs in once, the OIDC handshake completes, the device gets registered on headscale.

---

## Priority 19 — v1.5.2 OIDC node auto-tag Strategy E (SHIPPED 2026-08-25, B175)

### B175 — OIDC node auto-tag Strategy E (no more "⏳ pending forever" for OIDC devices)

**Status**: SHIPPED 2026-08-25 (commit `e4e1ac70`, v1.5.2-alpha1)
**Effort**: ~0.3 day
**What was delivered**: closes the operator-observed gap from 2026-08-25: "Проверь что Autoupdater тегов работает при варианте когда происходит добавление не по ключу а через OIDC потому что ожидание тега висит уже больше 5 минут и в будущем каждый раз дергать администратора для обновления неудобно" (check that the tag autoupdater works for OIDC-added devices — the 'pending' state is hanging more than 5 minutes and I don't want to keep asking the admin to fix it every time). Pre-B175 the `node-discovery` autoupdater (B77, runs every 5m by default) had 3 strategies for matching headscale nodes to portal users:
- **A**: `n.PreAuthKeyID == preauth_keys.headscale_preauth_id` (catches /my/preauth flow)
- **C**: `n.CreatedAt` within 1h of a preauth key (temporal fallback for A)
- **D**: existing `tag:dev-<user>-*` tag (post-hoc, after operator manually applied the tag)

None of those fire for an OIDC-registered node — OIDC flow doesn't use a preauth key (`n.PreAuthKeyID == ""`), the OIDC user has no `preauth_keys` row (Strategy C skip), and the node has no tags yet (Strategy D skip). Result pre-B175: the OIDC node stays orphaned in `node_owner_map`, the per-device dev-tag (`tag:dev-<user>-<device>`) is never applied, and `/my/devices` shows the device with "⏳ pending" forever. The operator had to hit "Force backfill" on `/admin/devices` (or run `headscale nodes tag -i <id> -t 'tag:dev-<user>-<skygate-vm>' --force` manually via headscale CLI) to clear it — a UX regression the operator flagged on 2026-08-25. B175 ships:
- New `matchOIDCStrategy(n headscale.NodeView, portalUsername string) (matchedTag string, ok bool)` helper in `internal/nodeownership/nodeownership.go` — returns `("tag:private", true)` for an OIDC node whose `n.UserName == portalUsername`, or `("", false)` otherwise. Guards:
  - `n.PreAuthKeyID != ""` → no match (Strategy A's territory — /my/preauth nodes)
  - `n.UserName != portalUsername` → no match (cross-user safety)
  - `portalUsername == ""` → no match (defensive guard)
  - `len(n.Tags) > 0` → preserve the first existing tag via `firstTagOrFallback` (matches Strategy A/C convention so an operator-applied `tag:subnet-router` isn't clobbered to `tag:private`)
- Backfill now calls `matchOIDCStrategy` as the 4th strategy (after A, C, D), so an OIDC node matches when no preauth key, no temporal correlation, and no existing tag are present. The `otherOwners` check at the top of the per-node loop still filters nodes whose headscale user is a different portal user, so the user-renaming-attack vector is closed.
- The synthetic `tagged-devices` headscale user (id=2147455555 in this deployment) has name="tagged-devices" — no real portal user has that name (UNIQUE constraint on `portal_users.username`), so Strategy E never matches a tagged-devices node.
- 7 unit tests in `internal/nodeownership/strategy_e_b175_test.go` covering the 5 critical paths (OIDC match, preauth-key no-match, username mismatch, tagged-devices synthetic user no-match, empty portalUsername no-match) + firstTagOrFallback preservation + idempotency.
- 16 contracts in `scripts/check_b175.sh` (source contract: 5 checks that `matchOIDCStrategy` exists, is called from Backfill, guards on PreAuthKeyID + UserName, returns the right tags; test contract: 7 subtests + the `MUST NOT match` assertion guard; build contract: `go build` + `go vet` + `go test ./internal/nodeownership/...`).

**What B175 does NOT cover (explicitly out of scope)**:
- **headscale `oidc.claim_map: { sub: "email" }`** — would create OIDC users with name = email local-part, not skygate username; Strategy E would not match. The operator explicitly deferred this to a future feature (B174.1+) since the change is global (adds `email` column to `portal_users`, rewires `UserLookup`, etc.) and would need a full e2e re-verification that nothing breaks or gets lost.
- **headplane-side user display** — headplane shows users by their headscale internal name; that layer is unchanged.
- **Cross-provider OIDC name collision** — if two OIDC providers (e.g. Google + GitHub) both issue a `name` claim that matches a skygate username, headscale creates two distinct headscale users (one per provider_id). Strategy E would match the most-recently-issued user, which is the operator's responsibility to keep unique.

**Verified locally**: `go build ./...` clean, `go vet ./...` clean, `go test ./...` (38 packages) all PASS, `go test ./internal/nodeownership/... -v -run TestMatchOIDCStrategy` 7/7 subtests PASS, `bash scripts/check_b175.sh` 16/16 PASS.

**Live verified on VM** (build `v1.5.0-alpha1-50-ge4e1ac7`): the autoupdater is running (`node-discovery: starting (interval=5m0s)` in stderr) and the Backfill function is being called (`DBG backfill node=35 name=skybars-1 matchedTag=tag:private api_tags=[] hasPrivate=false` + `DBG backfill AddTag called for node=35 (ensure tag:private)`) — the live state confirms B175 is in the binary. Final E2E test (a Tailscale client completes the full OIDC flow) requires the operator to run `tailscale up --login-server https://head.skynas.ru` on a real device; the autoupdater will then auto-apply the dev-tag within 1 tick (≤5 min) of the device appearing in headscale, and `/my/devices` will show "✓ tag:dev-skyadmin-<host> applied" instead of "⏳ pending". The OIDC user (id=86, name=skyadmin, provider=oidc) created by the B174 e2e test is still in headscale waiting for its first device registration.

---

## Priority 20 — v1.5.2 dev-tag lowercase + i18n tooltip rewrite (SHIPPED 2026-08-25, B176 + B175.1)

### B176 — dev-tag lowercase (headscale 0.29 rejects uppercase tags) + B175.1 i18n rewrite

**Status**: SHIPPED 2026-08-25 (commit `66b17a3d`, v1.5.2-alpha1)
**Effort**: ~0.2 day
**What was delivered**: closes the operator-observed gap from 2026-08-25 (right after B175 shipped): "старое отображение информации при навеадении на тег ожидания осталось также не обновил с новым проходом тег обновлятор устройство - не было обновления на VM?" (the old tooltip on the pending dev-tag is still there + the new tag-autoupdater pass didn't update the device on VM?). Two separate issues were diagnosed:
**B176 root cause**: headscale 0.29 rejects tags with uppercase letters. Pre-B176 the dev-tag `tag:dev-<user>-<hostname>` was constructed from the live headscale hostname (e.g. "SkyBars") WITHOUT lowercasing, so headscale silently rejected every AddTag call on a device with an uppercase hostname. The /my/devices UI showed the same uppercase dev-tag as "⏳ pending" because the live headscale never had the tag.
**B176 fix**: `strings.ToLower` at all 6 dev-tag construction sites in the codebase:
- `internal/nodeownership/nodeownership.go:599` (the per-device tag in the backfill's AddTag call)
- `internal/feature/my/devices.go:374, 434` (live + snapshot branches of the /my/devices listing)
- `internal/feature/admin/devices.go:783` (post-transfer dev-tag in `PostAdminNodeTransfer`)
- `internal/acl/acl.go:444, 1259` (the per-device src in the headscale ACL policy file — without this, the policy rule has `src = ["tag:dev-skyadmin-SkyBars"]` which headscale silently ignores because the node carries `tag:dev-skyadmin-skybars`)
**B175.1 fix (same commit, no separate scope)**: rewrote the `devices.dev_tag_pending_help` i18n key in `internal/i18n/catalog_my.go` (RU + EN) to explain (a) the autoupdater ticks every 5 min (B175), (b) ask admin if it persists > 5 min, (c) the most common cause is uppercase letters in the hostname (B176). The pre-B175.1 RU text "следующий /my/devices повторит попытку" was the operator's specific complaint on 2026-08-25.
**Verified locally**: `go build ./...` clean, `go vet ./...` clean, `go test ./...` (38 packages) all PASS, `bash scripts/check_b176.sh` 16/16 PASS.
**Live verified on VM** (build `v1.5.0-alpha1-52-g66b17a3`): `docker exec headscale headscale nodes tag -i 35 -t 'tag:private,tag:dev-skyadmin-skybars' --force` now SUCCEEDS (post-B176) — the same call returned "Error: tag should be lowercase" pre-B176. Node 35 "SkyBars" now has `['tag:dev-skyadmin-skybars', 'tag:private']` on headscale.

**Known remaining gap (NOT B176 — explicitly out of scope)**: nodes that are already on the synthetic "tagged-devices" headscale user (id=2147455555 in this deployment) with no live dev-tag are skipped by all 4 backfill strategies (no preauth key, no temporal match, no live dev-tag, `n.UserName="tagged-devices" ≠ portalUsername`). The DB may have a stale `node_owner_map` row (e.g. node 35's DB row has `hostname="skybars-1"` from a historical snapshot) but the backfill can't reconcile that with the live headscale state. For these legacy nodes the operator must apply the dev-tag manually via `headscale nodes tag --force` (we did this for node 35 during the live-verify). A future B-check (Strategy F: "node on tagged-devices user with a DB row matching portalUsername") would close this, but the typical path going forward is B175 + B176 — Strategy E applies the dev-tag on the FIRST autoupdate tick after device registration, before the node transitions to tagged-devices. The legacy orphan case only happens for nodes registered before B175 shipped.

---

## Priority 1 — Web UI completeness (in progress, 2026-07-30)

**Status**: shipped in v0.32.1 (this commit). All admin + user
pages now have sidebar links.

The `internal/handlers/templates/layout.html` sidebar was
missing 10 admin + 1 user links. They existed as fully-built
routes + handlers + templates, but were unreachable from the
nav. Mapped and added:

| Page | Was missing from sidebar | Now |
|---|---|---|
| /admin/control-planes | ✗ | ✓ `nav.control_planes` |
| /admin/exit-nodes | ✗ | ✓ `nav.exit_nodes_admin` |
| /admin/headscale | ✗ | ✓ `nav.headscale` |
| /admin/headplane | ✗ | ✓ `nav.headplane` |
| /admin/integrations | ✗ | ✓ `nav.integrations` |
| /admin/invites | ✗ | ✓ `nav.invites` |
| /admin/meshes | ✗ | ✓ `nav.meshes_admin` |
| /admin/update | ✗ | ✓ `nav.update` |
| /my/keys | ✗ | ✓ `nav.preauth` |

(/admin/backup/config and /admin/derp/config are sub-pages
reached from their parent /admin/backup and /admin/derp —
no separate sidebar item needed.)

---

## Priority 2 — PostgreSQL cutover (BLOCKED on operator's PG-staging VM)

**Status**: Phase 1 done (v0.31.0 foundation on main), Phases
2-5 blocked on operator providing a PG-staging VM for live
testing. The full plan is at
[`docs/internal/internal/v0.27.0-postgres-ha.md`](internal/v0.27.0-postgres-ha.md) (moved
from the dead `feat/postgres-migration` branch in this commit).

**What's done (Phase 1.1-1.3)**:
- Driver abstraction (`internal/db/driver.go` + `driver_postgres.go`)
- 27 PG-compatible migrations (`internal/db/migrations_pg.go`)
- 4 verification tests (`internal/db/test_pg_migrations_test.go`)
- Non-blocking ALTER helpers (`internal/db/pgmigrate/expand.go`)
- B11 (no destructive DDL) + B12 (helper unit tests) catalog
  checks
- B18: `go build -tags postgres ./cmd/skygate` succeeds

**What's still needed for the cutover**:
- **Placeholder rewrite**: `?` → `$1, $2, ...` in 30+ files
  (~5000 lines). The `rewrite_placeholders.py` script
  exists on the old branch but is not on main; needs to be
  brought in + run + carefully diffed.
- **`INSERT OR REPLACE` / `INSERT OR IGNORE` → `ON CONFLICT`**:
  same scope. ~30 places in `internal/db/queries.go` and
  callers.
- **`strftime('%s', 'now')` etc → `EXTRACT(EPOCH ...)::bigint`**
  and other SQLite-isms in migrations.
- **PG-staging VM**: PostgreSQL 16 on a separate VM, SSH
  access for Mavis, `SKYGATE_TEST_PG_DSN` env.
- **R27 verification**: lock_timeout + 4 roundtrip tests in
  PG-staging.
- **Manual cutover window**: skygate in read-only mode →
  `dump_sqlite.py` → apply to fresh PG → flip
  `SKYGATE_DB_DSN` → restart. ~15 min downtime.

**Operator action required**: provision a PG-staging VM. Until
then this is blocked.

---

## Priority 3 — HA Tier 1 hot standby (BL-2) — UNBLOCKED 2026-08-18, target v1.5.0

**Status**: **UNBLOCKED** as of 2026-08-18. The 2nd VM
(`svyatoslava-1`) is available, S3 bucket configured, Patroni +
etcd cluster in place (`<operator-vm-public-ip>:2379`).

**Locked-in design decisions** (from operator reply 2026-08-18):

| Decision | Value | Source |
|---|---|---|
| Public DNS FQDN | `skygate.<your-domain>` | operator (DNS provider is operator-specific) |
| Active node name | `skygate` (in headscale) | operator — "skygate-prod был confusing" |
| Standby node name | `skygate-standby` | derived |
| Active VM (today) | `svyatoslava-1` (new) | operator — "текуший проект на svyatoslava-1 и будет основным" |
| Standby VM (today) | `192.168.13.69` (current `<operator-public-ip>`) | operator — current VM becomes дублером |
| HA topology | Active-Passive with priority chain (NOT Active-Active) | operator — "стоит делать сразу с учетом приоритета" |
| Starter chain | 2 nodes (P1 + P2) | operator — "Starter chain пока из двух" |
| DNS failover | external DNS provider API (pluggable; B145 ships the adapter interface) | operator — РФ, DNS provider is operator-specific |
| Tailscale Funnel | NO (skipped) | operator — "сеть tailscale скорейвсего не доступна" |
| Cert acquisition | DNS-01 via the configured provider + manual upload fallback | per BL-2 design + operator request |
| Failover policy | auto + manual override | operator — "сделал авто но оставил возможность и в ручную" |
| Auto-reclaim | default OFF (manual "Reclaim primary" button) | anti-flap |
| Patroni config | **NOT touched** | operator — "Зачем мучаться с перенастройкой Patroni" |
| S3 bucket name | `s3://skygate-ha/` (suggested) | consistent with backup infra |
| headplane API key | file-based in `.env`, replicated via S3 deploy/ | operator — "прописывается автоматом" |

**What "HA Tier 1" means (revised for v1.5.0)**:
- RTO < 1 min (Patroni auto-failover + skygate HA role flip)
- RPO = 0 (Patroni async replication is acceptable for the
  household tailnet — synchronous is a separate decision)
- Active-Passive: 2nd VM (skygate-standby) is the warm standby
- etcd cluster for Patroni consensus (already in place)
- external DNS provider failover (A-record flip on role change)
- certsync: S3 ↔ local certs (single source of truth)
- /admin/ha page: chain editor + force promote/demote
- /admin/certificates: upload + DNS-01 toggle
- /admin/deploy: push/pull/promote buttons
- headscale stays on its current SQLite + Litestream
  (no change in v1.5.0)

**What's needed (now unblocked)**:
- ~~2nd VM~~ — DONE (svyatoslava-1)
- ~~etcd cluster~~ — DONE (<operator-vm-public-ip>:2379)
- ~~S3 bucket~~ — DONE (operator-confirmed)
- external DNS provider credentials — **NEEDED** (operator to provide)
- 10 open questions per `docs/internal/ha-v1.5.0-execution.md` §4
- ~3-4 weeks of work per `docs/internal/ha-v1.5.0-execution.md` §3

**Execution tracker**: [`docs/internal/ha-v1.5.0-execution.md`](internal/ha-v1.5.0-execution.md)
(10 phases, 10 open questions, locked decisions log, status updates).

**Note**: the existing Tier 0 (single-VM, daily backups) is
documented in [`docs/disaster-recovery.md`](disaster-recovery.md)
and works. Tier 1 is the "next step" if the operator wants
<1 min RTO.

---

## Priority 4 — Backup polish (not blocked, low priority)

**Status**: backup system fully built and working. Small
improvements remain.

**What's done**:
- `internal/backup/` (6 files, ~33KB Go): runner, scheduler,
  config, mount, schedule, checker
- `deploy/backup.sh` (6.6KB bash): .env + git bundle + skygate.db
  + headscale.db + headscale config + noise_private.key
- `/admin/backup` page: create/restore + Config (SMB/NFS/SFTP
  destination) + Test + Run now + Toggle
- Scheduled runs via in-app scheduler (configurable from UI)
- DR runbook: [`docs/disaster-recovery.md`](disaster-recovery.md)

**What's still on the wishlist**:
- **S3 destination** (currently SMB / NFS / SFTP only).
  Need to add an "S3" protocol option in
  `/admin/backup/config` + a `SKYGATE_BACKUP_S3_BUCKET` env
  var. ~half a day of work.
- **Auto-verify backups**: every Sunday, restore the latest
  backup to a temp dir and run `sqlite3 ... "PRAGMA
  integrity_check;"` on it. Send Telegram alert if it fails.
  This catches silent corruption before DR is needed.
  ~1 day of work.
- **DR doc update**: `docs/disaster-recovery.md` references
  `docs/internal/internal/ha-architecture.md` — that file now exists (as a
  stub in this commit) but the link target's content is
  minimal. May want to inline the relevant context into the
  DR doc itself or flesh out the stub.

---

## Priority 8 — DB corruption incident + recovery (RESOLVED 2026-07-30, v0.32.5)

**Status**: RECOVERED + ROOT CAUSE FIXED. R30 + R31 in
verify_post_deploy.sh catch future corruption + disk-full
early. Recovery uses sqlite3 `.recover` (the REAL fix that
handles the worst case) instead of DROP+CREATE.

**The REAL root cause (corrected 2026-07-30 21:38)**:

The v0.32.4 fix (synchronous=FULL + stop_grace_period +
graceful stop in rebuild_deploy.sh) was a partial fix. It
addresses ONE class of corruption (SIGKILL during deploy),
but the recurring corruption came from a DIFFERENT cause:

**The VM disk hit 100% full.** SQLite's WAL writes fail
silently when there's no free space on the filesystem —
`sqlite3_step()` returns `SQLITE_OK` to the caller but the
actual bytes don't make it to disk, so the btree pages
end up in an inconsistent state. The skygate process keeps
running (the writes "succeed" at the SQLite level), so the
corruption is invisible until a subsequent SELECT triggers
`PRAGMA integrity_check`.

The chain of events:
1. containerd's snapshotter (`/var/lib/containerd/io.containerd.
   snapshotter.v1.overlayfs`) grew to **6.7GB** holding old
   image layers that were never garbage collected.
2. `/var/log/journal` grew to **916MB**.
3. The autoupdate's per-tick INSERT to `exit_rule_logs` + the
   periodic reapply's INSERT to `acl_snapshots` started
   failing silently.
4. Next `PRAGMA integrity_check` (R30) found:
   - "Tree 12 page 7838 cell 334: 2nd reference to page 9775"
   - "Tree 13 page 9868 cell 212: 2nd reference to page 9787"
   - "Rowid 120378 out of order"
   - "database disk image is malformed (11)"

The disk-full → WAL-write-fails → btree-corruption causality
is well-documented in SQLite's docs but not in any error
log we have access to — the call returns OK and the process
keeps running.

**Why DROP+CREATE wasn't enough (corrected 2026-07-30 21:38)**:

The original `recover_db_corruption.sh` (v0.32.4) did
`DROP TABLE IF EXISTS acl_snapshots; CREATE TABLE ...` then
restarted skygate. R30 STILL FAILED after this because:

- DROP+CREATE leaves the OLD corrupted free pages in place.
- When the new tables' first INSERT allocates pages from
  the freelist, those pages have stale corrupted data.
- R30's `PRAGMA integrity_check` then finds the same errors
  on the SAME page numbers as before (Tree 12 page 7838,
  Tree 13 page 9868, etc.).
- Effect: the corruption keeps "recurring" every autoupdate
  tick even though the root cause (disk full) was fixed.

**The REAL fix (v0.32.5)**:

Use `sqlite3 .recover` to extract every salvageable row
into a SQL dump, then rebuild a fresh, clean DB file. The
corrupted free pages never get into the new file because
`.recover` only reads USED pages.

`scripts/recover_db_corruption.sh` now does:
1. **Disk space check FIRST** — if >85% full, prompt the
   operator to free space (`docker system prune -a`,
   `rm -rf /var/backups/skygate/PRE_*`) BEFORE proceeding.
2. Stop the skygate container.
3. Backup the corrupted DB.
4. **`.recover` the DB** in a throwaway `alpine:3.20`
   container (has `sqlite3`; the skygate container doesn't).
5. Filter `CREATE TABLE sqlite_sequence` (reserved name
   that `.recover` includes as data but can't be created).
6. Rebuild a clean DB from the SQL dump.
7. `PRAGMA integrity_check` on the rebuilt DB.
8. Swap the rebuilt DB back into the skygate-data volume
   (chown 1000:1000 for the in-container skygate process).
9. Restart skygate.
10. Trigger `/admin/exit-rules/reapply` to repopulate
    `acl_snapshots` (the last successful ACL is the
    auto-applied policy, not the pre-corruption history).

Verified on production 2026-07-30 21:38: 41MB clean DB
with `integrity_check=ok`, 4 users, 4670 audit_log entries,
372 device_rules (more than the pre-recovery DB had —
some rules were hidden in the corrupted free pages).

**Defensive measures added (v0.32.5)**:
- **R30 in verify_post_deploy.sh**: `PRAGMA integrity_check`
  on a fresh copy of the live DB. The check is non-destructive.
- **R31 in verify_post_deploy.sh**: disk space check. FAIL
  if `df -P /` shows ≥85% used. Catches the disk-full
  cause before the corruption happens.
- **scripts/recover_db_corruption.sh**: now uses `.recover`
  (the real fix), not DROP+CREATE.
- **scripts/_recover_helper.sh + _swap_recovered.sh**:
  helper scripts that run in the throwaway container.

**Why the v0.32.4 fixes (synchronous=FULL, stop_grace_period)
are STILL valuable**:
- `synchronous=FULL` is the textbook durability setting
  for serious SQLite deployments. Every commit is fsync'd
  before the call returns. This is what every SQLite user
  with a real workload should have.
- `stop_grace_period: 30s` + `/healthz`-based healthcheck
  gives docker time to send SIGTERM and let Go's `db.Close()`
  flush the WAL one last time before SIGKILL.
- These protect against the deploy-time SIGKILL class of
  corruption. They DON'T protect against disk-full (which
  silently fails at the syscall level) — that's R31's job.

**Follow-up (NOT done in this session, tracked here)**:
- Add a `scripts/monitor_disk.sh` cron that runs `df -h /`
  every 6h and dispatches a Telegram alert when the disk
  hits 75% / 85% / 95%. (Currently the only signal is
  the operator noticing when verify-post FAILs on R31.)
- Set up automated daily SQLite backup to
  `/var/backups/skygate/` so the next corruption can be
  restored instead of dropped. (Existing `deploy/backup.sh`
  is for the skygate+headscale+skygate-host-1 data, not the
  SQLite specifically — needs a separate dedicated script.)
- Investigate WHY containerd's overlayfs grew to 6.7GB
  without being garbage collected. Probably needs
  `docker builder prune -a` (we did this in the recovery)
  + maybe a `prune` cron.

**How to recover if this happens again**:
```bash
# 1. Free disk space FIRST (the cause)
ssh admin@192.0.2.1 'df -h /'
ssh admin@192.0.2.1 'sudo docker system prune -a -f'
ssh admin@192.0.2.1 'sudo rm -rf /var/backups/skygate/PRE_RECOVER_*'

# 2. Run the recovery (the fix)
bash scripts/recover_db_corruption.sh
# It will:
#   - stop skygate
#   - backup the corrupted DB
#   - .recover + rebuild clean DB
#   - swap into the volume
#   - restart skygate
#   - trigger /admin/exit-rules/reapply
#
# Expected: R30 PASS, R31 PASS on next verify-post.

# 3. Verify
bash scripts/verify_post_deploy.sh
```

---

## Priority 7 — Auto-update opt-in + manual Push button (SHIPPED in v0.32.3, 2026-07-30)

**Status**: shipped. `SKYGATE_AUTO_UPDATE_ENABLED` env var
(default `false`) gates the banner-driven "Apply" button
on /admin/update. New "Push update" button is always
visible and ALWAYS works (manual trigger). Plus 6 unit
tests for the skygate-vs-headscale drift detection
(computeSyncStatus).

**What's done**:
- `internal/config/config.go` — new `AutoUpdateEnabled`
  field, `SKYGATE_AUTO_UPDATE_ENABLED` env var, default
  `false`
- `internal/feature/admin/update.go` — new
  `PostAdminUpdatePush` handler + i18n keys
  `update.push` / `update.push_help` / `update.push_confirm`
  / `update.auto_disabled_banner` / `update.auto_enabled_banner`
- `internal/handlers/templates/admin/update.html` — new
  "Push update" button (always visible, separated from the
  gated "Apply" button) + a banner that shows the current
  mode (auto-update on/off)
- `cmd/skygate/main.go` — new route
  `POST /admin/update/push`
- `internal/feature/admin/exit_nodes.go` — extracted
  `computeSyncStatus()` pure function (was inline in the
  handler loop)
- `internal/feature/admin/exit_nodes_test.go` — 6 unit
  tests: TestComputeSyncStatus_EmptyExpected /
  _Synced / _Mismatch / _MismatchReversed /
  _OtherNodesIgnored + TestSeedNodeRulesAndReadExpected
- `scripts/verify_post_deploy.sh` — new R29 check for
  skygate-vs-headscale drift (currently WARN, not FAIL —
  the page warning is the primary signal)

**Motivation**: operator's correction — auto-update
should be opt-in, not opt-out. The "Apply" button
(banner-driven) is gated by the flag; the "Push" button
(manual) always works.

---

## Priority 6 — ACL perf + route correctness tests (SHIPPED in v0.32.2, 2026-07-30)

**Status**: shipped. Build-time B19 + runtime R28 added to the
verify catalog. 6 functional tests + 4 benchmarks live in
`internal/acl/perf_test.go`.

**Motivation**: operator reported "exit-node routing started
working slower" after a series of small refactors. The actual
root cause was likely the v0.32.0 via: sync bug (fixed in
63cd0ed), but the operator wanted permanent regression guards
so the next refactor can't silently break the same thing.

**What's covered**:

| Test | Catches |
|---|---|
| `TestGenerateACL_SizeWithinBound` | Policy bloat — 100 rules must stay <50KB |
| `TestGenerateACL_NoDuplicateHosts` | headscale 0.29.2 "host already defined" reject |
| `TestGenerateACL_FirstMatchOrdering` | v0.12.0.1 inter-user leak regression |
| `TestGenerateACL_ViaHonoredWhenEnabled` | v0.32.0 via: sync bug regression |
| `TestGenerateACL_ViaOmittedWhenDisabled` | "always-on via" opt-in broken regression |
| `TestGenerateACL_AllTagsInTagOwners` | headscale "tag not found" reject |
| `BenchmarkGenerateACL_Small` (10 rules) | baseline ~200µs |
| `BenchmarkGenerateACL_Medium` (100 rules) | prod target ~600µs |
| `BenchmarkGenerateACL_Large` (1000 rules) | stress: <5ms (currently ~2.3ms) |
| `BenchmarkGenerateACL_ViaEnabled` (10 users × 50 rules, via=1) | via emission perf |

Plus R28 in `verify_post_deploy.sh`:
- Live policy size < 100KB
- Live grant count < 500
- Live host count < 2000

**Operator action**: none — tests are passive guards. Run
`go test -bench=BenchmarkGenerateACL -run=^$ ./internal/acl/`
locally before any ACL refactor to capture baseline numbers.

---

## Priority 5 — Other deferred items (long-tail, no current demand)

These are explicitly NOT in active scope but tracked here so
they don't get lost:

### v0.19.1 — `exitnode.skygate-subnet-<user>` DNS records
Re-attempt of the reverted v0.19.0. v0.18.0 already provides
per-user MagicDNS; this is the "named record per user → their
chosen exit-node" step. **Blocked on headscale 0.30+** which
adds `dns.extra_records` policy support (0.29.2 doesn't have it
and rejects the policy). Re-enable when headscale 0.30 ships.

### v0.23.1 Phase 2 — safe user migration (compliance tier)
The v0.23.0 one-click per-user headscale provisioning ships
infrastructure but no data migration. Phase 2 would take a
user off the global headscale, move their nodes + ACL to the
per-user plane, flip the DB override. **Compliance tier only**
(SOX / multi-tenant SaaS / geographic isolation). Deferred
until a real operator need lands.

### ~2850 lines of testutil.go stubs
`internal/feature/*/testutil.go` has ~2850 lines of helpers
that were written during the refactor-v0.30 work but aren't
exercised by any test (infra without contracts). Low ROI to
backfill; will happen naturally as new features are added.

### B161.4 — headscale.conf snippet + e2e verification (SHIPPED 2026-08-24, v1.5.1)

**Status**: SHIPPED + DEPLOYED. The OIDC block
(B161.1+2+3) was the skygate-side; B161.4 is
the operator-side runbook that wires headscale
to it. The deliverable is documentation + a
B-check, not new skygate code (headscale.conf
lives on the headscale host, not skygate's
codebase).

**What's in B161.4**:
- `docs/internal/oidc-headscale.md` (~13 KB) — the
  operator runbook with the headscale.conf `oidc:`
  block snippet (YAML, with `automatic_authorization:
  true` for one-click UX), the 4 must-match values
  table (SKYGATE_OIDC_ISSUER ↔ oidc.issuer,
  SKYGATE_OIDC_CLIENT_ID ↔ oidc.client_id,
  SKYGATE_OIDC_CLIENT_SECRET ↔ oidc.client_secret,
  SKYGATE_OIDC_REDIRECT_URIS ↔ oidc.redirect_url),
  3-step smoke test (discovery + JWKS + /authorize)
  with expected JSON shapes, common e2e failures
  table (issuer mismatch, client_id mismatch,
  redirect_uri mismatch, kid not in JWKS, etc.) so
  the operator can self-diagnose when the first
  Tailscale client shows "authentication failed",
  `curl`-based drive-the-flow-yourself section
  for verification without a real Tailscale device,
  and 5 reusable lessons (4 must-match values, the
  discovery doc as the OIDC "heartbeat", the
  redirect_uri byte-for-byte match rule,
  automatic_authorization as the one-click UX,
  Tailscale's auth flow being server-driven).
- `scripts/check_b161_4.sh` (10 contracts A-D) —
  A: source contract (snippet exists + 4 must-match
  env vars + 3 smoke-test commands + common-failures
  table + automatic_authorization in the snippet).
  B: live endpoint smoke test (curls the 4 OIDC
  endpoints on the live VM, verifies field presence
  + JWKS key count + 302 vs 400 status codes). SKIPs
  (not FAILs) on a fresh deploy where
  SKYGATE_OIDC_ISSUER is unset. C: system test
  stub (soft warning if the operator wants to add
  a `headscale.oidc.*` test to the /admin/system_tests
  page). D: build + vet clean.

**Operator action** (the B161.4 deliverable
includes a step-by-step, but it boils down to):
1. Generate `client_secret`: `openssl rand -base64 32`
2. Store the same value in BOTH places:
   - `/etc/headscale/config.yaml` (the
     `oidc.client_secret` field)
   - `/etc/skygate-secrets/` on the skygate host
     (`SKYGATE_OIDC_CLIENT_SECRET` env var)
3. Add the `oidc:` block to headscale.conf with
   the 4 must-match values pointing at the
   operator's real headscale + skygate URLs
4. `docker restart headscale`
5. Run the 3-step smoke test (B161.4 §2 in the doc)
   to confirm discovery + JWKS + /authorize are
   reachable
6. Attach a real Tailscale client (macOS / Windows /
   iOS / Android with "Use custom coordination server")
   and run through the full flow once

**Reusable lessons** (for any future "wire skygate
to external X" integration):
- Always produce a 4-values-must-match table.
  The 90% of OIDC integration failures are typos
  in 1 of 4 env vars / config fields.
- Always produce a "common e2e failures" table.
  The operator will hit at least one of these on
  the first real Tailscale client; having the fix
  on the same page is the difference between "I can
  fix it" and "I have to call you".
- The 3-step smoke test is a guard against
  "deploy + pray". discovery + JWKS + /authorize
  reachable = IdP is alive. Anything past that is
  a headscale-side issue.
- The redirect_uri match is exact-string per
  RFC 6749 §3.1.2 (no wildcard, no substring).
  B161.2 already enforces this on skygate side.
  A trailing slash or query param = 400.
- Tailscale's auth flow is server-driven: the
  Tailscale client just opens the browser to the
  login-server URL. Everything else is headscale
  ↔ skygate. The Tailscale client has no idea OIDC
  is involved — it just sees a successful auth.

### Web UI refactoring + admin pages grouping (Priority 9, deferred)

**Recorded 2026-08-10 after v0.33.1.40 — `/admin/services`**
**made the admin sidebar hit 23 entries. The current nav**
**is unmaintainable: pages overlap conceptually, the**
**operator can't find anything, and info density is too**
**low (each page is a "silo" with its own status badges).**

**Current admin pages (23 total)** — proposed grouping into
6 logical sections:

1. **Devices & Nodes** (4 pages — user/device management)
   - /admin/devices
   - /admin/nodes/{id}/tag
   - /admin/nodes/{id}/untag
   - /admin/nodes/{id}/meta
   - /admin/devices/sync-from-headscale
   - /admin/devices/force-backfill-tags
   - /admin/devices/transfer
   - /admin/users/{id}/subnet/*
   - /admin/subnets
   - /admin/users/{id}/subnet/download

2. **Access Control** (3 pages — headscale ACL/permissions)
   - /admin/acls
   - /admin/acls/export
   - /admin/acls/import
   - /admin/headscale/acl
   - /admin/headscale/acl/add
   - /admin/headscale/acl/remove

3. **System Health & Logs** (4 pages — operational status)
   - /admin/services (B92 — integration status board, NEW)
   - /admin/system_tests
   - /admin/audit
   - /admin/update

4. **Integrations** (6 pages — external services)
   - /admin/integrations
   - /admin/telegram
   - /admin/tailscale
   - /admin/headscale
   - /admin/headplane
   - /admin/derp
   - /admin/derp/config

5. **Data** (3 pages — persistence)
   - /admin/backup
   - /admin/backup/*
   - /admin/devices/sync-from-headscale (cross-listed with #1)

6. **Settings & Users** (4 pages — global config)
   - /admin/settings
   - /admin/users
   - /admin/control-planes
   - /admin/users/{id}/plane

**Proposed UX changes** (deferred, not on roadmap):

a) **Accordion sidebar**: group the 6 sections into
   collapsible nav groups on the LEFT side of every
   admin page. Today every page is a flat list at the top.
   Single page load with the active group pre-expanded.
   Operator can collapse unused sections.

b) **Status badges on nav items**: each section's header
   shows a colored dot (green/yellow/red) based on
   whether all its pages are healthy:
   - Devices & Nodes: green if no per-user device carries
     a conflicting tag (B26 / R26), red if any
   - System Health: green if all /admin/services
     integrations are ok
   - Access Control: green if /admin/headscale/acl page
     renders + skygate ACL table is reachable (R31)
   - Integrations: green if B92 snapshot shows all
     configured integrations ok
   This makes the operator's "is everything ok?" glance
   possible WITHOUT opening any page.

c) **Consolidate `/admin/headscale` + `/admin/headplane`**
   into one "Control plane" section with a tabbed
   sub-page (current /admin/headscale content + headplane
   URL config + headscale-update-monitor history). Today
   they look like two separate things, but they're both
   "the headscale side of skygate". A single page with
   sub-tabs reduces nav clutter and the operator's mental
   model is simpler.

d) **Info density on detail pages**: today /admin/devices
   has 1 row per device, columns = hostname / OS / type /
   tags / owner / last seen / action. A redesign could
   collapse OS+type+last_seen into a single "info" cell
   with a tooltip on hover, freeing a column for a
   1-line "node status" badge (online/offline/stale).
   Same trick for /admin/exit-nodes (collapse "advertised
   routes" + "used routes" into one chip).

e) **Inline action confirmation**: many admin actions
   (delete user, force-backfill, ACL remove) use a
   "confirm=yes" checkbox pattern. A modern UX would
   use a small modal or inline "press Cmd+Enter to
   confirm" pattern. Lower priority — the current
   pattern works, just ugly.

**Why deferred**:
- ~3-4 days of frontend work for items (a) and (b)
- Touches every admin page (each page's nav sidebar)
- Risk of breaking existing operator muscle memory
- Not blocking: operators still navigate by URL or grep
  the nav, the current UX is functional even if cluttered
- Better done AFTER v0.33.1.41 (infra user) lands, so
  the B92 status board + new infra-section page can be
  factored in from the start

**Out of scope for v0.33.1.40–v0.33.1.42** (operator's
priorities are correctness, not UX polish). When
prioritized: open a refactor-v0.34 plan doc in
`docs/plans/`.

### Unmerged branches
- `feature/telegram-bot-ux` (4dca972) — SetMyCommands polish.
  Low value, can be deleted.
- `feat/postgres-migration` (cdec261) — replaced by
  `feat/v0.31.0-pg-foundation` which is on main.

---

## Adding items to this backlog

When you (operator) want a feature tracked, add it here with:
- What it is (one paragraph)
- Why it's blocked (if applicable)
- What the operator needs to provide to unblock
- The design doc / commit / branch where the work lives (if any)
- Priority number

When the work is done, move the entry to the "Completed" section
at the bottom of this file with the commit hash.

## Completed (moved out of backlog)

- **2026-08-03**: v0.32.18 — Subnet-router Remove handler
  (`POST /admin/users/{id}/subnet/remove`). Full lifecycle
  inverse of the v0.16.7 Provision flow. Idempotent, headscale
  delete failure tolerated (DB is source of truth), ACL NOT
  re-applied. 3 new tests + 9 i18n keys × 2 langs. B35 regression
  guard. Commit `3817e44`.
- **2026-08-03**: v0.32.17 — Exit-node monitor online detection
  bug fix. Was overriding `n.Online=true` to false on stale
  `last_seen`. Now trusts headscale's `n.Online` as primary
  signal; `last_seen` only as forgiving fallback when headscale
  says offline. Plus device_rules dedup: 365 duplicate rows in
  prod DB removed (was inflating `computeSyncStatus` mismatch).
  B34 regression guard. Commit `0b05a89`.
- **2026-08-03**: v0.32.16 — Headplane distroless healthcheck
  fix. `ghcr.io/tale/headplane:0.6.3` has no shell/wget/curl.
  `test: wget` healthcheck always failed. Switched to
  `/nodejs/bin/node -e "require('http').get(...)"` with
  `${HEADPLANE_SERVER__PORT}`. B33 regression guard. Commit
  `4e123f4`.
- **2026-08-03**: v0.32.15 — Tailscale OFF by default. v0.32.8's
  hung-entrypoint bug (empty `secrets/ts_authkey` file → `tailscale
  up --authkey=` waits for stdin forever) and v0.32.11's
  bind-mount shadowing bug were fixed in one go by gating
  tailscaled on `SKYGATE_TS_AUTHKEY_FILE` non-empty AND removing
  the literal `TS_AUTHKEY_FILE` env var from compose. The
  `secrets:` mount is also gated to not appear in compose unless
  the file is non-empty. Re-enabling Tailscale requires 3 manual
  steps (provision preauth, write file, un-gate env+mount).
  Commit `0f03c3a`.
- **2026-08-03**: v0.32.14 — CASCADE-LOCK fix. `SetMaxOpenConns(1)`
  + `synchronous=FULL` was causing /admin/exit-nodes to hang under
  concurrent load. Changed to `MaxOpenConns(15)`, `MaxIdleConns(5)`,
  `synchronous=NORMAL`, `busy_timeout=2s`. The original v0.32.4
  corruption was caused by disk-full, not by missing FULL sync;
  the FULL setting was a red herring that also killed throughput.
  The v0.32.8/11 background-job shutdowns and the v0.32.13
  `goroutine+select+2s` timeouts are the layering on top of this
  root fix. Commit `0705a34`.
- **2026-08-03**: v0.32.13 — Exit-nodes 504 timeout fix
  (5 layered bugs). B28 env-var-gates-goroutine pattern for
  exit-node-monitor. B29 ListAllNodes 2s timeout. B30
  ensureExitServers 2s timeout. v0.32.13 wrapped
  `db.ListExitServers` in 2s timeout. B31 cascade-lock
  (the v0.32.14 fix above). B32 hung-entrypoint
  (the v0.32.15 fix above). Commits `3d066ba`, `a91fdd7`,
  `10be5b9`, `6514e65`, `a5fffb2`.
- **2026-07-30**: v0.32.2 — ACL perf + route correctness tests.
  6 functional tests + 4 benchmarks in `internal/acl/perf_test.go`.
  Build-time B19 + runtime R28 added to the verify catalog.
  Commit in this change.
- **2026-07-30**: v0.32.1 — Sidebar completeness. 9 admin +
  1 user pages added to `layout.html`. Commit in this change.
- **2026-07-30**: v0.32.0 — Released. Build `v0.32.0-5-ge4dea76`.
  Per-device OS + type markers + via: sync bug fix + refactor-v0.30.
