# Slice B263–B290 (30 run_check entries, 30 scripts)

Static, read-only audit of every `run_check` registration in `scripts/verify_pre_deploy.sh` whose ID is
B263–B290 plus sub-IDs. Registration lines (all in `scripts/verify_pre_deploy.sh`), each of the form
`run_check "<ID>" "<desc>" 'test -f scripts/check_bNNN_*.sh && bash scripts/check_bNNN_*.sh'`; none uses
`run_check_slow`:

B264 `:4417`/`:4418` · B265 `:4404`/`:4405` · B266 `:4435`/`:4436` · B267 `:4401`/`:4402` · B268 `:4460`/`:4461` ·
B269 `:4487`/`:4488` · B270 `:4530`/`:4531` · B272 `:4564`/`:4565` · B272.3.1 `:4711`/`:4712` ·
B272.7 `:4742`/`:4743` · B273 `:4606`/`:4607` · B274 `:4650`/`:4651` · B275 `:4673`/`:4674` ·
B275.1 `:4689`/`:4690` · B276 `:4780`/`:4781` · B276.1 `:4800`/`:4801` · B278 `:4821`/`:4822` ·
B279 `:4845`/`:4846` · B279.1 `:4863`/`:4864` · B280 `:4887`/`:4888` · B281 `:4903`/`:4904` ·
B282 `:4927`/`:4928` · B283 `:4953`/`:4954` · B284 `:4973`/`:4974` · B285 `:4990`/`:4991` ·
B286 `:5004`/`:5005` · B287 `:5022`/`:5023` · B288 `:5048`/`:5049` · B289 `:5071`/`:5072` ·
B290 `:5091`/`:5092`.

Two IDs in the numeric range are **not** separate entries: **B263** rides in
`scripts/check_b261_native_self_update.sh` (sections O/P, registered under B261) and **B271** rides in
`scripts/check_b270_startup_blockers.sh` section F (`:269-304`; see the comment at
`verify_pre_deploy.sh:4511-4529`). Both are covered by their host entry, outside this slice.

Gate mechanics that shape every verdict: `run_check` decides PASS/FAIL **by exit code only**
(`verify_pre_deploy.sh:190`), captures stderr into the same stream (`:171`/`:177`/`:181`), and on failure
prints only the **first 20 lines** of the check's output (`:195`). Every script here ends with
`[ "$FAIL" -eq 0 ] || exit 1` (or that test as its last command), so a recorded FAIL normally exits non-zero —
the exceptions are named below.

## Table

