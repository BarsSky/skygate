# AGENTS.md — AI hints for Skygate

**Compact index, not an encyclopedia.** This file holds (1) the rules you must not
break, (2) the deployment traps that have already cost real time, and (3) a
one-line index of every B-block. The long per-block narratives that used to live
here (≈919 KB, of which the instruction loader only ever showed a fraction) now
live in:

| Looking for | File |
|---|---|
| Documentation catalogue, reading orders | [`docs/README.md`](docs/README.md) |
| Install / update procedures (RU + EN) | [`docs/INSTALL.md`](docs/INSTALL.md), [`docs/UPDATE.md`](docs/UPDATE.md) |
| Roadmap, technical debt, blocked work | [`docs/ROADMAP.md`](docs/ROADMAP.md) |
| Incidents, root causes, recurring traps | [`docs/LESSONS.md`](docs/LESSONS.md) |
| Package map, invariants, the B-check system | [`docs/internals.md`](docs/internals.md) |
| Release / deploy / DB cutover procedures | [`docs/operations.md`](docs/operations.md) |
| The authoritative machine-readable catalogue | `scripts/verify_pre_deploy.sh` (`run_check "<ID>" "…"`) |
| Any block's full history | `git log`, and the block's own `scripts/check_b*.sh` |

Everything the old file said is recoverable from git history; nothing was dropped
silently — it was folded into the files above.

---

## 1. Rules you must not break

1. **The gate is the contract.** Run `bash scripts/verify_pre_deploy.sh` before
   pushing; it must end with `0 FAIL`. A check that needs live state (docker
   daemon, a DB, the VM) must report **`SKIP`, never `FAIL`**, when that state is
   unavailable.
2. **One grep-contract per feature.** A new feature/behaviour ships with
   `scripts/check_bNNN_*.sh`, registered in `scripts/verify_pre_deploy.sh`, and a
   one-line entry in the block index below.
3. **`gofmt`, `go vet ./...`, `staticcheck ./...` must be clean** (0 findings).
   `go test` must pass; do not silence a test to make a gate green.
4. **Work on the VM, verify on the VM.** Checks and live verification run on
   `<VM_HOST>`; local Windows is for editing only. Long-running verification must
   be run so its output can be read back.
5. **Never edit Go source with PowerShell line-index tricks.** Use the file tools
   (`edit`/`write`); a line-range rewrite has already corrupted a file once.
6. **Never commit a private LAN IP, real internal hostname, credential or key.**
   Use `<VM_HOST>`, `<PUBLIC_HOST>`, `example.com` and RFC 5737 addresses
   (`192.0.2.1`). A pre-commit hook rejects the known leak patterns.
7. **Three operator-managed files must survive every pull/reset:**
   `docker-compose.yml` (contains the operator's `extra_hosts` + `SKYGATE_HOST_TS`
   hostname), `go.mod`, `go.sum`. Run `git status` on the VM before any
   `git reset`/`git checkout`.
8. **The Docker image is deliberately linux/amd64-only** (v1.5.4+, Issue #4
   closure: the old amd64+arm64 matrix pushed the same tag twice and the second
   push overwrote the first). ARM operators use the Go tarballs or build locally.
   GHCR tags are case-sensitive — always lowercase (`ghcr.io/barssky/skygate`).
9. **Schema changes land in BOTH migration chains** (PostgreSQL *and* SQLite) and
   the SQLite side goes through the single chokepoint `execSQLiteDDL`. Restore
   tests must stay green (`internal/db`).
10. **i18n keys come in pairs** (RU + EN) — a missing key on either side fails the
    parity test.
11. **`migrate-only` is a subcommand**, not a flag. `docker compose` must be run
    from the project directory (or with `--env-file`).
12. **Documentation is flat**: `docs/*.md` plus `docs/ru/`. No `plans/`,
    `runbooks/`, `internal/` trees; planning goes to `docs/ROADMAP.md`.

---

## 2. Deployment traps (already paid for)

1. **`docker compose` interpolation is CWD-relative.** `docker compose -f
   /path/docker-compose.yml …` from elsewhere does *not* load the project `.env`,
   so every `${VAR:-default}` silently takes the default (this broke the reference
   deployment once: `extra_hosts` rendered `127.0.0.1` instead of the host IP).
   Always `cd` into the project directory.
2. **A host resolver that honours `/etc/hosts` leaks into containers.** If the
   host resolves its own public name to `127.0.0.1`, containers do too and reach
   their own loopback. Fix with an `extra_hosts` entry whose IP comes from `.env`.
3. **`--force-recreate` is mandatory** after changing `extra_hosts`, volumes or the
   image: a plain `restart` keeps the old container network config.
4. **The binary is rebuilt inside the container entrypoint** (`go build` on every
   start), so `git pull` + `restart` is a full update; `docker compose build` is
   only needed for Dockerfile changes.
5. **The pre-push hook runs the whole gate and can take many minutes** (each check
   spawns its own bash). For an emergency deploy `git push --no-verify` is
   acceptable; CI will catch regressions afterwards.
6. **A crashed unit refuses to restart** until `systemctl reset-failed <unit>`
   (seen during the native-update rollback drill).
7. **`systemctl restart` from inside the unit kills its own cgroup** — that is why
   the native updater needs a separate root-owned path/service pair; a `setsid`
   child dies with the cgroup.
8. **Run the gate with the Go bin dir that holds `staticcheck` on `PATH`** (usually
   `$HOME/go/bin`). Two spurious-FAIL traps, both hit on 2026-09-19: a plain root
   shell finds no `staticcheck` (`B95` FAILs with "staticcheck not found"), and the
   unprivileged operator user cannot read a root-owned checkout, so `go test ./...`
   dies on `data/oidc-keys-test` and ~90 checks report `permission denied`. The
   reference-VM invocation is `sudo env PATH="$HOME/go/bin:$PATH" GOFLAGS=-p=2 bash
   scripts/verify_pre_deploy.sh`.
9. **Never pipe a long-running producer into `grep -q` in a check that sets
   `pipefail`.** `cmd | grep -q '^ok'` FAILS SPURIOUSLY: `grep -q` exits at the
   first match and closes the pipe, so a still-writing `go test`/`staticcheck`
   dies with SIGPIPE (141) and `pipefail` turns that into a failed pipeline even
   though the command succeeded. This is what produced one rotating FAIL per gate
   run on 2026-09-19 (a different check each time, each passing standalone
   20/20): B235 E.2/E.3, B237.20 D.2, B212 T, B213 S. Capture first, then match —
   `OUT="$(cmd 2>&1)"; if grep -q '^ok' <<< "$OUT"; then …` (`scripts/check_b235.sh`
   and `check_b237_20.sh` are the fixed reference). The remaining sites are listed
   in `docs/ROADMAP.md` (TD-19). For any single FAIL, re-run that check standalone
   before treating it as a regression.

---

## 3. Where things live (short map)

