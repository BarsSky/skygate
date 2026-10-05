# Skygate roadmap

**Single planning document.** What is being worked on now, what is next, what is
blocked, and the technical-debt register. It replaces the pre-v1.6 set
(`docs/PLANS.md`, `docs/plans/**`, `docs/BACKLOG.md`) which was removed in the
2026-09-18 documentation restructure — the full text of every removed plan stays
in git history.

**Last updated:** 2026-10-01 (v1.5.94 cycle — task caps the operator can change,
a gate that can actually fail, localization driven to zero, and the database
management tab).

> **This file was 85 releases behind.** Until 2026-10-01 it still described the
> **v1.5.9** cycle (`Version line: v1.5.9`, "Last updated 2026-09-18"), so every
> reader was told the project stood a year of work behind where it is. The
> per-release detail it was missing lives in [`RELEASE-NOTES.md`](../RELEASE-NOTES.md)
> (one canonical section per tag) and in the block index in [`AGENTS.md`](../AGENTS.md).
> Keep this file current at release time — the §3 table and the TD register are the
> two things that rot fastest.

Related: [`AGENTS.md`](../AGENTS.md) (conventions + the compact block index),
[`docs/LESSONS.md`](LESSONS.md) (what went wrong and why),
[`docs/internals.md`](internals.md) (code map), [`docs/operations.md`](operations.md)
(release + deploy procedures).

---

## 1. How to work with this file

* Items carry a tag: `TD-n` (technical debt), `BL-n` (blocked), `RR-n` (roadmap
  item). Feature work additionally carries its **B-block ID** (`B262`, `B-mod-core`,
  …) — the authoritative one-line description of each block lives in the block
  index inside `AGENTS.md`, and the durable lessons live in `LESSONS.md`.
* Review after every release: move shipped items into the §3 table in the
  same commit as the release bookkeeping, and re-tag anything the operator
  re-prioritises.
* A new item is added here **and** (if it becomes real work) as a B-block entry in
  `AGENTS.md` plus a `scripts/check_bNNN_*.sh` contract registered in
  `scripts/verify_pre_deploy.sh`.
* Anything not planned here and not in the index is either shipped (git history) or
  deliberately dropped (see §8).
* When this file and the code disagree, **the code wins** — and the fix is to
  correct this file in the same change, not to leave the contradiction for the
  next reader.

---

## 2. Current status

| | |
|---|---|
| Version line | **v1.5.94** (B328 — rule caps count what they name and are operator-editable) |
| Reference host | production deployment healthy; `/readyz` reports db / headscale / headplane / tailscale |
| Verify gate | `scripts/verify_pre_deploy.sh` runs **381 contracts** (`run_check` entries, counted 2026-10-01); live-state checks report `SKIP`, not `FAIL` (rule 1). The catalog **exits 0 even with FAILs locally** — see TD-22 |
| Static analysis | `go vet` and `staticcheck` clean; **`gofmt` is NOT clean repo-wide** (302 of 835 tracked files) and nothing enforces it in the gate — see TD-21 |
| Distribution | docker image `ghcr.io/barssky/skygate` (**linux/amd64 only**), Go tarballs linux/darwin × amd64/arm64, windows-amd64 zip, `SHA256SUMS` attached |
| Databases | **SQLite and PostgreSQL are both first-class.** Schema parity is now *measured*, not asserted: 45 tables / 404 columns identical, both chains end at V77 (`internal/db/schema_parity_pg_test.go`, TD-17) |
| Docs | flat catalogue (`docs/*.md` + `docs/ru/`); four documents are maintained bilingually — `README`, `INSTALL`, `UPDATE`, `ROADMAP` |
| Open operator decisions | the HA question list (§5), the Telegram DPI workaround, and the rotation call on the historical admin password |

---

## 3. Shipped since the v1.5.9 cycle (v1.5.10 → v1.5.94)

The v1.5.9 section that used to live here described 11 blocks; **85 releases have
landed since**. It is kept verbatim as §3.1 — the blocks it names are still what
`git log v1.5.8..v1.5.9` will show you, and two contracts (`check_b237_23.sh`
G.2, `check_b237_24.sh` C.1) legitimately assert that this file records them.
One line per theme for everything after it, with the version range and the
governing blocks; the reasoning and the failure modes are in
[`RELEASE-NOTES.md`](../RELEASE-NOTES.md) (one `## vX.Y.Z` section per tag) and the
contracts are in the corresponding `scripts/check_b*.sh`.

### 3.1 The v1.5.9 cycle

