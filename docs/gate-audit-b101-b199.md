# Slice B101–B199 (98 run_check entries, 92 scripts)

Static, read-only audit of every `run_check` entry in `scripts/verify_pre_deploy.sh`
whose ID is B101–B199 (plus sub-IDs). Verified by reading the gate, every check script,
and the product/test files each contract names.

Gate mechanics that shape every finding:

* `run_check` (`scripts/verify_pre_deploy.sh:95-198`) keys **only off the exit code**:
  `rc != 0` → FAIL, `rc == 0` → PASS (`:190-197`). PASS rows print no check output.
* A failing check's output is printed with `head -20` (`:186`, `:195`) — every FAIL line
  after the 20th is invisible in the gate log.
* Therefore: **a script that prints `FAIL` but exits 0 is shown as PASS and its `FAIL`
  text is never printed** (output is dumped only when rc != 0). This is real in B162 and
  B127/B131 (below).
* Style convention used in the table (one style per script): **behavioural** = executes
  product code / a real DB / a real server and asserts an outcome or computed value;
  **structural** = greps source text, symbols, files, templates, i18n keys,
  registrations; **mixed** = has both (an asserted `go test`/live probe plus greps);
  **live-only** = its meaningful assertions need a live dependency and SKIP elsewhere.
  A bare `go build`/`go vet` is treated as **structural** (a compile check cannot catch a
  wrong runtime behaviour), even when fail-closed.

Entry inventory (ID → gate line → script), complete for the slice:

| ID | gate line | script |
|----|-----------|--------|
| B101 | 2952 | check_b101.sh |
| B102 | 2969 | check_b102.sh |
| B103 | 2980 | check_b103.sh |
| B105–B116 | 3012–3235 | check_b105 … check_b116 (no B104/B117) |
| B118–B137 | 3236–3275 | check_b118 … check_b137 |
| B138 | 3276 | inline `bash -c` (two greps) |
| B139 | 3282 | inline `test -f` / `! test -f` |
| B140–B145 | 3284–3295 | check_b140 … check_b145 |
| B149, B150 | 3296, 3298 | check_b149.sh, check_b150.sh |
| **B152 (1st)** | **3300** | check_b152.sh — *misregistered, see Notes* |
| B147, B148 | 3302, 3304 | check_b147.sh, check_b148.sh |
| **B153 (1st)** | **3306** | check_b153.sh — *misregistered, see Notes* |
| B154–B167 | 3308–3336 | check_b154 … check_b167 |
| B151, **B152 (2nd)**, **B153 (2nd)** | 3338, 3341, 3344 | check_b151.sh, check_b152.sh, check_b153.sh |
| B168–B180 | 3389–3432 | check_b168 … check_b180 |
| B182–B188.3 | 3434–3480 | check_b182 … check_b188_3 |
| B191 | 3495 | check_b191.sh |
| B194–B198.1 | 3499–3516 | check_b194 … check_b1981 |
| B146 | 4052 | check_b146.sh |

`run_check_slow` is used only by B8 (out of slice). No entry in the slice uses it.

## Table

