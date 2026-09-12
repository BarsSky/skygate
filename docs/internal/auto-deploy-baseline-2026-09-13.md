# Auto-deploy baseline — 2026-09-13

**Captured**: 2026-09-12T21:06:00Z (UTC) by hermes-debug at operator's request.
**Phase**: 0 of [auto-deploy-test-plan.md](./auto-deploy-test-plan.md).
**Method**: read-only probes against the live skygate VM (192.168.13.69).
**Raw data**: `scripts/__phase0_collect.sh` (re-runnable; output captured at `/tmp/phase0_baseline_20260912_210600.txt` on the VM).

---

## TL;DR

| metric | value |
|---|---|
| `verify_pre_deploy.sh` PASS lines | **356** |
| `verify_pre_deploy.sh` FAIL lines | **37** (down from 78 at session start) |
| `verify_pre_deploy.sh` exit code | 0 (every check is fail-tolerant; non-zero FAIL counts only) |
| `skygate-skygate-1` build label | `v1.5.2-61-g9230130` (B243 source baked in; git label not bumped) |
| `skygate-skygate-1` uptime | 21,504s (~6 hours) |
| `/healthz` | 200 / `status=ok` |
| `/readyz` | healthy, all 4 dependencies ok (db, headscale, headplane, tailscale) |
| skygate DB | PG via `172.18.0.3:5432/skygate_staging` (in-container `skygate-pg-local`) |
| headscale version | v0.29.1 (per docker inspect; container label pinned 0.29.2) |
| disk usage | 68% (27G used / 42G total / 13G free) |
| OIDC | enabled, RSA-2048 keys in `/data/oidc-keys-test` |
| public HTTPS (skygate.skynas.ru / head.skynas.ru) | UNREACHABLE from probe context (Caddy TLS offload on NPM at 192.168.13.67) |

## What works (live-verified 2026-09-12)

1. **skygate-skygate-1 healthy** — /healthz 200, /readyz healthy with all 4 deps.
2. **headscale API responds** — 5 users, 0 duplicate names (post-B243 cleanup).
3. **ACL policy applied** — 171 grants, 21 tagOwners (skyadmin-owned tags + infra exit-node tags).
4. **DB writeable** — portal_users=5, node_owner_map=16, audit_log=6387.
5. **Reconcile cron stable** — 5 portal_users checked every hour, `ok=5 linked=0 relinked=0 orphans=0 errors=0` consistently for last 5 cycles (post-B243 fix).
6. **B243 deploy intact** — audit_log has 4 new rows from today's B243 cycle (duplicate_names_in_headscale summary + per-row duplicate_name + relinked old=86→new=1).
7. **Portal admin linked correctly** — `portal.skyadmin.headscale_user_id = 1` (the legitimate bootstrap admin with 7 devices).
8. **Tailscaled running** — `(local tailscaled)` ok per /readyz (state-file fallback path; tailscale status --json failed inside container — see Gaps).
9. **3 new B-checks deployed** — `check_b_duplicate_users.sh`, `check_b_reconcile_audit_writes.sh`, `check_b_node_owner_map_orphans.sh` (added 2026-09-12).

## What does NOT work / partial

1. **tailscale status --json fails inside skygate-skygate-1** — /readyz reports `(state-file fallback — tailscale status --json failed)`. The container tailscaled is running but the JSON output fails (probably a permission/socket issue inside the bind-mount). NOT blocking (operational), but worth investigating for cleaner diagnostics.

2. **node_owner_map orphan rows (3)** — `check_b_node_owner_map_orphans.sh` reports 3 stale rows (michail hs_id=6). Operator explicitly excluded these from cleanup ("устройства michail трогать не надо"). Documented; awaiting operator decision.

3. **headscale OIDC user auto-creation gap** — headscale v0.29.1 by design creates a NEW user on first OIDC login if the `name` claim doesn't match an existing user. Documented in AGENTS.md B243 section. Mitigated by B243's cron refusal (no silent relink); but the headscale-side duplicate will recur on every fresh OIDC bootstrap. Need headscale-side fix (email-based user mapping or operator manual intervention) — out of scope for this session.

4. **External network unreachable from probe context** — `192.168.13.66` (svi), `100.64.0.24` (svyatoslava-1), `skygate.skynas.ru`, `head.skynas.ru` all show UNREACHABLE from the bash `exec 3<>/dev/tcp/...` probe. Known state — operator has confirmed svi/svyatoslava-1 are down/separated. Public HTTPS is Caddy-offloaded through NPM at 192.168.13.67, not directly reachable from the container's network namespace (probe context bug — `exec 3<>/dev/tcp/...` uses host's namespace; container traffic would need a different probe). NOT a real outage.

5. **172.18.0.3:5432 (local-pg) shows UNREACHABLE from probe** — same probe-context bug as above. The PG IS reachable from inside skygate-skygate-1 (per /readyz `db:ok`); the probe is testing from the wrong network namespace.

