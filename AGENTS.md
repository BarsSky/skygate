# AGENTS.md — AI hints for Skygate

This file is for AI assistants (Hermes, Claude, Cline, Cursor, etc.) working on
or with Skygate. Read this **first** before suggesting changes or running tasks.

**Before proposing work, also read [`docs/BACKLOG.md`](docs/BACKLOG.md)** —
it tracks abandoned / blocked / in-progress features (HA skygate-host-2,
PG cutover, backup polish, perf regression tests, etc.) so you don't
re-litigate old decisions or propose work that's already in flight.

---

## v0.28.5 guarantee catalog (B1-B18 build-time + R1-R27 runtime)

**Why this exists.** The v0.28.5 incident revealed that
`make test` + `make smoke` is not enough. Three independent bugs
shipped through both:
- **B5/R20** Migration v0.47 was not idempotent: every skygate
  restart re-backfilled `via_enabled=1`, undoing the operator's
  un-pin via the UI.
- **B6/R11** v0.28.0 removed the catch-all `*` from grants, but
  the per-user grant kept `src=user@` which in Tailscale v2
  policy does NOT match tagged devices — every device without
  a per-device pref had no grant for `autogroup:internet` and
  was silently denied exit-node access.
- **R6/R21** The Tailscale state file persisted `--exit-node`
  prefs across restarts, so a leftover `tailscale set --exit-node=
  relay-1` (from a debug session) kept routing ALL skygate-host-1
  traffic through relay-1 — including the Docker bridge
  172.18.0.0/16 — which broke the openresty → skygate-host-1:8080
  path with a 504.

To prevent the next incident, every change to skygate must
pass `make verify-pre` (build-time) and every deploy must
pass `make verify-post` (runtime). The catalogs below are the
contract. If a check fails, the build/deploy is broken — do not
push or roll forward until it's fixed or the check is updated
to reflect a deliberate design change.

### Build-time (B1-B18) — run `make verify-pre` before `git push`

| # | Guarantee | How |
|---|-----------|-----|
| B1 | `go test ./...` exits 0 | `go test ./...` |
| B2 | `go vet ./...` exits 0 | `go vet ./...` |
| B3 | `go build ./cmd/skygate` produces a binary | `go build -o /tmp/x ./cmd/skygate` |
| B4 | i18n: ru and en key sets match | `go test ./internal/i18n/ -run TestCatalogsParity` |
| B5 | migration v0.47 idempotent (3 tests) | `go test ./internal/db/ -run TestMigrateV047` |
| B6 | ACL: per-device grant ordering + via opt-in + tagged-device loose | `go test ./internal/acl/...` |
| B7 | templates: all embed.FS templates parse | `go test ./internal/handlers/ -run TestLoadTemplates` |
| B8 | smoke RU+EN 83/83 each (VM only) | `make test` on VM; skipped on Windows |
| B9 | `RELEASE-NOTES.md` has an entry for the new version | `grep vX.Y.Z RELEASE-NOTES.md` |
| B10 | no `.env` / `*.key` / `*.pem` in git tracked paths | `git ls-files` filtered |
| B11 | migrations have no destructive DDL (DROP/RENAME/TRUNCATE) | grep + pgmigrate test |
| B12 | pgmigrate helpers are unit-tested (per-driver SQL form) | `go test ./internal/db/pgmigrate/ -run TestBuildCreateIndexStmt` |
| B13 | pre-push hook uses MSYSTEM for Git Bash detection | `grep -q MSYSTEM .githooks/pre-push` |
| B14 | skygate host-side wrapper exists + syntax-valid + uses correct label | `bash -n` + grep `com.docker.compose.service=skygate` |
| B15 | exit-rules `parent_domain` regression tests for DNS-resolved /32 | v0.30.x form/autoupdater chain (parentDomain field in `internal/feature/exit_rules/{store,sync,api}.go`; tests dropped during refactor — see B15 in `scripts/verify_pre_deploy.sh` for the new grep-based check) |
| B16 | exit-rules CDN detection regression tests (Cloudflare/Fastly/Google/Akamai) | v0.30.x Cloudflare anycast churn fix (`internal/feature/exit_rules/cdn.go`; tests dropped during refactor — see B16 in `scripts/verify_pre_deploy.sh`) |
| B17 | per-user device can't be tagged as exit-node (v0.30.1) | guard in `PostAdminNodeTag` + tests in `internal/feature/admin/devices_test.go` (moved from `internal/handlers/handlers_admin_nodes_test.go` in refactor-v0.30 Phase B step 3a) |
| B18 | PG foundation builds (v0.31.0) | `go build -tags postgres ./cmd/skygate` + 4 verification tests in `internal/db/test_pg_migrations_test.go` |

### Runtime (R1-R27) — run `make verify-post` after `docker compose up -d skygate`

| # | Guarantee | What it catches |
|---|-----------|-----------------|
| R1 | `/healthz` 200, `status:ok` | Process dead |
| R2 | `/readyz` 200 (DB + headscale OK) | Dependency down |
| R3 | skygate build label = HEAD commit | Wrong binary deployed |
| R4 | `tailscaled` running inside skygate-host-1 | TUN missing |
| R5 | skygate-host-1 tailnet IP = 100.64.100.10 | Node not registered |
| R6 | skygate-host-1 does NOT use an exit-node (status line shows `linux  -`) | Stale exit-node in state → Docker bridge unreachable |
| R7 | Docker bridge 172.18.0.0/16 reachable from skygate-host-1 | Network namespace broken |
| R8 | headscale `/api/v1/policy` returns non-empty policy | Auth/connectivity broken |
| R9 | Live policy `updatedAt` ≈ last applied snapshot (`acl_snapshots.applied_success=1`) | Reapply needed |
| R10 | 4 per-user grants, `src=user@`, `dst` includes `autogroup:internet` | v0.28.3 minimum shape |
| R11 | ≥5 per-device loose grants (`src=tag:dev-*`, `dst=autogroup:internet`, NO `via`) | v0.28.5b tagged-device fix |
| R12 | No catch-all `src=*` → `autogroup:internet` | v0.28.3 bypass fix regression |
| R13 | `*` → `tag:public` AND `*` → `tag:exit-node` catch-alls present | SSH reachability to relays |
| R14 | `tagOwners` contains `tag:public`, `tag:exit-node`, `tag:private`, `tag:subnet-router` | Parser accepts policy |
| R15 | No per-device grant has `via` for `via_enabled=0` row | Migration re-backfill regression |
| R16 | Per-user grant `via` matches `user_exit_node_prefs.via_enabled` | Same regression |
| R17 | relay-1, relay-2, relay-3 online in headscale | Relay outage |
| R18 | Each exit-node advertises `0.0.0.0/0` | Real exit-node, not stub |
| R19 | DB: all per-user `via_enabled` match live policy | Cross-check |
| R20 | Migration v0.47 idempotent at runtime | (Same as B5; covered by build test) |
| R21 | `tailscaled.state` on disk has no stale `ExitNodeID` | Won't re-trigger the 504 path |
| R22 | `https://skygate.example.com/healthz` → 200 | HTTPS path works end-to-end |
| R23 | TLS cert is Let's Encrypt, > 7 days valid | Cert renewal gap |
| R24 | openresty upstream (`localhost:8080`) returns 200 | Not 504 |
| R25 | skygate-host-1 pings `8.8.8.8` with 0% loss | Direct internet works |
| R26 | No headscale node has BOTH `tag:dev-*` AND `tag:exit-*` | v0.30.1 workstation-8-bug regression: per-user device as exit-node |
| R27 | PG-staging VM: live migration lock_timeout + 4 verification tests pass (v0.31.0) | `SKYGATE_TEST_PG_DSN` set; roundtrip + idempotency + lock_timeout + data_mig PASS |

### How to extend the catalog

If you add a new invariant (e.g. a new migration, a new exit-node,
a new TLS SAN, a new required i18n key), add the check to
`scripts/verify_pre_deploy.sh` (build-time) and/or
`scripts/verify_post_deploy.sh` (runtime) **in the same PR** as
the change. The catalog is the test — code that ships without
a check is code that will silently regress.

If a check legitimately needs to be removed (e.g. a feature
being deprecated), remove the check in the same PR as the
feature removal and add a one-line note in the commit message
explaining why.

---

## Release status

