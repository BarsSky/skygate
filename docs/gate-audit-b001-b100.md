# Slice B1–B100 (98 run_check entries, 10 scripts)

Scope: every `run_check` / `run_check_slow` entry in `scripts/verify_pre_deploy.sh` whose ID is
B1–B100 inclusive, plus their letter sub-IDs (`B68a` is the only one present). 98 entries.
IDs above B100 are out of scope. B28, B74 and B75 do not exist in the file.

Gate mechanics that frame every verdict below (read, not inferred):
- `run_check` decides purely on the child's **exit status** (`scripts/verify_pre_deploy.sh:190`) and prints
  the **first 20 lines** of the captured output on FAIL (`:195`).
- The gate sets only `set -u` (`:21`). There is **no `pipefail`** anywhere in the gate, so a
  `cmd | grep -q` pipeline's status is the last stage's, and `grep`'s own error (missing file, bad
  option) is silently swallowed.
- `bash -c "$cmd"` (`:171`/`:177`/`:181`) means the command's exit status is its last statement's.

**Headline finding.** Twenty-five entries — the whole B59–B90 band minus B82–B85/B87/B88 — build
their contract as `f=/tmp/bNN.sh; printf "%s" "<chain>" > "$f" && bash "$f"; rm -f "$f"`. The
status of that command list is the status of **`rm -f`**, which is always 0. Every one of those 25
checks therefore reports **PASS unconditionally**, whatever the generated script does. This is the
single mechanical reason the operator saw a green gate while exit rules, device tags and the applied
ACL policy broke.

## Table

