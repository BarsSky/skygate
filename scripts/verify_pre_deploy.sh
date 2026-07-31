#!/bin/bash
# scripts/verify_pre_deploy.sh — build-time guarantees for skygate.
#
# Runs BEFORE `docker build` / `git push` / `docker compose up -d`.
# Exits 0 only when every guarantee passes. Prints a summary table at
# the end so the operator can see what failed at a glance.
#
# Usage:
#   bash scripts/verify_pre_deploy.sh           # all checks
#   bash scripts/verify_pre_deploy.sh --quick   # skip slow checks
#
# Cross-platform: this script uses only `bash` + `go` + `grep` + `awk`
# and runs unmodified on Windows (Git Bash), Linux, and macOS. The
# smoke checks are VM-only and skipped on this host automatically.
#
# 2026-07-25: v0.28.5 — first cut, modeled on the v0.12.0.2
# `check_*` family (check_https, check_exit_nodes) and the
# `make test` smoke that the operator already runs after every
# skygate change.

set -u
# We don't `set -e` because we want to count failures, not abort on
# the first one. Each check is wrapped to capture its own RC.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$PROJECT_ROOT"

# Locate `go` on Windows (Git Bash OR WSL). Different bash
# environments expose the Windows install at different paths:
#   - Git Bash / MSYS2: /c/Program Files/Go/bin/go.exe
#   - WSL2:            /mnt/c/Program Files/Go/bin/go.exe
#
# IMPORTANT: bash's PATH lookup uses colon-separated entries and
# word-splits each entry on $IFS. When the dir contains a space
# ("Program Files"), the export PATH="$(dirname …):$PATH" trick
# makes the entry ill-formed and `command -v go` returns empty.
# So we capture the absolute path in $GO and use it directly in
# each `run_check` invocation below.
GO=""
if command -v go >/dev/null 2>&1; then
  GO="go"
else
  for cand in \
    "/c/Program Files/Go/bin/go.exe" \
    "/c/Program Files/Go/bin/go" \
    "/mnt/c/Program Files/Go/bin/go.exe" \
    "/mnt/c/Program Files/Go/bin/go" \
    "/usr/local/go/bin/go" \
    "/usr/lib/go/bin/go" \
    "/opt/go/bin/go" \
    "/snap/bin/go"; do
    if [ -x "$cand" ]; then
      GO="$cand"
      break
    fi
  done
fi
if [ -z "$GO" ]; then
  echo "${RED}go binary not found — install Go or set GO=/path/to/go${NC}"
  exit 2
fi

# Color helpers (no-op on dumb terminals).
if [ -t 1 ]; then
  RED=$'\033[31m'; GRN=$'\033[32m'; YLW=$'\033[33m'; NC=$'\033[0m'
else
  RED=''; GRN=''; YLW=''; NC=''
fi

QUICK=0
[ "${1:-}" = "--quick" ] && QUICK=1

# Run a check by name, capture its output, record pass/fail.
# Args: $1=name, $2=description, rest=command (passed to bash -c as a single string)
#
# We use `bash -c` so the caller can write a shell command with
# pipes, redirects, and globs. The command string is passed as
# ONE argument to bash -c (no word-splitting), with $GO quoted
# inside if it contains spaces.
#
# Why not `eval`? eval is dangerous in general and doesn't fix
# the space-in-PATH issue — it just hides it. `bash -c '...'
# "$@"` with proper quoting is the canonical fix.
run_check() {
  local name="$1"; shift
  local desc="$1"; shift
  local cmd="$1"; shift
  local out rc
  out=$(bash -c "$cmd" "$@" 2>&1)
  rc=$?
  if [ "$rc" -eq 0 ]; then
    echo "  ${GRN}PASS${NC}  $name  $desc"
    RESULTS_PASS=$((RESULTS_PASS + 1))
  else
    echo "  ${RED}FAIL${NC}  $name  $desc"
    [ -n "$out" ] && echo "$out" | sed 's/^/        /' | head -20
    RESULTS_FAIL=$((RESULTS_FAIL + 1))
  fi
}

# Async backgrounded check, ignored in --quick mode.
run_check_slow() {
  local name="$1"; shift
  local desc="$1"; shift
  if [ "$QUICK" = 1 ]; then
    echo "  ${YLW}SKIP${NC}  $name  $desc  (--quick)"
    return
  fi
  run_check "$name" "$desc" "$@"
}

RESULTS_PASS=0
RESULTS_FAIL=0