| ID | script | style | catches | CANNOT catch |
|----|--------|-------|---------|--------------|
| B264 | check_b264_admin_delegation.sh | mixed (structural-dominant: 67 of 70 sites) | handler/route/template/i18n presence, `is_primary` DDL in *some* chain half, i18n parity + full `internal/db`/`internal/feature/admin` test sweep | delegation **behaviour**: no DB/live probe, no HTTP request through Promote/Demote; primary/self/last-admin refusals only grep-visible; `:177` matches a comment |
| B265 | check_b265_derp_status_truth.sh | mixed (structural-dominant) | STUN wire impl/magic cookie, `derpRelayIsOwn`, derpmap URL + dead-node guards, i18n/template, STUN+map unit tests run | any real STUN round trip or derper state; §J (`:193-219`) is an inert heredoc whose only verdict is unconditional (`:219`) |
| B266 | check_b266_exit_node_register.sh | mixed (structural-dominant) | ssh-target/node-name validators, token parking, TTL cap, absolute key path, go build/vet + focused tests | key actually minted, token consumed once, ssh argv at runtime; C2 (`:104`) can never fail; i18n count is one-file |
| B267 | check_b267_headscale_cli_mode.sh | mixed (structural-dominant) | `runHeadscaleCLI` presence, "no raw `docker exec headscale`" (negative grep), focused helpers | the docker-vs-native branch itself is never executed; A (`:38`) cannot tell REST-first from REST-last; a SKIP is reported as PASS (`:93`); D2 (`:72`) is implied by D1 |
| B268 | check_b268_applier_failure_diagnostics.sh | mixed (**behavioural-dominant**, live-gated Linux+python3) | the real applier driven against a local artifact mirror with stubbed systemctl/ss/journalctl/curl, asserting `done`/`rolled_back`/`failed` verdicts + DIAG lines; Go order tests | anything off Linux; the mirror-not-ready path (`:234-239`) **exits 0 with FAILs already recorded**; B7 (`:314`) can't detect losing the boot phase |
| B269 | check_b269_startup_truth.sh | mixed (behavioural probe + structural) | real binary built+run against an unopenable DB: `/healthz` 200, build string, `phase="db-open+migrate"`, `ready:false`, process alive; bind-before-DB order (line numbers) | the deployed systemd unit; `srv.ListenAndServe` check is receiver-specific; 5 of its contracts vanish as SKIP if go/curl are absent (no live section) |
| B270 (+B271) | check_b270_startup_blockers.sh | mixed (structural-dominant + real-binary probe) | real binary with an uncreatable OIDC key dir (`/healthz` ok, `KEY STORE UNAVAILABLE`, alive) + B271 dialect unit tests | nothing live (no docker/PG/service); A4 (`:85`) is unreachable, the installer grep (`:134`) matches comments, D2 (`:155`) is redundant OR |
| B272 | check_b272_tag_drift.sh | mixed (structural-dominant: 44 of 50) | file-mode 500 classifier, REST-first tag write, reconciler wiring, alert sink, mirror/refspec policy; Go tests incl. a real policy-file write + rollback | **tags reaching headscale**: G's drift branch SKIPs (`:247`), G2 SKIPs (`:253`), G's PASS is reachable from a failed probe (`:242`); C7 (`:163`) duplicates C2; `-run 'B227'` (`:206`) matches no test |
| B272.3.1 | check_b272_3_policy_helper.sh | mixed (structural-dominant) | helper/applier source shape (temp+rename, verbatim write, no merge, verdict file), one full nodeownership package test | any host execution of the helper; C.4 (`:68`) compares a comment to code; D.1/A.7/B.3/F.4 (`:73`/`:60`/`:63`/`:112`) grep identifiers/comments |
| B272.7 | check_b272_7_tag_owner_batch.sh | mixed (structural-dominant) | one-batch-tagOwners contract via Go tests, transient-retry predicates, single `SetPolicy` call count | the live write: F1 (`:198`) is PASS/SKIP only, F2 (`:206`) is an unconditional `ok`; D1/D4/D5 are window-greps that an inverted guard still satisfies |
| B273 | check_b273_exit_node_truth.sh | mixed (structural-dominant + **real SQLite live**) | `exit_node_health` read from the real DB and FAILed on unknown state / online+advertising-but-offline; ladder unit tests run | headscale's live node list; A.3 (`:89`) collapses to a token existence check; J.1/J.2 treat empty SQL output (error, renamed column, empty table) as PASS |
| B274 | check_b274_prefix_ownership.sh | mixed (structural + pure-function tests + live docker) | G.1 is aimed at real prefix overlap; deterministic tie-break tests run | G.1 fail-opens: no `python3` guard and no parse-success check, and the field names are inconsistent in-repo (`given_name` here vs `givenName` at `check_b272_tag_drift.sh:240`); native installs SKIP; B.4 (`:106`) is a duplicate of B.2 |
| B275 | check_b275_prefix_assignment.sh | mixed (structural-dominant) | `Assign`/`SetManual`/sticky/manual receipts, `ViaForPrefix` in the generator, engine tests | live semantics: E.1 is ok-or-SKIP (empty table = "not populated yet"), E.2 (`:88`) is tautological (`prefix` is the PK) |
| B275.1 | check_b275_1_prefix_ui.sh | **structural** | handler + route + template + docs presence, 14 i18n pairs, i18n/handlers/admin test run | the pin changing an owner or the ACL; no handler-level test, no live check; `authMW` on the route is not asserted |
| B276 | check_b276_acl_ownership_sync.sh | mixed (structural + real-SQLite behavioural + live skip-only) | ownership→ACL handoff with a migrated SQLite DB + stub headscale (asserts 1 PUT and the new `via` tag), single reconcile entry point, drift UI wiring | live drift: E2 (`:279-283`) **SKIPs** when a pin names a non-serving relay and defaults the metric to the passing value `0 0`; E3 (`:290-298`) can only SKIP because it greps a retired log prefix (`prefix-owner:` vs the real `acl-drift:` at `sync.go:284/309`) |
| B276.1 | check_b276_1_all_devices.sh | mixed (structural + real-SQLite behavioural) | propagation to a later-registered device, idempotence, marker, labels; V074 schema parity; `propagateAllDeviceRules` ordered before the churn drift check | live: F1 (`:223-224`) has no FAIL branch at all; D4 (`:169-173`) pins i18n keys no template renders; E2 can be green with all four tests skipped |
| B278 | check_b278_dsn_ip_rotates.sh | **structural** | launch-script text: container DNS name, no `.IPAddress` in the auto-detect block, `--restart=unless-stopped`, `--pg-host` escape hatch, git-tracked | the script running: E (`:121-130`) is presence-only and the bare `--pg-host <ip>` form is broken at runtime (`launch_skigate.sh:55-63`, `exit 2` on the IP token) yet reported PASS; the perl dependency is unguarded (false FAIL, not SKIP) |
| B279 | check_b279_class_tag_identity.sh | mixed (structural-dominant + Go tests) | class-tag predicate/sentinel, `Tags[0]` producers, sync cannot clobber, i18n/UI, tests | live rule rewriting and the tag actually written; E2 (`:187`) is comment-satisfiable; G1 (`:219`) is presence-only for a "validates against LIVE exit nodes" claim; L2 (`:296`) fails only if *both* packages fail |
| B279.1 | check_b279_1_tag_to_hostname_one_copy.sh | mixed (structural-dominant + Go tests) | one tag→hostname implementation, guard-before-loop ordering, sentinel anti-drift, 3 test files | F4 (`:199`) is **unfalsifiable** — `internal/feature/admin` matches no test, so `ok … [no tests to run]` masks acl+db failures; E1b (`:168`) is comment-satisfiable; E1 detects one syntactic shape of `TrimPrefix` |
| B280 | check_b280_ci_gates_release.sh | mixed (**behavioural core**, hermetic) | `scripts/ci_gate.sh` verdicts 0/1/2 against a stubbed `gh` (8 cases), workflow wiring, real-git ancestry in a temp repo, hook wiring | GitHub's real CI verdict (the stub is the contract); B4 (`:102`) awk window never ends at the next job; A3 (`:76`) needles live in the doc block; F1 (`:251`) hook needles live in comments |
| B281 | check_b281_ci_catalog_truth.sh | **structural** (pure grep/ordering, self-declared) | catalog job budget, staticcheck install/pin, `timeout` wiring, no hardcoded go path, no `go test \| grep -q` | whether CI behaved: `:141` counts a `FAIL\|TIMEOUT` **pattern** that the workflow's own `grep -qE '^  (FAIL\|TIMEOUT)  '` line satisfies, so deleting `ci.yml:130 exit 1` stays green; `count()` turns grep errors into 0 (fail-open for every negative contract) |
| B282 | check_b282_admin_reads_dialect.sh | mixed (structural-dominant + real-SQLite tests) | one dialect cast/parse helper, no inline `::text`/`to_timestamp` in the named readers, `PolicyJSON` normaliser, real SQLite read tests | the SQL actually executing on PostgreSQL; B2 (`:92-93`) shape tokens are all present in a doc comment, so deleting the `case` arms keeps it green |
| B283 | check_b283_policy_write_safety.sh | mixed (structural-dominant + real-temp-dir tests) | atomic handoff strings, `UndeclaredTags` guard, generator declares owner tags, applier validation/rollback strings; `RequestPolicyApply` atomicity test | the applier/headscale running (E greps strings an inverted rollback also contains); A2's negative grep is one spelling; F2/F3 accept `ok … [no tests to run]` |
| B284 | check_b284_device_tag_source.sh | mixed (structural + tests) | `deviceTagForRule` reads `o.Tag` (awk-scoped), two user-name synthesis spellings absent, tests incl. the synthetic-owner shape | the **legacy** generator still mints `tag:dev-` from `userName` at `internal/acl/acl.go:718` (no test asserts `UndeclaredTags` there); B2/C3 are a single phrasing / a comment |
| B285 | check_b285_acl_tags_declared.sh | mixed (structural + tests) | grant-tag sweep presence, `UndeclaredTags(gen)` asserted in the test, full `internal/acl` package | the sweep working on live data; A2's OR is satisfied outside the sweep (`acl.go:2103`), A3 greps a banner comment |
| B286 | check_b286_template_index_bounds.sh | mixed (structural-dominant) | guarded index forms, templates parse + handlers package, git-tracked | a render-time panic: nothing renders a page with an empty slice (admitted at `:107-113`); A1's third pattern cannot match the documented broken line (`:57`) and the loop fail-opens if the dir is missing (`:58`) |
| B287 | check_b287_tag_ownership.sh | mixed (structural-dominant + DB tests) | helper definitions, both `/my/devices` ownership paths wired, ghost guard, real-SQLite tag tests | live attribution; A3 (`:74`) greps the keyword `ESCAPE`, C4 (`:128`) is a negative substring match without rc, B3 (`:96`) is a file-wide token count |
| B288 | check_b288_policy_drift_truth.sh | mixed (structural-dominant: 29 of 30) | set-based `PolicyEquivalent`, one owner derivation, self-heal wiring, applier verdict plumbing; 13 Go tests incl. "stable table still heals drift" (real SQLite + stub headscale) | **no live section at all** (only `command -v go`, `:240`): the served policy is never compared with the generated one on a host; the applier is never executed; several contracts are comment/ghost/over-broad (`:138`, `:164`, `:181`, `:190`, `:207`) |
| B289 | check_b289_derp_map_truth.sh | mixed (structural-dominant; strongest e2e fixture in the slice) | address/SNI split, candidate order, map-status rendering; `derp_reach_b289_test.go:266` drives the real handler over migrated SQLite against an **SNI-strict** TLS listener and asserts region 900 is published | the live derpmap/headscale map; F3 (`:189`) fail-opens on a missing directory and matches only the column-0 declaration; C3 (`:129`) is an i18n miscount |
| B290 | check_b290_oidc_ui_enablement.sh | mixed (structural-dominant + admin tests) | UI-vs-env precedence, `enc:v1:` storage, live-provider reporting, apply-without-restart (captured applier), 6 tests | the running provider being reconfigured (wiring is grep-only, `:91`); B6's negative branch is one English phrasing; B5 (`:101`) is template-only; "secret never echoed" is not asserted despite the header's contract C |