* **Current**: v0.33.0 — Network Access Manager + Admin Test Page.
  15 commits since v0.32.0 (1 ahead of origin/main after the
  v0.33.0 push). All tests green (`go test ./... -count=1 -short`
  27/27 PASS). What's added:
  - **`devicemeta`** (new `internal/devicemeta/` package, migration
    v0.48): per-device `os` + `device_type` columns on
    `node_owner_map`. Auto-detect heuristic
    (`DetectOS`/`DetectType` — DESKTOP-*/MSI/skygate-host-1 →
    windows/linux; iPhone/iPad → ios; Nothing Phone/android-* →
    android; MacBook* → macos). Auto-detect runs on every
    /my/devices load (first-detect-wins rule: rows already
    admin-set are skipped). Manual override form on
    /admin/devices (POST /admin/devices/{id}/meta, 2 selects +
    Save button, `<details>` collapsed by default). Setting
    both to "unknown" re-enables auto-detect. RU + EN keys
    + 5 unit tests.
  - **`via: sync bug fix`** (`Service.generateACL` in
    `internal/feature/exit_rules/store.go`): the
    /my/exit-rules + /admin/exit-rules + REST API paths
    hardcoded `acl.GenerateACL` (no-via), ignoring
    `SKYGATE_ACL_VIA_ENABLED`. The per-device-pref +
    admin-subnet paths already used
    `acl.ApplyACLPipelineForPlane` which honours the env
    var. Symptom: snapshot 1024 in DB had `"via":` 5 times,
    but live headscale policy had 0. Fix: dispatch helper
    reads the env var and routes to the right generator.
    2 unit tests pin the env-var contract.
  - **refactor-v0.30 Phase C + D (internal, no API change)**:
    catalog.go 4260 lines → 12 per-feature `catalog_*.go`
    files + glue (Phase C, 16 files changed, +56/-4255 lines);
    `SanitizeFilename` dedup → `internal/httputil/`
    (Phase D1, 3 copies → 1 + 6 tests);
    `backfillNodeOwnership` → `internal/nodeownership/`
    (Phase D2, 399 lines + 3 tests);
    per-user control plane router → `internal/controlplane/`
    (Phase D3, 192 lines + 8 tests); thin `*App` method
    wrappers collapsed (Phase D4). `internal/handlers/`
    shrunk from 76 files (19k lines, start of refactor) to
    9 files (infrastructure + 3 test files). 24/24
    packages green; `make verify-pre` 17/18 PASS.
  - **`scripts/split_i18n.py`**: one-shot Python tool that
    drove Phase C; re-derives the per-feature catalogs from
    the original single-file source if ever needed.
  - **`scripts/verify_pre_deploy.sh`**: B15/B16/B17 checks
    updated to point at the refactored test file locations
    (the tests themselves moved to the per-feature
    packages during the refactor).

  - **`Network Access Manager`** (new `internal/feature/admin/headscale_acl.go`,
    migration v0.50): `/admin/headscale/acl` UI for adding/removing
    skygate-managed headscale ACL rules. Critical invariant:
    read-modify-write of the live policy preserves every
    other field (ssh, groups, tagOwners, hosts) — only
    acls[] is mutated. Idempotent on rule fingerprint
    (re-adding the same rule returns the existing ID).
    Solves the 2026-08-04 incident where svyatoslava-1
    joined the headscale but couldn't reach skygate-vm
    because the policy had 0 acls (default deny).
  - **`Admin Test Page`** (new `internal/feature/admin/system_tests.go`,
    migration v0.51): `/admin/system_tests` runs an
    in-process test suite (6 tests across network/db/headscale
    categories) and stores results in `system_tests_runs`.
    Includes the `headscale.acl_admin_present` check that
    would have caught the svyatoslava-1 incident at the
    "is admin rule present?" level. 5s per-test timeout,
    history strip shows the last 20 runs.
  - **Catalog extended to B42 / R32**: B38-B42 (build-time
    code presence for the new feature) + R31, R32 (live
    page renders).

  **Roadmap (next features, recorded 2026-08-04)**:

  - **v0.34.0 — skygate duplicate auto-deploy**: admin enters
    a target VM's IP + SSH key on `/admin/deploy`; skygate
    clones itself (repo, env, headscale/etcd/PG/wal-g setup);
    the new skygate registers as a "site" in the original;
    cross-site sync via headscale replication + PG streaming
    + (optional) wal-g base restore. For failure-tolerance
    + DR. Estimated 1-2 weeks of work; the deployment
    infrastructure is mostly already in place
    (`deploy/skygate-cli.sh`, `deploy/deploy.sh`,
    `internal/update/orchestrator.go`).
  - **v0.35.0 — S3 storage connection from web UI**:
    `/admin/storage` page lets the operator set the MinIO
    endpoint + creds via the web UI (no more editing
    `secrets/ts_authkey` or `.env`). Includes a "Test
    connection" button (boto3 `list_buckets`) and persists
    to the new `global_settings` table. Used by v0.34.0's
    auto-deploy to find the operator's preferred MinIO
    without env-file editing. Estimated 2-3 days.

  **v0.28.5 guarantee catalog (extended through v0.33.0) applies**
  — every build must pass `make verify-pre` (42 build-time
  checks: B1-B42) and every deploy must pass `make verify-post`
  (32 runtime checks via SSH to the VM: R1-R32). The catalog
  is in the [v0.28.5 guarantee catalog section](#v0285-guarantee-catalog-b1-b18-build-time--r1-r27-runtime)
  (named for the incident that motivated it; extended
  incrementally through subsequent releases).

* **Previous**: v0.30.1 — per-user device can't be tagged as exit-node
  ([tag v0.30.1](https://github.com/skygate-operator/skygate/releases/tag/v0.30.1)).
  The "workstation-8" fix. user1's Windows box "workstation-8" had silently
  acquired `tag:exit-node` (probably from an old debug-session
  `headscale nodes tag` on the VM host) and self-routed all
  traffic to /dev/null. `PostAdminNodeTag` now refuses exit-node-like
  tags on per-user devices (extractable pure function
  `nodeTagRefusedForUserDevice`); 8 unit tests. R26 added:
  `verify_post_deploy.sh` walks `headscale nodes list` and
  fails if any node has both `tag:dev-*` and `tag:exit-*`
  (catches the direct-CLI bypass the B17 build-time guard
  can't see). B17 + R26 in catalog.

* **Previous**: v0.29.3 / v0.29.3.1 — Auto-swap via helper container
  in host PID namespace
  ([tag v0.29.3](https://github.com/skygate-operator/skygate/releases/tag/v0.29.3)).
  Closes the orchestrator loop: `git push → build → swap` end-to-end
  with auto-rollback. v0.29.3 first tried `Setsid` from inside the
  old container; the SIGTERM that compose sent to skygate (PID 1
  of the old container) propagated to the swap subprocess via the
  shared PID namespace and killed it mid-swap. v0.29.3.1 fix: a
  HELPER CONTAINER spawned via `docker run --rm --pid=host
  --net=host -v /var/run/docker.sock:... -v $SKYGATE_HOST_REPO_PATH:/host_repo:ro`
  — helper uses the HOST's PID namespace, installs docker-cli via
  apk, runs the full swap, polls /healthz on the new container
  for up to 60s, self-removes. `confirmPendingSwap` (called from
  `renderUpdatePage` on the first /admin/update load after the
  swap) does final-arbitration: detects `phase=build_done` or
  `phase=rolled_back`, calls `startStuckSkygateContainer` (with
  the `{{.Status}}` format-string fix for the v0.29.3 regression),
  promotes phase to `done` on /healthz 200. B13 (pre-push hook
  uses MSYSTEM for Git Bash detection) added.

* **Previous**: v0.29.2 — `skygate` host-side wrapper
  ([tag v0.29.2](https://github.com/skygate-operator/skygate/releases/tag/v0.29.2)).
  Removes `container_name: skygate` (and `caddy`) from
  docker-compose.yml to fix the `--force-recreate` race that
  occasionally left new containers in `Created` state. The
  auto-generated name (`skygate-skygate-1`, etc.) increments
  on every recreate, so the ~20 existing `docker exec skygate`
  callers (scripts, docs, verify_post_deploy.sh) all break
  unless we abstract. Solution: `deploy/skygate-cli.sh` — a
  host-side shell wrapper that does a label-based lookup
  (`com.docker.compose.service=skygate`) and forwards to
  `docker exec <real-id> ...`. Installed on the host by
  `deploy.sh` as `/usr/local/bin/skygate`. `verify_post_deploy.sh`
  also resolves `SKYGATE_CONTAINER` from the same label.
  B14 catalog check (wrapper exists + syntax-valid + uses
  correct label).

* **Previous**: v0.29.0 — Self-update orchestrator (in-app upgrade + auto-rollback)
  ([tag v0.29.0](https://github.com/skygate-operator/skygate/releases/tag/v0.29.0)).
  `/admin/update` page now has an "Apply update" button that
  runs an in-container orchestrator: backup tag, `git fetch`,
  `git checkout` target, rebuild image, recreate container,
  poll `/healthz` for 60s, auto-rollback on any failure.
  `SKYGATE_REPO_PATH` env (auto-detects container mode via
  `/.dockerenv` / `/run/.containerenv`); `SKYGATE_HOST_OWNER`
  override for non-standard UIDs (the orchestrator captures
  the host owner at job start so `git` mutations don't re-own
  bind-mounts to `root:root` and break the operator's `git
  pull` from the host shell). State file:
  `/data/skygate-update-status.json` (bind-mounted from host
  /home/admin/skygate/data/). 5 post-Phase-2 bugfixes
  shipped in v0.29.0+v0.29.1+v0.29.2+v0.29.3+v0.29.3.1.

* **Previous**: v0.28.5 — explicit opt-in for `via` constraint
  (Android-friendly) + tagged-device exit-node fix + idempotent
  migration + entrypoint always clears stale Tailscale exit-node
  ([tag v0.28.5](https://github.com/skygate-operator/skygate/releases/tag/v0.28.5)).
  Four patches: v0.28.5 (commit `206d26b`, the original
  Android-friendly opt-in), v0.28.5a (`1346f7d`, migration
  v0.47 idempotency), v0.28.5b (`1872f06`, loose per-device
  grant for tagged devices), v0.28.5c (`6e4000e`, entrypoint
  always passes `--exit-node=` to `tailscale up` to clear
  stale state). The motivation for the v0.28.6 guarantee
  catalog; without it, these three bugs passed `make test`
  and `make smoke`. Release notes in [`RELEASE-NOTES.md`](RELEASE-NOTES.md).

* **Previous**: v0.28.4 — per-device preferred exit-node
  ([tag v0.28.4](https://github.com/skygate-operator/skygate/releases/tag/v0.28.4)).
  The "workstation-3 → relay-3 override" release. v0.28.3 closed the
  exit-node bypass but pinned all of admin's devices to
  relay-1 (admin's per-user pref). v0.28.4 adds per-device
  prefs so a specific device can be pinned to a different
  exit-node than the user's default. Migration v0.46:
  `device_exit_node_prefs(user_id, device_hostname, exit_node_tag)`
  table. `GenerateACLWithViaForPlane` emits per-device grants
  BEFORE per-user grants (Tailscale first-match). The
  per-device grant covers ONLY autogroup:internet (the via
  override target); user's own stuff stays on the per-user
  grant. UI: `/my/devices` (self-service) + `/admin/devices`
  (operator override) — dropdown of available exit-nodes +
  pin/clear buttons. Endpoints: `POST /my/devices/preferred-exit`
  (caller-owns-device check via `node_owner_map`),
  `POST /admin/devices/preferred-exit`. 3 NEW ACL tests +
  1 UI hotfix (derive skygate user from dev tag, not
  `n.UserName` which is "tagged-devices" after headscale's
  tag-driven reassignment). All 17 packages green.
  Smoke RU+EN 83/83. Live: workstation-3 pinned to relay-3
  (per-device grant index 0; per-user grants at index 1+).
  The "workstation-3 без правил имеет доступ к сайтам и подсетям что только для
  workstation-1" fix. Catch-all `* → autogroup:internet` was the bypass:
  any device could use any exit-node for arbitrary internet
  destinations, including relay-3's 148 PrimaryRoutes. Fix has two
  parts: (1) per-user grant dst now includes `autogroup:internet`
  (every user can reach the internet through their own grant, and the
  via constraint pins them to their preferred exit-node if set);
  (2) catch-all src is changed from `*` to `tag:public` — only relay
  nodes can use `autogroup:internet` themselves (i.e. FORWARD
  exit-node traffic to the internet). 3 NEW tests + 5 UPDATED.
  `go test ./internal/acl/...` PASS. Smoke RU+EN 83/83. Live policy:
  4 per-user grants with `autogroup:internet` in dst, 3 with `via`,
  catch-all `tag:public → autogroup:internet` for relay forwarding.
  The "real proof that the per-user subnet-router flow
  works end-to-end" release. 5 things in one:
  1. `e2e_pilot.sh` (root) automates the full
     bundle-download → tailscale-register →
     sidecar-auto-approve → status-pill-router_active
     pipeline. Live-verified on skygate-host-1 2026-07-22
     (admin pilot, node id=26, route approved in 21s,
     status stable across multiple SyncOnce ticks).
  2. `headscale.AddTag` + Strategy C tag-respect
     fix — the backfill was silently clobbering
     `tag:subnet-router` → `tag:private` on every
     `/my/devices` load (headscale 0.29's `nodes tag
     --force` REPLACES tags, not appends). Two-line
     fix in `tags.go` + `handlers_node_ownership.go`.
  3. Sidecar `SyncOnce` now sets `status='router_active'`
     (not `'active'`) when the route is approved —
     pre-v0.22.3 binary value, v0.22.3 split it but the
     sidecar was never updated, so the status pill
     flickered every 30s. The unit test was renamed
     + updated to assert `StatusRouterActive`.
  4. `GET /healthz` + `GET /readyz` probes (1s cache,
     DB+headscale ping, 200 or 503). `SKYGATE_INSTANCE_ID`
     env. No actual HA infrastructure yet — the
     probes are the wiring for a future Tier 1 (1-2 day
     follow-up). `App.InstanceID/BuildVersion/StartedAt`
     fields, `BuildVersion = version + "+" + commit`.
  5. `scripts/check_subnet_router.sh <user>` — operator-side
     health check (DB + headscale + denorm + UI status +
     recent audit, exits 0/1/2 with [OK]/[WARN]/[FAIL]).
     Companion `scripts/_check_subnet_nodes.py` is the
     Python helper that `check_subnet_router.sh` shells
     out to. Plus docs/subnet-router.md rewritten with
     6 concrete use cases (home NAS, smart home, SOHO
     server room, family sharing, lab/dev, cross-site
     backup) and the e2e verification output.
  ([tag v0.26.0](https://github.com/skygate-operator/skygate/releases/tag/v0.26.0)).
  5 files new (e2e_pilot.sh, handlers_healthz.go,
  headscale/healthz.go, scripts/check_subnet_router.sh,
  scripts/_check_subnet_nodes.py), 10 files modified
  (backfill, tags, sidecar, handlers.go, main.go,
  bundle scripts, Makefile, docs/subnet-router.md),
  1 test renamed/updated. 17/17 packages green.
  check-bundles / check-nodes / check-https green.
  Smoke 79+79 pass, 4 fail in step 13 (multi-user
  mesh, pre-existing in v0.25.1, unrelated to v0.26.0,
  filed as v0.26.1 follow-up). No env-var changes,
  no schema migration, no breaking changes.
  ~830 lines added (5 files new, 11 modified, 1 test).

* **Previous**: v0.28.3 — close exit-node bypass
  ([tag v0.28.3](https://github.com/skygate-operator/skygate/releases/tag/v0.28.3)).
  The "workstation-3 без правил имеет доступ к сайтам и подсетям что только для
  workstation-1" fix. Catch-all `* → autogroup:internet` was the bypass:
  any device could use any exit-node for arbitrary internet
  destinations, including relay-3's 148 PrimaryRoutes. Fix has two
  parts: (1) per-user grant dst now includes `autogroup:internet`
  (every user can reach the internet through their own grant, and the
  via constraint pins them to their preferred exit-node if set);
  (2) catch-all src is changed from `*` to `tag:public` — only relay
  nodes can use `autogroup:internet` themselves (i.e. FORWARD
  exit-node traffic to the internet). 3 NEW tests + 5 UPDATED.
  `go test ./internal/acl/...` PASS. Smoke RU+EN 83/83. Live policy:
  4 per-user grants with `autogroup:internet` in dst, 3 with `via`,
  catch-all `tag:public → autogroup:internet` for relay forwarding.

* **Previous**: v0.28.2 — `hosts:` block workaround for headscale 0.29.2
  grants parser ([tag v0.28.2](https://github.com/skygate-operator/skygate/releases/tag/v0.28.2)).
  Workaround for headscale 0.29.2's grants parser (parseAlias does NOT
  split alias:port). Pre-collect all CIDRs referenced by a grant as
  host aliases in `hosts:` block, reference bare alias (no `:*`) in
  dst. `h-` prefix (headscale hostname validation rejects `:`). 6
  fix commits required to pass all 6 headscale errors. Final state:
  249 grants, 212 hosts, 3 per-exit-node tagOwners entries, via
  enforced for admin/user1/user2.

* **Previous**: v0.28.1 — per-user preferred exit-node (UI + data model)
  ([tag v0.28.1](https://github.com/skygate-operator/skygate/releases/tag/v0.28.1)).
  The "v0.28.1 data model" release. Migration v0.45:
  `user_exit_node_prefs` table. `GenerateACLWithViaForPlane` emits
  per-user grants with `via: ["<tag>"]`. `SKYGATE_ACL_VIA_ENABLED`
  config (default `false`). UI: `/admin/users/{id}/subnet` dropdown +
  `/my/exit-nodes` "Set as my preferred" button. 4 unit tests + 16
  i18n keys × 2 langs. **Known limitation**: headscale 0.29.2 grants
  parser rejects CIDR+port in dst (HTTP 500). Fix is v0.28.2.

* **Previous**: v0.28.0 — per-device ACL via `tag:dev-<user>-<device>`
  ([tag v0.28.0](https://github.com/skygate-operator/skygate/releases/tag/v0.28.0)).
  The "rules for workstation-1 should not propagate to workstation-3" release. The
  v0.27-and-earlier `device_ip` src was vulnerable to (a) Tailscale IP
  changes on reconnect, (b) any device acquiring the same IP
  inheriting the rule. v0.28.0: every device carries a unique
  per-user-per-device tag (e.g. `tag:dev-admin-workstation-1`); ACL
  references the tag as src. Tags survive IP changes, deterministic,
  headscale's tagOwners scopes per-user. Migration v0.44: `user_name`
  + `device_hostname` columns. 5 new tests + 8 i18n keys × 2 langs.

* **Previous**: v0.25.1 — Closing the loose ends (audit export + DR runbook + cleanup)
  ([tag v0.25.1](https://github.com/skygate-operator/skygate/releases/tag/v0.25.1)).
  The "before we add HA, let's clean up the corners"
  release. Three small items: (1) per-user audit log
  export (CSV/JSON) via GET /my/account/audit — each
  user (admin or not) can download their own audit
  trail, scoped by (user_id, username) OR-fallback so
  system events on the user's behalf (telegram_restart,
  etc.) are also included. (2) docs/disaster-recovery.md
  — full 15-min single-VM recovery runbook (RPO 1h, RTO
  30m, with quarterly DR drill cadence). (3) cleanup:
  .gitignore now ignores 22+ root-level scratch scripts
  (check_*.sh / verify_*.sh / test_*.sh / etc.),
  scripts/cleanup_orphan_meshes.sh ready to run for the
  21 v0.22.0-test meshes, per-user bot routing
  (v0.12.1 followup) closed retroactively (was already
  done in v0.12.0). 1 unit test (TestListAuditLogForUser)
  covers the audit export query. 17/17 packages green.
  No new env vars, no schema migration, no breaking
  changes. ~760 lines added (5 files new, 1 modified,
  1 .gitignore update).

* **Previous**: v0.25.0 — Mesh visibility on /my/devices + operator overview
  ([tag v0.25.0](https://github.com/skygate-operator/skygate/releases/tag/v0.25.0)).
  The "mesh view" UI release. Per the operator's spec
  (2026-07-21 22:40), each device on /my/devices now
  shows which virtual subnet it belongs to (e.g.
  "10.0.1.0/24" for tag:private devices, "shared" pill
  for tag:public/exit-node). The subnet card on
  /my/devices grows three new rows: "Mesh-сеть" (list
  of share-to / share-from), "Активные mesh-сети"
  (count + member list with their CIDRs), and the
  /admin/subnets page gets 3 new columns (Devices /
  Mesh / Shares) plus a global totals footer
  (Total devices / Active meshes / Sharing their /24
  / Shared with you). 18 new i18n keys × 2 langs (36
  entries). 17/17 packages green. No schema / env-var
  / package changes. ~329 lines added (handlers +170,
  templates +100, catalog +36, 2 × tests unchanged).
  The "per-user control plane" path (v0.23.0) is
  unchanged — v0.25.0 is purely UI on top of the
  default global-headscale path.

* **Previous**: v0.24.2 — Download bundle for per-user subnet-router
  ([tag v0.24.2](https://github.com/skygate-operator/skygate/releases/tag/v0.24.2)).
  The "user-friendly delivery" release. v0.24.0 shipped
  the setup.sh script and v0.24.1 fixed the /my/devices
  UI to show what each device does, but the operator
  still had to manually copy the script + the rendered
  `tailscale up` command into a chat. v0.24.2 ships
  `GET /admin/users/{id}/subnet/download` — a one-click
  flow that issues a fresh preauth, embeds it in a
  self-contained tar.gz, and returns the bundle as
  `application/gzip` with `Content-Disposition:
  attachment`. The bundle contains setup.sh + README.md
  (chmod +x) + commands.txt (chmod +x, with the preauth
  key and CIDR already filled in) + CIDR.txt. User
  scps the bundle to their router, untars, runs
  `sudo bash commands.txt`, and the rest is the same
  v0.24.0 5-step flow. New `make sync-bundles` +
  `make check-bundles` targets keep the embed copies
  of setup.sh / README.md in
  `internal/handlers/bundles/` in sync with the
  canonical `deploy/subnet-router/`. `docs/subnet-router.md`
  got three new top-level sections: TL;DR (concrete
  examples of what works after setup), Quick start
  (3-command path for users who already have
  tailscaled), What to download (GitHub raw URLs).
  2 new i18n keys × 2 langs. 17/17 packages green.
  No env-var changes, no schema migration. Same 4
  prod users, same subnet allocations.

* **Previous**: v0.24.1 — /my/devices shows tag:subnet-router + advertised routes
  ([tag v0.24.1](https://github.com/skygate-operator/skygate/releases/tag/v0.24.1)).
  The "what does this device actually do" UI fix. v0.24.0
  shipped `deploy/subnet-router/setup.sh` so users could
  *register* a subnet-router, but the /my/devices page
  showed every node with the same `tag:private` badge and
  the same `100.64.0.X` IP — no way for the user to see
  which device was their LAN bridge, or which routes it
  was advertising. v0.24.1 adds a "Subnets" column
  (shows every node's `AvailableRoutes` as small badges,
  with a "pending" pill if any route is waiting for admin
  approval) and a 4-state tag column
  (subnet-router → blue / exit-node → amber / public →
  green / private → grey). 5 new i18n keys × 2 langs
  (10 entries). 17/17 packages green. No Go schema /
  env-var / package changes. No Go code outside
  `handlers_my_devices.go` (+22 lines) and the template
  (+24 lines). Release also includes the "what is still
  left for full migration to per-user subnets + mesh"
  answer the operator asked for (4 legs: 1 mechanical, 2
  code-done-not-used, 1 external-blocked-on-headscale-0.30+).

* **Previous**: v0.24.0 — subnet-router setup tooling
  ([tag v0.24.0](https://github.com/skygate-operator/skygate/releases/tag/v0.24.0)).
  The "operator guide for getting a per-user subnet-router
  running end-to-end" release. Backend
  (`sidecar.SyncOnce` / `GeneratePreauth` /
  `BuildPreauthInfo`) has been in place since v0.16.7 but
  no operator-facing tooling existed. v0.24.0 ships
  `deploy/subnet-router/setup.sh` (runs on the user's
  RPi/NAS/mini-PC, takes a preauth from the admin,
  executes `tailscale up` with the correct flags + prints
  next-steps), `docs/subnet-router.md` (full user guide:
  5-step setup, troubleshooting, security notes), and
  `deploy/subnet-router/allocate-existing-users.sh` (one-off
  for backfilling users that were created before the
  v0.20.0 auto-allocate). **No Go code touched**, no new
  env vars, no schema migration, no new i18n keys, no
  UI changes. Same 256-user /24-per-user limits, same
  status semantics (pending ⇔ no router, router_active ⇔
  tag:subnet-router up). Production state: admin =
  10.0.1.0/24 active (pilot since v0.16.6), user1 =
  10.0.6.0/24 pending, user3 = 10.0.9.0/24 pending, user2
  = 10.0.10.0/24 pending. When each user runs
  `setup.sh`, the sidecar's 30s tick auto-approves the
  route and flips status to `router_active` within ~30s.

* **Previous**: v0.23.4 — expirewatch: skip nil-expiry nodes, not all tagged
  ([tag v0.23.4](https://github.com/skygate-operator/skygate/releases/tag/v0.23.4)).
  Hotfix for v0.23.3. The v0.23.3 watcher skipped ANY tagged
  node — but a user device that registers untagged (and
  picks up the Tailscale 1.98.x `RegisterRequest.Expiry =
  now+2-4s`) is then tagged `tag:private` by skygate's
  backfill on the next `/my/devices` load. Result: a node
  that's tagged AND has a 2-4s Expiry — which the v0.23.3
  rule froze in place. Symptom observed in production at
  16:01 on 2026-07-21: operator's Android (workstation-2, id=10)
  expired, watcher logs showed `seen=18 renewed=0
  skipped=18` every 5m. The v0.23.4 fix: skip only when
  `n.Expiry == ""` (covers `tag:exit-node`/`tag:public`/
  `tag:subnet-router` and any node the operator ran
  `--disable` on). Tagged nodes with a real Expiry
  (`tag:private` user devices) are now renewed just like
  untagged ones. The change is a 1-line edit to
  `SyncOnce` and removal of the `isTagged` helper.
  `TestExpireWatch_SkipsTagged` →
  `TestExpireWatch_SkipsOnlyNilExpiry` (4 sub-cases),
  `TestExpireWatch_HandlesMissingExpiry` removed
  (the "defensive renew for nil expiry" behaviour is
  gone — it would override `--disable`). 7/7 expirewatch
  tests PASS, 17/17 packages green. No env-var changes,
  no new i18n keys, no schema migration. Same defaults
  as v0.23.3 (5m / 7d / 30d). Live verification: after
  deploy, `docker logs skygate | grep expirewatch.tick`
  should show `seen=18 renewed=5 skipped=13 errors=0`
  (the 5 renewed are the `tag:private` nodes with
  near-expiry: workstation-2, workstation-2-old, Nothing Phone, Base,
  workstation-4; the 13 skipped are relay-1, relay-2,
  relay-3 + the 7 `agent*` test nodes from v0.23.3
  verification + skygate-host-1 which has nil Expiry).

* **Previous**: v0.23.3 — node-expiry
  watcher (the "device
  won't stay connected"
  release)
  ([tag v0.23.3](https://github.com/skygate-operator/skygate/releases/tag/v0.23.3)).
  Background goroutine in
  `internal/expirewatch` ticks
  every 5m, walks every node in
  headscale, and extends any node
  whose Expiry is missing or within
  7d of "now" out to 30d. Works
  around a Tailscale 1.98.x client
  behaviour where
  `RegisterRequest.Expiry` is only
  2-4s in the future and headscale
  0.29.x applies that Expiry verbatim
  — without the watcher, every fresh
  preauth-registered device gets
  force-logged-out within seconds.
  Discovered 2026-07-21 with the
  operator's Android phone (node 10 /
  workstation-2): manual `headscale nodes
  expire -i 10 --expiry +30d` was
  the one-shot fix; v0.23.3 makes it
  automatic. 4 new env vars
  (`SKYGATE_EXPIREWATCH_ENABLED` /
  `_INTERVAL` / `_THRESHOLD` /
  `_RENEWAL`, defaults `true` / `5m` /
  `168h` / `720h`); no `/admin/*`
  knobs (defaults are sensible).
  `NodeView.Expiry` added to the
  headscale client (was previously
  missing — required an extra
  `/api/v1/node/{id}` round-trip per
  node per watcher tick). **v0.23.4
  fix**: the original "skip any tagged
  node" rule was wrong (see Current
  above) and was replaced with "skip
  only nodes whose Expiry is nil".
  8 unit tests in
  `internal/expirewatch/manager_test.go`
  (PicksOnlyNearExpiry /
  SkipsTagged / HandlesMissingExpiry /
  RespectsIntervalZero /
  RunStopsOnContextCancel /
  RecordsAuditOnRenew /
  ParsesRFC3339NanoExpiry /
  HandlesAPIFailure), all PASS.
  The "v0.23.0 is for compliance, not
  default path" release. v0.23.0 shipped
  one-click per-user headscale
  provisioning; v0.23.1 makes explicit
  the cost (re-auth all devices + lose
  shared exit-nodes + lose mesh bridges)
  via a warning card on
  `/admin/users/{id}/plane`. New
  `check_cross_subnet_v0.23.1.sh` is an
  11-step live verification proving that
  the existing global headscale already
  delivers per-user subnets + shared
  exit-nodes + mesh for the 4 prod
  users — per-user control plane is
  not needed for the operator's actual
  goals. Use v0.23.0 only for compliance
  tier (SOX, multi-tenant SaaS,
  geographic isolation).
  Closes the v0.12.0 capability
  gap that left per-user control
  planes as a manual ssh + docker
  + headscale CLI flow. The
  bootstrap script
  (`deploy/headscale-users/headscale-bootstrap.sh`)
  creates a per-user docker
  container (port 50450+uid%50,
  base_domain `<username>.tsnet.example.com`),
  issues a 10-year API key, returns
  JSON. The handler encrypts the
  key with SKYGATE_SECRET_KEY
  and persists to
  `portal_users.headscale_api_key_enc`.
  The deprovision script
  (`headscale-deprovision.sh`)
  tears down + preserves the
  per-user data dir for recovery.
  `internal/headscale/provision.go`
  is a Go wrapper (8 unit tests,
  all PASS). Skyadmin pilot
  verified live: container up +
  healthy, DB has the URL + encrypted
  key, /admin/users/1/plane shows
  the post-provision UI. 11/11
  check_v0.23.0.sh steps PASS.
  Smoke 83/83 still green. **Phase 1
  is infrastructure only — no data
  migration yet.** admin still
  uses the global headscale for
  all node operations. Phase 2
  (v0.23.1) is the data migration
  step.
  The "why is my subnet `pending`?"
  release. Pre-v0.22.3 the status
  semantics was `active` ⇔
  subnet-router up, which left
  every user in `pending` because
  nobody deployed a sidecar. v0.22.3
  flips it: `pending` ⇔ 0 devices
  in tailnet, `active` ⇔ ≥1 device
  (logical namespace),
  `router_active` ⇔ bonus on top
  (real subnet-router up too).
  `subnet.SyncStatus(db, uid, hasRouter)`
  encapsulates the new logic; called
  from `backfillNodeOwnership` after
  every `/my/devices` load. UI gets
  colored pills (green/green/yellow/muted)
  on `/admin/users/{id}/subnet` +
  `/admin/users` subnet column, plus
  a new "Your personal subnet" card
  on `/my/devices`. 7 new unit tests
  in `internal/subnet/manager_test.go`
  (PendingWhenNoDevices / ActiveWhenDevices /
  RouterActiveWhenHasRouter / DisabledPreserved
  / NoSubnetRow / Idempotent / SetStatusAcceptsRouterActive).
  8 files, +405/-18 lines, 7 new tests,
  smoke 83/83 still green. For the 4
  production users (admin/user1/
  user3/user2) their subnets flip
  from `pending` to `active` on the
  next `/my/devices` load — user3
  (0 devices) stays `pending`, which
  is the intended behavior.

* **Previous**: v0.23.0 — one-click
  per-user headscale
  provisioning (Phase 1)
  ([tag v0.23.0](https://github.com/skygate-operator/skygate/releases/tag/v0.23.0)).
  Closes the v0.12.0 capability
  gap that left per-user control
  planes as a manual ssh + docker
  + headscale CLI flow. The
  bootstrap script
  (`deploy/headscale-users/headscale-bootstrap.sh`)
  creates a per-user docker
  container (port 50450+uid%50,
  base_domain `<username>.tsnet.example.com`),
  issues a 10-year API key, returns
  JSON. The handler encrypts the
  key with SKYGATE_SECRET_KEY
  and persists to
  `portal_users.headscale_api_key_enc`.
  The deprovision script
  (`headscale-deprovision.sh`)
  tears down + preserves the
  per-user data dir for recovery.
  `internal/headscale/provision.go`
  is a Go wrapper (8 unit tests,
  all PASS). Skyadmin pilot
  verified live: container up +
  healthy, DB has the URL + encrypted
  key, /admin/users/1/plane shows
  the post-provision UI. 11/11
  check_v0.23.0.sh steps PASS.
  Smoke 83/83 still green. **v0.23.0
  is infrastructure only — no data
  migration. v0.23.1 follows up
  with the compliance-tier warning
  + the cross-subnet verification
  (proves global headscale already
  gives the operator per-user subnets
  + shared exit-nodes + mesh without
  needing per-user control plane).**

* **Previous**: v0.22.3 — subnet
  status reflects device
  ownership, not subnet-router
  ([tag v0.22.3](https://github.com/skygate-operator/skygate/releases/tag/v0.22.3)).
  The "why is my subnet `pending`?"
  release. Pre-v0.22.3 the status
  semantics was `active` ⇔
  subnet-router up, which left
  every user in `pending` because
  nobody deployed a sidecar. v0.22.3
  flips it: `pending` ⇔ 0 devices
  in tailnet, `active` ⇔ ≥1 device
  (logical namespace),
  `router_active` ⇔ bonus on top
  (real subnet-router up too).
  `subnet.SyncStatus(db, uid, hasRouter)`
  encapsulates the new logic; called
  from `backfillNodeOwnership` after
  every `/my/devices` load. UI gets
  colored pills (green/green/yellow/muted)
  on `/admin/users/{id}/subnet` +
  `/admin/users` subnet column, plus
  a new "Your personal subnet" card
  on `/my/devices`. 7 new unit tests
  in `internal/subnet/manager_test.go`
  (PendingWhenNoDevices / ActiveWhenDevices /
  RouterActiveWhenHasRouter / DisabledPreserved
  / NoSubnetRow / Idempotent / SetStatusAcceptsRouterActive).
  8 files, +405/-18 lines, 7 new tests,
  smoke 83/83 still green. For the 4
  production users (admin/user1/
  user3/user2) their subnets flip
  from `pending` to `active` on the
  next `/my/devices` load — user3
  (0 devices) stays `pending`, which
  is the intended behavior.

* **Previous**: v0.22.2 — fix
  auto-apply tag:private for
  tagless nodes (MSI bug)
  ([tag v0.22.2](https://github.com/skygate-operator/skygate/releases/tag/v0.22.2)).
  The operator reported on
  2026-07-20 that MSI (id=15),
  registered via skygate preauth
  (id=98), never received
  tag:private in headscale. Root
  cause: backfillNodeOwnership's
  Strategy A branch set
  matchedTag = firstTagOrFallback(n),
  which returns "tag:untagged" for
  tagless nodes. The subsequent
  branch check `if matchedTag ==
  "tag:private"` failed, so
  HS.TagNode(15, "tag:private") was
  NEVER called. Strategy C had the
  same bug; it was fixed on
  2026-07-10 but Strategy A was
  missed. v0.22.2 fix applies the
  same override to Strategy A:
  when the preauth key came from
  skygate, default matchedTag to
  "tag:private". firstTagOrFallback
  is only used when the node ALREADY
  has tags (e.g. skygate-host-1 has
  tag:private in headscale, so the
  result is unchanged for that
  case). Two new tests in
  internal/handlers/handlers_node_ownership_test.go
  pin the fix. 8/8 live-validation
  checks PASS on the VM
  (check_v0.22.2.sh). Smoke 83/83
  (EN 83 + RU 83), check_exit_nodes
  3/3, check_https PASS.

* **Previous**: v0.22.1 — /my/meshes
  web UI (was bot-only in v0.22.0)
  ([tag v0.22.1](https://github.com/skygate-operator/skygate/releases/tag/v0.22.1)).
  v0.22.0 shipped the mesh (shared
  network) feature bot-only
  (/mesh create|join|leave|meshes).
  The operator flagged that users have
  no obvious place in the WEB interface
  to (1) create a shared network, (2)
  enter an invite code from another user.
  v0.22.1 fixes the gap: GET /my/meshes
  + 3 POST routes (create, join, leave)
  with the same form-based UX as
  /my/tokens / /my/devices. Web + bot
  share the same internal/mesh package
  state, so a mesh created via the web
  shows up in the bot's /meshes list (and
  vice versa). Sidebar entry + 34 new
  i18n keys (RU+EN, 68 entries). 10/10
  live-validation checks PASS on the VM
  (caught a real i18n-key-prefix bug in
  the first deploy; hotfix on top of
  the initial v0.22.1 commit). Smoke
  132/132 (EN 66 + RU 66), check_exit_nodes
  3/3, check_https PASS.

* **Previous**: v0.22.0 — mesh (shared
  network) + safe user migration design
  ([tag v0.22.0](https://github.com/skygate-operator/skygate/releases/tag/v0.22.0)).
  The 3rd primitive in the user-to-user
  networking stack (after the v0.17.1
  one-directional share + v0.21.0
  one-on-one invite bridge). A mesh is
  a named group of users whose personal
  subnets are all mutually visible to
  each other — like radmin VPN's
  "shared network". N-way bridge,
  automatic, deduped with v0.17.1 share
  rows. Migration v0.43 adds
  `meshes` + `mesh_members` tables.
  Bot commands `/mesh create|join|leave`
  + `/meshes` (user-scope) drive the
  workflow; `/admin/meshes` (admin-only,
  read-only) is for oversight. The
  operator's 2026-07-20 backlog message
  asked for this + 3 concerns about
  cross-subnet ACL, exit-node global
  access, and admin migration — all
  three verified by Phase 1 (12
  integration tests, all PASS locally)
  + Phase 1b (7 live-validation checks
  on real headscale round-trip, all
  PASS on VM). 18 files, +1932/-8
  lines, 130/130 smoke + 3/3
  check_exit_nodes + check_https PASS.
  Phase 3 (the safe user migration
  tool) is explicitly DEFERRED to a
  follow-up release — the operator's
  "только после проверки и гарантии
  работы" is honored literally, and
  the migration tool is a separate,
  opt-in, audit-tracked operation.

* **Previous**: v0.21.1 — fix headscale-side
  user delete (typo: `-u` should be `-i`)
  ([tag v0.21.1](https://github.com/skygate-operator/skygate/releases/tag/v0.21.1)).
  Pre-existing bug discovered while cleaning up
  test users after v0.21.0. Every
  `POST /admin/users/{id}/delete` left a
  stale "orphan" headscale user behind,
  surfacing as the "HSOrphans" banner on
  `/admin/users`. The root cause: a typo in
  the headscale CLI args — the code used
  `users delete -u -f <id>` but headscale's
  `users delete --help` shows the correct
  flag is `-i, --identifier` (the `--force`
  global flag has no short alias in 0.29.x).
  The audit log captured every failed
  attempt with `Error: unknown shorthand
  flag: 'u' in -u`. Fix: `-u -f <id>` →
  `-i <id> --force` in
  `internal/headscale/users.go`, extracted
  to a `deleteUserCmd` method for
  testability. Three new regression tests
  assert the correct args and reject the
  pre-fix shape. The 4 existing orphans
  from v0.21.0 test user cleanup get removed
  by a post-deploy manual `docker exec ...
  headscale users delete -i <id> --force`
  per orphan. After the post-deploy cleanup,
  `/admin/users` no longer shows the
  HSOrphans banner. Smoke 126/126 still
  green.

  **What comes next**: the three "close the
  backlog" features from the 2026-07-20
  message are done. v0.19.1 (the re-attempt
  of the reverted v0.19.0 dns.extra_records
  feature) is still blocked on headscale
  0.30+ — the weekly mavis cron
  (`headscale-milestone-16-check`) checks
  headscale milestone #16 (DNS Work) every 7
  days and reports if any progress lands.

* **Previous**: v0.21.0 — user-to-user subnet
  bridge (invite codes + bot /invite + /accept +
  /admin/invites)
  ([tag v0.21.0](https://github.com/skygate-operator/skygate/releases/tag/v0.21.0)).
  Closes the third feature the operator asked
  for in the 2026-07-20 backlog message. The
  v0.17.1 admin-mediated "share" path is
  unchanged; v0.21.0 adds the user-mediated
  path: A generates a code, B types it in the
  bot, the bridge auto-applies. New
  `invite_codes` table (migration v0.42) with
  a 32-char alphabet code (8 chars, ~1.1T
  possibilities, 7-day TTL). Bot commands:
  `/invite <username>` (grantor side, generates
  a code), `/accept <code>` (grantee side,
  validates + atomically consumes + applies the
  bridge via `invite.ApplyBridge` which writes
  a `user_subnet_shares` row + triggers the
  per-plane ACL re-apply goroutine), `/invites`
  (list the caller's outstanding + incoming
  invites, 10 per side). Admin UI:
  `/admin/invites` (admin-only overview with a
  Revoke button for active rows). The bot path
  does NOT require admin; the bridge row is
  written the same way the admin share would
  write it. `grantee_username` is TEXT (not an
  FK) so A can invite "bob" before bob has a
  skygate account — the consume path resolves
  the username to a user_id at consume time.
  16 files, +2348/-2 lines, smoke 126/126
  (EN 63 + RU 63), check_exit_nodes 3/3,
  check_https PASS.

  **v0.21.0 hotfix** (commit `cb94b37`,
  shipped immediately after v0.21.0):
  `cmd/skygate/main.go` had a duplicate
  registration of the `/admin/headscale` route
  (introduced by the v0.21.0 edit pattern that
  matched the v0.20.0 insertion twice). The
  first deploy of v0.21.0 panicked on boot
  with `pattern "GET /admin/headscale"...
  conflicts with pattern "GET /admin/headscale"`.
  The hotfix removes the duplicate, leaving
  the v0.20.0 registration (lines 320+325) as
  the single source of truth. Build verified
  live on VM; smoke 126/126 again.

  **What comes next**: the three "close the
  backlog" features from the 2026-07-20
  message are done. v0.19.1 (the re-attempt
  of the reverted v0.19.0 dns.extra_records
  feature) is still blocked on headscale
  0.30+ — the weekly mavis cron
  (`headscale-milestone-16-check`) checks
  headscale milestone #16 (DNS Work) every 7
  days and reports if any progress lands.

* **Previous**: v0.20.0 — headscale-update-monitor +
  auto-allocate subnet on user create
  ([tag v0.20.0](https://github.com/skygate-operator/skygate/releases/tag/v0.20.0)).
  Two operator-side UX cleanups bundled because
  they're both small and the operator asked for
  them in the v0.18.1 retro:

  1. **`/admin/headscale` page + monitor goroutine**
     — polls the juanfont/headscale GitHub
     Releases API every 24h (configurable via
     `SKYGATE_HEADSCALE_POLL_INTERVAL`), compares
     the latest tag against the operator's pinned
     version (`SKYGATE_HEADSCALE_VERSION_PIN`,
     e.g. "0.29.2"), and dispatches a Telegram
     alert + writes a row to `headscale_releases`
     when a newer version is available. New bot
     command `/headscale` (admin-only) renders
     the same status. `/admin/exit-nodes` gets
     a banner above the table when a newer
     headscale is known. `headscale_releases`
     table (migration v0.41) holds the history
     so the page has a "seen releases" view that
     survives skygate restarts. Page has a
     "Check now" button for an immediate re-poll.
     GitHub rate limit: 60 req/h unauthenticated;
     24h polling leaves 56/60 unused.

  2. **Auto-allocate subnet on user create** —
     `PostAdminUser` now calls `subnet.Create(userID)`
     automatically after the `portal_users` row
     is inserted, controlled by
     `SKYGATE_AUTO_ALLOCATE_SUBNET` (default
     `true`). The operator's stated preference
     was "by default, not via a separate button
     click". The manual "Allocate" button on
     `/admin/users/{id}/subnet` is unchanged
     (re-issue / disabled→re-allocate flows).
     `subnet.Create` is idempotent, so the
     button is safe to click even with auto-
     allocate enabled. Allocations failures are
     logged but don't roll back the user
     (the user is still created; the operator
     can retry via the manual button). The
     audit row records both `user_create` and
     the `subnet_allocate` outcome.

  19 files changed, +1740/-8 lines. Migration
  v0.41 adds the `headscale_releases` table.
  Config: `SKYGATE_HEADSCALE_VERSION_PIN`,
  `SKYGATE_HEADSCALE_POLL_INTERVAL`,
  `SKYGATE_AUTO_ALLOCATE_SUBNET`. Verified live
  on VM: smoke 122/122 (EN 61 + RU 61),
  check_exit_nodes 3/3, check_https PASS, "Check
  now" button end-to-end works (writes
  v0.29.2 to headscale_releases with
  is_breaking=0, notified=0 because it matches
  the pinned version).
  ([tag v0.18.1](https://github.com/skygate-operator/skygate/releases/tag/v0.18.1)).
  Operator-flagged issues from the v0.18.0 deploy,
  all closed in one small release:

  1. **`check_https.py` HSTS /login 404** — the VM
     uses openresty (not Caddy as the docs say) and
     openresty 404s `/login`. `check_hsts` now falls
     back to `/`, `/api/v1/apikey` in order and
     accepts HSTS from whichever path returns a real
     response. 4 new regression tests in
     `scripts/test_check_https.py`. `make test` is
     now FULLY green.

  2. **`/admin/exit-nodes` "Tag as exit-node" /
     "Untag" buttons** — replaces the operator's
     two manual `docker exec headscale headscale
     nodes ...` invocations (approve-routes + tag)
     with a single click. Approves ONLY
     `0.0.0.0/0` + `::/0` (NOT the full
     availableRoutes set, to avoid accidentally
     approving relay-3's 200+ subnets). Applies
     `tag:exit-node`. New headscale API
     `ApproveRoutesForNodeID`. 4 new handler tests
     + 6 new i18n keys (RU+EN).

  3. **`ControlURL` auto-injection in
     `renderWithLayout`** — the `/admin/exit-nodes`
     Step-2 tutorial and `/my/preauth` result page
     rendered with an EMPTY `--login-server=`
     because the handlers didn't pass ControlURL in
     the data map. `renderWithLayout` now
     auto-injects `data["ControlURL"] = a.ControlURL`
     on every page render. The operator's
     `SKYGATE_CONTROL_URL` env var flows through
     `New(...)` → `App.ControlURL` → data map →
     template. 2 new regression tests in
     `handlers_test.go`.

  12/12 packages green, smoke 118/118, live at
  build `45d25a9`.

  **Note on the v0.19.0 attempt (reverted)**: a
  v0.19.0 release was deployed briefly and then
  reverted (commit `0c394bd`) because the
  `exitnode.skygate-subnet-<user>.<workstation-8-domain>`
  DNS-record feature relied on headscale's
  `dns.extra_records` policy field, which
  headscale 0.29.x (the operator's version —
  0.29.2 as of 2026-07-20) doesn't support —
  pushing a policy with the `dns` key returns
  `unknown field: "dns"` and the policy is rejected.
  The v0.16.0+ subnets roadmap's "exitnode" record
  is **blocked on headscale 0.29.x** and will
  return as v0.19.1 once the operator upgrades
  headscale to a version that supports
  `dns.extra_records` (0.30+ based on headscale
  changelog history — v0.30.0 was removed from
  the "unreleased" section of headscale's
  CHANGELOG in commit 8eea894, which suggests
  it's close). The schema migration
  (`preferred_exit_node_id` column), helper
  functions, and the per-user-subnet UI/bot code
  paths are all in git history (commit `646f8fb`)
  and can be re-enabled cheaply via
  `git revert 0c394bd && git push` once the
  headscale upgrade lands.

  **Note on the headscale 0.29.2 upgrade (2026-07-20)**:
  the operator upgraded headscale from
  `headscale/headscale:0.29.1` to
  `headscale/headscale:0.29.2` (commit
  `8eea89488c642f3d5f617fab5493d5f51f6f4ad0`,
  build 2026-07-01). Three bugfixes ship in
  0.29.2 (none of which add `dns.extra_records`,
  so v0.19.0 is still blocked):

  1. **Map-generation serialization fix (#3358)**
     — fixes a stall on the policy lock that
     could push clients into `unexpected EOF`
     retry loops during a mass reconnect on
     `autogroup:self`, via or relay policies.
     **Relevant to us**: the policy uses
     `autogroup:self` (admin→tag:public, admin→
     tag:exit-node SSH rules) and we have 3
     relays in the mesh, so a relay hiccup or
     a mass-reconnect event would have hit
     this. Now safe.
  2. **`/ts2021` WebSocket GET fix (#3359)** —
     previously returned 405 to Tailscale
     JS/WASM control clients. Verified live:
     `curl -H 'Connection: Upgrade' -H
     'Upgrade: websocket' http://localhost:50444/
     ts2021` now returns `101 Switching Protocols`
     with a valid `Sec-Websocket-Accept`. (Note:
     openresty on the VM does NOT yet forward
     WebSocket Upgrade headers — `https://head.
     example.com/ts2021` still 500s. Tailscale
     native clients don't use this path, so
     the tailnet itself is unaffected; only
     a future JS/WASM client deployment would
     need an openresty config change. Out of
     scope for this upgrade.)
  3. **Invalid FQDN handling (#3349)** —
     nodes with empty or too-long FQDNs no
     longer fail map delivery; the offender
     is logged at startup with the fix
     command. Defensive: we don't have any
     such nodes today, but it's nice to have.

  **Upgrade procedure used** (reproducible for
  future bumps):
  1. Backup SQLite DB + config to
     `/tmp/headscale-backup-<timestamp>/` via
     a throwaway `alpine:3.20` container
     `docker run --rm -v
     headscale_headscale_data:/from:ro -v
     $BACKUP_DIR:/to alpine:3.20 cp -a /from/.
     /to/`. The headscale_data volume isn't
     readable by admin directly, so the
     throwaway container is the cleanest path.
     `acl.hujson` (399 B, generated) +
     `acl_policy.hujson` (11 B, the live
     config-file policy) + db.sqlite (8.3 MB)
     + db.sqlite-wal (4 MB) = 12 MB total.
  2. `sed -i 's|0.29.1|0.29.2|g'`
     `/home/admin/headscale/docker-compose.yml`
     (the headscale compose lives outside the
     skygate repo, in `/home/admin/headscale/`)
  3. `docker compose stop headscale && docker
     compose up -d --force-recreate headscale`
     — came up in 3 s, no policy churn
     (`updatedAt` unchanged from the v0.17.1
     deploy at `2026-07-20T09:37:26Z`).
  4. Verification: 11 nodes (8 online, 3
     offline, same as before), 256 ACL rules
     unchanged, 4 tagOwners unchanged (tag:exit-
     node, tag:private, tag:public,
     tag:subnet-router), 2 SSH rules unchanged,
     4 groups unchanged. `make test` 118/118
     PASS (smoke 59+59 en+ru), `check_exit_nodes
     .py` 3/3 PASS, `check_https.py` PASS via
     `/` fallback.

  **Why no skygate release tag for this?**
  This is a pure ops-level headscale image bump
  — no skygate code changed, no new i18n keys,
  no API surface delta. The next skygate release
  (whatever it ends up being — likely the v0.19.1
  re-attempt once headscale 0.30+ lands) will
  have the headscale version in its release
  notes. For now the v0.19.0 blocker note above
  is the only consumer-facing reference.

* **Previous**: v0.18.0 — MagicDNS for personal
  subnets
  ([tag v0.18.0](https://github.com/skygate-operator/skygate/releases/tag/v0.18.0)).
  Roadmap step 5 of the v0.16.0+ per-user subnets
  plan. Each user's sidecar now has a stable,
  auto-resolving FQDN
  (`skygate-subnet-<username>.tsnet.example.com`)
  so tailnet clients can reach the user's
  `10.0.<uid>.0/24` subnet without remembering
  the sidecar's tailnet IP. New
  `internal/subnet/magicdns.go` (pure string
  functions `ComputeMagicDNSNames` +
  `FormatMagicDNSNames`, no DB). Admin UI:
  `/admin/users/{id}/subnet` gets a "DNS имена"
  `<details>` card; `/admin/subnets` gets a new
  "DNS (MagicDNS)" column. Bot: `/mysubnet` reply
  appends a "MagicDNS" section. 12 new i18n keys
  (6 admin + 5 bot + 1 col_dns) RU+EN. 4 new
  unit tests in `magicdns_test.go`.
  `BaseDomain = "tsnet.example.com"` matches
  `internal/acl/acl.go`'s `baseDomain` constant.
  The `exitnode.skygate-subnet-<user>` special
  record is NOT shipped in v0.18.0 (headscale 0.29
  doesn't support per-user service records);
  v0.19.0 is the planned home. 12/12 packages
  green, smoke 118/118, live at build `8d722af`.

  2. **Auto-reapply ACL on Allocate/Share/Revoke** —
     the v0.17.0 caveat ("click Re-apply ACL to push
     the new rule") is closed. New subnets are
     routable within ~1s of allocation.

  Files:
  - `internal/db/migrations_v0.39.go` +
    `portal_users.go` + `queries.go` —
    `user_subnet_shares` table, FK CASCADE,
    `GetSharedSubnetsForPlane` query
  - `internal/subnet/shares.go` (new) — `Grant`,
    `Revoke`, `ListSharedBy`, `ListSharedWith`,
    `ErrSelfShare`, `ErrShareNotFound`
  - `internal/acl/acl.go` — per-user dst list now
    includes every grantor's CIDR shared with the
    user
  - `internal/handlers/admin_user_subnet.go` —
    `PostAdminUserSubnetShare` / `Revoke` +
    auto-reapply on `Allocate`
  - `internal/handlers/templates/admin/user_subnet.html` —
    Cross-user sharing card with two columns +
    share form
  - `internal/telegram/commands.go` +
    `commands_user.go` — `/mysubnet share|revoke`
    subcommands
  - `internal/i18n/catalog.go` — 23 new keys × 2
    langs (12 admin + 11 bot)
  - 8 new tests (6 subnet + 2 ACL)

  12/12 packages green, smoke 118/118, live on VM
  at build `2c8176c`.
* **Previous**: v0.16.7 — per-user subnet sidecar
  (auto-approver + preauth)
  ([tag v0.16.7](https://github.com/skygate-operator/skygate/releases/tag/v0.16.7)). Real
  sidecar runtime for the v0.16.0+ subnets feature
  (the schema shipped in v0.16.6, the UI in v0.16.8,
  the sidebar fix in v0.16.9). Adds:
  - `internal/sidecar/` package (~700 lines):
    Manager with GeneratePreauth (tag:subnet-router,
    1h TTL, single-use), SyncOnce (auto-approves
    routes + flips status active/disabled based on
    headscale state), Run (30s ticker), LastStats
    for admin UI
  - Admin UI: `/admin/users/{id}/subnet` "Issue
    preauth key" button + suggested `tailscale up`
    command snippet
  - Bot: `/mysubnet provision` — same preauth in
    chat reply (butler voice)
  - headscale API: `CreatePreauthKeyWithTags` for
    `tag:subnet-router` preauth; `ApprovedRoutes`
    field on NodeView (was only `AvailableRoutes`)
  - 11 new sidecar tests + 1 new admin handler test
    + 2 new bot tests
  - 2 critical fixes during the first deploy:
    `go sidecarMgr.Run(ctx)` (was inline, blocked
    main before HTTP could bind) +
    `HSForUser(0)` short-circuit (avoids 30s log spam
    for the global-plane sentinel)
  - 12/12 packages green, smoke 118/118, live on VM
    at build `ac73b8c`.
* **Previous**: v0.16.8 — UI: Subnet column + button
  in /admin/users
  ([tag v0.16.8](https://github.com/skygate-operator/skygate/releases/tag/v0.16.8)). The
  v0.16.6 release shipped the
  `/admin/users/{id}/subnet` page (4 routes, full
  template) but the page was unreachable from the UI
  — no link from `/admin/users`, no sidebar entry, no
  "Subnet" column. Operator reported "where are the
  buttons?". Fix: extend `User` struct with the 3
  v0.16.6 denorm fields, extend
  `qSelectAllPortalUsers` from 6 to 9 columns, add a
  "Subnet" column to `/admin/users` (CIDR + status
  pill: green active / amber pending / muted disabled
  / dim "—" none) and a "Subnet" link in the per-user
  `<details>` menu. 6 new i18n keys (RU+EN). 2 new
  tests. 12/12 packages green, smoke 118/118, live
  on VM at build `3fc44a2`.
* **Previous**: v0.16.7 — hotfix: t vs tf arg count
  in update banner
  ([tag v0.16.7](https://github.com/skygate-operator/skygate/releases/tag/v0.16.7)). The
  v0.16.6 release shipped an "update available" banner
  with `{{t "update.banner_body" .Version
  .UpdateLatest.TagName}}` — but `t` takes 1 arg, the
  call had 3. Every admin page rendered with only the
  banner (the only thing that survives a template
  panic mid-render) and no body. Operator reported it
  immediately. Fix: change to `{{tf ...}}` (varargs
  formatter). Plus `TestTemplateArgsMatchCatalog`
  regression guard in `templates_test.go` — walks
  every embedded template, verifies the arg count of
  every `{{t ...}}` / `{{tf ...}}` call matches the
  catalog's placeholder count for that key
  (handles `%%` escapes). 12/12 packages green,
  smoke 118/118, live on VM at build `19d8981`.
* **Previous**: v0.16.6 — per-user subnets foundation
  ([tag v0.16.6](https://github.com/skygate-operator/skygate/releases/tag/v0.16.6)). The
  first concrete step of the 6-release per-user
  subnets roadmap (v0.16.6 → v0.19.0) documented in
  `docs/v0.16.0-open-questions.md` (8 operator
  decisions confirmed 2026-07-17). v0.16.6 ships the
  data model + CRUD + admin form + bot `/mysubnet`;
  the actual sidecar container management is the
  v0.16.7 follow-up. Adds:
  - `user_subnets` table (11 columns, UNIQUE on
    user_id + cidr, FK to portal_users ON DELETE
    CASCADE) + 3 denormalized columns on
    `portal_users` (`subnet_cidr`, `subnet_status`,
    `subnet_router_node_id`) — read by `/mysubnet`
    and `/admin/users/{id}` without JOIN
  - `control_plane_url` column on `user_subnets` for
    multi-plane (per-user headscale since v0.12.0)
  - `internal/subnet/allocator.go` — pure function
    `AllocateCIDR(userID) → 10.0.<uid>.0/24` (256
    users max; `/28` migration reserved as
    `subnet_bits` column without DB schema change)
  - `internal/subnet/manager.go` — CRUD layer with
    pre-check (avoids "FOREIGN KEY constraint
    failed") + `tx.Rollback` before `Get` (avoids
    SQLite write-lock deadlock after failed UNIQUE
    INSERT) + denorm sync on every mutation
  - `/admin/users/{id}/subnet` — 4 routes
    (allocate, disable, test, list) with idempotent
    allocate
  - Bot `/mysubnet` — reads denormalized columns
    (no JOIN), shows CIDR + status + router
    hostname + plane label
  - 30 new catalog keys (14 `bot.mysubnet.*` + 16
    `user_subnet.*`) RU+EN, parity test green
  - 21 new tests (4 allocator + 10 manager + 5
    admin + 2 bot)
  - 12/12 packages green, smoke 118/118, live on
    VM at build `a450fa7`.
* **Previous**: v0.16.5 — split long bot replies into
  multiple bubbles
  ([tag v0.16.5](https://github.com/skygate-operator/skygate/releases/tag/v0.16.5)). The
  operator reported that on a phone, long bot replies
  (`/help`, `/audit`, `/my_rules`) are hard to scan
  because Telegram's default font is small and the
  entire reply sits in one bubble. Telegram's HTML
  subset has no font-size tag, so the cleanest fix is
  to break long replies into multiple shorter bubbles
  — each section gets its own screen real estate and
  the bubble boundary acts as a visual break. Adds
  `splitMessageMarker` sentinel + `splitReplyParts`
  helper. `RealNotifier.reply` detects the marker and
  issues separate `sendMessage` calls. Applied to:
  - `/help`: 3 bubbles (Auth / User-scope / Admin) for
    admin, 2 for user, 1 for locked
  - `/audit`: split if > 10 entries (LIMIT 20 max);
    first bubble ends with "(N more — see next
    message)" hint
  - `/my_rules`: split if > 12 rules; same hint
  5 new tests. 12/12 packages green, smoke 118/118,
  live on VM at build `22b97c8`.
* **Previous**: v0.16.4 — fix HTML-unsafe `<` / `>` in
  catalog keys
  ([tag v0.16.4](https://github.com/skygate-operator/skygate/releases/tag/v0.16.4)). Hotfix
  for v0.16.3 — the v0.16.3 "more HTML" pass for `/help`
  shipped the reply with `parse_mode=HTML`, but several
  `bot.*` catalog keys still contained literal
  `<word>` placeholders (like `<команда>`, `<ключ>`,
  `<HEADSCALE_URL>`). Telegram's HTML parser rejects
  the whole `sendMessage` payload with HTTP 400
  "can't parse entities: Unsupported start tag" when
  it sees a literal `<word>` that isn't a known HTML
  tag — so the live `/help` was silently failing. Fix
  HTML-escapes 11 catalog keys (only the ones whose
  replies go through `parse_mode=HTML`; plain-text
  keys keep their literal `<word>`). New test
  `TestHTMLSafeCatalog` in `i18n_test.go` pins the
  contract. 12/12 packages green, smoke 118/118, live
  on VM at build `27ee8e6`.
* **Previous**: v0.16.3 — "more HTML" pass for /help
  ([tag v0.16.3](https://github.com/skygate-operator/skygate/releases/tag/v0.16.3)). The
  v0.16.1/v0.16.2 "more HTML" pass left `/help` in
  plain text, so the catalog's markdown backticks
  (`<id>`, `<target>`, etc.) showed up as literal
  characters. This release:
  1) converts 37 `bot.help.*` catalog entries from
     markdown backticks to `<code>` tags (with `&`, `<`,
     `>` HTML-escaped inside the `<code>`)
  2) rewrites `helpReply` so each of the three sections
     (Auth / User-scope / Admin) renders as a tabular
     `<pre>` block with a 20-char gutter for the
     command column. `markHTMLReply()` at the top so
     `parse_mode=HTML` is set.
  1 test rewrite (`TestHelpReplyV0155Layout`) + 1 test
  extension (`TestHTMLRepliesMarkParseMode` adds
  the `/help` sub-case). 12/12 packages green, smoke
  118/118, live on VM at build `cdbefe5`.
* **Previous**: v0.16.2 — "more HTML" pass bug fix
  ([tag v0.16.2](https://github.com/skygate-operator/skygate/releases/tag/v0.16.2)). Hotfix
  for v0.16.1 — the v0.16.1 release shipped HTML
  formatting in 8 bot replies but forgot to set
  `parse_mode=HTML` on the sendMessage payload, so the
  `<b>/<i>/<pre>/<code>` tags showed up as raw source
  text. Adds `markHTMLReply()` helper in
  `internal/telegram/commands.go` and calls it at the
  top of: `myStatusReply`, `myNodesReply`,
  `myRulesReply`, `myQuotaReply`, `myExitNodesReply`,
  `versionReply`, `auditReply`,
  `exitNodesHealthReply`. Also fixes a related bug
  inside `myExitNodesReply` where the inline-keyboard
  assignment was wiping the `ParseMode` set by
  `markHTMLReply`. 2 new tests (9 sub-cases total).
  12/12 packages green, smoke 118/118, live on VM at
  build `39d6af6`.
* **Previous**: v0.16.1 — "more HTML" pass
  ([tag v0.16.1](https://github.com/skygate-operator/skygate/releases/tag/v0.16.1)). The
  "bot reply formatting should look like a table, not
  a wall of text" release. `internal/telegram/format.go`
  adds a small helper layer (`Field()` / `Section()` /
  `PreLinesRaw()` / `Code()` / `Header()` /
  `BulletList()` / `HeaderLine()`) and the remaining
  four read commands that were still in prose format
  now use the new helpers:
  * `/my_rules` — tabular `<pre>` (ID / EXIT / TYPE /
    TARGET / ACTION)
  * `/my_quota` — three `Field()` lines (rules / fill
    / cap) under a `Section()` divider
  * `/myexitnodes` — tabular `<pre>` (HOSTNAME / NODE /
    STATUS / DEFAULT) with a `Section()`+`Field()`
    summary, and the default marker is now `✓`
    (was `[default]`)
  * `/ack` — already clean (one-line summary), left
    unchanged
  * `~50 new catalog keys (RU+EN)`. `12/12 packages
    green`, smoke `118/118`, live on VM at build
    `006f3d5`.
* **Previous**: v0.16.0 — backlog release
  ([tag v0.16.0](https://github.com/skygate-operator/skygate/releases/tag/v0.16.0)). The
  "clean up the deferred v0.12 / v0.13 backlog before
  tackling v0.16" release. Six previously-deferred
  features ship in one go:
  1. **v0.12.1 — per-user bot routing**. `BotEnv`
     carries `HSForPortalUser` and `PortalPlaneURL`
     closures; every `/add_device`, `/add_rule`,
     `/delrule` etc. now routes to the right
     control plane.
  2. **v0.13.0 — per-plane ACL**.
     `GenerateACLForPlane(planeURL)` only includes
     the identities on that plane. `ApplyACLForAllPlanes`
     iterates every distinct URL and pushes the
     right policy to each.
  3. **v0.13.0 — ACL import/export with dry-run
     preview**. `/admin/acls/export` downloads the
     current policy; `/admin/acls/import` accepts
     a JSON file or pasted text, shows a
     side-by-side dry-run, and only pushes when
     the operator clicks Apply.
  4. **Butler voice v3 — urgency marks**.
     `WithUrgency(level)` appends `!` (warning) or
     `!!` (critical) to the chosen icon, so `🔑!!`
     in the chat list reads as "critical preauth reply".
     Applied to `/add_device`.
  5. **Personal API token rotation**. `/my/token`
     now has a TTL dropdown (1h / 1d / 7d / 30d /
     never) and an auto-rotate checkbox. Expired
     tokens are rejected by the Bearer-auth path.
     Background rotation job is v0.16.0+ follow-up
     (column is in v0.15.5 so the UI can store + read).
  6. **Documentation**: per-user subnets roadmap
     entry in AGENTS.md + `docs/v0.16.0-open-questions.md`
     parking the 8 design decisions for the next
     major work.
  * All five backlog items done in one release —
    the v0.12 / v0.13 backlog is now empty.
  * 4 new v0.13.0 tests + 1 new v0.12.1 test + 1 new
    butler v3 test (6 sub-cases) + 1 schema migration
    test.
  * 12/12 packages green
* **Previous**: v0.15.6 — /admin/backup + /admin/exit-nodes
  full localization
  ([tag v0.15.6](https://github.com/skygate-operator/skygate/releases/tag/v0.15.6)). The
  "no hardcoded English left in the admin pages" release.
  46 new catalog keys (RU + EN) cover the backup history
  table headers, the migration-to-another-host warning +
  5-item + 6-item ordered lists (with embedded `<code>`
  for the docker restart command), the "Run backup now?"
  JS confirm, the exit-nodes 5-step tutorial narrative
  (headings, "Run on the exit-node (one-time)" intro, the
  inline code-explanation paragraphs after the tailscale
  up command, and the long "for nodes that run other
  VPN services..." warning), the exit-nodes status pills
  (off / synced / idle), the accept-routes dropdown
  options (default / false / true with explanations), and
  the form label "Headscale Node ID". Code blocks in the
  tutorial stay verbatim — those are shell commands the
  operator types. After v0.15.6 every admin sidebar page
  has a complete Russian translation.
  * 46 new catalog keys (RU + EN, 92 entries)
  * `internal/handlers/templates/admin/backup.html`
  * `internal/handlers/templates/admin/exit_nodes.html`
  * 12/12 packages green, TestCatalogsParity +
    TestPlaceholderOrder + TestLoadTemplates all green
* **Previous**: v0.15.5 — admin body butler-voice polish +
  /help alignment + /unbind_self
  ([tag v0.15.5](https://github.com/skygate-operator/skygate/releases/tag/v0.15.5)). The
  "admin replies should read like a butler, not a log;
  /help columns should line up" release. Three fixes:
  1. Drop log-voice prefixes (`sync_nodes:`, `audit:`,
     `exit_nodes_health:`, `restart:`, `add_rule:`,
     `delrule:`, `clearrules:`) from every admin reply
     and capitalise the first letter; the
     `target:` / `rule_ids=` / `ACL v#` technical
     fields stay verbatim, the `✓` / `⚠` status
     markers stay where they were.
  2. Widen the /help command gutter from 12 chars to
     18 (max command today is `/exit_nodes_health`
     at 17 chars) and drop the duplicate
     `\`<cmd>\` — <explanation>` from every description
     — the gutter is the command, the description is
     the explanation, the args hint lives at the end
     as `[args: <hint>]`.
  3. Add `/unbind_self` to the Auth section of /help
     (was in the dispatch table since v0.14.0 but
     missing from the listing).
  * ~80 catalog keys rewritten (RU + EN, ~160 entries)
  * `commands.go` `helpReply()` — `gutter` const 18,
    new `TestHelpReplyV0155Layout` pins the contract
  * 12/12 packages green, smoke 118/118, live on VM
    at build `7650c5e`
* **Previous**: v0.15.1 — final /admin/telegram localization
  ([tag v0.15.1](https://github.com/skygate-operator/skygate/releases/tag/v0.15.1)). The
  "no hardcoded English left in the Telegram admin
  page" release. 32 new `telegram.*` keys × 2 langs
  cover the probe banner (3 states), status pills,
  the Send Test / Rotate token / Disable bot / Strict
  mode paths, and the where-to-look hints. i18n
  parity test green.
* **Previous**: v0.15.0 — HTTPS / TLS via Caddy
  ([tag v0.15.0](https://github.com/skygate-operator/skygate/releases/tag/v0.15.0)). The
  "make the tailnet's control plane actually speak
  HTTPS" release. Adds a Caddy sidecar that terminates
  TLS for skygate, headscale, and headplane; auto-issues
  Let's Encrypt certs via the DNS-01 challenge (no
  port-80 inbound required); per-hostname routing
  inside a single 30-line Caddyfile. No nginx Proxy
  Manager, no PHP, no DB. DERP relay already did TLS
  itself (certmode=letsencrypt).
  * `docs/https-setup.md` — 17KB operator guide with
    per-module checklist, full rendered Caddyfile,
    verification commands, alternatives for tailnet-only
    / headscale-only / Tailscale TLS deployments.
  * `scripts/check_https.py` — deploy-time HTTPS check
    (TLS handshake, cert SAN, cert validity, HTTP→HTTPS
    redirect, HSTS on /login; --strict hard-fail
    variant). Wired into `make test`.
  * Per-module: skygate no change, headscale no change
    (gRPC stays `grpc_allow_insecure: true` because
    the hop is on the internal Docker network), headplane
    one env var (`COOKIE_SECURE=true`), DERP no change.
  * 8 new `.env` vars under "HTTPS reverse proxy
    (Caddy, v0.15.0)". DNS-01 API token in a separate
    0600 file (not in `.env`).
  * `make check-https` + `make check-https-strict`
    targets; `make test` now runs `check-https`.
  * 12/12 packages green, `bash -n deploy.sh` OK.
* **Previous**: v0.14.0 — bot UX overhaul
  ([tag v0.14.0](https://github.com/skygate-operator/skygate/releases/tag/v0.14.0)). The
  "make the bot usable" release. Five operator-visible
  problems fixed: `/exit_nodes` empty (new
  `SyncNodesFromHeadscale` + admin button + `/sync_nodes`
  bot command), bot menu refresh path (`Refresh bot menu`
  button on `/admin/telegram`), `/help` restructured to a
  sectioned table (🔐 Auth / ✦ Your data / 🛠 Admin),
  inline keyboards for `/lang` + `/myexitnodes`, web
  update banner via `release.Monitor.Snapshot()`.
* **Previous**: v0.13.0 — exit-node health monitor
  ([tag v0.13.0](https://github.com/skygate-operator/skygate/releases/tag/v0.13.0)). The
  "is my tailnet's egress actually working?" release.
  A background goroutine polls headscale every 5 min
  (`SKYGATE_EXIT_NODE_CHECK_INTERVAL`), classifies each
  configured exit-node as `online` / `degraded` / `offline`,
  surfaces the result on `/admin/exit-nodes` and the new
  `/exit_nodes_health` bot command, and dispatches
  **calm-mode** alerts (online↔offline only) via the existing
  Notifier. Plus a `--strict` flag on the deploy-time
  `check_exit_nodes.py` so CI / automated deploys can
  hard-fail when an exit-node is offline.
* **Previous**: v0.12.0.2 — Android exit-node routing + Telegram
  tab speed + admin tab RU
  ([release notes](RELEASE-NOTES.md#v01202)). Three
  operator-visible follow-ups to v0.12.0.1:
  1. **Android exit-node routing restored** — the v0.12.0.1
     catch-all removal closed the inter-user security hole but
     also killed the internet-egress primitive that exit-node
     routing depends on. The last ACL rule is now
     `* → autogroup:internet:*` (Tailscale's standard
     internet-egress group, supported by headscale 0.23+).
     `autogroup:internet` explicitly excludes the 100.64.100.0/10
     tailnet range, so inter-user isolation is preserved.
  2. **`/admin/telegram` no longer blocks for 5 s on every
     page load** — added a 30 s result cache for the
     `api.telegram.org` reachability probe, keyed by the
     bot-token fingerprint. Save / rotate / disable / strict
     invalidate the cache eagerly. Subsequent GETs within the
     30 s window render in ~1.5 ms instead of 5 s.
  3. **Settings + Exit Rules admin tabs fully translated to
     RU** — 35 new `settings.*` / `exit_rules_admin.*` i18n
     keys wired through `{{t}}` / `{{tf}}` in the templates
     (the inline `<script>` for the sync status uses
     `{{t ... | safeJS}}`). 12/12 packages green, smoke
     118/118, live headscale policy verified (autogroup:internet
     present, no `*:*` catch-all).
* **Previous**: v0.12.0.1 — ACL catch-all security fix +
  /help Russian translation + login form fixes
  ([release notes](RELEASE-NOTES.md#v01201)). Drops the
  literal `"*:*"` catch-all from the generated ACL to close
  the inter-user leak (each portal user could previously
  reach every other user's `tag:private` device via the
  catch-all's first-match fallback). The fix breaks exit-node
  routing on clients without explicit per-device rules;
  v0.12.0.2 restores it via `autogroup:internet`. Also:
  full Russian translation of `/help` (92 new `help.*` keys),
  login form `v0.2` hardcode → `{{.Version}}`, missing NVIDIA
  theme added to the picker.
* **Previous**: v0.12.0 — per-user headscale control plane
  ([tag v0.12.0](https://github.com/skygate-operator/skygate/releases/tag/v0.12.0)). Skygate-as-shell
  step 2: each `portal_users` row now carries its own
  `(headscale_url, headscale_api_key)` override, encrypted
  with `SKYGATE_SECRET_KEY` (AES-GCM, 32 bytes hex). The
  per-user router (`App.HSForUser(userID)`) routes
  user-scoped requests (`/my/devices`, `/my/preauth`,
  `/my/keys`, `/my/exit-nodes`, `/dashboard`) to the user's
  own headscale; cross-user admin pages
  (`/admin/devices`) use `App.HSGlobal()` explicitly. New
  pages: `/admin/control-planes` (lists every distinct
  plane + user counts), `/admin/users/{id}/plane` (per-user
  edit form with URL + encrypted API key fields).
  35 new tests, 22 new i18n keys. Bot handlers
  (`/my_nodes`, `/admin_nodes` in the Telegram bot) still
  use the global `env.HS` — per-user bot routing is a
  v0.12.1 follow-up. `GenerateACL()` still writes to the
  global headscale; per-plane ACL is v0.13.0. 12/12
  packages green, smoke 118/118.
* **Previous**: v0.10.14 — /clearrules body i18n (закрытие
  RU-долга)
  ([tag v0.10.14](https://github.com/skygate-operator/skygate/releases/tag/v0.10.14)). The last
  hardcoded-English path in the bot — `/clearrules` — now
  goes through `i18n.T` / `i18n.Tf` on every visible
  line. 5 new `bot.clearrules.mint_*` and
  `bot.clearrules.scan_error` keys (× 2 languages). Audit
  log details and the `Notifier.SendAlert` body on
  SetPolicy failure stay in English by design (operator
  surface, not user reply). 6 new
  `TestClearRulesReplyRussian*` tests pin the RU reply
  on every major branch.
## Roadmap (next releases)

The v0.16.0+ per-user subnets roadmap (below the "Per-user control
plane: when to use" section) is **done** — shipped incrementally
through v0.16.6 (foundation) → v0.22.0 (mesh) → v0.23.0 (one-click
per-user headscale, compliance tier). The next big things:

- **`v0.33.0` — live PG cutover** (the natural next step from
  v0.31.0 / v0.32.0 foundation). What's done: driver abstraction,
  27 PG migrations, 4 verification tests, helper scripts
  (`port_migrations_pg.py`, `rewrite_placeholders.py`,
  `dump_sqlite.py`), and the v0.32.0 i18n per-feature split +
  refactor-v0.30 (Phase C + D). What's left: (1) `?` → `$N`
  placeholder rewrite in `internal/db/queries.go` (script
  exists; needs careful diff + live PG to validate), (2)
  PG-staging VM provisioning + `SKYGATE_TEST_PG_DSN` setup,
  (3) R27 goes from SKIP to PASS (roundtrip + idempotency +
  lock_timeout + data_mig), (4) manual maintenance window:
  skygate in read-only mode → `dump_sqlite.py` → apply to
  fresh PG → flip `SKYGATE_DB_DSN` → restart. Estimate:
  2-3 days once PG-staging is up. **Blocked on** the
  operator's PG-staging VM (not yet provisioned per v0.31.0
  release notes).

- **`refactor-v0.30` — feature module decomposition**
  ([plan](docs/plans/refactor-v0.30.md), 2026-07-25, ~8 days
  work, **Phase B + C + D complete as of 2026-07-29**, B15 +
  B16 follow-up ports complete 2026-07-30). The
  `internal/handlers/` package went from 76 .go files
  (19208 lines, pre-refactor) to 9 files (infrastructure
  only: App + handlers_export + app_controlplane + static +
  templates + 3 test files). All feature handlers moved to
  per-feature packages under `internal/feature/{auth,admin,my,
  exit_rules,healthz}/`. Phase C split the i18n catalog
  (catalog.go 4260 lines → 12 per-feature files + glue).
  Phase D extracted httputil.SanitizeFilename,
  nodeownership.Backfill, controlplane.Router. **No
  behavior changes, no API changes, no migration changes.**
  **B15 + B16 ports done 2026-07-30** (commits 68aa0d6 +
  3a52015): `parent_domain` regression tests + CDN detection
  tests moved from `internal/handlers/` to
  `internal/feature/exit_rules/` (13 pure-function tests +
  1 helper for CDN; 6 tests + 1 helper for parent_domain).
  Both `feature/exit_rules/` test files run in <2s on
  Windows (`go test -count=1 -short` PASS).
  **Этап 14 v2 telegram probe tests ported 2026-07-30**
  (commit 33ffbb9): all 20 unit tests moved from
  `internal/handlers/handlers_telegram_probe_test.go`
  (484 lines) to
  `internal/feature/admin/telegram_probe_test.go`
  (529 lines). The port handles the App → Service
  field/method rename for the cache state
  (now `s.telegramProbeCache.{at,result,tokenFP,mu}`
  rather than 4 separate `app.telegramProbe*` fields)
  and provides a minimal `newTestService` helper
  (open :memory: DB → return `&Service{DB: d}`).
  20 PASS, 0 FAIL on Windows.
  Remaining follow-up: ~2850 lines of test code still
  tracked in the dropped-test backlog (see "Test debt" in
  the deferred-items audit). The 4 admin/{subnets,
  exit_nodes_tag, backup_config, user_subnet, control_planes,
  integrations*}_test.go files need a templates FS
  (`makeSyntheticTemplates` from handlers_test.go is the
  reference pattern) and the user_subnet tests need
  `fakeSidecarHS` (httptest.Server for the headscale API).
  2026-07-30: handlers_my_telegram_test.go (753 lines,
  19 tests) ported to internal/feature/my/telegram_test.go
  (15 tests) + internal/feature/admin/telegram_strict_test.go
  (4 tests). Brought the feature/ test count from 117 to 136
  (+19). Test debt now ~2850 lines (was ~3600).

- **`v0.19.1` — `exitnode.skygate-subnet-<user>.<workstation-8-domain>`
  DNS records** (re-attempt of the reverted v0.19.0). Per-user
  `tag:subnet-router` already exposes a stable IP per
  personal subnet (v0.18.0 MagicDNS). The next step is a
  named DNS record that points to the user's **chosen
  exit-node** (not the subnet router), so tailnet clients can
  reach the user's exit-node via DNS without remembering
  which one they picked. **Blocked on headscale 0.30+** —
  v0.19.0 was reverted because headscale 0.29.x (the
  operator's current version, 0.29.2) rejects
  `dns.extra_records` in the policy with "unknown field: dns".
  Need to check the headscale changelog for 0.30 release
  status. If 0.30+ is out: re-implement against the new
  API. If still pending: defer.

- **`v0.23.1` Phase 2 — safe user migration** (compliance tier
  only, opt-in). v0.23.0 shipped per-user headscale
  provisioning (infrastructure only, no data migration).
  Phase 2 is the data-migration step: take a user off the
  global headscale, move their nodes + ACL to the per-user
  plane, flip the DB override. This requires read-only mode
  on the global headscale during the cutover and is
  intentionally **deferred until an operator needs it**
  (the per-user subnet + global-headscale path covers 95% of
  use cases; only SOX / multi-tenant SaaS / geographic
  isolation actually need this).

- **Unmerged branches** (`feature/backup-config-ui`,
  `feature/bot-i18n-v5`, `feature/butler-voice-v2`) are from
  the v0.10.x "Этап 14" series. **All three were already merged**
  into main (in v0.10.7, v0.15.x, and v0.16.x respectively)
  and the local branches were never cleaned up. **Deleted in
  this session (2026-07-29)** to keep `git branch` clean. The
  other 20+ `feature/*` branches from the v0.10.x–v0.26.0 era
  (e.g. `feature/v0.24.0-subnet-router-setup`, `feature/v0.26.0-ha-ready`)
  are also likely already merged but were left for a manual
  audit (deferred — none of them are blocking any work).

- **Other long-lived items** (not blocking, listed for
  context): butler voice v3 (urgency marks; deferred until
  user feedback on v2 lands), personal API token rotation
  (TTL + auto-rotate, column already exists from v0.15.5),
  `headscale` milestones #16 (DNS Work) — weekly mavis cron
  checks for progress.

---

## What is Skygate?

Tailscale/headscale management portal. Stack: **Go 1.23 + SQLite + Docker +
headscale 0.29 API + embedded HTML templates**.

Key features:
- **Exit-node rules** with per-device accept/deny ACL
- **Automatic DNS-driven /32 resolution** for domain rules (autoupdater)
- **Multi-user**, per-user rule limits (`SKYGATE_USER_MAX_RULES=admin:2000`)
- **Per-device limits** (`SKYGATE_MAX_RULES_PER_DEVICE=500`)
- **Cleanup of orphaned /32** (admin endpoint)
- **Sync to exit-node advertised-routes** (staggered per node)
- **Per-user headscale ACL** (each user sees only their own devices)
- **Tag-aware device ownership** (`tag:private` per portal user,
  `tag:public` shared exit-nodes)
- **Personal API tokens** for AI integration

User-facing pages:
- `/my/exit-rules` — user's own rules (add/delete/filter/search/multi-delete)
- `/my/exit-rules/help` — full help page with API reference
- `/admin/exit-rules` — admin view of all users' rules
- `/admin/exit-rules/cleanup` — admin: merge duplicate device_ids
- `/admin/exit-rules/sync` — admin: trigger advertised-routes sync
- `/admin/exit-rules/rollback` — admin: rollback ACL to a previous version
- `/admin/devices` — admin: list of all nodes with manual tag/untag
- `/admin/devices/taged` — admin: POST to tag a node
- `/admin/users` — admin: user CRUD
- `/admin/acls` — admin: ACL view (read-only)
- `/admin/audit` — admin: audit_log view
- `/admin/derp` — admin: DERP relay status
- `/admin/exit-nodes` — admin: list exit nodes
- `/admin/backup` — admin: backup/restore ACL
- `/admin/telegram` — admin: bot config (token in `global_settings`, sendMessage via Go-native HTTP in `internal/telegram/notify.go`)
- `/my/account` — self-service password change (current + new + confirm)
- Rate limits (in-memory, single-instance only):
  - POST /login: 5 attempts per username per 15s, 20 per IP per 30s
  - /api endpoints: 30 requests per IP per 60s
  - 429 + Retry-After header on block; sweep every 5 min
- `/my/tokens` — personal API tokens
- `/my/devices` — user's devices (tagged via portal)

API:
- `GET/POST /my/exit-rules/api` — list / bulk create rules (Bearer auth or
  cookie). **POST returns `{added, duplicates, errors, ids: [N1, N2, ...]}`
  so clients can clean up.**
- `POST /my/exit-rules/delete` — delete one (`id=X`) or many (`ids=X&ids=Y&...`)

---

## Per-user control plane: when to use (v0.23.0/v0.23.1)

The v0.23.0 + v0.23.1 releases added a "one-click per-user
headscale" capability. **This is a compliance tier, not the
default path.** The architectural decision documented in
[`RELEASE-NOTES.md`](RELEASE-NOTES.md#v0231) is:

> "Per-user control plane (v0.23.0) requires re-auth of all
>  devices, and the user loses access to shared exit-nodes
>  (relay-1/relay-2/relay-3) and mesh bridges with other
>  users. For most scenarios, per-user subnet already works
>  as a logical namespace in the global headscale (v0.16.6+).
>  Use v0.23.0 provisioning ONLY for compliance tier (SOX,
>  multi-tenant SaaS, geographic isolation)."

The reason: **Tailscale's protocol is one control server per
node**. Two headscales cannot share nodes. If user A is in
`headscale-A` and user B is in `headscale-B`, they cannot
see each other's devices, even if both are in the same
physical network. Cross-control-server routing does not
exist (Tailnet Lock/Sharing is enterprise-only, not in
headscale 0.29.x).

### When to use per-user control plane (v0.23.0)

Use ONLY when the operator has a real need for:
- **SOX / compliance**: tenant isolation, audit log separation,
  per-tenant API keys (compliance audit)
- **Multi-tenant SaaS**: each "customer" gets their own
  headscale container (no shared resources)
- **Geographic isolation**: per-region control plane (e.g.
  US users on us-east, EU users on eu-west)
- **Tailnet Key rotation**: per-tenant key with independent
  noise_private.key

### When NOT to use per-user control plane

The default path. **Don't use v0.23.0 for any of these** —
they're already solved by the global headscale:
- "Per-user subnet" — v0.16.6+ gives each user `10.0.<uid>.0/24`
  as a logical ACL namespace
- "Shared exit-nodes" — `tag:exit-node` in global ACL makes
  relay-1/relay-2/relay-3 accessible from all users
- "Mesh between users" — v0.22.0 N-way bridge gives
  cross-user subnet visibility via ACL cross-CIDR
- "Cross-user share" — v0.17.1 share rows
- "Tailscale --accept-routes" — works in global

### How to provision (when actually needed)

1. Open `/admin/users/{id}/plane`
2. Read the warning card carefully (re-auth cost, lost access)
3. Click "Provision per-user headscale"
4. Confirm the JS dialog
5. Wait ~15s for the container to come up
6. SSH to each of the user's devices, run:
   ```
   sudo tailscale logout
   sudo tailscale up --login-server=https://head.<username>.example.com \
     --authkey=<preauth from /admin/users/{id}/plane>
   ```
7. The user is now on their own control plane. The old
  device entries in the global headscale become orphaned
  (delete them via `docker exec headscale headscale nodes
  delete -i <N>`).

### How to deprovision

1. Open `/admin/users/{id}/plane` (user must be on per-user)
2. Click "Decommission per-user headscale"
3. Confirm the JS dialog
4. The container is stopped, the per-user data dir is
  preserved at `~/.decommissioned-<ts>` (recoverable for 30
  days)
5. The DB override is cleared — `HSForUser(uid)` falls back
  to `HSGlobal()`. The user's devices (still in the per-user
  headscale) are now invisible to skygate until they re-auth
  to the global headscale.

---

## v0.16.0+ per-user subnets (DEFAULT — use this)

For the 4 prod users (admin/user1/user3/user2), the
default path is per-user subnets in the global headscale
(v0.16.6+). Each user has `10.0.<uid>.0/24` as a logical
ACL namespace. Exit-nodes are shared. Mesh is cross-user.
No re-auth, no separate control plane. **Use this for 95% of
scenarios.**

### Operational note: fixing `node_owner_map` attribution for tag-bearing devices

**Symptom**: A user has 5+ devices in headscale (all with
`tag:private`), but their `/my/devices` page shows 0 devices.
`portal_users.subnet_status` stays `pending` even though the
user clearly has devices. Querying `node_owner_map` shows
all the user's rows with `username=tagged-devices` instead
of the user's actual username.

**Root cause** (v0.3.9 + v0.22.2 limitation): When headscale
applies a tag to a node, it reassigns ownership to a
synthetic `tagged-devices` user. The `backfillNodeOwnership`
function tries to recover the original owner via two
strategies:

- **Strategy A**: match `node.PreAuthKeyID` against a
  stored preauth (`preauth_keys.headscale_preauth_id`).
  Requires the preauth to have been issued through skygate
  AND have its headscale_id captured.
- **Strategy C**: temporal fallback — node created within
  1 hour of a preauth. Only works for very fresh devices.

For devices registered before v0.12.0 (when
`headscale_preauth_id` capture was added), Strategy A
cannot match. Strategy C doesn't work for old devices. The
manual recovery path is needed.

**Fix** (one-off, applied 2026-07-21 for admin): update
`node_owner_map` to attribute the known devices to the
right user:

```sql
UPDATE node_owner_map
   SET username = 'admin', tag = 'tag:private', tagged_by_user_id = 1
 WHERE hostname IN ('workstation-1','workstation-2','workstation-2-old',
                     'skygate-host-1','workstation-4','workstation-3');
```

After the UPDATE, the next `/my/devices` load (which fires
`backfillNodeOwnership` → `subnet.SyncStatus`) flips the
status from `pending` to `active`. The `backfillNodeOwnership`
GC pass doesn't undo the manual fix (it only removes rows
for nodes that no longer exist in headscale, not for nodes
that exist with the wrong username).

The `fix_admin_attribution.sh` script in the repo root
does this end-to-end (UPDATE → trigger → verify). It's
idempotent — re-running is a no-op.

**When to use**:
- A user has devices in headscale but `node_owner_map` has
  them as `tagged-devices` (look for the symptom above).
- The operator can enumerate the user's devices (by host
  or by checking `headscale nodes list -o json | jq` for
  `user.name == "tagged-devices"` and matching the device
  by preauth or registration time).
- The preauth was issued before v0.12.0, so
  `headscale_preauth_id` is NULL.

**When NOT to use**:
- New devices (post-v0.12.0) have `headscale_preauth_id`
  captured at issue time, so the backfill attributes them
  automatically. No manual fix needed.
- The user has no devices in headscale (the `pending` status
  is correct — they're not opted in to Tailscale yet).

### Operational note: node-expiry watcher (v0.23.3 + v0.23.4, the "device won't stay connected" release)

**Symptom**: User generates a preauth via `/my/preauth`,
pastes the key into a Tailscale client, the client
registers successfully, but the device disconnects within
seconds and never reconnects. The preauth is now `used=true`,
so the user can't re-register with it either. The Android
client shows "Sign in" with a key that was never accepted.
Delayed variant: the device connects and works for ~30
days, then disconnects. The preauth is `used=true`; the
device won't come back.

**Root cause** (discovered 2026-07-21 with the operator's
Android phone / node 10 / workstation-2): Tailscale 1.98.x's
`RegisterRequest.Expiry` field is only 2-4 seconds in
the future. headscale 0.29.x's `HandleNodeFromAuthPath`
(in `hscontrol/state.go`) applies that Expiry verbatim:

```go
if !node.IsTagged() {
    if !regReq.Expiry.IsZero() {
        node.Expiry = &regReq.Expiry
    } else if s.cfg.Node.Expiry > 0 {
        // ...
    } else {
        node.Expiry = nil
    }
}
```

The next netmap push to the client reports
`Expired: true, MachineAuthorized: false`, the client
interprets this as "your key was rejected, log out", and
the device goes back to `NeedsLogin`. The preauth is
already `used=true`, so re-registration is impossible.

**Fix** (v0.23.3): a background goroutine in
`internal/expirewatch` ticks every 5 minutes, walks
every node in headscale, and extends any node whose
Expiry is within 7 days of "now" out to 30 days.

**v0.23.4 fix** to v0.23.3: the original rule "skip any
tagged node" was wrong. A user device registers
UNTAGGED with a skygate-issued preauth, picks up the
2-4s Expiry, then gets `tag:private` attached by
skygate's `backfillNodeOwnership` on the next
`/my/devices` load. The v0.23.3 watcher saw `len(Tags)
> 0` and skipped it; the Expiry passed; the device
disconnected 30 days later. The corrected rule is
"skip only when `n.Expiry == ""`" — this covers
`tag:exit-node` / `tag:public` / `tag:subnet-router`
(never had a non-nil Expiry) and any node on which the
operator ran `headscale nodes expire -i N --disable`.
Tagged nodes with a real Expiry (`tag:private` user
devices) are now renewed just like untagged ones.

To verify the v0.23.4 fix is live: after deploy,
`docker logs skygate | grep expirewatch.tick` should
show `seen=N renewed>0 skipped<N` — if `renewed=0`
and `skipped=seen`, the old code is still running
(roll back or re-deploy).

**Verification**:
- `bash /tmp/check_v0.23.3.sh` — live test: force a
  node's expiry to 2s, wait for the watcher to tick,
  confirm the expiry is now at least 7d out and an
  `audit_log` row with `username=expirewatch,
  action=renewed, detail=node_id=<N> old_expiry=<...>
  new_expiry=<...>` was written.
- `docker logs skygate | grep expirewatch.tick` — every
  tick logs `seen=N renewed=N skipped=N errors=N`.
- The audit log table itself — every renewal is one
  row, queryable via `/admin/audit?action=renewed` (or
  `?username=expirewatch`).

**Tuning** (env vars, all optional, defaults are fine):
- `SKYGATE_EXPIREWATCH_ENABLED=true` — `false` disables
  the goroutine entirely.
- `SKYGATE_EXPIREWATCH_INTERVAL=5m` — tick frequency.
  `off` / `0` disables. Set to `1m` for faster recovery
  in exchange for more API calls.
- `SKYGATE_EXPIREWATCH_THRESHOLD=168h` (7d) — nodes
  within this window get renewed.
- `SKYGATE_EXPIREWATCH_RENEWAL=720h` (30d) — new
  expiry when renewing.

**One-shot manual fix** (if you can't immediately
deploy v0.23.4 or the watcher is disabled):
```bash
docker exec headscale headscale nodes expire \
  -i <NODE_ID> --expiry "$(date -u -d '+30 days' +'%Y-%m-%dT%H:%M:%SZ')"
```
The CLI `headscale nodes expire -i <id> --disable`
sets `Expiry = nil` and the watcher will then skip the
node indefinitely (use this on tagged infrastructure
nodes that you genuinely want to never expire).

**When NOT to look here**:
- A device that never registered in the first place
  (the issue is the preauth issuance path, not expiry
  — check `preauth_issued` audit events).
- A device that registered but immediately got the
  wrong ACL (issue is the policy, not expiry — check
  `headscale policy get` and the
  `/admin/devices/{id}/tag` flow).

### Operational note: subnet-router setup (v0.24.0, the "end-to-end per-user subnet" release)

**Symptom**: User has been allocated a per-user subnet
(`10.0.<uid>.0/24` — visible on `/admin/users/{id}/subnet`
as a `pending` status pill with a `Issue preauth key`
button) but the LAN behind the subnet-router isn't reachable
from the tailnet.

**End-to-end flow** (the user reads `docs/subnet-router.md`,
the admin reads this section):

1. **User has a subnet row** in `user_subnets` with status
   `pending`. The denormalized `portal_users.subnet_cidr`
   matches (set by `subnet.Create` on user create since
   v0.20.0 auto-allocate, or by
   `deploy/subnet-router/allocate-existing-users.sh` for
   pre-v0.20.0 users).

2. **Admin opens `/admin/users/{id}/subnet`**, clicks
   `Issue preauth key`. The handler
   (`PostAdminUserSubnetProvision` in
   `internal/handlers/admin_user_subnet.go`) calls
   `sidecar.Manager.GeneratePreauth` (returns a 1-hour
   TTL single-use preauth tagged `tag:subnet-router`),
   then `BuildPreauthInfo` (which formats the `tailscale
   up` command for the admin UI). The handler does NOT
   push any ACL — the sidecar's auto-approver handles
   route approval on the next 30s tick.

3. **User runs `deploy/subnet-router/setup.sh`** (or
   pastes the `tailscale up` command directly) on the
   host that's at the edge of their LAN. Sanity checks
   in the script: tailscale CLI present, tailscaled up,
   env vars (`PREAUTH_KEY`, `SUBNET_CIDR`,
   `SUBNET_ROUTER_HOSTNAME`) all set.

4. **Node registers in headscale** as
   `skygate-subnet-<username>` with tag `tag:subnet-router`
   and a pending route for `10.0.<uid>.0/24`.

5. **`sidecar.Manager.SyncOnce`** (30s tick) lists every
   `tag:subnet-router` node across all control planes,
   parses the username from the hostname, looks up the
   portal user, and calls `ApproveAllRoutesWithList` on
   the per-user CIDR. Then it flips
   `user_subnets.status` from `pending` to `active` and
   sets `router_node_id` + `router_hostname`.

6. **`subnet.SyncStatus`** (called from
   `backfillNodeOwnership` on every `/my/devices` load)
   reads `user_subnets.status` and `user_subnets.router_hostname`,
   then writes the **denormalized**
   `portal_users.subnet_status = 'router_active'` (the
   v0.22.3 semantics: `active ⇔ ≥1 device`,
   `router_active ⇔ + router up`).

7. **ACL re-apply**: not needed. The v0.17.0 ACL already
   includes `tag:subnet-router` in `tagOwners`, and the
   per-user rule already permits
   `tag:subnet-router → user_subnet:*`. No policy churn.

8. **Tailnet clients with `tailscale up --accept-routes`**
   see the new route within ~60s (the route push interval).
   `ping skygate-subnet-<username>` works via MagicDNS;
   `ping 10.0.<uid>.1` works to the gateway IP on the
   user's LAN (assuming the subnet-router has IP forwarding
   enabled — see `docs/subnet-router.md` § Optional).

**Verification** (on the skygate host):

```bash
# 1. status pill should flip
curl -fsS -u admin:... 'https://gate.example.com/admin/users/6/subnet' \
  | grep -A2 'subnet-status'

# 2. audit log
skygate sqlite3 /data/skygate.db \
  "SELECT id, created_at, username, action, substr(detail,1,80)
   FROM audit_log WHERE action LIKE 'subnet%' ORDER BY id DESC LIMIT 5;"

# 3. sidecar logs
docker logs --since 5m skygate | grep -E 'sidecar.*approved|sidecar.*10.0.6.0/24'

# 4. headscale state
docker exec headscale headscale nodes list -o json | \
  python3 -c "import sys,json; \
    [print(n['givenName'], 'allowed:', n.get('allowedRoutes',[]), 'enabled:', n.get('enabledRoutes',[])) \
     for n in json.load(sys.stdin) if 'skygate-subnet-user1' in n.get('givenName','')]"
```

**One-off for backfilling** (run once after this release
on the operator's VM):

```bash
ALLOCATE_NO_PROMPT=1 /home/admin/skygate/deploy/subnet-router/allocate-existing-users.sh
```

Already executed: user1/user3/user2 now have
`10.0.<uid>.0/24` rows in `user_subnets` with
`status='pending'` and the corresponding denorm columns on
`portal_users`.

**When NOT to look here**:
- A user with `subnet_status='active'` but no
  `subnet_router_node_id` — that's a user-owned subnet
  with no live router. Same as the `pending` case
  symptom-wise; the user just hasn't run `setup.sh` yet
  (or their router is down — sidecar marks
  `last_seen > 5 min` as `disabled`).
- A user with no subnet at all — they need
  `allocate-existing-users.sh` first, or the admin needs
  to click `Allocate` on `/admin/users/{id}/subnet`.

---

## Code structure (where to look)

**Entry point:** `cmd/skygate/main.go` — HTTP routes, app init, lifecycle.

**Package layout** (post-refactor-v0.30, 2026-07-29). The
`internal/handlers/` package shrunk from 76 files (~19k
lines, pre-refactor) to 7 files (infrastructure only: App +
handlers_export + app_controlplane + static + templates + 2
helpers). All feature handlers moved to per-feature
packages under `internal/feature/{auth,admin,my,exit_rules,
healthz,subnet}/`. The `internal/i18n/` catalog was split
from 1 file (4260 lines) into 12 per-feature
`catalog_<feature>.go` files + a glue. New utility packages
(`internal/{httputil,nodeownership,controlplane,devicemeta}/`)
own the helpers that were previously private methods on
`*App` or duplicated across handlers. Run
`find internal -name '*.go' | wc -l` for the current count.

| Package | Files | Lines | Purpose |
|---|---:|---:|---|
| `internal/handlers/` | 7 | ~1.2k | **Infrastructure only** post-refactor: `handlers.go` (App + render helpers), `handlers_export.go` (public Backend-interface wrappers), `app_controlplane.go` (thin Router delegates), `static.go` (embedded CSS/JS), `templates.go` (`embed.FS`). |
| `internal/feature/admin/` | 25 | ~6.4k | `/admin/*` pages — users, devices, exit-nodes, subnets, telegram, headscale, integrations, backup, settings, control-planes, invites, meshes, ACLs, audit. v0.30.0 refactor target. |
| `internal/feature/my/` | 12 | ~2.7k | `/my/*` pages — devices, keys, tokens, preauth, exit-nodes, account, audit-export, telegram-bind, devices preferred-exit, meshes, dashboard, settings. v0.30.0 refactor target. |
| `internal/feature/exit_rules/` | 17 | ~3.3k | The `/my/exit-rules` + `/admin/exit-rules/*` feature module (largest, biggest surface). Owns CDN detection, parent_domain fix, autoupdate, route script, sync, API. v0.30.0 refactor target. |
| `internal/feature/auth/` | 3 | ~0.4k | `/login`, `/logout`, `/lang`, `/help`. v0.30.0 refactor target. |
| `internal/feature/healthz/` | 4 | ~0.2k | `/healthz` + `/readyz` probes. v0.30.0 refactor target. |
| `internal/feature/subnet/` | 1 | ~12 | Placeholder; the subnet feature is still in `internal/subnet/` (data layer). Future Phase. |
| `internal/devicemeta/` | 2 | ~0.3k | **v0.32.0 NEW.** Per-device OS + device_type detection (DESKTOP-*/MSI → windows; iPhone → ios; Nothing Phone → android; etc). Pure functions `DetectOS`/`DetectType` + `OSIcon`/`TypeIcon`. Auto-detect runs on every /my/devices load. |
| `internal/nodeownership/` | 2 | ~0.7k | **Phase D2 NEW.** `backfillNodeOwnership` extracted from `*App` (was 393 lines, now a top-level `Backfill` function). Called via `handlers.BackfillNodeOwnershipFn` from `feature/my`. |
| `internal/controlplane/` | 2 | ~0.5k | **Phase D3 NEW.** Per-user control plane router (`Router.ForUser` / `Global` / `PlaneURLForUser` / `InvalidateCache` + the per-URL client cache). Was `*App.HSForUser` / `HSGlobal` etc. |
| `internal/httputil/` | 2 | ~0.1k | **Phase D1 NEW.** `SanitizeFilename` (3 copies collapsed to 1). |
| `internal/acl/` | 4 | ~4.3k | GenerateACL + ACL helpers. Was inside `exit_rules.go` before v7; extracted to its own package so the telegram bot can call it without `*App`. **v0.32.0 fix:** the `via:` sync bug — `Service.generateACL` now honours `SKYGATE_ACL_VIA_ENABLED`. |
| `internal/db/` | 64 | ~13.3k | SQLite layer + 48 migrations (v0.32.0 added the per-device `os` + `device_type` columns). Includes `pgmigrate/` (PG safety helpers) and `driver_postgres.go` (build tag `postgres`, v0.31.0). |
| `internal/telegram/` | 28 | ~13.5k | Bot dispatch + per-command handlers + i18n + formatting. Refactor target after `internal/handlers/`. |
| `internal/headscale/` | 14 | ~2.8k | headscale API client (split by resource: users, preauth, nodes, tags, acl, routes) + CLI fallback for tag/untag. |
| `internal/update/` | 12 | ~3k | v0.29.0 self-update orchestrator (already separate package, not affected by refactor). |
| `internal/headscale_version/` | 3 | ~0.8k | headscale-release-version monitoring (`/admin/headscale` page + `/headscale` bot command, v0.20.0). |
| `internal/i18n/` | 16 | ~4.3k | **Phase C:** 12 per-feature `catalog_<feature>.go` files + glue (`catalog.go`) + `T()`/`Tf()` helpers (`i18n.go`) + `GlobalCatalog`/`GlobalLang` (`global.go`) + `TestCatalogsParity` (B4). `scripts/split_i18n.py` re-derives the per-feature catalogs if ever needed. |
| `internal/backup/` | 6 | ~1.6k | ACL backup/restore (CLI in `admin_backup.go`, config in `admin_backup_config.go`). |
| `internal/invite/` | 4 | ~1k | v0.21.0 user-to-user invite bridge (bot `/invite` + `/accept` + `/admin/invites`). |
| `internal/mesh/` | 2 | ~0.7k | v0.22.0 N-way mesh between users. |
| `internal/sidecar/` | 2 | ~1k | v0.16.7 per-user subnet-router sidecar (auto-approve + status sync). |
| `internal/subnet/` | 8 | ~1.8k | v0.16.6 per-user subnet allocator + manager + shares. Data layer; the feature package (`internal/feature/subnet/`) reuses this. |
| `internal/expirewatch/` | 2 | ~0.7k | v0.23.3 node-expiry watcher (5m tick, 7d threshold, 30d renewal). |
| `internal/monitoring/` | 2 | ~1.1k | /healthz + /readyz probes (R1, R2 in catalog). |
| `internal/release/` | 3 | ~0.5k | GitHub Releases monitor for /admin/update banner. |
| `internal/auth/`, `internal/config/`, `internal/middleware/`, `internal/ratelimit/`, `internal/db/pgmigrate/` | small | — | Platform primitives — not affected by refactor. |

**Templates** (//go:embed from `internal/handlers/templates/`):
- `exit_rules.html`, `exit_rules_help.html` — /my/exit-rules + /my/exit-rules/help
- `admin/*` — /admin/* pages (per-page)
- `user/*` — /my/* pages
- `themes.css` — CSS embedded from `static/css/themes.css`

**Deploy / scripts:**
- `deploy/skygate-cli.sh` — host-side `skygate` wrapper (v0.29.2, B14)
- `deploy/{deploy,backup,validate}.sh`, `deploy/{subnet-router,tailscale-relay,headscale-users}/` — operator tooling
- `scripts/smoke.sh` (bilingual 83+83=166 HTTP-level checks, B8)
- `scripts/check_exit_nodes.py`, `scripts/check_https.py`, `scripts/audit_routes.py`
- `Makefile` — `build / run / test / smoke / verify-pre / verify-post / audit` targets
- `docs/plans/` — refactor-v0.30.md, pg-migration-handling.md, self-update-v0.29.md, refactor-v0.6.0.md (history)
- `AGENTS.md` — this file

**When adding a new feature** (post-refactor): drop a new directory
`internal/feature/foo/` with `handler.go + service.go + store.go +
types.go + template.html + i18n_keys.go + bot.go + tests`, add 5-10
lines to `cmd/skygate/main.go` for the route, add 1-2 lines to
`internal/telegram/dispatch.go` for the bot command. Done.

---

## Per-user headscale ACL policy

`GenerateACL()` in `internal/acl/acl.go` (was inside `internal/handlers/exit_rules.go` before Этап 14 v7; extracted to its own package so the telegram bot can call it without an `*App` reference) builds a **per-user** headscale ACL using identities from `portal_users`. The catch-all `*:*` rule that used to be first is REMOVED.

```json
{
  "acls": [
    {"src": ["admin@tsnet.example.com"], "dst": ["admin@tsnet.example.com:*"]},
    {"src": ["user1@tsnet.example.com"], "dst": ["user1@tsnet.example.com:*"]},
    ... per-device exit-rule targets (DNS, telegram IPs, etc) ...
    {"src": ["*"], "dst": ["tag:public:*"]},
    {"src": ["*"], "dst": ["tag:exit-node:*"]},
    {"src": ["*"], "dst": ["*:*"]}    // internet egress (last rule)
  ],
  "tagOwners": {
    "tag:private":   ["admin@...", "user1@...", ...ALL portal users...],
    "tag:public":    ["admin@tsnet.example.com"],
    "tag:exit-node": ["admin@tsnet.example.com"]   // added in v7 — was missing
  },
  "groups": { "group:admin": [...], "group:user1": [...], ... },
  "ssh": [
    {"action":"accept","src":["tag:private","admin@…"],"dst":["tag:exit-node"],"users":["root"]},
    {"action":"accept","src":["admin@…"],"dst":["tag:public"],"users":["root"]}
  ]
}
```

Tailscale ACL semantics: **first matching rule wins**. The catch-all `*:*` rule
that used to be first is gone; only the per-user rule applies to most traffic.
Each user can only talk to their own tag:private devices. tag:public /
tag:exit-node are visible to everyone (so users can pick exit-nodes).

**When editing `GenerateACL()`**: do NOT add `{"*", "*:*"}` as the first rule.
First-match semantics make it override everything else. The internet egress
must remain LAST, after per-user and tag rules. Also remember that every
`tag:*` referenced in `acls[]` or `ssh[]` must have a corresponding entry in
`tagOwners{}` (the v7 fix that broke reapply otherwise — see
"Admin SSH into tag:public relays" above for the full story).

The headscale workstation-8 domain is hard-coded as `tsnet.example.com` for now — it
is the only deployment. If you add another deployment, refactor to read it
from `config.Config`.

---

## Tailscale in skygate (Этап 14 v2 + v3 + v7, 2026-07-14)

The skygate container runs `tailscaled` in its own network namespace
and joins the tailnet with `tailscale up --accept-routes --accept-dns=false`.
The default-flag set has been `--accept-routes` only (no `--exit-node`):
the bot's traffic to api.telegram.org used to be routed through a
relay's subnet routes rather than a global exit-node. As of Этап 14
v7 the operator unified the relay model (see "Unified exit-node +
accept-routes" below) and may switch skygate to
`tailscale up --accept-routes --exit-node=<chosen-relay>` —
either is fine; the probe (described further down) is the source of
truth for whether a packet actually goes through Tailscale.

### Why not a sidecar (Этап 14 v2)

* **Sidecar (skygate-ts, removed in Этап 14 v2)**: `network_mode:
  service:tailscale` broke docker's embedded DNS (127.0.0.11:53
  refused UDP). The sidecar's `entrypoint.sh` also called
  `tailscale up --state=...` with a flag `tailscale up` doesn't
  accept, so the sidecar died at startup and took skygate down
  with it (exit 137).
* **Subnets-route / accept-routes model won** (Этап 14 v2) because
  per-destination routing keeps Docker's DNS, doesn't hijack the
  default route, and is auditable.

### Container layout

* `Dockerfile` (multi-stage): pulls `tailscale` + `tailscaled` from
  `tailscale/tailscale:latest`, copies them into the skygate runtime
  image along with `iptables`, `ip6tables`, `libcap`, etc.
* `entrypoint.sh`: if `TS_AUTHKEY_FILE` is set, starts `tailscaled`,
  runs `tailscale up --accept-routes --accept-dns=false`. Otherwise
  logs "Tailscale skipped (non-RF mode)" and continues with the
  skygate build. tailscaled is reparented to skygate (PID 1) when
  skygate execs.
* `docker-compose.yml`: skygate gets `NET_ADMIN` + `SYS_ADMIN` +
  `/dev/net/tun` + the `ts_authkey` docker secret. Tailscale state
  persists at `./data/ts/` across container restarts so we don't
  re-auth on every `docker compose restart`.

### `--accept-dns=false` is required

Tailscale's MagicDNS replaces `/etc/resolv.conf` with `100.100.100.100`,
which only knows about tailnet names. The Docker service name
`headscale` (used by `HEADSCALE_URL=http://headscale:50444`) stops
resolving, and skygate's API client dies with "lookup headscale on
100.100.100.100:53: no such host". With `--accept-dns=false` the
container keeps Docker's `127.0.0.11` DNS, and only the tailnet's
subnet routes (not its DNS) are accepted. Tailnet-name resolution
isn't currently needed.

### Unified exit-node + accept-routes (Этап 14 v7, 2026-07-14)

The project principle (confirmed by the operator) is that **every
relay node does BOTH things** and is interchangeable:

  1. **Exit node** — `tailscale set --advertise-exit-node` makes
     a node appear in the client's exit-node menu.
  2. **Accept-routes (subnet routes)** — the same node advertises
     a set of CIDRs that other tailnet members receive when they
     run `tailscale up --accept-routes`. The exit-node client then
     has both its default route AND the subnet routes pointing at
     that node, with the kernel doing the right thing for each
     destination.

There is no "Telegram-special" logic and no "primary" exit node.
skygate-host-1 is a regular client — it can be pointed at any relay,
and the operator may change it if a relay becomes flaky. The
client's Tailscale GUI shows all available exit nodes and
auto-failover happens at the metric level (Tailscale native).

The three relay nodes (Этап 14 v7 state):

* **relay-1** (100.64.100.3) — exit-node + Telegram 8 v4 + 4 v6 CIDRs
  (`91.108.4.0/22` etc.) + 2 v6 (Telegram 2001:.../48). Approx 14
  routes, all approved.
* **relay-2** (100.64.100.4) — exit-node + the same Telegram 8 v4
  + 4 v6 CIDRs as relay-1. Approx 10 routes, all approved.
* **relay-3** (100.64.100.2) — exit-node + ~148 PrimaryRoutes that
  were configured by the operator's Windows setup (WARP/Google/
  Cloudflare/Telegram/Amazon/... — whatever `tailscale up` was
  told to advertise on the operator's box). Approved as-is, do
  not touch without explicit operator request.

For an admin to enable exit-node on a fresh relay:

```bash
# On the relay (as root or via sudo):
sudo tailscale set --advertise-exit-node
# Then on the headscale host:
docker exec headscale headscale nodes approve-routes \
  --identifier <N> --routes 0.0.0.0/0,::/0
```

To re-synchronise relay-3's full route set after a re-install:

```bash
# On headscale host (uses headscale API key from .env):
API_KEY=$(grep ^HEADSCALE_API_KEY= /home/admin/skygate/.env | cut -d= -f2-)
ROUTES=$(curl -s -H "Authorization: Bearer $API_KEY" \
  http://localhost:50444/api/v1/node/11 | python3 -c \
  "import sys,json; print(','.join(json.load(sys.stdin)['node']['availableRoutes']))")
docker exec headscale headscale nodes approve-routes \
  --identifier 11 --routes "$ROUTES"
```

### Relay setup scripts

* `deploy/tailscale-relay/setup.sh` — one-time per node: joins
  tailnet, advertises the canonical Telegram 8 v4 + 4 v6 CIDRs.
* `deploy/tailscale-relay/update-routes.sh` — cron-friendly refresh
  of the Telegram IP ranges. Resolves api.telegram.org from three
  public resolvers, aggregates to canonical CIDRs, re-applies.
  Refuses to apply an empty route list.
* `Makefile` has a `tailscale-update-telegram-routes RELAY=<host>`
  target that SSHes to the relay and runs the update script.

### 3-state reachability probe

`/admin/telegram` runs a 5s GET probe to api.telegram.org on every
page load. Banner shows one of three states:

* **ok_direct** — kernel route for the resolved IPs goes via
  eth0 (direct internet, no Tailscale involvement for this
  destination). Typical for non-RF VPSes.
* **ok_relay** — kernel route for the resolved IPs goes via
  tailscale0, which means a relay's subnet route covers the
  destination. Typical for RF deployments.
* **unreachable** — 5s timeout, 5xx, or DNS failure. Banner shows
  a troubleshooting bullet list with the resolved IPs.

The check is per-IP via `ip route get <ip>` (shell-out with a
2s timeout safety net). It's more accurate than the v1
"is tailscaled running" heuristic — tailscaled can be running
(joining the tailnet for admin / headscale access) without any
subnet route covering api.telegram.org, in which case the actual
traffic still goes via eth0. The kernel routing table is the
source of truth for "would this packet go via Tailscale?".

Implementation: `internal/handlers/handlers_telegram_probe.go` +
tests in `handlers_telegram_probe_test.go` (17 unit tests, all
PASS — including `TestProbeDirectEvenWithTailscaled` which is
the explicit regression guard for the v1 → v2 behavior fix).
Template: `internal/handlers/templates/admin/telegram.html`
(`.alert-probe` / `.probe-ok-direct` / `.probe-ok-relay` /
`.probe-unreachable`).

### Relay failover (Этап 14 v3)

All three relays offer the same exit-node capability. Tailscale's
client GUI lists them all; the client picks based on metric and
auto-failover is native. If a relay goes down, the client just
uses the next one — no skygate-side logic involved.

`update-routes.sh` on relay-1 and relay-2 is still cron'd weekly
(`0 4 * * 1`) to refresh the Telegram CIDR list from DNS. The
operator's relay-3 route set is a one-shot — no cron.

### Admin SSH into tag:public relays (Этап 14 v7)

The default headscale ACL is per-user isolation; without an
explicit rule, no Tailscale peer can SSH into the relay VPSes
(relay-1, relay-2, relay-3) because the broker-level `acls[]`
rule "allow * → tag:public:*" is overridden by Tailscale's
SSH-enforcement layer (which only consults `ssh[]`).

Two pieces are required to make admin SSH work:

1. **ACL rule** in `internal/acl/acl.go`:
   ```json
   {"action":"accept","src":["admin@tsnet.example.com"],
    "dst":["tag:public"],"users":["root"]}
   ```
   The existing `tag:exit-node` rule is preserved. Both rules
   must be present in the rendered JSON (asserted by
   `TestGenerateACLValidJSONShape`).
2. **tagOwners entry**: `tag:exit-node` is referenced in the
   SSH rules and elsewhere in the policy, so the parser requires
   it in `tagOwners`. Without it, `headscale policy set` rejects
   the policy with "tag not found: tag:exit-node".

After editing `acl.go` (e.g. to add new tags or new rules), the
policy must be re-applied. Three paths exist:

  - `POST /my/exit-rules` or `POST /my/exit-rules/delete` —
    any data change to exit rules triggers a SetPolicy
  - `POST /admin/exit-rules/rollback` — restore a previous
    `acl_snapshots` row
  - **NEW in v7**: `POST /admin/exit-rules/reapply` — regenerates
    the policy from the current DB state and pushes to headscale.
    Use this when only the *shape* of the policy changed (a new
    SSH rule, a new tag) but no exit rule was added/removed.
    Has a "Re-apply ACL" button on `/admin/exit-rules` (admin-only).

Tailscale on each relay polls for the new ACL within ~5-10 min
(usually faster). Until then, SSH from a Tailscale client to that
relay still says "tailnet policy does not permit you to SSH".

### Files for this feature

* `Dockerfile` — multi-stage with tailscale binaries
* `entrypoint.sh` — tailscaled + tailscale up --accept-routes
* `docker-compose.yml` — caps + tun + secret
* `internal/handlers/handlers_telegram_probe.go` — probe logic
* `internal/handlers/handlers_telegram_probe_test.go` — 17 tests
* `internal/handlers/admin_telegram.go` — integrates probe
* `internal/handlers/templates/admin/telegram.html` — banner
* `static/css/themes.css` — probe-state CSS
* `deploy/tailscale-relay/setup.sh` — one-time relay setup
* `deploy/tailscale-relay/update-routes.sh` — IP refresh
* `docs/telegram-relay.md` — full procedure + troubleshooting
* `docs/headplane.md` — Headplane (optional sidecar UI) integration
  contract, version pin policy, compatibility matrix, optional/required
  status, upgrade procedure, **existing-Headplane mode
  (`HEADPLANE_EXTERNAL_URL`)** added in v0.10.12. The module is documented as a peer
  service that talks to Headscale independently — Skygate has no
  code-level integration with it.
* `docs/derp.md` — DERP relay (bundled + existing) integration
  contract. `DERP_ENABLED` and `DERP_EXTERNAL_URLS` cover both
  modes; admin-side web-UI config is the v0.11.0 follow-up.
* `docs/skygate-as-shell.md` — the v0.11.0+ roadmap for
  pluggable Headscale / multi-control-plane / ACL import.
  Architectural doc, no code; tracks B and C from the
  user's "shelled module" idea.
  service that talks to Headscale independently — Skygate has no
  code-level integration with it.
* `internal/acl/acl.go` — GenerateACL (per-user policy + ssh rules
  + tagOwners). Edit + reapply via `/admin/exit-rules/reapply`.
* `internal/feature/exit_rules/form_reapply.go` — admin
  "Re-apply ACL" endpoint (moved here from
  `internal/handlers/exit_rules_form_reapply.go` in
  refactor-v0.30 Phase B step 4d, 2026-07-29)
* `internal/handlers/templates/admin/exit_rules.html` — adds
  "Re-apply ACL" button to the admin exit-rules page (v7)

---

## Node tagging (tag:private auto-applied)

`backfillNodeOwnership` (method on `*App` since commit `cebabab`) propagates
each portal user's nodes from skygate `node_owner_map` to headscale:

- **Direct match**: `node.PreAuthKeyID == preauth_keys.headscale_preauth_id`
- **Temporal fallback (Strategy C)**: preauth key created within 1 hour before
  the node was registered — sets `matchedTag = "tag:private"` for the matched
  node, calls `HS.TagNode(nodeIDInt, "tag:private")` to push to headscale,
  and clears tag:untagged rows via UPDATE-then-INSERT.

When the backfill injects `tag:private`, existing `tag:public` exit-node rows
are **preserved** (the UPDATE only fires when the current tag is empty or
`tag:untagged`). Admin still owns `PostAdminNodeTag` for manual overrides.

The UI at `/my/devices` shows the local `node_owner_map.tag` snapshot (so the
Tailscale Android client must wait ~60 s after a tag change for ACL updates
to propagate through to the Tailscale clients).

---

## Tailnet node state (Этап 14 v7, 2026-07-14)

All nodes in the tailnet `tsnet.example.com`, headscale id assignments
approximate — they shift on node re-create.

**Relays (`tag:public`, all `offers exit node` since 2026-07-14):**

* `relay-1` (100.64.100.3, headscale id=3) — exit-node + 8 v4 + 4 v6
  Telegram CIDRs. Update-routes cron: weekly Monday 04:00.
* `relay-2` (100.64.100.4, id=4) — exit-node + same Telegram 8 v4
  + 4 v6 CIDRs. Update-routes cron: weekly Monday 04:00.
* `relay-3` (100.64.100.2, id=11) — exit-node + ~148 PrimaryRoutes
  (operator's Windows setup, includes WARP/Google/Cloudflare/Amazon
  /Telegram/...). No cron — one-shot config.

**Clients (`tag:private`):**

* `skygate-host-1` (100.64.100.10, id=13) — the in-image skygate container.
  Was `skygate-host-1-1` originally, auto-promoted after the old
  host-side node was deleted (commit `f784b48`). The host's
  `tailscaled` was stopped and disabled on 2026-07-14 to eliminate
  the duplicate `skygate-host-1-1` node.
* `workstation-1` (100.64.100.1, id=9) — operator's Windows machine.
  Has `tailscale up --accept-routes` and may pick any relay as
  exit-node from the Tailscale GUI.
* `workstation-8` (100.64.100.7, id=7) — older Windows box, currently
  `offline` since 2026-07-13. Tagged `tag:private` but not in
  active use.
* `workstation-2` (100.64.100.5, id=10) — Android phone, `active; relay
  "mow"` (uses DERP for direct, not direct endpoint).
* `workstation-2-old` (100.64.100.8, id=8) — older phone, `offline` since
  2026-07-14 morning.
* `workstation-5` (100.64.100.6, id=6) — Android phone, `active`
  via DERP relay.

**Health check pattern:** Tailscale on any relay that doesn't have
an `ssh[]` rule covering itself prints to `sudo tailscale status`:

> `# Health check:`
> `#     - Tailscale SSH enabled, but access controls don't allow`
> `#       anyone to access this device. Update your tailnet's`
> `#       ACLs to allow access.`

This is a noisy "ACL doesn't permit SSH inbound" warning — it
appears on relays because no rule says "allow SSH into this
specific node". The `ssh[]` rules in `acl.go` only say
"admin → tag:exit-node" and "admin → tag:public" — they permit
SSH *to* the tag, not from the tag to itself. The warning is
**expected** and does not affect exit-node functionality. To
silence it, add a rule like
`{"src":["admin@…"],"dst":["autogroup:self"],"users":["root"]}`
to `ssh[]` — but it's a cosmetic improvement, not a functional
one.

---

## Working environment (VM vs Windows)

**The VM is the source of truth for runtime behaviour.** All deployment,
runtime, and end-to-end verification work happens on the VM:
`admin@192.0.2.1` (a.k.a. `192.0.2.1`).

**VM is for:**
- Building skygate (`docker compose restart skygate`)
- Running `make test` (smoke + `check_exit_nodes.py`)
- Any `docker exec` / `docker compose` / `headscale` CLI work
- Final go/no-go decision before pushing to `origin/main`

**Windows (this workspace) is for:**
- Editing source code, SQL migrations, configs
- Static checks only — schema diffs, migration ordering, env-var review in
  `internal/config/config.go`, headscale API surface checks
- Fast iteration on code (build locally for syntax/compile sanity)

**Never** use Windows as the `make test` source for a shipping decision.
If local and VM results disagree, **VM wins**. Local build = iteration
speed; VM `make test` green = ship.

Quick rule: before any `git push`, ssh to the VM, pull, and run
`make test`. Only push if `FINAL_EXIT=0`.

### The `skygate` host-side wrapper (v0.29.2+)

The skygate container is auto-named by compose (e.g.
`skygate-skygate-1`). For host-side commands that need to
address the container (`docker exec ...`, `docker logs ...`,
`docker stop ...`), use the `skygate` shell wrapper which
does a label-based lookup:

```bash
# Install once after every docker-compose.yml change that
# affects the skygate service:
ssh admin@192.0.2.1 'sudo cp /home/admin/skygate/deploy/skygate-cli.sh /usr/local/bin/skygate && sudo chmod +x /usr/local/bin/skygate'

# Then everywhere, instead of `docker exec skygate ...`:
skygate sqlite3 /data/skygate.db ".tables"
skygate tailscale status
skygate ps
```

The wrapper takes any docker exec args. It looks up the
container by `com.docker.compose.service=skygate` label, so
it works regardless of the auto-generated name (and even
across recreates — same label, new name, same `skygate`
command). All existing scripts (`e2e_pilot.sh`,
`deploy/subnet-router/allocate-existing-users.sh`,
`docs/...`) keep using `docker exec skygate` (the literal
token) because the wrapper accepts that and translates to
`docker exec <real-id>`.

To find the real ID yourself (e.g. for `docker logs --tail
100` or `docker inspect`):

```bash
skygate --id                # prints just the container ID
# or
docker ps --filter "label=com.docker.compose.service=skygate"
```

### Updating the VM (canonical procedure)

The skygate container is managed by `docker compose` — never use
`docker run` manually. The compose file has all the right mounts,
env, secrets, and capabilities; manual `docker run` skips them and
the container fails to build skygate (no source mount).

```bash
ssh admin@192.0.2.1
cd /home/admin/skygate

# 1. Pull latest main
git fetch origin && git merge --ff-only origin/main

# 2. Fix root-owned tailscale dirs (container tailscaled runs as
#    root; the bind-mounted state dir gets re-owned). Without
#    this, `go test ./...` on the VM fails with
#    "permission denied" on data/ts/profile-data/*.
sudo chown -R admin:admin data/ts/

# 3. Build the new image (compose uses the local Dockerfile +
#    the bind-mounted source)
docker compose build skygate

# 4. Recreate the container with the new image
docker compose up -d skygate

# 5. Wait for /healthz (first build can take 3-5 min)
until curl -fsS http://localhost:8080/healthz >/dev/null; do
  sleep 5
done

# 6. Verify the new build label
curl -s http://localhost:8080/healthz | python3 -c \
  "import sys,json; print('build:', json.load(sys.stdin)['build'])"

# 7. Run verify-post from the OPERATOR'S machine (Windows/Linux/Mac)
#    — the script SSHes into the VM and runs the 25-check catalog.
#    Cannot run on the VM itself (it would SSH into itself).
# On the operator's workstation:
make verify-post
# Expected: 26 PASS, 0 FAIL
```

If `docker compose up -d` fails with "container name /skygate is
already in use", the previous attempt left a dangling container.
Fix:

```bash
docker stop skygate
docker rm skygate
docker compose up -d skygate
```

### Self-update orchestrator (v0.29.0+, `/admin/update`)

The `/admin/update` page has a `Apply update` button that runs an
in-container orchestrator: it `git checkout`s the target tag,
rebuilds the image, recreates the container, polls `/healthz` for
60s, and auto-rollbacks on any failure.

**How the orchestrator finds the source tree (RepoPath)**:
`SKYGATE_REPO_PATH` is the in-container path of the source
bind-mount, which is **always `/app`** for the standard
docker-compose layout (`./:/app`). The host path
`/home/admin/skygate` is NOT visible from inside the container
— only the bind-mount is. The config auto-detects container mode
via `/.dockerenv` (Docker) or `/run/.containerenv` (Podman/CRI-O)
and falls back to `/home/admin/skygate` on a bare/systemd host.
Override via `SKYGATE_REPO_PATH` for non-standard layouts.

**How the orchestrator restores host file ownership**: every `git`
mutation runs as root inside the container, which would re-own all
files in the bind-mount to `root:root` and break the operator's
`git pull` / `make test` from the host shell. The orchestrator
captures the host owner (`stat -c '%u:%g' .git/HEAD` once, at
the start of the job) and runs `chown -R <uid>:<gid> /app` after
the build phase. Override via `SKYGATE_HOST_OWNER="1000:1000"` for
non-standard UIDs (e.g. rootless Docker, custom operator user).

**State file**: `/data/skygate-update-status.json` (bind-mounted
from the host's `/home/admin/skygate/data/`, so it survives
container recreate). The page reads this on every load and
auto-refreshes every 5s while a job is in flight. Format: see
`internal/update/state.go`.

**When to use `/admin/update` apply vs the manual procedure
above**:
- **Apply** (in-app): when updating to a tag that's already
  pushed to origin AND the changes are confined to Go code,
  templates, JS, or static assets. The orchestrator handles
  chown + container recreate + healthz polling. Failure →
  automatic rollback to the previous tag (state file shows
  `phase: rolled_back`).
- **Manual** (the procedure above): when the update touches
  `docker-compose.yml` itself, env vars, secrets, or
  bind-mounts. The orchestrator does NOT manage those — a
  compose-shape change requires a `docker compose down` +
  `up` cycle on the host, which only the operator can do.

**If `/admin/update` apply gets stuck at "PhaseFailed" with
"chdir ...: no such file or directory"**: the orchestrator
can't see the source dir. Verify
`SKYGATE_REPO_PATH=/app` (or your custom path) is set
correctly AND the bind-mount `./:/app` is in
`docker-compose.yml`. The error appears in the status file
under `error: "..."`.

**If the auto-rollback itself fails**: the status file shows
`phase: failed` and the `manual_fallback: true` flag, with
the failed command logged. The operator clears it by:
```bash
ssh admin@192.0.2.1
cd /home/admin/skygate
git status                # see which tag/commit is checked out
git log --oneline -3      # see the backup tag (skygate-pre-update-XXXXXXXX)
git checkout skygate-pre-update-XXXXXXXX
sudo chown -R admin:admin data/ts/
docker compose build skygate
docker compose up -d --force-recreate --no-deps skygate
```

---

## Smoke testing (make test)

```bash
make test                        # = smoke (bilingual: ru + en) + check_exit_nodes
SMOKE_LANG=ru make test          # one language only
SMOKE_LANG=en make test          # one language only
```

`scripts/smoke.sh` is a bilingual HTTP-level smoke test that exercises login,
device listing, /my/exit-rules CRUD, multi-delete, cascading, the /help page,
admin sync, admin cleanup, /admin/exit-rules/sync, /admin/users, /admin/devices,
static assets. Each step uses `curl` against `localhost:8080`.

**Bilingual mode (since 2026-07-11).** When `SMOKE_LANG` is unset, the script
re-invokes itself once per language (ru, then en) and prints two SUMMARY
lines. All curl calls carry `-H "Accept-Language: $SMOKE_LANG"`; each
sub-run uses its own cookie jar (`/tmp/smoke_ck.<lang>`). Per-language UI
strings (active-count label, page headings, add-rule button text, etc.)
are checked in steps 2/4/11 — a missing or stale `enCatalog` key now fails
the run. ok/bad/note are prefixed `[ru]` or `[en]` so the two streams are
visually separable when interleaved. Total budget: 59 + 59 = 118 smoke
assertions per `make test`.

**Critical pitfalls smoke catches**:
- API returns `ids: [N]` after POST so cleanup-by-id works (was: API didn't
  return ids; smoke couldn't delete its own test rules, accumulating "198.51.100.x"
  orphans in the DB).
- Multi-delete accepts `?id=N&ids=N1&ids=N2` (union of single + many).
- `r.Form` is lazy in Go net/http — handlers must call `r.ParseForm()` before
  reading `ids`.
- Don't accidentally re-introduce a `*:*` first ACL rule; smoke would not
  detect it (smoke runs skygate, not headscale).

Run smoke after ANY change to:
- `internal/feature/exit_rules/*.go`
- `internal/acl/acl.go` (or any exit-rules / ACL helpers)
- `internal/handlers/handlers*.go` (the App-level rendering
  + audit paths still touch every page)
- `scripts/smoke.sh`
- `Makefile`

Skymate rebuilds on every `docker compose restart`. There is no separate
build step in the container — `entrypoint.sh` does `go build -o /app/skygate
./cmd/skygate`. So `docker compose restart skygate` is enough.

---

## Common gotchas

1. **`r.Form` is lazy**: handlers reading form-data MUST call
   `r.ParseForm()` first. Forgetting causes "empty form" bugs.
2. **Go embed**: `templates.go` does `//go:embed templates/*.html
   templates/*/*.html`. New template files appear in the binary automatically
   on rebuild, no manual registration needed.
3. **`TagNode` uses CLI fallback** (`HS.ExecContainer` = env
   `HEADSCALE_CONTAINER`, default "headscale"). The admin API lacks the
   permission for `/api/v1/node/{id}/tag`, so most tag changes go via
   `docker exec headscale headscale nodes tag`. Skymate fires this from
   `backfillNodeOwnership` and from `PostAdminNodeTag`.
4. **`acl_snapshots.config` is a BLOB** of the JSON policy sent to
   headscale. The most recent version is what's *in* headscale; older
   versions are rollback snapshots accessible via
   `/admin/exit-rules/rollback`. After `GenerateACL()` writes a snapshot,
   `SetPolicy()` applies it. If `SetPolicy()` fails, the snapshot stays
   with `applied_success=0` (you can re-trigger via `PostAdminRollbackACL`).
5. **WAL on docker cp**: copying `skygate.db` requires the `.db-wal` and
   `.db-shm` files for an in-flight consistent view, OR `sqlite3 ... "PRAGMA
   wal_checkpoint(FULL);"` to flush. Skymate uses WAL mode by default.
6. **Tailscale Android visibility lag**: tag changes propagate to Tailscale
   clients in ~60-90 s. To force a refresh: tap the Tailscale icon, swipe
   the toggle off and on.
7. **Headscale 0.29 image has no shell in PATH** (no `sh`, `bash`, or
   busybox). `docker exec headscale sh -c "cat > /etc/headscale/..."`
   fails with `exec: "sh": executable file not found in $PATH`. Use
   `docker cp <tmpfile> headscale:/etc/headscale/...` instead — the
   daemon writes the file via its API, no shell inside the target
   container required. The v0.11.1 runtime renderer uses this pattern.
8. **Apply paths must load the full config from DB**, not the form's
   partial struct. The DERP form only has DERP fields, so its cfg
   has `HeadplaneMode == ""` (zero value), which would match the "off"
   branch in `applyHeadplane` and accidentally stop the running
   `headplane` container. The fix: `applyAndRenderDerp` re-reads
   `db.LoadIntegrationsFromOS` after Save and overlays the form's
   fields on top, so the apply reflects the FULL saved config.
9. **`docker compose restart` does NOT rebuild the skygate binary**.
   The entrypoint only runs on container create, not on restart. To
   pick up a new build, use `docker compose up -d --force-recreate
   --no-deps skygate`. After a code change, the version in the
   `/version` / web footer stays on the old commit until you do this.
   (Applies to the production VM at `192.0.2.1`.)
10. **CASCADE-LOCK on SQLite WAL** (v0.32.14, the exit-nodes 504 fix):
    `db.SetMaxOpenConns(1)` + `synchronous=FULL` is catastrophic under
    concurrent load. Single conn = every concurrent request waits the
    full `busy_timeout` (2-5s) for the writer to commit. With WAL +
    NORMAL sync, you get the same durability guarantee (WAL file +
    checkpoint) at 10-30x the throughput. Defaults in v0.32.5+:
    `MaxOpenConns(15)`, `MaxIdleConns(5)`, `synchronous=NORMAL`,
    `busy_timeout=2s`, `journal_mode=WAL`. The v0.32.4 corruption was
    caused by **disk-full** (R31 catches it), not by missing FULL
    sync. Never re-introduce `MaxOpenConns(1)` — it breaks
    `/admin/exit-nodes` and `/admin/users` under any real load.
11. **Distroless healthcheck pattern** (v0.32.16, the headplane fix):
    Distroless images (ghcr.io/tale/headplane:0.6.3, anything
    `cgr.dev/chainguard/*` or `gcr.io/distroless/*`) have NO shell,
    no `wget`/`curl`, no `/bin` utilities. A `healthcheck: test: wget
    http://127.0.0.1:PORT/healthz` fails with "executable file not
    found". The fix: use the runtime binary at a non-PATH absolute
    path with `-e` / `-c` inline. For Node: `["CMD", "/nodejs/bin/node",
    "-e", "require('http').get('http://127.0.0.1:PORT/healthz', r =>
    process.exit(r.statusCode === 200 ? 0 : 1)).on('error', () =>
    process.exit(1))"]`. For Python: `["CMD", "/usr/local/bin/python",
    "-c", "import urllib.request,sys; sys.exit(0 if
    urllib.request.urlopen('http://127.0.0.1:PORT/healthz').status ==
    200 else 1)"]`. **Use `127.0.0.1`, not `localhost`** — IPv6 may
    resolve `localhost` to `[::1]` and the service binds `0.0.0.0`,
    not `::`. **Always use `${SERVICE_PORT}` env var in the URL** —
    hardcoding 5000 breaks when the operator changes the env.
12. **NPM (Nginx Proxy Manager) blocks iptables NAT** (v0.32.17, the
    synya.example.com investigation): if traffic routes through NPM
    (the common case for `skygate.example.com`, `synya.example.com`,
    etc. on the operator's VM), NPM terminates the TCP connection
    at its own port (80/443) and proxies to the backend. Adding
    VM-level iptables DNAT/SNAT rules for the same ports is **dead
    code** — packets never reach the iptables chains. Diagnostic:
    `tail -f /data/logs/fallback_access_log.log` in the NPM container
    shows the actual proxy hop. If you see `upstream timed out (110:
    Connection timed out)`, the issue is the skygate app
    (slow/hung), not the network. If you see `connect() failed (111:
    Connection refused)`, the skygate process is dead. If you see
    `SSL_do_handshake() failed (wrong version number)`, NPM is
    talking HTTPS to an HTTP backend (scheme mismatch in NPM's
    proxy host config). Never assume iptables will fix routing
    problems on this VM without first checking if NPM is in the path.
13. **Exit-node online detection: trust headscale, not `last_seen`**
    (v0.32.17, the /admin/exit-nodes "1/3 healthy" fix): the
    monitor in `internal/monitoring/exit_node_monitor.go` was
    overriding `n.Online=true` to `false` whenever `last_seen` was
    older than `OfflineAfter`. Idle VPS exit-nodes (no peer traffic
    for hours) have stale `last_seen` but headscale still considers
    them online. Correct rule: trust headscale's `n.Online` as
    primary signal. `OfflineAfter` is only consulted when headscale
    says offline (forgiving fallback for transient headscale-side
    booleans). `SKYGATE_EXIT_NODE_OFFLINE_AFTER=10m` is the v0.32.17
    default; setting it to 0/empty disables the fallback entirely.
14. **Per-user subnet requires a REAL subnet-router on a REAL LAN**
    (v0.32.17, the 10.0.1.0/24 phantom route fix): `10.0.<uid>.0/24`
    is a **logical namespace** in headscale's ACL — it's not magic.
    For a user's `10.0.<uid>.0/24` to actually deliver packets to
    devices, a tailscaled node must (a) run on a network that
    physically has `10.0.<uid>.x` devices, (b) advertise the route
    to headscale, (c) have it auto-approved by the sidecar
    (`tag:subnet-router`), and (d) have `ip_forward=1`. The route
    is "phantom" if the subnet-router is on a network like
    `192.0.2.0/24` (a private LAN that doesn't actually contain
    the user's `10.0.<uid>.x` devices) — headscale accepts
    the route, other clients install it in their routing table, but
    the kernel at the subnet-router drops the packet because there's
    no actual `10.0.<uid>.x` device behind it. The
    `POST /admin/users/{id}/subnet/remove` handler (v0.32.18)
    cleans up phantom routers; use it instead of just `disable`-ing.
15. **Subnet-router Remove handler is idempotent** (v0.32.18): the
    full lifecycle is `provision` (v0.16.7) → user runs
    `setup.sh` → sidecar auto-approves → `router_active`. The
    inverse is `POST /admin/users/{id}/subnet/remove` (admin
    only). It (1) reads `user_subnets.router_node_id`, (2) calls
    `headscale.Client.DeleteNode(nodeID)` (failure logged, doesn't
    abort), (3) clears the `user_subnets` and `portal_users`
    denorm columns, (4) writes an audit row. ACL is NOT re-applied
    because `h-user-<user>-subnet` is always in the per-user grant
    regardless of router status. Clicking Remove twice is safe
    (no `user_subnets` row → 404).

---

## Editing checklist

Before committing a change to `internal/handlers/`,
`internal/feature/<name>/`, `internal/acl/`, `internal/db/`,
`internal/telegram/`, scripts/, or Makefile:

```bash
# 1. sanity-build (fast iterative) — Windows / local
cd <repo>
go build ./... 2>&1
go vet ./...
go test -count=1 -short ./... 2>&1

# 2. verify-pre (catalog, full) — Windows / local
$env:MSYSTEM = 'MINGW64'
$env:SKYGATE_BASH_MOUNT_ROOT = '/mnt'   # WSL2
bash scripts/verify_pre_deploy.sh

# 3. verify-pre + verify-post + smoke — VM
ssh admin@192.0.2.1
cd /home/admin/skygate
git pull
make verify-pre     # 17/18 PASS on Windows, 18/18 on VM (incl. B8 smoke)
make verify-post    # ~26/27 PASS
make test           # bilingual smoke (EN + RU)
```

If `go test ./...` fails at any of the `internal/feature/<name>/`
packages — the new per-feature tests pin the contracts that
used to live as `internal/handlers/*_test.go`. Run the
specific test with `-v` to see the failure.

If smoke fails at "step 8" (delete) — `smoke.sh` expects the API to return
the new rule id in `{ids: [N]}`. Check
`internal/feature/exit_rules/api.go` (was
`internal/handlers/exit_rules_api.go` pre-refactor).

If smoke fails at "step 11" (UI sanity: localized strings) — a key is
missing in the active language's catalog. Run `go test -count=1
./internal/i18n/...` to find it (TestCatalogsParity catches missing
keys; TestPlaceholderOrder catches %s/%d count mismatches between
languages). The catalog is now split into 12 per-feature
`catalog_<feature>.go` files (Phase C) — add the new key to the
right file, run `scripts/split_i18n.py` to regenerate the
glue if you're adding a new per-feature bucket.

If smoke fails at "step 10" (admin sync) — check `/admin/exit-rules/sync`
route registration in `cmd/skygate/main.go`.

If `make verify-pre` fails at B15/B16/B17 — the checks look
for test symbols in `internal/feature/<name>/`, not the legacy
`internal/handlers/`. Add the new test to the feature package
and re-run.

---

## Decomposition status

> **Refactor-v0.30 is complete** (Phases A, B-steps-1-to-6, C, D-steps-1-to-4
> landed 2026-07-28 to 2026-07-30). The full per-step history,
> metrics, what-worked/what-didn't, and lessons-for-next-refactor
> are in [`docs/refactor-v0.30-postmortem.md`](docs/refactor-v0.30-postmortem.md).
> This section keeps the **actionable guidance** for future work.

### Per-feature package pattern (mandatory for new handlers)

When adding a new handler, prefer the per-feature package pattern
over growing `internal/handlers/`:
- Drop a new directory `internal/feature/<name>/` with
  `handler.go + service.go + store.go + types.go + i18n_keys.go
  + bot.go + tests`, add 5-10 lines to `cmd/skygate/main.go`
  for the route, add 1-2 lines to `internal/telegram/dispatch.go`
  for the bot command. Done.
- For very small one-off features that don't justify a new
  package, add to the closest existing `feature/<name>/`
  package (e.g. `feature/admin/devices.go` is a fine home for
  a one-screen "admin/devices/{id}/meta" form).
- `internal/handlers/` is now **infrastructure only** (App +
  render helpers + public Backend-interface wrappers +
  static.go + templates.go). Don't add new HTTP handlers there.

### `internal/handlers/` (current state — 9 files, ~1.3k lines)

The package is shrunk to shared infrastructure. Per-file:
- `handlers.go` (~570) — App + New + render/renderWithLayout +
  pageFromName/pageTitle/dataValue + currentUser/audit +
  getMaxRulesForUser + i18n + the per-feature Service
  constructors (adminSvc, exitRulesSvc, mySvc, authSvc).
- `handlers_export.go` (~100) — public Backend-interface
  wrappers (Render, RenderWithLayout, CurrentUser, Audit,
  Config, HSGlobalFn, HSForUserFn, BackfillNodeOwnershipFn).
  Used by every `feature/*` Service.
- `app_controlplane.go` (~30) — thin `*App` methods that
  delegate to `*controlplane.Router` (PlaneURLForUser +
  InvalidateHCache; the HSGlobal/HSForUser methods were
  collapsed in Phase D4).
- `static.go` (~30) — embedded CSS/JS.
- `templates.go` (~140) — `embed.FS` for all templates
  (admin/* + user/* + themes.css).
- `handlers_node_ownership.go` (~400) — `backfillNodeOwnership`
  helper (still in handlers because it's used by both
  `feature/my/devices.go` and `feature/admin/devices.go` via
  the Backend.BackfillNodeOwnership callback; future
  cleanup moves it to `internal/nodeownership/` permanently).
- `handlers_test.go` (~200) — render + renderWithLayout tests.
- `templates_test.go` (~130) — template args-vs-catalog parity (B7).
- `app_controlplane_test.go` (~150) — control plane router tests.

The legacy "feature handlers live here" pattern is
**deprecated**. The old file list (`exit_rules_form_my.go`,
`admin_user_subnet.go`, `handlers_admin_nodes.go`, etc.) is
preserved in git history for context but those files don't
exist in the working tree anymore.

---

## "No hardcoded personal data in code" policy (v0.32.29, 2026-08-03)

**The github repo is a public artifact.** Any operator-specific
information in source files (DNS, public IPs, Tailscale IPs,
machine hostnames, real personal names) is exposed the moment
the commit is pushed. The 2026-07-29 cleanup went wide but
left operator-specific values in source constants and
test fixtures; the v0.32.29 pass moved them all to env.

**For future work**, any new code MUST follow these rules:

1. **Source-level defaults are placeholders only.** When a
   value is deployment-specific (DNS, IP, hostname, operator
   username, path under /home), the source default MUST be
   either:
   - A `tsnet.example.com` / `192.0.2.x` / `198.51.100.x` /
     `example.com` placeholder (RFC 5737 docs IPs + reserved
     example domains).
   - A generic term (`admin`, `workstation-1`, `relay-1`,
     `skygate-host-1`, `user1`, `/home/operator/...`).
   - Empty string with a documented `os.Getenv` fallback.
2. **The operator's real value lives in `.env`** on the
   deployment VM (NEVER in code, NEVER in `.env.example`
   defaults, NEVER in a test fixture unless the test is
   specifically about reading the real value).
3. **Test fixtures use the same placeholder defaults as
   the source.** A test that hardcodes `admin@tsnet.example.com`
   violates the policy — the right shape is
   `admin@tsnet.example.com` and the assertion checks for
   the placeholder, not the real value.
4. **Comments don't leak either.** "the operator's NPM at
   192.0.2.67" is the same kind of leak as
   `const npm = "192.0.2.67"`. Either generalize
   ("the operator's NPM host") or move the value to env.
5. **What is NOT personal data**:
   - The 100.64.100.0/10 Tailscale IP range (it's a
     public standard documented at tailscale.com).
   - RFC 1918 / RFC 5737 placeholder IPs and the
     example.com domain (these are reserved FOR
     documentation use).
   - Generic protocol/standard terms ("Tailscale
     client", "subnet-router", "exit-node") when used
     to describe the protocol, not a specific device.
6. **Audit checklist for new PRs**:
   - `git grep -nE '192\.168\.[0-9]+\.[0-9]+|45\.[0-9]+\.[0-9]+\.[0-9]+|skynas\.ru|admin|user1|user2|skygate-host-2|relay-1|relay-2|relay-3|skygate-host-1|workstation-1|workstation-2|nothing-phone|workstation-3' <new-files>`
     returns 0 hits.
   - `git grep -nE '100\.64\.0\.[0-9]+' <new-files>` returns
     only references to the `100.64.100.0/10` range, never a
     specific device IP.
   - If a new env var is added, `.env.example` documents it
     and the in-source default is a placeholder.

**When in doubt**: put it in env, leave the source default
as a placeholder, and add a comment pointing at the env var.
The operator can override at deploy time without touching
code.