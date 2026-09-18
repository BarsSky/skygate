# Skygate roadmap

**Single planning document.** What is being worked on now, what is next, what is
blocked, and the technical-debt register. It replaces the pre-v1.6 set
(`docs/PLANS.md`, `docs/plans/**`, `docs/BACKLOG.md`) which was removed in the
2026-09-18 documentation restructure — the full text of every removed plan stays
in git history.

**Last updated:** 2026-09-18 (v1.5.9 cycle — native self-update, OpenRC, docs restructure)

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
* Review after every release: move shipped items into the "shipped" section in the
  same commit as the release bookkeeping, and re-tag anything the operator
  re-prioritises.
* A new item is added here **and** (if it becomes real work) as a B-block entry in
  `AGENTS.md` plus a `scripts/check_bNNN_*.sh` contract registered in
  `scripts/verify_pre_deploy.sh`.
* Anything not planned here and not in the index is either shipped (git history) or
  deliberately dropped (see §7).

---

## 2. Current status

| | |
|---|---|
| Version line | **v1.5.9** (native self-update + OpenRC + entry-point fixes) |
| Reference host | production deployment healthy; `/readyz` reports db / headscale / headplane / tailscale ok |
| Verify gate | `scripts/verify_pre_deploy.sh` → `0 FAIL` (live-state checks report `SKIP`, not `FAIL`) |
| Distribution | docker image `ghcr.io/barssky/skygate` (**linux/amd64 only**), Go tarballs linux/darwin × amd64/arm64, windows-amd64 zip, `SHA256SUMS` attached from v1.5.9 on |
| Docs | bilingual `INSTALL` / `UPDATE` / `ROADMAP` (RU + EN) + flat topical catalogue |
| Open operator decisions | the HA question list (see §5) and the Telegram DPI workaround |

---

## 3. Shipped in the v1.5.9 cycle

One line each; the reasoning and the failure modes are in `docs/LESSONS.md`, the
contracts are in the corresponding `scripts/check_b*.sh`.

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
| **Docs** | This file + `INSTALL`/`UPDATE` (RU+EN) + the flat catalogue; `docs/plans/**`, `docs/runbooks/**`, `docs/internal/**`, `docs/BACKLOG.md`, `docs/PLANS.md` removed. |

---

## 4. Next up (v1.6.0 candidates, rough priority)

1. **Release-pipeline verification (RR-1)** — assert in CI that every release has a
   `SHA256SUMS` **file** that lists every uploaded asset, and that the ghcr tag is
   lowercase. Closes the class of bugs that produced B262 and B237.24.
2. **Fix the in-app manual steps (RR-2)** — `internal/update/manual.go` still
   prints the historical asset names `skygate-linux-amd64` / `.sha256`, which the
   pipeline does not publish; point it at
   `skygate-<TAG>-<arch>.tar.gz` + `SHA256SUMS` (a contract currently pins the
   stale string, so the test changes with the code).
3. **Applier hardening (RR-3)** — `systemctl reset-failed <service>` before a
   restart (observed during the live rollback drill: a crashed unit refuses to
   start again and the helper reports a spurious failure).
4. **Skill/check hygiene (RR-4)** — the B-check catalogue is large and partly
   redundant; consolidate duplicates and keep the `SKIP`-not-`FAIL` rule for
   anything that needs live state (docker, a DB, the VM).
5. **SQLite ↔ PostgreSQL parity (TD-17)** — keep both migration chains and both
   dialect helpers in lockstep; add a parity check that fails when a schema change
   lands in only one chain (the `execSQLiteDDL` chokepoint work of the v1.5.9 cycle
   is the foundation).
6. **Backup polish (TD-4 + TD-12)** — S3 destination in `/admin/backup/config`, and
   the weekly auto-verify drill (restore the newest backup to a temp dir, run an
   integrity check, alert on failure).

---

## 5. Blocked / needs operator action