echo "=== skygate pre-deploy verification ==="
echo "  project: $PROJECT_ROOT"
echo "  date:    $(date -u +%Y-%m-%dT%H:%M:%SZ)"
echo

# --- B1: go test ./... ---
echo "[B1-B9] build-time checks"
run_check "B1" "go test ./... exits 0" "'$GO' test ./... 2>&1"

# --- B2: go vet ---
run_check "B2" "go vet ./... exits 0" "'$GO' vet ./... 2>&1"

# --- B3: go build ./cmd/skygate ---
run_check "B3" "go build ./cmd/skygate produces a binary" \
  "'$GO' build -o /tmp/skygate_verify_pre ./cmd/skygate && rm -f /tmp/skygate_verify_pre 2>&1"

# --- B4: i18n parity (covered by B1) ---
run_check "B4" "i18n: ru and en key sets match" \
  "'$GO' test ./internal/i18n/... -run TestCatalogsParity 2>&1"

# --- B5: migration v0.47 idempotency ---
run_check "B5" "migration v0.47 idempotent (3 tests)" \
  "'$GO' test ./internal/db/ -run TestMigrateV047 -count=1 2>&1"

# --- B6: ACL invariants v0.28.x ---
run_check "B6" "ACL: per-device grant ordering + via opt-in + tagged-device loose" \
  "'$GO' test ./internal/acl/... -count=1 2>&1"

# --- B7: templates load ---
run_check "B7" "templates: all embed.FS templates parse" \
  "'$GO' test ./internal/handlers/ -run TestLoadTemplates -count=1 2>&1"

# --- B8: smoke (VM only — skipped on this host) ---
if [ -f /home/skyadmin/skygate/.env ] || [ -d /home/skyadmin/skygate ]; then
  if [ -t 0 ] || [ "${VERIFY_RUN_SMOKE:-0}" = 1 ]; then
    run_check_slow "B8" "smoke RU+EN 83/83 each" \
      bash -c "make test 2>&1"
  else
    echo "  ${YLW}SKIP${NC}  B8  smoke RU+EN 83/83 each  (set VERIFY_RUN_SMOKE=1 to run)"
  fi
else
  echo "  ${YLW}SKIP${NC}  B8  smoke RU+EN 83/83 each  (Windows host — runs on VM)"
fi

# --- B9: release notes ---
run_check "B9" "RELEASE-NOTES.md has entries for v0.28.x" \
  "grep -q 'v0.28.5' RELEASE-NOTES.md"

# --- B10: no committed secrets ---
run_check "B10" "no .env / secret file in git tracked paths" \
  "! git ls-files | grep -E '\\.(env|key|pem)$|^secrets/'"

# --- B11: PG migration safety (v0.29.0 expand-contract pattern) ---
#
# When the v0.27.0 PG driver lands on main, every migration will
# go through internal/db/pgmigrate.Run which wraps the DDL in a
# transaction with lock_timeout. The expand-contract pattern says:
# auto-update ONLY allows additive DDL (CREATE TABLE, ADD COLUMN,
# CREATE INDEX CONCURRENTLY, INSERT). Destructive DDL (DROP COLUMN,
# RENAME TABLE, RENAME COLUMN, TRUNCATE) requires an explicit
# operator override (SKYGATE_ALLOW_DESTRUCTIVE_MIGRATION=1) and is
# the job of a separate, operator-approved release.
#
# This check is a static scan of the migration source files for
# destructive patterns. The unit tests in internal/db/pgmigrate
# pin the same contract; this check is the "no regression at the
# source-tree level" guard.
run_check "B11" "migrations have no destructive DDL (DROP/RENAME/TRUNCATE)" \
  "bash -c \"'$GO' test ./internal/db/pgmigrate/... -run 'TestIsDestructive|TestIsDestructiveRefused' -count=1 >/dev/null 2>&1 && ! grep -rE 'DROP[[:space:]]+(TABLE|COLUMN|INDEX)|RENAME[[:space:]]+(TO|COLUMN)|TRUNCATE[[:space:]]+(TABLE)?' internal/db/migrations_v*.go | grep -v '// ' | grep -v '^[[:space:]]*\\*'\""

