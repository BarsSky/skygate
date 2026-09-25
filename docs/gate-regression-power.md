# The guarantee catalog's regression-detection power (B322 audit)

**Date:** 2026-09-25 · **Trigger:** operator report — «необходимо провести ревизию всех
тестов B1-320 на тему того помогают ли они определить регрессию … после некоторых правок
что было раньше по сессии правила перестали работать но предварительные прогоны не помогли
определить что они сломались тоже и с тегами было и политикой ACL. Правки что вносились
ломали рабочее состояние и тесты что должны были это отловить ничего не показывали.»

This document is the audit record and the backlog it produced. It is **not** a claim that
the catalog is now complete — it says exactly which classes were repaired, which were
proven harmless, and which runtime invariants still have only a grep behind them.

## 1. Method

Five read-only passes over `scripts/verify_pre_deploy.sh` and every `scripts/check_b*.sh`
(≈351 `run_check` entries, 270 scripts, 2.5 MB), one per ID range:

| Slice | Entries | Report |
|---|---|---|
| B1–B100 | 98 | [`gate-audit-b001-b100.md`](gate-audit-b001-b100.md) |
| B101–B199 | 98 | [`gate-audit-b101-b199.md`](gate-audit-b101-b199.md) |
| B200–B262 | 68 | [`gate-audit-b200-b262.md`](gate-audit-b200-b262.md) |
| B263–B290 | 30 | [`gate-audit-b263-b290.md`](gate-audit-b263-b290.md) |
| B291–B320 | 30 | [`gate-audit-b291-b320.md`](gate-audit-b291-b320.md) |

Each entry was classified by **style** — `behavioural` (executes the product and asserts an
outcome), `structural` (asserts source text / symbol presence / file existence), `live-only`
(needs docker/PG/headscale and SKIPs elsewhere) — and every assertion that cannot fail for
the reason it claims was listed as a *dead contract*.

The aggregate finding: **only a small minority of the catalog executes the product.** The
largest slice is source-text greps. That is not automatically wrong (a grep is the cheapest
way to pin a wiring decision), but it means a change that keeps every symbol in place and
breaks the behaviour is invisible — which is exactly what the operator hit.

## 2. The four mechanical classes that were repaired (B322)

These are the reason a green gate did not mean a working product. All four are now
contracts of their own in `scripts/check_b322_gate_can_fail.sh`.

1. **A masked exit status made 24 entries unconditionally green.** B59–B81, B86, B89 and
   B90 were written as
   `f=/tmp/bNN.sh; printf "%s" "<chain>" > "$f" && bash "$f"; rm -f "$f"`
   — the pipeline's status was `rm`'s, which is always 0, and the gate treats rc==0 as PASS.
   Arming them exposed **21 genuinely failing contracts** (section 3). The wrapper is now
   `… && bash "$f"; rc=$?; rm -f "$f"; exit $rc`.
2. **A check that counts failures but never exits non-zero is decorative.**
   `scripts/check_b251.sh` printed `FAIL` lines and exited 0; the gate hides a check's own
   output on PASS, so its 13 contracts — including the reserved `skygate-host` logic — were
   never seen. **33 further scripts had the same defect** and now carry a non-zero exit on a
   recorded failure.
3. **`go test -run <PATTERN>` filters pointing at deleted tests always pass.**
   `go test` answers `ok … [no tests to run]` and exits 0. Live examples: B5
   (`TestMigrateV047`), B21 (`TestShouldInclude`), `check_b272_tag_drift.sh` (`B227`),
   `check_b203.sh` (`TestDBSwap`), `check_b222.sh` (`pollOnce`), and band B's
   `TestAutoBackfill_*` / `TestBackfill_StrategyD_*` / `TestListLastRunWithResults_*`, which
   were deleted with the v1.3.0 SQLite→PG test purge and replaced by `t.Skip` stubs. The
   catalog now also has a contract that **no filter may match zero tests**.
4. **A check script referenced by no catalog never runs.** 21 scripts were in that state
   (Telegram async page, device adoption, the Tailscale enable/disable UI, derper-in-docker
   packaging, the live OIDC probe, the DERP route script, the prefix-admin scripts, …).
   Registration status is now itself a contract.

Additionally the gate no longer hides the evidence: on FAIL it prints the check's own
`FAIL`/`SKY-FAIL`/`TIMEOUT` lines *before* the 20-line window that used to cut them off
(`verify_pre_deploy.sh`, `run_check`). The audit found that the late behavioural sections —
B315 G7/H1/I2, B317 I2/I3, B273 F–J, B276 D–F, B288 E–H, B289 F, B290 E–G — were exactly
the ones falling outside that window.

## 3. Real regressions the armed band immediately found

Renegotiating the 21 entries was not cosmetic: two of them were guarding a genuine,
user-visible defect that the masked contract had hidden.