**Style counts: structural 3 (B275.1, B278, B281) · mixed 27 · behavioural 0 · live-only 0.**
No entry is behavioural outright. The behavioural centres of gravity are B268 §B (real applier,
live-gated), B269 §E / B270 §E (real binary probes), B280 §D/E (real `ci_gate.sh` against a stub `gh`
and a real temp git repo) and Go-test halves that use a real migrated SQLite DB plus a stub/fixture HTTP
server (B273, B276, B276.1, B282-B290) — never a real headscale.

## Dead or near-dead contracts

78 numbered findings covering ~110 contract sites (DEAD = can never fail, or cannot fail for the property
its message claims; NEAR-DEAD = fails only for the wrong reason / silently green on the regression it exists
for). Eight findings are on the four unregistered scripts, listed last.

### 1. Can never fail at all

1. `check_b268_applier_failure_diagnostics.sh:239` — **DEAD, and it defeats the gate**: the
   mirror-not-ready branch prints the summary and `exit 0`, bypassing `:328 [ "$FAIL" -eq 0 ] || exit 1`.
   Any §A/§C FAIL recorded earlier is reported to the gate as PASS (the gate keys on `rc`,
   `verify_pre_deploy.sh:190`). *Revive:* `[ "$FAIL" -eq 0 ] || exit 1; exit 0`.
2. `check_b265_derp_status_truth.sh:219` — `ok "J: live-state note printed (run manually after deploy)"`.
   §J (`:193-219`) is `cat <<'NOTE'`; the psql query is a comment inside it (`:211`). *Revive:* implement the
   `derp_health` query behind a `command -v psql`/docker guard with SKIP, or demote `:219` to `echo`.
3. `check_b266_exit_node_register.sh:104` — `... && ! grep -q '--authkey=' "$REG"` makes `bad` unreachable:
   the builder necessarily contains `--authkey=` (`internal/feature/admin/exit_node_register.go:113`), so C2
   PASSes even if a key *were* interpolated into a redirect. *Revive:* assert the redirect shape
   (`http.Redirect(` + `QueryEscape(token)`), not the absence of a builder string.
4. `check_b267_headscale_cli_mode.sh:93` — `ok "F: go test skipped (no go in PATH)"`: a SKIP counted as PASS
   (the script has no `skip()` counter). *Revive:* add a SKIP counter.
5. `check_b270_startup_blockers.sh:85-88` — A4: `if grep -q 'return nil, err' "$OIDC_SVC"; then bad …; else ok …`.
   `internal/oidc/service.go` contains **no** `return nil, err`, so `bad` is unreachable and any other fatal
   spelling passes. *Revive:* assert the property (no `log.Fatalf`/`os.Exit` on the key-store path).
6. `check_b273_exit_node_truth.sh:89-90` — `if grep -q 'IsExitNode:      hasExitNodeTag' … || grep -q 'IsExitNode:' …; then`:
   the second alternative is a substring of the first, so A.3 reduces to "the token exists" and stays green if
   the field becomes `IsExitNode: false,` (`internal/headscale/nodes.go:135`). *Revive:* delete the alternative.
7. `check_b274_prefix_ownership.sh:106-107` — B.4's first grep is byte-identical to B.2 (`:96`) and that literal
   occurs at `internal/feature/exit_rules/sync.go:402` (SyncAdvertisedRoutes) **and** `:511`
   (SyncAdvertisedRoutesForNode), so deleting the per-node call keeps B.4 green. *Revive:* scope it with
   `grep -A60 'func (s \*Service) SyncAdvertisedRoutesForNode' "$SYNC" | …`.
8. `check_b275_prefix_assignment.sh:88-89` — E.2 (`SELECT COUNT(*) … GROUP BY prefix HAVING COUNT(*)>1`) is
   tautological: `prefix` is the PRIMARY KEY (`internal/db/migrations_v0_73_prefix_owner.go:38`), so the only
   FAIL path is a psql error. *Revive:* assert a real invariant (`exit_node_id <> '' AND source IN (…)`) or
   diff `prefix_owner` against the `device_rules` claims.
9. `check_b276_1_all_devices.sh:223-224` — F1 `if [ -n "${N:-}" ]; then ok "F1: $N row(s) … (0 is normal …)"`:
   a `COUNT(*)` result is non-empty even when `0`; F has no `bad` branch at all. *Revive:* compare against the
   marked groups, or `bad` when a marked group has no row for a device in `node_owner_map`.
10. `check_b272_7_tag_owner_batch.sh:205-206` — F2 computes `TAGGED="$(grep -c 'tag:dev-' "$POLICY" …)"` and then
    `ok "… lists ${TAGGED:-0} tag:dev- entr(y/ies) — compare against node_owner_map rows"`: it prints a number
    and asks a human to compare; it PASSes for the exact live symptom (1 entry while three tags were needed).
    *Revive:* derive the expected count from `node_owner_map` and `bad` on mismatch.
11. `check_b272_3_policy_helper.sh:68` — C.4 compares the first **textual** hit of `ReasonPolicyWriteRefused`
    (the doc comment at `internal/nodeownership/auto_alert.go:131`; the const is at `:141`) with the first
    `Contains(s, "InvalidArgument")` (`:199`). It asserts declaration order and stays green if the policy check
    is moved **below** the gRPC checks — the exact regression it names. *Revive:* extract
    `func ClassifyFailure` and compare positions inside the body, or add a classifier Go test.
12. `check_b276_acl_ownership_sync.sh:279-283` — E2 calls `skip` (never `bad`) when `MIS` pins name a relay that
    does not serve the prefix, and its `${DRIFT:-0 0}` fallback is the **passing** value, so a missing
    `python3` (never checked here) or a parser exception prints PASS. The documented live failure
    ("70 of 177 grants named a non-primary relay") is reported as SKIP. *Revive:* `bad` on `MIS>0`, default to
    a failing sentinel, and SKIP only when the DB/headscale read genuinely failed.
13. `check_b276_acl_ownership_sync.sh:290-298` — E3 greps `prefix-owner: ACL re-applied…|prefix-owner: .* live policy already matches`,
    but the real lines are `acl-drift: …` (`internal/feature/exit_rules/sync.go:284` and `:309`; `prefix-owner`
    is only the actor argument at `:191`), so E3 can **only** SKIP. It is also ok-or-skip. *Revive:* match
    `acl-drift: ACL re-applied` and turn "no such line in 24h while `prefix_owner.updated_at` moved" into `bad`.
14. `check_b275_prefix_assignment.sh:86-91` — E.1 is ok-or-SKIP: `${rows} -ge 1` else `skip "live prefix_owner not
    populated yet"`. An empty table (advertising falls back, nothing pinned) is a SKIP, and a schema error reads
    as "not populated yet". *Revive:* `bad` when the table is empty while rules with `exit_node_id` exist.
15. `check_b272_tag_drift.sh:249-253` — G2's negative branch is `skip`, never `bad`, so losing the metric is
    invisible; and `check_b272_tag_drift.sh:242-243` prints `ok "G: every headscale node carries a per-device tag"`
    when `rows` is empty — which is also what a failed/absent `python3` (`:231`, unguarded) produces.
    *Revive:* capture the JSON, `[ -n "$JSON" ] || bad`, then compare; and `curl -fsS … || bad` for the metric.