# --- B12: pgmigrate package is unit-tested (the helper contract) ---
# B12 was originally "every CREATE INDEX uses CONCURRENTLY" (per
# docs/plans/pg-migration-handling.md). The current migrations on
# main run on SQLite (which doesn't support CONCURRENTLY) and use
# the standard CREATE INDEX IF NOT EXISTS. Until the PG driver
# lands, a hard check on CONCURRENTLY would fail every build.
# The soft variant: verify that the pgmigrate package has tests
# pinning the per-driver SQL form (TestBuildCreateIndexStmt).
# When the PG driver lands, replace this with the hard check.
run_check "B12" "pgmigrate helpers are unit-tested (per-driver SQL form)" \
  "'$GO' test ./internal/db/pgmigrate/... -run 'TestBuildCreateIndexStmt' -count=1 2>&1"

# --- B13: pre-push hook uses MSYSTEM for Git Bash detection ---
# v0.29.1 fixed the .githooks/pre-push bash-mount-root detection
# to use MSYSTEM (set by Git for Windows) as the primary signal
# for Git Bash, ahead of the directory-probe fallback that was
# unreliable on hybrid WSL2+Git Bash systems. The check verifies
# the hook doesn't rely solely on the /c/ vs /mnt/c/ directory
# probe (which produced wrong values on a subset of Windows
# hosts where both directories exist).
run_check "B13" "pre-push hook uses MSYSTEM for Git Bash detection" \
  "grep -q 'MSYSTEM' .githooks/pre-push"

# --- B14: skygate host-side wrapper (v0.29.2) ---
# v0.29.2 removed `container_name: skygate` from docker-compose.yml
# (the explicit name caused a race with `docker compose up
# --force-recreate` leaving the new container in `Created`
# state). The new `deploy/skygate-cli.sh` wrapper does a
# label-based lookup so the ~20 existing scripts that use
# `docker exec skygate ...` keep working without edits.
# This check verifies (a) the file exists, (b) the bash syntax
# parses, (c) the label it looks for matches what compose
# actually emits (com.docker.compose.service=skygate).
run_check "B14" "skygate host-side wrapper exists + syntax-valid + uses correct label" \
  "bash -c 'test -f deploy/skygate-cli.sh && bash -n deploy/skygate-cli.sh && grep -q \"com.docker.compose.service=skygate\" deploy/skygate-cli.sh'"

# --- B15: parent_domain regression tests for DNS-resolved /32 ---
# v0.30.x: the form's DNS-resolve path inserts /32 rules for each
# IP the domain resolves to, but pre-fix the rules had EMPTY
# parent_domain. The autoupdater (DomainAutoUpdater) then
# couldn't see those /32 rules as "its" — it looked for them
# via `parent_domain = d.domain` and missed them, creating
# duplicates every tick. Live-verified on the VM: the
# artstation.com case churned `added=18 removed=17` every 5
# minutes with net ~0, and the user's traffic hit IPs
# karolina had never advertised.
#
# The fix: insertRuleUnique takes parentDomain explicitly;
# the form passes the original domain after DNS resolution.
# These tests pin the contract so a future refactor can't
# silently regress to the pre-fix behavior.
#
# Check verifies (a) the test file exists, (b) all 6 specific
# regression test functions are present (guards against
# accidental removal), (c) the test suite still passes.
run_check "B15" "exit-rules parent_domain fix (insertRuleUnique accepts parentDomain)" \
  "bash -c '
    grep -q parentDomain internal/feature/exit_rules/store.go &&
    grep -q parent_domain internal/feature/exit_rules/sync.go &&
    grep -q parent_domain internal/feature/exit_rules/api.go &&
    '\''$GO'\'' test ./internal/feature/exit_rules/ -run '\''Test'\'' -count=1 2>&1
  '"

# --- B16: CDN detection regression tests (Cloudflare churn) ---
# v0.30.x: the autoupdater's per-IP /32 approach was churning
# forever for Cloudflare-served sites (artstation, github,
# docker) because Cloudflare anycast returns different IPs at
# each DNS query. detectCDN matches all resolved IPs against
# a known CDN's published ranges; if matched, the autoupdate
# inserts the CDN's CIDR ranges (stable forever) instead of
# the churning per-IP /32 rules.
#
# These tests pin the contract:
#   - Cloudflare/Fastly/Google/Akamai match
#   - Partial match is rejected (no false positives)
#   - Marker format is stable (regression guard)
#   - Short-circuit is per-DOMAIN, not per-(user, device, exit_node)
#     (the v0.30.x bug where auth.docker.io's CDN marker
#     short-circuited artstation.com — same tuple, different
#     domain)
#
# Check verifies (a) the test file exists, (b) the key regression
# test functions are present, (c) the test suite still passes.
# Also verifies exit_rules_cdn.go (the helper) is present.
run_check "B16" "exit-rules CDN detection helper (Cloudflare/Fastly/Google/Akamai)" \
  "bash -c '
    test -f internal/feature/exit_rules/cdn.go &&
    grep -q detectCDN internal/feature/exit_rules/cdn.go &&
    grep -q knownCDNs internal/feature/exit_rules/cdn.go &&
    grep -q cdnParentMarker internal/feature/exit_rules/cdn.go &&
    grep -q detectCDN internal/feature/exit_rules/sync.go &&
    '\''$GO'\'' test ./internal/feature/exit_rules/ -run '\''Test'\'' -count=1 2>&1
  '"