| ID | script | style | catches | CANNOT catch |
|----|--------|-------|---------|--------------|
| B1 | INLINE | behavioural | `go test ./...` exit status | DB/headscale subtests (they `t.Skip` to rc 0) |
| B2 | INLINE | structural | `go vet ./...` findings | any runtime outcome |
| B3 | INLINE | behavioural | `./cmd/skygate` compiles | the binary ever running |
| B4 | INLINE | behavioural | RU/EN key-set parity computed | render-time key/arg misuse |
| B5 | INLINE | behavioural | **nothing — filter matches no test** | v0.47 migration idempotency, entirely |
| B6 | INLINE | behavioural | ACL pure-helper assertions | DB-generated ACL; PG tests SKIP |
| B7 | INLINE | behavioural | every embedded template parses | data-dependent render errors |
| B8 | INLINE | live-only | VM smoke RU+EN (VM only) | everything off-VM (SKIP) |
| B9 | INLINE | structural | "v0.28.5" still in changelog text | current release notes |
| B10 | INLINE | structural | tracked `*.env/*.key/*.pem` names | untracked secrets; git error ⇒ PASS |
| B11 | INLINE | mixed | no destructive DDL in `migrations_v*.go` | `migrations_pg.go` (41 migrate fns) |
| B12 | INLINE | behavioural | pgmigrate SQL-form unit tests | DDL actually executed on PG |
| B13 | INLINE | structural | "MSYSTEM" substring in the hook | that the hook *uses* it (comment suffices) |
| B14 | INLINE | structural | wrapper exists + `bash -n` parses | the label lookup working |
| B15 | INLINE | mixed | `parentDomain` greps + pkg tests | the `api.go` clause is comment-only |
| B16 | INLINE | mixed | CDN helper greps + pkg tests | live CDN/DNS expansion behaviour |
| B17 | INLINE | structural | guard symbol + test NAMES exist | the guard logic; no test is run |
| B18 | INLINE | mixed | PG files/symbols + build + vet | migrations running against real PG |
| B19 | INLINE | structural | `GenerateACL` + `/admin/acls` substrings | generation executing (comments satisfy) |
| B20 | `scripts/check_b20.sh` | structural | `--force` on each literal fetch line | fetch really executed; other call shapes |
| B21 | INLINE | structural | filter greps (real) | `go test -run TestShouldInclude` matches nothing |
| B22 | INLINE | structural | Dockerfile/entrypoint text | image builds/starts; comments satisfy |
| B23 | INLINE | structural | CI go-version == go.mod | CI actually using that Go |
| B24 | INLINE | structural | no stale root wrapper files | nothing runtime |
| B25 | INLINE | structural | Caddy off in 3 files | profile actually honoured by compose |
| B26 | INLINE | structural | no gcc/musl/sqlite pkgs in Dockerfile | `CGO_ENABLED` in the entrypoint build |
| B27 | INLINE | structural | entrypoint build steps present | image actually builds/serves |
| B29 | INLINE | structural | gate line present | `! grep -qE "^go …"` can never match |
| B30 | INLINE | structural | gate line present | `! grep -qE "^go …"` can never match |
| B31 | INLINE | structural | pool calls exist | the VALUES; `SetMaxIdleConns(0)` passes |
| B32 | INLINE | structural | compose env-line shapes | Tailscale skip actually happening |
| B33 | INLINE | structural | probe text in template | `! grep -qE "^\s*wget"` can never match |
| B34 | INLINE | structural | index NAME in `migrations_pg.go` | the 6-column key; DDL at runtime |
| B35 | INLINE | structural | route string + handler symbol | remove button actually deleting |
| B36 | INLINE | structural | checksum helpers + V049PG + test name | checksum mismatch being detected |
| B37 | INLINE | structural | schedule handler/route/key names | schedule persisting or running |
| B38 | INLINE | structural | ACL method names, sort call, table | ACL add/remove/fingerprint behaviour |
| B39 | INLINE | structural | route strings, handlers, template keys | page rendering at all |
| B40 | INLINE | structural | ≥6 `Name:` lines, 3 categories | the registered tests actually passing |
| B41 | INLINE | structural | route/handler/template/link symbols | `/admin/system_tests` run behaviour |
| B42 | INLINE | structural | migrateV050/051PG symbols in 2 files | migrations running, or in order |
| B43 | INLINE | structural | signature, ssh flags, helpers, mount | SSH actually reconfiguring the relay |
| B44 | INLINE | structural | OpenPostgres calls migrate + DDL text | fresh DB really getting the tables |
| B45 | INLINE | structural | six `define` names, dashed names absent | `renderBody` resolving at runtime |
| B46 | INLINE | structural | render-test file + function names | rendering without panic (test not run) |
| B47 | INLINE | structural | ≥1 `{{if $.LiveResults}}` in the file | that it sits inside `{{range .Tests}}` |
| B48 | INLINE | behavioural | real template body-name resolution | page behaviour with real data/DB |
| B49 | INLINE | structural | wrong 2 files; `-P` may abort | the short form in `admin/exit_nodes.html:714` |
| B50 | INLINE | structural | `.table-wrap` div present | the wide table actually being wrapped |
| B51 | INLINE | structural | env var names read; no `const backupDir` | dir existing/writable at runtime |
| B52 | INLINE | structural | no `style="…{{` in `admin/update.html` | file deletion (negative ⇒ PASS) |
| B53 | INLINE | structural | handlers, switch cases, template+i18n keys | egress apply really SSHing |
| B54 | INLINE | structural | `placeholdersList(2)` + file presence | generated SQL valid on PostgreSQL |
| B55 | INLINE | structural | handlers/cases/template/i18n/env names | tailscale actually starting/stopping |
| B56 | INLINE | structural | config fields, env vars, owner plumbing | `checker.go:130` hardcoded org survives |
| B57 | INLINE | structural | legacy SQLite strings absent | help text being accurate |
| B58 | INLINE | structural | generate-key symbols + i18n keys | key minting + authkey write working |
| B59 | INLINE | structural | **nothing — wrapper always exits 0** | every one of its 14 clauses |
| B60 | INLINE | structural | **nothing — wrapper always exits 0** | every one of its 18 clauses |
| B61 | INLINE | mixed | **nothing — wrapper always exits 0** | `--help`/`--bad-flag` rc, SSH_HOST default |
| B62 | INLINE | structural | **nothing — wrapper always exits 0** | DB-first resolution order |
| B63 | INLINE | structural | **nothing — wrapper always exits 0** | `PlaceholderAt` dispatch; stale paths |
| B64 | INLINE | structural | **nothing — wrapper always exits 0** | tagOwners containing via+dev tags |
| B65 | INLINE | structural | **nothing — wrapper always exits 0** | restart actually writing `.env` |
| B66 | INLINE | structural | **nothing — wrapper always exits 0** | mismatch actually blocking the rule |
| B67 | INLINE | structural | **nothing — wrapper always exits 0** | `?device=` filter rows |
| B68 | INLINE | structural | **nothing — wrapper always exits 0** | autoupdater running; grants present |
| B68a | INLINE | structural | **nothing — wrapper always exits 0** | v0.52 data-repair migration |
| B69 | INLINE | structural | **nothing — wrapper always exits 0** | rename actually untagging+retagging |
| B70 | INLINE | structural | **nothing — wrapper always exits 0** | migrate phase actually executing |
| B71 | INLINE | structural | **nothing — wrapper always exits 0** | that curl was not reintroduced |
| B72 | INLINE | structural | **nothing — wrapper always exits 0** | banner rendering; pinned strings are stale |
| B73 | INLINE | structural | **nothing — wrapper always exits 0** | leaked org absent from layout.html |
| B76 | INLINE | structural | **nothing — wrapper always exits 0** | pre-update tags actually normalized |
| B77 | INLINE | structural | **nothing — wrapper always exits 0** | new devices actually getting tagged |
| B78 | INLINE | structural | **nothing — wrapper always exits 0** | per-row status icons rendering |
| B79 | INLINE | structural | **nothing — wrapper always exits 0** | placeholder numbering correctness |
| B80 | INLINE | structural | **nothing — wrapper always exits 0** | compose interpolation working |
| B81 | INLINE | structural | **nothing — wrapper always exits 0** | helper actually called by sync |
| B82 | INLINE | structural | 3 literals present in `exit_nodes.go` | filter logic (literals also in comments) |
| B83 | INLINE | structural | `SSHKeyPath: sshKeyPath,` in handlers.go | later overwrite; runtime assignment |
| B84 | INLINE | structural | name appears in telegram.go + sync.go | `sync.go` hit is a comment |
| B85 | INLINE | mixed | migration/SQL/UI/i18n names + 3 PG tests | port-suffix logic (tests SKIP without PG) |
| B86 | INLINE | structural | **nothing — wrapper always exits 0** | everything (generated script is invalid bash) |
| B87 | INLINE | mixed | AddTag union via 4 real httptest tests | handler switching back to `TagNode` |
| B88 | INLINE | structural | 5 `TestRegistry` `Name:` strings | the SQL bodies under them |
| B89 | INLINE | structural | **nothing — wrapper always exits 0** | everything (`$GO` is empty in the child) |
| B90 | INLINE | structural | **nothing — wrapper always exits 0** | everything (`$GO` is empty in the child) |
| B91 | `scripts/check_b91.sh` | structural | compose/entrypoint text, YAML structure, `bash -n` | skygate's real restart policy |
| B92 | `scripts/check_b92.sh` | mixed | availability probe semantics via httptest | runtime wiring; D-item semantics |
| B93 | `scripts/check_b93.sh` | structural | V054/infra symbol text; `t.Skip` markers | that any test runs; build output |
| B94 | `scripts/check_b94.sh` | mixed | D1–D8 text presence + availability tests | D5 `/readyz` DB-only semantics |
| B95 | `scripts/check_b95.sh` | structural | whole-tree build/vet/staticcheck; tombstones | nil-deref ordering; `cmd/` debt |
| B96 | `scripts/check_b96.sh` | structural | `layout.html` text: 10 sections, 22 hrefs, i18n | that a section/open-flag renders correctly |
| B97 | `scripts/check_b97.sh` | structural | `themes.css` text: 768px, transforms, 44px | that the drawer works in a browser |
| B98 | `scripts/check_b98.sh` | mixed | speed/availability test defs run correctly | test-count guard degrades silently |
| B99 | INLINE | structural | `bash` in Dockerfile apk add + `exec "bash"` | which stage; a real backup run |
| B100 | `scripts/check_b100.sh` | mixed | S3 helpers/Validate/detectProtocol via `go test` | audit-row contract (WARN); mount adjacency |