16. `check_b281_ci_catalog_truth.sh:141` — `check_ge "D-fail-grep" 1 "$(job_count 'FAIL\|TIMEOUT')"` is satisfied by
    the workflow's own refusal line (`ci.yml:127-129` contains the literal `FAIL|TIMEOUT`), so deleting
    `ci.yml:130 exit 1` — which is what makes green mean "0 FAIL" — leaves B281 fully green.
    *Revive:* add `check_ge "D4-exit1" 1 "$(job_count 'exit 1')"`.

### 2. Empty output / absent input is the PASS value

17. `check_b273_exit_node_truth.sh:291-296` (J.1) — `bad_states="$(sqlite3 … 2>/dev/null)"; if [ -z "$bad_states" ]; then ok`.
    An SQL error, a renamed table/column, or an empty `exit_node_health` (monitor never ran) all PASS.
    *Revive:* assert the table exists and `COUNT(*)>0` first.
18. `check_b273_exit_node_truth.sh:298-303` (J.2) — same shape for the `state='offline' AND online=1 AND advertised_routes_ok=1` probe.
19. `check_b274_prefix_ownership.sh:226-241` (G.1) — `overlap="$(printf '%s' "$HS_JSON" | python3 -c '… n.get("subnet_routes") …' 2>/dev/null)"`;
    no `command -v python3` guard and no parse-success check, so a missing interpreter, a JSON error, or a
    renamed field prints PASS. The field names are inconsistent in-repo for the same command
    (`givenName` at `check_b272_tag_drift.sh:240` vs `given_name` here). *Revive:* guard python3 and require a
    non-empty parse sentinel before judging.
20. `check_b278_dsn_ip_rotates.sh:97` (C) — vacuously green when the `perl` extraction (`:74`, unguarded) yields an
    empty block; B fails in the same run, so the red is misattributed. *Revive:* `[ -n "$AUTODETECT_BLOCK" ] || bad`.
21. `check_b286_template_index_bounds.sh:58` (A1) — `grep -rE "$pat" … | wc -l || true`: a missing templates
    directory yields 0 matches → "no unguarded index". *Revive:* `[ -d "$TPL" ] || bad` before the loop.
22. `check_b287_tag_ownership.sh:127-128` (C4) — `if ! grep -q 'FAIL' <<< "$OUT"; then ok "… pass"`: a negative
    substring match with no exit-status and no `^ok` check; an unresolved package path prints no `FAIL`.
    *Revive:* `[ "$rc" -eq 0 ] && grep -q '^ok'`.
23. `check_b288_policy_drift_truth.sh:207-211` (F2) — the negative grep for `merged = dict(old["tagOwners"])`
    **fail-opens**: renaming `deploy/skygate-apply-policy.sh` makes the grep fail and the `else` declare success;
    the pattern is also a byte-exact ghost of removed code that survives as a comment
    (`deploy/skygate-apply-policy.sh:174`). *Revive:* `[ -f "$APPLIER" ] || bad`, then assert no executable merge.
24. `check_b289_derp_map_truth.sh:189-193` (F3) — `grep -rq '^func httpGet(' internal/feature/admin/` fail-opens on a
    missing directory (exit 2 → `else` → PASS) and matches only the column-0 spelling; a re-introduced method
    evades it. *Revive:* `[ -d … ] || bad` plus match call sites, not the declaration.
25. `check_b276_1_all_devices.sh:204-205` (E2) — the four selected tests `t.Skipf` when the sqlite dialect is
    unavailable (`all_devices_b276_1_test.go:19-21`) and a skipped run still prints `ok`, so E2 can be green with
    zero assertions executed. *Revive:* `-v` and require the `--- PASS: TestB2761_` lines.

### 3. Comment, banner, or identifier satisfies the grep

26. `check_b264_admin_delegation.sh:177` — `grep -qE 'already_user='` is satisfied by the comment at
    `internal/feature/admin/users.go:137`; deleting the real redirect (`users.go:378`) keeps it green.
    *Revive:* pin `"/admin/users?already_user="+url.QueryEscape(username)`.
27. `check_b264_admin_delegation.sh:109` — `grep -qE 'maxV != [0-9]+'` matches any chain-head number, including a
    wrong one, while the message claims the head is pinned. *Revive:* assert the expected value.
28. `check_b270_startup_blockers.sh:134-139` — `grep -q 'oidc-keys' "$inst"` is satisfied by the comments at
    `deploy/install-common.sh:1038-1039`; deleting the real `install -d … "$data_dir/oidc-keys"` (`:1042`) keeps
    it green. *Revive:* `grep -v '^[[:space:]]*#' "$inst" | grep -q 'oidc-keys'`.
29. `check_b270_startup_blockers.sh:155` — `grep -q 'port_mismatch_warning$' || grep -q '^port_mismatch_warning$'`: the
    second alternative is a subset of the first and both match a comment line ending in the identifier.
    *Revive:* `grep -v '^[[:space:]]*#' "$APPLIER" | grep -qx port_mismatch_warning`.
30. `check_b272_3_policy_helper.sh:60` — A.7's `grep -q 'root'` is satisfied by `# Usage (as root):`
    (`deploy/install-policy-helper.sh:18`). *Revive:* strip comments first.
31. `check_b272_3_policy_helper.sh:63` — B.3 asserts a header comment (`before v1.5.16`/`ProtectSystem`), not that
    the installer still fixes that failure. *Revive:* assert `write_policy_units`/`daemon-reload`.
32. `check_b272_3_policy_helper.sh:73` — D.1 greps the identifier `ReasonPolicyWriteRefused`, never
    `TestB272_ReconcileReportsOwnerFailure` (`internal/nodeownership/auto_b272_test.go:192`). *Revive:* grep the test name.
33. `check_b272_3_policy_helper.sh:112` — F.4 `grep -q 'connection refused' "$AUTO"` is satisfied by the comments at
    `internal/nodeownership/auto.go:729`/`:756`. *Revive:* `grep -A8 'func isTransientHeadscaleDown' …`.
34. `check_b272_tag_drift.sh:163` — C7 greps the **byte-identical** string to C2 (`:134`), so it cannot fail
    independently and proves nothing about "the configured base domain". *Revive:* assert `baseDomain` is used
    where the owner string is built.
35. `check_b276_acl_ownership_sync.sh:160` (B3) — greps a comment (`internal/prefixowner/prefixowner.go:263`).
36. `check_b279_class_tag_identity.sh:187` (E2) — `grep -q 'PickPerNodeTag'` is satisfied by comments in 4 of the 7
    named files (`internal/feature/admin/devices.go:364`, `internal/nodeownership/nodeownership.go:868`,
    `internal/monitoring/exit_node_monitor.go:251`, `internal/telegram/commands_phase2.go:62`).
    *Revive:* `grep -qE '^[^/]*PickPerNodeTag\('`.
37. `check_b279_1_tag_to_hostname_one_copy.sh:168` (E1b) — `grep -q 'IsClassTag'` is satisfied by comments in all
    three non-exempt files (`internal/acl/acl.go:115`, `internal/feature/exit_rules/preferred_check.go:164`,
    `internal/feature/admin/user_subnet.go:449`). *Revive:* `grep -qE '^[^/]*db\.IsClassTag\('`.