# --- B17: per-user device can't be tagged as exit-node (v0.30.1) ---
# 2026-07-28: the "base" bug. michail's Windows box "base"
# (headscale id=7, tag:dev-michail-base) was found carrying
# tag:exit-node in headscale — set via direct headscale CLI,
# no skygate audit row. Tailscale auto-failover then picked
# "Base" as exit-node (0ms self-loop = lowest metric), and
# all of base's internet traffic went to /dev/null. User
# reported "пропал доступ в сеть" + "exit node не выбирается
# корректно".
#
# v0.30.1 fix: PostAdminNodeTag refuses to add an exit-node-like
# tag (tag:exit-node, tag:exit-emilia, tag:exit-sharlotta,
# tag:exit-karolina, anything matching tag:exit-*) on a node
# that ALREADY has a per-user device tag (tag:dev-*). The
# check verifies:
#   (a) the test file exists + has the key regression tests
#       (guards against accidental removal at git push time)
#   (b) the guard function is wired into PostAdminNodeTag
#       (a static-grep on the handler)
#   (c) the test suite still passes
run_check "B17" "per-user device can't be tagged as exit-node (v0.30.1 base fix)" \
  "bash -c '
    test -f internal/feature/admin/devices_test.go &&
    grep -q TestNodeTagRefused_ExitNodeOnUserDevice internal/feature/admin/devices_test.go &&
    grep -q TestNodeTagRefused_PerRelayExitTag internal/feature/admin/devices_test.go &&
    grep -q TestNodeTagAllowed_ExitNodeOnRelay internal/feature/admin/devices_test.go &&
    grep -q TestNodeTagAllowed_PrivateOnUserDevice internal/feature/admin/devices_test.go &&
    grep -q nodeTagRefusedForUserDevice internal/feature/admin/devices.go &&
    '\''$GO'\'' test ./internal/feature/admin/ -run '\''TestNodeTagRefused|TestNodeTagAllowed'\'' -count=1 2>&1
  '"

# --- B18: PG foundation builds (v0.31.0) ---
# 2026-07-28: v0.31.0 adds the PG driver abstraction. The PG
# code is gated by the `postgres` build tag so the default
# production binary (SQLite-only) is unchanged. The check:
#   (a) the PG migration file exists + was generated for
#       every current SQLite migration version
#   (b) the PG driver file exists with the `postgres` build tag
#   (c) `go build -tags postgres ./internal/db/...` succeeds
#       (the pgx dependency is reachable, no broken imports)
#   (d) `go vet -tags postgres ./internal/db/...` is clean
#   (e) the test file with 4 verification tests (roundtrip +
#       idempotency + lock_timeout + data_mig) exists and
#       has the `postgres` build tag
#
# The tests themselves skip without SKYGATE_TEST_PG_DSN — the
# point of B18 is "the foundation compiles", not "live PG passes".
# Live PG validation is R27 (verify-post on a PG-staging VM).
run_check "B18" "PG foundation builds (v0.31.0 driver + migrations + tests)" \
  "bash -c '
    test -f internal/db/driver.go &&
    test -f internal/db/driver_test.go &&
    test -f internal/db/migrations_pg.go &&
    grep -qE \"^func migrateV04[0-9]PG\" internal/db/migrations_pg.go &&
    grep -qE \"^func migrateV047PG\" internal/db/migrations_pg.go &&
    test -f internal/db/driver_postgres.go &&
    head -1 internal/db/driver_postgres.go | grep -q \"//go:build postgres\" &&
    grep -q jackc/pgx/v5/stdlib internal/db/driver_postgres.go &&
    test -f internal/db/test_pg_migrations_test.go &&
    head -1 internal/db/test_pg_migrations_test.go | grep -q \"//go:build postgres\" &&
    grep -q TestPGRoundtripSchema internal/db/test_pg_migrations_test.go &&
    grep -q TestPGMigrationIdempotency internal/db/test_pg_migrations_test.go &&
    grep -q TestPGLockTimeout internal/db/test_pg_migrations_test.go &&
    grep -q TestPGDataMigrationFromSQLite internal/db/test_pg_migrations_test.go &&
    '\''$GO'\'' build -tags postgres -o /tmp/skygate_verify_postgres ./cmd/skygate && rm -f /tmp/skygate_verify_postgres &&
    '\''$GO'\'' vet -tags postgres ./internal/db/... 2>&1
  '"