Style totals: **behavioural 8** (B1, B3, B4, B5, B6, B7, B12, B48), **live-only 1** (B8),
**mixed 11** (B11, B15, B16, B18, B61, B85, B87, B92, B94, B98, B100),
**structural 78**. Only 9 of 98 entries ever execute product code, and one of those (B5) executes
nothing at all.

## Dead or near-dead contracts

### A. Unconditionally green: the `/tmp/bNN.sh` wrapper (25 entries)

`scripts/verify_pre_deploy.sh:1514, 1534, 1553, 1571, 1600, 1631, 1660, 1683, 1702, 1732, 1761,
1809, 1854, 1880, 1907, 1939, 1969, 2013, 2063, 2104, 2146, 2208, 2441, 2590, 2618` —
each entry's command is `f=/tmp/bNN.sh; printf "%s" "<chain>" > "$f" && bash "$f"; rm -f "$f"`
(B86 uses the escaped-quote variant `> \"\$f\" && bash \"\$f\"; rm -f \"\$f\"` at `:2441`; B89/B90
close the same shape at `:2590`/`:2618`).
**Why it cannot fail:** the command list's status is the last statement's. `rm -f` returns 0 even when
the file does not exist, so `bash -c` returns 0 and `run_check` prints PASS (`:190-192`) regardless of
what the generated script did. The `printf … && bash "$f"` group's status is discarded, and stderr from
a syntax error or a `grep` failure is only ever stored in `$out`, which the PASS path never prints.
**Affected IDs:** B59, B60, B61, B62, B63, B64, B65, B66, B67, B68, B68a, B69, B70, B71, B72, B73,
B76, B77, B78, B79, B80, B81, B86, B89, B90.
**Smallest revival (one change fixes all 25):** make the wrapper propagate the status —
`f=/tmp/bNN.sh; printf "%s" "…" > "$f" && bash "$f"; rc=$?; rm -f "$f"; exit $rc`
— or, better, delete the wrapper and run the chain inline (the chain is already valid `bash -c` text).
Two entries are green for a second, independent reason and need their own fix:
- **`scripts/verify_pre_deploy.sh:2441` (B86)** — the generated script is not valid bash: the pattern
  `TS_LOGIN_SERVER:-${SKYGATE_TS_LOGIN_SERVER` opens a `${` that is never closed, so the child dies
  with a syntax error before running any grep. Revival: quote the patterns —
  `grep -qF 'TS_LOGIN_SERVER:-${SKYGATE_TS_LOGIN_SERVER' entrypoint.sh` (both strings exist at
  `entrypoint.sh:113` and `:116`).
- **`scripts/verify_pre_deploy.sh:2589` and `:2617` (B89/B90)** — `$GO` is expanded by the *child*
  shell, where it is unset (`GO` is a plain gate variable, `:40-64`, never exported), so the
  intended `go test` / `go build` line becomes a bare `test …` / `build …`. `export GO` next to
  `:82` is the one-line fix.

### B. Test filters that match no test (silent `ok … [no tests to run]`, rc 0)

- `scripts/verify_pre_deploy.sh:236` — B5 asserts "migration v0.47 idempotent (3 tests)" via
  `go test ./internal/db/ -run TestMigrateV047`. No function by that name exists anywhere in the
  repo (verified: `grep 'func TestMigrateV047'` ⇒ 0 hits); the neighbouring
  `TestMigrationsV047_SkipPendingPGRewrite` (`internal/db/migrations_v0.47_test.go:14`) is a
  `t.Skip` stub. Go prints `ok skygate/internal/db … [no tests to run]` and exits 0.
  **Revival:** `-run 'TestMigrationsV047|TestPGMigrationIdempotency'` **and** a real body in the stub.
- `scripts/verify_pre_deploy.sh:520` — B21's `go test -run 'TestShouldInclude'` clause. No identifier
  containing `ShouldInclude` exists (the production symbol is lowercase `shouldIncludeAsExitServer`,
  `internal/feature/admin/exit_nodes.go:1767`). **Revival:** add the `TestShouldInclude_*` test the
  filter already looks for, or delete the clause.