| Ref | Item | Blocked on |
|---|---|---|
| **BL-2** | HA Tier 1 (active/passive with a priority chain, Patroni + etcd failover, DNS failover, certsync via S3) | External DNS-provider credentials, and answers to the **open HA questions** list. Topology, second host, etcd and the S3 bucket are already in place; see [`docs/ha.md`](ha.md) |
| **BL-3** | Telegram bot behind a DPI-blocked network (`api.telegram.org` times out) | Operator decision: route the bot through an exit node without DPI, or tunnel it |
| **TD-5 / RR-5** | Per-user `exitnode.<user>.<domain>` DNS records | headscale 0.30+ (`dns.extra_records`); 0.29.x rejects the policy |
| **RR-6** | Compliance-tier per-user headscale plane migration (move a user's nodes + ACL off the global plane, flip the DB override) | A real operator need; infrastructure exists, no data migration yet |
| **RR-7** | Public release of v1.5.9 | Operator decision — run the clean-host install → update acceptance first, then tag |
| **RR-8** | `node_owner_map`: 4 stale rows (B243) | Operator decision on relink-vs-delete (see below) |
| **RR-9** | ACL drifted from the DB: orphan `tagOwners` entry + a missing per-CIDR `via` pin (B188.2/B188.3/B-mod-tag-owners-coverage) | One ACL reapply from the DB (`/admin/acls`) |
| **RR-10** | Telegram relay probe unreachable from the check environment (B185 `[O]`) | An active relay/exit-node route, or accept as environmental |
| **RR-11** | Flaky live/whole-package contracts (B183 `[I]`, B237.2) | Gate hardening: SKIP when the live probe is unavailable (RR-4) |

### 5.1 Live-state contract failures on the reference host (2026-09-18)

The full gate run on the reference host ends with **PASS=352 / 5 live checks failing / 3 SKIP**.
None of the five reads a documentation file — every one of them queries the live DB,
the headscale policy or the network, and their scripts are **byte-identical** to the
pre-restructure commit. Evidence collected on the host:

* **`node_owner_map` (B243).** Live headscale users are `1 skyadmin`, `8 michail`,
  `11 guest`, `12 daniil`, `85 infra`. Four rows point elsewhere:
  `node_id 6/29/31 → headscale_user_id=6 (michail)` — `michail` now lives at **8**, so
  these are relinkable; and `node_id 45 → 2147455555 (tagged-devices)`, the
  int-overflow sentinel, which is pure residue.
  → `UPDATE node_owner_map SET headscale_user_id=8 WHERE headscale_user_id=6;` and
  `DELETE FROM node_owner_map WHERE headscale_user_id=2147455555;`
  (the hourly reconciler, B237.18, is supposed to relink by username — worth checking
  `SKYGATE_RECONCILE_HEADSCALE_USERS_ENABLED` and its audit rows).
* **ACL drift (B188.2/B188.3/B-mod-tag-owners-coverage).** The DB is correct:
  `device_exit_node_prefs` has `(6, basic, tag:dev-infra-emilia, via_enabled=1)` and
  `device_rules` has the rule (`id 189985`, `youtube.com`, device 29, exit `emilia`),
  but the live headscale policy has neither the `via=[emilia]` pin on that `/32` nor a
  matching `node_owner_map`/`device_rules` row for its `tagOwners` entry
  `tag:dev-skyadmin-emilia`. One ACL regeneration from the DB should clear all three
  contracts at once.
* **Telegram relay (B185).** `[O] expected=ok_relay got=probe_unreachable_B185_not_live`
  — the probe cannot reach the relay from the check environment.
* **`device_rules` "duplicates" (B183 `[J]`, informational).** `exit_node=emilia` has
  106 rows, 46 distinct 5-tuples and **106 distinct 6-tuples**: the extra rows differ by
  `parent_domain` (`cdn:cloudflare:discordapp.com` vs `…discord.gg` vs … all resolving to
  `188.114.96.0/20`). That is the *current* 6-column design (B232 + B237.23), so a
  5-tuple-uniqueness expectation is unreachable on this host; `[J]` already SKIPs and the
  check reports the numbers.
* **Flakiness.** `check_b183.sh` (11/11) and `check_b237_2.sh` (21/21) both pass when run
  standalone seconds after failing inside the full gate; B237.2's live probe needs UDP to
  `1.1.1.1:53`, which timed out during the run. These belong to the RR-4 hardening item
  (SKIP-not-FAIL for unavailable live state).

---

## 6. Feature roadmap (rough order)

* **v1.6.0** — RR-1…RR-4 (release verification, manual-steps fix, applier hardening,
  check hygiene) + TD-4 S3 backup destination.
* **v1.6.x** — TD-17 parity contract; backup auto-verify drill; UI information
  density on `/admin/devices`; inline confirmation modals.
* **v1.7.0** — module system completion (`B-mod-*`): Tailscale-as-module and the
  remaining sub-features, building on the module core (`internal/module/`).
* **v1.8.0** — HA residuals that depend on operator choices (DNS failover provider
  hardening, auto-reclaim policy, cluster management UI polish).
* **Later** — RR-5 when headscale 0.30 is available; RR-6 if a compliance need
  lands; ARM docker images (revisit the amd64-only decision, see `AGENTS.md`).

---

## 7. Technical-debt register

| Ref | Item | Status |
|---|---|---|
| TD-1 | Admin UI grouped into 6 collapsible sidebar sections | DONE (v1.1.0, B96) |
| TD-2 | Replace numeric HTTP status codes with `http.StatusXxx` | DONE (v1.2.0; stragglers fixed in B237.16) |
| TD-3 | Mobile-responsive UI (drawer < 768px, 44 px tap targets) | DONE (v1.1.0, B97) |
| TD-4 | S3 backup destination | OPEN (RR-2 candidate) |
| TD-5 | Per-user DNS records | BLOCKED on headscale 0.30 |
| TD-6 | `/admin/exit-nodes` per-row `accept_routes` toggle | DONE (v1.4.0, B140) |
| TD-7 | `/admin/users` headscale-orphan "add as skygate user" | DONE (v1.4.0, B141) |
| TD-8 | System-test run persistence + history tab | DONE (v1.4.4) |
| TD-9 | Subnet-router smoke-mesh cleanup cron | **DONE** (v1.4.3, B237.17) |
| TD-10 | `portal_users.headscale_user_id` reconciliation | **DONE** (B237.18) |
| TD-11 | UI-only CDN grouping for exit rules (Approach G) | DONE (B237.22) |
| TD-13 | Test-helper stubs (~2.8k lines) not exercised by any test | OPEN (low ROI; backfill as features land) |
| TD-14 | Five `SA1012` staticcheck false positives in test files | CLOSED — never materialised in the current tree (v1.2.0); five real status codes were the actual issue |
| TD-15 | Backticks inside `run_check` descriptions were executed by bash | DONE (v1.5.9 gate) |
| TD-16 / TD-18 | Missing i18n keys (`common.online`, `common.offline`, `cluster.col_actions`, `cluster.node_upgrade_help`) | DONE (v1.5.9 gate) |
| TD-17 | SQLite ↔ PostgreSQL schema/DDL parity contract | OPEN (RR-1 candidate) |
| TD-19 | Native self-update needs `systemctl reset-failed` | OPEN (RR-3) |
| TD-20 | Documentation sprawl (`plans/`, `runbooks/`, `internal/`, 900 KB `AGENTS.md`) | DONE (2026-09-18 restructure) |

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
  first, leaving `:v1.5.2` without an amd64 manifest). Revisiting requires either a
  single `build-push-action` with both platforms, or two buildx jobs merged with
  `docker imagetools create`.