| Block | What shipped |
|---|---|
| **B261** | Native self-update (systemd / OpenRC / bare) via a root-owned privileged helper: data-only request file → path/service unit → applier that backs up, downloads, verifies SHA256, migrates **before** the swap, swaps atomically, restarts, polls `/healthz` for the target **build string**, and rolls back on failure. Trust boundary preserved (no root skygate). |
| **B261.1** | `SKYGATE_DB=sqlite:/path` never opened — the installer's own DSN form made every native SQLite install dead on arrival. The `sqlite:` scheme is now stripped in `openSQLite` + a test that performs a real file open. |
| **B261.2** | Release-integrity fallback: when a release has no `SHA256SUMS` asset, the applier verifies against the GitHub Releases API per-asset `digest: sha256:<hex>`; with neither source it fails closed. |
| **B261.3** | `mktemp -d` (0700) blocked `migrate-only` running as the service user → work dir is now `0755` (contents are SHA256-verified). |
| **B261.4** | `--migrate-only` is not a flag — the subcommand is `migrate-only`; fixed in the applier and in the in-app manual steps. |
| **B261.5** | `SKYGATE_UPDATE_BASE_URL` mirror support (root-owned config, https or loopback http, `SHA256SUMS` mandatory) for air-gapped hosts. |
| **B262** | OpenRC/Alpine as a first-class `InstallKind` (`/run/openrc` marker, `rc-service` restart, sudoers trigger, install kind exported from `/etc/conf.d/skygate`) **and** the missing `SHA256SUMS` release asset (the workflow downloaded the artifact into a path whose name collided with its single file, so the flatten step attached a directory → releases v1.5.6–v1.5.8 shipped no checksums). |
| **B237.23** | Device autoupdater `ON CONFLICT` drift: B232 recreated the natural-key index with 6 columns while `sync.go` still used 5, so every insert failed silently and new `/32` rules never reached the DB. Restored to 6 columns with a contract asserting the 5-column form is gone. |
| **B237.24** | Lowercase GHCR tag path: GitHub Actions parses `format()` arguments as literals, so `| lower` never applied and the mixed-case owner (`BarsSky`) broke the image push for v1.5.0/v1.5.2. The workflow pre-computes `lower_owner` in the meta step. |
| **TD-10** | `portal_users.headscale_user_id` reconciliation cron (B237.18): hourly, outcomes `ok` / `linked` / `relinked` / `orphan`, orphans are never auto-deleted. |
| **TD-11** | UI-only CDN grouping on `/my/exit-rules` + `/admin/exit-rules` (B237.22): per-CIDR rows stay individually editable; storage, migrations and the autoupdater are untouched (pinned by a "storage unchanged" contract). |
| **Gate green** | B237.20 staticcheck-clean, B237.16 numeric HTTP statuses, TD-15 backticks-in-descriptions, TD-16/TD-18 four missing i18n keys, B237.2 DNS-probe contract, B191 / B-mod-admin-user-sync now `SKIP` instead of `FAIL` when the docker daemon is absent. |
| **Clean-host acceptance** | Passed **2026-09-19** on this commit (throwaway `jrei/systemd-debian:12`: install → path unit → applier → `verdict: done`, `/healthz` build `v1.5.9+acc0001`) — record in [`operations.md`](operations.md) §1.4. This was the last gate before the tag. |
| **Release notes** | One canonical `RELEASE-NOTES.md` (newest first, v1.5.4–v1.5.8 backfilled); `release.yml` extracts the `## vX.Y.Z` section for the GitHub Release body and falls back to a generated commit list, so the empty-body bug of v1.5.8 cannot repeat. |
| **Docs** | Flat catalogue + `INSTALL`/`UPDATE`/`ROADMAP` (RU+EN); `docs/plans/**`, `docs/runbooks/**`, `docs/internal/**`, `docs/BACKLOG.md`, `docs/PLANS.md` removed. |

### 3.2 Everything after it (v1.5.10 → v1.5.94)