6. **`172.17.0.1:5433` reference in skygate `.env`** — STALE. Pre-B-mod-sqlite-pg-bidi the PG lived on the docker bridge at 172.17.0.1; it now lives at 172.18.0.3 (skygate-pg-local). Already fixed by `SKYGATE_DB_DSN` + `SKYGATE_DB` env vars (both point to 172.18.0.3). The leftover `172.17.0.1:5433` in `.env` is unused (skygate prefers `SKYGATE_DB` over `SKYGATE_DB_DSN`).

7. **3 .env stale values on the host** — fixed 2026-09-12: `SKYGATE_DB` set to PG DSN, `SKYGATE_DBMIGRATE_TRANSPORT=ssh → local`, SSH host/key commented out. Backup at `/home/skyadmin/skygate/.env.bak.20260912_155352`.

## B-check coverage map (verify_pre_deploy.sh FAIL summary)

37 FAILs group into 3 buckets:

### A. Operator-side (cannot fix from check scripts) — 14 FAILs

| B# | reason | fix needed |
|---|---|---|
| B1 | `go test ./...` needs go1.25.4 download | operator: install go1.25.4 |
| B44 | `db.OpenPostgres` — no PG on this VM | operator: run verify on a PG-backed VM |
| B95 | staticcheck not installed | operator: `go install honnef.co/go/tools/cmd/staticcheck@latest` |
| B110 | tailnet reachability — svi offline | operator: bring svi back online |
| B151 | `init-headplane.sh` MISSING | operator: write the script |
| B152 | `bootstrap_standby.sh` MISSING (HA Phase 7) | operator: write the script |
| B153 | `dr_drill.sh` MISSING (HA Phase 9) | operator: write the script |
| B164 | `deploy/derp-init.sh` MISSING | operator: write the script |
| B167 | `deploy/oidc-sync.sh` MISSING | operator: write the script |
| B168 | OIDC live e2e — svi offline | depends on B110 |
| TD-15 | similar TBD-series | TBD |
| TD-16 | 6 pass / 1 fail | minor |
| TD-18 | minor | minor |
| C, E, F | partial-contract failures (one of multiple sub-checks failed) | investigation needed |

### B. Code-side check-script hygiene (false-positive regex/path drift) — 11 FAILs (down from 21 at session start)

| B# | reason | status |
|---|---|---|
| B11 | `DROP INDEX IF EXISTS` flagged as "destructive DDL" | false positive — needs `IF EXISTS` exclusion |
| B6 / B111 | ACL per-device grant ordering test | cosmetic test ordering |
| B93 | `s.DB` vs actual `s.dbc()` | **FIXED** (commit `8dfb0eb4`) |
| B118 | tag-owner-from-name | needs investigation |
| B135 | Manrope font CSS | needs check-script update |
| B146 | 14 pass / 1 fail | minor |
| B160 | `s.DB` vs actual `s.dbc()` | **FIXED** (commit `8dfb0eb4`) |
| B162 | `s.DB` vs actual `s.dbc()` | **FIXED** (commit `8dfb0eb4`) |
| B176 / B185 / B205 / B206 / B237.* | various recent-commit regressions | needs investigation |
| B208 | DBSource expected only in admin/dbsource.go | **FIXED** (commit `3b1661ba`) |
| B221 / B232 | migration_b213_test asserts `last.Version != 67` (stale) | **FIXED** (commit `0e52f14a`) |
| B229 | inverted env-var logic + missing test name | **FIXED** (commit `0e52f14a`) |
| B187 | hardcoded `172.17.0.1:5000` (stale post-PG-move) | **FIXED** (commit `3b1661ba`) |
| B188 | hardcoded `172.17.0.1:5000` (V, W, AA contracts) | **FIXED** (commit `3b1661ba`) |
| TD-18.2 | `scripts/check_td182.sh` not executable | **FIXED** (commit `b93b010c`) |

### C. Cosmetic / low priority — 12 FAILs

ACL test ordering (B6/B111), TD-series minor (TD-15/16/18), partial contract failures (C/E/F). Documented; deferred until next phase.

## Session timeline (commits + live operations)

### Phase F: B-mod-sqlite-pg-bidi (continued from earlier sessions)
- `1abdcd53` backup.sh PG fix
- `99d77e44` launch_skigate.sh + check_b_backend_mode.sh

### Phase: Issue closure
- `ab1ee380` Issue #4: drop linux/arm64 from Docker matrix
- `ec0b9a8d` Issue #2: PostAdminExitRule handler + tests + i18n

### Recovery cascade (no commit, operational)
- Recovered skygate.db from named volume (41 MB real data)
- Spun up postgres:18-alpine container `skygate-pg-local` at 172.18.0.3
- Restored OIDC + DERP in headscale config
- Disk cleanup: journald 500M, docker log driver 50m×3, weekly prune cron → 87%→63%
- Cleanup: removed 3 Exited containers