- `scripts/check_b96.sh:82` — `"$GO" test -run "TestB96_" ./internal/handlers/` with no test-name
  guard; a rename yields `[no tests to run]`, rc 0.
  **Revival:** `"$GO" test -list 'TestB96_' ./internal/handlers/ | grep -q TestB96_`.
- `scripts/check_b97.sh:66` — same shape for `-run "TestB97_"`. **Revival:** `-list` guard as above.

### C. Assertions satisfied by a comment, or by text that survives deletion of the code

- `scripts/verify_pre_deploy.sh:343` — B15's `grep -q parent_domain internal/feature/exit_rules/api.go`
  is satisfied only by the comment at `internal/feature/exit_rules/api.go:164-168`; the executable code
  uses the local `apiParent`. Reverting the fix keeps the gate green.
  **Revival:** `grep -qF 'apiParent = rl.TargetValue' internal/feature/exit_rules/api.go`.
- `scripts/verify_pre_deploy.sh:305` — B13's `grep -q 'MSYSTEM' .githooks/pre-push` matches the
  explanation comment at `.githooks/pre-push:65`; deleting the real detection at `:70` keeps it green.
  **Revival:** `grep -qF 'if [ -n "${MSYSTEM:-}" ]; then' .githooks/pre-push`.
- `scripts/verify_pre_deploy.sh:318` — B14's label grep matches the comment at
  `deploy/skygate-cli.sh:22`; the real filter is `:54`.
  **Revival:** `grep -qF 'label=com.docker.compose.service=skygate"' deploy/skygate-cli.sh`.
- `scripts/verify_pre_deploy.sh:474` — B19's `GenerateACL` / `GenerateACLWithVia` / `/admin/acls`
  all appear in comments (`internal/acl/acl.go:9,157`, `cmd/skygate/main.go:841,1485`).
  **Revival:** anchor to declarations/registration (`^func GenerateACL\(`, `mux.Handle("GET /admin/acls"`).
- `scripts/verify_pre_deploy.sh:559` and `:705` — B22/B27 grep `"go mod download"` / `"go build"` in
  `entrypoint.sh`, which already appear in the header comment (`entrypoint.sh:12-13`); the real steps
  are `:387` and `:416`.
  **Revival:** `grep -qE '^[[:space:]]*go mod download' entrypoint.sh`.
- `scripts/verify_pre_deploy.sh:936` — B34 pins only the index *name* (`device_rules_natural_key_uniq`),
  which is also in comments (`internal/db/migrations_pg.go:1108,1326,1357`), and never the column list.
  **Revival:** `grep -A2 'CREATE UNIQUE INDEX IF NOT EXISTS device_rules_natural_key_uniq' internal/db/migrations_pg.go | grep -qF 'user_id, device_id, exit_node_id, target_type, target_value, parent_domain'`.
- `scripts/verify_pre_deploy.sh:2254` — B82's three literals all also occur in comments in
  `internal/feature/admin/exit_nodes.go` (`:1725,1729,1730,1736,1742,1743,1782`); deleting both
  `return false` guards keeps it green. **Revival:** a pure unit test on
  `shouldIncludeAsExitServer([]string{"tag:subnet-router"}, 1) == false`.
- `scripts/verify_pre_deploy.sh:2345` — B84's `LookupExitServerSSHTarget` hit in
  `internal/feature/exit_rules/sync.go` is the comment at `:663-664`; the helper is not called anywhere
  in that package (real callers: `internal/feature/admin/exit_nodes.go:250,870`,
  `internal/feature/admin/telegram.go:868`). **Revival:** pin the real resolution line
  (`lookupRelaySSHConfig`, `sync.go:810`).
- `scripts/verify_pre_deploy.sh:1452-1453` — B56's two negatives miss the surviving hardcode
  `internal/update/checker.go:130` `c.Owner = "skygate-operator"`: one pattern needs a trailing comma,
  the other needs the `owner/repo` slash. **Revival:** add `Owner\s*=\s*"skygate-operator"` to the alternation.
- `scripts/verify_pre_deploy.sh:1939` — B73's first negative is `-F "…/skygate/releases\""` (trailing
  `"`), but the occurrence at `internal/handlers/templates/layout.html:308` is followed by a backtick.
  **Revival:** match the rendered attribute instead.
- `scripts/verify_pre_deploy.sh:1074` — B40 counts **every** `^\s*Name:\s*"` line in the file (17 today)
  rather than registry entries, and the second alternative (`[6-9]|[1-9][0-9]`) is unanchored.
  **Revival:** count only `Name:` lines inside the `TestRegistry` literal.
- `scripts/verify_pre_deploy.sh:2540` — B88 pins five `Name:` strings in
  `internal/feature/admin/system_tests.go` (all present at `:256,626,700,770,964`) while the SQL body
  under each is unpinned, and the unit-test backstop named in the description
  (`system_tests_b66_b68_test.go`) no longer exists.
  **Revival:** one execution test per registry test, or a post-deploy assertion that
  `/admin/system_tests` reports these five as PASS.
- `scripts/check_b20.sh:91-95` — contract E.1 asserts `grep -q 'would clobber existing tag'`
  ("the stale-tag failure mode is documented"), i.e. it asserts a comment
  (`internal/update/docker.go:194`). **Revival:** delete it, or observe the behaviour in
  `internal/update/docker_fetch_b272_test.go` (real git) with a moved-tag case.