| Theme | Versions | What changed |
|---|---|---|
| **Tags actually reach headscale** | v1.5.13–v1.5.17, v1.5.35 | B272.x: `policy.mode: file` support, REST tag writes instead of `docker exec`, the drift reconciler that walks `node_owner_map`, `ensureTagIsPermitted`, the policy permission audit, the root policy applier, the offline-host mirror. B272.7: one `tagOwners` write per pass |
| **Exit nodes tell the truth** | v1.5.18, v1.5.57–v1.5.59, v1.5.65–v1.5.68 | B273 (one "is this an exit node" predicate + a ladder ordered by what blocks egress), B293.x (a relay that IS the skygate host is managed locally), B294 (reading headscale without docker), B300/B301 (one transport decision; a sudo refusal that cannot succeed falls through), B303 (an ownerless device has an admin path) |
| **Prefix ownership** | v1.5.19–v1.5.27, v1.5.39–v1.5.43, v1.5.94 | B274/B274.1/B274.2 (one advertising relay per prefix), B275.x (skygate decides the owner; the operator can pin it), B276 (the ACL follows the assignment table), B276.1/B276.2, B277.x, B328 (the caps count what they name and are editable) |
| **DERP / relay status** | v1.5.61–v1.5.62, v1.5.67, v1.5.72, v1.5.74, v1.5.80, v1.5.82 | B289.x (dial the ADDRESS, speak the HOSTNAME), B296 (the probe address is panel-set and applies at once), B302, B307 (the STUN false negative was ours), B309 (a relay skygate cannot configure stops owning prefixes), B315 (real metrics instead of invented zeros), B317 (the local relay is recommended again) |
| **OIDC from the panel** | v1.5.44, v1.5.54, v1.5.69, v1.5.78, v1.5.89 | B-oidc-setup (enable from the UI), B290, B304 (fully controllable from the panel), B313 (applied from the panel, not copy-pasted), B324 (the disabled banner says *what value* to set) |
| **Cluster / HA reads SQLite** | v1.5.55, v1.5.77 | B291 (the whole cluster/HA tree branches on the dialect), B312 (an exit node shows where it sits; a lost relay's prefixes go to the nearest one) |
| **One monitoring inbox** | v1.5.70 | B305 — every skygate signal (exit-node transitions, tag-reconcile failures, failing system tests, degraded DB) lands in `monitor_events` with severity, fingerprint dedup, ack and resolve, and a page that shows the current state |
| **Panel UX** | v1.5.79, v1.5.83–v1.5.86, v1.5.88–v1.5.91 | B314 (the sidebar grouped the way the operator asked), B318 (two pages, one daemon, one story), B320/B321/B321.1 (the reserved tailnet name belongs to the live client), B323 (the SERVICE CONTROL block: restart / recreate with the install kind detected), B326/B326.1 (a wide table must not be clipped on a phone; every select labelled), B329 (the database tab and the backend conversion — **see §3.3**) |
| **Localization** | v1.5.90, v1.5.92 | B325 froze a ratchet (RU values without Cyrillic; hardcoded English nodes) and B325.1 drove **both to zero**, closed the two holes a metric sweep opens (an empty translation is not an ASCII one; a restored test that never runs is not coverage) and restored 21 vacuous `t.Skip` test bodies |
| **The gate itself** | v1.5.46, v1.5.76, v1.5.87, v1.5.93 | B280/B281 (green CI means 0 FAIL), B299 (the catalog cannot hang), **B322** (four mechanical classes that made contracts decorative: masked exit statuses, checks that print FAIL and exit 0, `-run` filters matching deleted tests, checks referenced by no catalog), B327 (a live-state check must SKIP, never redden the commit) |
| **Boot and update reliability** | v1.5.10–v1.5.12 | B268 (the applier explains a failed swap), B269 (**bind the socket first** — an active unit with nothing listening was invisible), B270 (a broken OIDC key store no longer kills the boot; the key dir is never relative) |
| **Rules and devices** | v1.5.81, v1.5.83–v1.5.84 | B316 (a device mesh follows the device, not a username headscale rewrote), B319 (the rules lookup runs on PostgreSQL) |

### 3.3 Unreleased — in the working tree on 2026-10-01

Not tagged yet; listed here so the next release bookkeeping has something to move.

| Block | What it is |
|---|---|
| **B329** | **The database tab.** `/admin/database` was reachable only from a button on `/admin/cluster`, could not name the backend (no template contained the word `sqlite`), and was *broken rather than unhelpful* on a SQLite install: it parsed `SKYGATE_DB` with a `postgres://`-only parser and probed it with `pgx`. It now shows the live backend from `db.ActiveDialect()`, the file and its size on SQLite, the version and table count read through the live connection, and a warning when the env DSN disagrees. It has a navigation entry, and `POST /admin/database/convert` moves the whole dataset to the **other backend type**, refusing a same-kind or already-populated target before opening anything. 48 contracts in `scripts/check_b329_db_backend_ui.sh` |
| **Conversion engine** | `internal/db/convert.go` rewritten. It used to refuse a PostgreSQL source outright (*"only SQLite source supported in v1.5.4"*), so the direction an operator actually needs — PostgreSQL → SQLite — could not run. The target schema now comes from the **target's own migration chain** (indexes, triggers and partial UNIQUE indexes included) instead of six regex substitutions over the source's DDL; tables are filled parents-first from the target's own FOREIGN KEY metadata by a real topological sort; values are coerced to the target column's declared type; the copy runs in **one transaction**; row counts are read back and compared (the old doc comment *claimed* this and the code did `_ = n`); and the PostgreSQL identity sequences are advanced past the copied ids. The CLI also accepts the `--from=<dsn>` form its own help documents |
| **TD-17 closed (measured)** | `internal/db/schema_parity_pg_test.go` compares the two chains on **real databases**: 45 tables / 404 columns, identical, both heads at V77. Two `TEXT[]` columns in the SQLite chain are a documented exception with their compensating code (`rolesContainLiteral`, `parsePGTextArray`), and a *new* array column in the SQLite chain fails the contract |
| **PostgreSQL-side tests** | `internal/db/convert_cross_pg_test.go` — both directions, additive drift, dry run, against a real server; SKIP without `SKYGATE_TEST_PG_DSN`. Verified on PostgreSQL **15** (a local container) and **18.4** (the reference VM, where the full `./internal/db/...` suite is green in 166 s) |
| **Test-infrastructure fix** | `db.OpenTestPG` named each test's schema after the test and only dropped it on a graceful exit, so an interrupted run left the schema and its rows behind and the next run failed with `duplicate key value violates unique constraint "portal_users_username_key"` — measured: 13 stale schemas produced four "failures" in `display_prefs_b136_test.go` that had nothing to do with the code. The helper now drops before it creates |
| **`deploy/pg-ha/check_pg_health.sh`** | documented in `docs/ha.md` six times and in `deploy/pg-ha/README.md`, but **not in git** — the unanchored `.gitignore` rule `check_*.sh` swallowed it, so a fresh clone carried the instruction and no script. Now tracked (LF, executable) and guarded by `check_b152.sh` contract D |
| **Docs** | `docs/operations.md` §14 (recipes recovered from 21 one-off scripts that were deleted) and §15 (running the gate with PostgreSQL coverage, including the `CREATE SCHEMA` permission contract); `docs/backup-restore-and-migration.md` §7 (changing the backend type) |
| **B330** | **The documentation must agree with the tree.** `docs/ROADMAP.md` was 85 releases behind, the RU mirror had diverged, three relative links were broken and the catalogue omitted two documents. Fixed, and now *measured*: 15 contracts in `scripts/check_b330_docs_consistency.sh` check the links, the RU/EN structure, that both roadmaps name the newest release heading (**the drift detector**), and that the catalogue names every `docs/*.md` |
| **B331** | **The smoke-mesh cleanup was silently dead on SQLite** (TD-23). `ANY($1::bigint[])` + a PostgreSQL array literal → `SQL logic error: near "[]": syntax error (1)`, and only from the moment there was cruft to remove. Now a portable `IN (…)` list through `db.PlaceholdersList`/`PlaceholderAt`, a real SQLite round-trip test, and a class guard: no live `ANY($…)` in any tracked non-test Go file. **The old contract had to be renegotiated** — `check_b143.sh` contract A *required* `int64ArrayToPGArray` and the `ANY($1::bigint[])` literal, i.e. it was pinning the bug |
| **B350 (v1.5.99)** | **The client must resolve through the tailnet — and the panel must not talk it out of that.** Live report: YouTube was unreachable on one device while the *same* rule set carried another; every server-side fact was green (10 per-CIDR grants `via=[tag:dev-infra-emilia]`, `prefix_owner` 10/10, 73 of 73 approved routes including all ten youtube prefixes) and the device answered `curl https://www.youtube.com` → `Could not resolve host`. The missing half was the client preference `--accept-dns=false`: skygate grants access by IP **prefix**, so a rule is reachable only once the device resolves the domain *into* that prefix, and a filtering ISP resolver answers `NXDOMAIN` (L-58). The product itself had taught the wrong command in `/my/exit-rules`, its help page, the preauth key page, the device help, the Telegram add-device instructions, both i18n catalogues and `docs/windows-client.md` — and the **B49 contract enforced that spelling** with a negative lookahead. All of them now say `--accept-routes --accept-dns=true`, the registration commands that carried no flags at all were fixed (a device could not even accept a route), the server-side installers keep `--accept-dns=false` (a relay is not a client), and two new warning surfaces explain symptom → check → fix (`tailscale debug prefs` → `CorpDNS`, `tailscale set --accept-dns=true`, and the DNS-free proof `curl --resolve`). **What cannot be detected:** headscale carries no client DNS preference and `host_info.Services` is not a proxy — `peerapi-dns-proxy` was advertised by all 12 nodes, the broken one included. 14 contracts in `scripts/check_b350_client_dns_truth.sh`; the B337 ratchet was paid (266 → 265) in its own commit |
| **B350.1 (open)** | **Tailnet DNS must be carried by design, not by accident.** `8.8.8.0/24` + `8.8.4.0/24` are advertised by the relay (so `--accept-dns=true` leaves the filtered network), but they are advertised only because a **user rule** happens to claim `8.8.8.8` — delete that rule and every device silently falls back to its ISP resolver. `dns.nameservers.global` is still `[1.1.1.1, 8.8.8.8]` and **no node advertises `1.1.1.1`** (measured: `/32` and `/24` → nobody), so half the queries leave locally. Planned: advertise the configured resolver prefixes as a system-owned prefix (independent of user rules), keep `nameservers.global` to what the tailnet actually routes, and surface a warning when it is not |
| **B325 blind spot (open, found by B350)** | The B325 contract B1 counts RU values with `"key"[[:space:]]*:` **on one line**, so any catalogue whose hand alignment puts a space *before* the colon is invisible to it — that was `internal/i18n/catalog_bot.go` until B350's gofmt payment, which is exactly why five untranslated RU values survived a budget of 0 (they are translated now, from the recommendations already recorded in the i18n audit). `internal/i18n/catalog_user_subnet.go` still has that alignment and is still gofmt-frozen, so its values are still uncounted. Fix the matcher and re-measure before trusting the budget again. The same payment also surfaced a **false positive** in `check_b325_1_i18n_sweep.sh` B1: `bot.__catalog_parity_marker__` is empty **on purpose** (it proves `TestCatalogsParity` compares key sets), so B1 now excludes that **key** and the new contract B2 asserts it is still declared twice (RU + EN) and still empty — the exclusion cannot hide a real blank |
| **B351 (v1.5.100)** | **The admin page measured the PAGE, and the auto-updater wrote rows without their owner.** Operator report with a screenshot: «правил 7 страниц у админа, и при этом на каждой по разному … нет ли обновлятора сервиса что переписывает в неправильном ключе». Measured live (PostgreSQL, 349 enabled rows): the page is `ORDER BY r.id LIMIT 50 OFFSET n` while the CDN auto-updater inserts rows for every device on every tick, so one `(user, device)` group is scattered across the table — and the groups *and* their counters were built from that slice (the same user read «19 правил / 3% (19/500)» on page 6 and «49 правил / 9% (49/500)» on page 7, while the heading said «ВСЕ ПРАВИЛА (50)» next to a pagination line saying «всего 349 правил»). And `device_rules.user_name` was empty on **220 of 349** rows — including **all 68** of `basic` — because it is filled only by the one-time V0.44 migration: the runtime backfill that exists for `device_hostname` has no twin and the auto-updater's two raw INSERTs listed neither column, so `DeviceRuleCountsForAdmin` (B348) listed one device twice. `basic`'s **access was never affected** (all 68 rules named `karolina`, the `prefix_owner`, with 188/188 routes approved and grants `via=[tag:dev-infra-karolina]`) — the bookkeeping was what lied. Fix: the INSERTs write the pair (from `portal_users` + `node_owner_map`, one lookup per unique pair per pass), the tick **heals** existing rows via `db.BackfillDeviceRuleUserNames`, the inventory resolves the owner by `user_id`, and every counter is the unpaginated database number with «показано N из M». 12 contracts in `scripts/check_b351_rule_owner_and_counts.sh` |
| **`devices` table is stale (observation from B351)** | The recon found no rows in `devices` for the live devices (9/29/56) — the portal resolves device ownership from `node_owner_map` (headscale) plus `device_rules.device_hostname`, and `devices` is a legacy table nothing updates. Not a defect today (no page under discussion reads it), but it is a trap for the next reader: **`node_owner_map` is the source of truth for `device_id → (username, hostname, tag)`.** Either drop the table or document it in `docs/internals.md` |

---

## 4. Next up (v1.6.0 candidates, rough priority)

1. **Make the gate's verdict real (TD-22).** `verify_pre_deploy.sh` maintained
   `RESULTS_PASS`/`RESULTS_FAIL` and **never read them**: there was no summary and
   no `exit`, so the script's status was its last statement's — and the measured
   baseline (368 PASS, 10 FAIL, 1 SKIP, **exit code 0**) proves it, because
   `.githooks/pre-push` branches on that status and therefore blocked nothing.
   **(b) is DONE (B335):** the catalog prints `PASS / FAIL / TIMEOUT / SKIP /
   checks / sub-contract SKIP rows`, counts a TIMEOUT as a failure, lists the
   failing check names, and ends on the verdict's own `exit 0` / `exit 1`;
   `scripts/check_b335_gate_verdict.sh` extracts that block and executes it, so
   the contract cannot drift from the code. **(a) is the remaining work:**
   reclassify the five live-state contracts — `B118`, `B119`, `B188.2`, `B189`,
   `B190` — which measure the reference VM's accumulated debris (stale
   `svyatoslava-legacy` / `b188_*` fixture rows, `derp_health` absent from the
   live DB, 84 `skyworker` grants still pinned to `emilia`) rather than the tree,
   and which are also **non-deterministic** (two runs of the same catalog gave
   368/10 and 362/9). `B188.3` only fails because it inherits `B188.2`.
   **(c)** then assert the exit code in `scripts/check_b322_gate_can_fail.sh`,
   **(d)** then simplify the pre-push hook to a status check. `B1`, `B176`,
   `B237.16` and `B294` were in the baseline list only because they run
   `go test` and inherited the two non-hermetic `internal/headscale` tests that
   **B332** fixed.