38. `check_b280_ci_gates_release.sh:76` (A3) — six needles (`EXIT CODES`, `exit 1`, `exit 2`, `--wait`,
    `--allow-off-main`, `merge-base --is-ancestor`) are satisfied by the doc/EXIT-CODES comment block
    (`scripts/ci_gate.sh:33-44`); no D case passes `--wait`, so deleting the `--wait` arm keeps A3 green.
    *Revive:* anchor to code (`grep -nE '^[[:space:]]*--wait\)'`) and add a `--wait 1` stub case.
39. `check_b280_ci_gates_release.sh:251` (F1) — both needles exist only in `.githooks/pre-tag` comments
    (`:11`, `:58`, `:27`, `:83`); the real call `bash "$gate" --sha …` (`:67`) is unasserted. *Revive:* `grep -q 'bash "\$gate"'`.
40. `check_b280_ci_gates_release.sh:308-309` (I3) — `grep -c '\$VERSION'` over the whole workflow (`≥8`, including
    the workflow's own comment) does not measure "the later steps read `$VERSION`". *Revive:* assert per step.
41. `check_b282_admin_reads_dialect.sh:92-93` (B2) — the four shape tokens (`time.Time`, `int64`, `float64`, `[]byte`)
    all appear in the doc comment of `internal/db/db_time.go:66-71`, so deleting the `case` arms keeps B2 green.
    *Revive:* assert the arms (`case int64:`) or the behavioural `TestParseDBTimeB282`.
42. `check_b283_policy_write_safety.sh:78-82` (A2) — the negative grep for the in-place write is keyed to one
    argument spelling (`os.WriteFile(req, []byte(body)`); any other spelling passes. *Revive:* assert the
    invariant (only `os.CreateTemp`+`os.Rename` touch the request path).
43. `check_b284_device_tag_source.sh:101` (C3) — greps the literal `B284 (2026-09-22) — CONTRACT RENEGOTIATED`, which
    exists as a comment in `internal/acl/acl_b265_test.go:197`; and `:96` (C2) is satisfied by comments in
    `acl_b284_test.go`. *Revive:* assert the renegotiated assertion, not the banner.
44. `check_b285_acl_tags_declared.sh:71` (A3) — greps `B285`, all three hits are comments; `:65-66` (A2) ORs
    `emitTagOwner2(tag, ownerJSON)`, which already matches the earlier `sortedDevTags` loop at
    `internal/acl/acl.go:2103`, not the sweep at `:2136/2139`, so breaking only the sweep stays green.
    *Revive:* scope both greps to the sweep's body.
45. `check_b288_policy_drift_truth.sh:138` (C1) — presence-only `grep -q 's.periodicDriftCheck()'`: it passes with
    the call moved back **inside** `if chg > 0 {` (`internal/feature/exit_rules/sync.go:123-126`), the pre-B288
    defect. *Revive:* assert position, or delete C1 and rely on `sync_b288_test.go:138`.
46. `check_b288_policy_drift_truth.sh:164` (C4) — `grep -q 'chg > 0 {'` also matches `sync.go:386` and `:976`.
47. `check_b288_policy_drift_truth.sh:190` (E1) — one grep for a `tag:public` string that exists at two generator
    sites (`internal/acl/acl.go:856`, `:1957`), masking a regression in either. *Revive:* `-c … -ge 2`.
48. `check_b290_oidc_ui_enablement.sh:106-109` (B6) — the negative branch greps one English sentence that exists
    nowhere; any other restart wording passes. `:101` (B5) inspects only the template while the populating field
    (`internal/feature/admin/oidc_settings.go:137`) is unpinned; `:142` (D3) proves an assignment exists, not that
    routes stay mounted. *Revive:* assert the applier was invoked / the page field is populated / the route is registered.
49. **i18n "RU and EN" counts that count two different keys in one file** (cannot fail for the parity reason they
    print): `check_b265_derp_status_truth.sh:143-150`, `check_b266_exit_node_register.sh:163-170`,
    `check_b275_1_prefix_ui.sh:48-56`, `check_b276_acl_ownership_sync.sh:198-207`,
    `check_b288_policy_drift_truth.sh:181-187`, `check_b289_derp_map_truth.sh:129-135`,
    `check_b290_oidc_ui_enablement.sh:149-156`, `check_b276_1_all_devices.sh:169-173` (the last also pins
    `all_devices_badge`/`_tip`, which **no template renders** — the rendered keys are
    `all_devices_fanout_badge`/`_fanout_tip`, `internal/handlers/templates/exit_rules.html:358,369,377,408`).
    *Revive:* count per language map, or rely on gate B4's `TestCatalogsParity`.

### 4. Over-broad / duplicated / subsumed alternatives

50. `check_b264_admin_delegation.sh:118-129` — file-wide `grep -c` counts for the `is_primary` DDL (`-ge 2`) and the
    partial UNIQUE index (`-ge 1`) are satisfied by two copies inside one `migrateV072*` function, so a missing
    SQLite half passes. *Revive:* scope each grep to its function body.
51. `check_b267_headscale_cli_mode.sh:72-76` (D2) — `grep -q 'SKYGATE_HEADSCALE_CLI'` is a strict subset of D1's
    condition (`:66-68`), so D2 has zero incremental power. *Revive:* assert `os.Getenv("SKYGATE_HEADSCALE_CLI")`.
52. `check_b279_class_tag_identity.sh:219` (G1) and `:266` (J2) — G1 is presence-only for a "validated against LIVE
    exit nodes" claim (deleting the refusal `return` at `internal/feature/exit_rules/form_my.go:1509` keeps
    G1+G2 green); J2 checks one template while the ghost fallback still exists in
    `internal/handlers/templates/user/devices.html:487` and `admin/devices.html:372`. *Revive:* assert the `return`,
    and widen J2.
53. `check_b280_ci_gates_release.sh:102` (B4) — the `awk` window never ends at the next job header, so docker's
    `needs: preflight` is satisfied by the **binaries** job's line (`.github/workflows/release.yml:306`) even when
    docker's own line (`:168`) is deleted. *Revive:* `f && /^  [a-zA-Z0-9_-]+:/ {exit}` in the awk script.
54. `check_b276_1_all_devices.sh:169-173` (D4) — see finding 49 (wrong key family).
55. `check_b272_7_tag_owner_batch.sh:135,150,157-158` (D1/D4/D5) — `grep -A12/-A3/-A10 … | grep -q 'isTransientHeadscaleDown(err)'`
    is satisfied by an **inverted** guard; the test that pins the semantics
    (`TestB2724_PermissionRefusalIsNotRetried`) is not selected by E3's `-run 'B2727|EnsureTagOwner'`.
    *Revive:* extend the `-run` filter, or assert the call order.
56. `check_b283_policy_write_safety.sh:136` (E2) — greps `did not answer on` + `previous policy was restored`; an
    inverted or never-firing rollback keeps both strings. *Revive:* drive the applier under the B268 stub harness.

### 5. `go test` filters and multi-package matching

