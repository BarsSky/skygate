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
| **Clean-host acceptance** | Passed **2026-09-19** on this commit (throwaway `jrei/systemd-debian:12`: install → path unit → applier → `verdict: done`, `/healthz` build `v1.5.9+acc0001`) — record in [`operations.md`](operations.md) §1.4. This was the last gate before the tag. |
| **Release notes** | One canonical `RELEASE-NOTES.md` (newest first, v1.5.4–v1.5.8 backfilled); `release.yml` extracts the `## vX.Y.Z` section for the GitHub Release body and falls back to a generated commit list, so the empty-body bug of v1.5.8 cannot repeat. |
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
| **RR-7** | Public release of v1.5.9 | **Ready — the clean-host acceptance passed 2026-09-19 (§3).** Only the operator's decision to tag remains |
| **RR-8** | `node_owner_map`: 4 stale rows (B243) | Operator decision on relink-vs-delete (see below) |
| **RR-9** | ACL drifted from the DB: orphan `tagOwners` entry + a missing per-CIDR `via` pin (B188.2/B188.3/B-mod-tag-owners-coverage) | One ACL reapply from the DB (`/admin/acls`) |
| **RR-10** | Telegram relay probe: the in-container Tailscale client is disabled and the canonical Telegram CIDRs are not advertised/approved on any relay | **Enablement plan (operator)** — see RR-13 |
| **RR-13** | Turn the Telegram egress relay path on: (1) create a preauth key in headscale for the `infra` user, (2) put it in `data/ts/authkey` and set `SKYGATE_TS_AUTHKEY_FILE` (or use `/admin/tailscale`'s enable flow), (3) recreate the container so the entrypoint starts `tailscaled`, (4) `/admin/telegram` → *Egress relay* → select the relay → **Apply** (SSH `tailscale set --advertise-routes=<TelegramCIDRs>`), (5) approve those routes for that node in headscale, (6) verify `ip route get 149.154.167.220` → `dev tailscale0` and the probe → `ok_relay` | Operator go-ahead (needs a preauth key + a container recreate + relay route changes) |
| **RR-11** | Flaky Go-load contracts (B183 `[I]`, B213, B235, B237.2) | **Mostly closed 2026-09-19.** `B202.5`'s rotating FAIL was a real race: `TestSSHDumpTransport_Dump_FakeSsh` asserted stderr log lines that the reader goroutine could still be holding when `cmd.Wait()` closed the pipes — both dump transports now drain to EOF before `Wait()` (`go test -count=10` green on Linux). `check_b237_24`'s live `git ls-remote` probe reported an unreachable remote as `FAIL`; it now `SKIP`s. `B213` flaked once inside a full run and passed standalone + in two later full runs — kept under observation. The `GOFLAGS=-p=2` cap and the 180 s budgets stay |
| **RR-12** | **Credential hygiene — swept 2026-09-19.** The default PostgreSQL password literal (the string `.githooks/pre-commit` blocks) no longer appears in any tracked script: 34 scripts now resolve it at runtime through `scripts/lib/db_credentials.sh` — `$SKYGATE_DB_PASSWORD` → the password inside `$SKYGATE_DB`/`$SKYGATE_DB_DSN` → the DSN in `.env` → empty (psql then fails loudly). `check_ha_state.sh` keeps the literal **on purpose**: it is the guard that greps for a regression, and the hook keeps blocking the string. The live **admin** password removed earlier is still in git history | Operator call: rotate the admin password |

### 5.1 Live-state contract failures on the reference host (2026-09-18)

The gate ends with **PASS=289 / 0 FAIL / 1 SKIP** (`B8`, a Windows-host smoke test that
runs on the VM) in the verification run 2026-09-19. The one live dependency this host cannot
satisfy is the Telegram relay probe — **BL-3** (DPI). Contract `O` of `check_b185.sh` now
prints **WARN + SKIP with the evidence** when the container's routing is healthy (contract
`N` passes: `RouteAll` works, ping through the relay succeeds) and fails only when the
routing itself regresses — the original B185 bug broke `N` as well, so the distinction is
safe. History of the live-state work:

* **`node_owner_map` (B243).** Live headscale users are `1 skyadmin`, `8 michail`,
  `11 guest`, `12 daniil`, `85 infra`. Four rows point elsewhere:
  `node_id 6/29/31 → headscale_user_id=6 (michail)` — `michail` now lives at **8**, so
  these are relinkable; and `node_id 45 → 2147455555 (tagged-devices)`, the
  int-overflow sentinel, which is pure residue.
  **Why nothing self-heals it:** the hourly reconciler (B237.18) is healthy — its audit
  row shows `ok:5, linked:0, relinked:0, orphans:0` and `portal_users.headscale_user_id`
  is correct (`michail=8`) — but it reconciles `portal_users` **only**, so
  `node_owner_map.headscale_user_id` has no reconciliation and keeps stale IDs.
  → step 5A of the repair helper, or (as a follow-up feature) extend the reconciler to
  `node_owner_map` so this class self-heals.
