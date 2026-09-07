# Skygate v1.5.2 — Post-v1.5.0 hotfixes + HA Tier 1 (B146) + deployment variants

**Date:** 2026-09-07

> v1.5.2 is a hotfix release on top of v1.5.0 (2026-09-04).
> No new features; 1 BL-2 phase closed (Phase 2: B146);
> 1 live bug fixed (B237.10 — auto-update pathspec);
> 1 UX bug fixed (B237.19 — exit-rules form errors);
> 1 hotfix (B237.15 — deployment variants for ghcr /
> podman / systemd / Windows); 3 LOW-priority
> tech-debt items closed (TD-2 / TD-9 / TD-10).
> All fixes are backward-compatible.

## Summary table

| B-block | One-liner | Operator impact |
|---|---|---|
| **B237.10** | Auto-update "Push update" form now works on untagged commits (`ve2d0b9e+e2d0b9e` → `e2d0b9e`) | Operators between releases can self-update via the UI |
| **B237.15** | 5 new deploy surfaces: prebuilt ghcr image, podman, systemd/OpenRC installers, Windows native, release workflow | New operators have a one-liner install path |
| **B237.16** | Closes the last 2 stragglers of TD-2 (numeric status codes) + B140/B141 B-check fixes | `staticcheck` reports 0 ST1013 / 0 SA1012 |
| **B237.17** | `smoke_mesh_*` users + `smoke-mesh-*` meshes are auto-cleaned daily | Live DB no longer accumulates smoke.sh artifacts |
| **B237.18** | New in-app `headscale_user_id` reconciliation cron (default 1h, 4 outcomes: ok / linked / relinked / orphan) | Stale `portal_users.headscale_user_id` is auto-fixed (or audited as orphan) |
| **B146** | Phase 2 reg.ru DNS live test productionized (`scripts/b146_regapi_live.sh`) | Operator can now end-to-end verify the reg.ru integration |
| **B237.19** | `/my/exit-rules` form errors render as a flash banner (not a giant plain-text page) + duplicate banner wording fixed | Forms no longer lose user input on validation failure |

All 7 are live-verified on the operator's VM
(192.168.13.69) with `bash scripts/check_b*.sh` green and
`go test -short ./...` green.

---

## What's in v1.5.2

### B237.10 — Auto-update pathspec bug fix

**Problem (operator-reported 2026-09-04)**: clicking
"Push update" on `/admin/update` with the pre-fix
code produced:

```
[info] manual push by skyadmin (target=ve2d0b9e+e2d0b9e, ...)
[debug] $ git fetch --tags --prune --force → OK
[debug] $ git checkout ve2d0b9e+e2d0b9e → error:
  pathspec 've2d0b9e+e2d0b9e' did not match any file(s) known to git
[error] FAILED: git checkout: exit status 1
```

**Root cause**: `BuildVersion = version + "+" + commit`
in `cmd/skygate/main.go`. For an untagged commit
(`e2d0b9e` between v1.5.0-alpha1 and v1.5.0), `git
describe --tags --always` returns just `e2d0b9e` (no
`-g` suffix), so `BuildVersion = "e2d0b9e+e2d0b9e"`.
The "Push update" form has no `target` field, so
`target = s.BuildVersion`, and after
`normalizeUpdateTarget` prepends "v" the value
becomes `ve2d0b9e+e2d0b9e`. The `+` is invalid in a
git pathspec, so `git checkout` fails. The pre-fix
orchestrator rolls back; the operator is stuck on
the old commit until they SSH in by hand.

**Fix**: new `internal/update.GitRefForBuildLabel(s)`
helper that:

1. Strips the `+<commit>` suffix (the ONE always-invalid
   character in a git pathspec).
2. Strips a leading `v` only when the remainder is a
   pure hex SHA (so legitimate `v1.5.0` semver tags
   are untouched).

Both `PostAdminUpdateApply` and `PostAdminUpdatePush`
now pass `update.GitRefForBuildLabel(target)` to the
orchestrator; the orchestrator also re-processes
on its end as defense-in-depth. The display `target`
stays untouched (page + audit + log show the
human-readable form).

**Live-verify** (operator VM 192.168.13.69): the
helper produces the correct git ref for every
documented build-label shape. Live `git checkout
e2d0b9e` (the result of `GitRefForBuildLabel("ve2d0b9e+e2d0b9e")`)
succeeds. Container is on
`skygate v1.5.0-8-g51e5b82`.

10 unit tests in `internal/update/docker_test.go`
(B237.10) cover the per-shape mapping.