57. **`-run` filter that matches no test (permanent PASS via `ok … [no tests to run]`):**
    `check_b272_tag_drift.sh:206` — `-run 'B227' ./internal/nodeownership/` (all tests there are
    `TestClassifyFailure_*`/`TestReportFailure_*`/`TestNewTagAlertSink_*`/`TestBuildAlertText_Format`/
    `TestBuildFailureDetail_AuditFormat`, so "F2: the B227 alert-sink contracts still pass" is a permanent PASS).
    *Revive:* `-run 'ClassifyFailure|ReportFailure|TagAlertSink|BuildAlertText|BuildFailureDetail'`.
    Phantom alternatives that are currently masked by other alternatives: `check_b267_headscale_cli_mode.sh:87`
    (`TestBuildSetAdvertisedRoutes` exists nowhere; the real names are `TestSetAdvertisedRoutes_NamesTheKeyProblem…`/
    `…ContainerDefaultHint…`, `internal/headscale/ssh_key_b292_test.go:144,163`); `check_b276_2_routescript.sh:137`
    (`B2762`); `check_b277_3_apply_preferred.sh:150` (`B2762`, `RuleRow`) — the last two are in unregistered scripts.
58. **`^ok` satisfied by a package that ran nothing** — `check_b279_1_tag_to_hostname_one_copy.sh:199-200` (F4):
    the filter `'B279|IsExitNodeTagForm|TagToHostname'` matches **no test** in `internal/feature/admin`, which
    always prints `ok … [no tests to run]`, so F4 stays green even when `internal/acl` **and** `internal/db` both
    FAIL. *Revive:* drop the admin package and add `&& ! grep -q '^FAIL' <<< "$OUT"`.
59. **`^ok` satisfied by one of several packages** — `check_b279_class_tag_identity.sh:296-297` (L2) fails only if
    *both* `internal/db` and `internal/feature/exit_rules` fail; `check_b287_tag_ownership.sh:127-128` (C4) is the
    same shape with a negative match. *Revive:* `&& ! grep -q '^FAIL' <<< "$OUT"`.
60. **Latent (non-vacuous today, verified names; a rename makes them silent PASS):** every
    `OUT="$(go test … -run 'TOKEN')"; grep -q '^ok' <<< "$OUT"` in the slice —
    `check_b270_startup_blockers.sh:163,297`, `check_b272_tag_drift.sh:200,300,348,384,428`,
    `check_b272_7_tag_owner_batch.sh:181`, `check_b273:256`, `check_b274:201`, `check_b275:69`,
    `check_b275_1:61`, `check_b276:228`, `check_b276_1:204,210`, `check_b279:296`, `check_b279_1:199`,
    `check_b281` (none), `check_b282:203-226`, `check_b283:151,158`, `check_b284:107`,
    `check_b285:94`, `check_b286:89`, `check_b287:120`, `check_b288:241`, `check_b289:149,159`,
    `check_b290:165`. *Revive (all):* add `&& ! grep -q 'no tests to run' <<< "$OUT"`.

### 6. SIGPIPE under `set -uo pipefail` (AGENTS trap #9)

All 30 scripts set `set -uo pipefail`. `producer | grep -q` can therefore invert a verdict when the producer
writes more than the pipe buffer after the match:

61. `check_b276_acl_ownership_sync.sh:291` — `journalctl -u skygate --since '-24h' … | grep -qE …`: on a match
    `journalctl` dies with SIGPIPE, `pipefail` makes the pipeline non-zero, and E3 reports "no ownership change in
    the last 24h" **even when the line is there** (it is already SKIP-only, so this is a second masking layer).
    *Revive:* `OUT="$(journalctl …)"; grep -qE … <<< "$OUT"`.
62. `check_b284_device_tag_source.sh:67` — `awk '/^func deviceTagForRule\(/,/^}/' "$ACL" | grep -q '"tag:dev-"'`: a
    SIGPIPE takes the `else` branch and prints `ok "A2: deviceTagForRule invents no tag"` while the synthesis is
    present — the worst inversion in the slice. *Revive:* capture first (`OUT="$(awk …)"; grep -q … <<< "$OUT"`).
63. `check_b284_device_tag_source.sh:72` — same shape for `o.Tag` (a SIGPIPE yields a spurious FAIL).
64. Others of the class (benign today because the producer is short or self-terminating, but armed as files grow):
    `check_b267_headscale_cli_mode.sh:79`, `check_b272_tag_drift.sh:122`, `check_b272_7_tag_owner_batch.sh` (none),
    `check_b274:220`, `check_b275:84`, `check_b276:240,253`, `check_b276_1:221`, `check_b277_prefix_admin.sh:260`
    (unregistered).

### 7. Unregistered contracts in the same numeric range (never run by the gate)

`scripts/check_b276_2_routescript.sh`, `scripts/check_b277_prefix_admin.sh`,
`scripts/check_b277_3_apply_preferred.sh`, `scripts/check_b277_4_consistency.sh` exist and are documented in
`RELEASE-NOTES.md` as hand-run (`:4334`, `:4444`, `:4445`, `:4678`), but **there is no wildcard registration in
the gate** (registrations are explicit per-ID lines; the only loop is over `run_check_v0.*.sh` at
`verify_pre_deploy.sh:604`), and nothing in `verify_post_deploy.sh`, the `Makefile`, `.github/workflows/` (only
`ci.yml:125` runs the catalog and `ci.yml:268` hardcodes `check_b236.sh`) or `.githooks/` invokes them. Their
own contracts are therefore dead by construction:

65. `check_b277_4_consistency.sh:114` — `if grep -q 'r\.AllDevices = ad == 1' "$DB" && grep -q 'r\.AllDevices = ad == 1' "$DB" | head -1 | grep -q .; then`
    — `grep -q` prints nothing, so `head -1 | grep -q .` is always false; with no `else` (`:115-116`) C2 never
    prints PASS or FAIL (the release notes record "21/21" while the file has 22 `ok` sites — the missing one is C2).
66. `check_b277_4_consistency.sh:206` — `if ! awk '/range \$host…/,/range \.AllDevicesByExitNodeCDN/' "$TMPL" | grep -q '{{if \.AllDevices}}'; then ok …`
    passes when an anchor is renamed (awk emits nothing); `:128` and `:176` guard contracts with `[ -n "$VAR" ]`
    and no `else`, so a rename makes them disappear rather than fail.
67. `check_b276_2_routescript.sh:25` advertises a contract G ("live state") that does not exist in the body (F ends
    `:145`, H starts `:147`).
68. `check_b277_3_apply_preferred.sh:73,78` — whole-file greps (`db.GetUserExitNodePref`, `TagToHostname`,
    `PreferredExitNodeForRule`) are also satisfied by pre-existing code (`internal/feature/exit_rules/form_my.go:614-615`,
    `:265`), i.e. they match the broken version; `:146-149` admits the new handler has no test.
69. `check_b277_prefix_admin.sh:263-264` — F1 `[ -n "${N:-}" ]` → unconditional `ok` for any row count.

### 8. Additional findings on registered scripts (B281-B285 detail)

70. `check_b281_ci_catalog_truth.sh:236` (H1) — the pattern `count .*'annotateRulesWithPrefs\(rr, func'` requires a
    `count ` phrasing that no longer exists in the target: the live site is
    `scripts/check_b182.sh:118 check_ge "E2" 1 "$(grep -cE 'annotateRulesWithPrefs\(rr, func' …)"`, so
    re-introducing the unbalanced `grep -E` group (the defect H1 exists for) stays invisible. *Revive:* match the
    property generically (`grep -nE "grep -cE 'annotateRulesWithPrefs\(rr, func'"`).