| ID | script | style | catches | CANNOT catch |
|----|--------|-------|---------|--------------|
| B101 | check_b101.sh | structural | `do_pg_restore`/`load_dsn_from_env`/PG dispatcher present in `scripts/restore.sh`; `bash -n`; case-8 ordering `do_env` before `do_skygate_db` | whether a restore actually runs, against which DSN/DB; the advertised end-to-end smoke test (dead, §A); the throwaway-container replay, `ON_ERROR_STOP` (WARN only), mount path |
| B102 | check_b102.sh | structural | Dockerfile installs cifs-utils/nfs-utils/sshfs; `Protocol{SMB,NFS,SFTP}` cases in `mount.go`; `ProtocolLocal` early return; `bash -n` of `test_backup_protocols.sh` | that SMB/NFS/SFTP mounts work in the container; the helper is never run (protocol names matched as bare substrings over the whole file, §B) |
| B103 | check_b103.sh | structural | handler/route/template/i18n/audit markers for in-app S3 download; `minio-go` import | that the download streams bytes; `StatObject`/`GetObject`/`Content-Disposition` are greps. Its one execution — `go build \| grep -q 'error'` (`:140`) — is fail-open twice (§A) |
| B105 | check_b105.sh | mixed | `.table-wrap` per admin template + `.title-row` 60px rule; real `go test -run TestB105_` (2 real tests) | that a page scrolls or the title clears the hamburger on a viewport; `.table-wrap` counted per file, not per table; the Go test re-reads CSS text |
| B106 | check_b106.sh | structural | `@media` rules hide `.sidebar .toggle`; `.collapsed` force-cleared; source order of the HIDE/TOUCH rules | CSS cascade resolution; `layout.html` onclick behaviour; a later `@media` block re-showing the toggle (blocks concatenated, §B) |
| B107 | check_b107.sh | structural | breadcrumb mobile padding; collapsed summary 16px icon rules | that the browser applies them (later overrides invisible); "collapsed rules AFTER base rules" is claimed but never checked |
| B108 | check_b108.sh | structural | inline `<script>` removes `collapsed` on summary click (substrings) | any JS execution — a `{{if}}`-wrapped or broken script passes; the Go twin asserts the same substrings |
| B109 | check_b109.sh | structural | desktop breadcrumb `padding:10px 24px 10px 40px`; mobile block still 60px (duplicate of B107's assertion) | later desktop overrides; the mobile half is not an independent contract |
| B110 | check_b110.sh | mixed | tailnet symbols/hostnames/categories/4 CLI flags + docs headings; real `go test -run 'TestVpsHostnameSet\|…'` (19 tests) | real probe/SSH behaviour (tests inject fake `tailscale status`); `B110_QUICK=1` downgrades to greps only while still printing PASS; §B items |
| B111 | check_b111.sh | mixed | `isInfraNode` rule, `BackfillInfra` UPDATE text, catch-all call sites, ≥4 test names; real `go test -run TestGetInfraExitNodeTags` | the DB-backed `BackfillInfra` path (never executed); the call-site count fails open at 0 (§A); whether grants are emitted |
| B112 | check_b112.sh | structural | 5 dead-code removals absent; 3 check-script updates; fail-closed `go build ./...` | removal in another declaration shape/method/generic form (anchored `^(var\|func) name`, §B) |
| B113 | check_b113.sh | mixed | `isValidIPOrCIDR` call site + message; fail-closed `go build`; real `go test -run TestIsValidIPOrCIDR` | handler wiring → HTTP 400; that a bad value never reaches the DB/ACL builder; the needle loop prints PASS after `err` (§B) |
| B114 | check_b114.sh | structural | `verify_migration.sh` exists, `bash -n`, phase markers, portable-driver strings, `scp`/`docker cp` literals | the 3-phase chain and Python staging (never run); the hardcoded `docker exec skygate-skygate-1` cannot be detected as broken |
| B115 | check_b115.sh | structural | skip-filter symbols/env names; ≥3 `tailnetSkipHostnames()` mentions; `relay-1` in one test body | per-test filtering (the loop ignores its variable); the count is met by the definition + 2 of 3 call sites; contract 17 is an unconditional PASS row (§A) |
| B116 | check_b116.sh | structural | derp_relays table/symbols/columns, 6 handlers, 6 routes, i18n keys, template markers | handler/DB behaviour; columns can be dropped while the word survives elsewhere; the route grep is method-agnostic; contracts 18/19 are unconditional PASS rows (§A/B) |
| B118 | check_b118.sh | mixed | via-loop owner parsing; `infra@` owner for `tag:exit-node`; live psql of `acl_snapshots`/`node_owner_map` (VM) | the live section needs `/home/skyadmin/.env` + `psql` (silent skip when the DSN is empty); off-VM nothing observes the generated policy. The `"infra@"` emit-site check degrades to "≥1" (§B); DB errors are reported as FAIL, not SKIP |
| B119 | check_b119.sh | mixed | `TagToHostname` 4 case labels; pref read call sites; a live psql probe of pref tags | the live probe is **dead** (`NOT LIKE '%'` is unsatisfiable, §A), so no runtime behaviour is covered; helper semantics only by grep |
| B120 | check_b120.sh | mixed | breadcrumb CSS offsets + sibling ordering; `go test -run TestB120_` (4 tests, but they only re-read `themes.css`) | CSS cascade/rendering. **Contract E cannot fail the gate** (§A); `cmd \| grep -q` + `pipefail` false-FAIL hazard (TD-19) |
| B121 | check_b121.sh | mixed | Mint theme tokens, scrollbar CSS, contrast bump; `go test -run TestB121_` (4 tests, re-read CSS) | rendering; **contract F cannot fail the gate** (§A); same SIGPIPE hazard |
| B122 | check_b122.sh | structural | `restore.sh` PG path text (`do_pg_restore`, postgres:18-alpine, `psql -f`), DSN source | whether the replay works; `ON_ERROR_STOP` missing is a WARN (§A); the live archive probe self-skips on a SIGPIPE'd `tar \| grep -q` (§D); contract C is satisfied by the v1.3.8 comment (§B) |
| B123 | check_b123.sh | mixed | duplicate-alert params, anchor, 3 i18n keys; real `go test -run TestBuildDuplicateRedirectURL` (5 genuine pure-function tests) | the redirect round-trip in a browser; the handler's duplicate branch (DB/session/form re-fill) |
| B124 | check_b124.sh | mixed | dev-build banner/`not .DevBuild`; 4-part `splitVersionParts`; real `go test -run TestDevBuild` + `-run 'TestCompareSemver\|…'` | contracts E and F assert the **same** literal that occurs twice in `update.html` (§B); no page render with `DevBuild=true`; G greps test-table text |
| B125 | check_b125.sh | mixed | 5-col UNIQUE INDEX text, `ON CONFLICT … RETURNING id`, 3 test names; runs `go test -tags=postgres -run TestAppendDeviceRule_B125` | the SQL against a real DB unless `SKYGATE_TEST_PG_DSN` is set — and a skip is reported as PASS (§A); the "column types verified" row asserts nothing (§A) |
| B126 | check_b126.sh | mixed | R9 uses direct `created_at` (no `EXTRACT(epoch…)`); live SSH psql probe (self-skips by default) | contracts F and G are local simulations on hard-coded literals (§A) and the live probe self-skips unless `SSH_HOST` is set; the real R9 comparison never runs |
| B127 | check_b127.sh | structural | R11–R18 blocks use `json_field`; `DB_JSON_FILE` pattern; json_field defined | **currently FAILs for the wrong reason**: the R28/R29 windows contain no `json_field` (§C); it never runs `verify_post_deploy.sh`; "all JSON parsing happens on the VM" is an absence grep |
| B128 | check_b128.sh | mixed | 4-part `splitVersionParts` + 4-iteration loops in 3 files; test-table literals; **real `go test -run TestCompareSemver ./internal/update/ ./internal/release/`** (`:150`) | three CompareSemver implementations are never cross-checked against each other; D/E pin table strings (a wrong `want` passes); G is B129's form-action grep |
| B129 | check_b129.sh | structural | unconditional Apply button (6-line window), schedule card, config fields, i18n keys, route | the schedule actually firing; a re-introduced multi-line auto-toggle banner passes; the scheduler-file check is WARN-only (§A) |
| B130 | check_b130.sh | mixed | scheduler struct/tick/`runScheduled`, `scheduler_db.go` bindings, main.go guard, storage keys; real `go test ./internal/update/` | that a tick ever runs / writes `update_schedule_last_run`; the three main.go greps are independent (same block/goroutine not checked) |
| B131 | check_b131.sh | structural | Linear palette tokens, alert opacities, `var(--bg-elev)` (broken), theme selectors | **currently FAILs for the wrong reason**: contract E's awk range stops before the rule it wants (§C); a python-less host turns the contrast contract into a false FAIL; no rendering/contrast ratio |
| B132 | check_b132.sh | mixed | per-node sync helper/handler/route/template/i18n; real `go test` over exit_rules+admin+i18n | that Re-sync re-advertises routes to headscale; `a_loop_call` and `a_pernode_call` are the **same** grep (§B); `miss_ru` is a dead variable, so "RU+EN" is a total count |
| B133 | check_b133.sh | structural | Linear palette deltas, zebra rows, `.card` border-strong, 3-stop shadow | contract F's condition omits the border-color requirement (§B); no rendering |
| B134 | check_b134.sh | structural | `themes.css` has no `<style>` wrappers, starts with a CSS comment; re-runs B131/B133 | `match_count` returns 0 → PASS for a file never searched (§A); a missing/non-executable B131/B133 is a WARN (§A) |
| B135 | check_b135.sh | structural | Manrope `--font`, size bumps, 4 Manrope @font-face files | contract G greps a count and is skippable (§B); the deleted gstatic preconnect is still advertised in the gate text |
| B136 | check_b136.sh | structural | V057 columns (PG only), prefs helpers/handler/route/template, 18 i18n keys | `file_check … \|\| exit 0` greens the script on a missing file (§A); no SQLite chain check; no DB round-trip of the prefs |
| B137 | check_b137.sh | structural | 14 swatch tiles, `.color-swatch` CSS, click JS, 2 i18n keys | that a click sets the hidden input or that the colour persists (same `file_check \|\| exit 0`, §A) |
| B138 | inline `:3277-3281` | structural | `device_rules_natural_key_uniq` present; `globalSettingsKeyAutoUpdate` constant gone | everything else in the "catalog cleanup" claim (B104 removal, B95 fixes) — the whole contract is two greps |
| B139 | inline `:3283` | structural | one test file exists; five stub test files do not | that the replacements are meaningful or run (B1 covers `go test ./...` separately) |
| B140 | check_b140.sh | mixed | SQL/setter/getter/sentinel/handler/route/select/6 test names + real `go test -run TestParseAcceptRoutesFormValue` | that the UPDATE reaches the DB and changes `accept_routes`; the handler redirect/flash |
| B141 | check_b141.sh | mixed | `InsertPortalUserAdopt` ON CONFLICT, handler/validator/route/template/flash, 4 i18n keys + real `go test -run TestValidateHSOrphanName` | the adopt insert against a DB; `err == sql.ErrNoRows` is unanchored (12 unrelated sites) |
| B142 | check_b142.sh | mixed | verify-scheduler struct/tick/due/readers/keys, config, wire-up, route, template, 12 test names + real `go test -run 'TestTailLines\|…'` | that a tick alerts on failure — the "Fail handler writes archive/error" conjuncts are the **same greps** as the OK handler (§A) |
| B143 | check_b143.sh | mixed | cleanup scheduler struct/tick/due, storage keys, wire-up, subcommand/i18n, 14 test names + real `go test -run 'TestFormatCleanupMessage\|…'` | that a tick DELETEs rows / alerts; vacuous i18n conjunct (§B) |
| B144 | check_b144.sh | mixed | history aggregator/handler/query/placeholders/sort, template tabs/windows/columns, 11 test names + real `go test -run 'TestParseHistoryWindow\|…'` | the rendered History tab numbers; `grep -cPzo` has no fallback → Windows Git Bash false FAIL (known trap) |
| B145 | check_b145.sh | structural | HA chain/elector/storage/credentials symbols, env names, wiring, test names | failover decisions, DNS provider selection, "encrypted at rest"; provider-name matching is satisfied by prose (§B) |
| B146 | check_b146.sh | structural | `b146_regapi_live.sh` inventory, `bash -n`, 4 error codes, format, docs/registration | the live reg.ru mTLS call; B.2 is a WARN that can never fail (§A) |
| B147 | check_b147.sh | mixed | certsync structs/S3 layout/tests, config fields+env defaults, wire-up + real `go test -run 'TestNoVersionIsNoOp\|…'` (4 tests) | the real S3 pull + Caddy reload; `CertSyncInterval` 30s is satisfied by unrelated 30s defaults (§B) |
| B148 | check_b148.sh | mixed | certificate admin helpers/templates/i18n + i18n parity test + 5 admin helper tests | the upload→S3→Caddy path and DNS-01 toggle; reintroduces the `/tmp \| tee` + `pipefail` trap (§D) |
| B149 | check_b149.sh | mixed | HA page handlers/route/template/i18n/audit + parity test | HA handler behaviour; count-based RU/EN parity accepts an untranslated RU value (§B); `/tmp \| tee` trap (§D) |
| B150 | check_b150.sh | mixed | deploy CLI symbols, admin page, template, routes, subcommands, i18n + `go test -run TestCatalogsParity` | push/pull/sync/status, the dry-run failover prediction, the audit list (never executed); 12 gate-claimed helper signatures are not pinned; i18n is a count |
| B151 | check_b151.sh | mixed | `init-headplane.sh` modes/steps/env/audit strings; live guard runs it with a fake `.env` and asserts the docker-ps early failure | key minting, `.env` backup, NEEDS_KEY idempotency, `/admin/healthz` verify (never executed); step order not checked |
| B152 (both rows) | check_b152.sh | structural | `deploy/scripts/bootstrap_standby.sh` exists/executable, 3 env names, 6 step markers, idempotency/healthz/audit/S3 strings | the live bootstrap; several markers are satisfied by comments/log lines (§B). **The `:3300` "static asset sanity" contract is a misregistration — Notes** |
| B153 (both rows) | check_b153.sh | structural | `dr_drill.sh` exists/executable, 5 step markers, 3 flags, "no data destruction" greps, 60s/90s strings | the drill outcome; step ORDER; `seq 1 60` also matches `seq 1 600` (§B). **The `:3306` "/my/tokens UX" contract is a misregistration — Notes** |
| B154 | check_b154.sh | mixed | tokenrotate symbols/SQL/wiring; live `go test ./internal/tokenrotate/` (8 tests) | the rotation itself; **both `go vet` checks are always-green** (§A) |
| B155 | check_b155.sh | mixed | reissue handler/custom TTL/invalidations/i18n; live `go test ./internal/feature/my/` (24 tests) | handler behaviour; the claimed call ORDER; `go vet` always-green (§A) |
| B156 | check_b156.sh | mixed | keynotify scheduler/SQL/sink/config; live `go test ./internal/keynotify/` (11 tests) | who gets notified / `notified_at` dedup; contract E is satisfied by the literal "B156" comment (§B); `go vet` always-green |
| B157 | check_b157.sh | mixed | notification wrappers/SQL/table/handlers/routes/CSS/i18n; `go vet`+`go test` over 4 packages | **the live `go test` assertion is dead** (§A) — a failing notifications/keynotify/my/db suite cannot redden B157; the bell is never executed |
| B158 | check_b158.sh | structural | 20 woff2 files exist + >1KB, `@font-face`/local srcs, no Google Fonts `<link>`, dir size <1MB | gate text says 24 files, the array has 20 (`:35-56`); the claimed <500KB bound is <1MB; nothing serves `/webfonts/*` or checks `Cache-Control`; the 4 font-awesome woff2 files are in no check |
| B159 | check_b159.sh | structural | keys page device column/relative-time/cleanup SQL/i18n + `go build`/`go vet` | the cleanup flow; "used keys never deleted" is one SQL string; the `keys.never_expires` else-branch is `ok` (§A); the ≥14-case count is file-wide |
| B160 | check_b160.sh | mixed | renew handler/route/template/i18n/warning branch + `go test -run TestTemplateArgsMatchCatalog` | renewal itself; the 410 path; `bad` branches unreachable under `set -e` (§A) |
| B161 | check_b161.sh | mixed | 115 contracts (OIDC files/routes/env/JWKS/token/userinfo/snippet) + `go test ./internal/oidc/` incl. `TestE2E_HeadscaleClientFlow` | the consent-key loop has `ok` in BOTH branches (§A); every `out=$(go test …)` under `set -e` aborts before its `bad` (§A); the live probe `check_b161_4.sh` is invoked by nothing |
| B162 | check_b162.sh | structural | delete handler/helper/route/template/confirm/i18n greps; `go build`/`go vet` | the delete cascade (never executed). **The `go build` contract cannot fail the gate** (§A); the `go vet` path FAILs instead of SKIPping on a Go-less host |
| B163 | check_b163.sh | structural | `<details class="system-test-output">`, `<pre>` styling, copy button, 6 i18n keys + build/vet | that FAIL output is collapsed/visible on load; `bad` branches unreachable (§A) |
| B164 | check_b164.sh | structural | DERP-init handler/route/template/`derp-init.sh` markers/JSON keys/i18n + build/vet | the remote install and the JSON→`derp_relays` insert; the JSON-shape regex pins text order (§B) |
| B165 | check_b165.sh | structural | registration-form grid/hints/`<details>` help/OS snippets/16 i18n keys + build/vet | any rendering; "collapses to 1 column on mobile" is two untied greps; RU+EN is a total count; `bad` unreachable (§A) |
| B166 | check_b166.sh | structural | the two system tests' SOURCE text (`Name:`, `ExtendNodeExpiry`, deferred restore, 29d/31d literals, gRPC wordings) | the tests themselves (live `/admin/system_tests` only); the restore proof uses a file-global `defer` latch (§B); `x=$(grep …)` under `set -e` aborts before its `bad` |
| B167 | check_b167.sh | mixed | OIDC sync source contract + **executes the real `deploy/oidc-sync.sh --download-only`** and asserts its JSON; live route probe 200/302 | the Go wrapper (`RunSync` parse); `"func RunSync"` is satisfied by `RunSyncCtx` (§B); i18n parity is a count; live halves SKIP on Windows |
| B168 | check_b168.sh | structural | nginx snippet locations/`X-Forwarded-Proto`, setup-script step labels/env names/backup/audit token | public OIDC reachability, nginx reload, `.env` write order, the audit row — all echoed-message pins (§B); the script is never run |
| B169 | check_b169.sh | structural | admin delete handler/route/template/i18n greps | the delete cascade; A.2 accepts the bare word `forbidden`; the A.4–A.6 fallbacks accept any `devicedelete.Delete(` mention; `RU_COUNT=$(…)` under `set -e` can abort before the parity `bad` |
| B170 | check_b170.sh | structural | classifier call site, `5*time.Minute` token, 3 template branches, i18n emptiness; 4 test NAMES | the 5-minute rule (never evaluated); `grep -q '""'` for the empty case; test bodies can be no-ops (§B) |
| B171 | check_b171.sh | structural | devicedelete coordinator fields/methods/SQL/audit note/flash params + build/vet | the delete cascade; `awk_grep_match` is defined but never called (§A/B); RU/EN inferred from a total count; literal-TAB struct matching |
| B172 | check_b172.sh | mixed | `next` handling in PostLogin/GetLogin/login.html, `safeNextRedirect` shape, e2e steps + real `go test` of auth and oidc packages | browser redirect behaviour; B.2's 5 categories are OR'd (§B); A.8's primary regex cannot match the markup; C.1 matches a `t.Logf` |
| B173 / B173.1 | check_b173.sh | structural | B173: form ids, spinner, disabled/readOnly JS, `login.submitting`; B173.1: overlay markup, `form.submit` override, 3 navigation listeners, `showLoading()` | any JS execution (no runner): syntax errors, wrong selectors, uncalled `showLoading()` pass; D.3 untied declarations; A.8/A.9 match selectors anywhere; B.1 is count-based |
| B174 | check_b174.sh | mixed | `ParseJWT` usage, colon-format absence, `NewService` param count, e2e assertions + real `go test ./internal/oidc/...` | the security property beyond the unit tests; A.6's greps are file-wide (§B); param counting breaks on a multi-line signature |
| B175 | check_b175.sh | mixed | Strategy-E symbols/literals + real `go test ./internal/nodeownership/...` (7 subtests) | the auto-tag run (never executed against headscale); A.3 is satisfied by two identical guards elsewhere (§B); `tag:private` pinned as a literal |
| B176 | check_b176.sh | mixed | `ToLower(n.Hostname)` at 4 sites, tooltip text (old text gone) + real `go test ./...` | that headscale accepts the tag; the no-straggler sweep is vacuous when the regex stops matching (§B); the A.5 B284 branch is dead code |
| B177 | check_b177.sh | structural | `hs.AddTag`/`hs.UntagNode` presence, B177 comment, warn text, docs/registration, 2 file-existence rows | **the headline guarantee (AddTag before UntagNode, untag only on success) is not checked — contract E is explicitly unimplemented** (§A); no test is run |
| B178 / B178.1 | check_b178.sh | mixed | annotation fields/helper/line ordering/templates/test names + real `go test ./internal/feature/exit_rules/...` (captured then matched) | the rendered page; G2 silently skips when either anchor is missing (§A); `grep -q '^ok'` proves only that some test in the package passed; K greps the stub's return value |
| B179 | check_b179.sh | live-only | live iptables/rules.v4 inspection + `curl` to headscale :50444 (VM only) | everything off-VM: A/B/C/D report PASS "no-block" with **no evidence** (§A); the live `iptables -L` field order cannot match the pattern; E FAILs (not SKIPs) when headscale is stopped |
| B180 | check_b180.sh | structural | no `json.NewEncoder` in the sync handler, `http.Redirect` + target | the browser outcome; the broad raw-JSON detector `A` is computed and unused (§B); the A PASS line is a tautology (§A) |
| B182 | check_b182.sh | mixed | `ApprovedInHeadscale`/helper/annotator/templates/pass-through + real `go test ./internal/feature/exit_rules/...` | that headscale really approved the CIDRs; E1 silently skips when the annotator is missing (§A); `cmd \| grep -q` + `pipefail` (§D) |
| B183 | check_b183.sh | mixed | migration registration + dedup CTE + ON CONFLICT shape + live psql drift (VM) + captured-then-matched `go test ./internal/db/...` | the migration never runs without PG; the live probe is emilia-only ×2; `TOTAL -le 50` can report `still-has-dup` with zero duplicates (§C); F requires exactly one `DELETE FROM device_rules` in the whole file |
| B184 | check_b184.sh | mixed | `LoadResolvedByDomain`/SQL filter/key format/call sites/7 test funcs + real `go test` + live psql counts (VM) | the rendered badge; F-resolved-key matches a doc comment only (§B); the live N pin (discord=0) turns red after an ordinary autoupdater run; `cmd \| grep -q` (§D) |
| B185 | check_b185.sh | mixed | entrypoint `--advertise-tags`, `LookupResolvedForDomain`+`cdn:`, telegram card; live docker/curl probe (N/O/P) | live N/O/P SKIP with in-container Tailscale disabled and P on any psql error; B is satisfied by a **comment** in entrypoint.sh (§B); D is comment-heavy; the ping counts the stats line, so 100% loss passes |
| B186 | check_b186.sh | mixed | rich builder types/blocks/fallbacks/labels + `go build` and `go test ./internal/telegram/...` | the Bot API endpoint working (never sent); B-fallback matches a log line (§B); I is comment-satisfiable; J-build is fail-open under `pipefail` (§A); Q's pinned test is itself a source grep |
| B187 | check_b187.sh | mixed | `$1` placeholder, the exact `?` literal absent, test name + live psql of the operator's chat row (VM/docker/sudo) | `/my_status` rendering; only the one literal `?` form is excluded (§B); without passwordless `sudo` the live probe false-FAILs |
| B188 | check_b188.sh | mixed | `DevTag` template usage/migration/backfill strings + live docker psql ghost-row and `headscale policy get` via-pin checks (VM) | B/C/D are **comment-only** (`NormalizeExitNodeTag` is no longer called — `db.ResolveExitNodeTag` is), P matches unrelated migrateV047 lines, and K/L pass while `user/devices.html:487` + `admin/devices.html:372` still synthesise `tag:exit-<host>` (§B). Only J2 checks the fallback, and only for `user/exit_nodes.html` |
| B188.2 | check_b188.2.sh | mixed | per-CIDR via code, `exitNodeTagToHostname`, `ACLEntry.ExitNodeID`, SQL, 2 test names + live policy checks S–X (VM+docker) | contract B accepts the pre-B265 shape (no pin) as PASS (§A); **M can never FAIL** — a broken build prints `SKIP [M-build-vet]` (§A); G/H matched by other structs/other `exit_node_id` hits; J is comment-satisfiable; the live checks need the hardcoded checkout path |
| B188.3 | check_b188_3.sh | mixed | `resolvePerCIDRVia` used by both generators, struct field, docs + unit tests + live re-run of check_b188_2.sh | **K can never FAIL** (§A); **M reports a genuine integration-test failure as `SKIP [M] PG not reachable`** (§A), so the useVia=false path is unguarded; L passes on "no tests to run" if the test names change; live J needs `/home/skyadmin` |
| B191 | check_b191.sh | live-only | live device registration via a real preauth key + OIDC non-404 probe | **off the VM it prints SKIP and `exit 0` at `:217`/`:233`, so E–H (incl. the OIDC probe) never run and the gate shows PASS**; cleanup PASSes after `\|\| true` (§A); the `&&`/`\|\|` precedence accepts any text containing "1." |
| B194 | check_b194.sh | structural | deployrun files, 6 step files, DeployStep impls, `Framework.Run`, migration tables, SSE strings, docs/registration | any run of the framework; steps whose `Run` always errors or `Rollback` is a no-op pass all ~30 contracts |
| B195 | check_b195.sh | structural | migration file, 6 cluster_* tables, idempotency, driver registration, docs, fail-closed build/vet | that tables are created/used at runtime; contract C duplicates B (§B); G pins docs prose |
| B196 | check_b196.sh | structural | handler/page/template/i18n/route/`cluster.go` helpers + build/vet | that the page renders, that `probeDB` reports reachability; F claims RU+EN parity but passes with an RU-only key (§B) |
| B197 | check_b197.sh | structural | test/edit handlers, form fields, routes, template form, i18n, audit string, build/vet | Test Connection connecting / Edit persisting; E passes with an RU-only key (§B); F/G are not tied to the Edit handler |
| B198 | check_b198.sh | structural | dbmigrate package/steps/interface/Run+rollback/migration/SSE/routes + build/vet | the workflow itself (dump/restore/cleanup are documented STUBs); `Rollback` presence cannot see a no-op |
| B198.1 | check_b1981.sh | structural | migrate card/`migrate_run.html`/LoadRun/RunView/routes/i18n + build/vet | that the UI renders live progress; F passes with RU-only keys (§B); the F FAILs sit after the 20-line cut |

## Dead or near-dead contracts

Verified by reading the cited lines. "Dead" = cannot fail, or cannot fail for the reason
the contract claims.

### A. Cannot fail at all (fail-open / unconditional pass / tautology / silent skip)

- `scripts/check_b162.sh:249-251` — the `go build ./...` contract — `bad "go build output: …"` runs **inside a pipeline subshell** (`"$GO_BIN" build ./... 2>&1 | (read line; … bad …)`); its `exit 1` ends only the subshell, the script never checks the status and falls through to `all contracts satisfied` → **exit 0**. The gate prints PASS and suppresses the FAIL line (output is dumped only on rc != 0). Revival: `out=$("$GO_BIN" build ./... 2>&1) || true; [ -z "$out" ] || bad "go build output: $out"`.
- `scripts/check_b101.sh:156-196` — "12. End-to-end smoke test" — builds a synthetic archive, then runs `bash /tmp/skygate-restore-smoke-test.sh` (`:190`), a script **nothing creates**; `|| true` swallows the ENOENT and `SMOKE_OUT` is never read, so no PASS/FAIL row is emitted. Revival: write the driver into `${SMOKE_DIR}` (or pipe the choice into `bash scripts/restore.sh`) and assert the output reached `do_pg_restore`/`psql`.
- `scripts/check_b111.sh:124-129` — "both GenerateACL call sites" — `COUNT=$(grep -c … || echo 0)`: with 0 matches `grep -c` prints `0` **and exits 1**, so `|| echo 0` appends a second line (`COUNT="0\n0"`); `[ "0\n0" -lt 2 ]` errors (status 2, never true) → the `else` prints PASS "…both GenerateACL variants covered" and the script exits 0. Deleting both call sites in `internal/acl/acl.go` reports PASS. Same bug at `:136-141` for the ≥4 test count. Revival: `|| true`.
- `scripts/check_b119.sh:183` — the live "no unsupported tag format" probe — `AND exit_node_tag NOT LIKE '%'` is **false for every non-NULL string**, so the WHERE is unsatisfiable, `count(*)` is always 0 and `:185` prints `ok` unconditionally on every VM run. Revival: `SELECT DISTINCT exit_node_tag` against an explicit allow-list, or run `TagToHostname` over each row in a Go test.
- `scripts/check_b125.sh:115-117` — "natural-key column types verified" — the count `nullable_count` is computed and printed but **never branched on**; the row passes with 0 matches and claims a NOT NULL precondition the index does not use. Revival: assert the 6 columns explicitly.
- `scripts/check_b125.sh:224-234` — contract I ("B125 Go tests pass") — `internal/db/test_helpers_pg.go:62-65` `t.Skip`s when `SKYGATE_TEST_PG_DSN` is unset; `go test` still exits 0, so the `rc -eq 0` branch prints "ok … passed" and the `elif … SKIP` branch is unreachable. A fully skipped suite is reported as a pass. Revival: require `--- PASS: TestAppendDeviceRule_B125` in the captured output.
- `scripts/check_b120.sh:128-136` and `scripts/check_b121.sh:176-184` — contracts E/F — `if go test … | grep -E … | tail -5; then … else warn` under `set -uo pipefail`: a failing suite makes the pipeline non-zero and reaches only `warn`, which does not touch FAIL → **exit 0**; the `bad` at `b120:132`/`b121:180` needs the duplicate `go test` run on the next line to fail after the first one passed. The `else warn` branches are themselves dead (the `tail` decides the pipeline). Revival: capture, then branch on `$?`.
- `scripts/check_b157.sh:275-279` — the live `go test` over 4 packages — `if go test … | grep -E '^FAIL' | head -1; then bad; else ok; fi` with `set -euo pipefail` (`:40`) → a failing suite is non-zero → `else` → PASS; `bad` is unreachable. The B157 live section therefore cannot report failure at all. Revival: `if go test …; then ok; else bad; fi`.
- `scripts/check_b154.sh:247-250`, `scripts/check_b155.sh:216-219`, `scripts/check_b156.sh:254-257` — `if go vet … | grep -E '…(error|undefined)' | head -1; then bad; else ok; fi` under `set -euo pipefail`: a real vet finding makes `go vet` exit 1 → `pipefail` makes the pipeline non-zero → `else` prints "clean"; `bad` is unreachable. Revival: capture then match.
- `scripts/check_b103.sh:140-144` — the only execution (`go build ./cmd/skygate`) is fail-open twice: `cmd | grep -qE 'error'` with `pipefail` (`:31`) can SIGPIPE the build (141 → `else` → "clean"), and any compile failure whose text lacks the literal `error` (`undefined:`, `declared and not used`) also lands in `else`. Revival: `if ! "${GO_BIN}" build ./cmd/skygate >/tmp/b103.out 2>&1; then bad …; fi`.
- `scripts/check_b186.sh:167` — `J-build`: `if (go build ./internal/telegram/... 2>&1) | grep -q '^# '; then FAIL else ok` — under `pipefail` a failing build that trips SIGPIPE reports `ok`, and any failure whose first line is not `# ` reports `ok`. Revival: capture and test emptiness.
- `scripts/check_b188_2.sh:209-220` — contract M (build+vet) — the `else` prints `SKIP [M-build-vet] go not reachable from this shell` **even when `$GO_BIN` is set and the build/vet failed**, so M can never FAIL. Same defect at `scripts/check_b188_3.sh:144-149` (contract K). Revival: mirror `check_b188.sh:266-271` — FAIL when `GO_BIN` is non-empty, SKIP only when empty.
- `scripts/check_b188_3.sh:164-174` — contract M (the B188.3 integration tests, the only executing test of the useVia=false path) — **any non-zero exit is reported as `SKIP [M] PG not reachable (likely local dev)`**, so a genuine test failure can never redden the gate. Revival: a pre-flight PG probe for SKIP, then FAIL on a test failure.
- `scripts/check_b159.sh:135-142` — the `keys.never_expires` reference check — the `else` branch calls `ok`. Revival: `bad`.
- `scripts/check_b161.sh:375-388` — OIDC consent-screen i18n keys — **both** the `if` and the `else` call `ok`. Revival: fail when the keys are required.
- `scripts/check_b177.sh:80-87` — contract E (the pre-B177 "untag → add" order must not return) is **explicitly not implemented** — the block ends with "(no separate check; E is 'passed by D')". B (`:72`) and C (`:75`) only require both call strings anywhere in `nodeownership.go`; D (`:78`) only requires the B177 comment. Reverting the rename order keeps every contract green. Revival: awk the rename block and assert `AddTag` precedes `UntagNode` and that `UntagNode` sits in the AddTag error `else`.
- `scripts/check_b177.sh:66` / `scripts/check_b180.sh:80` — `check_eq "A" "yes" "yes"` / `check_eq "A" "0" "0"`: the PASS branch compares a literal with itself (the real decision was the `if` above). Revival: pass the computed variable.
- `scripts/check_b115.sh:118`, `scripts/check_b116.sh:175`, `scripts/check_b116.sh:181` — unconditional `pass` rows ("go build … covered by B1", "templates parse … covered by B7/B4"): decorative, can never fail. Revival: run the build/parse, or delete.
- `scripts/check_b134.sh:69-70` — `match_count` returns 0 when neither `grep -P` nor python exists, and contracts A/B then print PASS for a file never searched. Revival: treat "no matcher" as FAIL/SKIP.
- `scripts/check_b136.sh:123` (and `:158`, `:211`, `:230`, `:244`, `:268`, `:297`; `scripts/check_b137.sh:85`, `:124`, `:171`) — `file_check <path> || exit 0`: any missing expected file makes the script exit **0** → the gate shows PASS. Revival: `fail`.
- `scripts/check_b178.sh:135-145` — the B178.1 ordering contract (G2) is entirely inside `if [ -n "$ANNO_LINE" ] && [ -n "$GROUP_LINE" ]` with no `else`: a renamed/reformatted anchor silently skips it and `exit "$FAIL"` still returns 0. Revival: add an `else` that fails.
- `scripts/check_b182.sh:107-115` — contract E1 (annotator takes 3 args) is skipped with neither PASS nor FAIL when `func annotateRulesWithPrefs` is absent. Revival: `else check_eq "E1" "3-args" "annotator-missing"`.
- `scripts/check_b127.sh` end-to-end — see §C: the script already exits 1 today, and its FAIL lines are cut by `head -20`.
- `scripts/check_b122.sh:90-94` — "`ON_ERROR_STOP=1` present" is a `warn` and the script exits only on FAIL. Revival: `bad`.
- `scripts/check_b101.sh:120`, `scripts/check_b101.sh:133` — two `restore.sh` menu-text contracts use `warn()`; WARN never affects the exit (`:199-202` tests only FAIL). Revival: `bad()`.
- `scripts/check_b146.sh:138-148` vs `:192` — contract B.2 (the §6 status-log entry) is a `WARN` and the script ends with `exit "$FAIL"`, so it can never fail. Revival: increment FAIL.
- `scripts/check_b134.sh:124-132`/`:136-144` — running B131/B133 is skipped with a WARN when the sibling is missing/non-executable, so D/E can never fail. Revival: FAIL.
- `scripts/check_b129.sh:209-213` — the scheduler-file existence contract is `ok`/`warn` (never FAIL); it is the only contract that would notice a missing `internal/update/scheduler.go`. Revival: `bad`.
- `scripts/check_b179.sh:76-87`, `:90-101` — A/B/C/D all report PASS "no-block" when the evidence is absent: off-VM (or without passwordless sudo) both variables are empty → `echo "" | grep -q …` fails → `else` → PASS; C/D grep `/etc/iptables/rules.v4` with `2>/dev/null` and no `[ -r ]` → a missing file is "no block". Revival: explicit `SKIP` when the evidence source is unavailable.
- `scripts/check_b191.sh:211-218`, `:226-234` — contracts C/D print `SKIP` and then `exit 0`, so E–H (including the OIDC discovery/JWKS/authorize/token/userinfo probe) never run on a host without a local tailscale CLI and the gate reports PASS. Revival: run the dependency-free contracts before the tailscale gate.
- `scripts/check_b191.sh:96-114` — `cleanup()` calls `ok` after `|| true` for `nodes delete`, `preauthkeys expire` and `docker rm -f`, so every cleanup outcome is a PASS; `:178-183` also records `ok "preauth key created (ID lookup failed — cleanup will skip expire)"` when the key ID could not be resolved (orphan keys stay on the tailnet). Revival: `bad` (or a counted WARN).
- `scripts/check_b124.sh:144-148` vs `:164-168` — contracts E and F assert the **same** literal `if and .IsNewer (not .DevBuild)`, which occurs twice in `admin/update.html` (`:86` banner, `:148` Apply form); each is satisfied by the other's line, so a lost banner gate or a lost Apply gate is invisible. Revival: anchor E to `grep -B2 'banner_new_version'`, F to `grep -B2 'action="/admin/update/apply"'`.
- `scripts/check_b195.sh:49-53` — the first half of contract C re-asserts `CREATE TABLE IF NOT EXISTS cluster_database`, already asserted by B at `:40`; it cannot fail while B passes. Revival: make C assert "every `CREATE TABLE` carries `IF NOT EXISTS`".
- `scripts/check_b166.sh:135`/`:141` — `x=$(grep -A5 … | grep -c 'Category:.*"headscale"')` under `set -euo pipefail`: on 0 matches the assignment aborts the script **before** the `bad` at `:139`/`:145`, so contract D's second half never runs and the FAIL line is never printed (the failing check shows only PASS lines). Revival: `|| true` + `${x:-0}`.
- `scripts/check_b169.sh:159-165` — `RU_COUNT=$(awk … | grep -cE …)` under `set -euo pipefail`: a 0-count half aborts before the `bad` at `:164`, so the i18n-parity diagnostic is dead and contract D never runs. Revival: `|| true` + `${VAR:-0}`.

### B. Cannot fail for the reason claimed (satisfied by comments, unrelated text, or the wrong evidence)

- `scripts/check_b188.sh:138-150` — contracts B/C/D claim the three POST handlers call `NormalizeExitNodeTag`; that string now occurs **only in doc comments** (`internal/feature/my/device_exit_pref.go:51`, `:58`; `exit_nodes.go:110`, `:114`) — the handlers call `db.ResolveExitNodeTag` (`device_exit_pref.go:97`, `:177`; `exit_nodes.go:144`). Comment-only match. Revival: pin `db.ResolveExitNodeTag` inside each handler body.
- `scripts/check_b188.sh:206-211` — K/L only require `.DevTag` to appear in `user/devices.html` and `admin/devices.html`, but the ghost fallback survives in both (`or .DevTag (printf "tag:exit-%s" (tolower .Hostname))` at `:487` and `:372`; admin also matches `.DevTagMap` at `:304`). Only J2 (`:195-199`) checks the fallback, and only for `user/exit_nodes.html`, where it is gone. Revival: run J2's `printf "tag:exit-%s"` absence check across all three templates.
- `scripts/check_b188.sh:230-232` — contract P (`grep -A1 'via_enabled = 1' migrations_pg.go | grep -c 'user_exit_node_prefs\|device_exit_node_prefs'` ≥2) is satisfied by the unrelated migrateV047PG statements at `migrations_pg.go:892`/`:895`; deleting migrateV061PG's own clause leaves the count unchanged. Revival: awk the `migrateV061PG` body.
- `scripts/check_b185.sh:110` — contract B counts `--advertise-tags=` in `entrypoint.sh`, but that substring also appears in the **comment at `entrypoint.sh:124`** while the real flag is at `:213`; removing the flag leaves B green (the exact B185(1) regression). Revival: scope the grep to the `tailscale up` invocation. `:118` (`cdn:`) is 7/8 comments; `:176` counts `packets received` in ping's statistics line, so **100% packet loss still passes**.
- `scripts/check_b186.sh:86` — `B-fallback` (`fall.*back.*sendMessage`) is satisfied by the log line `internal/telegram/rich.go:286`; deleting the actual fallback call passes. `:120` (`sendRichMessage`) is 6/7 comments/strings; `:153-154` pins a test that is itself a source-text grep.
- `scripts/check_b187.sh:73` — the "no `?` placeholder" contract excludes only the single literal `SELECT username FROM portal_users WHERE id = ?`; any other SQLite-style placeholder (the bug class B187 fixed) is invisible. Revival: scan for `= ?` / `?,` markers.
- `scripts/check_b183.sh:133-134` — contract F requires exactly one `DELETE FROM device_rules` in the whole of `migrations_pg.go`; an unrelated DELETE in another migration makes a B183 contract fail.
- `scripts/check_b184.sh:117` — `F-resolved-key` counts `ResolvedKeyForTuple` in `form_admin.go`, where the only occurrence is the doc comment at `:193` (the live consumer is `LookupResolvedForDomain` at `:221`). Revival: count the DOMAIN-path consumer.
- `scripts/check_b188_2.sh:185-190` — `G` counts `ExitNodeID string` in `internal/db/device_rules.go`, which also matches `DomainRule` (`:102`) and `SubnetIPRule` (`:113`); `H` counts `exit_node_id` in `queries.go` (19 hits) so it passes even if `qSelectEnabledACLEntries` loses the column. `:197` (`J`) is comment-satisfiable and names a `TestB1882` that does not exist.
- `scripts/check_b132.sh:73-74` — `a_loop_call` and `a_pernode_call` are the **same** expression (`grep -cE 'syncOneExitNode\(s\.HS'`); the message claims "both call sites" but only one is asserted. `:143-151` — `miss_ru` is initialised and never used, so the "RU+EN" claim rests on a file-total count.
- `scripts/check_b149.sh:170-179` — count-based RU/EN parity accepts an untranslated value: `internal/i18n/catalog_admin.go:374-375` (RU) are the identical English strings as `:1420-1421` (EN) for `ha.title`/`ha.subtitle`. The same count-parity pattern is at `check_b165.sh:151-159`, `check_b171.sh:429-434`, `check_b173.sh:178-187`, `check_b176.sh:154-191`, and — passing with an **RU-only** key — `check_b196.sh:76-85`, `check_b197.sh:84-91`, `check_b1981.sh:103-111`.
- `scripts/check_b147.sh:244` — "CertSyncInterval default = 30s" is `grep -q '30\*time.Second' internal/config/config.go`; the file has that literal at `config.go:600` (`StaggerInterval`) and `:623` (`SidecarSyncPeriod`), so changing/removing the `CertSyncInterval` default (`:717`) still passes. Revival: `grep -q 'CertSyncInterval:.*30\*time.Second'`.
- `scripts/check_b145.sh:120-125` — provider-name matching with a leading `(^|\s)` is satisfied by prose in `provider_build.go`; deleting the real `case` arms stays green. `:174` (`SKYGATE_HA_MISSED_THRESHOLD.*3`) also matches a default like `30`.
- `scripts/check_b142.sh:217-225` — H claims OK **and** Fail handlers write `last_verify_archive`/`last_verify_error`, but `h_fail_arch`/`h_fail_err` (`:220-221`) are byte-identical greps to `h_ok_arch`/`h_ok_err` (`:218-219`). Revival: scope to `runBackupVerifyFail`.
- `scripts/check_b167.sh:72-73` — "has `func RunSync`" is satisfied by `func RunSyncCtx` at `internal/oidc/sync.go:214`, so deleting `func RunSync` (`:204`) stays green. Revival: `grep -qE '^func RunSync\('`.
- `scripts/check_b175.sh:85` — A.3 greps the whole file; the identical guard exists at `internal/nodeownership/nodeownership.go:279` and `:828`, so deleting the Strategy-E guard at `:92` stays green. Revival: awk-scope `matchOIDCStrategy`.
- `scripts/check_b172.sh:195` — B.2 claims "all 5 case categories" but is one `grep -qE` with five OR'd alternatives → a single `"empty"` satisfies it. `:226` (C.1) matches a `t.Logf` at `internal/oidc/e2e_test.go:314`; `:132-143` (A.8) cannot match the two-line markup and its fallback is a substring.
- `scripts/check_b166.sh:56-60` — the idempotency proof latches `in_defer=1` on **any** `defer func() {` in `system_tests.go` and never resets; `:94-99` greps four gRPC wording strings as free text (one is the `Errorf` message itself).
- `scripts/check_b170.sh:354-388` — D.2–D.8 pin test **names** and literals rather than assertions; `:162` ("wired up") is satisfied by a comment near the `if`; `:365` uses `grep -q '""'`.
- `scripts/check_b171.sh:97-107` — `awk_grep_match()` is defined and documented as the pipefail fix but **never called**; every site it was written for still uses the inline `awk | grep -q`. `:429-434` infers "one RU + one EN" from a total count; `:537` pins a local-variable message string.
- `scripts/check_b168.sh:101`/`:111`/`:123`/`:141` — the setup script's behaviour is pinned by its own **echoed messages** (including a "validate BEFORE the .env write" ordering that is never checked) and the audit token anywhere in the file.
- `scripts/check_b169.sh:46` — A.2 accepts the bare alternative `forbidden`, which matches `http.Error(w, "forbidden", 403)` (`internal/feature/admin/devices.go:1068`) or any comment; `:69`/`:83`/`:102` fallbacks accept any `devicedelete.Delete(` mention in the file.
- `scripts/check_b152.sh:62-63`, `:73`, `:83`, `:102`, `:110` — A.4/A.5/A.6/A.8/A.9 are satisfied by comments/log strings in `deploy/scripts/bootstrap_standby.sh` ("already bootstrapped", `/healthz` log lines, the `ha/deploy|deploy/` alternation matching bare `deploy/`), so the idempotency guard, the healthz poll, the psql chain check and the audit INSERT can be deleted.
- `scripts/check_b153.sh:112`, `:135-136`, `:145-146` — B.3's "does NOT write to the DB" only excludes the exact `psql … -c … INSERT` spelling; C.1's `seq 1 60` also matches `seq 1 600` (two sites); C.2's `seq 1 90` matches `seq 1 900`; "5 steps in the right order" is five independent greps.
- `scripts/check_b174.sh:130-131` — A.6 ("readSession handles the UserLookup error") is two file-wide greps also satisfied by a comment (`internal/oidc/authorize.go:286`) and the assignment (`:298-299`); `:164-173` counts `NewService` params from one line, so a multi-line signature yields a false FAIL and a comma can hide a dropped param.
- `scripts/check_b176.sh:102-108` — the "no stragglers" negative sweep reports 0 whether the code is clean **or** the regex stopped matching the construction shape; `:142-145` pins "the loop calls `deviceTagForRule`" as a whole-file string.
- `scripts/check_b102.sh:77-83` — "the test script mentions all 5 protocols" is `grep -qF "${proto}"` over the whole file (`local`/`s3`/`smb`/`nfs`/`sftp` occur in comments and function names).
- `scripts/check_b116.sh:72-74` — the `derp_relays` column loop greps the **whole** `migrations_pg.go`, where the same words occur in other migrations/comments; `:122` is unanchored and method-agnostic; `:109` matches comments/call chains.
- `scripts/check_b112.sh:25-31`, `:47-54` — "symbol removed" is `^(var|func) name`, so a re-added **method** or generic form slips through; `^type s3Client ` requires exactly one space.
- `scripts/check_b113.sh:57-63` — the bad-input needle loop prints `PASS: bad-input test cases …` unconditionally (even after `err`) and `'youtube.com'` is a prefix of `'youtube.com/32'`.
- `scripts/check_b110.sh:158-163` — `pass "shell script has all 4 flags"` prints even when `err` fired (exit code still non-zero, so it is a misleading PASS row); `:101-107`/`:111-117` whole-file greps are satisfied by comments.
- `scripts/check_b111.sh:78` — `perl -0777 -ne 'exit !(/isInfraNode.*?tag:exit-node/s)'` matches the doc comment at `internal/nodeownership/auto.go:820`, so deleting the real rule at `:834` stays green. `:95` (`tag:dev-infra-`) is comment-satisfied.
- `scripts/check_b114.sh:106-114` — "portable driver staging" pins the literal `docker exec skygate-skygate-1 python3`; the hardcoded compose name is exactly what would break elsewhere, and the text check cannot see it.
- `scripts/check_b115.sh:80-88` — the per-test loop never uses its variable; the `-ge 3` count is met by the helper definition (`system_tests_tailnet.go:167`) plus 2 of 3 call sites.
- `scripts/check_b118.sh:109` (+fallback `:115`) — the `"infra@"` ≥2-emit-sites contract falls back to accepting a single `"infra@` in a ±3-line window; it is effectively "≥1".
- `scripts/check_b122.sh:119-122` — contract C matches the v1.3.8 **comment** at `restore.sh:8-18` (`grep -B1 -A10 "do_pg_restore()"` hits the comment first and the window contains "skygate.env"); the real function body is never inspected.
- `scripts/check_b124.sh:171-175`, `:200-225` — only the exact pre-B129 token order is rejected; G greps test-table rows (a row with a wrong `want` passes).
- `scripts/check_b128.sh:113-132`, `:172-176` — D/E pin test-table strings (presence ≠ expectation); G is B129's form-action grep and says nothing about the 4-part compare. (The live compare IS run at `:150`.)
- `scripts/check_b133.sh:169-176` — contract F's condition omits the border-color requirement; `f_vercel`/`f_mint` are computed but only echoed in the FAIL text, so deleting the theme override (`static/css/themes.css:453`) still passes.
- `scripts/check_b135.sh:175-190` — contract G ("Manrope font option") asserts only a `DisplayFont` count; `:222-236` makes it skippable.
- `scripts/check_b125.sh:93-99`, `:192-197` — "V056 is new" is satisfied by any added V05x migration; the per-IP RowsAffected claim is warn-only and does not fire.
- `scripts/check_b158.sh:144-154`, `:179-186` — the stated <500KB bound is implemented as `<1024` KB without subtracting font-awesome; the template-balance counter misses `{{- if`/`{{- end}}` and ignores nesting.
- `scripts/check_b165.sh:168-174`, `:151-159` — the mobile-collapse contract is two untied whole-file greps; the 16-key i18n "RU+EN" claim is a total-occurrence count (2 RU + 0 EN passes).
- `scripts/check_b130.sh` — no dead contract found; the outcome gaps below apply.
- `scripts/check_b194.sh:108` — the FK contract is a grep of migration text that a comment satisfies. `scripts/check_b198.sh:66-68`, `:78-84` — the DeployStep interface and the "rollback chain" are method-name greps; a no-op `Rollback` and the documented STUB steps pass.

### C. Currently FAILing for the wrong reason (false red, hidden by `head -20`)

- `scripts/check_b131.sh:197-203` — contract E extracts CSS with `awk '/\[data-theme="linear"\] input/,/^}/' static/css/themes.css`. That range starts at `themes.css:604` and ends at the first `^}` — `themes.css:608`. The rule it wants (`background:var(--bg-elev);`) lives in a **separate** block at `themes.css:615-618`, so `e_new=0` → `bad "input background rule is wrong"` → the script exits 1 (`:253`) on every run, although the invariant it describes holds. Fix: widen the extraction (`sed -n '604,620p'`) or assert file-wide (`var(--bg-elev)` present ∧ `background:#1a1a1a` absent). Separation from B121 (which split the block at `:604-621`) is what broke it.
- `scripts/check_b127.sh:163-171` — for `R28`/`R29` the marker resolves to `verify_post_deploy.sh:1413`/`:1450` and the 60-line windows contain **no `json_field`** (the nearest uses are `:1155` and `:1673`), so both print `bad "R28/R29 does NOT use json_field"` and the script exits 1 (`:212`). R11–R18 pass first, so the two FAIL lines sit past the gate's 20-line cut and the operator sees a bare `FAIL B127` with no evidence. Fix: assert the remote-command contract those blocks actually use, or drop R28/R29 from the loop at `:169`.
- Consequence: the catalogue is not uniformly green. A permanently red contract (or two) is worse than a missing one — it trains the operator to ignore FAIL rows, and the `head -20` cap guarantees the reason is invisible.

### D. Spurious-FAIL hazards (documented TD-19 / SIGPIPE and `/tmp` traps)

- `cmd | grep -q` under `pipefail`: `scripts/check_b120.sh:129`, `check_b121.sh:177`, `check_b182.sh:164`, `check_b184.sh:146`, `check_b186.sh:167`, `:172`. Capture first, then match (`check_b183.sh:164-165` is the fixed reference).
- `go test … | tee /tmp/…` under `set -euo pipefail` (unwritable/non-sticky `/tmp` turns a green test red): `scripts/check_b148.sh:156`, `:257`, `check_b149.sh:155`, `check_b150.sh:178`. `check_b147.sh:142-151` documents this as the fixed pattern.
- `tar … | grep -q … && archive=…` (`scripts/check_b122.sh:168`): a SIGPIPE'd `tar` empties `archive` and silently downgrades the live archive probe to a WARN.
- DB-error-is-FAIL (rule 1: must SKIP): `scripts/check_b118.sh:235-239`, `:256-296`, `check_b119.sh:185-188`, `check_b126.sh` SSH probe, `check_b179.sh:109-114` (headscale merely stopped), `check_b187.sh:106-113` (no passwordless sudo).
- Environment false FAILs: `scripts/check_b144.sh:108` (`grep -cPzo`, Windows Git Bash — known trap #10); `scripts/check_b131.sh:155-171` (no python → contrast delta 0 → `bad`); `scripts/check_b162.sh:252` (`out=$([ -n "$GO_BIN" ] && go vet …)` returns 1 with an empty `GO_BIN` → `set -e` exits 1 = FAIL instead of SKIP).
- Data-drift false FAILs: `scripts/check_b183.sh:194` (`TOTAL -le 50` on emilia's subnet rows — the pre-B183 count was 102); `scripts/check_b184.sh:205` (discord.com pinned at exactly 0 resolved subnets); `scripts/check_b118.sh` (exactly 4 infra tags).

## Runtime invariants with no behavioural contract

Only `check_b167.sh` (executes `deploy/oidc-sync.sh --download-only`) and the live
sections of `check_b188*`/`check_b182`–`check_b187` reach a real dependency; everything
else is source text plus, sometimes, a package-level `go test` of pure helpers.

1. **A device tag actually reaching headscale** (`tag:dev-<user>-<host>` on the node,
   `tagOwners` permitting it, the REST/CLI write succeeding — especially on a native
   install). *User sees:* per-device rules match nothing and the device is blocked or
   unpinned while the DB claims the tag. *Covered only by:* `check_b177.sh:72-78`
   (call strings; the ordering contract is unimplemented, §A), `check_b176.sh:68-108`
   (ToLower call shape), `check_b175.sh:85` (dead grep), `check_b188.sh` B/C/D
   (comment-only). *Proposed behavioural assertion:* after the tag-reconcile pass, assert
   `GET /api/v1/node` (or `headscale nodes list -o json`) carries the tag **and**
   `GET /api/v1/policy` carries the `tagOwners` entry; FAIL when the DB row says tagged
   and headscale disagrees.
2. **Save-a-rule → apply-the-policy** (the operator's "exit rules stopped working" /
   "the applied ACL went stale" incidents). *User sees:* the UI lists the rule as active
   while the live policy has no grant (or an old `via=`), so traffic is silently dropped.
   *Covered only by:* `check_b188.sh:313-334` and `check_b188_2.sh:222-394`, which read
   the live policy for **fixed fixtures** (user 6 / device 29) and never create a rule and
   re-read the policy; no contract compares generated vs applied policy (that is B276,
   outside the slice). *Proposed behavioural assertion:* in the live section, insert a
   rule for a throwaway device, run the apply path (`skygate acl-apply`), assert the
   policy gained exactly the expected `{src,dst,via}` grant, delete the rule and assert
   the grant disappears; plus a generated==applied comparator after a tick.
3. **A scheduler tick actually firing.** `check_b130.sh` (auto-update), `check_b142.sh`
   (backup verify), `check_b143.sh` (smoke-mesh cleanup), `check_b147.sh` (certsync),
   `check_b154.sh` (token auto-rotate), `check_b156.sh` (keynotify) test pure helpers
   only; none asserts that a tick reads its `global_settings` keys, performs the side
   effect, and writes `*_last_run`. *User sees:* certificates/keys never renew, backup
   verification never alerts, cruft accumulates — silently. *Proposed behavioural
   assertion:* drive the exported `tick`/`runScheduled` against SQLite with a fake clock
   and assert the DB mutation plus the notifier call.
4. **The device-delete cascade** (headscale node + `node_owner_map` +
   `device_exit_node_prefs` + `device_rules` + ACL regen + cache + audit) —
   `check_b162.sh`, `check_b169.sh`, `check_b171.sh` are greps, and B162's only execution
   cannot fail the gate (§A). *User sees:* a deleted device keeps receiving rules or a
   stale ACL keeps granting it. *Proposed behavioural assertion:* call
   `devicedelete.Delete` with a test DB and a stubbed headscale client; assert the node is
   gone, all three tables are empty for that device, `AuditFn` saw `device_deleted` with
   counts, and the ACL-regen hook ran.
5. **Per-key RU/EN translation parity** for the keys added by B136/B137/B149/B165/B171/
   B173/B176/B182/B194–B198.1. Count-based checks accept an RU-only key or an untranslated
   RU value (proved: the RU `ha.title`/`ha.subtitle` at `catalog_admin.go:374-375` are the
   English text). B4 (outside the slice) compares key **sets**, not values. *User sees:* a
   Russian UI that silently shows English strings. *Proposed behavioural assertion:* split
   each `catalog_*.go` at its `en` map, assert equal key sets and that every non-empty RU
   value differs from the EN value (with an explicit allow-list).
6. **The front-end outcomes behind the CSS/JS contracts** (B105–B109, B120/B121,
   B131/B133–B137, B163, B165, B173/B173.1): "the toggle is hidden on mobile", "the swatch
   click fills the field", "the FAIL output is collapsed", "the login button shows a
   spinner". No script in the slice renders a page or evaluates JS; a `{{if}}`-wrapped or
   syntactically broken script passes everything. *Proposed behavioural assertion:* a
   small browser smoke (or, minimally, `go test` cases that execute the template with
   fixture data and assert the rendered fragment) covering one computed style and one
   click→DOM change per contract.
7. **Static assets + `/static/*` cache policy** and **the `/my/tokens` token-UX
   feature** — both are *claimed* by misregistered gate contracts (B152 at
   `verify_pre_deploy.sh:3300`, B153 at `:3306`) and asserted nowhere (Notes).
   *User sees:* 404 fonts on every navigation / a token page with no renew affordance.
   *Proposed behavioural assertion:* `curl -sI` the five font-awesome files plus one
   hashed and one versioned asset, asserting 200 + the documented `Cache-Control`; and a
   template-execution test for `my_tokens.html` with an expiring token asserting the
   Renew form and the badge.

## Notes

- **Duplicate IDs, two different contracts (verified).** The gate registers
  `run_check "B152" …` twice (`:3300`, `:3341`) and both run `scripts/check_b152.sh`,
  whose header (`check_b152.sh:2`) implements *bootstrap_standby* (`:3341`). `B153` is
  registered twice (`:3306`, `:3344`), both run `scripts/check_b153.sh`, which implements
  *dr_drill* (`check_b153.sh:2`). So the `:3300` "static asset sanity (font-awesome …
  Cache-Control on /static/*)" and the `:3306` "personal API token UX (/my/tokens)"
  contracts execute unrelated checks — and nothing else in `scripts/` checks them (a grep
  for `font-awesome`, `max-age=31536000`, `my/tokens` finds only `check_b158.sh` comments,
  `check_b135.sh` Manrope fonts, and `check_b154.sh:6`). Each duplicate ID also prints two
  PASS rows for one script.
- **IDs in range with a script but no gate entry:** `B189` (`check_b189.sh` — DERP health
  dashboard source/migration/routes/CLI/build+tests), `B190` (`check_b190.sh` — B188.3
  fixture pollution; also SKIPs+exits 0 without a DSN), `B199` (`check_b199.sh` —
  `/admin/cluster`), `B104` (removed by B138) and the orphan `scripts/check_b161_4.sh`,
  which holds the only **live OIDC probe** in the tree (discovery, JWKS,
  `/oidc/authorize`, `/oidc/token`) and is referenced by nothing (`grep -r check_b161_4`
  matches only its own header). B161 in the gate is source greps plus an in-process
  `go test ./internal/oidc/`.
- **`grep -c … || echo 0` is a recurring bug class**: with zero matches `grep -c` prints
  `0` *and* exits 1, so `|| echo 0` yields `"0\n0"`; the following `[ "$x" -lt N ]` errors
  (status 2 = false) and the PASS branch runs (`check_b111.sh:124`, `:136`;
  `check_b177.sh:59` produces malformed FAIL diagnostics; `check_b188_2.sh:150` makes the
  "B188.2 shape" arm unreachable). `check_b178.sh:83-92` is the fixed reference.
- **Live sections are gated on a hardcoded path** `[ -d /home/skyadmin/skygate ]` (B183,
  B184, B185, B186, B187, B188, B188_2, B188_3, B191, B179; B122 uses
  `/home/skyadmin/skygate-backups`). On any host whose checkout/data dir differs, every
  live contract silently SKIPs and the gate stays green — the liveness analogue of
  AGENTS trap #11.
- **`head -20` hides the reason for long checks**: B133 (~45 output lines), B136, B137,
  B142, B143, B144, B149, B161, B171, B188_2, B194, B198.1 print their summary and their
  later FAILs past line 20 — and B127/B131 (which fail today) are exactly this case.
- **`out=$(failing cmd)` under `set -e`** makes the following `bad "… output: …"` lines
  unreachable and hides why a check died: `check_b159.sh:256`/`:262`, `check_b160.sh:179-190`/`:200`,
  `check_b161.sh:56`/`:409`/`:529`/`:579`/`:228`/`:234`, `check_b162.sh:252`,
  `check_b163.sh:154-164`, `check_b164.sh:185-195`, `check_b165.sh:187`/`:193`. Revival:
  `out=$(cmd 2>&1) || true` then branch on `$?`/emptiness.
- **`B138`/`B139` are inline commands, not scripts** — B138 is two greps in a `bash -c`
  (`:3277-3281`); B139 is one `test -f` plus five `! test -f` (`:3283`). Both structural.
- **What the slice does well:** `check_b167.sh:209` genuinely runs
  `deploy/oidc-sync.sh --download-only` and asserts its JSON; `check_b188.sh`
  (`:278-334`) and `check_b188_2.sh:222-394` read the live headscale policy and assert
  `via=` pins (the only place in the slice where the operator's reported ACL regression
  would be caught); `check_b123.sh:258`, `check_b125.sh:225`, `check_b128.sh:150`,
  `check_b130.sh:150`, `check_b140-144`, `check_b147.sh:152`, `check_b172.sh:301-310`,
  `check_b174.sh:297`, `check_b175.sh:185`, `check_b176.sh:229`, `check_b178.sh:204`
  run real Go tests whose filters all match existing test functions (no "no tests to run"
  holes); `check_b183.sh:164` uses the safe capture-then-match pattern the others lack.