2. **`gofmt` ratchet, then the renormalisation (TD-21).** Land the ratchet first
   (the 302-file legacy set frozen as an allow-list, exactly like the B325 i18n
   budgets, so the number can only fall), then `gofmt -w` over the tracked files as
   **its own single-purpose release**: 38 737 whitespace-only lines would otherwise
   bury a real change and poison `git blame`.
3. **PostgreSQL coverage in the gate (TD-24).** CI's `test-pg` job is scoped to
   `./internal/db/...`, so the live-PG tests in the two other packages that call
   `db.OpenTestPG` — `internal/feature/admin` (18 call sites) and `internal/acl`
   (4) — always skip; and `verify_pre_deploy.sh` exports no `SKYGATE_TEST_PG_DSN`,
   so the VM gate reports PASS while testing SQLite only. Measured 2026-10-01:
   CI's job now runs `go test ./...`, and the gate prints its PostgreSQL coverage
   on every run instead of leaving it implicit. Remaining: run the full-tree
   PG-enabled gate on the reference VM and record the result. Recipe and the
   `CREATE SCHEMA` permission contract (including the reserved `pg_` schema
   prefix that made the old probe fail): `docs/operations.md` §15.
4. **Applier hardening (RR-3).** `systemctl reset-failed <service>` before the
   restart (observed live: a crashed unit refuses to start again and the privileged
   helper reports a spurious failure).
5. **Fix the in-app manual steps (RR-2).** `internal/update/manual.go` prints the
   historical asset names `skygate-linux-amd64` / `.sha256`, which the pipeline does
   not publish; point it at `skygate-<TAG>-<arch>.tar.gz` + `SHA256SUMS` (a contract
   currently pins the stale string, so the test changes with the code).