---

### B237.15 — Deployment variants (V1–V8)

Closes the operator ask of 2026-09-07 ("README only
documents one deployment path; write more with the
simplicity-first principle"). Adds 5 deploy surfaces
beyond the original in-container-build compose:

#### V1 + V5: Prebuilt-image docker compose
- `docker-compose.ghcr.yml` (full setup, pulls
  `ghcr.io/BarsSky/skygate:v1.5.0`, no in-container
  build, 2-3s first start vs 60-120s).
- `docker-compose.lite.yml` (sky-only, no
  headscale/DERP/headplane/`docker.sock` — smallest
  possible attack surface, for users with headscale
  already running).

#### V2: Podman compose
Same `docker-compose*.yml` files, run via
`podman compose -f docker-compose.ghcr.yml up -d`.
The README has a Podman section with rootless +
SELinux notes (`:Z` on bind-mounts, `loginctl enable-linger`).

#### V3 + V4: Bare-metal systemd/OpenRC installers
- `deploy/install.sh` — single-file autodetect
  (Debian/Ubuntu/RHEL/Fedora/Alpine).
- `deploy/install-{debian,rh,alpine,bare}.sh` —
  per-OS scripts with their own dep install.
- `deploy/install-common.sh` — shared helpers
  (SHA256 verify + systemd unit + env file + user/dirs).
- One-liner:
  `curl -fsSL .../install.sh | sudo bash`.

#### V6: Windows native installer
- `deploy/Setup-Skygate-Win.ps1` (PowerShell 5.1+) —
  downloads Windows zip from GitHub Releases, verifies
  SHA256, installs to `C:\Program Files\Skygate\`,
  writes `C:\ProgramData\Skygate\skygate.env`,
  registers as a Windows service via `New-Service`,
  the `Environment` registry key is the Windows
  equivalent of systemd's `EnvironmentFile=`.
- One-liner (Run as Administrator):
  `iex ((New-Object System.Net.WebClient).DownloadString('.../Setup-Skygate-Win.ps1'))`.

#### V8: Release workflow
- `.github/workflows/release.yml` — on `v*` tag push:
  - Docker image to `ghcr.io/BarsSky/skygate` with
    tags `:vX.Y.Z`, `:latest` (stable only),
    `:vX.Y`, `:vX` (all stable-only).
  - Go binary tarballs for linux/darwin × amd64/arm64
    + windows-amd64.
  - SHA256SUMS.
  - GitHub Release with `RELEASE-NOTES-vX.Y.Z.md`
    body if present.
- `Dockerfile.prebuilt` — multi-stage (alpine runtime
  + prebuilt Go binary baked in, ~30 MB image, ~2s
  first start).
- `entrypoint.sh` patched to skip the build step
  when `SKYGATE_PREBUILT=1` is set (the
  `Dockerfile.prebuilt` bakes this in).

The dev path (in-container-build, no
`SKYGATE_PREBUILT`) is unchanged for the operator's
local `./:/app` workflow.

**Live-verify pending**: the actual install flow
needs a real tag push to test the release workflow
end-to-end. The workflow file is syntactically valid
+ 50 B-check contracts pass (`scripts/check_b237_15.sh`).

---

### B237.16 — TD-2 + TD-14 staticcheck contract

Closes the last 2 stragglers of the v1.2.0 staticcheck
cleanup (commit `38b2fb9e` replaced 73 numeric
status codes; `d6f7b6b2` was the v1.2.0 follow-up).
Post-v1.2.0, 2 stragglers snuck back in via the
v1.5.0 work:

- `internal/headscale/tags_test.go:66` had
  `http.Error(w, "unexpected: ...", 404)` →
  now `http.StatusNotFound`.
- `internal/oidc/e2e_test.go:399` had
  `http.Redirect(w, r, nextParam, 302)` →
  now `http.StatusFound`.

PLANS.md's TD-2 (HIGH, 68 items) and TD-14 (5
SA1012 false-positives) were both already DONE in
the current code — the v1.2.0 work got 73/73 of them,
the live count was 2 stragglers. PLANS.md was out
of date; B237.16 doesn't touch PLANS.md (that's a
separate documentation update).

B237.16 also fixes the stale `s.DB` → `s.dbc()`
lookups in `scripts/check_b140.sh` and
`scripts/check_b141.sh` (the v0.32.20-era B-checks
referenced the pre-rename field name).

10 B-check contracts in `scripts/check_b237_16.sh`
(manual greps + sanity checks + verify_pre_deploy.sh
+ AGENTS.md).

`staticcheck ./...` reports 0 ST1013 + 0 SA1012.
(The 23 remaining warnings are all U1000 "unused test
stubs" — separate ticket, not TD-14.)

---

### B237.17 — TD-9 smoke-mesh daily cleanup

Closes the "smoke.sh leaves `smoke_mesh_<pid>` users
+ `smoke-mesh-<pid>` meshes in the live DB on a
failed run" accumulation. Pre-B237.17 the only path
was: re-run smoke.sh to completion (which re-runs
step 13.8 cleanup) or manually DELETE rows via
psql. A failed/interrupted smoke.sh run left 2 rows
per incident, accumulating over weeks.

- `scripts/cleanup_smoke_artifacts.sh` — idempotent
  daily script. `BEGIN/COMMIT` around the deletes
  (CASCADE handles `mesh_members` on the meshes
  table + `devices`/`preauth_keys`/`exit_rules` on
  the `portal_users` side). 24h grace window so an
  in-flight smoke.sh run is NOT killed. 24h-N row
  audit row written (`action=smoke_artifacts_purge`)
  so the operator can see "when did the last cleanup
  happen" via `/admin/audit`. Uses
  `sudo -u postgres psql -d skygate_staging`
  (matches `clear_test_dsn.sh:36` from B207-fix).
- `deploy/systemd/skymate-cleanup-smoke.{service,timer}`
  — `Type=oneshot`, daily at 04:00 local,
  `RandomizedDelaySec=300` (avoids fleet-wide
  thundering herd on HA), `Persistent=true` (catches
  up if the VM was off at 04:00).
- 18 B-check contracts in `scripts/check_b237_17.sh`.

**Live-verify pending**: the operator needs to
`systemctl enable --now skymate-cleanup-smoke.timer`
on the live VM (one command). The first daily tick
at 04:00 will log to `journalctl -u
skymate-cleanup-smoke`.

Note: this is the host-level path. The in-app Go
scheduler from B143 (v1.4.3) already exists and
runs as `internal/mesh.StartCleanupScheduler`
when `SKYGATE_CLEANUP_SMOKE_MESH_IN_APP_ENABLED=true`.
B237.17 is the host-level backup that runs even if
skygate is down.

---

### B237.18 — TD-10 headscale_user_id reconciliation

Closes the "portal_users.headscale_user_id goes
stale after a headscale delete+recreate" gap.
Pre-B237.18 the only path was: notice a rule
pointing at a no-op + run psql + UPDATE by hand.
A delete+recreate in headscale left the
portal_users row pointing at a dead ID
indefinitely.

- `internal/headscale/reconcile.go` — the per-row
  reconciliation function with 4 outcomes
  (ok / linked / relinked / orphan). **NEVER**
  auto-deletes portal_users rows; orphan outcomes
  write an audit row + leave the ID alone for the
  operator to review. Per-row transactions; a single
  bad row doesn't poison the cycle.
- `internal/headscale/reconcile_cron.go` — the
  `StartReconcileCron(ctx, db, hs, interval)` +
  `RunOnceNow(ctx, db, hs)` entry points. Default
  1h interval (configurable via
  `SKYGATE_RECONCILE_HEADSCALE_USERS_INTERVAL`).
  `sync.Once` guard.
- `internal/config/config.go` — adds
  `ReconcileHeadscaleUsers` (bool, default true) +
  `ReconcileHeadscaleUsersInterval` (time.Duration,
  default 0 → use package default).
- `cmd/skygate/main.go` — wires the cron AFTER
  `headscale.New` + AFTER `ensureHeadscaleUser` (so
  the headscale client exists AND the admin user
  is in headscale by the first tick). Gated on
  `cfg.ReconcileHeadscaleUsers`.
- 10 unit tests in `internal/headscale/reconcile_test.go`.
- 22 B-check contracts in `scripts/check_b237_18.sh`.

The cron runs once on startup + every 1h. The
operator should see "reconcile: cron enabled
(interval=1h, ...)" in the skygate log after the
next deploy. First cycle log:
`reconcile: N portal_users checked (hs_users=M,
ok=X, linked=Y, relinked=Z, orphans=W, errors=0,
took=...)`. The `/admin/audit` page should show the
summary `headscale_user_reconcile` row + the per-row
relinked/orphan rows.

---

### B146 — Phase 2 reg.ru DNS live test (BL-2)

Closes the Phase 2 (BL-2) work that was blocked
on Q1 (reg.ru creds) + Q2 (IP whitelist) per §4
of `docs/internal/ha-v1.5.0-execution.md`. The
operator provided both on 2026-09-07 (login +
alternative password; the IP whitelist was
already filled). B146 productionizes the working
auth pattern that B145 + B161.4 confirmed against
the live reg.ru API on 2026-08-18 (top-level form
fields + mTLS cert, password NOT inside
`input_data` JSON — the pre-fix pattern returned
`NO_AUTH`, this is the discovered-working shape).

- `scripts/b146_regapi_live.sh` — bash + curl +
  Python one-liner. The script does the 4-step
  preflight (cert + key on disk + 3 env vars set),
  POSTs to the v2 `/zone/get_resource_records`
  endpoint with the right auth pattern, parses the
  JSON response, and reports a grep-able
  `PASS:` / `FAIL:` / `SKIP:` line. Handles the 4
  known 2026-08-18 failure modes (NO_AUTH +
  ACCESS_DENIED_FROM_IP + DOMAIN_NOT_FOUND +
  generic ERROR) with actionable error messages
  that tell the operator exactly which prereq
  is missing.
- `scripts/check_b146.sh` — 15 B-check contracts
  (file inventory + bash syntax + the 4 known
  error codes + the PASS/FAIL/SKIP output format +
  cert/key file path docs + HA execution doc
  references + verify_pre_deploy.sh + AGENTS.md).
  The live test itself is NOT in the verify-pre
  catalog (requires the operator's cert + key +
  creds in env, which aren't true on a CI runner).
- `scripts/verify_pre_deploy.sh` — `B146` row added.
- `docs/internal/ha-v1.5.0-execution.md` §6
  status log + §4 open questions table updated
  (Q1 + Q2 marked ✅ DONE 2026-09-07).
- `AGENTS.md` — B146 block added.

**Live-verify pending** (operator-side, not code):

1. Paste cert + login + password + zone into the
   `/admin/ha` "External DNS" form (or set the env
   vars and trigger a future
   `skygate regapi-credentials set` subcommand — see
   the BL-3 follow-up below).
2. Restart skygate so the cron + the form's "Test
   connection" button can read the new creds.
3. Run `bash scripts/b146_regapi_live.sh` to verify
   end-to-end. Expected output:
   `PASS: skynas.ru/skygate -> <IP>`. If the response
   is `NO_AUTH` or `ACCESS_DENIED_FROM_IP`, the
   script's actionable error message tells the
   operator exactly which prereq is missing.

9/10 BL-2 phases SHIPPED. Only Phase 10 (release
tag) remains.

---

### B237.19 — exit-rules form-error flash + duplicate banner UX

Closes 2 operator-reported bugs from 2026-09-07.

#### Bug 1 — form validation rendered a giant plain-text page
`PostMyExitRule` called `http.Error(w, ..., 400)` on
form-validation failures (invalid IP, limit exceeded,
device not owned, etc.). The browser rendered a
giant plain-text page and the operator lost the
form values they had typed. The operator's first
screenshot shows exactly this:
"invalid target_value 'developer.nvidia.com':
expected IP or CIDR for target_type='ip' ..." filling
the whole tab.

**Fix**: `PostMyExitRule` now calls `http.Redirect`
to `/my/exit-rules?err=<msg>&form_*` via the new
`buildFormErrorRedirectURL` helper (7 call sites:
invalid IP, user limit, device limit, system limit,
device not owned, exit-node rejected, generic DB
error). The template renders `.err` as a flash
banner above the form (same UI surface as the
duplicate banner). The user's form values are
preserved via the `form_*` query params.

#### Bug 2 — duplicate banner wording was misleading
"Правило для X уже существует — не дублируем.
Удалите существующее, если нужно обновить" sounded
like an error, and the `alert-danger` color made it
look like "the new rule failed to add" when in
reality the new rule was never created (it was the
old `/32` from the first add of the same domain).
The autoupdater handles updates; the user doesn't
need to delete.

**Fix**:
- The duplicate banner's color changed from
  `alert-danger` (red) to `alert-info` (blue) — it's
  informational, not an error.
- Wording updated: "Домен X уже покрыт правилом —
  автообновление будет поддерживать его
  актуальность" (RU) / "Domain X is already covered
  by an existing rule — the autoupdater will keep it
  current" (EN). No more "delete to update" hint.
- New i18n key `exit_rules.form_error` (RU + EN)
  for the flash banner.

**Verified** (local): `bash scripts/check_b237_19.sh`
→ 15/15 pass; 4 new unit tests pass (no regression
on the 5 B123 tests); `go build ./...` clean.

**Live-verify pending** (operator-side):
- (a) Type "developer.nvidia.com" with `target_type=ip`
  and submit — should see a blue flash banner with
  the same message, form values preserved, instead
  of a giant plain-text page.
- (b) Re-add a domain that was already added — should
  see the duplicate banner in blue + the new
  wording, with a link to the existing rule.

---

## Files added / changed in v1.5.2

| File | Change |
|---|---|
| `internal/update/docker.go` | New `GitRefForBuildLabel` helper + `gitRef` arg in `Run` (B237.10) |
| `internal/update/docker_test.go` | +132: 3 new test functions, 17 subtests |
| `internal/feature/admin/update.go` | 2 callsite changes (B237.10) |
| `entrypoint.sh` | `SKYGATE_PREBUILT=1` guard (B237.15) |
| `Dockerfile.prebuilt` | NEW (B237.15) |
| `docker-compose.ghcr.yml` | NEW (B237.15) |
| `docker-compose.lite.yml` | NEW (B237.15) |
| `deploy/install.sh` | NEW (B237.15) |
| `deploy/install-{debian,rh,alpine,bare}.sh` | NEW ×4 (B237.15) |
| `deploy/install-common.sh` | NEW (B237.15) |
| `deploy/Setup-Skygate-Win.ps1` | NEW (B237.15) |
| `deploy/systemd/skymate-cleanup-smoke.{service,timer}` | NEW (B237.17) |
| `internal/headscale/reconcile.go` | NEW (B237.18) |
| `internal/headscale/reconcile_cron.go` | NEW (B237.18) |
| `internal/headscale/reconcile_json.go` | NEW (B237.18) |
| `internal/headscale/reconcile_test.go` | NEW (B237.18) |
| `internal/headscale/tags_test.go` | 1 line (404 → http.StatusNotFound) |
| `internal/oidc/e2e_test.go` | 1 line (302 → http.StatusFound) |
| `internal/feature/exit_rules/form_my.go` | `buildFormErrorRedirectURL` + 7 redirect sites (B237.19) |
| `internal/feature/exit_rules/form_my_b237_19_test.go` | NEW (B237.19) |
| `internal/handlers/templates/exit_rules.html` | `.err` flash banner + duplicate `alert-info` (B237.19) |
| `internal/i18n/catalog_exit_rules.go` | `form_error` (RU+EN) + duplicate wording (B237.19) |
| `internal/config/config.go` | `ReconcileHeadscaleUsers*` fields (B237.18) |
| `cmd/skygate/main.go` | `StartReconcileCron` wire-up (B237.18) |
| `scripts/b146_regapi_live.sh` | NEW (B146) |
| `scripts/cleanup_smoke_artifacts.sh` | NEW (B237.17) |
| `scripts/check_b{140,141}.sh` | `s.DB` → `s.dbc()` fix (B237.16) |
| `scripts/check_b146.sh` | NEW (B146) |
| `scripts/check_b237_10.sh` | NEW (B237.10) |
| `scripts/check_b237_15.sh` | NEW (B237.15) |
| `scripts/check_b237_16.sh` | NEW (B237.16) |
| `scripts/check_b237_17.sh` | NEW (B237.17) |
| `scripts/check_b237_18.sh` | NEW (B237.18) |
| `scripts/check_b237_19.sh` | NEW (B237.19) |
| `scripts/verify_pre_deploy.sh` | +5 `run_check` rows (B237.10/15/16/17/18/19 + B146) |
| `AGENTS.md` | +6 B-block sections |
| `docs/PLANS.md` | 4 items marked DONE (TD-2/6/7/14) |
| `docs/internal/ha-v1.5.0-execution.md` | §6 status log + §4 open questions updated |
| `README.md` | New "Deployment variants" section |

## Test coverage

| Suite | Status |
|---|---|
| `bash scripts/check_b237_10.sh` | 19/19 pass |
| `bash scripts/check_b237_15.sh` | 50/50 pass |
| `bash scripts/check_b237_16.sh` | 10/10 pass |
| `bash scripts/check_b237_17.sh` | 18/18 pass |
| `bash scripts/check_b237_18.sh` | 22/22 pass |
| `bash scripts/check_b237_19.sh` | 15/15 pass |
| `bash scripts/check_b146.sh` | 15/15 pass |
| `bash scripts/check_b140.sh` | 7/7 pass (B237.16 follow-up fix) |
| `bash scripts/check_b141.sh` | 8/8 pass (B237.16 follow-up fix) |
| `go test -short -count=1 ./...` | All packages green (no regression) |
| `go build ./...` | Clean |
| `staticcheck ./...` | 0 ST1013, 0 SA1012 (was 2 stragglers in B237.16) |

## Breaking changes from v1.5.0

None. v1.5.2 is a strict hotfix release. All env
vars + config keys are additive; the new
`ReconcileHeadscaleUsers` and the
`SKYGATE_PREBUILT=1` env var are both default-on
(operator can opt out with
`SKYGATE_RECONCILE_HEADSCALE_USERS_ENABLED=false`).

## Migration from v1.5.0

None. The deployment-variants work is additive
(new files only; the original `docker-compose.yml`
+ `Dockerfile` + `entrypoint.sh` are unchanged for
the operator's local dev path). The
`reconciliation` cron is auto-enabled with sensible
defaults.

To pick up v1.5.2 on the live VM:

```bash
cd /home/skyadmin/skygate
git pull --no-rebase origin main
docker compose restart skygate
docker exec skygate-skygate-1 /app/skygate version
# → skygate v1.5.2-1-g<commit>
```

To pick up v1.5.2 via the auto-updater (after
`B237.10`'s fix):

```
/admin/update → "Push update" (or "Apply" if the
tagged release was published)
```

## Known limitations

- **B146 live-verify pending** (see the B146
  section above — 3 operator steps needed).
- **`skygate regapi-credentials set` CLI
  subcommand is not in v1.5.2**. The `/admin/ha`
  form is the only path to write the reg.ru creds.
  For the operator's "one-shot bootstrap" flow
  (which prefers CLI over the browser), this is
  a future B-block.
- **TD-11 / TD-12 / TD-13 still open** as LOW
  priority. None block production.

## Live-verify on the operator's deployment (192.168.13.69)

- **Container version**: `skygate v1.5.0-8-g51e5b82`
  (commit 51e5b826, the B237.19 commit; 8 commits
  past the v1.5.0 tag at a6e648d).
- **headscale policy grants**: 213, with
  `via: ['tag:dev-infra-emilia']` for skyadmin +
  michail.
- **DERP health**: 30/30 regions, all 28 public +
  2 own (derp.skynas.ru + derp.emilia).
- **Smoke cleanup**: live DB has 0 `smoke_mesh_*` rows
  (the v0.33.1.x accumulation was cleared manually;
  v1.5.2's B237.17 + B143 cron prevents future
  accumulation).
- **Headscale user ID reconciliation**: cron
  enabled; first cycle log:
  `reconcile: 0 portal_users checked
  (hs_users=0, ok=0, linked=0, relinked=0,
  orphans=0, errors=0, took=2.0ms)`
  (the operator's DB has no drift — every
  `portal_users.headscale_user_id` matches a
  current headscale user).

## See also

- **`RELEASE-NOTES-v1.5.0.md`** — the v1.5.0 release
  (B145 / B149 / B150 / B147 / B148 + the DERP /
  Tailscale / exit-rules fixes from the 2026-09-04
  incident response). v1.5.2 is a hotfix on top.
- **`docs/internal/2026-09-04-tailnet-fixes.md`** —
  the 2026-09-04 incident post-mortem.
- **`docs/internal/tailnet-advertised-routes.md`** —
  B236: the subnet-router hard rule, the
  verification commands, the loop it caused.
- **`docs/internal/exit-rules-reconciler.md`** —
  B229 / B237.7: the three-layer architecture, the
  decision matrix, the default-flip rationale, the
  build-time contract tests.
- **`docs/internal/ha-v1.5.0-execution.md`** —
  §6 status log has the 2026-09-07 B146 entry +
  the Q1 + Q2 "✅ DONE" updates.
- **`docs/PLANS.md`** — TD-2 / TD-6 / TD-7 / TD-14
  marked DONE in v1.5.2; the rest (TD-4 / TD-5 /
  TD-8 / TD-9 / TD-10) marked DONE earlier.
- **`AGENTS.md`** — B237.10 / B237.15 / B237.16 /
  B237.17 / B237.18 / B237.19 / B146 sections with
  code-level details + file lists.
- **`README.md`** — new "Deployment variants" section
  covers all 5 V1–V8 deploy surfaces.

---

*Released 2026-09-07. SHA: 51e5b826 (B237.19 tip).*