- `scripts/check_b93.sh:96-107` and its closing echo `:109` — the two "unit test" gates assert the
  **presence of `t.Skip` stubs** (`internal/nodeownership/infra_test.go:11`,
  `internal/feature/admin/B93_infra_audit_test.go:13`), the following `"$GO" build` (`:99`, `:107`)
  cannot compile `_test.go` files at all, and the script still prints "wired + 11 unit tests pass".
  **Revival:** `go vet` (which type-checks tests) plus a real or explicitly-SKIP test, and reword `:109`.
- `scripts/check_b94.sh:68` — `grep -qF 'state.Healthy = state.DB == "ok"'` passes even if the line is
  commented out; and `:79-85` claims the D5 change is "validated by the test passing" while the filter
  runs only `TestAvailability_*`/`TestNewChecker_*`/`TestChecker_*`, none of which reference
  `Healthy`/`DependenciesHealthy`. D5 is pinned by text alone.
  **Revival:** build `readyzState` with a failed dependency and assert
  `Healthy==true && DependenciesHealthy==false`.
- `scripts/check_b95.sh:98` and `:104` — the load-bearing ordering claims ("Sprintf is AFTER the
  `if res != nil` guard", "ack AFTER the nil check") are verified by looking for the **comment**
  `2026-07-30 (v0.34 fix)`. Moving the code back above the guard keeps both greps green.
  **Revival:** compare line numbers with `awk` (guard vs. dereference) instead of grepping prose.
- `scripts/check_b95.sh:141-142` — asserts `telegram_probe_test.go` contains `t.Skip`; the contract was
  written for the very breakage it now certifies.
  **Revival:** delete the contract and the dead stub, or restore the real cache-miss `t.Errorf` test.

### D. Patterns that can never match, or comparisons that are always true

- `scripts/verify_pre_deploy.sh:737` / `:757` — B29/B30's `! grep -qE "^go expireWatchMgr.Run"` /
  `"^go sidecarMgr.Run"`. In a gofmt'd `main()` the launch is tab-indented
  (`cmd/skygate/main.go:2843`, `:2722`), so `^go` at column 0 can never match and the clause cannot
  reject the pre-fix shape it names. **Revival:** assert the launch is inside the gate —
  `grep -A1 -F 'if cfg.ExpireWatchEnabled {' cmd/skygate/main.go | grep -qF 'go expireWatchMgr.Run(ctx)'`.
- `scripts/verify_pre_deploy.sh:913` — B33's `! grep -qE "^[[:space:]]*wget"` can never fire: the probe
  is a YAML flow list (`deploy/templates/headscale-compose.yml.tmpl:69`
  `test: ["CMD", "/nodejs/bin/node", …]`), so a `wget` probe would never begin a line.
  **Revival:** `! grep -qF 'wget' deploy/templates/headscale-compose.yml.tmpl`.
- `scripts/verify_pre_deploy.sh:805` — B31 asserts `conn.SetMaxIdleConns` **exists**; `SetMaxIdleConns(0)`
  and `SetMaxOpenConns(2)` both satisfy the check while contradicting the description ("pool: 10 conns").
  **Revival:** pin the values (`SetMaxOpenConns\(10\)`, `SetMaxIdleConns\([1-9]`).
- `scripts/verify_pre_deploy.sh:673` — B26's `! grep -qE "^ENV CGO_ENABLED=1" Dockerfile` cannot see
  the actual build: the Dockerfile never runs `go build`; the build is `entrypoint.sh:416`.
  **Revival:** `! grep -qE 'CGO_ENABLED=1' Dockerfile entrypoint.sh`.
- `scripts/verify_pre_deploy.sh:2909` — B99 can see a `bash` in *any* stage's `apk add`
  (`Dockerfile:87-88`) and cannot tell which stage it belongs to.
  **Revival:** parse the final stage's `RUN apk add` block.
- `internal/feature/healthz/availability_test.go:79-80` (executed by `scripts/check_b92.sh:76`) —
  `if st.LatencyMS < 0 { t.Errorf(...) }`. `LatencyMS` is `time.Since(t0).Milliseconds()`, so the
  comparison can never be true; the test cannot fail. **Revival:** assert an upper bound
  (`LatencyMS <= 5000`) or compare against a second monotonic reading.
- `scripts/check_b98.sh:80` — `test_count=$(grep -cE '^func Test' … || echo 0)`. `grep -c` prints `0`
  **and** exits 1 on no matches, so `test_count` becomes the two-line string `"0\n0"`;
  `[ "$test_count" -lt 15 ]` then errors ("integer expression expected", rc 2) and, being an `if`
  condition under `set -e`, the FAIL branch is **skipped** — the count contract silently disappears.
  **Revival:** drop the `|| echo 0` and use `|| true`.
- `scripts/check_b98.sh:64` — a single `grep -qF 'Category:    "network"'` is satisfied by *either* of
  the two new defs, so one losing its category stays green. **Revival:** require two matches
  (`grep -cF … | grep -q '^2$'`).
- `scripts/check_b95.sh:84` — `DEBT_OUT=$("$STATICCHECK" ./... 2>&1 | grep -E '^(internal|-).*\(…\) ?$' || true)`.
  The `|| true` means a staticcheck that cannot run produces no findings and the "0 debt" contract
  **passes on no evidence**; and the `^(internal|-)` anchor silently excludes `cmd/` and repo-root
  files, so U1000/SA5011 in the entrypoint are invisible to B95 forever.
  **Revival:** capture first, check staticcheck's own rc, and widen the anchor to `^(internal|cmd|-)`.
- `scripts/check_b95.sh:161-168` — `if git ls-remote origin <ref> 2>/dev/null | grep -q .` with `set -e`
  and no `pipefail`: on an unreachable/blocked remote the pipeline yields nothing, the condition is
  false, and the "branch deleted from origin" contracts **pass vacuously** while `2>/dev/null` hides
  git's error. **Revival:** capture first and distinguish "empty" from "ls-remote failed".
- `scripts/check_b100.sh:139-148` — `grep -qE 'c\.Protocol == ProtocolS3.*\n.*return nil'`: grep is
  line-based, so `\n` can never match and the "tight" branch is dead. The fallback is a tautology on its
  second conjunct — `grep -q 'return nil'` matches 14 unrelated lines in that file.
  **Revival:** `grep -A2 -F 'c.Protocol == ProtocolS3' internal/backup/mount.go | grep -q 'return nil'`.
- `scripts/check_b100.sh:179-183` — the `s3_bucket` audit-log contract is a `warn`, and the exit
  condition (`:265`) counts only `FAIL`, so this assertion can never redden the gate.
  **Revival:** promote it to `bad` (or make WARN affect the exit status).
- `scripts/check_b100.sh:246-261` — the only executed contract (`go test ./internal/backup/ -run
  'TestBuildS3Key|…'`) is skipped with a `warn` when `go` is absent, and WARN does not affect the exit
  code, so the script can exit 0 having verified nothing at runtime.
  **Revival:** emit a real `SKIP` line and fail when the evidence is required.
- `scripts/check_b100.sh:199-213` — `ru_ok`, `en_ok`, `ru_count` are assigned and never used; the i18n
  parity claim in the comment is not backed by its own evidence.
  **Revival:** use the counters for the pass/fail decision or delete them.
- `scripts/check_b91.sh:38-39` — `grep -qF "restart: unless-stopped" docker-compose.yml` and
  `grep -qF "healthcheck:" docker-compose.yml` are unanchored to the skygate service.
  Verified: the skygate service is `docker-compose.yml:20…378` and its policy is
  `restart: on-failure:5` (`:378`); `unless-stopped` survives only on `postgres` (`:444`) and
  `caddy` (`:516`). **The check certifies the opposite of the current skygate configuration and can
  never fail** while any other service keeps the string.
  **Revival:** assert `services["skygate"]["restart"] == "on-failure:5"` inside the existing
  `scripts/check_skygate_depends_on.py` parser and delete the raw-file greps.
- `scripts/check_b92.sh:71-72` — the 9-name existence loop uses unanchored `grep -qF "$t"`, and the list
  contains both `TestChecker_TailscaleFn` and `TestChecker_TailscaleFnNil`; the shorter name is a
  substring of the longer, so deleting the shorter test still passes.
  **Revival:** `grep -qF "func $t(" internal/feature/healthz/availability_test.go`.
- `scripts/check_b10`-class negative-assertion blindness:
  - `scripts/verify_pre_deploy.sh:264` (B10) — `! git ls-files | grep -E …`; the gate has no `pipefail`,
    so if `git ls-files` fails, grep reads EOF, exits 1, and `!` turns that into **PASS** — the secret
    scan is green exactly where it could not look.
    **Revival:** `files=$(git ls-files) || exit 1; ! grep -qE '\.(env|key|pem)$|^secrets/' <<<"$files"`.
  - `scripts/verify_pre_deploy.sh:1332` (B52) — `!` negates the whole pipeline, so deleting
    `internal/handlers/templates/admin/update.html` makes the first grep error with no stdout, the final
    `grep -q .` exit 1, and `!` return 0 ⇒ PASS. A negative assertion cannot detect removal of the
    artefact it guards. **Revival:** prefix `test -f … &&`.
- `scripts/verify_pre_deploy.sh:1286` (B49) — `grep -nP "tailscale up --accept-routes(?! --accept-dns=false)"
  … | head -5 | grep -q . && exit 1 || true`. It scans only `exit_rules.html` and `exit_rules_help.html`
  while the actual old short form lives in `internal/handlers/templates/admin/exit_nodes.html:714`; `-P`
  aborts under the MSYS/Git-Bash grep 3.0 locale (AGENTS.md trap #10) and `|| true` absorbs the failure.
  **Revival:** `! grep -rqF 'tailscale up --accept-routes' internal/handlers/templates/`.
- `scripts/verify_pre_deploy.sh:1074`, `:1101` — B40/B42's count and "called in order" claims are
  presence-only (B42 asserts the symbols exist in `internal/db/driver_postgres.go:155-156`, not their order).

### E. Actions that fail for the wrong reason / cannot fail at all (script-level)

- `scripts/check_b98.sh:87-92` — `test_output=$(cd internal/feature/admin && "$GO_BIN" test …)` under
  `set -e`: a **failing** `go test` makes the assignment itself non-zero, so the shell exits before the
  `FAIL:` diagnostic at `:88-91` ever prints. The gate shows `FAIL B98` with an empty body.
  **Revival:** append `|| true` and test `$?`/the captured text explicitly.
- `scripts/check_b98.sh:94-105` — the "B40 category coverage preserved" guard greps
  `internal/feature/admin/system_tests.go`, a file B98 does not touch, so it cannot detect the *new*
  defs losing coverage. **Revival:** assert coverage over the concatenated registry.
- `scripts/check_b91.sh:29-33` — the entrypoint pre-flight contracts (`v0.33.1.39`, `pre-flight`, `B91`,
  `/health`) all hit comments in `entrypoint.sh`; deleting the whole wait loop (`:276-294`) keeps B91
  green. **Revival:** assert the loop itself (`for i in $(seq 1 "$HS_WAIT_TIMEOUT")`).
- `scripts/check_b91.sh:69-73` — a machine without PyYAML makes the helper `sys.exit(2)`, which the gate
  reports as a hard **FAIL** (not SKIP) — a red gate for a non-regression. **Revival:** map exit 2 to
  `echo SKIP; exit 0`.
- `scripts/check_b93.sh:73` — `grep -qF "BackfillInfra(dbConn, nodes)" internal/nodeownership/auto.go`
  proves only that the call text occurs somewhere in the file; moving it into a never-invoked function
  keeps it green. **Revival:** `awk`-scope the match to `runOneTick`.
- `scripts/check_b20.sh:51-63` — the awk invariant only matches lines containing the literal
  `runGit(ctx, "fetch"`, so the mirror invocation built from an argv slice
  (`internal/update/docker.go:585`) is invisible to it (its `--force` is pinned only by an exact-string
  grep at `:65`). **Revival:** match `[]string{"fetch"` as an argv element and assert `--force` in the
  same construct.

**Count: 67 dead or near-dead contracts** — 25 entries that can never fail at all (section A) plus 42
individual assertions (sections B–E). Sixteen of the 25 wrapper entries additionally fail
"for the wrong reason" today because the code they pin has moved
(`internal/db/migrations_v0.46.go` does not exist — glob of `internal/db/migrations_v0.4*.go` returns
only `migrations_v0.47_test.go` and `migrations_v0.48_test.go`; the tests named in B64/B65/B66/B67/B68/B69/B76/B77/B78
no longer exist; `use-preferred-btn` was deliberately deleted and `scripts/check_b277_3_apply_preferred.sh:114-115`
now asserts its **absence**, i.e. B66 and B277.3 are mutually exclusive).

## Runtime invariants with no behavioural contract

1. **The applied ACL policy matches the generated one (freshness).** Nothing in the slice ever pushes or
   reads the live policy: B19 greps `GenerateACL`/`GenerateACLWithVia` (comment-satisfiable), B6 runs
   `internal/acl` tests whose DB-backed cases are `t.Skip` stubs
   (`internal/acl/acl_test.go:12`, `acl_b188_3_integration_test.go:47`, `acl_b265_test.go:31` — gated on
   `SKYGATE_TEST_PG_DSN`), B38/B39 grep the ACL admin UI. A user-visible break: a device's per-CIDR
   grant keeps naming a relay that no longer holds the prefix and the device silently loses those
   destinations (the B276 case). Proposed behavioural assertion: build a `device_rules` fixture, run
   the real generator, `SetPolicy` to a real headscale (or a recording fake), then read the policy back
   and assert `PolicyEquivalent(applied, generated)` — and assert the `via` on each CIDR grant equals
   the current prefix owner.
2. **Device tags actually reach headscale.** B69/B77/B81/B82/B84 are greps (three of them
   comment-satisfiable or wrapper-masked); only B87 exercises `AddTag` for real, and only over
   `httptest` (`internal/headscale/tags_test.go:52,118,151,179`). A user-visible break: `node_owner_map`
   claims `tag:dev-x` while `headscale nodes list` shows no tags, so every per-device ACL rule matches
   nothing. Proposed assertion: drive `ReconcileTags` against a fake headscale store seeded from a
   `node_owner_map` fixture and assert the tag is present on the node afterwards, plus a
   tag-owner/permission-denied path.
3. **Exit-rule → ACL outcome (parent-domain plumbing and CDN→CIDR expansion).** B15/B16's only real
   evidence is a package-wide `go test -run 'Test' ./internal/feature/exit_rules/`, whose relevant tests
   are `t.Skip` stubs (`internal/feature/exit_rules/parent_domain_test.go:11`, `store_test.go:11`).
   A user-visible break: a parent-domain rule stops covering its sub-domains, or a CDN rule stops
   expanding, and traffic is silently dropped. Proposed assertion: feed a rule set through
   `insertRuleUnique`/`sync` into a temp DB and assert the emitted CIDR set and `parent_domain` value.
4. **Migrations actually apply, and are idempotent.** B5's filter matches no test; B11's source scan
   excludes `internal/db/migrations_pg.go` (41 `migrateV0NNPG` functions); B34 pins an index *name*;
   B12 tests only the helper's SQL form; B18 builds and vets but never runs a migration. A user-visible
   break: a fresh install starts with a missing column/table, or a re-run fails. Proposed assertion:
   open a throwaway PostgreSQL, run `MigratePostgres` twice, and assert the schema + `applied_migrations`
   rows are identical after both runs.
5. **The `/admin/system_tests` registry tests actually pass.** B40 counts `Name:` lines; B88 greps five
   names; the SQL under each closure is unpinned. A user-visible break: the page reports a healthy
   tailnet while its DB/headscale checks are erroring. Proposed assertion: execute each registry closure
   against the test DB/headscale and assert a PASS verdict, or assert the JSON the page renders.
6. **The exit-node SSH transport chain (B81/B84/B85) really resolves and applies.** B81/B84's greps are
   comment-satisfiable, and B85's only behavioural half needs `SKYGATE_TEST_PG_DSN`
   (`internal/db/test_helpers_pg.go:64` `t.Skip`), which the reference invocation does not set. A
   user-visible break: `--advertise-routes` is never applied to the relay and clients lose egress.
   Proposed assertion: unit-test the `user@host[:port]` target builder and run
   `SetAdvertisedRoutes` against a stubbed SSH runner, asserting the exact argv.
7. **Backups really run and reach the destination.** B99 greps `bash` in the Dockerfile/runner; B100's
   only executed contract is `warn`-gated and the mount short-circuit clause is a tautology. A
   user-visible break: a scheduled backup never produces an archive. Proposed assertion: run
   `scripts/backup.sh` (or `backup.Run`) against a temp dir and assert a non-empty archive plus the
   expected audit row.
8. **The sidebar/mobile drawer renders.** B96/B97 assert CSS and `layout.html` text; the Go tests they
   run only regex the same file. A user-visible break: a section is unreachable on a phone. Proposed
   assertion: render the layout with a real data map and assert the sections/links appear in the HTML.

## Notes

- **The wrapper explains the operator's report.** The whole B59–B90 band (25 checks, ~31% of the slice)
  is structurally incapable of failing, and that band is exactly where the exit-rules, tag-bridging and
  ACL-adjacent contracts live. A single-line fix in the gate (propagate `rc`) would turn ~16 of them red
  immediately, which is a better starting point than trusting the current green.
- **Silence, not truncation, is the failure mode in this slice.** Almost every check is a `grep -q`
  chain; on a miss `grep` prints nothing, so `FAIL B<n> <essay>` arrives with an empty body
  (`scripts/verify_pre_deploy.sh:194-195`) and the operator cannot see which of 4–25 clauses broke.
  Only `scripts/check_b100.sh` (37 PASS lines + summary), `check_b92/93/94/96/97/98` (unbounded
  `go test`/`go build` output printed *before* their `SKY-FAIL` marker) and `check_b98.sh:87` (no output
  at all) are genuinely at risk of the 20-line cap; B20 (8 lines) is safe.
- **Style is not the same as coverage.** B1 (`go test ./...`) is behavioural, but a green B1 cannot be
  read as "the DB-backed invariants ran": a large share of the suite is `t.Skip` stubs
  (`internal/db/migrations_v0.47_test.go:15`, `internal/acl/acl_test.go:12`,
  `internal/feature/exit_rules/parent_domain_test.go:11`) that report rc 0.
- **B91's contract contradicts the code and the description.** `docker-compose.yml:378` is
  `restart: on-failure:5` (deliberate, comment at `:365-377`) while `check_b91.sh:38` demands
  `restart: unless-stopped`; the check passes off `postgres`/`caddy`. The gate description
  (`scripts/verify_pre_deploy.sh:2667`) still repeats the old claim.
- **Documentation drift inside the scripts:** `check_b96.sh:12-16`/the gate description
  (`:2834`) say "6 collapsible sections" while `check_b96.sh:52` and the Go test assert **10**;
  `check_b91.sh:15` still says `unless-stopped`; `check_b93.sh:109` claims "11 unit tests pass" while
  the script asserts `t.Skip` stubs; `check_b92.sh:25` says 8 tests, `:78` says 9, and the test file
  has 10. Anyone reviving these from their own comments will revive the wrong invariant.
- **`$GO` / `GO_BIN` plumbing is inconsistent.** `GO` is not exported by the gate
  (`scripts/verify_pre_deploy.sh:40-64`), so the two single-quoted wrapper entries that reference it
  (B89 `:2589`, B90 `:2617`) expand it empty. `check_b93/94/95/96/97/92` resolve their own `GO=""` +
  fallback; `check_b98.sh:49` and `check_b100.sh:246` use `GO_BIN`. Nothing enforces one convention.
- **`test -f` proves nothing about what is committed** (AGENTS.md trap #11) — several entries here rely
  on `test -f` (`B46:1229`, `B54:1393-1395`, `check_b92.sh:70`, `check_b96.sh:45`, `check_b97.sh:40`)
  and on a temp-file wrapper, so a `.gitignore`d script would still look green locally. I could not run
  `git ls-files` (read-only audit), so committed-ness of `scripts/check_b20.sh`/`check_b9*.sh`/
  `check_b100.sh` is unverified.
- **Not found in the slice (explicit negatives):** no `check_ge LABEL 0`-style helper, no script that
  prints FAIL and exits 0 (`check_b20.sh:98`, `check_b100.sh:265` do gate on `FAIL`), no PASS/FAIL
  totals computed from mismatched variables, no `-ge 0` numeric guard, no empty-list loop, and no
  `set -uo pipefail` + long-producer `| grep -q` SIGPIPE site beyond the low-risk
  `scripts/check_b100.sh:256` (`echo "$test_out" | grep -qE '^ok'` — `ok` is the last line, but the
  here-string form is the documented-safe one).
- **Method:** static read only; the gate was not run. Every finding above was verified by reading the
  cited line. 98 entries, 10 distinct scripts, plus the B8 gate (`:247-256`, the only `SKIP` path in the
  slice: requires `/home/admin/skygate` and `VERIFY_RUN_SMOKE=1` or a TTY).