6. **Widen the dialect leak guard (TD-27, PARTIAL).**
   `internal/db/dialect_leak_guard_test.go` still guards exactly two forbidden
   tokens and skips every `internal/db/migrations_*` file. B331 added a real guard
   for the one class the guard missed (a live `ANY($…)` in a tracked non-test Go
   file), but the Go-side guard itself has to grow the token set and stop skipping
   the migration chains.

**Landed since this list was written (2026-10-01):** TD-23 — the SQLite dialect leak
in `internal/mesh/cleanup.go` (B331); TD-25 and TD-26 — the migration-chain audit
covered only PostgreSQL and the V070 registry label disagreed between the chains
(B333); RR-14 — the `docs/ru/` mirror had drifted from the English original (B330),
which is now a standing contract rather than a one-off fix.

---

## 5. Blocked / needs operator action

| Ref | Item | Blocked on |
|---|---|---|
| **BL-2** | HA Tier 1 (active/passive with a priority chain, Patroni + etcd failover, DNS failover, certsync via S3) | External DNS-provider credentials, and answers to the **open HA questions** list. Topology, second host, etcd and the S3 bucket are already in place; see [`docs/ha.md`](ha.md) |
| **BL-3** | Telegram bot behind a DPI-blocked network (`api.telegram.org` times out) | Operator decision: route the bot through an exit node without DPI, or tunnel it |
| **RR-5 / TD-5** | Per-user `exitnode.<user>.<domain>` DNS records | headscale 0.30+ (`dns.extra_records`); 0.29.x rejects the policy |
| **RR-6** | Compliance-tier per-user headscale plane migration (move a user's nodes + ACL off the global plane, flip the DB override) | A real operator need; infrastructure exists, no data migration yet |
| **RR-10 / RR-13** | Telegram egress relay enablement: the container's Tailscale client is off (`SKYGATE_TS_AUTHKEY_FILE=/dev/null`) and no node advertises the canonical Telegram CIDRs. A working path already exists — `karolina` advertises and has approved `91.108.12/16/20/56.0/22`, `149.154.160.0/20` and `185.76.151.0/24` — so switching the selector on `/admin/telegram` is the cheapest fix. Procedure: [`docs/TELEGRAM.md`](TELEGRAM.md) §8 | Operator go-ahead (preauth key + container-side client + relay route changes) |
| **RR-12** | Credential hygiene — swept 2026-09-19; 34 scripts resolve the DB password at runtime through `scripts/lib/db_credentials.sh` instead of embedding it. `check_ha_state.sh` keeps the literal **on purpose** (it is the guard that greps for a regression) | Operator call: rotate the admin password (the removed literal is still in git history) |
| **RR-15** | Onboarding a second skygate host from the panel alone | **IMPLEMENTED 2026-10-02 as B342 (option (a), decided 2026-10-01).** `POST /admin/cluster/onboard` is one admin action: it creates the `cluster_node` row, mints the signed invite (`cluster.IssueInvite`) **and** a tailnet preauth key for the `infra` user, parks the payload as JSON in `global_settings` behind an opaque 16-byte token, and `GET /admin/cluster?boot=<token>` consumes it **once** to render a five-step block — tailnet (`--netfilter-mode=nodir`), install the exact version this primary runs (SHA256 verified), `skygate join` with `--write-dsn-to`, `systemctl enable` + healthz, then Approve in the panel. The credentials are shown once, never in a URL, and the audit row carries lengths; an unrendered block is swept after 15 minutes. 25 contracts in `scripts/check_b342_cluster_onboard.sh` + 7 pure unit tests; the manual §2.4 path stays as the reference. **Remaining:** the live end-to-end run on a real second host (`svaytoslava`) — the panel side is complete, and the block is exercised by the unit tests rather than by a second VM |

### 5.1 Historical: the 2026-09-18 live-state record

The long per-item record that used to sit here (four stale `node_owner_map` rows,
the orphan `tagOwners` entry, the ACL `via` pin, the Telegram relay misdiagnosis,
the B183 duplicate-rule report and the flaky Go-load contracts) is **resolved or
superseded**: the ownership rows and the ACL drift were addressed by B316 and by
B272+B276+B288, the B183 `[J]` report is the current 6-column design, and the
flakiness was the `producer | grep -q` SIGPIPE class that B322/B327 and TD-19
closed. The original text is one `git log -p -- docs/ROADMAP.md` away, and the
durable lessons are in [`docs/LESSONS.md`](LESSONS.md).

---

## 6. Feature roadmap (rough order)

* **v1.6.0** — RR-2, RR-3 and the two gate items (TD-21, TD-22), plus TD-24
  (PostgreSQL coverage) and TD-23 (the mesh dialect leak).
* **v1.6.x** — TD-13 backfill as features land; UI information density on
  `/admin/devices`; inline confirmation modals; backup auto-verify drill.
* **v1.7.0** — module system completion (`B-mod-*`): Tailscale-as-module and the
  remaining sub-features, building on the module core (`internal/module/`).
* **v1.8.0** — HA residuals that depend on operator choices (DNS failover provider
  hardening, auto-reclaim policy, cluster management UI polish).
* **Later** — RR-5 when headscale 0.30 is available; RR-6 if a compliance need
  lands; RR-15 once the onboarding model is chosen; ARM docker images (revisit the
  amd64-only decision, see `AGENTS.md`).

---

## 7. Technical-debt register

| Ref | Item | Status |
|---|---|---|
| TD-1 | Admin UI grouped into collapsible sidebar sections | DONE (v1.1.0, B96; regrouped in B314) |
| TD-2 | Replace numeric HTTP status codes with `http.StatusXxx` | DONE (v1.2.0; stragglers B237.16) |
| TD-3 | Mobile-responsive UI (drawer < 768px, 44 px tap targets) | DONE (v1.1.0, B97; revisited by B326/B326.1) |
| TD-4 | S3 backup destination | **DONE — shipped in v1.3.8.** This row claimed OPEN for ~85 releases (`internal/feature/admin/backup_config.go`, `RELEASE-NOTES.md` v1.3.8) |
| TD-5 | Per-user DNS records | BLOCKED on headscale 0.30 |
| TD-6 | `/admin/exit-nodes` per-row `accept_routes` toggle | DONE (v1.4.0, B140) |
| TD-7 | `/admin/users` headscale-orphan "add as skygate user" | DONE (v1.4.0, B141) |
| TD-8 | System-test run persistence + history tab | DONE (v1.4.4) |
| TD-9 | Subnet-router smoke-mesh cleanup cron | DONE (v1.4.3, B237.17) — **but see TD-23: it does not work on SQLite** |
| TD-10 | `portal_users.headscale_user_id` reconciliation | DONE (B237.18) |
| TD-11 | UI-only CDN grouping for exit rules (Approach G) | DONE (B237.22) |
| TD-13 | Test-helper stubs (~2.8k lines) not exercised by any test | OPEN (low ROI; backfill as features land) |
| TD-14 | Five `SA1012` staticcheck false positives in test files | CLOSED — never materialised; five real status codes were the issue |
| TD-15 | Backticks inside `run_check` descriptions were executed by bash | DONE |
| TD-16 / TD-18 | Missing i18n keys | DONE |
| TD-17 | SQLite ↔ PostgreSQL schema/DDL parity contract | **PARTIAL — the contract now exists** (`internal/db/schema_parity_pg_test.go`: 45 tables / 404 columns compared on real databases, both chains at V77). Remaining: the two `TEXT[]` columns in the SQLite chain are a documented semantic divergence (SQLite has no array type; `DEFAULT '{}'` stores a two-character string) with compensating readers. The structurally auditable half is now closed too — B333 made the B233 shape-drift audit cover the SQLite chain instead of skipping `migrations_sqlite.go` (TD-25) and pinned the two registries against each other (TD-26). The schema contract itself only runs when `SKYGATE_TEST_PG_DSN` is set — see TD-24 |
| TD-19 | `producer \| grep -q` under `pipefail` | **CLOSED — 0 sites remain.** This row claimed "~25 sites" long after they were paid down; the class is now pinned by B322/B327 |
| TD-20 | Documentation sprawl (`plans/`, `runbooks/`, 900 KB `AGENTS.md`) | DONE (2026-09-18 restructure) |
| TD-21 | `gofmt -l` is not clean repo-wide and no gate check runs `gofmt` | **PARTIAL 2026-10-01 (B337) — the RATCHET landed.** Re-measured on the reference VM with `go1.25.4`: **847 tracked `.go` files, 276 not `gofmt`-clean (32.6 %)** — the 2026-09-25 figure (302 of 835) was stale in both directions. `scripts/gofmt_legacy_allowlist.txt` freezes those 276 paths (with the measurement and the rules in its header) and `scripts/check_b337_gofmt_ratchet.sh` enforces four things: every non-frozen tracked `.go` file must be clean; a frozen file that has **become** clean must be **removed** from the list; the list may never grow (budget asserted at 276); and it is sorted under `LC_ALL=C` and unique. It also holds the **file-level** half of rule 3 that a whole-tree ratchet cannot express — every `.go` file changed in *this* tree must be clean even if it is allow-listed — which immediately caught three files touched by the `/admin/database` work (`internal/feature/admin/database.go`, `internal/i18n/catalog_admin.go`, `internal/i18n/catalog_common.go`) that were still drifted; they are clean now. The teeth are proven by mutation: fixing one frozen file without removing it from the list fails D1, restoring it passes. **Remaining: the one-commit renormalisation** (all 276 at once, its own single-purpose release), after which the allow-list empties and the budget drops to 0. Plan in §4, item 2 |
| TD-22 | `verify_pre_deploy.sh` prints no summary and exits 0 even with FAILs | **PARTIAL 2026-10-01** — the verdict is now real (B335): the catalog prints `PASS / FAIL / TIMEOUT / SKIP / checks / sub-contract SKIP rows`, treats a TIMEOUT as a failure, lists the failing check names, and ends on the verdict's own `exit 0` / `exit 1`, so no later statement can decide the status again. `scripts/check_b335_gate_verdict.sh` extracts that very block from the catalog and executes it against synthetic counters, so the contract cannot drift from the code (clean run → 0, 10 FAILs → non-zero, TIMEOUT alone → non-zero, SKIPs alone → 0). **Baseline measured on the reference VM (v1.5.94, 2026-10-01): 368 PASS, 10 FAIL, 1 SKIP, exit code 0** — an earlier run of the same catalog gave 362/9/1, so the live-state FAILs are also non-deterministic. The 10 FAILs are: `B1` (+`B176`, `B237.16`, `B294`, which run `go test` and inherit it) — the two `internal/headscale` tests B332 made hermetic; `B188.3`, which inherits `B188.2`; and the five live-state contracts `B118`, `B119`, `B188.2`, `B189`, `B190`, which measure the reference VM's accumulated debris (stale `svyatoslava-legacy` / `b188_*` fixture rows, `derp_health` absent from the live DB) rather than the tree. Reclassifying those five is the remaining work — see §4 item 1. **RESOLVED 2026-10-01 (B336), and not by reclassification:** the five FAILs were an ARTEFACT — the checks read `SKYGATE_DB_DSN` from `.env` and ran `psql` from the HOST, while that DSN's host is the docker DNS name `skygate-pg-local` (B278 made it so on purpose; the bridge IP rotates), which resolves only inside the compose network. The psql DNS error went to `/dev/null` and the empty result was compared against 0, so the checks announced facts about a database they had never reached — `B190 A.1 found  b188_* users (expected 0)` with an EMPTY count, `B189 B.4 derp_health table NOT in live DB`. Measured through the new helper: `derp_health` IS in the live DB, `svyatoslava-legacy` refs = 0, `tag:dev-infra-*` owners = 4, `node_owner_map` = 4, `b188_*` debris = 0 — i.e. every one of those contracts already held. `scripts/lib/db_credentials.sh` gained a live-DB access layer that runs psql INSIDE the container (or a plain DSN for external PostgreSQL) and PROBES first, so an unreachable database SKIPs with the real error and `skygate_live_db_query` cannot return a zero without a connection. Two VACUOUS contracts fell out of the same pass (B119's always-true `NOT LIKE '%'`; B118's search for a literal `<placeholder>` hostname inside its SQL) and B188.2's contract X had been pointing at a dead hardcoded `172.17.0.1:5000`. Verified on the VM: B118 18/0, B119 0 fail, B189 0 fail, B190 0 fail, B188.2 X green (`rules=129 via=129 diff=0`). **The last genuine failure, `B188.2 U`, was a STALE CONTRACT, not a stale deployment** — renegotiated in the same block. It asserted `skyworker` must never carry `via=emilia`, written 2026-08-26, before B265 made the pin conditional, B274 made exactly one relay the advertiser of each prefix, and B275 moved the decision into `prefix_owner`. Measured: all **200** of skyworker's enabled subnet/ip rules declare `exit_node_id=karolina`, `prefix_owner` splits those same prefixes **77 emilia / 123 karolina** (`source=explicit`), and the live policy carries **exactly** 77 emilia / 123 karolina — the control plane agrees with the data plane, and the old assertion was measuring the pre-B275 design. U now asserts that agreement per owner (±10 for the autoupdater's churn) across all four sanctioned relays, and it can fail. `B188.2` on the VM: **23 pass / 0 fail** |
| TD-23 | **The smoke-mesh cleanup was PostgreSQL-only.** `internal/mesh/cleanup.go` deleted with `WHERE id = ANY($1::bigint[])` plus a `{1,2,3}` array literal; SQLite answers `SQL logic error: near "[]": syntax error (1)` (the B282 class). It runs from `mesh.StartCleanupScheduler` on **every** install kind, and it only reaches that statement when `Total > 0` — i.e. it failed exactly when the cleanup had work to do, so the B143 feature was silently dead on SQLite | **FIXED 2026-10-01 (B331)** — the ids are passed as N placeholders in a portable `IN (…)` list built through `db.PlaceholdersList` / `db.PlaceholderAt`, the PG array helper is deleted, and `TestDeleteSmokeMeshes_SQLite` runs the real statement against a real migrated SQLite database (verified to fail with the exact SQLite error when the old form is restored). 11 contracts in `scripts/check_b331_mesh_cleanup_dialect.sh` |
| TD-24 | **PostgreSQL coverage is CI-only and narrow.** `.github/workflows/ci.yml`'s `test-pg` job sets `SKYGATE_TEST_PG_DSN` but ran **only** `./internal/db/...`, so the live-PG tests in the two other packages that call `db.OpenTestPG` — `internal/feature/admin` (18 call sites) and `internal/acl` (4) — never ran anywhere; and `verify_pre_deploy.sh` never sets the variable, so the VM gate reports PASS while exercising SQLite alone | **PARTIAL 2026-10-01** — CI's `test-pg` job now runs `go test ./... -count=1` with the DSN (and gained the `timeout-minutes` it lacked), and `verify_pre_deploy.sh` prints `PostgreSQL test coverage: ENABLED` / `SQLite only` on every run so the gap can no longer be silent. **MEASURED 2026-10-01 — the PG half is green.** With `SKYGATE_TEST_PG_DSN` pointed at `skygate_citest` (a database the role owns, so `OpenTestPG` can `CREATE SCHEMA`), all three packages that call `OpenTestPG` pass against real PostgreSQL **with 0 SKIP rows** — i.e. the tests actually executed rather than skipping: `internal/db` ok in 210 s (this one CI did cover), `internal/acl` ok in 4.8 s and `internal/feature/admin` ok in 62.8 s — the two that ran **nowhere** before. `scripts/check_b334_pg_test_coverage.sh` reports 24 passed / 0 failed / 0 skipped with the DSN set, including its D3/D4 contracts that assert exactly that: internal/acl's and internal/feature/admin's PG tests really ran. The gate banner prints `PostgreSQL test coverage: ENABLED`. **Confirmed in a FULL gate run** (2026-10-01, the whole 386-check catalog with the DSN exported): `PASS B1 go test ./... exits 0` — the decisive line, because without the DSN that same command silently skips every PG-gated test and still reports success. Recipe: `docs/operations.md` §15. Overstated in this row before the measurement: `internal/backup` never called `OpenTestPG` (comments only), and `cmd/skygate`/`internal/headscale` likewise. Recipe and the `CREATE SCHEMA` contract (plus the reserved `pg_` schema prefix that made the documented probe schema always fail): `docs/operations.md` §15 |
| TD-25 | `internal/db/migrations_audit_b233_test.go` skips `migrations_sqlite.go`, so the shape-drift audit (the B232 class) covers only the PostgreSQL chain | **FIXED 2026-10-01 (B333)** — the audit is chain-aware: a file belongs to a chain exactly when it defines a `migrateV<NNN>PG` or `migrateV<NNN>SQLite` function (so `migrations_v0_77_exit_location.go`, which defines both, correctly lands in both chains), each chain is walked separately in version order, and the decision logic moved into the pure `shapeDriftOffenders` helper that the mutation test now drives instead of an inlined copy. The V068/B232 repair is additionally pinned on **both** backends by extracting each chain's `migrateV068` body and requiring its `DROP` to precede its `CREATE` |
| TD-26 | The V070 migration's `Name` string differs between the chains — `v0.70 (B238)` in `driver_postgres.go` vs `v0.70 (B236)` in `driver_sqlite.go`, with the same `SourceFile`. This contradicts the invariant stated in `driver_sqlite.go`'s own header ("same version numbers, **same Name strings**, same SourceFile") | **FIXED 2026-10-01 (B333)** — the SQLite label now reads `v0.70 (B238)`, and `TestMigrations_ChainsRunInLockStep` fails on any future `Name` (or `SourceFile`) drift between the two registries. The same block found and fixed a second registry defect: V060/V061/V062 recorded `SourceFile` as two `_test.go` files and a wrong per-version file while the functions live in `migrations_pg.go` — now pinned by `TestMigrations_SourceFileNamesTheDefiningFile`, which resolves each entry's `Run` function through `runtime.FuncForPC(...).FileLine(...)` |
| TD-27 | `internal/db/dialect_leak_guard_test.go` guards exactly **two** forbidden tokens and skips all `internal/db/migrations_*`. TD-23 was a live counter-example it did not catch | **PARTIAL 2026-10-01 (B331)** — the specific class has a real guard now: contract B1 of `scripts/check_b331_mesh_cleanup_dialect.sh` fails on any live `ANY($…)` in a tracked non-test Go file (comments may quote it). The Go leak-guard itself still covers two tokens and still skips the migration chains |
| TD-28 | A killed PG-enabled test run made the next one "fail". `OpenTestPG` named each schema after the test and only dropped it on a graceful exit, so an interrupted run left rows behind and seeded INSERTs failed with a duplicate-key error that looked like a regression (13 stale schemas → four false failures in `display_prefs_b136_test.go`) | **FIXED 2026-10-01** — the helper now drops before it creates; verified by killing a run on purpose and re-running the four tests green |

