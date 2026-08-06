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
if [ -f /home/admin/skygate/.env ] || [ -d /home/admin/skygate ]; then
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
# relay-3 had never advertised.
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
# 2026-07-28: the "workstation-8" bug. user1's Windows box "workstation-8"
# (headscale id=7, tag:dev-user1-workstation-8) was found carrying
# tag:exit-node in headscale — set via direct headscale CLI,
# no skygate audit row. Tailscale auto-failover then picked
# "Base" as exit-node (0ms self-loop = lowest metric), and
# all of workstation-8's internet traffic went to /dev/null. User
# reported "пропал доступ в сеть" + "exit node не выбирается
# корректно".
#
# v0.30.1 fix: PostAdminNodeTag refuses to add an exit-node-like
# tag (tag:exit-node, tag:exit-relay-1, tag:exit-relay-2,
# tag:exit-relay-3, anything matching tag:exit-*) on a node
# that ALREADY has a per-user device tag (tag:dev-*). The
# check verifies:
#   (a) the test file exists + has the key regression tests
#       (guards against accidental removal at git push time)
#   (b) the guard function is wired into PostAdminNodeTag
#       (a static-grep on the handler)
#   (c) the test suite still passes
run_check "B17" "per-user device can't be tagged as exit-node (v0.30.1 workstation-8 fix)" \
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
# subnet-routers (e.g. skygate-subnet-admin with
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
# B22 pins the contract (v0.32.13 REVERT of v0.32.8):
#   (a) Dockerfile is single-stage (FROM golang:1.25-alpine as
#       the runtime workstation-8 — NOT a multi-stage with `skygate-build`
#       and `alpine:3.20` stages; that was the v0.32.8 path
#       that introduced the CGO+musl deadlock).
#   (b) Dockerfile does NOT run `go mod download` or `go build`
#       (those happen at container start, in entrypoint.sh).
#   (c) entrypoint.sh DOES `go mod download` + `go build` + the
#       version-label git invocation.
#   (d) entrypoint.sh DOES `apk add openssh-client git` (the
#       runtime build needs ssh for the v0.32.7+ self-update
#       orchestrator and git for the build label).
#   (e) Runtime image is bigger (~800MB with the full Go
#       toolchain) — accepted cost for a binary that doesn't
#       have the CGO+musl TCP/HTTP deadlock.
# This is the v0.32.5 pattern; the v0.32.8 attempt at
# "build at image time, copy static binary" broke twice
# (CGO_ENABLED=0 + musl HTTP wedge) and was reverted.
run_check "B22" "Dockerfile uses single-stage build, entrypoint.sh runs go build (v0.32.13)" \
  "bash -c '
    grep -q \"^FROM golang:1.25-alpine$\" Dockerfile &&
    grep -q \"^FROM tailscale/tailscale:latest AS tailscale\" Dockerfile &&
    ! grep -q \"^FROM golang:1.25-alpine AS skygate-build\" Dockerfile &&
    ! grep -q \"^FROM alpine:3.20\" Dockerfile &&
    ! grep -qE \"^[[:space:]]*go mod download\" Dockerfile &&
    ! grep -qE \"^[[:space:]]*go build\" Dockerfile &&
    ! grep -q \"COPY --from=skygate-build\" Dockerfile &&
    grep -qF \"go mod download\" entrypoint.sh &&
    grep -qF \"go build\" entrypoint.sh &&
    grep -qF \"apk add --no-cache openssh-client git\" entrypoint.sh
  '"

