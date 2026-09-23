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
13. **Deployments go through the operator's `/admin/update` tab, not through
    SSH + `git pull` + `restart`.** The agent's job ends at "push the tag and
    publish the GitHub release"; the operator (or the cluster's in-app updater)
    applies it from the UI. SSH to the VM is the emergency / break-glass path,
    not the routine. This applies to every install kind — docker, native
    (systemd), k8s. Native install trap #12 below is unchanged: the new binary
    still has to come from somewhere the unit can read, but the "somewhere" is
    now the GitHub release the operator triggers through the UI, not a manual
    `git pull` on the host.
14. **A tag and a release are CI-gated — never tag a commit whose CI is not
    green.** `scripts/ci_gate.sh` is the single implementation of that question
    (0 green / 1 not green / 2 cannot verify, and `2` blocks too). Three callers
    use it: `.githooks/pre-tag`, the `preflight` job in
    `.github/workflows/release.yml` (every publishing job `needs: preflight`, so
    a tag pushed with `--no-verify` publishes nothing), and
    `.github/workflows/tag-release.yml` (the sanctioned way to create the tag
    itself — `gh workflow run tag-release.yml -f version=vX.Y.Z`). Do not add a
    second CI-status query anywhere; do not add a switch that skips the check
    (only `SKIP_PRE_TAG_CHECK=1` for the local hook and the
    `SKYGATE_ALLOW_TAG_OFF_MAIN` repo variable for a hotfix branch exist).
    Procedure: `docs/operations.md` §1.2.

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
10. **A Windows/Git-Bash gate run reports one extra FAIL that is not real: `B144`
    (`grep -P`).** The MSYS grep 3.0 aborts with `grep: -P supports only unibyte
    and UTF-8 locales` under the default Windows locale, so
    `grep -cPzo 'if tab != "history" \{\s*tab = "tests"'` prints nothing and
    `scripts/check_b144.sh` reports `default_tab=0`. The same check passes on the VM
    (UTF-8 locale) — that is the authoritative run. Do not "fix" the contract for
    this; fix the environment (`LC_ALL=C.UTF-8`) or read the VM result.
11. **`test -f` proves nothing about what was committed — `.gitignore` can eat a
    new script silently.** `cleanup_*.sh` is ignored (line 78), so
    `scripts/cleanup_b188_3_fixtures.sh` passed every local B274 contract and was
    still absent from the pushed tag; the operator's `--apply` run on the freshly
    pulled VM died with `No such file or directory` (v1.5.20 renamed it to
    `scripts/b188_3_fixture_cleanup.sh`). When a contract guards a file the
    operator is told to run, assert git **tracks** it —
    `git ls-files --error-unmatch <path>` — not that it exists on this disk
    (`scripts/check_b274_prefix_ownership.sh` contract E.1b is the reference).