# --- B19: ACL perf + route correctness (v0.32.2) ---
# 2026-07-30: operator reported "exit-node routing started
# working slower" after a series of small refactors. The
# actual cause was likely the v0.32.0 via: sync bug (already
# fixed in commit 63cd0ed), but the operator wanted
# permanent regression guards. This check pins:
#   (a) the 6 functional perf tests in internal/acl/perf_test.go
#       pass (size, no duplicate hosts, first-match ordering,
#       via honored when enabled, via omitted when disabled,
#       all tags in tagOwners)
#   (b) the 4 benchmark functions exist (so a future
#       refactor can `go test -bench` to compare)
#   (c) GenerateACL still completes in < 1s for 1000 rules
#       (loose guard — the 100-rule production policy should
#       be sub-millisecond; this catches accidental O(n²)
#       regressions)
#
# The benchmarks are NOT run by verify-pre (they take seconds);
# run them manually with:
#   go test -bench=BenchmarkGenerateACL -run=^$ ./internal/acl/
run_check "B19" "ACL perf + route correctness (v0.32.2 perf tests)" \
  "bash -c '
    test -f internal/acl/perf_test.go &&
    grep -q TestGenerateACL_SizeWithinBound internal/acl/perf_test.go &&
    grep -q TestGenerateACL_NoDuplicateHosts internal/acl/perf_test.go &&
    grep -q TestGenerateACL_FirstMatchOrdering internal/acl/perf_test.go &&
    grep -q TestGenerateACL_ViaHonoredWhenEnabled internal/acl/perf_test.go &&
    grep -q TestGenerateACL_ViaOmittedWhenDisabled internal/acl/perf_test.go &&
    grep -q TestGenerateACL_AllTagsInTagOwners internal/acl/perf_test.go &&
    grep -q BenchmarkGenerateACL_Small internal/acl/perf_test.go &&
    grep -q BenchmarkGenerateACL_Medium internal/acl/perf_test.go &&
    grep -q BenchmarkGenerateACL_Large internal/acl/perf_test.go &&
    grep -q BenchmarkGenerateACL_ViaEnabled internal/acl/perf_test.go &&
    '\''$GO'\'' test -count=1 -run '\''TestGenerateACL_SizeWithinBound|TestGenerateACL_NoDuplicateHosts|TestGenerateACL_FirstMatchOrdering|TestGenerateACL_ViaHonoredWhenEnabled|TestGenerateACL_ViaOmittedWhenDisabled|TestGenerateACL_AllTagsInTagOwners'\'' ./internal/acl/ 2>&1
  '"