Module-system work is tracked under the `B-mod-*` IDs in the block index in
`AGENTS.md` — `B-mod-core`, `B-mod-tailscale`, `B-mod-install`,
`B-mod-admin-user-sync`, `B-mod-reregister`, `B-mod-telegram`, `B-mod-derp`,
`B-mod-exit`, `B-mod-cluster`, `B-mod-static-embed`, `B-mod-sqlite-pg-bidi`,
`B-mod-cleanup`, `B-mod-bcheck`. The module core shipped; the remaining
sub-features are in §6.

---

## 8. Long-tail deferred (explicitly out of scope)

These are tracked so they are not lost, **not** planned:

* `~2850` lines of `internal/feature/*/testutil.go` helpers that no test exercises
  (TD-13) — low ROI to backfill; grows naturally with new features.
* Compliance-tier per-user plane migration (RR-6).
* Re-enabling multi-arch docker images — deliberately dropped in v1.5.4 (the
  pre-v1.5.4 matrix pushed the same tag twice and the second push overwrote the
  first). Revisiting requires either a single `build-push-action` with both
  platforms, or two buildx jobs merged with `docker imagetools create`.
* Historical superpowers B-mod plans and v0.2x refactor plans — removed from the
  tree; the durable outcome is captured in `docs/internals.md` + `docs/LESSONS.md`,
  the text itself in git history.