* `internal/mesh/mesh.go` — `INSERT OR IGNORE INTO mesh_members` with no dialect branch.
  PostgreSQL answers `ERROR: syntax error at or near "OR"` (verified on PG 15), so on a PG
  install — including the reference deployment — **joining a mesh failed outright**
  (`POST /my/meshes` join, and the bot's mesh join).
* `internal/subnet/shares.go` — the same SQLite-only clause for `user_subnet_shares`;
  **granting a subnet share failed** on PG (`/admin/user-subnet`, the bot's share command).

Both now branch on the backend like `internal/db/migration_tracking.go`
(`INSERT OR IGNORE` for SQLite, `ON CONFLICT … DO NOTHING` for PostgreSQL). This is the
dialect-leak class of `docs/LESSONS.md` L-15 and the `skygate-db-dialect-audit` skill; the
`B60` sweep that used to guard it was one of the 24 masked entries.

## 4. The renegotiated band (B59–B90)

Every one of the 24 entries was rewritten to assert the **current** implementation, verified
three ways (probe file with `set -e`, the production `printf|bash` wrapper, and a
byte-for-byte printf round-trip), then re-verified through the gate's own `run_check` in
`tmp/band_harness.sh`. Verdicts: **23 of 24 PASS through the real gate path**; B61 is a
live-state check whose payload runs `scripts/verify_post_deploy.sh` (VM-only) and is
reported below.

Notable renegotiations:

* **B64** dropped two comment-only greps (`augmentedTagsByUser`) and now asserts the real
  constructs plus `TestB288_EveryRecordedDevTagIsDeclared`.
* **B66** dropped `use-preferred-btn`: B277.3 replaced that JS button with
  `POST /my/exit-rules/apply-preferred`, so the old contract demanded a construct whose
  removal was itself a fix.
* **B69**'s chain was **invalid bash** (unquoted parentheses), so it had never run a single
  assertion.
* **B68a** re-pointed `migrateV052` → `migrateV061PG`/`migrateV061SQLite` (the v0.52 file
  was purged and its stub named a function that does not exist).
* **B89** re-asserted Strategy D against `db.TagNamesUser`/`TestTagNamesUserB287`, and
  dropped a `SKYGATE_NODE_DISCOVERY_INTERVAL` assertion that was a copy-paste error in the
  contract itself.
* **B90** now asserts the re-bind ORDER behaviourally (awk line-number comparison) instead
  of grepping a version string in a comment.
* **B61** turned out to be broken by the same class of mistake as the others: its first
  statement contained a bare `<VM_HOST>`, which bash parsed as a **redirection**, so the
  probe never assigned anything. The replacement compares the `-x` trace for a positional
  `SSH_HOST` and the `--bad-flag` exit code.

Honest gaps left in this band: **B76** now has no behavioural test at all (its
`TestNormalizeUpdateTarget_*` unit tests are `t.Skip` stubs; the guard is pinned
structurally), and B77/B78 lost their unit tests the same way. Writing those Go tests is
tracked as follow-up work, not silently dropped.

## 5. Runtime invariants that still have only a grep behind them

These are the gaps that matter to the operator's report. Each needs an **outcome** contract
(a behavioural assertion against a real database / a live control plane), not another
source pin. Prioritised by what the operator actually hit:

1. **The preferred-exit reconciler computes state on PostgreSQL.** B319's own check proves
   the *query text*, and its only live half SKIPs unless a specific docker container named
   `skygate-pg-local` exists (hard-coded `admin`/`skygate_staging`), so on a SQLite-only
   machine — and on any host whose PG is not that container — the regression is invisible.
   No contract asserts that a reconcile tick completes without SQL errors.
2. **A device tag in `node_owner_map` actually reaches headscale.** B272's live section
   SKIPs; `check_b_node_owner_map_orphans.sh` selects the tag and then ignores it. Nothing
   compares the database with the tailnet.
3. **The applied ACL policy equals the generated one.** `check_b276_acl_ownership_sync.sh`
   contract E2 **SKIPs precisely when it finds drift** and defaults to the passing value;
   `check_b288`'s ACL state is grep-only. A stale policy is invisible.
4. **A prefix-owner change actually reaches the relays.** Only a stub-headscale Go test
   behind a `-run` filter that matches nothing.
5. **A policy write that is accepted but never lands.** `deploy/skygate-apply-policy.sh` is
   never executed by any contract.
6. **The Tailscale self-name / autostart behaviour** (B320/B321) is asserted structurally —
   the live outcome ("the client is up and called `skygate-host` after a recreate") is not.

Follow-up blocks: **B323** closes 1–5 with a live-outcome contract set that SKIPs cleanly
when its dependencies are absent; **B324** covers the remaining ~200 individual weak
assertions catalogued in the five slice reports (comment-satisfied greps, always-true
conjuncts, `grep -c … || echo 0` producing `0\n0`, `ok` in both branches of an `if`, and the
i18n "RU+EN" rows that compare counts instead of keys — one script scores a perfect RU/EN
parity while the RU value is English).

## 6. How to use this

* `bash scripts/check_b322_gate_can_fail.sh` — the meta-contract set; run it whenever you
  add a check, and the four classes above cannot come back unnoticed.
* `bash bash tmp/band_harness.sh` (regenerate with `python tmp/b322_band_harness.py`) —
  run one band through the gate's own `run_check` without waiting for the whole catalog.
* Any single FAIL: re-run that check standalone before treating it as a regression
  (AGENTS trap #9), and read the FAIL lines the gate now prints first.