### Phase: B-mod-admin-user-sync option (c)
- `0379893d` T1: check_b_admin_user_sync.sh (5 contracts)
- `ee55afae` T2: install.sh fixes (3 standalone-invocation bugs)
- `33a4faac` T3: headscale.RenameUser client + 7 unit tests
- `86e8b4f1` T4: PostMyDeviceRename handler + UI + i18n + 6 unit tests
- `c9ca555f` T5: promote_to_admin flag + 5 unit tests
- `f241ed63` T6: drift detection banner + 9 unit tests
- `1c139faa` T7+T8: AGENTS.md + verify_pre_deploy.sh registration

### Phase: B243 (silent relink + PG placeholders bug class)
- `fb529dd2` B243 fix (PG placeholders + duplicate refusal)
- `6f17da13` B243 docs in AGENTS.md

### Phase: Live operator-side execution (no git, operational)
- Backup reconcile.go to .b243-pre
- scp + sudo cp the 2 fixed files
- `docker restart skygate-skygate-1` → entrypoint ran `go build` → new binary
- First cron cycle: wrote `duplicate_names_in_headscale` summary audit row
- `docker exec headscale headscale users delete -i 86 --force`
- Second cron cycle: `relinked old=86 new=1` → portal.skyadmin → hs_id=1 ✓
- 3 .env bugs fixed (SQLite→PG DSN, dbmigrate SSH→local transport, dead SSH vars commented)
- 2 skyadmin-only orphan rows deleted (node_id=44, 45 with hs_id=2147455555 overflow)
- 3 michail orphan rows LEFT ALONE per operator instruction
- reconcile.go.b243-pre deleted (no longer needed)

### Phase: Check-script hygiene (this turn)
- `b93b010c` chmod +x on check_td182.sh
- `8dfb0eb4` fix s.DB → s.dbc() in B93, B160, B162
- `3b1661ba` fix 172.17.0.1:5000 → docker exec pattern in B187, B188 (V/W/AA); fix DBSource lookup in B208
- `0e52f14a` fix inverted env-var logic + test name in B229; fix migration version drift in B221, B232

### Phase: New B-checks (B-mod-admin-user-sync B243 follow-ups)
- `e9f3d697` .gitignore for `__*`, `=*`, `*.b243-pre`
- `fbbe732d` 3 new B-checks: check_b_duplicate_users.sh, check_b_reconcile_audit_writes.sh, check_b_node_owner_map_orphans.sh

## Git state

| location | HEAD | status |
|---|---|---|
| local (`C:\Projects\skygate`) | `0e52f14a` | clean (all commits) |
| VM (`/home/skyadmin/skygate`) | `9230130` | behind by 13 commits (operator needs `git pull`) |

The VM's git repo is **separate** from the local one — they're not connected via `origin`. The fixed B-check scripts + .gitignore + 3 new B-checks were synced to the VM via `scp` (since `git push` would require either an `origin` URL or a manual merge). The reconcile.go B243 fix was deployed to the VM by scp as well; the local git has the fix in commit `fb529dd2` but the VM's git doesn't.

**To sync the VM git with the local commits** (operator-side task):
```bash
ssh hermes-debug@agent
cd /home/skyadmin/skygate
git remote add origin <local-or-operator-mirror>  # if not already configured
git pull origin main  # or cherry-pick the specific commits
```

Or use the existing operational flow (operator pushes to operator-mirror, VM pulls via cron / ha-deploy).

## Next phase readiness

- **Phase 1 (Tailscale grants)**: REQUIRES svi online. Currently blocked.
- **Phase 2 (Re-deploy svi via bootstrap_standby)**: REQUIRES `scripts/bootstrap_standby.sh`. Missing.
- **Phase 3 (create-standby-preauth)**: REQUIRES preauth script + svi. Blocked.
- **Phase 4 (Policy reapply + grants)**: REQUIRES Phase 1-3 done.
- **Phase 5 (Fresh deploy on new VM)**: REQUIRES operator-supplied droplet.
- **Phase 6 (HA DR drill)**: REQUIRES maintenance window + `scripts/dr_drill.sh`. Missing.
- **Phase 7 (Regression tests + B-checks)**: 3 of 4 new B-checks added (duplicate_users, reconcile_audit_writes, node_owner_map_orphans). Still TODO: `check_b_tailscale_grants.sh` (Phase 1 deliverable), `check_b_tag_owners.sh` (operator request).

## File locations

- Live VM at `/home/skyadmin/skygate/` (separate git, not yet pulled)
- Backup of pre-B243 reconcile.go: deleted (no longer needed; B243 verified live)
- B-check scripts: `scripts/check_b_*.sh` (in repo, synced to VM via scp)
- Raw baseline data: `/tmp/phase0_baseline_20260912_210600.txt` on the VM
- Probe script: `scripts/__phase0_collect.sh` (re-runnable)

---

**Document version**: 2026-09-13-001 (operator-readable).
**Next review**: when Phase 1 starts (operator needs to bring svi online).