* **The large-file refactor.** `cmd/skygate/main.go` was 4 655 lines with a
  ~3 400-line `main()`; `internal/i18n/catalog_admin.go` is 2 333;
  `internal/feature/my/devices.go` 1 596; `internal/feature/exit_rules/form_my.go`
  1 543. **`main.go` is DONE (2026-10-02, 4 655 → 2 553):** the contracts now read
  the package SURFACE (`gosurface … cmd/skygate/*.go`, 110 scripts — 655 path
  occurrences were only 196 operands, L-55/L-56), then the route table became
  `routes.go` (byte-identical move, 241 registrations) and the boot helpers became
  `main_bootstrap.go` / `main_helpers.go` / `main_subcommands.go` (contiguous
  slices, proven). B339 §E keeps it split. What remains of the large-file work:
  the two 1.5 k-line feature files above, the de-duplication of
  `acl_generate.go` / `acl_generate_via.go` (behavioural, needs its own block), and
  TD-21 (a `gofmt -w` renormalisation of the frozen 305-file set) — the old gate
  this item cited, "155 check scripts grep main.go", no longer exists.

---

## 9. Shipped history (compact)

| Release | Headline |
|---|---|
| v1.0.0 | Squashed initial release: headscale/headplane integration, Tailscale preauth flow, per-user exit nodes, ACL engine, Telegram bot, backups, audit log, SQLite + PostgreSQL parity |
| v1.1.0 | UI refactor (grouped sidebar) + mobile-responsive layout (TD-1, TD-3) |
| v1.2.0 | Style/debt cleanup (TD-2, TD-14) |
| v1.3.0–v1.3.2 | PostgreSQL cutover in three phases: Go source, docker + scripts, docs |
| v1.3.8 | S3 / S3-compatible backup destination (TD-4) |
| v1.4.0 | `/admin/exit-nodes` accept-routes toggle + orphan adoption (TD-6, TD-7) |
| v1.4.3–v1.4.4 | Smoke-mesh cleanup cron (TD-9) + system-test history (TD-8) |
| v1.5.0–v1.5.2 | HA groundwork (cluster tables, deploy subcommands, certsync), OIDC end-to-end, login/UX fixes (B167–B178) |
| v1.5.4 | Docker image pinned to linux/amd64 (Issue #4); SQLite restored alongside PostgreSQL; the `db-migrate` conversion subcommand |
| v1.5.6–v1.5.8 | DERP status/probe fixes, derper-in-docker migration, Tailscale auth-key UX, device-delete with ACL regen |
| v1.5.9 | Native self-update (B261), OpenRC (B262), `SHA256SUMS` fix, SQLite `sqlite:` DSN fix, autoupdater `ON CONFLICT` fix |
| v1.5.10–v1.5.17 | Tag/ACL truth (B272.x), boot reliability (B268–B270), route approval without docker (B267) |
| v1.5.18–v1.5.36 | Exit-node health truth (B273), prefix ownership (B274–B277), cluster/HA on SQLite (B291) |
| v1.5.37–v1.5.59 | Exit-rule model rework, OIDC auto-setup, the operator surfaces for prefix assignment and OIDC (B276.x–B294) |
| v1.5.60–v1.5.86 | DERP/relay truth (B296–B317), one monitoring inbox (B305), sidebar regrouping (B314), the reserved tailnet name (B320/B321), OIDC panels (B304/B313) |
| v1.5.87–v1.5.94 | **The gate (B322, B327), localization to zero (B325/B325.1), responsive forms (B326.x), service control (B323), rule caps (B328)** |

---

## 10. Where the detail lives

| Looking for | File |
|---|---|
| One-line description of every B-block (the full index) | [`AGENTS.md`](../AGENTS.md) |
| What shipped in a release, with root cause and measurement | [`RELEASE-NOTES.md`](../RELEASE-NOTES.md) |
| What broke, why, and the guard that prevents it | [`docs/LESSONS.md`](LESSONS.md) |
| Package map, invariants, the check-contract system | [`docs/internals.md`](internals.md) |
| Release, deploy, PG cutover, host bootstrap, the gate-with-PostgreSQL recipe | [`docs/operations.md`](operations.md) |
| HA topology, failover, open questions | [`docs/ha.md`](ha.md) |
| Backup / restore / changing the database backend | [`docs/backup-restore-and-migration.md`](backup-restore-and-migration.md) |
| Install / update procedures (operator-facing) | [`docs/INSTALL.md`](INSTALL.md), [`docs/UPDATE.md`](UPDATE.md) |
| How much the catalog can and cannot catch | [`docs/gate-regression-power.md`](gate-regression-power.md) |
| The removed planning archives in full | git history — `git log --diff-filter=D --name-only -- docs/plans docs/runbooks docs/internal docs/BACKLOG.md docs/PLANS.md` finds the restructure commit |