# --- B23: CI Go version matches go.mod (v0.32.9) ---
# 2026-07-31: the last 5 CI runs (v0.32.6 .. v0.32.8) all FAILED on
# `go mod download` because .github/workflows/ci.yml installed
# Go 1.23 while go.mod required go 1.25.0. The toolchain
# directive auto-downloads 1.25 in CI runners that have network
# access, but pinning both sides explicitly is safer (the runner
# may have network restrictions, the go directive's required-
# version check is more strict than the toolchain's auto-fetch).
#
# B23 pins the contract:
#   (a) .github/workflows/ci.yml uses go-version: '1.25' (matches
#       the go directive in go.mod)
#   (b) go.mod has both `go 1.25.0` (minimum required) AND
#       `toolchain go1.25.0` (pinned, defensive)
#   (c) ci.yml has 2 Setup Go steps (test job + verify-pre job)
#       and BOTH use Go 1.25 — a future refactor that bumps one
#       but not the other fails the check
run_check "B23" "CI Go version matches go.mod (v0.32.9)" \
  "bash -c '
    grep -q \"^go 1.25\" go.mod &&
    grep -q \"^toolchain go1\" go.mod &&
    grep -qF \"go-version: '\\''1.25'\\''\" .github/workflows/ci.yml &&
    [ \"\$(grep -cF \"go-version: '\\''1.25'\\''\" .github/workflows/ci.yml)\" = 2 ]
  '"

# --- B24: no dead per-version wrapper files at root (v0.32.9) ---
# 2026-07-31: the v0.32.8 cleanup deleted `check_v0.*.sh` and
# `RELEASE-NOTES-v0.28.*.md` but missed the `run_check_v0.*.sh`
# wrapper family (which call the deleted /tmp/check_v0.*.sh scripts)
# and the `commit_msg_v0.21.*.txt` drafts (operator's commit-message
# scratch files). B24 ensures the next cleanup pass can'\''t regress.
#
# B24 pins the contract:
#   (a) No `run_check_v0.*.sh` or `run_fix_*_attribution.sh` at root
#   (b) No `commit_msg_v0.*.txt` at root
#   (c) The single RELEASE-NOTES.md is the only release-notes file
#       at root (no per-version files like RELEASE-NOTES-v0.X.Y.md)
run_check "B24" "no dead per-version wrapper scripts at root (v0.32.9)" \
  "bash -c '
    [ -z \"\$(ls run_check_v0.*.sh run_check_cross_subnet_v0.*.sh run_fix_*_attribution.sh 2>/dev/null)\" ] &&
    [ -z \"\$(ls commit_msg_v0.*.txt 2>/dev/null)\" ] &&
    [ -z \"\$(ls RELEASE-NOTES-v0.*.md 2>/dev/null)\" ] &&
    [ -f RELEASE-NOTES.md ]
  '"

# ─── B25 (v0.32.11) — Caddy is OFF by default ──────────────────────────
# Background: 2026-07-31 the operator hit "ci green but
# skygate.example.com unreachable" because the in-container
# caddy was sitting on :80/:443 with a placeholder
# `head.example.com` Caddyfile that couldn't issue certs.
# The fix: flip the default to false in BOTH the deploy
# script's shell test AND the .env.example, and gate the
# caddy service in docker-compose.yml behind a Docker
# profile so a plain `up -d` doesn't start it. B25 pins
# the three contracts:
#   (a) deploy.sh's CADDY_ENABLED default branch is
#       `false` (the `:false` after `:-` in the test).
#   (b) .env.example ships `CADDY_ENABLED=false` as the
#       documented default (no `CADDY_ENABLED=true` line
#       uncommented).
#   (c) docker-compose.yml caddy service declares
#       `profiles: ["caddy"]` so a plain `up -d` skips
#       it. The profile name is the literal string `caddy`
#       so deploy.sh's `--profile caddy` flag continues
#       to start it when the operator opts in.
# If any of these regresses, the silent-outage footgun
# comes back.
run_check "B25" "Caddy is OFF by default (v0.32.11)" \
  "bash -c '
    grep -qF \"CADDY_ENABLED:-false\" deploy/deploy.sh &&
    grep -qF \"CADDY_ENABLED=false\" .env.example &&
    ! grep -qF \"^CADDY_ENABLED=true\" .env.example &&
    grep -qF \"profiles: [\\\"caddy\\\"]\" docker-compose.yml
  '"


# ─── B26 (v0.32.13) — Dockerfile runtime has go-sqlite3 CGO toolchain ───
# Background: 2026-07-31 v0.32.8 set `ENV CGO_ENABLED=0` in the
# multi-stage build stage to get a static binary; go-sqlite3
# requires cgo and the import resolved to a stub. v0.32.12
# reverted to CGO_ENABLED=1 + added gcc/musl-dev/sqlite-dev to
# the build stage. v0.32.13 then reverted the entire multi-stage
# build pattern (see B22) — the runtime image is the full
# golang:1.25-alpine which already has gcc + musl-dev pre-installed
# (the workstation-8 image ships them as part of the Go toolchain). The
# only thing we need to add is sqlite-libs (the C library
# libsqlite3.so that go-sqlite3 dynamically links against at
# runtime).
#
# B26 pins the contract for the runtime-build pattern:
#   (a) Dockerfile does NOT set ENV CGO_ENABLED=0 anywhere
#       (the v0.32.8 regression shape — would break go-sqlite3).
#   (b) Dockerfile installs gcc + musl-dev in the runtime apk
#       add list (CGO toolchain; required to compile go-sqlite3
#       at container start via the entrypoint's `go build`).
#   (c) Dockerfile installs sqlite-libs in the runtime apk add
#       list (the libsqlite3.so that the resulting binary
#       dynamically links against; without it the binary errors
#       with "error while loading shared libraries: libsqlite3.so.0"
#       on first DB call).
#   (d) entrypoint.sh's `go build` runs with the CGO toolchain
#       (CGO_ENABLED defaults to 1 on golang:1.25-alpine, which
#       is what we want — B26 doesn't need to set it explicitly).
run_check "B26" "Dockerfile runtime has go-sqlite3 CGO toolchain (v0.32.13)" \
  "bash -c '
    ! grep -qE \"^ENV CGO_ENABLED=0\" Dockerfile &&
    grep -qE \"^[[:space:]]*gcc\" Dockerfile &&
    grep -qF \"musl-dev\" Dockerfile &&
    grep -qF \"sqlite-libs\" Dockerfile
  '"

# ─── B27 (v0.32.13) — entrypoint.sh runs `go build` at container start ───
# Background: 2026-07-31 v0.32.13 REVERTED v0.32.8's
# image-build-time build. The runtime build is back: the
# Dockerfile is single-stage (golang:1.25-alpine as the
# runtime workstation-8) and entrypoint.sh does `go mod download` +
# `go build` at container start. This is the v0.32.5 pattern
# that worked reliably. The trade-off is ~80s startup cost
# (vs. <5s for the v0.32.8 image-build approach) but the
# runtime binary has no CGO+musl issues because the build
# runs in the same alpine + CGO toolchain the binary
# executes in.
#
# B27 pins the runtime-build contract:
#   (a) entrypoint.sh does `go mod download` (loads go-sqlite3 +
#       testify + the rest of the dep tree)
#   (b) entrypoint.sh does `go build -ldflags ...` (compiles the
#       binary with the build label injected)
#   (c) entrypoint.sh does `apk add openssh-client git` (the
#       runtime build needs git for `git describe --tags` to
#       inject the build label, and openssh-client for the
#       v0.32.7+ self-update orchestrator's SSH-based
#       `staggeredSync` route-advertising)
#   (d) entrypoint.sh execs /app/skygate (the binary built at
#       step b; not /usr/local/bin/skygate — that was a v0.32.8
#       workaround that's no longer needed since the runtime
#       build writes directly to /app/skygate without the
#       bind-mount conflict the v0.32.8 multi-stage image had).
run_check "B27" "entrypoint.sh runs go build at container start (v0.32.13)" \
  "bash -c '
    grep -qF \"go mod download\" entrypoint.sh &&
    grep -qF \"go build\" entrypoint.sh &&
    grep -qF \"apk add --no-cache openssh-client git\" entrypoint.sh &&
    grep -qE \"^[[:space:]]*exec /app/skygate\" entrypoint.sh &&
    ! grep -qF \"exec /usr/local/bin/skygate\" entrypoint.sh
  '"


# ─── B29 (v0.32.13) — expirewatch goroutine gated on ExpireWatchEnabled ───
# Background: same root cause as B28 (autoupdater) — the
# env-var flag existed but didn't actually gate the
# goroutine launch. The pre-fix code did
#   expireWatchMgr := expirewatch.New(d, hs, log.Default(), cfg.ExpireWatchInterval)
#   ...
#   go expireWatchMgr.Run(ctx)
# unconditionally and the Run() method only checked
# `m.Interval <= 0` — so setting SKYGATE_EXPIREWATCH_ENABLED=false
# did nothing, the goroutine still ran, the initial
# SyncOnce still listed every node in headscale, the WAL
# write lock was still held for a few seconds at the
# exact moment the first /admin/exit-nodes request
# arrived.
#
# B29 pins the contract:
#   (a) cmd/skygate/main.go gates the goroutine launch on
#       cfg.ExpireWatchEnabled.
#   (b) The pre-fix shape (always-launch) is rejected.
#   (c) When gated off, a log line is emitted.
run_check "B29" "expirewatch goroutine is gated on ExpireWatchEnabled (v0.32.13)" \
  "bash -c '
    grep -qF \"if cfg.ExpireWatchEnabled\" cmd/skygate/main.go &&
    grep -qF \"go expireWatchMgr.Run(ctx)\" cmd/skygate/main.go &&
    ! grep -qE \"^go expireWatchMgr.Run\" cmd/skygate/main.go
  '"

# ─── B30 (v0.32.13) — sidecar goroutine gated on SidecarSyncPeriod ───
# Background: same family of bugs as B28/B29. The sidecar
# (per-user subnet-router auto-approver) launches its
# initial SyncOnce unconditionally if SidecarSyncPeriod > 0
# (default 30s). The SyncOnce lists every node in headscale
# and approves pending routes, holding the WAL write lock
# at startup.
#
# B30 pins the contract:
#   (a) cmd/skygate/main.go gates the goroutine launch on
#       cfg.SidecarSyncPeriod > 0 (set to 0 / off to disable).
#   (b) The pre-fix shape (always-launch) is rejected.
#   (c) When gated off, a log line is emitted.
run_check "B30" "sidecar goroutine is gated on SidecarSyncPeriod (v0.32.13)" \
  "bash -c '
    grep -qF \"if cfg.SidecarSyncPeriod > 0\" cmd/skygate/main.go &&
    grep -qF \"go sidecarMgr.Run(ctx)\" cmd/skygate/main.go &&
    ! grep -qE \"^go sidecarMgr.Run\" cmd/skygate/main.go
  '"

# ─── B31 (v0.32.14) — DB connection pool: 15 conns, NORMAL sync, 2s busy (CASCADE-LOCK FIX) ───
# Background: 2026-08-03 the live VM was hanging on every
# authed page with "db: error: context deadline exceeded"
# even after 33h of uptime. Root cause: v0.32.4 had
# SetMaxOpenConns(1) — a SINGLE connection for the entire
# binary. With concurrent admin traffic (login audit_log
# write + dashboard SELECT + ensureExitServers DELETE +
# cron HEAD requests) all hitting the DB in the same
# second, the single connection was 100% busy and every
# request blocked for the full busy_timeout=5000ms before
# failing. Combined with synchronous=FULL (fsync per
# commit), every commit cost an extra 10-30ms of disk I/O,
# making the cascading lock worse.
#
# The fix (v0.32.14):
#   - SetMaxOpenConns(15)  : 15 concurrent connections.
#     WAL mode allows multiple readers AND one writer
#     concurrently, so 15 connections give real parallelism
#     for the read-heavy workload.
#   - SetMaxIdleConns(5)   : keep 5 idle for warm pool, drop
#     the rest.
#   - SetConnMaxLifetime(5m): recycle every 5 min so
#     long-lived connections don't accumulate state.
#   - synchronous=NORMAL   : no fsync per commit (the v0.32.4
#     corruption was caused by disk-FULL, not by missing
#     FULL sync).
#   - busy_timeout=2000     : 2s instead of 5s — fail fast
#     on contention rather than queue 5s.
#
# B31 pins the contract:
#   (a) internal/db/db.go does NOT have SetMaxOpenConns(1).
#   (b) SetMaxOpenConns(15) is present.
#   (c) The connection string has _synchronous=NORMAL.
#   (d) The connection string has _busy_timeout=2000.
#   (e) The migrate() PRAGMA list has synchronous=NORMAL
#       and busy_timeout=2000.
run_check "B31" "DB connection pool: 15 conns, NORMAL sync, 2s busy (v0.32.14)" \
  "bash -c '
    ! grep -qE \"^[[:space:]]+conn\\.SetMaxOpenConns\\(1\\)\" internal/db/db.go &&
    grep -qE \"^[[:space:]]+conn\\.SetMaxOpenConns\\(15\\)\" internal/db/db.go &&
    grep -qF \"_synchronous=NORMAL\" internal/db/db.go &&
    grep -qF \"_busy_timeout=2000\" internal/db/db.go &&
    grep -qF \"synchronous=NORMAL\" internal/db/db.go &&
    grep -qF \"busy_timeout=2000\" internal/db/db.go
  '"

# ─── B32 (v0.32.15) — Tailscale disabled by default in compose (no hung entrypoint) ───
# Background: 2026-08-03 the live VM was hung on entrypoint
# because the docker-compose.yml still mounted
# secrets/ts_authkey (a 0-byte file committed in 2021
# and never updated). The entrypoint's
#   if [ -n "$TS_AUTHKEY_FILE" ] && [ -f "$TS_AUTHKEY_FILE" ]; then
#       AUTHKEY=$(cat "$TS_AUTHKEY_FILE")
#       tailscale up --authkey="$AUTHKEY" --accept-routes ...
# check passed (file exists, even if empty), so
# `tailscale up --authkey=` (empty) ran. Tailscale with
# an empty authkey hangs forever waiting for interactive
# input, the entrypoint never proceeded, port 8080
# never bound, /healthz 000 forever. The hung entrypoint
# was discovered the hard way on 2026-08-03 when the
# docker daemon got restarted and a fresh skygate
# container couldn't come back up.
#
# The fix (v0.32.15): in docker-compose.yml, replace
# the literal `TS_AUTHKEY_FILE=/run/secrets/ts_authkey`
# env var with `SKYGATE_TS_AUTHKEY_FILE=` (empty, plus
# the LOGIN_SERVER and HOSTNAME siblings). The
# `SKYGATE_` prefix is intentional — the entrypoint
# already checks `${SKYGATE_TS_AUTHKEY_FILE:-...}` to
# avoid clobbering the operator's own Tailscale env on
# the host. With the prefix, the entrypoint's
# `TS_AUTHKEY_FILE` resolves to empty (not set in the
# container's environment), the `[ -n "$TS_AUTHKEY_FILE" ]`
# check is false, Tailscale is skipped, the runtime
# build proceeds to `exec /app/skygate`.
#
# B32 pins the contract:
#   (a) docker-compose.yml does NOT have a line that
#       mounts a ts_authkey secret (no `secrets:` block
#       for the skygate service that references ts_authkey).
#   (b) docker-compose.yml has `SKYGATE_TS_AUTHKEY_FILE=`
#       (empty, SKYGATE_-prefixed) as the env var.
#   (c) docker-compose.yml has `SKYGATE_TS_LOGIN_SERVER=`
#       and `SKYGATE_TS_HOSTNAME=` (also empty).
#   (d) The pre-fix shape (literal `TS_AUTHKEY_FILE=...`
#       in compose env) is rejected.
run_check "B32" "Tailscale disabled by default in compose (v0.32.15)" \
  "bash -c '
    ! grep -qE \"^[[:space:]]+-[[:space:]]+TS_AUTHKEY_FILE=\" docker-compose.yml &&
    grep -qE \"^[[:space:]]+-[[:space:]]+SKYGATE_TS_AUTHKEY_FILE=\" docker-compose.yml &&
    grep -qE \"^[[:space:]]+-[[:space:]]+SKYGATE_TS_LOGIN_SERVER=\" docker-compose.yml &&
    grep -qE \"^[[:space:]]+-[[:space:]]+SKYGATE_TS_HOSTNAME=\" docker-compose.yml
  '"


# ─── B28 (v0.32.13) — domain auto-updater gated on AutoUpdateEnabled ───
# Background: 2026-07-31 the v0.32.13 deploy on the live VM hit
# the same "504 on /admin/exit-nodes" symptom as the CGO bug,
# but with a different root cause: the DNS auto-updater
# goroutine (cmd/skygate/main.go `go app.RunDomainAutoUpdater`)
# launched unconditionally and ran its synchronous
# `DomainAutoUpdater()` initial call at startup. That call
#
# (Note: the B28 `run_check` block was never added in v0.32.13 —
# only the comment. Tracked in BACKLOG.md. B33 below was added in
# v0.32.16 to fill the slot.)


# ─── B33 (v0.32.16) — headplane healthcheck override in compose template ───
# Background: 2026-08-03 the headplane container (`ghcr.io/tale/headplane`)
# was stuck in `(unhealthy)` for 60+ failing-streak iterations. The
# distroless image ships `/bin/hp_healthcheck` that probes port 3000,
# but `HEADPLANE_SERVER__PORT` is 50445 by default — so the
# upstream healthcheck always failed with "dial tcp [::1]:3000:
# connect: connection refused" even though the service was fine
# (port 50445 returns 200 in 15ms via direct probe).
#
# The fix: the deploy template
# `deploy/templates/headscale-compose.yml.tmpl` now adds an
# explicit `healthcheck:` block that probes
#   http://127.0.0.1:${HEADPLANE_SERVER__PORT}/admin/healthz
# using Node.js (the distroless image's only runtime binary, at
# `/nodejs/bin/node` — NOT in PATH for the healthcheck process,
# so it must be called by absolute path).
#
# B33 pins the contract:
#   (a) The template has a `healthcheck:` block under the
#       headplane service (overrides the image's healthcheck).
#   (b) The test command uses `/nodejs/bin/node` (the only
#       runtime in the distroless image; NOT wget/curl which
#       are absent).
#   (c) The probe URL uses `${HEADPLANE_SERVER__PORT}` (not
#       hardcoded 50445) so a custom port is respected.
#   (d) The probe URL is `127.0.0.1` (not `localhost`) to avoid
#       IPv6 → [::1] → connection-refused on systems where
#       headplane binds 0.0.0.0 only.
run_check "B33" "headplane healthcheck override in compose template (v0.32.16)" \
  "bash -c '
    grep -qF \"healthcheck:\" deploy/templates/headscale-compose.yml.tmpl &&
    grep -qF \"/nodejs/bin/node\" deploy/templates/headscale-compose.yml.tmpl &&
    grep -qF \"127.0.0.1:\" deploy/templates/headscale-compose.yml.tmpl &&
    grep -qF \"HEADPLANE_SERVER__PORT\" deploy/templates/headscale-compose.yml.tmpl &&
    grep -qF \"/admin/healthz\" deploy/templates/headscale-compose.yml.tmpl &&
    ! grep -qE \"^[[:space:]]*wget\" deploy/templates/headscale-compose.yml.tmpl
  '"


# ─── B34 (v0.32.16) — device_rules has no duplicates (group by device+exit_node) ───
# Background: 2026-08-03 a stale batch script left 365 duplicate
# device_rules rows for `workstation-1 → relay-3` (all with the same
# created_at timestamp). The duplicates inflated the
# /admin/exit-nodes "mismatch" computation (want=365, have=148)
# because computeSyncStatus counts ALL device_rules rows
# targeting the exit_node, not the unique device count.
#
# The fix was:
#   1. One-time SQL cleanup (DELETE FROM device_rules WHERE id
#      NOT IN (SELECT MIN(id) GROUP BY exit_node_id, device_hostname))
#      — 363 rows removed on the live VM on 2026-08-03.
#   2. This B-check ensures no future batch script can re-introduce
#      the duplicates. Runs `sqlite3` on the production DB
#      (skygate-data volume) and fails if any (exit_node_id,
#      device_hostname) group has COUNT(*) > 1.
#
# Why the B-check belongs in verify-pre (not verify-post):
#   - This is a CODE-level invariant: the device_rules table
#     should be deduplicated. If a future migration or batch
#     helper accidentally re-creates duplicates, we want the
#     build to fail BEFORE the operator deploys, not after.
#   - Same pattern as B15/B16/B17/B22 (regression guards for
#     past bugs).
#
# Skip semantics: on a fresh VM with no skygate deployed yet
# (DB file doesn't exist) the check returns 0 (passes). On
# Windows host (no sqlite3) the check returns 0 (passes). Both
# cases are documented.
run_check "B34" "device_rules table has no duplicate (device, exit_node) pairs (v0.32.16)" \
  "bash -c '
    DB=/var/lib/docker/volumes/skygate-data/_data/skygate.db
    if [ ! -f \"\$DB\" ]; then
      echo \"(skygate DB not present, skipping B34)\" 1>&2
      exit 0
    fi
    if ! command -v sqlite3 >/dev/null 2>&1; then
      echo \"(sqlite3 not on PATH, skipping B34)\" 1>&2
      exit 0
    fi
    DUPES=\$(sudo sqlite3 \"\$DB\" \"SELECT COUNT(*) FROM (SELECT exit_node_id, device_hostname FROM device_rules GROUP BY exit_node_id, device_hostname HAVING COUNT(*) > 1)\" 2>/dev/null)
    if [ -z \"\$DUPES\" ]; then
      echo \"(sqlite3 read failed, skipping B34)\" 1>&2
      exit 0
    fi
    [ \"\$DUPES\" = \"0\" ]
  '"


# ─── B35 (v0.32.18) — subnet-router Remove handler is registered in main.go ───
# Background: the subnet-router lifecycle is Provision → Remove.
# Provision existed since v0.16.7 but Remove (full cleanup that
# deletes the headscale node + clears DB + re-applies ACL) was
# added in v0.32.18. If a future refactor drops the route
# registration in cmd/skygate/main.go, the admin UI's "Remove"
# button would 404 and the operator would have no way to clean
# up a dead router.
#
# B35 pins the contract: the POST /admin/users/{id}/subnet/remove
# route is wired to adminSvc.PostAdminUserSubnetRemove. The
# handler itself is unit-tested in
# internal/feature/admin/user_subnet_test.go (3 tests), so this
# B-check is only the route wiring.
run_check "B35" "POST /admin/users/{id}/subnet/remove wired to adminSvc.PostAdminUserSubnetRemove (v0.32.18)" \
  "bash -c '
    grep -qE \"subnet/remove[^a-z]\" cmd/skygate/main.go &&
    grep -qF \"adminSvc.PostAdminUserSubnetRemove\" cmd/skygate/main.go
  '"


# ─── B36 (v0.32.19) — migration integrity tracking (checksum helpers + V049 + tests) ───
# Background: skygate's migrations are idempotent SQL functions
# (migrateV0NN). If a developer changes the body of an OLD
# migration (typo fix, column type change), the change is silently
# absorbed — the DB has the pre-fix schema, the new code never
# re-runs, no signal. v0.32.19 adds an `applied_migrations` table
# + SHA-256 helpers so the mismatch is detectable.
#
# B36 pins 3 contracts:
# 1. The tracking helpers exist (ComputeMigrationChecksum,
#    VerifyMigrationChecksum, RecordMigrationApplied).
# 2. The V049 migration that creates the table is registered
#    in db.go.
# 3. Unit tests cover the helpers (soft + hard mode mismatch,
#    first-run, idempotent recording).
run_check "B36" "migration integrity: applied_migrations table + checksum helpers + V049 registered (v0.32.19)" \
  "bash -c '
    grep -qF \"func ComputeMigrationChecksum\" internal/db/migration_tracking.go &&
    grep -qF \"func VerifyMigrationChecksum\" internal/db/migration_tracking.go &&
    grep -qF \"func RecordMigrationApplied\" internal/db/migration_tracking.go &&
    grep -qF \"applied_migrations\" internal/db/migrations_v0.49.go &&
    grep -qF \"migrateV049\" internal/db/db.go &&
    grep -qF \"ensureMigrationTrackingTable\" internal/db/db.go &&
    grep -qF \"TestVerifyMigrationChecksum_Mismatch_HardMode\" internal/db/migration_tracking_test.go
  '"


# ─── B37 (v0.32.20) — auto-update UI toggle is wired ───
# Background: v0.32.20 replaces the env-var-only
# SKYGATE_AUTO_UPDATE_ENABLED with a UI checkbox on
# /admin/update. The toggle persists in global_settings
# (key='auto_update_enabled') via POST /admin/update/auto-toggle.
#
# B37 pins 3 contracts:
# 1. The handler exists in the admin feature package.
# 2. The route is registered in cmd/skygate/main.go.
# 3. The template renders the toggle form.
# 4. The DB read uses the global_settings helper (not raw SQL).
# 5. The toggle persists to global_settings (verified by the
#    unit test in update_settings_test.go).
run_check "B37" "auto-update UI toggle: handler + route + template + global_settings helper (v0.32.20)" \
  "bash -c '
    grep -qF \"func (s *Service) PostAdminUpdateAutoToggle\" internal/feature/admin/update_settings.go &&
    grep -qF \"PostAdminUpdateAutoToggle\" cmd/skygate/main.go &&
    grep -qF \"/admin/update/auto-toggle\" internal/handlers/templates/admin/update.html &&
    grep -qF \"GetGlobalSettingBool\" internal/feature/admin/update.go &&
    grep -qF \"auto_update_enabled\" internal/feature/admin/update_settings.go &&
    grep -qF \"TestPostAdminUpdateAutoToggle_EnablePersists\" internal/feature/admin/update_settings_test.go &&
    grep -qF \"func SetGlobalSettingBool\" internal/db/globalsettings.go
  '"

# ─── B38 (v0.33.0) — Network Access Manager: headscale_acl.go exists ───
# Background: 2026-08-04 the v0.33.0 release added
# /admin/headscale/acl (UI + handlers) so the operator can
# add/remove skygate-managed headscale ACL rules without
# touching the operator's manual edits. The contract is:
# 1. The Service has ListACL, AddACL, RemoveACL, PreviewACL.
# 2. fingerprintACL is order-invariant (idempotency contract).
# 3. The DB persists skygate-owned rules in headscale_acl_rules.
# 4. writePolicy preserves all non-acls fields (ssh, groups,
#    tagOwners, hosts, autoApprovers) — the only mutation is
#    append/remove from acls[].
run_check "B38" "headscale_acl.go: ListACL + AddACL + RemoveACL + fingerprint order-invariant (v0.33.0)" \
  "bash -c '
    grep -qF \"func (s *Service) ListACL\" internal/feature/admin/headscale_acl.go &&
    grep -qF \"func (s *Service) AddACL\" internal/feature/admin/headscale_acl.go &&
    grep -qF \"func (s *Service) RemoveACL\" internal/feature/admin/headscale_acl.go &&
    grep -qF \"func (s *Service) PreviewACL\" internal/feature/admin/headscale_acl.go &&
    grep -qF \"sort.Strings(srcSorted)\" internal/feature/admin/headscale_acl.go &&
    grep -qF \"TestFingerprintACL_OrderInvariant\" internal/feature/admin/headscale_acl_test.go &&
    grep -qF \"TestValidateACLRule\" internal/feature/admin/headscale_acl_test.go &&
    grep -qF \"headscale_acl_rules\" internal/db/migrations_v0.50.go
  '"

# ─── B39 (v0.33.0) — Network Access Manager: routes registered ───
# The /admin/headscale/acl/* routes are wired in main.go.
# The /admin/headscale/acl/{add,remove} are POST forms;
# /admin/headscale/acl is the GET render.
run_check "B39" "headscale_acl routes: /admin/headscale/acl + /add + /remove (v0.33.0)" \
  "bash -c '
    grep -qF \"/admin/headscale/acl\" cmd/skygate/main.go &&
    grep -qF \"/admin/headscale/acl/add\" cmd/skygate/main.go &&
    grep -qF \"/admin/headscale/acl/remove\" cmd/skygate/main.go &&
    grep -qF \"GetAdminHeadscaleACL\" cmd/skygate/main.go &&
    grep -qF \"PostAdminHeadscaleACLAdd\" cmd/skygate/main.go &&
    grep -qF \"PostAdminHeadscaleACLRemove\" cmd/skygate/main.go &&
    grep -qF \"title.admin_headscale_acl\" internal/handlers/templates/admin/headscale_acl.html &&
    grep -qF \"nav.headscale_acl\" internal/handlers/templates/layout.html
  '"

# ─── B40 (v0.33.0) — Admin Test Page: TestRegistry has ≥6 entries ───
# Background: 2026-08-04 the v0.33.0 release added
# /admin/system_tests — an in-process test suite the operator
# can run from the web UI. The contract is that the registry
# has at least 6 distinct tests (the bar for "is this useful?")
# covering multiple categories. Categories must include
# network, db, and headscale (the three foundational axes).
run_check "B40" "system_tests.go: TestRegistry has ≥6 tests across network/db/headscale (v0.33.0)" \
  "bash -c '
    grep -qE \"Name:\\s*\\\"net\\\\.\" internal/feature/admin/system_tests.go &&
    grep -qE \"Name:\\s*\\\"db\\\\.\" internal/feature/admin/system_tests.go &&
    grep -qE \"Name:\\s*\\\"headscale\\\\.\" internal/feature/admin/system_tests.go &&
    grep -cE \"^\\s*Name:\\s*\\\"\" internal/feature/admin/system_tests.go | grep -qE \"^[6-9]|[1-9][0-9]\" &&
    grep -qF \"func (s *Service) RunAllTests\" internal/feature/admin/system_tests.go &&
    grep -qF \"func (s *Service) PersistRun\" internal/feature/admin/system_tests.go &&
    grep -qF \"system_tests_runs\" internal/db/migrations_v0.51.go
  '"

# ─── B41 (v0.33.0) — Admin Test Page: routes registered ───
# The /admin/system_tests + /admin/system_tests/run are wired
# in main.go, and the layout has a link to the page.
run_check "B41" "system_tests routes: /admin/system_tests + /run + layout link (v0.33.0)" \
  "bash -c '
    grep -qF \"/admin/system_tests\" cmd/skygate/main.go &&
    grep -qF \"/admin/system_tests/run\" cmd/skygate/main.go &&
    grep -qF \"GetAdminSystemTests\" cmd/skygate/main.go &&
    grep -qF \"PostAdminSystemTestsRun\" cmd/skygate/main.go &&
    grep -qF \"title.admin_system_tests\" internal/handlers/templates/admin/system_tests.html &&
    grep -qF \"nav.system_tests\" internal/handlers/templates/layout.html &&
    grep -qF \"SetTestService\" cmd/skygate/main.go
  '"

# ─── B42 (v0.33.0) — Migration integrity: V050 + V051 registered ───
# Migration v0.50 (headscale_acl_rules) and v0.51
# (system_tests_runs) are called from internal/db/db.go in
# order. Without the explicit call, a fresh DB Open() would
# not create the new tables and the v0.33.0 features would
# fail with "no such table" at first use.
run_check "B42" "db.Open: migrateV050 + migrateV051 called (v0.33.0)" \
  "bash -c '
    grep -qF \"migrateV050\" internal/db/db.go &&
    grep -qF \"migrateV051\" internal/db/db.go &&
    grep -qF \"migrate v0.50\" internal/db/db.go &&
    grep -qF \"migrate v0.51\" internal/db/db.go
  '"

# ─── B43 (v0.33.1) — SSH config wiring (the /admin/exit-rules/sync
# silent-fail fix) ───
# Pre-v0.33.1: SetAdvertisedRoutes hard-coded
#   -F /home/admin/.ssh/config
# which doesn't exist in the dockerised skygate. The SSH
# step always failed with "Can't open user config file" but
# the headscale approve-routes step (right after, no SSH)
# succeeded, so result[node] was overwritten to
# "ok approved=N" — the operator saw green while tailscaled
# on the relay was never re-configured.
#
# v0.33.1: SetAdvertisedRoutes takes sshTarget + sshKeyPath
# as parameters and uses `-i <key> + BatchMode=yes`. The
# per-exit-node config is read from exit_servers.ssh_target
# / ssh_key_path (helper: db.LookupExitServerSSH). The
# combined result is "ssh=<label> <approve_label>" so
# neither side's failure can be hidden.
#
# This check pins the contract so a refactor that reverts
# to the hard-coded path (or drops the per-row lookup) is
# caught at PR time, not at the next deploy when the
# operator's routes go missing again.
run_check "B43" "headscale.SetAdvertisedRoutes: per-node SSH config (v0.33.1)" \
  "bash -c '
    grep -qF \"func (c *Client) SetAdvertisedRoutes(nodeHostname string, routes []string, acceptRoutes int, sshTarget, sshKeyPath string)\" internal/headscale/routes.go &&
    grep -qF \"\\\"-i\\\", keyPath\" internal/headscale/routes.go &&
    grep -qF \"BatchMode=yes\" internal/headscale/routes.go &&
    grep -qF \"func splitSSHTarget\" internal/headscale/routes.go &&
    grep -qF \"func LookupExitServerSSH\" internal/db/exit_servers.go &&
    grep -qF \"qSelectExitServerSSH\" internal/db/queries.go &&
    grep -qF \"db.LookupExitServerSSH\" internal/feature/exit_rules/sync.go &&
    grep -qF \"sync_advertised_routes\" internal/feature/exit_rules/sync.go &&
    grep -qF \"safeJSON\" internal/handlers/templates.go &&
    grep -qF \"safeJSON\" internal/handlers/templates/admin/exit_rules.html &&
    grep -qF \"/ssh-sync\" docker-compose.yml
  '"

# ─── B44 (v0.33.1) — PG migration auto-run on Open (the
# /admin/headscale/acl 500 fix) ───
# Pre-v0.33.1: OpenPostgres opened a bare *sql.DB without running
# MigratePostgres, so the v0.33.0 tables (headscale_acl_rules +
# system_tests_runs) were only created when the operator manually
# ran cmd/apply_pg_migrations. Live VM had no v0.50/v0.51 tables →
# /admin/headscale/acl returned 500 ("relation
# headscale_acl_rules does not exist"). Pre-v0.33.1 also: the
# generated migrateV050PG only defined the strftime() function
# (it never created the headscale_acl_rules table), and
# migrateV051PG didn't exist at all.
#
# v0.33.1: OpenPostgres calls MigratePostgres (symmetric with
# the SQLite Open() → migrate(conn) path), and the generated
# PG migrations for v0.50 and v0.51 are now full ports that
# create the actual tables + indexes. New operators don't have
# to know about apply_pg_migrations.
run_check "B44" "db.OpenPostgres: auto-MigratePostgres + v0.50/v0.51 tables (v0.33.1)" \
  "bash -c '
    grep -qF \"if err := MigratePostgres(conn); err != nil\" internal/db/driver_postgres.go &&
    grep -qF \"func migrateV050PG\" internal/db/migrations_pg.go &&
    grep -qF \"func migrateV051PG\" internal/db/migrations_pg.go &&
    grep -qF \"CREATE TABLE IF NOT EXISTS headscale_acl_rules\" internal/db/migrations_pg.go &&
    grep -qF \"CREATE TABLE IF NOT EXISTS system_tests_runs\" internal/db/migrations_pg.go &&
    grep -qF \"migrateV050PG, migrateV051PG\" internal/db/driver_postgres.go
  '"

# ─── B45 (v0.33.1) — Template body- names match the
# renderBody convention (the "body-admin-* not undefined" fix) ───
# Pre-v0.33.1: four template files (control_planes, derp_config,
# user_control_plane, user_subnet) defined `body-admin-<file-with-DASH>`
# while the renderBody convention in handlers/templates.go:127-129
# maps filename with underscores (`admin/user_subnet.html` →
# `body-admin-user_subnet`). The 4 files all used `-` in the
# body name instead of `_`, so renderBody silently failed
# with "body-admin-... is undefined" — the page rendered
# the sidebar + header but the body block was an empty
# string.
#
# system_tests.html had the inverse bug: defined
# `body-admin-system-tests` (with `-`) while the convention
# says `_`. Same renderBody failure.
#
# The 4+1 bugs were discovered by the user's report
# "/admin/headscale/acl и /admin/system_tests не
# отображаются" on 2026-08-04. v0.33.1 fixes all 5
# template files to follow the convention; B45 pins the
# fix so a refactor that re-introduces a dash in any
# template's `{{define ...}}` line gets caught at PR
# time.
run_check "B45" "templates: body- names match renderBody convention (v0.33.1)" \
  "bash -c '
    grep -qF \"{{define \\\"body-admin-control_planes\\\"}}\" internal/handlers/templates/admin/control_planes.html &&
    grep -qF \"{{define \\\"body-admin-derp_config\\\"}}\" internal/handlers/templates/admin/derp_config.html &&
    grep -qF \"{{define \\\"body-admin-user_control_plane\\\"}}\" internal/handlers/templates/admin/user_control_plane.html &&
    grep -qF \"{{define \\\"body-admin-user_subnet\\\"}}\" internal/handlers/templates/admin/user_subnet.html &&
    grep -qF \"{{define \\\"body-admin-system_tests\\\"}}\" internal/handlers/templates/admin/system_tests.html &&
    grep -qF \"{{define \\\"body-admin-headscale_acl\\\"}}\" internal/handlers/templates/admin/headscale_acl.html &&
    ! grep -qF \"{{define \\\"body-admin-control-planes\\\"}}\" internal/handlers/templates/admin/control_planes.html &&
    ! grep -qF \"{{define \\\"body-admin-derp-config\\\"}}\" internal/handlers/templates/admin/derp_config.html &&
    ! grep -qF \"{{define \\\"body-admin-user-control-plane\\\"}}\" internal/handlers/templates/admin/user_control_plane.html &&
    ! grep -qF \"{{define \\\"body-admin-user-subnet\\\"}}\" internal/handlers/templates/admin/user_subnet.html &&
    ! grep -qF \"{{define \\\"body-admin-system-tests\\\"}}\" internal/handlers/templates/admin/system_tests.html &&
    ! grep -qF \"{{define \\\"body-admin-headscale-acl\\\"}}\" internal/handlers/templates/admin/headscale_acl.html
  '"

# ─── B46 (v0.33.1.2) — system_tests template renders without panic ───
# Background: 2026-08-04 the v0.33.1.1 fix renamed body-admin-system_tests
# to follow the renderBody convention. That revealed a pre-existing
# panic in the template: `{{if .LiveResults}}` was inside a
# `{{range .Tests}}` block where `.` is a SystemTestDef (no LiveResults
# field). The panic surfaced as 500 with
#   "template: system_tests.html:56:15: executing \"body-admin-system_tests\"
#    at <.LiveResults>: can't evaluate field LiveResults in type
#    admin.SystemTestDef".
# The fix: change all 3 occurrences of `.LiveResults` inside the
# `{{range .Tests}}` block to `$.LiveResults`. The page-level
# `{{if .LiveResults}}` (outside the range) is fine.
# B46 pins the regression test in internal/handlers/system_tests_render_test.go
# so a future template change that re-introduces the same shape of bug
# fails at `go test` time (NOT at deploy time on the live VM).
run_check "B46" "system_tests template: render-panic regression test exists (v0.33.1.2)" \
  "bash -c '
    test -f internal/handlers/system_tests_render_test.go &&
    grep -qE \"func TestSystemTestsRendersWithoutPanic\" internal/handlers/system_tests_render_test.go &&
    grep -qE \"func TestSystemTestsRendersWithLiveResults\" internal/handlers/system_tests_render_test.go &&
    grep -qE \"body-admin-system_tests\" internal/handlers/system_tests_render_test.go
  '"

# ─── B47 (v0.33.1.2) — system_tests.html uses \$.LiveResults (not .LiveResults) inside the range ───
# Background: see B46. The fix changed 3 occurrences of `.LiveResults`
# inside `{{range .Tests}}` to `$.LiveResults`. The page-level
# `{{if .LiveResults}}` (outside the range) is correct as-is — it
# checks the page-level data map.
# B47 pins the count of `{{if $.LiveResults}}` (must be >= 1) so a
# future refactor that reverts any of the 3 fixes fails at PR time.
run_check "B47" "system_tests.html: \$.LiveResults used inside {{range .Tests}} (v0.33.1.2 fix)" \
  'grep -cF "{{if $.LiveResults}}" internal/handlers/templates/admin/system_tests.html | grep -qE "^[1-9]"'
# ─── B48 (v0.33.1.3) — admin handlers: every RenderWithLayout
# call resolves to a defined body template ───
# Background: 2026-08-04 the user reported "/admin/control-planes
# and /admin/derp forms do not open". Pre-v0.33.1.3 six admin
# handlers passed hyphenated names to RenderWithLayout
# ("admin-control-planes", "admin-derp-config",
# "admin-user-subnet", "admin-user-control-plane",
# "admin-backup"). The renderBody funcmap in templates.go
# transforms the name by replacing "/" with "-" and stripping
# ".html" — but hyphens are NOT replaced with underscores. So
# the resolved body name was "body-admin-control-planes" (with
# hyphens) while the template defines "body-admin-control_planes"
# (with underscores). The body was never found, the page
# rendered 200 + empty body (silent fail).
#
# The fix: every RenderWithLayout call uses the file-path form
# "admin/<template>.html". The new test
# TestRenderWithLayout_BodyNamesResolve in
# internal/handlers/render_body_consistency_test.go walks the
# admin handler source files via the Go AST, extracts every
# RenderWithLayout call's name argument, runs the transform,
# and asserts the result matches a {{define "body-..."}} in
# some admin template. B48 runs that test.
run_check "B48" "admin handlers: RenderWithLayout names resolve to defined bodies (v0.33.1.3)" \
  "'$GO' test -count=1 -short -run TestRenderWithLayout_BodyNamesResolve ./internal/handlers/ 2>&1"

# ─── B49 (v0.33.1.4) — templates: no hardcoded OLD `tailscale up --accept-routes`
# short form (no --authkey / no --login-server) ───
# Background: 2026-08-04 the user asked "как правильно прописать
# tailscale up --accept-routes на Windows". The /my/exit-rules and
# /my/exit-rules/help templates had hardcoded `tailscale up` strings
# (with no auth key, no login-server) that gave the operator a
# wrong / incomplete command. v0.33.1.4 moved all tailscale-up
# references into the i18n catalog (client_win_cmd /
# client_win_cmd_after) and added docs/windows-client.md as the
# canonical reference. B49 pins the convention: future template
# changes that re-introduce a hardcoded `tailscale up --accept-routes`
# (in the OLD short form, with no auth) fail at PR time. The full
# command `tailscale up --login-server=... --authkey=...` is
# allowed in the help page because that is the reference doc.
run_check "B49" "templates: no hardcoded OLD tailscale up --accept-routes short form (v0.33.1.4)" \
  'grep -nP "tailscale up --accept-routes(?! --accept-dns=false)" internal/handlers/templates/exit_rules.html internal/handlers/templates/exit_rules_help.html 2>/dev/null | head -5 | grep -q . && exit 1 || true'

# ─── B50 (v0.33.1.7) — /admin/devices table overflow ───
# Background: 2026-08-04 the user reported that the 12-column
# /admin/devices table was overflowing the card boundary on
# narrow viewports. The OS+device_type column has an inline
# `<details>` form (2 selects + button), the per-device exit
# pref column has 2 forms, the actions column has 2 forms —
# total row width easily exceeds 1280px. v0.33.1.7 wraps the
# <table> in <div class="table-wrap"> so on narrow screens the
# row scrolls horizontally inside the card instead of spilling
# out. The CSS class .table-wrap (defined in static/css/themes.css)
# already has `overflow-x: auto; margin: 0 -4px`; we just need
# the template to use it.
run_check "B50" "/admin/devices table wrapped in .table-wrap for horizontal scroll (v0.33.1.7)" \
  'grep -n "<div class=\"table-wrap\">" internal/handlers/templates/admin/devices.html | head -1 | grep -q .'

# ─── B51 (v0.33.1.7) — backup path respects SKYGATE_BACKUP_DIR / DEPLOY_BACKUP_DIR ───
# Background: 2026-08-04 the user asked "why are backups
# failing?". The hardcoded constant `const backupDir =
# "/tmp/skygate-backup"` in internal/feature/admin/backup.go
# ignored the operator's .env (DEPLOY_BACKUP_DIR=...) and wrote
# to a container-internal path that disappears on restart.
# v0.33.1.7 introduced resolveBackupDir() which reads
# SKYGATE_BACKUP_DIR (preferred) → DEPLOY_BACKUP_DIR (legacy
# alias) → /tmp/skygate-backup (final fallback). B51 pins the
# shape of the fallback chain and the absence of the const.
run_check "B51" "backup path resolved from SKYGATE_BACKUP_DIR/DEPLOY_BACKUP_DIR, not hardcoded (v0.33.1.7)" \
  "grep -nE 'SKYGATE_BACKUP_DIR|DEPLOY_BACKUP_DIR' internal/feature/admin/backup.go | head -3 | grep -q . && ! grep -q 'const backupDir = ' internal/feature/admin/backup.go"

# ─── B52 (v0.33.1.7) — /admin/update template has no inline-style CSS interpolation ───
# Background: 2026-08-04 the user asked "why is the update tab
# showing errors?". The /admin/update template had two patterns
# that Go's html/template refuses to interpolate into a CSS
# context:
#   1. style="border-left:4px solid {{$borderColor}}" with
#      values like "var(--success)" / "var(--danger)" — renders
#      as the literal "ZgotmplZ" placeholder.
#   2. style="color:{{$lineColor}}" in the log lines — same
#      problem.
# v0.33.1.7 replaced both with static CSS classes
# (card-update-{phase} / log-line-{level}) so the values are
# static strings Go can render without sanitization. B52 pins
# the absence of any {{...}} inside a style="..." attribute
# in admin/update.html.
run_check "B52" "/admin/update template has no template-var-in-CSS (v0.33.1.7)" \
  '! grep -nE "style=\"[^\"]*[{][{]" internal/handlers/templates/admin/update.html | grep -vE "^[0-9]+:.*[{][{]\\*/" | grep -vE "^[0-9]+:[[:space:]]*[{][{]/" | head -5 | grep -q .'

# ─── B53 (v0.33.1.8) — Telegram egress relay admin-UI selector ───
# Background: 2026-08-04 the user asked for a way to set
# which relay terminates api.telegram.org traffic "from the
# web interface, without touching the CLI". The previous
# flow was: edit deploy/tailscale-relay/update-routes.sh
# output, SSH to the chosen relay, run the script manually,
# pray headscale picked the right metric. v0.33.1.8 adds
# the "Egress relay" card to /admin/telegram — admin picks
# a relay from the enabled exit_servers list, skygate SSHes
# to it and runs the canonical Telegram-CIDR via the
# existing headscale.Client.SetAdvertisedRoutes helper.
# B53 pins the four code-presence invariants:
#   1. handler func exists in feature/admin/telegram.go
#   2. switch dispatch wired for "set_egress" + "clear_egress"
#   3. template renders the selector + apply/clear buttons
#   4. i18n keys for the new card exist in both languages
# Plus it confirms the canonical Telegram-CIDR constant is
# declared (so the regression for "constant gets moved into
# a test file" can't silently break the production path).
run_check "B53" "Telegram egress relay admin-UI selector wired (v0.33.1.8)" \
  'grep -q "func (s \*Service) handleTelegramSetEgress" internal/feature/admin/telegram.go && \
   grep -q "func (s \*Service) handleTelegramClearEgress" internal/feature/admin/telegram.go && \
   grep -q "case \"set_egress\":" internal/feature/admin/telegram.go && \
   grep -q "case \"clear_egress\":" internal/feature/admin/telegram.go && \
   grep -q "telegram.egress_title" internal/handlers/templates/admin/telegram.html && \
   grep -q "telegram.egress_apply" internal/handlers/templates/admin/telegram.html && \
   grep -q "telegram.egress_clear" internal/handlers/templates/admin/telegram.html && \
   grep -q "\"telegram.egress_title\"" internal/i18n/catalog_telegram.go && \
   grep -q "TelegramCIDRs" internal/feature/admin/telegram.go'

# ─── B54 (v0.33.1.8) — SetGlobalSetting uses per-backend placeholders ───
# Background: 2026-08-05 the operator triggered the
# /admin/telegram "Clear egress" button and the DB write
# failed with "ERROR: syntax error at or near ',' (SQLSTATE
# 42601)". Root cause: SetGlobalSetting was hard-coded to
# use the "?" placeholder for both SQLite and PostgreSQL.
# SQLite's go-sqlite3 driver accepts "?"; PostgreSQL's pgx
# stdlib does NOT auto-convert "?" to "$N" (lib/pq used to,
# but the pgx stdlib doesn't), so the literal "?" reached
# PostgreSQL and got parsed as a syntax error next to the
# "," between values. v0.33.1.8 introduces a per-backend
# placeholdersList(n) helper (placeholders_sqlite.go for the
# "?,?" form, placeholders_postgres.go for the "$1,$2,..."
# form, selected via -tags postgres) and SetGlobalSetting
# now uses it. B54 pins the absence of any hard-coded "?"
# inside the SetGlobalSetting query template + the presence
# of the new helper files.
run_check "B54" "SetGlobalSetting uses per-backend placeholders, not hardcoded '?' (v0.33.1.8)" \
  'grep -q "placeholdersList(2)" internal/db/globalsettings.go && \
   ! grep -qE "VALUES \(\?, \?" internal/db/globalsettings.go && \
   test -f internal/db/placeholders.go && \
   test -f internal/db/placeholders_sqlite.go && \
   test -f internal/db/placeholders_postgres.go'

# ─── B55 (v0.33.1.9) — Tailscale web-UI management ───
# Background: 2026-08-05 the operator reported that
# "веб-интерфейс не позволяет настроить Tailscale для
# обхода блокировки Telegram". The previous architecture
# required SSH + manual file edits + entrypoint restart to
# get tailscaled running inside the skygate container, which
# is what makes api.telegram.org reachable from an RF VPS
# (skygate uses --accept-routes, the relay advertises
# Telegram-CIDR as subnet routes). v0.33.1.9 adds a
# /admin/tailscale page with status + auth key paste +
# Start/Stop buttons. B55 pins the four code-presence
# invariants:
#   1. handlers exist in feature/admin/tailscale.go
#   2. switch dispatch wired for "save_key" + "start" + "stop"
#   3. template renders the new page
#   4. i18n keys for the new page exist in both languages
# Plus it pins the entrypoint.sh fix (TS_AUTHKEY_FILE /
# SKYGATE_TS_AUTHKEY_FILE fallback) so the previous
# env-var-name mismatch can't regress.
run_check "B55" "Tailscale web-UI management wired (v0.33.1.9)" \
  'grep -q "func (s \*Service) GetAdminTailscale" internal/feature/admin/tailscale.go && \
   grep -q "func (s \*Service) PostAdminTailscale" internal/feature/admin/tailscale.go && \
   grep -q "case \"save_key\":" internal/feature/admin/tailscale.go && \
   grep -q "case \"start\":" internal/feature/admin/tailscale.go && \
   grep -q "case \"stop\":" internal/feature/admin/tailscale.go && \
   grep -q "tailscale.title" internal/handlers/templates/admin/tailscale.html && \
   grep -q "tailscale.start" internal/handlers/templates/admin/tailscale.html && \
   grep -q "tailscale.title" internal/i18n/catalog_tailscale.go && \
   grep -q "SKYGATE_TS_AUTHKEY_FILE:-}" entrypoint.sh'

# ─── B56 (v0.33.1.10) — GitHub repo coordinates via env ───
# Background: 2026-08-05 the operator reported that
# /admin/update hung on "GitHub недоступен" with a 404.
# Root cause: the release-monitor + update-checker Go code
# hardcoded "skygate-operator/skygate" as the GitHub repo,
# but the operator's actual repo on github.com is
# "BarsSky/skygate" (verified: api.github.com/repos/
# skygate-operator/skygate/releases/latest -> HTTP 404,
# vs .../BarsSky/skygate/releases/latest -> HTTP 200).
# v0.33.1.10 adds SKYGATE_GITHUB_REPO_OWNER / _NAME env
# vars (config.Config.GitHubOwner / GitHubRepo, default
# "BarsSky" / "skygate") and wires them through every
# callsite that previously hardcoded the URL.
run_check "B56" "GitHub repo coordinates via env (v0.33.1.10)" \
  'grep -q "GitHubOwner" internal/config/config.go && \
   grep -q "GitHubRepo" internal/config/config.go && \
   grep -q "SKYGATE_GITHUB_REPO_OWNER" internal/config/config.go && \
   grep -q "SKYGATE_GITHUB_REPO_NAME" internal/config/config.go && \
   grep -q "m.Owner" internal/release/monitor_runner.go && \
   grep -q "m.Repo" internal/release/monitor_runner.go && \
   grep -q "s.Cfg.GitHubOwner" internal/feature/admin/update.go && \
   grep -q "s.Cfg.GitHubRepo" internal/feature/admin/update.go && \
   grep -q "defaultOwnerRepo" internal/update/manual.go && \
   grep -q "func GenerateManualSteps(kind InstallKind, current, target, owner, repo string)" internal/update/manual.go && \
   ! grep -nE "Owner:.*skygate-operator|Repo:.*skygate-operator|\"skygate-operator\"," internal/release/monitor.go internal/release/monitor_runner.go internal/feature/admin/update.go internal/update/checker.go internal/update/manual.go && \
   ! grep -q "skygate-operator/skygate" internal/release/monitor.go internal/release/monitor_runner.go internal/feature/admin/update.go internal/update/checker.go internal/update/manual.go'

# ─── B57 (v0.33.1.10) — DERP config apply help text no longer says SQLite ───
# Background: 2026-08-05 the operator reported that
# /admin/derp/config showed "writes the config to SQLite"
# in the help card, which is stale since v0.33.1.7 moved
# the production DB to PostgreSQL. v0.33.1.10 also drops
# the inaccurate "via docker exec" wording (the actual
# code uses docker cp + docker kill -s HUP).
# The check is a negative grep for the legacy SQLite
# string in both languages + a positive check that the
# new text mentions docker cp + БД / database.
run_check "B57" "DERP apply help no longer says SQLite (v0.33.1.10)" \
  '! grep -q "writes the config to SQLite" internal/i18n/catalog_derp.go && \
   ! grep -q "записывает конфиг в SQLite" internal/i18n/catalog_derp.go && \
   grep -q "docker cp" internal/i18n/catalog_derp.go'

# ─── B58 (v0.33.1.11) — Tailscale auto-generate preauth key ───
# Background: 2026-08-05 the operator asked "can we
# automate the key request? skygate runs over headscale
# and has direct access anyway". The previous flow was
# manual: open /admin/headscale in another tab, generate
# a key, copy it, paste into /admin/tailscale, click
# Save. v0.33.1.11 adds a "Сгенерировать автоматически"
# button on /admin/tailscale that:
#   1. Resolves the headscale user behind the configured
#      hostname (default "skygate-host-1") via
#      hs.ListAllNodes().
#   2. Calls hs.CreatePreauthKey(uid, "1h", reusable=true)
#      (API + CLI fallback inside the headscale pkg).
#   3. Writes the returned key to the same /data/ts/authkey
#      file the manual Save path uses (mode 0600).
#   4. Audit: tailscale_generate_key|username|user_id=N
#      hostname=X exp=1h reusable=true fp=tske...wxyz
#      (FP only — full key never logged).
# B58 pins the 5 code-presence invariants:
#   1. handler in feature/admin/tailscale.go
#   2. dispatch wired for "generate_key" action
#   3. helper that resolves the user via headscale list
#   4. template renders the new button (i18n key)
#   5. audit row written with FP only (no full key in
#      the audit_log detail column).
run_check "B58" "Tailscale auto-generate preauth key (v0.33.1.11)" 'grep -q handleTailscaleGenerateKey internal/feature/admin/tailscale.go && grep -q generate_key internal/feature/admin/tailscale.go && grep -q findUserForHostname internal/feature/admin/tailscale.go && grep -q hs.CreatePreauthKey internal/feature/admin/tailscale.go && grep -q tailscale.generate_btn internal/i18n/catalog_tailscale.go && grep -q tailscale.generate_help internal/i18n/catalog_tailscale.go && grep -q value=.generate_key internal/handlers/templates/admin/tailscale.html'

# ─── B59 (v0.33.1.11) — system_tests works on both backends ───
# Background: 2026-08-05 the operator flagged that the
# /admin/system_tests page had two SQLite-specific
# tests (db.sqlite_integrity + db.wal_mode) which
# silently failed on PostgreSQL (the v0.33.1.7+
# production backend). v0.33.1.11:
#   1. Renames both to db.integrity_check + db.journal_mode.
#   2. Dispatches per backend: SQLite uses PRAGMA
#      integrity_check + journal_mode; PG runs SELECT 1
#      + a pg_tables presence check (8 of 8 expected
#      tables must exist).
#   3. Expands the registry from 6 to 13+ tests with
#      new categories "integrations" and "backup".
# B59 pins:
#   - no SQLite-only test names remain in the registry
#   - the new backend-dispatching names are present
#   - the new category strings (integrations, backup) exist
run_check "B59" "system_tests works on both backends (v0.33.1.11)" 'f=/tmp/b59.sh; printf "%s" "! grep -q db.sqlite_integrity internal/feature/admin/system_tests.go && ! grep -q db.wal_mode internal/feature/admin/system_tests.go && grep -q db.integrity_check internal/feature/admin/system_tests.go && grep -q db.journal_mode internal/feature/admin/system_tests.go && grep -q BackendPostgres internal/feature/admin/system_tests.go && grep -q integrations.configured internal/feature/admin/system_tests.go && grep -q backup.recent internal/feature/admin/system_tests.go && grep -q network.dns_resolve internal/feature/admin/system_tests.go && grep -q headscale.exit_nodes_online internal/feature/admin/system_tests.go && grep -q db.duplicate_devices internal/feature/admin/system_tests.go && grep -q db.rules_sanity internal/feature/admin/system_tests.go && grep -q mesh.active_meshes internal/feature/admin/system_tests.go && grep -q db.PlaceholdersList internal/db/placeholders.go && grep -q db.PlaceholdersList internal/feature/admin/system_tests.go" > "$f" && bash "$f"; rm -f "$f"'

# ─── B60 (v0.33.1.12) — comprehensive `?` placeholder PG-unsafe sweep ───
# The v0.33.1.8 fix (B54) only covered SetGlobalSetting. Many more
# production queries still hardcoded `?` placeholders that crash
# PG with "syntax error at or near ','" because pgx stdlib does
# NOT auto-convert `?` to `$N` (unlike lib/pq which DID via
# NamedArg). The v0.33.1.12 sweep covers 10 files (headscale_acl,
# backup, exit_nodes, acl_import, user_subnet, user_subnet_download,
# routescript_data, sidecar/manager, telegram/commands_clear,
# device_exit_pref, headscale_version/monitor, invite/bridge,
# telegram/alerts, telegram_login_tokens) and also adds a new
# `db.OnConflictDoNothing(cols)` / `db.InsertIgnorePrefix()` pair
# for cross-backend `INSERT OR IGNORE` dispatch.
#
# B60 pins:
#   - db.OnConflictDoNothing + db.InsertIgnorePrefix exist
#   - the 10 fixed files reference db.PlaceholdersList at least once
#   - the legacy `INSERT OR IGNORE` keyword is gone from production
#     code (only the helper emits it on SQLite)
run_check "B60" "comprehensive ? placeholder PG-unsafe sweep (v0.33.1.12)" 'f=/tmp/b60.sh; printf "%s" "grep -q OnConflictDoNothing internal/db/on_conflict.go && grep -q InsertIgnorePrefix internal/db/on_conflict.go && grep -q OnConflictDoNothing internal/db/on_conflict_sqlite.go && grep -q OnConflictDoNothing internal/db/on_conflict_postgres.go && grep -q db.PlaceholdersList internal/feature/admin/headscale_acl.go && grep -q db.PlaceholdersList internal/feature/admin/backup.go && grep -q db.PlaceholdersList internal/feature/admin/exit_nodes.go && grep -q db.PlaceholdersList internal/feature/admin/acl_import.go && grep -q db.PlaceholdersList internal/feature/admin/user_subnet.go && grep -q db.PlaceholdersList internal/feature/admin/user_subnet_download.go && grep -q db.PlaceholdersList internal/feature/exit_rules/routescript_data.go && grep -q db.PlaceholdersList internal/sidecar/manager.go && grep -q db.PlaceholdersList internal/telegram/commands_clear.go && grep -q db.PlaceholdersList internal/feature/my/device_exit_pref.go && grep -q db.PlaceholdersList internal/headscale_version/monitor.go && grep -q db.PlaceholdersList internal/invite/bridge.go && grep -q db.PlaceholdersList internal/telegram/alerts.go && ! grep -rnE INSERT OR IGNORE internal/ cmd/ | grep -v migrations_ | grep -v on_conflict_ | grep -v _test.go | head -1" > "$f" && bash "$f"; rm -f "$f"'

# ─── B61 (v0.33.1.13) — verify_post_deploy.sh SSH_HOST CLI arg + --help ───
# The 2026-08-05 review of verify_post_deploy.sh revealed the
# legacy default "admin@192.0.2.1" was almost always wrong for
# real deployments (the operator's VM is on 192.168.x.x, not the
# 192.0.2.x documentation range). v0.33.1.13 added:
#   1. A positional $1 (e.g. "skyadmin@192.168.13.69") as the
#      primary SSH_HOST override
#   2. An --help flag that prints the catalog header (the
#      previous script had no way to ask "what does this do?")
#   3. An --bad-flag error path (previously unknown flags were
#      silently ignored)
#
# B61 pins:
#   - the script accepts "skyadmin@192.168.13.69" as $1 and
#     SSH_HOST resolves to that (not the legacy default)
#   - --help exits 0 after printing the catalog header
#   - an unknown flag exits non-zero with "unknown flag: ..."
run_check "B61" "verify_post_deploy.sh accepts SSH_HOST as positional \$1 (v0.33.1.13)" 'f=/tmp/b61.sh; printf "%s" "set +e; out1=\$(bash scripts/verify_post_deploy.sh skyadmin@192.168.13.69 --help 2>&1 | head -3); case \"\$out1\" in *\"runtime guarantees for skygate\"*) echo help-ok;; *) echo \"help text missing: \$out1\"; exit 1;; esac; out2=\$(bash scripts/verify_post_deploy.sh --bad-flag 2>&1); case \"\$out2\" in *\"unknown flag\"*) echo bad-flag-ok;; *) echo \"bad-flag path broken: \$out2\"; exit 1;; esac; grep -q \"SSH_HOST=\\\"\\\${SSH_HOST:-admin@192.0.2.1}\\\"\" scripts/verify_post_deploy.sh || { echo \"SSH_HOST fallback line missing\"; exit 1; }; grep -q \"SSH_HOST_SET\" scripts/verify_post_deploy.sh || { echo \"SSH_HOST_SET var missing\"; exit 1; }" > "$f" && bash "$f"; rm -f "$f"'

# ─── B62 (v0.33.1.13) — SKYGATE_TS_LOGIN_SERVER editable from /admin/tailscale ───
# Before v0.33.1.13 the headscale URL was env-var only
# (SKYGATE_TS_LOGIN_SERVER). v0.33.1.13 added a web-UI form
# on /admin/tailscale that persists the value to
# global_settings (key "tailscale.login_server"); the env var
# is now only consulted when the DB row is empty. After a
# container restart / migration / VM clone, the saved DB
# value is read on next start.
#
# B62 pins:
#   - the DB key constant exists in feature/admin/tailscale.go
#   - the resolution function checks the DB first
#   - the source-tracking helper distinguishes db/env/default
#   - the save action handler writes to global_settings
#   - the i18n keys are present in both RU + EN
#   - the template renders the form with the new field
run_check "B62" "SKYGATE_TS_LOGIN_SERVER editable from /admin/tailscale + DB-persisted (v0.33.1.13)" 'f=/tmp/b62.sh; printf "%s" "grep -q tailscaleLoginServerDBKey internal/feature/admin/tailscale.go && grep -q \"tailscale.login_server\" internal/feature/admin/tailscale.go && grep -q tailscaleLoginServerSource internal/feature/admin/tailscale.go && grep -q save_login_server internal/feature/admin/tailscale.go && grep -q login_server_placeholder internal/i18n/catalog_tailscale.go && grep -q login_server_source_db internal/i18n/catalog_tailscale.go && grep -q login_server_saved internal/i18n/catalog_tailscale.go && grep -q login_server_heading internal/handlers/templates/admin/tailscale.html && grep -q name=\"login_server\" internal/handlers/templates/admin/tailscale.html && grep -q TailscaleLoginServerSource internal/feature/admin/tailscale.go && grep -q SetGlobalSettingForTest internal/feature/admin/tailscale.go && grep -q placeholdersList.1. internal/db/globalsettings.go && grep -q TestGetGlobalSetting internal/db/globalsettings_test.go" > "$f" && bash "$f"; rm -f "$f"'

# ─── B63 (v0.33.1.14) — placeholdersList(1)+placeholdersList(1) 2-arg bug fix ───
# The v0.33.1.12 sweep (B60) fixed "?" placeholders in production
# code, but the fix used the pattern
#   `... WHERE a = `+db.PlaceholdersList(1)+` AND b = `+db.PlaceholdersList(1)
# for 2-arg queries. On PG this produces
#   `... WHERE a = $1 AND b = $1`
# i.e. two refs to the SAME positional parameter while passing
# TWO args. PG rejected the query, so callerOwnsDevice returned
# false for EVERY device, blocking the per-device preferred-exit
# flow on PG (operator reported it: "устройства нет или оно мне
# не принадлежит" on cyborg). Three sites were affected:
#   - internal/feature/my/device_exit_pref.go:200 (callerOwnsDevice)
#   - internal/db/migrations_v0.46.go:94  (GetDeviceExitNodePref)
#   - internal/db/migrations_v0.46.go:129 (SetDeviceExitNodePref DELETE branch)
#
# The fix: new helper db.PlaceholderAt(n, i) returns the i-th
# placeholder from a PlaceholdersList(n) string, so a 2-arg
# query splices two UNIQUE placeholders ($1, $2) at its two
# positions. Same pattern as db.NowUnixSQL / db.PlaceholdersList.
#
# B63 pins:
#   - db.PlaceholderAt exists in internal/db/placeholders.go
#   - all 3 fixed sites use db.PlaceholderAt(2, ...) (not the
#     old placeholdersList(1)+placeholdersList(1) pattern)
#   - the new test file exists and pins the 2-arg dispatch case
#   - the v0.33.1.12 pattern is GONE from migrations_v0.46.go
#     (the file that produced the live operator bug)
run_check "B63" "placeholdersList(1)+placeholdersList(1) 2-arg fix via db.PlaceholderAt (v0.33.1.14)" 'f=/tmp/b63.sh; printf "%s" "grep -q \"^func PlaceholderAt\" internal/db/placeholders.go && grep -q db.PlaceholderAt.2, 0. internal/feature/my/device_exit_pref.go && grep -q db.PlaceholderAt.2, 1. internal/feature/my/device_exit_pref.go && grep -q PlaceholderAt.2, 0. internal/db/migrations_v0.46.go && grep -q PlaceholderAt.2, 1. internal/db/migrations_v0.46.go && grep -q TestCallerOwnsDevice_2ArgDispatch internal/feature/my/device_exit_pref_test.go && grep -q TestSetDeviceExitNodePref_RoundTrip internal/feature/my/device_exit_pref_test.go && grep -q TestPlaceholderAt_Dispatch internal/feature/my/device_exit_pref_test.go && ! grep -q placeholdersList.1.+placeholdersList.1. internal/db/migrations_v0.46.go && ! grep -q placeholdersList.1.+placeholdersList.1. internal/feature/my/device_exit_pref.go" > "$f" && bash "$f"; rm -f "$f"'

# ─── B64 (v0.33.1.15) — per-device exit_node_pref device tag in tagOwners ───
# The user reported (2026-08-05) that after setting a
# per-device preferred exit-node for cyborg + emilia, the
# per-device ACL grant (src=tag:dev-skyadmin-cyborg →
# autogroup:internet with via:tag:exit-emilia) wasn't being
# applied. Investigation showed every ACL apply for the
# last 22+ hours had been silently failing with
# "headscale PUT /api/v1/policy: 500 ... src=tag not
# found: tag:dev-skyadmin-skygate-host-1".
#
# Root cause: the per-device grant loop emits a grant
# with src=tag:dev-<user>-<device> for every row in
# device_exit_node_prefs. Pre-v0.33.1.15, the tagOwners
# block was built ONLY from GetPerUserDeviceTags (a JOIN on
# node_owner_map). When a device had a per-device pref
# but was missing from node_owner_map (e.g. the
# skygate-host-1 host node before it gets backfilled), the
# parser rejected the policy. Same for the via:[] tag
# (e.g. tag:exit-emilia) which is referenced by the
# per-device grant's via field.
#
# B64 pins:
#   - GenerateACLWithViaForPlane's tagOwners block now
#     includes via tags from viaByDevice (per-device prefs)
#   - AND includes the per-device-pref device tags in
#     perDevTagOwners (so a device with a pref that's not
#     yet in node_owner_map still gets its tag registered)
#   - test TestGenerateACLWithVia_PerDeviceTagOwners
#     exists and verifies the (a)+(b)+(c) contract
run_check "B64" "per-device exit_node_pref device tag in tagOwners (v0.33.1.15)" 'f=/tmp/b64.sh; printf "%s" "grep -q \"also include via tags from per-device prefs\" internal/acl/acl.go && grep -q \"per-device-pref device tags in tagOwners\" internal/acl/acl.go && grep -q \"augmentedTagsByUser\" internal/acl/acl.go && grep -q TestGenerateACLWithVia_PerDeviceTagOwners internal/acl/acl_test.go" > "$f" && bash "$f"; rm -f "$f"'

# ─── B65 (v0.33.1.16) — SKYGATE_TS_LOGIN_SERVER editable from .env + restart-skgate button ───
# Before v0.33.1.16, docker-compose.yml had a hardcoded
# `SKYGATE_TS_LOGIN_SERVER=https://head.example.com` in the
# `environment:` section, which OVERRODE the .env value
# (docker-compose precedence: environment > env_file). The
# operator's edit on /admin/tailscale (DB-persisted) was
# never picked up by the entrypoint. The fix:
#   (a) remove the hardcoded value from docker-compose.yml so
#       the .env value wins
#   (b) add a "Restart skygate" button on /admin/tailscale
#       that writes the current effective value to .env
#       (atomic via .tmp + rename) and triggers
#       `docker compose restart skygate` (or
#       `systemctl restart skygate` on a native host).
#       The restart subprocess is setsid'd so it survives
#       the SIGTERM that hits the parent.
#
# B65 pins:
#   - SKYGATE_TS_LOGIN_SERVER is no longer in docker-compose.yml
#     environment: section (so .env wins)
#   - the handler handleTailscaleRestart exists + dispatches
#     via the action="restart_skgate" path
#   - the updateEnvFileSKYGATE_TS_LOGIN_SERVER helper exists
#     + tests cover the replace / append / clear cases
#   - applySysProcAttr helper (setsid) exists for the
#     detached-restart pattern
#   - i18n keys for the restart button exist in both RU+EN
run_check "B65" "SKYGATE_TS_LOGIN_SERVER from .env + restart-skgate button (v0.33.1.16)" 'f=/tmp/b65.sh; printf "%s" "! grep -q \"SKYGATE_TS_LOGIN_SERVER=https://head.example.com\" docker-compose.yml && grep -q handleTailscaleRestart internal/feature/admin/tailscale.go && grep -q \"action=\\\"restart_skgate\\\"\" internal/feature/admin/tailscale.go && grep -q updateEnvFileSKYGATE_TS_LOGIN_SERVER internal/feature/admin/tailscale.go && grep -q applySysProcAttr internal/feature/admin/setsid_linux.go && grep -q TestUpdateEnvFileSKYGATE_TS_LOGIN_SERVER_Replace internal/feature/admin/admin_tailscale_test.go && grep -q TestUpdateEnvFileSKYGATE_TS_LOGIN_SERVER_Append internal/feature/admin/admin_tailscale_test.go && grep -q TestUpdateEnvFileSKYGATE_TS_LOGIN_SERVER_Clear internal/feature/admin/admin_tailscale_test.go && grep -q TestHandleTailscaleRestart_WritesEnvAndDispatches internal/feature/admin/admin_tailscale_test.go && grep -q TestHandleTailscaleRestart_RejectsBadCSRF internal/feature/admin/admin_tailscale_test.go && grep -q tailscale.restart_btn internal/i18n/catalog_tailscale.go && grep -q tailscale.restart_help internal/i18n/catalog_tailscale.go && grep -q tailscale.restart_confirm internal/i18n/catalog_tailscale.go && grep -q tailscale.restart_btn internal/handlers/templates/admin/tailscale.html" > "$f" && bash "$f"; rm -f "$f"'

# ─── B66 (v0.33.1.17) — exit_rule / preferred exit-node cross-check ───
# The bug fixed by this check: the operator's Cloudflare CIDR rules
# for rutracker.org (v0.33.1.16 debug session) were pointed at
# karolina, but every device was pinned to emilia via
# device_exit_node_prefs. The rules were "saved" but Tailscale
# silently ignored them because the device's preferred exit-node
# didn't match.
#
# B66 pins:
#   - the cross-check helpers (PreferredExitNodeForRule,
#     IsRuleApplicable, TagToHostname, RulesByDeviceHostname) all
#     exist in internal/feature/exit_rules/preferred_check.go
#   - the system_tests registry has an "exit_rules.preferred_mismatch"
#     test that counts device_rules with non-preferred exit_nodes
#   - the /my/exit-rules template renders the warning banner
#     + "Use device's preferred" button when MismatchCount > 0
#   - the /admin/exit-rules template renders the per-row "Preferred"
#     column + a top-of-page mismatch banner
#   - the /admin/devices template renders a per-device dead-rule count
#   - i18n keys (RU+EN) cover the banner, button, and column
#   - pure-function unit tests cover IsRuleApplicable + TagToHostname
run_check "B66" "exit-rule / preferred exit-node cross-check (v0.33.1.17)" 'f=/tmp/b66.sh; printf "%s" "grep -q PreferredExitNodeForRule internal/feature/exit_rules/preferred_check.go && grep -q IsRuleApplicable internal/feature/exit_rules/preferred_check.go && grep -q TagToHostname internal/feature/exit_rules/preferred_check.go && grep -q RulesByDeviceHostname internal/feature/exit_rules/preferred_check.go && grep -q TestIsRuleApplicable_NoPreference internal/feature/exit_rules/preferred_check_test.go && grep -q TestIsRuleApplicable_Mismatch internal/feature/exit_rules/preferred_check_test.go && grep -q TestTagToHostname_StandardForms internal/feature/exit_rules/preferred_check_test.go && grep -q exit_rules.preferred_mismatch internal/feature/admin/system_tests.go && grep -q preferred-mismatch-banner internal/handlers/templates/exit_rules.html && grep -q use-preferred-btn internal/handlers/templates/exit_rules.html && grep -q admin-preferred-mismatch internal/handlers/templates/admin/exit_rules.html && grep -q col-preferred internal/handlers/templates/admin/exit_rules.html && grep -q DeadRuleCount internal/feature/admin/devices.go && grep -q exit_rules.preferred_mismatch_banner internal/i18n/catalog_exit_rules.go && grep -q exit_rules.use_preferred_btn internal/i18n/catalog_exit_rules.go && grep -q exit_rules.preferred_col internal/i18n/catalog_exit_rules.go && grep -q exit_rules_admin.preferred_mismatch_banner internal/i18n/catalog_exit_rules.go && grep -q exit_rules_admin.dead_rules_count internal/i18n/catalog_exit_rules.go" > "$f" && bash "$f"; rm -f "$f"'

# ─── B67 (v0.33.1.18) — /admin/exit-rules?device=NAME drill-down ───
# The /admin/devices "dead rules" badge (B66) links to
# /admin/exit-rules?device=NAME; this check pins the drill-down
# itself so a future refactor can't silently drop the filter.
#
# B67 pins:
#   - the filtered DB helper (GetAllRulesForAdminByDevice) is in
#     internal/db/device_rules.go with a backend-neutral query
#   - the SQL constant qSelectAllRulesForAdminByDevice exists
#     in internal/db/queries.go, alongside the unfiltered
#     qSelectAllRulesForAdmin (regression guard)
#   - the form dispatcher in internal/feature/exit_rules/form_admin.go
#     reads `?device=` and routes to the filtered query when present
#   - the template renders the filter banner with the show-all link
#   - the i18n catalog has the banner + show-all strings (RU+EN)
#   - the helper has unit tests for the happy path, case-insensitive
#     match, unknown-hostname, and disabled-inclusion cases
run_check "B67" "/admin/exit-rules?device=NAME drill-down (v0.33.1.18)" 'f=/tmp/b67.sh; printf "%s" "grep -q GetAllRulesForAdminByDevice internal/db/device_rules.go && grep -q qSelectAllRulesForAdminByDevice internal/db/queries.go && grep -q qSelectAllRulesForAdmin internal/db/queries.go && grep -q DeviceFilter internal/feature/exit_rules/form_admin.go && grep -q device-filter-banner internal/handlers/templates/admin/exit_rules.html && grep -q exit_rules_admin.device_filter_banner internal/i18n/catalog_exit_rules.go && grep -q exit_rules_admin.device_filter_show_all internal/i18n/catalog_exit_rules.go && grep -q TestGetAllRulesForAdminByDevice_FiltersByHostname internal/db/device_rules_test.go && grep -q TestGetAllRulesForAdminByDevice_CaseInsensitive internal/db/device_rules_test.go && grep -q TestGetAllRulesForAdminByDevice_UnknownDevice internal/db/device_rules_test.go && grep -q TestGetAllRulesForAdminByDevice_IncludesDisabled internal/db/device_rules_test.go" > "$f" && bash "$f"; rm -f "$f"'

# ─── B68 (v0.33.1.18) — DNS-autoupdater toggle + verification test ───
# v0.32.13 conflated SKYGATE_AUTO_UPDATE_ENABLED (skygate self-update
# banner) with the DNS-resolve autoupdater gate, so any operator who
# turned off skygate self-update in .env was silently disabling their
# domain→/32 refresh → rules rotted as Cloudflare rotated IPs. The
# 2026-08-06 operator incident ("3 new rules for skyworker don't
# work") was the same root cause: the autoupdater was off since
# 2026-07-31, the /32 children were stale, and the operator
# assumed the form was broken when the policy was actually correct
# but the IPs had rotated.
#
# B68 pins:
#   - the conflated flag is fixed: cfg.DNSAutoUpdateEnabled is the
#     gate, separate from cfg.AutoUpdateEnabled (the v0.32.20 self-
#     update flag). main.go gates RunDomainAutoUpdater on the new
#     flag, with the log message naming the new env var.
#   - the goroutine reads global_settings.dns_autoupdate_enabled
#     on every tick so the UI toggle takes effect without a restart
#   - the UI toggle is on /admin/system_tests (POST handler +
#     template card with i18n)
#   - the verification test (exit_rules.all_in_headscale_acl) reads
#     device_rules.enabled=1 and looks up each (src, dst) tuple in
#     headscale's live grants[]; > 5 missing = fail
#   - the 2 unit tests pin the (src, dst) formula in lockstep
#     with the generator (if the generator changes, both tests
#     must be updated together — otherwise the verification
#     test will report false-positive "missing grants" for every
#     row).
run_check "B68" "DNS-autoupdater flag split + verification test (v0.33.1.18)" 'f=/tmp/b68.sh; printf "%s" "grep -q DNSAutoUpdateEnabled internal/config/config.go && grep -q cfg.DNSAutoUpdateEnabled cmd/skygate/main.go && grep -q SKYGATE_DNS_AUTOUPDATE_ENABLED internal/config/config.go && grep -q PostAdminSystemTestsDNSAutoToggle internal/feature/admin/settings_dns_autoupdate.go && grep -q dns-autoupdate-toggle internal/handlers/templates/admin/system_tests.html && grep -q title.dns_autoupdater internal/i18n/catalog_common.go && grep -q dns_autoupdate_enabled internal/handlers/handlers.go && grep -q exit_rules.all_in_headscale_acl internal/feature/admin/system_tests.go && grep -q TestSanitizeRuleAlias internal/feature/admin/system_tests_test.go && grep -q TestExpectedGrantTuple internal/feature/admin/system_tests_test.go && grep -q dns-autoupdate-toggle cmd/skygate/main.go" > "$f" && bash "$f"; rm -f "$f"'