* **ACL drift (B188.2/B188.3/B-mod-tag-owners-coverage).** Two different causes, both now
  understood:
  * **B188.2/B188.3 — check bug, fixed in code (2026-09-18).** Contract T asserted one
    frozen resolved CIDR (`h-rule-64-233-164-91-32`, youtube's `/32` when B188.2 was
    written) carried `via=[emilia]`. Domain rules are re-resolved periodically, so after
    DNS moved the alias vanished while the behaviour was always correct: the reference host
    had 30 per-CIDR grants pinned to `tag:dev-infra-emilia` and a correctly UN-pinned
    catch-all. T now asserts the behaviour (≥1 pinned per-CIDR `h-rule-*` grant); S and W
    still pin the un-pinned catch-all. Both contracts pass (B188.2 19/0, B188.3 12/0).
  * **B-mod-tag-owners-coverage — stale policy.** The live policy's `tagOwners` still lists
    `tag:dev-skyadmin-emilia`, `tag:dev-skyadmin-skygate-host-1` and
    `tag:dev-skyadmin-svyatoslava-1` (legacy pre-B188 naming), and **no** DB row references
    them (`node_owner_map`, `device_exit_node_prefs`, `user_exit_node_prefs`,
    `device_rules` all return 0), so a regeneration from the DB drops them. The DB itself is
    correct for B188.2 (`device_exit_node_prefs (6, basic, tag:dev-infra-emilia,
    via_enabled=1)` + the `youtube.com` rule, id 189985).
    → step 5B of the repair helper (`skygate acl-apply`).
* **Telegram relay (B185) — misdiagnosed on 2026-09-19, now measured correctly.** The design
  (operator clarification) is: a **Tailscale client runs alongside skygate** and routes
  `api.telegram.org` through an exit node **where the API is reachable** — the
  `/admin/telegram` *Egress relay* selector advertises the canonical Telegram CIDRs
  (`149.154.160.0/20`, `91.108.*`, `185.76.151.0/24`) on the chosen relay and the container's
  client accepts those routes. Verified live on 2026-09-19:
  * the relays **can** reach Telegram — `emilia` (213.176.92.205) and `karolina` both answer
    `https://api.telegram.org/` with **HTTP 302**, so this is *not* an upstream/DPI dead end;
  * **the container's `tailscaled` is not running** — `SKYGATE_TS_AUTHKEY_FILE=/dev/null`
    (the documented "Tailscale off by default" opt-in state; log line
    `[init] TS_AUTHKEY_FILE not set — Tailscale skipped (non-RF mode)`), so no route can be
    used at all;
  * **no node advertises the canonical Telegram CIDRs**: `emilia` advertises a single
    `149.154.167.99/32` (approved), while the probe resolves `149.154.167.220`, so the
    request leaves via `eth0` (Docker NAT) and dies in the operator's DPI-blocked path.
  * `check_b185.sh` contract **N** used to pass in this state because `ping 8.8.8.8` succeeds
    through the Docker bridge regardless of Tailscale; N now requires `tailscale status` inside
    the container first, and **O** distinguishes "client disabled" (SKIP + the enablement path)
    from "client up but probe unreachable" (FAIL + the route checklist).
* **`device_rules` "duplicates" (B183 `[J]`, informational).** `exit_node=emilia` has
  106 rows, 46 distinct 5-tuples and **106 distinct 6-tuples**: the extra rows differ by
  `parent_domain` (`cdn:cloudflare:discordapp.com` vs `…discord.gg` vs … all resolving to
  `188.114.96.0/20`). That is the *current* 6-column design (B232 + B237.23), so a
  5-tuple-uniqueness expectation is unreachable on this host; `[J]` already SKIPs and the
  check reports the numbers.
* **Flakiness — resolved for this host.** Four contracts used to fail inside a full gate run
  and pass standalone seconds later (`check_b183` 11/11, `check_b213` 19/19,
  `check_b235` 22/22, `check_b237_2` 21/21) — all of them include a heavy Go step
  (`go build ./...` or a whole-package `go test`). Capping compile parallelism
  (`export GOFLAGS=-p=2` in `verify_pre_deploy.sh`, overridable via
  `SKYGATE_GATE_GOFLAGS`) removed the whole flaky set in the verification run. If a
  Go-dependent contract fails again on a loaded host, that cap is the first thing to
  check (RR-11 tracks the general SKIP/retry hardening).

**Repair helper.** The two data drifts above are packaged as one reviewed command:
`bash scripts/operator_repair_live_drift.sh` (dry run by default) discovers the live
headscale ID for `michail`, prints the exact relink/delete plan, backs up the live policy
(`headscale policy get`) and the `node_owner_map` rows, prints the rollback recipe, and only
mutates anything with `--apply` — after which it re-applies the ACL from the DB and re-runs
the four contracts. The dry run was verified on the reference host on 2026-09-18 (13 rows
read, relink targets `29,31,6`, sentinel `45`, 52 KB policy backup, nothing changed).

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
| v1.5.9 | **Native self-update (B261)**, OpenRC (B262), `SHA256SUMS` fix, SQLite `sqlite:` DSN fix, autoupdater `ON CONFLICT` fix, green gate, clean-host acceptance passed 2026-09-19 |

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