* `cmd/skygate/main.go` — entrypoint: config → DB open/migrate → headscale client
  → services → routes.
* `internal/config`, `internal/db` — configuration and the dual-dialect data layer
  (migrations, dialect helpers, `DetectDSN`).
* `internal/headscale` — API client (+ cache invalidation).
* `internal/feature/*` — one package per feature (admin pages, my-pages, auth,
  exit rules, backup, cluster, …); handlers + templates + i18n catalogues live
  with the feature.
* `internal/handlers/templates/` — embedded `html/template` files.
* `internal/update/` — Docker upgrader, native privileged-helper client, state
  store, scheduler, manual-steps generator.
* `internal/acl`, `internal/nodeownership`, `internal/devicedelete`,
  `internal/oidc`, `internal/module` — ACL generation, node↔user ownership
  backfill, the shared device-delete coordinator, the OIDC provider, the module
  system.
* `deploy/` — installers (`install*.sh`), `skygate-apply-update.sh`, compose files,
  templates, snippets.
* `scripts/` — one `check_bNNN_*.sh` per contract, `verify_pre_deploy.sh`,
  `verify_post_deploy.sh`, operational helpers.

Full package map, invariants and the check-contract mechanics: `docs/internals.md`.

---

## 4. Guarantee catalog

Two catalogues run the contracts:

* **before a push** — `bash scripts/verify_pre_deploy.sh` (build-time + structural
  contracts; live-state checks SKIP when their dependency is missing);
* **after a deploy** — `bash scripts/verify_post_deploy.sh` (runtime checks against
  the running instance).

To extend them: write `scripts/check_bNNN_<slug>.sh`, print `PASS`/`FAIL`/`SKIP`
lines, exit non-zero only on a real failure, register it in
`scripts/verify_pre_deploy.sh` with a `run_check "BNNN" "…" 'test -f … && bash …'`
line, and add one line to the index below. Never put backticks inside a
`run_check` description — it is evaluated by bash (that bug is TD-15).

### Block index

One line per block, extracted from the pre-compaction AGENTS.md. Where the line
reads as a cross-reference rather than a description, treat it as a pointer: the
authoritative description is the block's `run_check` entry plus its
`scripts/check_b*.sh`. `B0`/`B87`/`B1883` are historical numbering artefacts.