* Historical superpowers B-mod plans and v0.2x refactor plans — removed from the
  tree; the durable outcome is captured in `docs/internals.md` + `docs/LESSONS.md`,
  the text itself in git history.

---

## 9. Shipped history (compact)

| Release | Headline |
|---|---|
| v1.0.0 | Squashed initial release: headscale/headplane integration, Tailscale preauth flow, per-user exit nodes, ACL engine, Telegram bot, backups, audit log, SQLite + PostgreSQL parity |
| v1.1.0 | UI refactor (grouped sidebar) + mobile-responsive layout (TD-1, TD-3) |
| v1.2.0 | Style/debt cleanup (TD-2, TD-14) |
| v1.3.0–v1.3.2 | PostgreSQL cutover in three phases: Go source, docker + scripts, docs |
| v1.4.0 | `/admin/exit-nodes` accept-routes toggle + orphan adoption (TD-6, TD-7) |
| v1.4.3–v1.4.4 | Smoke-mesh cleanup cron (TD-9) + system-test history (TD-8) |
| v1.5.0–v1.5.2 | HA groundwork (cluster tables, deploy subcommands, certsync), OIDC end-to-end on a public hostname, OIDC auto-sync, login/UX fixes (B167–B178) |
| v1.5.4 | Docker image pinned to linux/amd64 (Issue #4); image-pull update button |
| v1.5.6–v1.5.8 | DERP status/probe fixes, derper-in-docker migration, Tailscale auth-key UX, device-delete with ACL regen |
| v1.5.9 | **Native self-update (B261)**, OpenRC (B262), `SHA256SUMS` fix, SQLite `sqlite:` DSN fix, autoupdater `ON CONFLICT` fix, green gate |

---

## 10. Where the detail lives

| Looking for | File |
|---|---|
| One-line description of every B-block (the full index) | [`AGENTS.md`](../AGENTS.md) |
| What broke, why, and the guard that prevents it | [`docs/LESSONS.md`](LESSONS.md) |
| Package map, invariants, the check-contract system | [`docs/internals.md`](internals.md) |
| Release, deploy, PG cutover, host bootstrap | [`docs/operations.md`](operations.md) |
| HA topology, failover, open questions | [`docs/ha.md`](ha.md) |
| Install / update procedures (operator-facing) | [`docs/INSTALL.md`](INSTALL.md), [`docs/UPDATE.md`](UPDATE.md) |
| The removed planning archives in full | git history — `git log --diff-filter=D --name-only -- docs/plans docs/runbooks docs/internal docs/BACKLOG.md docs/PLANS.md` finds the restructure commit |