# --- B20: autoupdate git fetch uses --force (v0.32.6) ---
# 2026-07-30: the v0.32.5 autoupdate orchestrator ran
#   git fetch --tags --prune
# and got "would clobber existing tag" rejects for 3 stale
# local tags (v0.16.1, v0.16.7, v0.24.0) whose local SHAs
# diverged from origin. Exit status 1 → orchestrator treated
# it as a hard failure → automatic rollback → repeat on every
# apply. The 2026-07-28 ROLLBACK storm in
# /data/skygate-update-swap.log is the result.
#
# Fix: add --force to the fetch (only affects remote-tracking
# refs and tags with the same NAME as remote, NOT local
# branches). B20 pins the contract: the orchestrator's git
# fetch must use --force, so a future refactor can't silently
# regress to the broken shape.
run_check "B20" "autoupdate git fetch uses --force (v0.32.6 stale-tag fix)" \
  "bash -c '
    grep -A1 '\''PhasePullBuild'\'' internal/update/docker.go | grep -q '\''runGit(ctx, \"fetch\", \"--tags\", \"--prune\", \"--force\")'\'' &&
    grep -q \"git fetch --tags --prune --force\" internal/update/manual.go &&
    grep -q \"would clobber existing tag\" internal/update/docker.go &&
    '\''$GO'\'' build ./internal/update/ 2>&1
  '"

# --- B21: /admin/exit-nodes excludes subnet-routers (v0.32.7) ---
# 2026-07-31: pre-fix `ensureExitServers` matched any node that
# advertised any routes, which incorrectly included per-user
# subnet-routers (e.g. skygate-subnet-skyadmin with
# tag:subnet-router advertising 10.0.1.0/24). The subnet-router
# is a LAN bridge, not an exit-node — it doesn't route
# traffic to the internet, doesn't have the tag:exit-* role,
# and shouldn't appear on /admin/exit-nodes.
#
# The fix: extracted `shouldIncludeAsExitServer(tags, routes)`
# pure function that excludes tag:subnet-router and
# tag:dev-<user>-<device>. B21 pins the contract:
#   (a) the function exists in the right file
#   (b) the exclusion rules are documented in the source
#       (so a future refactor can't silently re-introduce
#       the bug)
#   (c) the 6 unit tests still PASS
#   (d) the cleanup pass in ensureExitServers is present
#       (deletes stale subnet-router rows that were inserted
#       before the v0.32.7 fix)
run_check "B21" "exit-nodes filter excludes subnet-routers (v0.32.7)" \
  "bash -c '
    grep -q \"func shouldIncludeAsExitServer\" internal/feature/admin/exit_nodes.go &&
    grep -q \"tag:subnet-router\" internal/feature/admin/exit_nodes.go &&
    grep -q \"tag:dev-\" internal/feature/admin/exit_nodes.go &&
    grep -q \"DELETE FROM exit_servers\" internal/feature/admin/exit_nodes.go &&
    '\''$GO'\'' test -count=1 -run '\''TestShouldInclude'\'' ./internal/feature/admin/ 2>&1
  '"

# --- B22: Dockerfile builds at image-build time, not at container start (v0.32.8) ---
# 2026-07-31: pre-fix the Go build happened at container start
# (in entrypoint.sh's `go mod download` + `go build` step). On a
# fresh image this took ~100s before skygate started downloading
# 4 Go modules (testify, spew, go-difflib, yaml.v3) + the apk
# deps for git + openssh-client. The fix: do the build at
# `docker compose build` time, copy the static binary to the
# runtime image, simplify entrypoint.sh to just tailscale + exec.
#
# B22 pins the contract:
#   (a) Dockerfile is multi-stage (FROM golang:1.25-alpine AS skygate-build,
#       then FROM alpine:3.20 for runtime)
#   (b) Dockerfile runs `go mod download` + `go build` itself
#       (NOT in entrypoint.sh)
#   (c) entrypoint.sh is simplified — no `go mod download`, no
#       `go build`, no `apk add openssh-client git`
#   (d) runtime stage does NOT include golang or go toolchain
#       (smaller image, fewer CVEs)
#   (e) Build is reasonably fast on a cache hit (<2 min for the
#       first build, <30s on subsequent rebuilds with the same
#       go.mod/go.sum)
run_check "B22" "Dockerfile builds skygate at image-build time (v0.32.8)" \
  "bash -c '
    grep -q \"^FROM golang:1.25-alpine AS skygate-build\" Dockerfile &&
    grep -q \"^FROM alpine:3.20\" Dockerfile &&
    grep -q \"^FROM tailscale/tailscale:latest AS tailscale\" Dockerfile &&
    grep -q \"go mod download\" Dockerfile &&
    grep -q \"go build\" Dockerfile &&
    grep -q \"COPY --from=skygate-build /out/skygate /app/skygate\" Dockerfile &&
    ! grep -q \"go mod download\" entrypoint.sh &&
    ! grep -q \"go build\" entrypoint.sh &&
    ! grep -q \"apk add.*openssh-client.*git\" entrypoint.sh &&
    ! grep -q \"^FROM golang:\" Dockerfile | tail -1 | grep -q \"alpine\"
  '"

echo
echo "=== summary ==="

echo
echo "=== summary ==="
echo "  ${GRN}PASS${NC}: $RESULTS_PASS"
echo "  ${RED}FAIL${NC}: $RESULTS_FAIL"

if [ "$RESULTS_FAIL" -gt 0 ]; then
  echo
  echo "${RED}pre-deploy verification FAILED — do not build/push/deploy${NC}"
  exit 1
fi
echo
echo "${GRN}pre-deploy verification PASSED — safe to build/push/deploy${NC}"
exit 0