71. `check_b281_ci_catalog_truth.sh:203` (G) — `i_cmd=$(grep -n 'command -v go' "$f" | head -1 | cut -d: -f1)`
    counts **comment** lines, while the hardcoded-path probe at `:188` strips comments (`text=${text%%#*}`); a
    comment mentioning `command -v go` above a hardcoded probe defeats the ordering invariant. *Revive:* strip `#`
    for `i_cmd` too.
72. `check_b281_ci_catalog_truth.sh:84` — `n=$(grep -cE "$2" "$1" 2>/dev/null) || n=0` turns a grep **error**
    (rc 2, e.g. an unbalanced `(`) into `0`, which is the passing value for every negative contract it feeds
    (`:114`, `:125`, `:236`, `:246`, `:247`, `:267`, `:293`, `:311`, `:339`). *Revive:* `bad` when grep rc ≥ 2.
73. `check_b281_ci_catalog_truth.sh:310` (M2) — `count "$B268" '\-o root \-g root'` is satisfied by the assertion's
    own text (`check_b268_applier_failure_diagnostics.sh:270`) and a comment (`:163`); it proves the harness
    mentions root:root, not that the applier requests it. *Revive:* cite the behavioural half (which already
    asserts the recorded `install.argv`) instead of re-grepping the source. (`:256` J1 and `:357` O3 are weaker
    instances of the same "assert the shape, not the effect" idea.)
74. `check_b282_admin_reads_dialect.sh:124,138,166` (C5/D2/E4) — `hits=$(grep -nF "$token" "$PAGES" | grep -v ':[[:space:]]*//' || true)`
    folds grep's **error** status into the "clean" branch, so a broken invocation reports a negative contract as
    satisfied. *Revive:* test `rc` explicitly.
75. `check_b283_policy_write_safety.sh:100-101` (B3) — `refusing to set a policy that does not parse` is emitted by
    **two** branches (`internal/headscale/acl.go:346` parse, `:354` undeclared tag), so the contract cannot tell
    them apart and the tag branch borrows the parse wording; `:131` (E1) greps the python validator
    (`deploy/skygate-apply-policy.sh:167`) instead of running it; `:136`/`:124` (E2/D2) are string-only.
    *Revive:* assert the distinct tag message (`acl.go:356`) and execute the validator with a malformed body.
76. `check_b284_device_tag_source.sh:67` (A2) — `awk '/^func deviceTagForRule\(/,/^}/' … | grep -q '"tag:dev-"'`
    vacuous-PASSes when the window is empty (function deleted/renamed) — the pair with A3 (`:72`) is not blind,
    but A2 alone is. `:96` (C2) is satisfied by the test file's header comments (`internal/acl/acl_b284_test.go:5,10,20-21`).
    *Revive:* assert the window is non-empty; grep the fixture rows (`acl_b284_test.go:115`).
77. `check_b285_acl_tags_declared.sh:83` (B2) — presence of `UndeclaredTags(gen)` in the test file, not that its
    result is checked (a `_ = undeclared` rewrite keeps it green, though B4 would still catch the behaviour);
    `:88` (B3) `grep -q 'tagged-devices' "$TEST"` is satisfied by header comments (`acl_b285_test.go:15,34`).
    *Revive:* assert the assertion / the fixture INSERT (`acl_b285_test.go:64-66`).
78. **Both B284/B285 behavioural tests force the grants path on** —
    `internal/acl/acl_b284_test.go:43` and `acl_b285_test.go:41` both `t.Setenv("SKYGATE_ACL_VIA_ENABLED", "true")` —
    so the legacy generator (`GenerateACLForPlane`, reached via `internal/acl/acl.go:1141-1143` → `:1178` whenever
    `SKYGATE_ACL_VIA_ENABLED` is not `true`) is never executed by the gate. This is what lets finding 15 (the
    surviving `tag:dev-` synthesis at `acl.go:718`) sit behind a green gate.

## Runtime invariants with no behavioural contract

1. **Tags can stop reaching headscale and no live contract fails.** What a user sees: `node_owner_map` records
   `tag:dev-<user>-<host>` while `headscale nodes list` shows no tags, so every per-device ACL rule matches
   nothing and exit rules silently stop working (the B272 incident). Standing in the way: source greps
   (`check_b272_tag_drift.sh:134,163`), Go tests with a stub client (`:200`), and a live section whose drift
   branch is `skip` (`:242-247`) and whose metric branch is `skip` (`:249-253`);
   `check_b272_7_tag_owner_batch.sh:206` prints a count and PASSes unconditionally; `check_b287_tag_ownership.sh`
   only tests DB helpers. **Proposed behavioural assertion:** with the headscale CLI/API and `skygate.db`
   available, read `SELECT node_id, tag FROM node_owner_map WHERE tag LIKE 'tag:dev-%'` and the live tag list for
   those node ids, and `bad` when a live node lacks its recorded tag after a completed reconcile pass
   (corroborate with `skygate_tag_unmatched_total` / `skygate_tag_autoupdate_failures_total{reason="tag_missing"}`).
2. **The applied ACL policy can drift from the generated one and no live contract fails.** What a user sees:
   clients keep a `via=` pin to a relay that no longer serves the prefix, so those destinations are dropped with
   no log line (B276: 70 of 177 grants pinned a non-primary relay). Contracts: `check_b276_acl_ownership_sync.sh:115-124`
   (single entry point) and `:215-236` (Go tests, stub headscale); the live E2 **skips** exactly when the drift
   exists (`:279-283`) and E3 always skips (`:290-298`); `check_b288_policy_drift_truth.sh` has **no live section
   at all** (`:240` is only `command -v go`) and its strongest evidence is a Go test with a real SQLite DB plus a
   stub policy server (`sync_b288_test.go:138`); `check_b283_policy_write_safety.sh:119-140` greps the applier
   script. **Proposed behavioural assertion:** after a sync pass, read the live policy (API or the discovered
   `policy.path`) and the generated document, compare with `headscale.PolicyEquivalent`/`PolicyDriftDetail`, and
   `bad` on mismatch together with the applier verdict file (`ReadPolicyApplyStatus`, already implemented by B288).
3. **The prefix-ownership reconciler can stop moving owners and only a stub-level test notices.** What a user
   sees: a device whose rule names relay B is served by relay A; headscale's one-primary-per-prefix rule then
   drops the destination. `check_b276_acl_ownership_sync.sh:215-236` (`TestB276_OwnershipChangeReappliesACL`, real
   SQLite + stub headscale) does assert `chg > 0` and exactly one PUT, so a silent stop **would** fail it — but
   only through `-run 'B276'` (`:228`), which is vacuous if the tests are renamed, and nothing live compares
   `prefix_owner` with the advertised routes. **Proposed assertion:** on a host, diff `prefix_owner` against the
   live `subnet_routes` (already parsed at `:254-262`) and `bad` when an owner does not advertise its prefix
   after a pass — i.e. promote E2's `skip` to `bad`.
4. **A policy write can be accepted but never land (applier verdict) with only source greps behind it.** What a
   user sees: the ACL history shows `status OK` every five minutes while the served document stays days old
   (B288.1). `check_b288_policy_drift_truth.sh:202-226` greps the applier and the page for
   `policy-apply.status`/`the write is NOT landing`; `check_b272_3_policy_helper.sh:120-142` python-scans the
   applier's **text** (and is labelled "behavioural"). No contract executes `deploy/skygate-apply-policy.sh`.
   **Proposed assertion:** run the applier once against a temp dir with stubbed `systemctl` (the B268 harness
   pattern) and assert the verdict file plus a rollback when headscale does not come up.