- **B0** — 62f33b05). B260.2.2 verify check uses SNI matching cert
- **B1** — go test ./... exits 0 — go test ./...
- **B10** — no .env / *.key / *.pem in git tracked paths — git ls-files filtered
- **B100** — catalog check (scripts/check_b100.sh): 37/37
- **B101** — -B104 (v1.3.8) | BL-15 restore.sh for PG + BL-16 mount tests + BL-17 mig-verify + BL-18 in-app S3 download | 4 individual checks in verify_pre_deploy.sh |
- **B104** — BL-15 restore.sh for PG + BL-16 mount tests + BL-17 mig-verify + BL-18 in-app S3 download — 4 individual checks in verify_pre_deploy.sh
- **B105** — -B109 (v1.3.9) | Mobile-friendly + sidebar fixes | 5 checks in verify_pre_deploy.sh |
- **B107** — admin-breadcrumb sidebar offset: the breadcrumb was a SIBLING of .shell inside <main>, but only .shell had margin-left:220px — the breadcrumb had no left offset, so its leftmost 220px sat under the fixed sidebar. Fix:...
- **B109** — Mobile-friendly + sidebar fixes — 5 checks in verify_pre_deploy.sh
- **B11** — migrations have no destructive DDL (DROP/RENAME/TRUNCATE) — grep + pgmigrate test
- **B110** — (v1.3.10) | TAILNET SPLIT detection (3 Go tests + shell script + docs) | bash scripts/check_b110.sh |
- **B111** — (v1.3.11) B93 incomplete — isInfraNode rule 3
- **B112** — (v1.3.12) 5 staticcheck U1000 dead-code items
- **B113** — (v1.3.13) internal/feature/exit_rules/form_my.go
- **B114** — (v1.3.14) | BL-17 autonomous migration verify: 3-phase chain + portable Python driver staging + pre-state capture | bash scripts/check_b114.sh (9 contracts) |
- **B115** — (v1.3.16) side-effect of v1.3.15 (port fallback) —
- **B116** — (v1.3.17) new page /admin/derp/relays (the
- **B118** — contract E (5 → 4) B-check check_b118.sh
- **B119** — (v1.3.19.2) | TagToHostname (exported helper) extended to handle tag:dev-infra-X (v1.3.18.1 only fixed the LOCAL tagToHost closure in system_tests.go; missed the exported helper used by /my/exit-rules + /admin/exit-ru...
- **B12** — pgmigrate helpers are unit-tested (per-driver SQL form) — go test ./internal/db/pgmigrate/ -run TestBuildCreateIndexStmt
- **B120** — (v1.3.19.2) | admin-breadcrumb sidebar offset: the breadcrumb was a SIBLING of .shell inside <main>, but only .shell had margin-left:220px — the breadcrumb had no left offset, so its leftmost 220px sat under the fixed...
- **B121** — (v1.3.19.2 follow-up) | Three things in one: (1) new "Mint" theme (silver #f5f7f6 bg + mint-green #10b981 accent) for comfortable long admin sessions; (2) thin themed scrollbar (8px WebKit + scrollbar-width: thin Fire...
- **B122** — B122). 31/31 B123 contracts PASS in
- **B123** — (v1.3.19.2 follow-up) pure helper +
- **B124** — Previous: v1.3.19.2 follow-up — B123 + B124 (Goal 39) Exit Rules
- **B125** — (v1.3.19.2 follow-up) UNIQUE INDEX +
- **B126** — (v1.3.19.4) 2-line SQL change in
- **B127** — (v1.3.19.4) 9 R-checks refactored + R34 init
- **B128** — (v1.3.20) splitVersionParts(a, 4) + splitVersionParts(b, 4) in checker.go + 4-iteration loops in monitor.go + client.go
- **B129** — (v1.3.20) Apply button unconditional (no more AutoUpdateEnabled gating) + new Schedule section (toggle + HH:MM input + save + last-run) + config fields + i18n keys
- **B13** — pre-push hook uses MSYSTEM for Git Bash detection — grep -q MSYSTEM .githooks/pre-push
- **B130** — (v1.3.20) internal/update/scheduler.go (SchedulerDeps + Start + tick + runScheduled) + scheduler_db.go (init() binding db helpers) + main.go wire-up with cfg.UpdateScheduleEnabled guard + schedulerNotifierSink adapter
- **B14** — skygate host-side wrapper exists + syntax-valid + uses correct label — bash -n + grep com.docker.compose.service=skygate
- **B145** — "db.last_failover" (the B145-era key-value
- **B146** — fix
- **B147** — NOT /var/lib/derper/certs/ — see B147). The
- **B148** — localization debt that has accumulated since B148
- **B15** — + B16 ports done 2026-07-30 (commits 68aa0d6 +
- **B150** — internal/deploy/ is the B150 deploy CLI subcommand
- **B151** — + B152 + B153 close the
- **B152** — §6 status log 2026-08-24) — B151 + B152 + B153 close the
- **B153** — §6 status log 2026-08-24) — B151 + B152 + B153 close the
- **B155** — indicator + automatic InvalidateCache() in B155
- **B16** — exit-rules CDN detection regression tests (Cloudflare/Fastly/Google/Akamai) — v0.30.x Cloudflare anycast churn fix (internal/feature/exit_rules/cdn.go; tests dropped during refactor — see B16 in scripts/verify_pre_dep...
- **B160** — Root cause: the run_check "B160" description was
- **B160.1** — (v1.5.0) 2 deploy-bug fixes: 410 Gone (not
- **B160.2** — (v1.5.0) /my/devices cache bypass via
- **B161** — OIDC provider for headscale (B161.1 skeleton +
- **B161.1** — (v1.5.0) internal/oidc/ package skeleton
- **B161.2** — (v1.5.0) internal/oidc/authcode.go (in-memory
- **B161.3** — (v1.5.0) internal/oidc/jwt.go (RS256
- **B161.4** — closes the OIDC block with the
- **B162** — per-row device delete from /my/devices.
- **B163** — collapsible FAIL output on
- **B164** — DERP server init on a new host via SSH.
- **B165** — /my/devices registration form UX fix.
- **B166** — e2e + system tests for B160 + B162.
- **B167** — full Option C (docker + systemd + k8s + manual +
- **B167.1** — (rolled into B167 above): the generated
- **B168** — closes the operator side:
- **B169** — (v1.5.2) admin-side device delete on
- **B17** — per-user device can't be tagged as exit-node (v0.30.1) — guard in PostAdminNodeTag + tests in internal/feature/admin/devices_test.go (moved from internal/handlers/handlers_admin_nodes_test.go in refactor-v0.30 Phase B...
- **B170** — (v1.5.2) expired-row sub-classification
- **B171** — (v1.5.2) comprehensive device-delete with
- **B172** — (v1.5.2) login next-redirect fix
- **B173** — (v1.5.2) login form submit loading-state
- **B173.1** — (v1.5.2) full-page loading overlay
- **B174** — (v1.5.2) OIDC readSession uses
- **B174.1** — has no email column — a B174.1+ would
- **B175** — (v1.5.2) OIDC node auto-tag
- **B175.1** — uppercase tags) + B175.1 i18n tooltip rewrite
- **B176** — + B175.1 (v1.5.2) dev-tag
- **B177** — (v1.5.2) defensive dev-tag
- **B178** — (v1.5.2) /admin/exit-rules "preferred exit
- **B178.1** — (v1.5.2) live follow-up to B178. The first
- **B179** — (v1.5.2) iptables DOCKER-USER/INPUT over-broad
- **B18** — PG foundation builds (v0.31.0) — go build -tags postgres ./cmd/skygate + 4 verification tests in internal/db/test_pg_migrations_test.go
- **B180** — (v1.5.2) /admin/exit-nodes per-row "Re-sync"
- **B182** — (v1.5.2) /admin/exit-rules and /my/exit-rules
- **B183** — (v1.5.2) autoupdater duplicate device_rule
- **B184** — (v1.5.2) DOMAIN rule status propagates from
- **B185** — (v1.5.2) three follow-ups to the live operator
- **B186** — (v1.5.2) Telegram Bot API 10.1 Rich Messages
- **B186.1** — (v1.5.2) wire Telegram Rich Messages into
- **B186.2** — (v1.5.2) extend Rich Messages migration to
- **B186.3** — (v1.5.2) fix silent "0 devices" regression in
- **B187** — (v1.5.2) fix silent env.Username = "" regression
- **B188** — (v1.5.2) — ghost tag:exit-<host> exit-node-pref tags
- **B188.1** — (v1.5.2) — skygate acl-apply subcommand operator
- **B188.2** — fix
- **B188.3** — ports the per-CIDR via= loop to GenerateACLForPlane too (see B188.3 section below), so both useVia=true and useVia=false paths now emit the same selective pin. The original "B188.3 TODO" comment is obsolete — see the ...
- **B1883** — TestGenerateACLForPlane_B1883_NoDevicePref_NoPin
- **B189** — in internal/derphealth (B189 cron) — that's a
- **B19** — ACL perf + route correctness (v0.32.2) — go test ./internal/acl/ -run 'Benchmark\ — TestACLPerf'
- **B191** — fix
- **B194** — B194 (v1.5.0) — auto-deploy framework
- **B195** — wires automatic SSH execution (B195).
- **B195.1** — cluster_node. This was the planned B195.1
- **B196** — B196 (v1.5.0+) — /admin/database (Phase 1.1,
- **B197** — B197 (v1.5.0+) — /admin/database Phase 1.2
- **B198** — B198 (v1.5.0+) — DB migration workflow
- **B198.1** — B198.1 (v1.5.0+) — DB migration UI
- **B199** — B199 (v1.5.0+) — /admin/cluster cluster
- **B2** — backup verify (NEW) scripts/verify_backup.sh
- **B20** — (intermediate B-checks for v0.32.x-era bugs) — see scripts/verify_pre_deploy.sh for the full grep pattern
- **B200** — + audit). Framework waits for B200 + a second PG host
- **B201** — Phase 2.3 (B201, see below). Until B201 ships
- **B202** — B202 (v1.5.0+) — real dump/restore/cleanup
- **B202.5** — abstracted so B202.5 can drop in SSHDumpTransport
- **B203** — B203 (v1.5.0+) — pgxpool.Reset() + skygate-watchdog
- **B203.1** — same NULL + TEXT[] scan issues as B203.1.
- **B204** — Follow-up: B204 (HA elector auto-failover based on
- **B204.1** — errors (the B204.1 fix that switched from
- **B205** — the heartbeat state from B201) and B205 (skygate
- **B205.1** — (the live row hasn't been promoted yet). B205.1
- **B206** — is deferred to a follow-up B-block (the B206
- **B207** — changed" gap that was implicit in B207's
- **B207_fix** — B207_fix (v1.5.0+, 2026-09-02) — clear the
- **B208** — Small follow-up to the B208/B209/B210 incident where
- **B208.1** — — DBSource migration. The pre-B208
- **B208.2** — — /admin/ha cluster_audit events.
- **B209** — (probably Phase 4.5 — the B209 e2e base has the
- **B209.1** — across swaps. This is the B209.1 "clear current_dsn"
- **B209.2** — What's NOT covered (gaps for B209.2 once svi
- **B21** — B-block (B211 in the plan) and the
- **B210** — REST API directly, and hope the watchdog (B210)
- **B210.1** — The B210.1 fixedDBSource type alias in
- **B211** — B-block (B211 in the plan) and the
- **B212** — B212 (v1.5.0+, 2026-09-02) — skygate join
- **B213** — a follow-up (B213 candidate).
- **B214** — B214 (v1.5.0+, 2026-09-02) — /admin/database
- **B215** — B215 (v1.5.0+, 2026-09-02) — bootstrap state
- **B216** — B216 (v1.5.0+, 2026-09-02) — /admin/cluster
- **B217** — B217 (v1.5.0+, 2026-09-02) — /admin/cluster
- **B218** — / skygate init <standby-hostname> runs on the PRIMARY,
- **B219** — B219 (v1.5.0+, 2026-09-03) — /admin/database
- **B220** — B220 (v1.5.0+, 2026-09-03) — /admin/database
- **B221** — B221 (v1.5.0+, 2026-09-03) — generic audit
- **B222** — B222 (v1.5.0+, 2026-09-03) — /admin/cluster
- **B222.1** — keys in RU + EN). The B222.1 follow-up
- **B223** — B223 (v1.5.0+, 2026-09-03) — /admin/cluster
- **B223.1** — + cluster.discover_help). The B223.1
- **B224** — B224 (v1.5.0+, 2026-09-03) — stabilize
- **B225** — / skygate standby join consumes the preauth file to
- **B225.1** — is B225.1: DB health degraded alert (single
- **B225.2** — B225.2 follow-up is the "PG health degraded →
- **B226** — B226 (v1.5.0+, 2026-09-03) — /admin/cluster
- **B227** — B227 (v1.5.2+, 2026-09-03) — B77 tag-autoupdater
- **B227.1** — B227.1 (B-mod-reregister, 2026-09-13): per-row
- **B228** — adds
- **B229** — B229 (v1.5.2+, 2026-09-03) — preferred-exit
- **B230** — Out of scope (B230+ candidates): per-user
- **B231** — B231 (v1.5.2+, 2026-09-03) — preferred-exit
- **B232** — was silently reverted by B232 (V068, B188.2-era repair)
- **B233** — Out of scope (B233+ candidates): the
- **B234** — deferred to B234+ (needs an embedded DB).
- **B235** — fix
- **B236** — B236 (v1.5.2+, 2026-09-04) — Tailscale
- **B237** — fix (all three at once):
- **B237.10** — fix introduces a single conversion
- **B237.15** — B237.15 (v1.5.2+, 2026-09-07) — Deployment
- **B237.16** — B237.16 (v1.5.2+, 2026-09-07) — TD-2 + TD-14
- **B237.17** — fix
- **B237.18** — fix
- **B237.19** — fix
- **B237.20** — fix
- **B237.21** — B237.21 (v1.5.2+, 2026-09-07) — skygate
- **B237.22** — B237.22 (v1.5.2+, 2026-09-07) — UI-only CDN
- **B237.23** — fix restore 6-col ON CONFLICT in
- **B237.24** — initial fix (commit 1cd2ece6): append | lower
- **B237.24.1** — fix (commit, 2026-09-08): pre-compute the
- **B237.7** — fix (3 changes, no UI / no deploy
- **B238** — B238 (v1.5.2+, 2026-09-11) — portal_users
- **B24** — B17/B18/B19/B24/B31/B36-B40/B42/B54/B82-B85/B88/B93/B95 from
- **B243** — B243 (2026-09-12, B237.18 follow-up) — reconcile cron: PG placeholders + duplicate refusal
- **B245** — follow-up (the actual silent-failure root cause).
- **B251** — B251 (v1.5.6+, 2026-09-15) — skygate-host reserved name + B245 hujson fix.
- **B252** — B252 (v1.5.6+, 2026-09-15) — DERP cert auto-renewal (bundled derper).
- **B253** — fix stale-while-revalidate. The page render never
- **B254** — operator's own B254 (commit cb5f99f9 fix(sql): B254 — convert
- **B255** — fix (3 parts):
- **B256** — fix (single-line SQL change, no migration needed):
- **B257** — (v1.5.8+, 2026-09-15) scripts/check_hygiene.sh +
- **B258** — B258 (v1.5.8+, 2026-09-16) — /admin/tailscale mirrors entrypoint.sh "Tailscale skipped" state
- **B258.1** — (v1.5.8+, 2026-09-17) /admin/tailscale third visual state (auth-key path configured but file missing) + **2026-09-19: the «Сгенерировать ключ» control is a visible button** in the auth-key card (it used to be a collapsed `<details>` labelled «Сгенерировать автоматически», so the warning named a button the page did not show); contracts K/L in `scripts/check_b258_1_auth_key_missing.sh`
- **B259** — B259 (v1.5.8+, 2026-09-16) — /admin/tailscale toggle: Enable / Disable Tailscale in container via UI
- **B259.1** — B259.1 (v1.5.8+, 2026-09-17) — B259 enable flow uses canonical findUserForHostname (no phantom skygate-host headscale user)
- **B259.2** — B259.2 (v1.5.8+, 2026-09-17) — findUserForHostname DO-NOT-INLINE banner + B-check section K regression guard
- **B26** — (v1.3.1) Dockerfile runtime is CGO_ENABLED=0 — no
- **B260** — (v1.5.8+, 2026-09-17) /admin/derp status
- **B260.1** — (v1.5.8+, 2026-09-17) derperLivenessWebSocketProbe
- **B260.2** — (v1.5.8+, 2026-09-17) derper-in-docker migration.
- **B260.2.1** — systemd derper with the docker container. B260.2.1
- **B260.2.2** — verify check uses SNI matching cert
- **B260.2.3** — (commit 8abf3ca4). B260.2.3 resolveDERPPort prefers DB
- **B260.2.4** — DERP_HTTP_PORT=8443 env-var shadow class. B260.2.4
- **B261** — (v1.5.9, 2026-09-18) native (systemd / bare-binary)
- **B261.1** — (v1.5.9, 2026-09-18) SKYGATE_DB=sqlite:/path never
- **B261.2** — –B261.5 (v1.5.9, 2026-09-18) the rest of what the live
- **B261.3** — mktemp -d creates the work dir 0700 root-owned and
- **B261.4** — --migrate-only does not exist: it is the
- **B261.5** — SKYGATE_UPDATE_BASE_URL (root-owned, https:// or
- **B262** — (v1.5.9, 2026-09-18) OpenRC/Alpine install kind + the missing
- **B263** — (v1.5.9, 2026-09-19) single-file release notes + the integrity chain for a checksum-less release: the `release` job (which had **no checkout step at all** — the real reason v1.5.8's body was empty) now checks `RELEASE-NOTES.md` out sparsely, extracts its `## vX.Y.Z` section into the GitHub Release body, always sets `notes_path`, and falls back to a generated commit list; the **installers** fall back to the GitHub per-asset `digest: sha256:<hex>` when a release publishes no `SHA256SUMS` (v1.5.3, v1.5.6–v1.5.8) instead of aborting. Contracts in `scripts/check_b261_native_self_update.sh` sections O (O–O8) and P (P, P2a–d, P3)
- **B264** — (v0.72, 2026-09-19) admin role delegation with an immutable primary admin: `/admin/users` gets per-row **Promote** / **Demote** buttons (`POST /admin/users/{id}/promote|demote`, admin-only, audit `admin_promote` / `admin_demote`) so an admin can grant **and** revoke the role for other users; V072 adds `portal_users.is_primary INTEGER NOT NULL DEFAULT 0` in **both** migration chains (backfill for `SKYGATE_ADMIN_USER`, default `admin`, lowest-id-admin fallback) plus the partial UNIQUE index `portal_users_one_primary_uniq WHERE is_primary = 1`, so at most one **primary** (bootstrap/root) row exists — it can never be demoted, deleted or renamed, and demote additionally refuses self-demotion and the last remaining admin. New helpers `SetPortalUserPrimary` / `IsPortalPrimaryAdmin` / `GetPortalIsAdminByID` / `CountPortalAdmins`; `bootstrapAdmin` re-asserts the marker on its already-exists branch. **Contract renegotiation:** `scripts/check_b_admin_user_sync.sh` contracts A/A2/B now assert *exactly one `is_primary=1` row, which must be `is_admin=1` and named `SKYGATE_ADMIN_USER`* (multiple delegated admins are expected and legal) plus a new contract P for the column; its live-DB sections still `SKIP` when docker/PG is unavailable. `scripts/check_b264_admin_delegation.sh` carries 60 contracts; procedure in `docs/operations.md` §11
- **B265** — (v1.5.9+, 2026-09-19) `/admin/derp` status truth + real exit-node pinning for tagged devices. (1) The STUN tile read `stun.counter_requests.success` from derper's `GET /debug/vars`, which upstream gates behind `tsweb.AllowDebugAccess` — the skygate container is never an admitted source, so the answer was always `403 debug access denied` and the page showed "STUN UDP :3478 closed" on a relay with 33709 successful STUN requests, a bound UDP socket and clients on `relay "mow"`. New `internal/feature/admin/derp_stun.go` performs a real RFC 5389 STUN Binding Request round trip over UDP (transaction-id + magic-cookie validated, XOR-MAPPED-ADDRESS decoded), reports the RTT and the failure reason, and surfaces the 403 as `DebugAccessDenied` instead of drawing zeros for the traffic/client/byte panels. (2) `internal/derphealth/map.go` mapped ownership inverted (`d.IsOwn = isBundled == 0`), so "Recommended DERP" and the main-page hero selected region 901 (`controlplane.tailscale.com`, a row `AutoMigrateDerpRelays` created from the legacy `derp.external_urls` **derpmap** URL) instead of the operator's own region 900 (live: 901 is_own=1 108ms vs 900 is_own=0 12ms); fixed via the pure helper `derpRelayIsOwn`, and `GetAdminDerpRelaysDerpmap` now skips derpmap-document rows, refuses to advertise a node that fails a TCP+TLS reachability probe (the live VM also published a dead `:8443` node) and de-duplicates node names inside a region. (3) ACL grants: the per-device `autogroup:internet` grant now carries `via=[<preferred tag>]` **only when the device has a resolved `device_exit_node_prefs` row** — pre-B265 nothing in the grants policy constrained a tagged device's exit-node choice, because headscale honours `via` for exit-node selection only when the grant's dst is `autogroup:internet` and the grant that carried it used the never-matching `src=user@`; the loose unpinned grant is replaced (not duplicated — grants are additive) for pinned devices, and devices WITHOUT a preference keep the old global behaviour. (4) `device_rules` rows with an empty denormalised `user_name` (173 of 328 live) emitted `src: ["100.64.0.x"]`, a selector that cancels headscale's exit-route exclusion for that prefix and dies on re-registration; `deviceTagForRule` now resolves the owner from `node_owner_map` by `device_id` and always prefers the tag. (5) `deny` rules are not expressible in `grants[]`: the apply pipeline now logs + audits how many were skipped instead of dropping them silently. (6) Case-sensitivity: `qSelectPerUserDeviceTags` and `nodeownership.BackfillInfra` lower-case the hostname, matching the tag actually applied to the node (a ghost tag silently blocks the device). (7) `SanityCheckExitNodeColocation` warns at boot when skygate runs on the same host as an exit node, with the warning also rendered on `/admin/telegram`. Contracts in `scripts/check_b265_derp_status_truth.sh`; `scripts/check_b188_2.sh` contracts B/S/V/W renegotiated (the pin is conditional now, not absent)
- **B267** — (v1.5.11, 2026-09-19) route approval must not require docker: `approveRoutesForNodeID` hardcoded `docker exec headscale /ko-app/headscale nodes approve-routes …`, so a NATIVE (systemd) install failed every "Tag as exit-node" / ApproveAllRoutes / relay approve with `exec: "docker": executable file not found in $PATH`. The REST API `POST /api/v1/node/{id}/approve_routes` is now tried FIRST (works on every install kind), and the CLI fallback goes through `runHeadscaleCLI` — `docker exec` only when `exec.LookPath("docker")` succeeds, otherwise the local `headscale` binary, with `SKYGATE_HEADSCALE_CLI` overriding the in-container path and an error naming both attempts. 8 contracts in `scripts/check_b267_headscale_cli_mode.sh`
- **B271** — (v1.5.12+, 2026-09-19) **the `/db/health` sampler must speak the database's dialect.** From the same operator journal: every 30 s the log carried `db_health: tick: db_health: 4 query error(s): [server: SQL logic error: no such function: pg_is_in_recovery … database.size: … current_database … maintenance: … pg_stat_user_tables … xlog.current: … pg_current_wal_lsn]` — `internal/feature/healthz/db_health.go`'s collector is PostgreSQL-shaped and ran unchanged against **SQLite**, so a healthy native install showed a permanently degraded DB-health badge plus five useless errors per tick (LESSONS L-16's dialect leak, in a *reader* the leak guards did not scan). `DBHealthConfig` gains `Dialect` (`"postgres"` default → every existing caller unchanged; `"sqlite"` → `collectSQLite` answering the same operator questions with `PRAGMA page_count`×`page_size` for the size, `SELECT sqlite_version()` for the version, `PRAGMA quick_check` for integrity (non-`ok` becomes the sample error) and `PRAGMA journal_mode` logged; the PostgreSQL-only replication/xlog panels stay empty instead of inventing values), and `main.go` passes `dialectKind.String()` — the value it already computed with `db.DetectDSN` — and logs `db-health: started (…, dialect=sqlite)`. Contracts F/F2–F5 in `scripts/check_b270_startup_blockers.sh` + `internal/feature/healthz/db_health_b271_test.go` (runs both branches against a real SQLite DB: the SQLite branch must succeed, the PostgreSQL branch must fail naming `pg_is_in_recovery`) — procedure in `docs/troubleshooting.md` §8.0.3
- **B270** — (v1.5.12+, 2026-09-19) **a broken OIDC key store must not kill the process, and the key dir must never be relative.** Same operator report as B269 — the journal showed the real exit was *not* the DB but `oidc: init failed: oidc: mkdir ./data/oidc-keys: mkdir ./data: permission denied`. `NewService`'s error was `log.Fatalf`, so an **unconfigured optional feature** (`SKYGATE_OIDC_ISSUER` was unset) killed the process *before* it bound its HTTP port; the unit stayed `active` with nothing listening. (1) `NewService` now degrades: it logs `oidc: KEY STORE UNAVAILABLE (…) — the process keeps running and the OIDC routes answer 503; fix SKYGATE_OIDC_KEY_DIR …`, returns the `Service` with a nil `Keys` + a `KeyStoreErr` reason, and `main.go` only warns; `KeyStore.Ready()` is nil-safe and the signing paths return an error, so `/oidc/jwks.json` answers 503 instead of panicking. (2) `defaultOIDCKeyDir` + `sqlitePathFromDSN` make the default key dir **absolute and data-dir anchored** (`<dir of skygate.db>/oidc-keys`, or `/var/lib/skygate/oidc-keys` for PostgreSQL); an explicit `SKYGATE_OIDC_KEY_DIR` still wins. (3) `install-common.sh` + `install-alpine.sh` create `<data_dir>/oidc-keys` (`0700`, service-owned). (4) The applier compares the port in `SKYGATE_UPDATE_HEALTH_URL` with `SKYGATE_PORT` from the env file and warns when they disagree — the live host polled `:8080` while running `SKYGATE_PORT=8082`, so **every** self-update rolled back on a healthy service (`WARN: PORT MISMATCH`). 21 contracts in `scripts/check_b270_startup_blockers.sh` (incl. a live probe that runs the real binary with an uncreatable key dir and asserts `/healthz` still answers) + `internal/oidc/oidc_b270_test.go`. **Also fixed here:** the B269 handler swap stored an `http.HandlerFunc` and then an `*http.ServeMux` in one `atomic.Value`, which panics (`store of inconsistently typed value`) **inside** the handover goroutine — invisible to grep, found by running the binary, pinned by `cmd/skygate/startup_handover_b269_test.go`
- **B269** — (v1.5.12+, 2026-09-19) **startup truth: bind the socket first, announce every boot phase.** A native/systemd install reported `systemctl is-active skygate` = `active` while `ss -ltnp | grep :8080` showed **nothing listening**; the process was demonstrably alive (its `dbmigrate-watchdog` + `db_health` samplers were logging) but `main()` had never reached `http.ListenAndServe`, and the operator's self-updater could only say `healthz did not report build 'v1.5.11' within 90s (last build: none)`. Nothing anywhere named the blocking step, because the listener was created **last** — so any slow or fatal step before it (config, DB open + 5×retry, migrations, headscale client, ACL/telegram/exit-rules wiring, route table) produced an active, portless, mute process. (1) New `internal/startup` package (`SetBuild`/`Enter`/`Phase`/`StageName`/`MarkReady`/`SetFatal`/`StatusCode`/`StageHandler`/`ResetForTest`) tracks the boot phase and renders the provisional body. (2) `cmd/skygate/main.go` validates `SKYGATE_PORT` (`listenAddr`), binds the socket **before** the DB open, and reports a bind failure via `startup.SetFatal` + non-zero exit — a port conflict is now the first thing reported instead of a `listen:` line minutes later. (3) The bound listener serves a provisional `/healthz` (`status:ok` + `build` + `phase` + `stage` + `ready` + `uptime_ms`/`phase_ms`/`timeline`) and an `atomic.Value` handler swap hands the **same** listener to the real mux at handover — no second bind, no `ListenAndServe`, hence no gap (and no port-takeover window). (4) Every phase (`config`, `db-open+migrate`, `headscale-client`, `background-crons`, `services+telegram`, `routes`, `handover`) logs `startup: phase=<name> +Nms (total=Nms)`, so the last phase line in the journal **is** the blocker. (5) A panic is logged with `debug.Stack()` and its phase, recorded as fatal (provisional `/healthz` → 503 + `error`, never a 200 for a dead boot) and exits 3 so systemd + the updater both see a failed start. (6) Because a booting process now answers with the **new** build string, `deploy/skygate-apply-update.sh` refuses any health body carrying `"ready":false` (its explicit boot marker) and logs the boot phase it is stuck in — a still-booting process can never be declared a deployed one. 18 contracts in `scripts/check_b269_startup_truth.sh` (source order + a behavioural probe that builds the real binary, points it at an unopenable DB and asserts `/healthz` still answers 200 + build + `phase:"db-open+migrate"` + `ready:false` while the process stays alive) and `internal/startup/startup_test.go`
- **B268** — (v1.5.12, 2026-09-19) the privileged update applier must explain a failed swap. A live native/systemd self-update rolled back with only `healthz did not report build 'v1.5.11' within 90s (last build: none)` — a sentence equally true for a masked unit, an unrunnable artifact, a port already owned by another instance and a service listening elsewhere, so the operator could not act on it. B268 makes `deploy/skygate-apply-update.sh` self-explaining: (1) `binary_smoke_test` runs the freshly extracted binary with `--version` under `timeout` **before** the swap and aborts without touching the installed binary when it cannot execute; (2) a pre-swap health baseline logs which build answers on `SKYGATE_UPDATE_HEALTH_URL` and **warns** when it disagrees with `FROM_VERSION`, naming the listener on that port (the docker-container-owns-8080 / two-instances class, where post-restart verification can never succeed); (3) `service_diagnostics` (unit state, listener via `ss`/`netstat`, binary listing, 15-line `journalctl -u` tail) is logged before **every** rollback; (4) verdicts now distinguish `the service did not come up: … never returned a healthy body within Ns` from `healthz did not report build X … (last build: Y)`, both carrying the unit state; (5) B269 follow-up: `build_matches` also rejects a body carrying `"ready":false` and the wait loop logs the boot phase, so the new "socket first" behaviour cannot make the updater blind. 11 contracts in `scripts/check_b268_applier_failure_diagnostics.sh` — including a behavioural half that drives the real applier against a local HTTP mirror with stubbed `systemctl`/`ss`/`journalctl`/`curl` and asserts the `done` / `rolled_back` / honest-`failed` verdicts plus the DIAG lines and the booting-body case (SKIPs off-Linux) — and 7 Go contracts in `internal/update/applier_b268_test.go` that pin the ORDER (smoke test + baseline before the swap, diagnostics before the rollback)
- **B266** — (v1.5.9+, 2026-09-19) exit-node registration from the admin panel + `ssh_target` option-injection fix. New `POST /admin/exit-nodes/register` (`internal/feature/admin/exit_node_register.go`, admin-only) mints a pre-auth key with `CreatePreauthKeyWithTags(infraHeadscaleUserID, ttl, false, ["tag:exit-node"])` — the tag's ACL owner is `infra@<baseDomain>`, so headscale rejects the tag when the key belongs to any other user, which is why no page could do this before — and renders a ready-to-run command block (install, ip_forward/NAT, `hostnamectl`, `tailscale up --login-server/--authkey/--hostname/--advertise-exit-node/--accept-routes`) with the TTL capped at 24 h. The key is parked in `global_settings` under an opaque one-time token (never in a redirect URL, so it cannot leak via access logs, browser history or Referer) and is cleared on the single render or by a 15-minute sweep; the node name is validated by `isSafeNodeName` before it enters the copy-paste command; a hostname already taken in the tailnet and an existing `exit_servers` row are surfaced as warnings next to the key. The same audit closed a confirmed container-root escalation: `exit_servers.ssh_target` is operator-writable and was appended POSITIONALLY to the ssh argv, so a stored `-oProxyCommand=<cmd>` was parsed by ssh as an OPTION and executed inside the container that holds `/var/run/docker.sock` + `NET_ADMIN`/`SYS_ADMIN`; `internal/headscale/routes.go` now gates it with `IsSafeSSHTarget` (anchored `[user@]host[:port]` regex, rejects a leading dash and every shell metacharacter), appends `--` before the host and pins `-o ProxyCommand=none -o IdentitiesOnly=yes`, and requires an absolute key path; `/admin/exit-nodes/add` validates both fields at write time. 26 contracts in `scripts/check_b266_exit_node_register.sh` + 4 pure-function test groups. **Still open** (V073 candidate): accepting an SSH private key in the panel (encrypted column via `db.EncryptForColumn`), an SSH provisioning script for a fresh VPS, per-row edit/enable/disable, and widening `SyncAdvertisedRoutes` to iterate `exit_servers` so a brand-new relay with no `device_rules` still gets `--accept-routes`
- **B3** — docs docs/disaster-recovery.md "See also"
- **B31** — B17/B18/B19/B24/B31/B36-B40/B42/B54/B82-B85/B88/B93/B95 from
- **B32** — verify-pre check (updated in the same PR) —
- **B34** — (v1.3.1) device_rules has no duplicates, queried via
- **B36** — B17/B18/B19/B24/B31/B36-B40/B42/B54/B82-B85/B88/B93/B95 from
- **B38** — fix (v1.3.12) last pre-existing FAIL in
- **B4** — i18n: ru and en key sets match — go test ./internal/i18n/ -run TestCatalogsParity
- **B40** — fix (in passing): the pre-existing B40 grep
- **B42** — B17/B18/B19/B24/B31/B36-B40/B42/B54/B82-B85/B88/B93/B95 from
- **B43** — empty string, which the v0.33.1 B43
- **B5** — /R20 Migration v0.47 was not idempotent: every skygate
- **B50** — as v0.33.1.6 (R1-R32, B1-B52) + B50 (devices table-wrap) +
- **B51** — B51 (backup path env) + B52 (no template-var-in-CSS).
- **B52** — as v0.33.1.6 (R1-R32, B1-B52) + B50 (devices table-wrap) +
- **B53** — scripts/verify_pre_deploy.sh (B53);
- **B54** — B17/B18/B19/B24/B31/B36-B40/B42/B54/B82-B85/B88/B93/B95 from
- **B56** — v0.33.1.10 B56 fix) or removed. Not a regression
- **B6** — /R11 v0.28.0 removed the catch-all * from grants, but
- **B65** — The v0.33.1.16 (B65) docker-compose.yml
- **B66** — 4 system_tests test bugs (B66-B68
- **B67** — portal user removal. 3 commits since v1.3.10 (10b672d,
- **B68** — a verify-pre check (12 grep-pins): the
- **B69** — verify-pre check (22 grep-pins) — the rename
- **B7** — templates: all embed.FS templates parse — go test ./internal/handlers/ -run TestLoadTemplates
- **B70** — (v1.3.1) auto-update orchestrator migrate step. The
- **B71** — page. The auto-update orchestrator (B70 + B71) was
- **B72** — verify-pre check (12 grep-pins): the 4
- **B73** — + B76 verify-pre checks (8 + 11 grep-pins):
- **B76** — all B1-B76+ checks). The comment was wrong
- **B77** — autoupdater didn't auto-tag the new
- **B78** — verify-pre check (16 grep-pins): the
- **B79** — (v1.3.1) exit-node pref INSERT placeholder fix,
- **B8** — smoke RU+EN 83/83 each (VM only) — make test on VM; skipped on Windows
- **B80** — : orchestrator swap uses operator .env .
- **B81** — : SSH target fallback to Tailscale IP .
- **B82** — : per-user device + tag:exit-node override .
- **B83** — after the B83 + B84 fixes. Tailscale
- **B84** — are dropped. The post-B84 "Operation
- **B85** — B17/B18/B19/B24/B31/B36-B40/B42/B54/B82-B85/B88/B93/B95 from
- **B86** — (B86, the post-v0.33.1.33 follow-up that
- **B87** — D7: was already B87, skipped.
- **B88** — : /admin/system_tests bug fixes . Pre-fix the
- **B89** — : Backfill Strategy D (tag fallback) .
- **B9** — RELEASE-NOTES.md has an entry for the new version — grep vX.Y.Z RELEASE-NOTES.md
- **B90** — /admin/telegram "Send test" works (B90, the
- **B91** — headplane down (consistent with B91 architectural
- **B92** — : Availability Checker (30s) + /admin/services page .
- **B93** — /B95 verify-pre updates (v1.3.12) pinned the
- **B94** — Previous: v0.33.1.42 — code debt cleanup (D1-D8) + operator cleanup (C2/C4/C5/C6) + backup polish (B2/B3) (B94). 1 commit since v0.33.1.41. All tests green; make verify-pre 91/91 PASS (B1-B94, B8 SKIP VM-only). What's...
- **B95** — : v0.34.0 code debt cleanup . The first release
- **B96** — (v1.1.0, TD-1) 22 admin pages grouped into 6
- **B97** — (v1.1.0, TD-3) mobile-responsive sidebar. The
- **B98** — (v1.1.1) | Exit-node speed/availability system tests (3 Go tests + i18n + form_reapply + TestRegistry pinning) | bash scripts/check_b98.sh (runs go tests + greps TestRegistry/TestLatency/TestAvailability) |
- **B99** — (v1.3.6) | bash is in Dockerfile runtime apk add (B99, v1.3.6 backup error fix) | grep -q 'apk add.*bash' Dockerfile |
- **B-mod-admin** — 580ed0cb — B-mod-admin — /admin/modules list + /admin/modules/{name} detail + POST handlers (install / start / stop / enable / disable / sub/{name}) + 18 i18n keys (RU+EN) + 2 templates + 6 unit tests + 13 B-check con...
- **B-mod-admin-user-sync** — B-mod-admin-user-sync (2026-09-12) — SKYGATE_ADMIN_USER ↔ headscale admin drift remediation (option c, full rename flow)
- **B-mod-bcheck** — d6ce98e9 — B-mod-bcheck — scripts/check_b_modules_admin_live.sh (314 lines, 7 live contracts): /healthz 200 + POST /login (302 + skygate_session cookie) + GET /admin/modules (200 + <code>tailscale</code> row + state p...
- **B-mod-bcheck-live** — B-mod-tailscale, B-mod-admin, B-mod-install, B-mod-bcheck-live,
- **B-mod-cleanup** — 5c8282f7 — B-mod-cleanup (cleanup-skygate.sh + B-check)
- **B-mod-cluster** — / B-mod-telegram / B-mod-derp / B-mod-exit sub-features already in B-mod-tailscale as 4 sub-features; the B-mod-* name suggests they could be split into separate B-blocks for independent review, but the implementation...
- **B-mod-core** — 4da6d1b2 — B-mod-core re-merge — Wires module.Manager in cmd/skygate/main.go (was reverted in 82c74b38 because Patroni was down + etcd on svi was unreachable at the time). Restored when svi polygon had working PG 18.6...
- **B-mod-db-retry** — ✓ 13/13 pass — B-mod-db-retry exponential backoff
- **B-mod-derp** — a96859d2 — B-mod-derp (state.Info derp_relay + prereq)
- **B-mod-exit** — e30fa525 — B-mod-exit (state.Info exit_node + timestamp)
- **B-mod-first-run-adoption** — portal_users row (B-mod-first-run-adoption +
- **B-mod-install** — 7c3d7275 — B-mod-install — deploy/scripts/install-tailscale.sh (operator-facing wrapper for the 3 install modes + none + uninstall) + integration in install-debian.sh step 7 (opt-in via SKYGATE_TS_* env vars, WARN on ...
- **B-mod-install-followup** — / bootstrap_standby.sh runs on the
- **B-mod-pg18-strftime-fix** — 20052463 — B-mod-pg18-strftime-fix — applied_migrations.applied_at DEFAULT uses PG-native EXTRACT(EPOCH FROM now())::bigint instead of SQLite-only strftime('%s','now'). Closes the chicken-and-egg where migrateV050PG d...
- **B-mod-pg-alive-polygon** — adc2c9e7 — B-mod-pg-alive-polygon (check_b_pg_alive.sh polygon mode)
- **B-mod-pg-bypass** — ✓ 8/8 pass (polygon mode) — B-mod-pg-bypass + B-mod-pg-alive-polygon
- **B-mod-reregister** — B-mod-reregister-fix1 (2026-09-14) — unescape <b> in re-register banner
- **B-mod-reregister-fix1** — B-mod-reregister-fix1 (2026-09-14) — unescape <b> in re-register banner
- **B-mod-reregister-fix2** — B-mod-reregister-fix2 (2026-09-14) — HTML-in-i18n audit script
- **B-mod-sqlite-pg-bidi** — B-mod-sqlite-pg-bidi (2026-09-11) — restore explicit SQLite support alongside Postgres
- **B-mod-static-embed** — B-mod-static-embed (2026-09-11) — embed static/ into the binary via embed.FS
- **B-mod-tailscale** — 5de05be5 — B-mod-tailscale — internal/module/tailscale/ package: real TailscaleModule with 3 install modes (os_level / in_container / attach) + 4 sub-features (cluster / telegram / derp / exit) + CmdRunner interface (...
- **B-mod-telegram** — eef9ae07 — B-mod-telegram (state.Info telegram_route + cidr)
- **B-mod-template-fix** — Bug 1: body-admin-modules template undefined (B-mod-template-fix)

---

## 5. Named blocks and terms pinned by contracts

These strings are asserted verbatim by existing check scripts, so they stay in this
file even though the surrounding narrative now lives in `docs/LESSONS.md` and
`docs/ROADMAP.md`.

* **`B-derper-cert`** — DERP certificate automation: the `derper certmode` setting
  and the cert-sync path (`derp_cert_sync` bookkeeping, `reloadDerper`). Operator
  view: `docs/derp.md` + `docs/https.md`.
* **`B-derp-fix` / `B-bug-fix`** (2026-09-15 DERP/infra batch) — the DERP status
  collection and relay-probe fixes (hardcoded probe URL, TLS vs plain HTTP probe,
  non-deterministic bundled-port lookup, WebSocket liveness fallback). Symptoms:
  `docs/troubleshooting.md`.
* **Standby provisioning** — `skygate init <standby-host>` uses
  `create-standby-preauth` and `SKYGATE_STANDBY_TS_AUTHKEY` (`netfilter-mode=nodir`
  for the standby's Tailscale). Procedure: `docs/ha.md`, `docs/operations.md`.
* **Registration methods** — both methods are supported and must stay documented:
  the classical pre-auth key flow and the OIDC flow (OIDC devices are matched to
  portal users by Strategy E; see the `tagged-devices` synthetic-user caveat).
* **`B252`** — DERP cert auto-renewal: `derp_cert_sync` table + `StartCertSyncCron`
  (24 h) + `/admin/derp/cert-sync/run`.
* **`B253`** — Telegram probe async: cached probe with stale-while-revalidate
  (`telegramProbeTTLSuccess` 30 s / error 5 min, background refresh).
* **`B255`** — Telegram background poll and "pin nearest exit node" for the relay
  selector.
* **`Phase 3.4`** — HA "Force cluster node failover"; **`Phase 3.6`** — the
  `failover-drill`. Implementation status: `docs/ha.md`.
* **Technical-debt markers kept for the contract scripts:** `TD-15` (backticks
  inside `run_check` descriptions were executed by bash), `TD-16` and `TD-18`
  (missing i18n keys `common.online`, `common.offline`, `cluster.col_actions`,
  `cluster.node_upgrade_help`), plus their follow-ups `TD-18.1` and `TD-18.2`.
  Status of all technical debt: `docs/ROADMAP.md`.

---

## 6. Maintaining this file

* Keep it an **index**: rules, traps, one line per block. Depth goes to
  `docs/LESSONS.md` (why), `docs/ROADMAP.md` (what next), `docs/internals.md`
  (how), `docs/operations.md` (procedures).
* When you add a block: append one line to the index, add the
  `scripts/check_bNNN_*.sh` contract, register it in `verify_pre_deploy.sh`, and
  describe the durable lesson in `docs/LESSONS.md`.
* When a block ships something an operator must know, update `docs/INSTALL.md` /
  `docs/UPDATE.md` / `docs/operations.md` in the same change — not just this file.
* Keep the file small on purpose: the agent instruction loader truncates oversized
  files (the pre-2026-09-18 version was ~919 KB and only a fraction was ever read).