12. **On a NATIVE (systemd) install `git pull && systemctl restart skygate` does
    NOT update the Go code.** The unit runs an installed binary and nothing
    rebuilds it — unlike the docker entrypoint (trap #4 above), which builds on
    every start. Scripts and templates in the checkout DO change, so the symptom
    is maddening: a new diagnostic *script* behaves like the new version while the
    fix itself silently is not running. Live on the `aro` host:
    `native_tagging_diag.sh` printed its new section 5b while `ReconcileTags` was
    still the old binary and logged nothing, and two verification rounds were
    spent on an empty journal. Upgrade a native host through the privileged
    applier (`/admin/update`, or whatever path installed the previous version) and
    confirm the deployed build in `/healthz` (`"build":"vX.Y.Z+&lt;sha&gt;"`) before
    believing any behavioural result.

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
- **B20** — (intermediate B-checks for v0.32.x-era bugs) — the autoupdater's `git fetch` must use `--force` (v0.32.6: without it a moved tag killed every later fetch with `would clobber existing tag`). 2026-09-19 **contract renegotiated** for B272.5: the inline fetch moved into `DockerUpgrader.fetchTarget` (so the offline-host mirror fallback reuses it), which the old `grep -A1 'PhasePullBuild'` adjacency assertion necessarily broke; `scripts/check_b20.sh` now asserts that EVERY fetch invocation carries `--force`, that the mirror argv slice does too, that `fetchTarget` exists, and that `manual.go` + the stale-tag note stay — see scripts/verify_pre_deploy.sh for the full description
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
- **B275** — (v1.5.22, 2026-09-20) **prefix assignment: skygate decides which relay serves which prefix.** B274 made prefix ownership deterministic but left the decision implicit ("the relay with the most rules wins") and left the per-CIDR ACL pin pointing at the rule's own exit node — so a device whose rule lost the contest was simply blocked (live: after B274, `skyworker`'s rutracker/Cloudflare timed out because its grant said `via=karolina` while `emilia` held the primary). B275 moves the decision into skygate and makes it editable: (1) v0.73 `prefix_owner(prefix PK, exit_node_id, source, claims, devices, updated_at)` in **both** migration chains (SQLite via `execSQLiteDDL`); (2) new `internal/prefixowner`: `Assign` — explicit rules win by **majority** with a hostname tie-break, a `source='manual'` operator pin is **never** overwritten while its relay is healthy, everything else is spread over the healthy relays and is **sticky** (nothing migrates without a reason), an unhealthy owner loses the prefix (even a manual one — a pinned relay that is down cannot carry traffic), and an empty healthy set falls back to the relays the rules named so the table is never emptied; `SetManual` (operator pin; empty relay = hand it back to the engine), `Reconcile`, `OwnerByPrefix`, `TagByPrefix`, `ViaForPrefix`; (3) `SyncAdvertisedRoutes` reconciles the table (B274's in-memory computation stays as the first-pass fallback) and the relays advertise exactly their owned set; (4) the per-CIDR ACL grant carries **`via=[OWNER]`** instead of `via=[the rule's exit node]`. 13 contracts in `scripts/check_b275_prefix_assignment.sh` + `internal/prefixowner/prefixowner_b275_test.go`. **Still open (B275.1):** an admin page/action for per-prefix re-assignment (the table is engine-owned + `SetManual` today), failover notifications when an owner goes down, and load weights per relay
- **B274** — (v1.5.19, 2026-09-20) **one advertising relay per prefix.** Live case on the operator's own Windows box (`skyworker`, accept-routes, **no exit node selected**): Cloudflare/Google/Akamai sites intermittently failed **only there** while every other device worked. `StaggeredSync`/`SyncAdvertisedRoutes` built each relay's `--advertise-routes` from that relay's OWN rules, so two relays advertised the same prefixes whenever two devices of different users pointed their rules at different relays — routine, because the CDN resolver expands ONE domain rule into the CDN's whole published range set: `basic` (michail) pinned 28 ranges (discord.*, rutracker.org) to **emilia**, `skyworker` (skyadmin) pinned the same 28 (auth.docker.io, registry.npmjs.org, rutracker.org) to **karolina**. headscale assigns ONE primary per prefix and both relays advertised them, so the primary **moved between two `nodes list` dumps on the same day** (emilia first, karolina after a staggeredSync pass); `skyworker`'s per-CIDR grant names karolina, so those destinations were dropped for it whenever emilia held the primary, while a device WITHOUT a pin kept working (its unpinned `autogroup:internet` grant follows whatever primary exists). New `internal/feature/exit_rules/prefix_owner.go`: `PrefixOwnership` (most claims wins, hostname tie-break so it cannot oscillate), `PrefixLosers`, `OwnedPrefixes` (always keeps `0.0.0.0/0` + `::/0`); all three sync paths advertise only the owned set; losers are logged (`prefix-ownership(...)`) and counted into the sync result as `prefix_conflicts`; `CollapseDuplicateDerivedRules` collapses the CDN-expansion duplicates (partition on the natural key, keep the `cdn:`-prefixed `parent_domain` per B183 — live: `basic` had **five** rows for `104.16.0.0/12`); `scripts/b188_3_fixture_cleanup.sh` removes the B188.3 fixture rows from a closed allow-list, dry-run unless `--apply`. Device rule lists are **never rewritten** — a contested prefix is reported so the operator picks the owner, because headscale serves a prefix from exactly one relay at a time. 21 contracts in `scripts/check_b274_prefix_ownership.sh` + `internal/feature/exit_rules/prefix_owner_b274_test.go`
- **B273** — (v1.5.18, 2026-09-19) **exit-node health tells the truth: one predicate for "is this an exit node", a ladder ordered by what blocks client egress, and a named reason instead of a false "не работает".** Operator report on the native host `aro`: `/admin/exit-nodes` rendered the red banner «Нет рабочих exit-узлов!» with `0/1 здоровых` and `state=offline` for the tailnet's ONLY relay (`exit-node-vps`, `100.64.0.1`, online, advertising `0.0.0.0/0` + `::/0`, last seen 47 m ago), while `/my/exit-nodes` showed the very same node as **online** — both pages read `headscale.NodeView`, so the disagreement was inside skygate. Root cause: `internal/headscale.hasExitNodeTag` (used by `ListExitNodes` → `/my/exit-nodes`) counts `tag:exit-node` **OR** an `exit-`/`exitnode` name prefix **OR** an advertised `0.0.0.0/0|::/0`, but `internal/monitoring.computeSnapshot` required the literal tag and ran `case !online || !hasTag: state = "offline"`, so a working untagged relay was filed as offline with `Healthy=false`. (1) `computeSnapshot` now gates on `NodeView.IsExitNode` (the shared predicate) and returns `(snapshot, ok)`; a node that is not an exit node gets **no snapshot row** and its stale row is pruned, so the `N/M здоровых` denominator counts exit nodes instead of every device in the tailnet (a 14-node tailnet carried 11 permanent `state=offline` rows describing laptops, and the bot listed them all). (2) New ladder: `offline` (headscale says not online) → `degraded` (**the `0.0.0.0/0` route is not APPROVED** — pre-B273 the test read `AvailableRoutes`, i.e. what is merely *advertised*, so an unapproved relay counted as healthy: the exact false positive the monitor exists to prevent) → `untagged` (approved + online, only `tag:exit-node` missing: it **works**, so it stays healthy and is surfaced as its own state with the tag hint) → `online`. (3) `exitNodeUsable()` (`online|untagged`) is the single source of truth for `Healthy` **and** for the alert boundary: `isCalmModeAlert` now fires on every crossing of it, so losing/gaining route approval alerts (silent before) while tagging or untagging a working relay stays quiet. (4) `/admin/exit-nodes` fills `ApprovedV4Default`/`ApprovedRoutesOK` from the live node's `ApprovedRoutes`, flags «не одобрено» next to the advertised route count, and renders two **named** warning banners («N без tag:exit-node» / «N с неодобренными маршрутами») instead of answering both problems with the red zero-healthy banner; the admin tag test is case-insensitive (`EqualFold`), matching headscale. (5) `/exit_nodes_health` gained the `untagged` bucket and counts untagged relays as healthy. 36 contracts in `scripts/check_b273_exit_node_truth.sh` + `TestComputeSnapshot_UntaggedIsHealthy_B273`, `TestComputeSnapshot_NotAnExitNode_IsSkipped_B273`, `TestTick_PrunesNonExitNodeRows_B273` in `internal/monitoring/exit_node_monitor_test.go`
- **B272** — (v1.5.13 … v1.5.17, 2026-09-19) **tags must actually reach headscale — file-mode policy, REST tag writes, drift reconciliation.** Operator audit on the native host `aro`: `node_owner_map` claimed `tag:dev-daniil-workpc` while `headscale nodes list` showed **no tags at all** on that node, so every per-device ACL rule matched nothing — and no audit row, metric or UI element said so. Three silent defects: (1) headscale ran with `policy.mode: file`, so `PUT /api/v1/policy` answered `500 {"code":2,"message":"update is disabled for modes other than database"}`; `SetPolicy` fell back to the file path only on 404/405 (so the fallback never ran) and wrote through a hardcoded docker volume (`/home/admin/headscale/config`) that cannot exist on a native install — now `isFileModePolicyError` recognises the 500 body, `setPolicyViaFile` writes the path resolved by `DiscoverPolicyPath`/`parseHeadscalePolicyConfig` atomically (native: direct temp+rename write + `systemctl`/`rc-service` restart, rolling the previous policy back when the reload fails; container: `SKYGATE_HEADSCALE_CONFIG_VOLUME`), and an unknown path errors naming `SKYGATE_HEADSCALE_POLICY_PATH` plus the policy text to apply by hand; (2) `TagNode`/`UntagNode` went straight to `docker exec … headscale nodes tag`, which on a native host fails with `exec: "docker": executable file not found in $PATH` — the REST API `POST /api/v1/node/{id}/tags` is now tried FIRST with an install-kind-aware `runHeadscaleCLI` fallback (the B267 pattern), so `AddTag`/`UntagNode` work without docker; (3) the B77 autoupdater walked headscale and only considered nodes that ALREADY carried a dev-tag, so a node whose tag never landed was skipped forever — new `ReconcileTags` walks **`node_owner_map`** every tick and re-applies any tag headscale is missing (`reason=tag_missing` through the B227 sink), while nodes attributed to no portal user are reported as `no_strategy` with their own `skygate_tag_unmatched_total` counter, so a device no per-device rule can match is finally visible. The admin adopt/transfer tag paths now report through the same `TagAlertSink` (metric + audit + rate-limited Telegram) instead of a bare audit row. v1.5.14 adds the missing half found on the live host one tick later: the reconciler called AddTag and headscale answered `400 requested tags [tag:dev-daniil-workpc] are invalid or not permitted` because the tag had no `tagOwners` entry — `ensureTagIsPermitted` now creates that entry from the database row (`<username>@<baseDomain>` + `tagged-devices@<baseDomain>`) BEFORE applying the tag, from the base domain `main.go` already has, and reports a missing `SKYGATE_BASE_DOMAIN` instead of retrying blindly. v1.5.15 adds the audit that makes the LAST link self-reporting: with `policy.mode: file` the policy file must be readable by the headscale SERVICE USER (`headscale:headscale`) and writable by skygate — live case: the file was `root:skygate 0660`, headscale answered `500 reading policy from path …: permission denied` for its own policy, and nothing on any page said so. `headscale.AuditHeadscalePolicy()` reports the status (`ok` / `not_applicable` / `unreadable_by_headscale` / `unreadable_by_skygate` / `missing` / `api_error`), the resolved path, who can do what (skygate's access asked from the kernel via `unix.Access`, the headscale account probed for real with `sudo -n -u … test -r`, falling back to a conservative owner/group/mode model) and ready-to-paste FIX commands — logged at boot and rendered as a red banner on /admin/derp. The audit is deliberately read-only: it never chowns another service's configuration. v1.5.16 closes the two blockers that surfaced next: (a) even with correct file permissions the write failed with `open …policy.hujson.skygate.tmp: read-only file system` because the skygate unit runs with `ProtectSystem=strict` + `ReadWritePaths=${data_dir} ${etc_dir}` — `/etc/headscale` is read-only for it BY DESIGN; rather than widening the service's mount namespace, the project's privilege split (B261) is reused: `RequestPolicyApply` drops a DATA-ONLY `<update_dir>/policy.request.props` and the root-owned `deploy/skygate-apply-policy.sh` (fired by `skygate-policy.path`/`.service`, written by install-common.sh) validates the absolute path against headscale's own `policy.path`, writes atomically, keeps `policy.prev` + rolls back, verifies the headscale user can read it; (b) an image update aborted with `error: The following untracked working tree files would be overwritten by checkout: scripts/skygate-move-to-infra.sh` — a file the target revision TRACKS, left untracked in the working tree — locking the operator out of every future update; `DockerUpgrader.checkoutRef` now computes the conflict set (untracked ∩ target tree, no non-existent `--dry-run`), backs each file up to `<update_dir>/checkout-stash/<ts>/` with its mode, removes it and retries, while a MODIFIED TRACKED file still aborts untouched. v1.5.17 makes an OFFLINE host updatable: `git fetch` died with `fatal: unable to access https://github.com/BarsSky/skygate.git/: Failed to connect to github.com:443 after 132571 ms` (network reachability, not a URL/404 issue) and the operator had no way to point the updater elsewhere — `DockerUpgrader.fetchTarget` keeps `origin` as the default and falls back to `SKYGATE_UPDATE_GIT_URL` (alias `SKYGATE_UPDATE_GIT_MIRROR`) or the DB key `update.git_url` (read per job through `Service.globalSettingFn()`, so the panel can change it without a restart), fetching with an explicit refspec (`+refs/heads/*:refs/remotes/origin/*`, `+refs/tags/*:refs/tags/*`) so tag and branch targets both resolve; with no mirror the log names the knob instead of only git's error. 48 contracts in `scripts/check_b272_tag_drift.sh` (sections H, I, J, K) + `internal/update/docker_fetch_b272_test.go` (real git: fallback brings tag + branches) + `internal/update/docker_checkout_b272_test.go` + `internal/headscale/policy_helper_b272_test.go` + `internal/update/docker_checkout_b272_test.go` (real git) + `internal/headscale/acl_b2722_test.go` + `internal/headscale/tags_b272_test.go` (incl. a real policy-file write with rollback) and `internal/nodeownership/auto_b272_test.go`
- **B272.7** — (v1.5.35) **one tagOwners policy write per reconcile pass.** Live on native `aro`: `applied=1 failed=2`, three devices, `grep -c 'tag:dev-'` = 1 — `EnsureTagOwner` read-modify-writes the WHOLE policy and ran per device while the file-mode applier writes asynchronously, so each write started from a snapshot without the previous one (the lost writes succeeded: a retry could not help). New `Client.EnsureTagOwners(map[string][]string)` (one write; shared `loadPolicyMap`/`policyTagOwners`; whole-request validation; `EnsureTagOwner` delegates); `ReconcileTags` computes the pending set with the apply loop's own predicates BEFORE the loop, permits all tags at once, retries the apply call in the same transient window (`addTagRetry`), falls back per tag on failure, and writes nothing for an empty set. 21 contracts in `scripts/check_b272_7_tag_owner_batch.sh`; depth in `docs/LESSONS.md` L-10.8
- **B276** — (v1.5.36) **the per-CIDR ACL pin must follow the assignment table.** skygate wrote the two halves of one decision at different times: the DATA plane (which relay advertises which prefix; headscale serves a subnet prefix from exactly ONE relay) is recomputed every sync pass, while the CONTROL plane (the ACL, whose per-CIDR grant carries `via=[owner]` since B275) was regenerated only on a rule/user/device change. Live: the ACL was applied 18:19, the table moved 28 Cloudflare/Google prefixes to the other relay after 19:26 and the routes followed — but the pins stayed on the old relay, and since `via` on a CIDR grant is a permission FILTER the clients silently lost those routes (measured: 70 of 177 grants named a non-primary relay, 35 named a prefix nobody advertises; the operator's device lost its whole Cloudflare set with no log line). `reconcilePrefixOwnership()` is now the single entry point of both sync paths: on an owner CHANGE it regenerates and pushes the policy through the shared `acl.ApplyGeneratedPolicy` tail behind a 60s shared throttle, after a fresh live read compared with `headscale.PolicyEquivalent`; `LoadClaims` GROUPs BY (prefix, relay, device) so the auto-updater's churn cannot decide the owner; `/admin/exit-nodes` shows whether the live policy equals the generated one and flags silent/duplicated/unserved owners (+ resync button). 28 contracts in `scripts/check_b276_acl_ownership_sync.sh`; depth in `docs/LESSONS.md` L-10.9
- **B276.1** — (v1.5.36) **«все мои устройства» must cover a device registered later.** The option exists since B275.3, but `PostMyExitRule` only materialised the rule for the devices that existed at that moment and forgot why — a laptop registered a week later had no rule while the page still promised the rule covered all of the user's devices. V074 adds `device_rules.all_devices` (both chains, additive default 0: pre-V074 rows stay single-device, because the fan-out copies are indistinguishable from hand-made ones and guessing would silently start copying rules nobody asked to copy); `db.MarkDeviceRulesAllDevices` marks the whole group on save; `propagateAllDeviceRules()` re-materialises the intent for the user's CURRENT devices (natural-key match → idempotent, copies keep the marker, an owner with no attributed device is reported) and runs in the rule-maintenance tick BEFORE the B276 policy-drift check, so the ACL covers the new device in the same pass. /my/exit-rules shows a badge on those rows (RU+EN). 21 contracts in `scripts/check_b276_1_all_devices.sh`
- **B271** — (v1.5.12, 2026-09-19) **the `/db/health` sampler must speak the database's dialect.** From the same operator journal: every 30 s the log carried `db_health: tick: db_health: 4 query error(s): [server: SQL logic error: no such function: pg_is_in_recovery … database.size: … current_database … maintenance: … pg_stat_user_tables … xlog.current: … pg_current_wal_lsn]` — `internal/feature/healthz/db_health.go`'s collector is PostgreSQL-shaped and ran unchanged against **SQLite**, so a healthy native install showed a permanently degraded DB-health badge plus five useless errors per tick (LESSONS L-16's dialect leak, in a *reader* the leak guards did not scan). `DBHealthConfig` gains `Dialect` (`"postgres"` default → every existing caller unchanged; `"sqlite"` → `collectSQLite` answering the same operator questions with `PRAGMA page_count`×`page_size` for the size, `SELECT sqlite_version()` for the version, `PRAGMA quick_check` for integrity (non-`ok` becomes the sample error) and `PRAGMA journal_mode` logged; the PostgreSQL-only replication/xlog panels stay empty instead of inventing values), and `main.go` passes `dialectKind.String()` — the value it already computed with `db.DetectDSN` — and logs `db-health: started (…, dialect=sqlite)`. Contracts F/F2–F5 in `scripts/check_b270_startup_blockers.sh` + `internal/feature/healthz/db_health_b271_test.go` (runs both branches against a real SQLite DB: the SQLite branch must succeed, the PostgreSQL branch must fail naming `pg_is_in_recovery`) — procedure in `docs/troubleshooting.md` §8.0.3
- **B270** — (v1.5.12, 2026-09-19) **a broken OIDC key store must not kill the process, and the key dir must never be relative.** Same operator report as B269 — the journal showed the real exit was *not* the DB but `oidc: init failed: oidc: mkdir ./data/oidc-keys: mkdir ./data: permission denied`. `NewService`'s error was `log.Fatalf`, so an **unconfigured optional feature** (`SKYGATE_OIDC_ISSUER` was unset) killed the process *before* it bound its HTTP port; the unit stayed `active` with nothing listening. (1) `NewService` now degrades: it logs `oidc: KEY STORE UNAVAILABLE (…) — the process keeps running and the OIDC routes answer 503; fix SKYGATE_OIDC_KEY_DIR …`, returns the `Service` with a nil `Keys` + a `KeyStoreErr` reason, and `main.go` only warns; `KeyStore.Ready()` is nil-safe and the signing paths return an error, so `/oidc/jwks.json` answers 503 instead of panicking. (2) `defaultOIDCKeyDir` + `sqlitePathFromDSN` make the default key dir **absolute and data-dir anchored** (`<dir of skygate.db>/oidc-keys`, or `/var/lib/skygate/oidc-keys` for PostgreSQL); an explicit `SKYGATE_OIDC_KEY_DIR` still wins. (3) `install-common.sh` + `install-alpine.sh` create `<data_dir>/oidc-keys` (`0700`, service-owned). (4) The applier compares the port in `SKYGATE_UPDATE_HEALTH_URL` with `SKYGATE_PORT` from the env file and warns when they disagree — the live host polled `:8080` while running `SKYGATE_PORT=8082`, so **every** self-update rolled back on a healthy service (`WARN: PORT MISMATCH`). 21 contracts in `scripts/check_b270_startup_blockers.sh` (incl. a live probe that runs the real binary with an uncreatable key dir and asserts `/healthz` still answers) + `internal/oidc/oidc_b270_test.go`. **Also fixed here:** the B269 handler swap stored an `http.HandlerFunc` and then an `*http.ServeMux` in one `atomic.Value`, which panics (`store of inconsistently typed value`) **inside** the handover goroutine — invisible to grep, found by running the binary, pinned by `cmd/skygate/startup_handover_b269_test.go`
- **B269** — (v1.5.12, 2026-09-19) **startup truth: bind the socket first, announce every boot phase.** A native/systemd install reported `systemctl is-active skygate` = `active` while `ss -ltnp | grep :8080` showed **nothing listening**; the process was demonstrably alive (its `dbmigrate-watchdog` + `db_health` samplers were logging) but `main()` had never reached `http.ListenAndServe`, and the operator's self-updater could only say `healthz did not report build 'v1.5.11' within 90s (last build: none)`. Nothing anywhere named the blocking step, because the listener was created **last** — so any slow or fatal step before it (config, DB open + 5×retry, migrations, headscale client, ACL/telegram/exit-rules wiring, route table) produced an active, portless, mute process. (1) New `internal/startup` package (`SetBuild`/`Enter`/`Phase`/`StageName`/`MarkReady`/`SetFatal`/`StatusCode`/`StageHandler`/`ResetForTest`) tracks the boot phase and renders the provisional body. (2) `cmd/skygate/main.go` validates `SKYGATE_PORT` (`listenAddr`), binds the socket **before** the DB open, and reports a bind failure via `startup.SetFatal` + non-zero exit — a port conflict is now the first thing reported instead of a `listen:` line minutes later. (3) The bound listener serves a provisional `/healthz` (`status:ok` + `build` + `phase` + `stage` + `ready` + `uptime_ms`/`phase_ms`/`timeline`) and an `atomic.Value` handler swap hands the **same** listener to the real mux at handover — no second bind, no `ListenAndServe`, hence no gap (and no port-takeover window). (4) Every phase (`config`, `db-open+migrate`, `headscale-client`, `background-crons`, `services+telegram`, `routes`, `handover`) logs `startup: phase=<name> +Nms (total=Nms)`, so the last phase line in the journal **is** the blocker. (5) A panic is logged with `debug.Stack()` and its phase, recorded as fatal (provisional `/healthz` → 503 + `error`, never a 200 for a dead boot) and exits 3 so systemd + the updater both see a failed start. (6) Because a booting process now answers with the **new** build string, `deploy/skygate-apply-update.sh` refuses any health body carrying `"ready":false` (its explicit boot marker) and logs the boot phase it is stuck in — a still-booting process can never be declared a deployed one. 18 contracts in `scripts/check_b269_startup_truth.sh` (source order + a behavioural probe that builds the real binary, points it at an unopenable DB and asserts `/healthz` still answers 200 + build + `phase:"db-open+migrate"` + `ready:false` while the process stays alive) and `internal/startup/startup_test.go`
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
- **B279** — (v1.5.46, 2026-09-22) **a class tag is not a node identity.** Live case (native host `aro`): the only relay carried `tag:exit-node` and nothing else, four copies of "strip `tag:` to get a hostname" disagreed about it, and the exit_rules copy returned the hostname **`node`** — a relay that has never existed (headscale had `exit-node-vps`, `workpc`, `laptop`, `homepc`). The operator's "Use preferred" click wrote it into 21 + 3 rules (`my_exit_rules_apply_preferred preferred=node updated=21`), after which route advertisement, route approval and the ACL `via` pin all resolved to nothing; meanwhile `exit-node-monitor`'s per-tick `SyncNodesFromHeadscale` fed headscale's `Tags[0]` (the class tag) back into `node_owner_map`, so no per-node tag survived and the B272 reconciler reported "already in sync" (silence). New `internal/db/tag_kind.go` is the single predicate (`IsClassTag` / `IsPerNodeTag` / `PickPerNodeTag`) and every consumer calls it: `NormalizeExitNodeTag` refuses the class tag (`ErrClassTagNotPerNode`), `TagToHostname` returns "" (= any exit-node), `PlanDevicePrefChange` never derives a pref from a class tag and `ClearClassTagPrefs` drops stored ones, both headscale→DB syncs refuse to overwrite a per-node tag with a class tag, `PostMyExitRulesApplyPreferred` validates its target against the live exit nodes, `SyncAdvertisedRoutes` also visits the relays the assignment table names, and `/my/exit-nodes` explains "give the relay its own tag" instead of offering the trap. 40 contracts in `scripts/check_b279_class_tag_identity.sh`; corrects v1.5.45's root-cause narrative (no rename ever happened).
- **B279.1** — (v1.5.46, 2026-09-22) **one tag→hostname implementation, one class-tag predicate.** B279 closed the bug but left four copies of "strip `tag:` to get a hostname": `db.isExitNodeTagForm`, `exit_rules.TagToHostname`, `acl.exitNodeTagToHostname` and an inline `tagToHost` closure in `internal/feature/admin/system_tests.go` (plus a hand-written `!HasPrefix(tag, "tag:exit-node")` guard in `user_subnet.go`). The ACL copy was the "safe" one — it returned `node` for the sentinel and its callers were documented to treat that as a non-match — which is precisely the shape that broke in the copy with no comment (live `aro`: 23 rules rewritten to the phantom relay `node`). Now `internal/db/tag_kind.go` owns the knowledge (`IsClassTag`, `IsPerNodeTag`, new `IsExitNodeTagForm`), `db.isExitNodeTagForm` delegates, the acl helper guards on `db.IsClassTag` *before* its bucket loop, the system-test page calls `exit_rules.TagToHostname`, and the sentinel literal is gone from `user_subnet.go`. New `internal/acl/acl_b279_1_test.go` pins the sentinel (`""`) and carries an anti-drift guard against `db.IsClassTag`; `check_b119.sh` contract H was renegotiated (it asserted the string `tag:dev-infra-` anywhere in `system_tests.go`, which a comment satisfies — a contract a comment can pass is not a contract). 16 contracts in `scripts/check_b279_1_tag_to_hostname_one_copy.sh`.
- **B280** — (v1.5.46, 2026-09-22) **a tag and a release are CI-gated.** The v1.5.41 → v1.5.45 cycle pushed tags after `git push --no-verify`; CI caught two real regressions in that window (v1.5.44 introduced an RU i18n parity break and a raw-`http.Error` leak) and both releases shipped them anyway, because the tag already existed and `release.yml` builds whatever the tag points at. Hooks are per-clone and `--no-verify` skips them, so the rule now exists server-side too. ONE implementation — `scripts/ci_gate.sh` (exit `0` green / `1` not green / `2` cannot verify, and **`2` blocks as well**; `--wait N` tolerates a run in progress; refuses a commit that is not an ancestor of the release branch; reads `ci.yml` runs filtered by `head_sha` so a later push can never vouch for an earlier commit) — with three consumers: `.githooks/pre-tag` (local: refuses `git tag -a`), the new `preflight` job in `release.yml` (`docker`, `binaries`, `sums` and `release` all `needs: preflight`, so an untested tag publishes neither images nor a Release), and the new `tag-release.yml` (manual `workflow_dispatch`: gate → annotated tag → dispatch `release.yml`, because a tag pushed with the repository `GITHUB_TOKEN` does not fire `push` events). Escape hatches are narrow and visible: `SKIP_PRE_TAG_CHECK=1` for the local hook, the `SKYGATE_ALLOW_TAG_OFF_MAIN` repo variable for a hotfix branch, `SKYGATE_PREFLIGHT_WAIT_SECONDS` for the wait budget; the CI check itself is never skippable. Rule 14 in §1, procedure in `docs/operations.md` §1.2, 43 contracts in `scripts/check_b280_ci_gates_release.sh` (incl. a stubbed-`gh` behavioural half covering green/failed/cancelled/timed-out/running/absent/API-error and the two ancestry outcomes).
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
- **B281** — (v1.5.46, 2026-09-22) **the CI catalog job must terminate, and "green" must mean 0 FAIL.** The v1.5.46 tag could not be cut because `verify-pre` came back `cancelled` on every push: it was not a concurrency cancellation, the job exceeded its own `timeout-minutes: 30` and the log stopped mid-catalog (last check B260, then 22 min of silence). Four defects behind that one row: (1) `run_check` in `scripts/verify_pre_deploy.sh` ran every check unbounded — it now wraps each in `timeout "${SKYGATE_CHECK_TIMEOUT:-900}"` and reports `TIMEOUT <name>` as a NAMED row while the rest of the catalog keeps running; (2) the job budget is 60 min; (3) the catalog is fail-tolerant by design (always exit 0 — `docs/operations.md` §1.3), so CI reported SUCCESS with ~15 FAIL rows in it — the run step now tees the log, strips ANSI and fails the job on any `^  (FAIL|TIMEOUT)  ` row, which is the half that makes `scripts/ci_gate.sh` (B280) mean something; (4) the FAILs: 36 check scripts probed `/usr/local/go/bin/go` BEFORE `command -v go` (runner Go 1.24.13 + `GOTOOLCHAIN=local` vs `go.mod` >= 1.25 — the `test` job was green throughout because it uses the PATH Go), `B95`/`B237.16` needed a `staticcheck` the runner never had (now installed + `$(go env GOPATH)/bin` on `$GITHUB_PATH`), 8 live-state checks FAILed where AGENTS.md §1.1 requires SKIP (`bad()` in `check_b_tag_owners.sh` even exited 1), `check_b191.sh` printed `FAIL …` and *then* SKIPped with exit 0, and `check_b182.sh` [D-annotator-call] spelled an unescaped `(` inside `grep -E` (unbalanced group → grep exit 2 → count 0 → a contract that could never pass; the property was already asserted by E2 + I, so the duplicate is gone). Contract: `scripts/check_b281_ci_catalog_truth.sh` (job-block-scoped A–D + a repo-wide regression guard on the go-probe order; sections L–N pin the three environment-fact repairs — the temp-dir glob that hung the job for 22 minutes, the B268 harness's root-only `install` and leaked mirror port, and the manual dry-run-by-default `ghcr-prune.yml` that keeps the registry at the newest release: 77 → 1 package versions).

- **B282** — (2026-09-22) **an operator page must read the database the operator actually runs.** Live on the native host `aro` (SQLite at `/var/lib/skygate/skygate.db`) three surfaces were dead because the SQL was written for PostgreSQL and never branched: (1) `/admin/audit` answered `500 SQL logic error: unrecognized token: ":"` — the UNION over `audit_log` + `cluster_audit` typed the PG-only forms inline (`'audit_log'::text`, `to_timestamp(created_at)`, `''::text`, `detail::text`), so the page that records every silent rewrite (e.g. `my_exit_rules_apply_preferred preferred=node updated=21`) was unreadable; it now builds through `buildUnifiedAuditQuery(db.ActiveDialect(), …)` and decodes the timestamp with the new `db.ParseDBTime` (time.Time on PG, INTEGER unix or TEXT on SQLite) instead of scanning into a bare `time.Time`; (2) the `exit_rules.preferred_mismatch` system test failed with the same message because the join `node_owner_map.node_id = r.device_id::text` hardcoded the PG shorthand — it is now `preferredMismatchRulesQuery(kind)` using the new `db.DialectKind.CastText` (PG needs `::text`: `text = integer` is SQLSTATE 42883 without it; SQLite has no `::` token, so the statement does not parse); (3) `/admin/headscale/acl` answered `500 list acl: unmarshal policy: json: cannot unmarshal string into Go value of type admin.ACLView` because headscale returns the policy as a STRINGIFIED object (`{"policy":"{…}"}`) while `GetACL` cached the quoted literal — `GetACL` now unquotes that field and the new `headscale.PolicyJSON` (unquote → `hujson.Standardize`) is the single normaliser shared by `GetACL`, `ListACL`, the `exit_rules.all_in_headscale_acl` system test and `PolicyEquivalent`. 27 contracts in `scripts/check_b282_admin_reads_dialect.sh` + real-SQLite regression tests in `internal/db/dialect_b282_test.go`, `internal/headscale/policy_b282_test.go`, `internal/feature/admin/b282_sqlite_reads_test.go`.

- **B283** — (2026-09-22) **a policy write must not take the control plane down.** Live outage on native `aro` (`policy.mode: file`): skygate wrote a policy file headscale could not load — `hujson: line 302, column 23: invalid character ']' after object name` — the daemon crash-looped (counter 248), the tailnet lost its control plane and every device disappeared from the portal. Two defects: (1) `RequestPolicyApply` rewrote the watched `policy.request.props` **in place** while the root applier read it line by line, so a request landing mid-read spliced the head of one document onto the tail of another (the 7869-byte file matched the saved snapshot's size, was valid JSON in the DB and unparseable on disk); the handoff is now temp + `rename`; (2) nothing validated the document before writing it, and the applier treated `systemctl restart` as success (it returns 0 as soon as the unit is STARTED), so it logged `done` over a dying daemon and the `policy.prev` rollback never fired — the applier now validates the body before writing and polls `/health`, restoring the previous policy when headscale does not answer. Two amplifiers fixed too: the generated policy pinned per-CIDR grants to the B275 assignment table's owner tag while `tagOwners` did not declare it (headscale refuses such a document as a whole and will not START on it — the owner tags are now merged into the declared set, and `SetPolicy` refuses any policy whose grants reference undeclared tags), and the same policy was rewritten every ~5 minutes with each write restarting headscale (a semantically unchanged document is no longer rewritten). Note: the *first* outage was handled by hand — snapshot 297 was restored from `acl_snapshots` and headscale started on it; keep skygate stopped until this release is installed, because the running build rewrites the policy within minutes. Contracts in `scripts/check_b283_policy_write_safety.sh` + `internal/acl/acl_b283_test.go`, `internal/headscale/policy_validate_b283_test.go`, `internal/headscale/policy_helper_b283_test.go`.

- **B284** — (2026-09-22) **a device tag must be the tag the node carries.** The second, independent cause of the same `aro` outage: the ACL generator *synthesised* `tag:dev-<user>-<host>` from a user name instead of reading the tag off the node, and the name it used was headscale's synthetic owner for tagged nodes, `tagged-devices`. The refused snapshot's undeclared tags were exactly `tag:dev-tagged-devices-exit-node-vps` and `tag:dev-tagged-devices-workpc` (proven with `python3` over `acl_snapshots`; the working policy named none), and headscale refuses a policy that references an undeclared tag **as a whole** — in file mode it will not START on it (crash-loop 248, control plane down, every device gone from the portal). `deviceTagForRule` now returns `node_owner_map.tag` for the rule's device (the tag headscale carries and the one this generator declares) and otherwise **nothing**, so the caller falls back to the device-IP selector (weaker than a tag, but always matches); both per-device pref loops resolve the tag by hostname through `prefixowner.TagsByHost` instead of minting one; the B265 unit contract that pinned the synthesis is explicitly renegotiated with the synthetic-owner case. 11 contracts in `scripts/check_b284_device_tag_source.sh` + `internal/acl/acl_b284_test.go` (fails without the fix, naming the invented tag).

- **B285** — (2026-09-22) **a policy must declare every tag its grants reference.** One pass after B284: `acl-drift: re-apply FAILED (refusing to set a policy that references 1 tag(s) missing from tagOwners: tag:dev-daniil-workpc)`. The refusal came from B283's guard and was correct (headscale rejects such a document as a whole and in file mode will not start on it) — but it also meant the ACL could never apply. The grants take their tag from `node_owner_map.tag` (B284) while the `tagOwners` block derives entries from `GetPerUserDeviceTags`, which needs a **portal-user** row; on `aro` both `node_owner_map` rows are owned by headscale's synthetic `tagged-devices`, so the per-user block emitted nothing while the grants named `tag:dev-daniil-workpc` and `tag:dev-infra-exit-node-vps`. The generator now records every tag its grants name and sweeps the missing ones into `tagOwners` (owner parsed from the tag name), sorted for a stable document. Contracts: `scripts/check_b285_acl_tags_declared.sh` + `internal/acl/acl_b285_test.go` (the test seeds both rows with the synthetic owner and fails without the sweep, naming the undeclared tag).
- **B286** — (2026-09-22) **an empty slice must not take a page down.** `/my/exit-rules` answered `template: exit_rules.html:264:35: error calling index: reflect: slice index out of range` instead of the user's rules, because the template initialised its preferred-exit fallback with an unguarded `{{$pref := index $.DeviceInfos 0}}` while the page had rules but no device rows (live after the relay was moved to the infra owner). The same pattern sat in four more templates — `{{index .IPAddresses 0}}` on the user/admin device pages and the exit-nodes page (a node without addresses) and `{{if index .DERPs 0}}` on the DERP dashboard (an install with no relays). All five now use guarded forms (`{{with .IPAddresses}}{{index . 0}}{{end}}`, `{{with .DERPs}}`, and a plain-string `$pref`). Contracts in `scripts/check_b286_template_index_bounds.sh` (static: pins the absence of the construct that caused the panic, plus parse + handlers tests — no render test with an empty device list exists in that package yet, and the check says so).

- **B287** — (2026-09-22) **the per-device tag is an ownership record.** Operator report on `aro`: a user's tagged devices did not appear on `/my/devices` at all, only on `/admin/devices` — where every row's owner read `tagged-devices`. headscale reassigns a node to that synthetic user as soon as it wears any tag, and the DB snapshot copied the name verbatim, so both ownership tests on the user page (the live `n.UserName == username`, and `ListNodeOwnerNodeIDsByUsername`) missed exactly the devices the page exists to show. New `internal/db/device_tag.go`: `PerDeviceTag` (lowercased minting), `TagNamesUser` (matches the `tag:dev-<user>-` segment *including* its separator dash, so a shorter username cannot claim another's device; infra and class tags deliberately name no portal user), `HasPerDeviceTag`, and `ListNodeOwnerNodeIDsByUserTag` (the snapshot twin, with LIKE metacharacters escaped). `/my/devices` now treats a node wearing `tag:dev-<user>-<host>` as that user's in BOTH paths, and `IsTaggedGhost` (the B-mod-reregister banner + per-row button) is now `UserName == "tagged-devices" && !devTagApplied` — the synthetic owner alone no longer marks a correctly tagged device as a ghost to be re-keyed. 15 contracts in `scripts/check_b287_tag_ownership.sh` + `internal/db/device_tag_b287_test.go` (pins the pre-B287 blind spot: the by-username lookup returns nothing on the live data).

- **B288** — (2026-09-22) **policy drift must mean drift — and it must heal itself.**Operator report on `aro`: `/admin/exit-nodes` kept showing «политика headscale УСТАРЕЛА» while Exit Rules was green and the tailnet has one relay. The documents were 5082 (generated) vs 11341 (live) bytes, but 16 of the live `grants` are **duplicates** of generated ones (the pre-B274 generator emitted one grant per rule row; headscale grants are additive, so a duplicate is a no-op) and the rest of the difference was `tagOwners`. Four defects: (1) `PolicyEquivalent` compared decoded documents with `DeepEqual`, so duplicates read as drift forever — it now compares NORMALISED documents (`internal/headscale/policy_compare_b288.go`: set-like arrays — grants, ssh, owner/member lists — sorted and de-duplicated, while the order-sensitive legacy `acls`/`rules` list is deliberately left alone), and `PolicyDriftDetail` names which section differs so the banner stops blaming the `via` pins by default; (2) THREE writers emitted a per-device tag's owners differently (`<user>@` / `<portal-user>@+tagged-devices@` / `<row-username>@+tagged-devices@`, and the row username is the synthetic owner for every tagged node) so each rewrite undid the other's entry — one derivation now: `db.PerDeviceTagUser` + `db.TagOwnersForUser`, used by the ACL generator, `nodeownership` backfill and the tag reconciler; (3) the generator's declarations came from a portal-user JOIN plus a grant-referenced sweep, so `tag:dev-daniil-homepc`/`-laptop` (in `node_owner_map`, referenced by no grant) were in the live policy and MISSING from the generated one — `db.ListDevTagsFromOwnerMap` now supplies them, so an apply cannot delete a declaration a node wears; (4) `reconcilePrefixOwnership` consulted the drift check only when the assignment table MOVED (`chg > 0`), which never happens on a healthy host — it now runs on every pass behind a 5-minute throttle (`periodicDriftCheck`), so a pre-existing mismatch heals itself. Also fixed here: the legacy generator emitted `tag:public` as `["admin@<base>]` — invalid JSON, so on a host without `SKYGATE_ACL_VIA_ENABLED` every apply would have failed to parse. **B288.1** (same release, found by investigating the running instance): its ACL history showed an apply every five minutes with status `OK` while the served document stayed the pre-B274 one for days — (a) an accepted handoff is not a written policy, so the applier now records its verdict in `<update_dir>/policy-apply.status` (`ok`/`unchanged`/`failed` + reason), skygate reads it, the journal logs `the write is NOT landing` and `/admin/exit-nodes` prints it; (b) the applier's `tagOwners` UNION with the file on disk (B272.4) could never remove a stale key, so the live document could never equal the generated one — the union is removed now that both writers emit complete documents. 30 contracts in `scripts/check_b288_policy_drift_truth.sh` + `policy_compare_b288_test.go`, `policy_helper_b288_test.go`, `device_tag_b288_test.go`, `acl_b288_test.go`, `sync_b288_test.go`.

- **B289** — (2026-09-22) **the co-located DERP relay must reach the map — or the page must say why it did not.** Operator report on the containerised VM `skygate-host`: «локальный DERP сервер никак не заработает». The relay (region 900, `derp.skynas.ru:443`) was healthy — TCP 443 + UDP 3478 bound, the right Let's Encrypt cert, reachable at `192.168.13.69:443` — while `/admin/derp/relays/derpmap.json` (the map headscale merges with the public Tailscale map) answered `{"Regions":[]}`, so no client ever learned the local region existed. Cause: the B265 reachability guard dialled the relay HOSTNAME from inside the skygate container, and the container inherits the host's `/etc/hosts`, where the relay's own public name maps to `127.0.0.1` (deployment trap #2) — inside the container that is its own loopback, so every bundled node was refused (`dial tcp 127.0.0.1:443: connect: connection refused`) and the journal was the only place that said so. Fix: `derpReachabilityCandidates` + `hostResolvesToLoopback` probe every address a row is known by and try `SKYGATE_DERP_PROBE_HOST` FIRST when the name resolves to loopback; `probeDERPNodeReachableAny` publishes the node when any candidate answers (the public `HostName` stays in the map, clients resolve it themselves); the journal names the address that answered and lists every probed address on a skip; an enabled `is_bundled` region that publishes NO node is logged as an ERROR with its causes; and `/admin/derp/relays` renders a cached per-row «в карте / пропущен + причина» block (RU+EN) so this can never again be a journal-only fact. 15 contracts in `scripts/check_b289_derp_map_truth.sh` + `internal/feature/admin/derp_reach_b289_test.go` (incl. an end-to-end derpmap render on a real SQLite schema).

- **B289.1** — (2026-09-23) **dial the relay's ADDRESS, speak its HOSTNAME.** B289 made the guard *try* the operator's address (`SKYGATE_DERP_PROBE_HOST`) but also passed that address as the TLS `ServerName`; `derper --certmode=manual` resolves its certificate **by SNI** and Go's TLS client sends **no SNI at all for an IP literal**, so the LAN-IP candidate died with `cert mismatch with hostname: ""` (derper log) and the map stayed `{"Regions":{}}` on the live host while the relay was healthy — i.e. the fallback could never work in the one case it was written for. `derpProbeTarget{Addr,SNI}` + `derpProbeTargetsFor` keep the two apart for the map guard; `httpGetVia` does the same for the `/admin/derp` status probes (URL keeps the hostname, the TCP dial is pinned to the first answering candidate); `derphealth.dialTargetFor` fixes the health probe (it dialled the bare hostname, forced `:443` and ignored the row's URL, so `derp_health` read `dial tcp 127.0.0.1:443: connect: connection refused` for a healthy relay). The regression fixture is **SNI-strict** (fails on an unexpected/empty SNI) because `httptest.NewTLSServer` ignores SNI — which is why the broken fallback shipped green; one case pins the old behaviour as *unreachable*. No `docker-compose.yml` edit is needed (the hint travels in `.env`), and `deploy/deploy.sh` now writes `SKYGATE_DERP_PROBE_HOST` automatically when the relay name resolves to loopback and then asserts end to end that the map publishes region 900. `scripts/check_b289_derp_map_truth.sh` grew to 22 contracts (A6/A7, D3/D4, F1/F2, F3) + `internal/derphealth/probe_b289_1_test.go`; operator procedure in `docs/derp.md` §B289.1.

- **B296** — (2026-09-23) **the relay probe address is editable from the panel, and applying it needs no container recreate.** B289.1 taught every DERP probe to dial the operator's address (`SKYGATE_DERP_PROBE_HOST`) while still speaking the relay's public hostname as TLS SNI, and `deploy/deploy.sh` writes that variable for the operator — but the skygate container is created from `env_file: .env`, and Docker freezes that environment at container **creation**: `docker compose restart` re-runs the entrypoint and keeps the old environment, so applying an edited value meant `docker compose up --force-recreate` (AGENTS trap #3) over SSH, for something the operator can see is wrong right on `/admin/derp/relays` («пропущен: unreachable (probed …)»). The knob now has two layers, resolved **per probe** (never once at boot) by the new `internal/derpcfg`: `global_settings.derp.probe_host` (what the new card writes) **>** `.env SKYGATE_DERP_PROBE_HOST` **>** none — and every probe path reads it: the derpmap reachability guard, the `/admin/derp` status probes, the STUN candidate list and the `derp_health` cron (`DERPInfo.ProbeHost`, filled once by `FetchOwnDERPs`; `dialTargetFor` keeps the `.env` fallback for hand-built rows). No non-test code reads the environment variable directly any more, so a saved address cannot be silently ignored on one path. The card shows the effective value **and which layer it came from** (`db`/`env`/`default`), accepts a bare address (scheme, port, path, list, a typo'd IPv4 and a non-hostname are each refused with their own translated reason — the port belongs to the relay row's URL), drops the 30s map-verdict cache so the redirect renders the fresh «в карте / пропущен» verdict, and writes an audit row. 25 contracts in `scripts/check_b296_derp_probe_host_ui.sh` + `internal/derpcfg/derpcfg_b296_test.go` + `internal/feature/admin/derp_probe_host_b296_test.go` (which saves **through the handler** and re-fetches `/admin/derp/relays/derpmap.json`: without an address the unresolvable relay is dropped, after saving it is published under its public name, after clearing it is dropped again). Procedure in `docs/derp.md` §B296.

- **B290** — (2026-09-22) **OIDC must be enableable from the UI, without hand-editing `.env`.** Operator report: «нет удобного выставления включения OIDC — пока нет в env строчки нельзя никак настроить, но из описания непонятно что и как добавлять». The `oidc_settings` table + form have existed since v0.75 and the boot path read the row, but three defects made the feature feel absent: (1) saving answered *«OIDC settings saved. Restart skygate (/admin/update) to apply»*, so nothing the operator could observe changed; (2) the page rendered the **env** values, not the effective ones — a saved row was invisible on the page that saved it — and a DB row with `enabled=0` was ignored at boot (only the env off-switch counted); (3) `client_secret` was stored **in the clear**. Fix: `effectiveOIDCSettings` resolves **UI over env** with a per-field `ui`/`env`/`default` source badge and reports the env off-switch; `internal/oidc.Service` gained a lock-guarded `ApplyConfig`/`ConfigSnapshot` (every handler now reads through `Issuer()`/`ClientIDValue()`/…), and `adminSvc.OIDCApplier`/`OIDCStatusFn` (wired in `main.go`) make **Save apply immediately, no restart**; the page shows what the *running* provider holds next to the form; the secret is encrypted at rest with `SKYGATE_SECRET_KEY` behind an `enc:v1:` marker (legacy plaintext rows still read, a wrong key is a **named** error instead of a silently empty secret); `enabled=0` disables the provider at boot **and** live, expressed as an empty issuer so the routes stay mounted and can be switched back on; the form validates the issuer/redirect URIs and refuses to enable without an issuer or a secret (an empty secret field keeps the stored one); and every field carries RU+EN help naming what to put there and which headscale key it must match. 26 contracts in `scripts/check_b290_oidc_ui_enablement.sh` + `internal/feature/admin/oidc_settings_b290_test.go`.
- **B295** — (2026-09-23) **a failed policy READ must not become a blind WRITE (and a headscale restart).** Live on `aro`: the prefix card said «состояние политики неизвестно … `connect: connection refused`» while headscale was listening on exactly that address (`ss -ltnp` showed `headscale pid=117261` on `127.0.0.1:8081`, `systemctl is-active` = active, `listen_addr: 127.0.0.1:8081` and `HEADSCALE_URL=http://127.0.0.1:8081` agreed) — the refusal was **transient** — and the code made it permanent: `acl-drift: cannot read the live policy to decide whether a re-apply is needed (…) — applying unconditionally`. On a `policy.mode: file` host a policy write means `systemctl restart headscale` (0.29 re-reads the file only at startup), so **answering a read failure with a write answers a restart with another restart**: the observation fed the outage, the page could never converge, and every prefix stayed «нет маршрута». Fix (three parts): (1) a **transient** read failure (`connection refused` / `actively refused` / `no connection could be made` / `connectex` / i/o timeout / `deadline exceeded` / `timeout` / EOF, in POSIX **and** Windows spellings) is retried with a bounded, injectable delay (`aclReadRetries`/`aclReadRetryDelay`) before any fallback — covering the restart window — while an **HTTP answer** (401/404/500) is deliberately NOT retried, because the daemon is demonstrably up and the policy-file/CLI rungs are the interesting part; (2) when the read still fails, the write decision comes from the **last APPLIED snapshot in `acl_snapshots`** (`db.LastAppliedACLVersion` + `db.GetACLConfig` + `PolicyEquivalent`): generated == snapshot → **no write and no restart**, different → write, absent or unparseable → the pre-B295 blind apply survives but the log says the decision was blind; (3) `/admin/exit-nodes` reports «в синхроне» from that snapshot and **names the source** (`PolicyVia` = "compared with the last APPLIED snapshot vN (headscale did not answer)") instead of «состояние политики неизвестно», while a read failure with no usable snapshot stays a real error. 19 contracts in `scripts/check_b295_no_blind_policy_write.sh` + `internal/headscale/policy_snapshot_b295.go`/`policy_snapshot_b295_test.go`, `internal/feature/admin/exit_nodes_b295_test.go`.
- **B294** — (2026-09-23) **the live-policy read must not need docker, and a failed read must never render as a fact.** Operator report from `/admin/exit-nodes` on `aro`: «состояние политики неизвестно: read live policy: api: Get "http://127.0.0.1:8081/api/v1/policy": dial tcp 127.0.0.1:8081: connect: connection refused; cli: all variants failed» with «владелец не объявляет: 0 · никто не объявляет: 19 · объявляет несколько: 0» and every one of the 19 prefixes showing «АНОНС: нет · нет маршрута» (19 «проблемных»), and the same page on the docker VM «не меняется … таблицы». Three defects behind one screen: (A) the live-policy READ had only two rungs — the API and a **docker-only** `headscale policy get` — so on a native install whose API is unreachable it gave up immediately and answered «cli: all variants failed» about a CLI it never ran, while the policy **FILE** headscale serves in `policy.mode: file` was never read (the WRITE path has used that file since B272); (B) the prefix table's advertisement map is filled from `ListAllNodes()` and the loader **discarded the error**, so an unreachable headscale made every prefix look unadvertised — absence of evidence presented as a negative fact, which no click on that page could change; (C) the unreachable API is a **configuration** class (the address must be reachable from the skygate PROCESS; inside a container `127.0.0.1:<port>` is the container's own loopback, never the host's headscale) and nothing named `HEADSCALE_URL`. Fix: `GetACL` walks **API → policy file (`c.PolicyPath` / `DiscoverPolicyPath`) → headscale CLI through the B267 install-kind ladder** (`docker exec` when docker+container exist, else the local binary, including the legacy `policy show` / `policy` variants) → an error naming **every** rung it tried plus the reachability hint (`HEADSCALE_URL`, the container-loopback trap, a ready `curl` probe), where the hint fires only for a genuine reachability failure (`connection refused` / `actively refused` / `no such host` / timeout, POSIX **and** Windows spellings) and never for a 401/500 from a reachable daemon; the prefix card now carries `LiveReadErr` + `LiveReadHint` so it declares «не удалось прочитать маршруты из headscale — «нет» здесь значит «не удалось спросить»» instead of counting 19 phantom problems (RU+EN). 20 contracts in `scripts/check_b294_headscale_read_truth.sh` + `internal/headscale/acl_read_b294_test.go`, `internal/feature/admin/exit_nodes_b294_test.go`.
- **B293.1** — (2026-09-23) **detecting a local relay must not need daemon privileges.** Live follow-up to B293: right after installing v1.5.57 the operator reported «команда ничего не дала» — the `IS this host` line never appeared and the relay stayed on SSH. Cause: B293's detection asked the live daemon (`tailscale status --json`) and treated "I could not ask" as "fall back to SSH", but the native install runs skygate as an **unprivileged service user** and tailscaled's socket is root-owned unless `--operator` was granted — so the probe failed on permissions and detection never matched. Fix: a second, privilege-free source of evidence — `LocalInterfaceIPs()` reads this machine's own addresses straight from the kernel (`net.InterfaceAddrs`, loopback/link-local excluded), because a tailnet address bound on a local interface is the same proof as the daemon's own answer; `DetectRelayPlacement()` is now the evidence chain **daemon → interfaces → not local**, where a NEGATIVE daemon answer is final (it knows its own address) and the fallback runs only when the daemon could not be asked at all, carrying `Evidence` / `MatchedIP` / `SelfIPs` / `DaemonErr` so the log names what proved it and why the daemon was unreadable; the pages use the same chain (`LocalSelfIPs`) so the «локальный узел» badge and the corrected Telegram egress advice appear for an unprivileged service user too, and the self-covering-route loop guard is fed the interface-derived list. Division of labour: **detection** needs no privileges, **applying** still needs root / `--operator` / `NOPASSWD` / the root-owned helper (B293's ladder). 56 contracts in `scripts/check_b293_local_exit_node.sh` (section I) + `TestDetectRelayPlacement_B293_1`, `TestLocalInterfaceIPs_B293_1`.
- **B293** — (2026-09-23) **an exit node that IS the skygate host is managed LOCALLY, and the transport is chosen from evidence.** Operator clarification of the B292 report: «тот exit-node что существует он расположен на той же машине что и headscale и skygate … в таком случае доступ как таковой не нужен по ssh», confirmed on the host — `tailscale status --json` reports `Self.HostName=exit-node-vps` with `100.64.0.1` + `fd7a:115c:a1e0::1`, i.e. the local tailscaled IS the relay (headscale + skygate + the exit node in one place, and `telegram.egress_node_id` pointing at it can therefore route nothing). Managing it over SSH (B292's path) meant an SSH session from the host to itself, through the very tailnet it configures: pointless (a key + `authorized_keys` on the same box) and fragile (it fails exactly when the local tailscaled is the thing needing repair). The operator also required the feature to work without privilege («бывает что ставят без [root] … чтобы не было ситуации что нет возможности настроить по причине доступа»). Fix: the decision is **address equality** between the live daemon's `Self.TailscaleIPs` and the relay's headscale addresses (a match is proof; a shared hostname is deliberately not enough — the B265.1 `skygate-host` lesson; "I could not ask" keeps SSH); a local relay is configured with a LOCAL `tailscale set` through a **privilege ladder** — `direct` (root, or the daemon's `--operator` user) → `sudo -n` → a data-only `<update_dir>/routes.request.props` consumed by the new root-owned `deploy/skygate-apply-routes.sh` (`skygate-routes.path`/`.service`, written by `install-common.sh`, re-installable via `deploy/install-routes-helper.sh`, the same split as the policy helper) → a NAMED error listing all four ways to grant access (`RoutesFallbackHint`); only a **privilege refusal** falls through (a real `tailscale set` error is surfaced as-is) and a staged-but-unconsumed request is an **error**, not success (the B288.1 lesson); the sync also refuses to advertise a subnet this host sits INSIDE — the documented "advertise your own LAN" loop, base routes exempt — and reports what it skipped; remote relays keep the unchanged SSH path; `/admin/exit-nodes` marks a local relay with a «локальный узел» badge, shows the rung the next sync will use and **suppresses** B292's missing-key warning for it (no permanent false alarm); and `/admin/telegram` stops folding the two co-location shapes together — a relay that IS this host cannot be an egress detour (its way out is skygate's own), while the `api.telegram.org` probe becomes **representative** instead of untrustworthy. 48 contracts in `scripts/check_b293_local_exit_node.sh` + `internal/headscale/local_node_b293.go`/`local_apply_b293.go` and `internal/headscale/local_node_b293_test.go`, `local_apply_b293_test.go`, `internal/feature/admin/exit_nodes_b293_test.go`.
- **B292** — (2026-09-23) **the exit-node SSH sync must name its blocker instead of echoing `ssh`.** Operator report from `/admin/exit-nodes` («он даёт ошибку при пересинхронизации»), in the GREEN flash box: `Sync exit-node-vps: ssh=err=ssh exit-node-vps (key /ssh-sync/id_ed25519): Warning: Identity file /ssh-sync/id_ed25519 not accessible: No such file or directory. ssh: Could not resolve hostname exit-node-vps: Name or service not known approved=21` — on a page whose only relay showed «1/1 здоровых», «2 маршрутов» and «mismatch: have 2, want 19». Three defects in that line: (A) `/ssh-sync/id_ed25519` is the **container** default (docker-compose binds the operator's `~/.ssh` there) and cannot exist on a native install, and the `ssh` warning was the entire explanation; (B) `Could not resolve hostname exit-node-vps` means the SSH target was the bare node NAME — `exit_servers` had neither `ssh_target` nor `tailscale_ip`, the discovery pass writes `INSERT OR IGNORE` (so an empty column stays empty forever), while the page displayed the relay's Tailscale IP (`100.64.0.1`) read from headscale, and the «Use Tailscale IP» button resolves through the same empty chain — there was **no in-UI way out**; (C) the per-row Re-sync handler always redirected with `?ok=`, so a failed SSH sync rendered as success. Fix: new `internal/headscale/ssh_key.go` (`SSHKeyProblem` / `SSHKeyFixHint` / `SSHKeyState` / `SSHKeyStateNeedsOperator`) preflights empty / non-absolute (POSIX-aware, since `filepath.IsAbs` calls the container default relative on Windows) / not-found / directory / unreadable and explains the container default, and `SetAdvertisedRoutes` refuses **before** spawning `ssh`, appending the target's origin (`targetSource`) so a DNS failure is no longer the whole story; the SSH key default is now resolved **per install kind** (`resolveExitSSHKeyPath`: container → `/ssh-sync/id_ed25519` unchanged, native → `<data dir>/ssh/id_ed25519`, the same `nativeDataDir` anchor B270 used for the OIDC keys) and the installers create `<data_dir>/ssh` (`0700`, service-owned); `syncOneExitNode` resolves the relay from the **live headscale view** when the row has no address, persists it through `db.SetExitServerTailscaleIPIfEmpty` (only into an EMPTY column) so the B81 chain and the button work afterwards, and logs the case where headscale has no address either (`db.FirstTailscaleIP` is the one address-picking rule: IPv4 wins); the per-row Re-sync flash is derived from the result (`exitSyncFailed`: `ssh=err=` / `approve=err=` / `error=` → `err=`, while `ssh=ok approved=0` stays success — it is the normal state of an unapproved relay); and `/admin/exit-nodes` shows the effective key path, a per-row badge whose tooltip carries the reason **and** the fix, plus a banner naming both fields to set (RU+EN). 40 contracts in `scripts/check_b292_exit_ssh_truth.sh` + `internal/headscale/ssh_key_b292_test.go`, `internal/feature/exit_rules/sync_b292_test.go`, `internal/db/exit_servers_b292_test.go`, `internal/feature/admin/exit_nodes_b292_test.go`. **Contract renegotiation:** `scripts/check_b266_exit_node_register.sh` E3 (the absoluteness check moved into the shared preflight) and `TestConfigSSHKeyPath_DefaultChangedForDocker` → `TestConfigSSHKeyPath_DefaultIsPerInstallKind`.
- **B291** — (2026-09-23) **the cluster/HA tree must work on SQLite (it was PostgreSQL-only, therefore dead on a native install).** Live on the native `aro` host: `cluster.discovery.error cluster_node:workpc error="insert discovered node: SQL logic error: near \"['skygate-standby']\": syntax error"` **every 5 minutes** (137 rows ≈ 11 h), and `/admin/cluster` + `/admin/ha` rendered empty. Every write path was PostgreSQL-shaped — `ARRAY['skygate-standby']::text[]`, `'[]'::jsonb`, `'node-' || substr(md5(random()::text), 1, 12)`, `NOW()`, `SELECT … FOR UPDATE`, `'skygate' = ANY (roles)`, `roles || ARRAY['skygate']::text[]`, `array_remove(roles, 'skygate')`, `array_to_string(roles, ',')`, `$N::jsonb` — so `cluster_node` never received a row, the elector could not transition a node or recommend a failover, and `skygate cluster failover` could not promote anything. New `internal/db/cluster_sql_b291.go` owns the dialect spellings (`NowExpr`, `NowMinusExpr`, `CastJSON`, `JSONField`, `CastTextArray`, `RandomHexExpr`, `ForUpdateExpr` — PostgreSQL only, SQLite would answer `near "FOR": syntax error` — and `TimeValue`, which binds RFC3339 on SQLite instead of Go's `time.Time.String()` form that `sql.NullTime` refuses and `ParseDBTime` could not decode before B291); role membership and role surgery (promote/demote/drill/CLI) happen in **Go** with exact matching (`RolesContain`/`RolesAdd`/`RolesRemove`, never `LIKE '%skygate%'`, which also matches `skygate-standby`); every timestamp is decoded through `db.ParseDBTime` on read **and** bound through `DialectKind.TimeValue` on write, so all three shapes SQLite can hold render instead of dropping the row; the pending-invite expiry filter and the elector's 5-minute `failover_recommend` dedup moved from SQL into Go (no `NOW()`/`INTERVAL`, no `jsonb ->>`); and a bug that was broken on **both** backends is fixed — the `/admin/ha` event union read `unix_timestamp` and `actor` from `audit_log`, columns that exist in **neither** schema, so half that history was silently missing on PostgreSQL too. 50 contracts in `scripts/check_b291_cluster_sqlite.sh` + `internal/cluster/cluster_sqlite_b291_test.go`, `internal/elector/elector_sqlite_b291_test.go`, `internal/feature/admin/cluster_ha_sqlite_b291_test.go` (all running the real entry points against a migrated in-memory SQLite database).

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