5. **A device tag can still be minted from a user name on the legacy generator path.** What a user sees: the
   generated policy references `tag:dev-<user>-<host>` entries that no node wears and that `tagOwners` cannot
   declare, so `SetPolicy` refuses the whole document and the ACL stays stale (the B284/B285 incident).
   `check_b284_device_tag_source.sh:84` only greps one spelling (`'"tag:dev-" + dp.Username'`), while
   `internal/acl/acl.go:718` still mints `devTag = "tag:dev-" + e.userName + "-" + strings.ToLower(e.deviceHostname)`
   inside `GenerateACLForPlane` — the `user_name`-derived path from the live incident — and no test asserts
   `headscale.UndeclaredTags` empty for that generator; both tests force the other path on
   (`internal/acl/acl_b284_test.go:43`, `acl_b285_test.go:41` set `SKYGATE_ACL_VIA_ENABLED=true`).
   **Proposed assertion:** `grep -q '"tag:dev-" +'` must find
   nothing in `internal/acl/acl.go`, plus a legacy-path test asserting no undeclared tags.
6. **"All my devices" can fail to cover a later device with a dead live check.** What a user sees: the page
   promises coverage while a laptop registered later has no rule. The behavioural half is real
   (`all_devices_b276_1_test.go` uses a migrated SQLite DB), but the live section is inert:
   `check_b276_1_all_devices.sh:223-224` prints PASS for any count including 0, and E2 can be green with all four
   tests skipped (`:204-205`). **Proposed assertion:** count marked groups (`all_devices=1`) and the rules
   materialised for each current device in `node_owner_map`, and `bad` when a marked group lacks a rule.
7. **The local DERP relay can silently vanish from the map.** `check_b289_derp_map_truth.sh` has the strongest
   fixture here (`internal/feature/admin/derp_reach_b289_test.go:266`: real handler, SNI-strict TLS listener,
   migrated SQLite), but nothing fetches `/admin/derp/relays/derpmap.json` from a running instance and asserts
   region 900 is present — the B289 header itself records `{"Regions":[]}` on a healthy relay.
   **Proposed assertion:** a post-deploy probe of that endpoint.
8. **Container-only and journal-only live guards miss the native hosts where the breakages happened.** Live
   sections are gated on a docker container name (`check_b274_prefix_ownership.sh:220`,
   `check_b275_prefix_assignment.sh:84`, `check_b276_acl_ownership_sync.sh:240,253`,
   `check_b276_1_all_devices.sh:221`, unregistered `check_b277_prefix_admin.sh:260`) or on systemd/journal
   (`check_b272_7_tag_owner_batch.sh:195`, `check_b276_acl_ownership_sync.sh:290`). On a native/systemd host
   those all SKIP. Only `check_b273_exit_node_truth.sh:282-303` reads a native SQLite DB directly, and it inspects
   `exit_node_health` — not the tag or ACL paths. **Proposed assertion:** give the tag/ACL checks a SQLite +
   local-CLI path (the pattern B273 §J already uses).

## Notes

- **The gate truncation hides exactly the contracts that matter.** `run_check` prints only the first 20 output
  lines (`verify_pre_deploy.sh:195`). B276 prints ~29 lines before its D section, B288 prints 19 before E1 and 30
  before G2/H1, B289 prints 20 before F1-F3/E1, B290 prints 18 before its E1 rows, B273 prints ~20 before F-J,
  B274 before E.2-G.1. Those FAILs are counted (the exit code and the stderr `bad` rows), but the operator cannot
  see *why* from the gate log — and the unregistered family is invisible entirely.
- **No entry is `behavioural` outright and none is `live-only`.** Detection rests on (a) real-binary probes
  (B269 §E, B270 §E2), (b) Go tests with a migrated SQLite DB plus a stub/fixture HTTP server (B276, B276.1,
  B282-B290), (c) a stubbed-`gh`/real-git behavioural core (B280 §D/E), (d) one native SQLite read (B273 §J) and
  one docker-only live check (B274 §G.1, itself fail-open on a parse failure). Everything else is text.
- **Live-state contracts SKIP instead of FAIL more often than the rule intends.** Regressions (not missing
  dependencies) that yield SKIP: `check_b272_tag_drift.sh:247,253`, `check_b272_7_tag_owner_batch.sh:201`,
  `check_b275_prefix_assignment.sh:91`, `check_b276_acl_ownership_sync.sh:282,294`,
  `check_b276_1_all_devices.sh:226`. AGENTS §1.1's SKIP rule is about an unavailable dependency; these branches
  also swallow observed bad state.
- **Negative greps are the dominant regression guard in the tag/ACL area, and they are all keyed to one
  spelling:** `check_b283_policy_write_safety.sh:78`, `check_b284_device_tag_source.sh:84`,
  `check_b285_acl_tags_declared.sh:65-66`, `check_b272_tag_drift.sh:122`, `check_b288_policy_drift_truth.sh:207`,
  `check_b289_derp_map_truth.sh:189`. Each one also fail-opens if its target file is renamed.
- **False-FAIL / wrong-environment traps inside the slice:** `check_b276_acl_ownership_sync.sh:228` and
  `check_b276_1_all_devices.sh:204,210` call `timeout 600 go test` (on Git Bash `timeout` is Windows
  `timeout.exe`); `check_b278_dsn_ip_rotates.sh:74` requires `perl` without a guard, producing a FAIL (not a SKIP)
  on a host without it; `check_b265_derp_status_truth.sh:37-42` and `check_b267_headscale_cli_mode.sh:93` handle
  the missing-toolchain case inconsistently (the latter reports PASS).
- **Doc drift:** the gate descriptions state contract counts that no longer match the scripts (B264 "60" vs 70
  sites, B268 "11" vs 14, B272 "48"/"22" vs 50, B273 "36" vs 38 labels, B274 "21" vs 24, B275 "13" vs 15,
  B276 "28" vs 33). Not a detection defect, but it makes "the gate is green" harder to audit.
- **Two unregistered scripts in the range contain the only fully tautological assertion found in the whole
  slice** (`check_b277_4_consistency.sh:114`, which never prints a verdict) — a reminder that unregistered
  checks rot silently: nothing in CI, the hooks or `make` runs them.
- Method: read-only, no gate run. Every claim was checked against the cited script line and, for negative-grep
  or comment-matching claims, against the product line it is supposed to pin:
  `internal/feature/admin/users.go:137`, `internal/feature/admin/exit_node_register.go:113`,
  `internal/db/device_tag.go:241-243`, `internal/db/migrations_v0_73_prefix_owner.go:38`,
  `internal/nodeownership/auto_alert.go:131/141/193/199`, `internal/feature/exit_rules/sync.go:123/284/309/386/976`,
  `internal/oidc/service.go` (no `return nil, err`), `deploy/install-common.sh:1038-1042`,
  `internal/acl/acl.go:718`, `internal/headscale/nodes.go:135`, `.github/workflows/ci.yml:127-131`,
  `internal/headscale/ssh_key_b292_test.go:144,163`, `internal/nodeownership/auto_b227_test.go` (no B227 test name),
  and the templates/i18n keys named in the text.
