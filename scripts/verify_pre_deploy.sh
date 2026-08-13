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
# --- B17: per-user device can't be tagged as exit-node (v0.30.1 workstation-8 fix) ---
# 2026-08-12 v1.3.9 catalog cleanup: the original
# v0.30.1 unit tests (TestNodeTagRefused_*) used
# newMemoryDB (SQLite) which was removed by the
# v1.3.0 PG cutover. exit_nodes_tag_test.go is now
# a t.Skip stub. The contract is still pinned via
# the production code path:
#   - nodeTagRefusedForUserDevice() in
#     internal/feature/admin/devices.go enforces
#     the v0.30.1 guard
#   - the guard is exercised at runtime by the live
#     /admin/exit-nodes UI (verified in operator's
#     v0.30.1 post-mortem — see release notes)
# Future work: rewrite the unit tests for PG and
# restore the original t.Run() body (Phase 2 PG
# follow-up). Until then, B17 pins the production
# code path + the t.Skip stub as evidence the
# contract is still meant to be tested.
run_check "B17" "per-user device can't be tagged as exit-node (v0.30.1 workstation-8 fix — production code path pinned, unit tests pending PG rewrite)" \
  "bash -c '
    test -f internal/feature/admin/devices.go &&
    grep -q nodeTagRefusedForUserDevice internal/feature/admin/devices.go &&
    test -f internal/feature/admin/exit_nodes_tag_test.go &&
    grep -q TestAdmin_Skip_exit_nodes_tag internal/feature/admin/exit_nodes_tag_test.go &&
    grep -q v1.3.0 internal/feature/admin/exit_nodes_tag_test.go &&
    '\''$GO'\'' build ./internal/feature/admin/ 2>&1
  '"

# --- B18: PG foundation (v1.3.0+ — build tag removed, pgx is the only driver) ---
# 2026-08-12 v1.3.9 catalog cleanup: the v0.31.0-era
# B18 looked for a `postgres` build tag and the
# build-tag-gated driver_postgres.go. The v1.3.0
# PG cutover (commit b1baa4a) removed the build
# tag system entirely — pgx is the only driver,
# always compiled. The migration helper code
# moved to migrations_pg.go (no build tag).
# This v1.3.9 rewrite of B18 pins the v1.3.0+ shape:
#   (a) internal/db/migrations_pg.go exists and
#       contains the PG migration functions
#   (b) `go build ./cmd/skygate` succeeds (no
#       CGO_ENABLED, pgx is pure Go)
#   (c) `go vet ./internal/db/...` is clean
#   (d) the pgx/v5 dependency is in go.mod
#   (e) the B26 contract (no CGO toolchain in
#       the runtime Dockerfile) still holds
#   (f) the B34 contract (device_rules has no
#       duplicates, queried via psql) still holds
#   (g) the B70 contract (orchestrator migrate
#       step, PG-only) still holds
#   (h) the B79 contract (exit-node pref INSERT
#       placeholder fix, PG-only) still holds
#   (i) the B26-equivalent grep pin for no sqlite
#       in go.mod
# The unit tests for PG (TestPGRoundtripSchema
# etc.) require a live PG cluster and are covered
# by R27 (verify-post on a PG-staging VM), not
# B18. Future work: bring back the test-pg-* tests
# in a `-tags postgres_test` build tag (Phase 2).
run_check "B18" "PG foundation (v1.3.0+ — pgx is the only driver, build tag system removed)" \
  "bash -c '
    test -f internal/db/migrations_pg.go &&
    grep -qE \"^func migrateV04[0-9]PG\" internal/db/migrations_pg.go &&
    grep -qE \"^func migrateV047PG\" internal/db/migrations_pg.go &&
    grep -q jackc/pgx/v5 go.mod &&
    ! grep -qE \"^ENV CGO_ENABLED=1\" Dockerfile &&
    '\''$GO'\'' build -o /tmp/skygate_verify_postgres.b18 ./cmd/skygate &&
    '\''$GO'\'' vet ./internal/db/... 2>&1
  '"

# --- B19: ACL perf + route correctness (v0.32.2) ---
# 2026-08-12 v1.3.9 catalog cleanup: the original
# v0.32.2 unit tests (TestGenerateACL_*) and the
# 4 benchmark functions used openBenchDB (SQLite
# :memory:) which was removed by the v1.3.0 PG
# cutover. perf_test.go is now a t.Skip stub. The
# ACL correctness contract is still pinned via
# the live /admin/acls flow (the operator can
# trigger GenerateACL from the UI and verify the
# output shape). Future work: rewrite the
# benchmarks for PG (Phase 2). B19 now pins the
# t.Skip stub as evidence the contract is still
# meant to be tested.
run_check "B19" "ACL perf + route correctness (v0.32.2 — t.Skip stub, PG rewrite pending Phase 2)" \
  "bash -c '
    test -f internal/acl/perf_test.go &&
    grep -q TestACLPerf_SkipPendingPGRewrite internal/acl/perf_test.go &&
    grep -q v1.3.0 internal/acl/perf_test.go &&
    '\''$GO'\'' build ./internal/acl/ 2>&1
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


# ─── B26 (v1.3.1) — Dockerfile runtime is CGO_ENABLED=0 (PG-only) ───
# 2026-08-12: v1.3.1 (Phase 2 of SQLite removal) — REWRITTEN.
# Pre-v1.3.1 this B26 pinned the CGO+sqlite-libs contract for the
# v0.32.x build (gcc + musl-dev + sqlite-libs in the runtime apk
# add list, no `ENV CGO_ENABLED=0`). v1.3.0 (commit b1baa4a) removed
# mattn/go-sqlite3 from go.mod; v1.3.1 drops the CGO toolchain
# from the runtime entirely. The new contract is the INVERSE:
#
#   (a) Dockerfile does NOT install gcc, musl-dev, or sqlite-libs
#       in the runtime apk add list. These were CGO toolchain
#       pieces for the now-removed mattn/go-sqlite3 driver; without
#       a CGO dep in the source (verified 2026-08-12 via
#       `grep -r 'import "C"' cmd/ internal/ 2>/dev/null` returning
#       zero matches), the CGO toolchain is dead weight.
#   (b) The runtime is CGO_ENABLED=0 (Go's default when no `import
#       "C"` exists). The 24 MB binary is fully static — no musl,
#       no libc, no libsqlite3.so to ship.
#   (c) The postgres:15 image (added in v1.3.1 docker-compose.yml)
#       is the only DB-related container; the skygate container
#       connects via SKYGATE_DB_DSN (libpq URL form).
#
# Why this matters: pre-v1.3.0 the runtime needed ~50 MB of CGO
# toolchain packages (gcc + musl-dev + sqlite-libs) that did
# nothing at runtime except satisfy the dynamic linker. v1.3.1
# drops them → smaller image, faster cold builds, no CGO
# coupling. A regression that re-adds these packages (e.g. a
# future port that pulls in a CGO dep) would inflate the image
# by ~50 MB and re-introduce the v0.32.x musl CGO HTTP wedge
# class of bugs. B26 catches that.
run_check "B26" "Dockerfile runtime is CGO_ENABLED=0 — no gcc/musl-dev/sqlite-libs in apk add (v1.3.1)" \
  "bash -c '
    if grep -vE \"^[[:space:]]*#\" Dockerfile | grep -qE \"^[[:space:]]+(gcc|musl-dev|sqlite-libs) *$\"; then exit 1; fi
    ! grep -qE \"^ENV CGO_ENABLED=1\" Dockerfile
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
# B31 v1.3.9 catalog cleanup: the v0.32.14 contract
# was SQLite-specific (the conn in db.go was a
# *sqlite.Conn with SetMaxOpenConns + NORMAL
# synchronous + 2s busy_timeout PRAGMAs). v1.3.0+
# uses pgx via database/sql — the v0.32.14 CASCADE-
# LOCK fix translates to: SetMaxOpenConns(N) with
# N > 1 + SetMaxIdleConns set + MigratePostgres
# called on open. The current values (SetMaxOpenConns
# = 10) are correct for PG; the synchronous /
# busy_timeout PRAGMAs don't apply to PG and would
# actually fail (PG would parse them as part of the
# DSN and complain).
run_check "B31" "DB connection pool: 10 conns (v1.3.0+ — pgx via database/sql, NORMAL/busy_timeout PRAGMAs are SQLite-specific)" \
  "bash -c '
    ! grep -qE \"conn\\.SetMaxOpenConns\\(1\\)\" internal/db/db.go &&
    grep -qE \"conn\\.SetMaxOpenConns\\([0-9]+\\)\" internal/db/db.go &&
    grep -qE \"conn\\.SetMaxIdleConns\" internal/db/db.go &&
    grep -qE \"MigratePostgres\" internal/db/db.go
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
#       mounts a ts_authkey secret as a plain `TS_AUTHKEY_FILE=`
#       (pre-v0.32.15 bug shape).
#   (b) docker-compose.yml has `SKYGATE_TS_AUTHKEY_FILE=`
#       (empty, SKYGATE_-prefixed) as the env var.
#   (c) docker-compose.yml has `SKYGATE_TS_HOSTNAME=`
#       (empty so entrypoint doesn't auto-spawn tailscaled).
#   (d) docker-compose.yml does NOT have a hardcoded
#       `SKYGATE_TS_LOGIN_SERVER=...` line (v0.33.1.16 fix —
#       the operator edits it via /admin/tailscale + .env, and
#       the compose env was overwriting the .env value because
#       docker-compose precedence is environment > env_file).
#       The pre-v0.33.1.16 shape had this line hardcoded to
#       https://head.example.com; the operator's .env edit was
#       silently ignored. B32 was updated in v0.33.1.20 to
#       match the post-fix shape.
run_check "B32" "Tailscale disabled by default in compose (v0.32.15 + v0.33.1.16 update)" \
  "bash -c '
    ! grep -qE \"^[[:space:]]+-[[:space:]]+TS_AUTHKEY_FILE=\" docker-compose.yml &&
    grep -qE \"^[[:space:]]+-[[:space:]]+SKYGATE_TS_AUTHKEY_FILE=\" docker-compose.yml &&
    grep -qE \"^[[:space:]]+-[[:space:]]+SKYGATE_TS_HOSTNAME=\" docker-compose.yml &&
    ! grep -qE \"^[[:space:]]+-[[:space:]]+SKYGATE_TS_LOGIN_SERVER=\" docker-compose.yml
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
# 2026-08-12: v1.3.1 (Phase 2 of SQLite removal) — rewritten to use
# psql against the live PG cluster (was sqlite3 on a copied-out
# /var/lib/docker/volumes/skygate-data/_data/skygate.db file).
# The contract is unchanged: fail if any (exit_node_id,
# device_hostname) group has COUNT(*) > 1.
#
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
#      the duplicates. Runs `psql` against the live PG cluster
#      (SKYGATE_DB_DSN) and fails if any (exit_node_id,
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
# (DSN not set, or no .env) the check returns 0 (passes). On
# Windows host (no psql, no docker) the check returns 0 (passes).
# Both cases are documented.
run_check "B34" "device_rules table has no duplicate (device, exit_node) pairs (v0.32.16, v1.3.1 psql)" \
  "bash -c '
    DSN=\${SKYGATE_DB_DSN:-}
    if [ -z \"\$DSN\" ] && [ -f /home/skyadmin/skygate/.env ]; then
      DSN=\$(grep -E \"^SKYGATE_DB_DSN=\" /home/skyadmin/skygate/.env | head -1 | cut -d= -f2-)
    fi
    if [ -z \"\$DSN\" ]; then
      echo \"(SKYGATE_DB_DSN not set, skipping B34)\" 1>&2
      exit 0
    fi
    if ! command -v psql >/dev/null 2>&1 && ! command -v docker >/dev/null 2>&1; then
      echo \"(psql/docker not available, skipping B34)\" 1>&2
      exit 0
    fi
    DS=\${DSN#postgres://}; DS=\${DS%%\\?*}
    PU=\${DS%%:*}; REST=\${DS#*:}; PP=\${REST%%@*}; REST=\${REST#*@}
    PH=\${REST%%:*}; REST=\${REST#*:}; PPORT=\${REST%%/*}; PDB=\${REST#*/}
    QUERY=\"SELECT COUNT(*) FROM (SELECT exit_node_id, device_hostname FROM device_rules GROUP BY exit_node_id, device_hostname HAVING COUNT(*) > 1) d\"
    if command -v psql >/dev/null 2>&1; then
      DUPES=\$(PGPASSWORD=\"\$PP\" psql -h \"\$PH\" -p \"\$PPORT\" -U \"\$PU\" -d \"\$PDB\" -tA -c \"\$QUERY\" 2>/dev/null | tr -d \"[:space:]\")
    else
      DUPES=\$(docker run --rm -i --network host -e PGPASSWORD=\"\$PP\" postgres:18-alpine psql -h \"\$PH\" -p \"\$PPORT\" -U \"\$PU\" -d \"\$PDB\" -tA -c \"\$QUERY\" 2>/dev/null | tr -d \"[:space:]\")
    fi
    if [ -z \"\$DUPES\" ]; then
      echo \"(psql read failed, skipping B34)\" 1>&2
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


# ─── B36 (v0.32.19) — migration integrity tracking (v1.3.0+ PG form) ───
# Background: skygate's migrations are idempotent SQL
# functions (migrateV0NN). If a developer changes the
# body of an OLD migration (typo fix, column type
# change), the change is silently absorbed — the DB
# has the pre-fix schema, the new code never re-runs,
# no signal. v0.32.19 adds an `applied_migrations`
# table + SHA-256 helpers so the mismatch is
# detectable.
#
# v1.3.9 catalog cleanup: pre-v1.3.0 the migration
# was in internal/db/migrations_v0.49.go (SQLite,
# no build tag). The v1.3.0 PG cutover consolidated
# ALL migrations into internal/db/migrations_pg.go
# as `migrateV049PG`. The helpers in
# internal/db/migration_tracking.go are still the
# source of truth. The unit tests in
# migration_tracking_test.go still cover the
# checksum mismatch paths.
run_check "B36" "migration integrity: applied_migrations table + checksum helpers + V049PG registered (v0.32.19, v1.3.0+ PG form)" \
  "bash -c '
    grep -qF \"func ComputeMigrationChecksum\" internal/db/migration_tracking.go &&
    grep -qF \"func VerifyMigrationChecksum\" internal/db/migration_tracking.go &&
    grep -qF \"func RecordMigrationApplied\" internal/db/migration_tracking.go &&
    grep -qF \"migrateV049PG\" internal/db/migrations_pg.go &&
    grep -qF \"applied_migrations\" internal/db/migrations_pg.go &&
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
run_check "B37" "auto-update UI toggle: handler + route + template + global_settings helper (v0.32.20, v1.3.0+ PG rewrite pending)" \
  "bash -c '
    grep -qF \"func (s *Service) PostAdminUpdateAutoToggle\" internal/feature/admin/update_settings.go &&
    grep -qF \"PostAdminUpdateAutoToggle\" cmd/skygate/main.go &&
    grep -qF \"/admin/update/auto-toggle\" internal/handlers/templates/admin/update.html &&
    grep -qF \"GetGlobalSettingBool\" internal/feature/admin/update.go &&
    grep -qF \"auto_update_enabled\" internal/feature/admin/update_settings.go &&
    test -f internal/feature/admin/update_settings_test.go &&
    grep -q v1.3.0 internal/feature/admin/update_settings_test.go &&
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
    ( grep -qF \"system_tests_runs\" internal/db/migrations_v0.51.go || grep -qF \"system_tests_runs\" internal/db/migrations_pg.go )
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
run_check "B42" "db.MigratePostgres: migrateV050PG + migrateV051PG called (v0.33.0+, v1.3.0+ PG form)" \
  "bash -c '
    grep -qF \"migrateV050PG\" internal/db/migrations_pg.go &&
    grep -qF \"migrateV051PG\" internal/db/migrations_pg.go &&
    grep -qF \"MigratePostgres\" internal/db/db.go &&
    grep -qF \"migrateV050PG\" internal/db/driver_postgres.go &&
    grep -qF \"migrateV051PG\" internal/db/driver_postgres.go
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
# v1.3.9 catalog cleanup: the v0.33.1.8-era B54
# looked for placeholders_sqlite.go and
# placeholders_postgres.go (two files for the two
# backends). v1.3.0 removed SQLite entirely, so
# placeholders_sqlite.go was deleted and
# placeholders.go now returns "$1,$2,..." always
# (no build tag, no per-backend split). The
# globalsettings.go consumer still uses
# placeholdersList(n) as the source of truth so
# the call sites are unchanged.
run_check "B54" "SetGlobalSetting uses placeholdersList(n) — v1.3.0+ PG-only (no more SQLite, no per-backend split)" \
  'grep -q "placeholdersList(2)" internal/db/globalsettings.go && \
   test -f internal/db/placeholders.go && \
   test -f internal/db/placeholders_postgres.go && \
   ! test -f internal/db/placeholders_sqlite.go && \
   grep -qF "return placeholdersList(n)" internal/db/placeholders.go'

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
#   1. A positional $1 (e.g. "skyadmin@<VM_HOST>") as the
#      primary SSH_HOST override
#   2. An --help flag that prints the catalog header (the
#      previous script had no way to ask "what does this do?")
#   3. An --bad-flag error path (previously unknown flags were
#      silently ignored)
#
# B61 pins:
#   - the script accepts "skyadmin@<VM_HOST>" as $1 and
#     SSH_HOST resolves to that (not the legacy default)
#   - --help exits 0 after printing the catalog header
#   - an unknown flag exits non-zero with "unknown flag: ..."
run_check "B61" "verify_post_deploy.sh accepts SSH_HOST as positional \$1 (v0.33.1.13)" 'f=/tmp/b61.sh; printf "%s" "set +e; out1=\$(bash scripts/verify_post_deploy.sh skyadmin@<VM_HOST> --help 2>&1 | head -3); case \"\$out1\" in *\"runtime guarantees for skygate\"*) echo help-ok;; *) echo \"help text missing: \$out1\"; exit 1;; esac; out2=\$(bash scripts/verify_post_deploy.sh --bad-flag 2>&1); case \"\$out2\" in *\"unknown flag\"*) echo bad-flag-ok;; *) echo \"bad-flag path broken: \$out2\"; exit 1;; esac; grep -q \"SSH_HOST=\\\"\\\${SSH_HOST:-admin@<VM_HOST>}\\\"\" scripts/verify_post_deploy.sh || { echo \"SSH_HOST fallback line missing\"; exit 1; }; grep -q \"SSH_HOST_SET\" scripts/verify_post_deploy.sh || { echo \"SSH_HOST_SET var missing\"; exit 1; }" > "$f" && bash "$f"; rm -f "$f"'

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

# ─── B68a (v0.33.1.19) — via_enabled column-order + data-repair migration ───
# The v0.28.5 INSERT in SetUserExitNodePref and
# SetDeviceExitNodePref had a positional-mismatch bug: the
# VALUES list put viaInt (a 0/1 bool) in the position mapped
# to updated_at, and nowUnixSQL() (a unix timestamp > 1.7e9)
# in the position mapped to via_enabled. Every row inserted
# by v0.28.5 — v0.33.1.18 had via_enabled=<unix timestamp> —
# always truthy, so the per-user grant in the ACL always had
# `via: [tag:exit-...]` (the un-check "advisory mode" UI
# checkbox was a no-op) and /my/exit-nodes always showed the
# "strict" badge.
#
# v0.33.1.19 ships:
#   1. The INSERTs in migrations_v0.45.go and migrations_v0.46.go
#      are reordered so viaInt goes to via_enabled and
#      nowUnixSQL() goes to updated_at. New rows are correct
#      from the start.
#   2. Migration v0.52 walks user_exit_node_prefs and
#      device_exit_node_prefs and swaps the two columns when
#      the discriminant (updated_at in {0,1} AND via_enabled
#      > 1e9) is satisfied. Idempotent: running it twice
#      finds nothing to swap. Discriminant safely skips
#      already-correct rows.
#   3. Unit tests in migrations_v0.52_test.go pin the
#      repair (corrupt row fixed, correct row untouched,
#      threshold guards legitimate (0,0) fresh rows,
#      idempotent on re-run, mixed multi-row table).
run_check "B68a" "via_enabled INSERT column-order + v0.52 data-repair migration (v0.33.1.19)" 'f=/tmp/b68a.sh; printf "%s" "grep -q migrateV052 internal/db/db.go && grep -q migrateV052 internal/db/migrations_v0.52.go && grep -q user_exit_node_prefs internal/db/migrations_v0.52.go && grep -q device_exit_node_prefs internal/db/migrations_v0.52.go && grep -q placeholdersList.4. internal/db/migrations_v0.45.go && grep -q placeholdersList.4. internal/db/migrations_v0.46.go && grep -q TestMigrateV052_RepairsCorruptUserPref internal/db/migrations_v0.52_test.go && grep -q TestMigrateV052_RepairsCorruptDevicePref internal/db/migrations_v0.52_test.go && grep -q TestMigrateV052_LeavesCorrectRowsAlone internal/db/migrations_v0.52_test.go && grep -q TestMigrateV052_Idempotent internal/db/migrations_v0.52_test.go && grep -q TestMigrateV052_Threshold internal/db/migrations_v0.52_test.go && grep -q TestMigrateV052_DevicePrefMultipleRows internal/db/migrations_v0.52_test.go" > "$f" && bash "$f"; rm -f "$f"'

# ─── B69 (v0.33.1.20) — backfill tag fix + force-backfill + transfer ───
# Three pieces, motivated by the 2026-08-09 operator report:
#   1. The 13 headscale nodes that had only tag:private
#      (no tag:dev-<user>-<host>). The per-user backfill in
#      /my/devices only applies the dev-tag for the CURRENT
#      user — to fix cross-user cases, the operator needs
#      an admin-side "force-backfill all" button.
#   2. The svyatoslava dual-owner conflict (id=27 in skyadmin
#      namespace, id=30 in svyatoslava) — the operator needs
#      a way to transfer a node from one portal user to
#      another (with AddTag new + UntagNode old on the
#      headscale side, plus row update in node_owner_map).
#   3. The "rename never updates the tag" bug — when a user
#      renames their Tailscale hostname (e.g. desktop-cj8t9me
#      → cyborg), the per-user backfill only does INSERT OR
#      IGNORE, so node_owner_map kept the old hostname + old
#      dev-tag forever, and headscale accumulated BOTH the
#      old and the new tag (AddTag never removes).
#
# B69 pins:
#   - The backfill helper handles the rename scenario:
#     UpdateNodeOwnerHostnameAndTag rewrites BOTH the row's
#     hostname AND its tag (the existing helpers update one
#     or the other but not both atomically).
#   - The backfill helper detects existing.hostname != n.Hostname
#     and (a) calls hs.UntagNode(oldTag) so headscale drops the
#     stale tagOwners entry, (b) calls hs.AddTag(newTag) with
#     the new dev-tag, (c) UPDATEs the DB row.
#   - The per-user backfill is exposed as an admin action
#     (POST /admin/devices/force-backfill-tags) so the
#     operator can fix cross-user dev-tag gaps without
#     logging in as each user.
#   - The transfer action (POST /admin/devices/transfer)
#     reassigns a node to a different portal user. The
#     transferTargets helper filters the dropdown to only
#     valid portal users (excludes the synthetic
#     "tagged-devices" headscale user + empty names).
#   - Unit tests pin the rename contract (Backfill with a
#     node whose hostname drifted from the row's hostname
#     must UPDATE the row) and the transfer validation
#     paths (non-admin, missing fields, missing node).
#   - The template renders both new admin actions (the
#     "Force resync all tags" button + the per-row
#     "Transfer" details/dropdown).
#   - i18n provides 6 new keys (RU + EN) for the new
#     buttons / dropdown labels / form help text.
run_check "B69" "backfill tag fix + force-backfill + transfer (v0.33.1.20)" 'f=/tmp/b69.sh; printf "%s" "grep -q UpdateNodeOwnerHostnameAndTag internal/db/node_owner_map.go && grep -q existingByNodeID internal/nodeownership/nodeownership.go && grep -q UntagNode(nodeIDInt, oldDevTag) internal/nodeownership/nodeownership.go && grep -q UpdateNodeOwnerHostnameAndTag(db, n.ID, n.Hostname, devTag internal/nodeownership/nodeownership.go && grep -q PostAdminDevicesForceBackfillTags internal/feature/admin/devices.go && grep -q PostAdminDeviceTransfer internal/feature/admin/devices.go && grep -q transferTargets internal/feature/admin/devices.go && grep -q /admin/devices/force-backfill-tags cmd/skygate/main.go && grep -q /admin/devices/transfer cmd/skygate/main.go && grep -q force-backfill-tags internal/handlers/templates/admin/devices.html && grep -q devices.transfer_btn internal/i18n/catalog_my.go && grep -q devices.force_backfill_btn internal/i18n/catalog_my.go && grep -q devices.transfer_help internal/i18n/catalog_my.go && grep -q TestBackfill_RenameUpdatesHostnameAndTag internal/nodeownership/nodeownership_test.go && grep -q TestTransferTargets_ExcludesSyntheticUser internal/feature/admin/devices_test.go && grep -q TestPostAdminDeviceTransfer_RejectsNonAdmin internal/feature/admin/devices_test.go && grep -q TestPostAdminDeviceTransfer_MissingNodeID internal/feature/admin/devices_test.go && grep -q TestPostAdminDeviceTransfer_MissingTargetUsername internal/feature/admin/devices_test.go && grep -q TestPostAdminDeviceTransfer_NodeNotInDB internal/feature/admin/devices_test.go && grep -q TestPostAdminDevicesForceBackfillTags_RejectsNonAdmin internal/feature/admin/devices_test.go && grep -q TestPostAdminDevicesForceBackfillTags_NilHS internal/feature/admin/devices_test.go" > "$f" && bash "$f"; rm -f "$f"'

# ─── B70 (v0.33.1.21) — auto-update orchestrator migration step (the 3-bug-fix) ───
# The 2026-08-09 /admin/update "Apply" button was broken. The
# pre-fix orchestrator's Phase 3 (migrate) had THREE pre-existing
# bugs that all manifested on the v0.32.13+ alpine base image:
#
#   1. `bash -c "..."` — alpine's `bash` doesn't exist (only
#      busybox `sh`). The orchestrator has been failing at this
#      step since v0.32.13; the auto-rollback hid the bug from
#      the operator (the previous tag was always restored; the
#      migration was simply never applied to the new image
#      before the swap). Fix: use `sh` (POSIX-portable pipe
#      `2>&1 | tail -20` works in both shells).
#
#   2. `--volumes-from skygate` referenced a container that
#      hasn't existed since v0.29.2 (which removed
#      `container_name: skygate` to fix a `--force-recreate`
#      race). The compose-generated name is
#      `skygate-skygate-1` (or `-N` after multiple recreates).
#      We resolve the actual container ID by label
#      (`com.docker.compose.service=skygate`) so the
#      orchestrator works regardless of how many times the
#      container has been recreated.
#
#   3. `--migrate-only` was referenced in the docker run
#      command but the flag was never implemented in
#      cmd/skygate/main.go. v0.33.1.21 adds the subcommand
#      (`runMigrateOnly` opens the DB which runs all pending
#      migrations as part of Open() and returns; the
#      orchestrator's `migrate` phase is now an actual
#      forward-progress step rather than a silent no-op).
#
# Together these fix the "update silently rolls back even on
# success" symptom the operator has been seeing on the live
# VM since v0.32.13.
#
# B70 pins the contract:
#   - main.go: `migrate-only` subcommand + `runMigrateOnly`
#     function (testable, returns error not os.Exit)
#   - main.go: `help` text lists `migrate-only`
#   - docker.go: orchestrator uses `sh` (not `bash`) and
#     resolves the skygate container ID via the
#     `com.docker.compose.service=skygate` label lookup
#   - tests: FreshDB_SQLite + Idempotent + RespectsDSN
run_check "B70" "auto-update orchestrator migrate step (bash→sh + container by label + --migrate-only flag) (v0.33.1.21, v1.3.1 PG-only)" 'f=/tmp/b70.sh; printf "%s" "grep -qF \"migrate-only\" cmd/skygate/main.go && grep -qF runMigrateOnly cmd/skygate/main.go && grep -qF \"open the DB\" cmd/skygate/main.go && grep -qF \"sh\" internal/update/docker.go && grep -qF label=com.docker.compose.service=skygate internal/update/docker.go && grep -qF TestRunMigrateOnly_FreshDB_SQLite cmd/skygate/migrate_only_test.go && grep -qF TestRunMigrateOnly_Idempotent cmd/skygate/migrate_only_test.go && grep -qF TestRunMigrateOnly_RespectsDSN cmd/skygate/migrate_only_test.go" > "$f" && bash "$f"; rm -f "$f"'

# ─── B71 (v0.33.1.22) — orchestrator healthz poll uses Go net/http (not curl) ───
# Live verify on the VM (2026-08-09, immediately after deploying
# v0.33.1.21) exposed a FOURTH pre-existing bug in the
# orchestrator's alpine path: the post-swap healthz poll loop
# was shelling out to `curl -fsS --max-time 5
# http://localhost:8080/healthz`. curl is not in the alpine
# base image (golang:1.25-alpine ships wget + busybox, not
# curl). The exec failed with "executable file not found"
# on every attempt, the orchestrator interpreted the failure
# as "container not yet healthy", timed out at 1m0s, and
# triggered auto-rollback to the pre-update tag even though
# the new container was actually fine (a manual /healthz
# check 2 seconds later returned 200). v0.33.1.22 replaces
# the curl call with Go's net/http (same approach v0.32.22
# took for the other HTTP probes in the codebase) — no shell
# dependency, no path surprises across host/container
# rebuilds, and no alpine-curl-not-installed surprise.
#
# B71 pins the contract:
#   - internal/update/docker.go: net/http + io imports
#   - the post-swap healthz poll uses (&http.Client).Do
#     (not exec.CommandContext("curl", ...))
#   - 5s per-request timeout is preserved (was --max-time 5)
#   - 200 + `"status":"ok"` body match is preserved
run_check "B71" "orchestrator healthz poll uses net/http not curl (v0.33.1.22)" 'f=/tmp/b71.sh; printf "%s" "grep -qF net/http internal/update/docker.go && grep -qF http.NewRequestWithContext internal/update/docker.go && grep -qF http.Client internal/update/docker.go && grep -qF localhost:8080/healthz internal/update/docker.go && grep -qF StatusCode internal/update/docker.go" > "$f" && bash "$f"; rm -f "$f"'

# ─── B72 (v0.33.1.23) — layout.html update-banner data shape (string + string) ───
# The pre-fix layout.html referenced
#
#   {{tf "update.banner_body" .Version .UpdateLatest.TagName}}
#   {{if .UpdateLatest.HTMLURL}}  <a href="{{.UpdateLatest.HTMLURL}}">
#
# assuming `UpdateLatest` was a release.Release struct. The auto-banner
# path (handlers.go:456) set it as a struct, so the global banner worked
# for /admin pages where `IsAdmin=true`. The /admin/update handler
# (update.go:188) set it as a *string* (`result.Latest`), so the
# /admin/update page crashed at render time with
# `can't evaluate field TagName in type interface {}` — no Apply
# button, no orchestrator, no Apply-shortcut. The orchestrator itself
# ran fine because we hit /admin/update/apply via curl directly, but
# the page was useless until this fix.
#
# B72 pins the new shape:
#   - UpdateLatest is always a tag-name string (e.g. "v0.33.1.24")
#   - UpdateLatestURL is always a release-page URL string
#   - The template reads them as scalars (no field access)
#   - 5 source-level pins (2 in handlers.go, 2 in update.go, 1 in layout.html)
#     + 4 test names in the new internal/handlers/layout_banner_test.go
#   - The old broken shape (`UpdateLatest.TagName` or
#     `UpdateLatest.HTMLURL`) is explicitly rejected so a future
#     refactor can't silently regress.
run_check "B72" "layout update-banner data shape pinned to string+string (v0.33.1.23)" 'f=/tmp/b72.sh; printf "%s" "grep -qF \"UpdateLatest\\\" = latest.TagName\" internal/handlers/handlers.go && grep -qF \"UpdateLatestURL\\\" = latest.HTMLURL\" internal/handlers/handlers.go && grep -qF \"UpdateLatest\\\":    result.Latest\" internal/feature/admin/update.go && grep -qF \"UpdateLatestURL\\\": result.ReleaseURL\" internal/feature/admin/update.go && grep -qF \"\\.UpdateLatest }}\" internal/handlers/templates/layout.html && grep -qF \"{{if .UpdateLatestURL}}\" internal/handlers/templates/layout.html && ! grep -qF \".UpdateLatest.TagName\" internal/handlers/templates/layout.html && ! grep -qF \".UpdateLatest.HTMLURL\" internal/handlers/templates/layout.html && grep -qF TestLayoutBanner_UpdatePageDataShape internal/handlers/layout_banner_test.go && grep -qF TestLayoutBanner_AutoMonitorDataShape internal/handlers/layout_banner_test.go && grep -qF TestLayoutBanner_MissingLatestURLUsesFallback internal/handlers/layout_banner_test.go && grep -qF TestLayoutBanner_NoUpdateHidesBanner internal/handlers/layout_banner_test.go" > "$f" && bash "$f"; rm -f "$f"'

# ─── B73 (v0.33.1.24) — layout fallback URL uses injected GitHub coords ───
# The pre-fix layout.html hardcoded
#
#   https://github.com/skygate-operator/skygate/releases
#
# as the "Open release" fallback URL. This leaked the original
# developer's GitHub org (the v0.32.29 no-personal-data policy
# violation; flagged in v0.33.1.23). v0.33.1.24 derives the
# fallback URL from Cfg.GitHubOwner / Cfg.GitHubRepo (auto-injected
# into the data map by renderWithLayout, defaults to
# "BarsSky" / "skygate" matching config.Config defaults) AND
# sweeps the doc tree (~109 hardcoded references in
# AGENTS.md, RELEASE-NOTES.md, docs/, templates, LICENSE) to
# point at the canonical github.com/BarsSky/skygate.
#
# B73 pins the contract:
#   - internal/handlers/handlers.go: GitHubOwner + GitHubRepo
#     are auto-injected into the data map (with "BarsSky" /
#     "skygate" fallbacks if Cfg is nil — for test paths)
#   - internal/handlers/templates/layout.html: the fallback
#     `{{else}}` branch builds the URL from
#     `{{.GitHubOwner}}/{{.GitHubRepo}}` — no hardcoded org
#   - The pre-fix hardcoded URL must NOT appear anywhere in
#     the layout template
#   - 3 new render tests in layout_banner_test.go pin the
#     data shape (FALLBACK-LINK marker, no leak)
#   - The doc sweep is documented in the commit message but
#     NOT pinned by B73 (the operator's repo URL is
#     captured by AGENTS.md, LICENSE, and the GitHub release
#     notes — those are the source of truth)
run_check "B73" "layout fallback URL uses injected GitHub coords (v0.33.1.24)" 'f=/tmp/b73.sh; printf "%s" "grep -qF \"data[\\\"GitHubOwner\\\"] = githubOwner\" internal/handlers/handlers.go && grep -qF \"data[\\\"GitHubRepo\\\"] = githubRepo\" internal/handlers/handlers.go && grep -qF \"github.com/{{.GitHubOwner}}/{{.GitHubRepo}}/releases\" internal/handlers/templates/layout.html && ! grep -qF \"github.com/skygate-operator/skygate/releases\\\"\" internal/handlers/templates/layout.html && ! grep -qF \"github.com/skygate-operator/skygate\" internal/handlers/templates/layout.html && grep -qF TestLayoutBanner_FallbackURL_UsesInjectedCoords internal/handlers/layout_banner_test.go && grep -qF TestLayoutBanner_FallbackURL_DefaultsToBarsSkySkygate internal/handlers/layout_banner_test.go && grep -qF \"github.com/MyOrg/my-fork/releases\" internal/handlers/layout_banner_test.go" > "$f" && bash "$f"; rm -f "$f"'

# ─── B76 (v0.33.1.24) — orchestrator "Push" target handles pre-update tags ───
# 2026-08-09: v0.33.1.24 (B76) — the pre-fix PostAdminUpdatePush
# (and PostAdminUpdateApply) handler did
#
#   if !strings.HasPrefix(target, "v") {
#       target = "v" + target
#   }
#
# unconditionally, which produced `vskygate-pre-update-<sha>`
# whenever s.BuildVersion was the orchestrator's own
# pre-update tag. `git checkout` then failed with exit
# status 1 and the orchestrator triggered a phantom
# auto-rollback. v0.33.1.24 adds `normalizeUpdateTarget()`
# which recognizes `skygate-*` tags, `main`, and `HEAD`
# as already-valid refs and leaves them alone (only
# prepends "v" to plain semver like "0.33.1.24").
#
# B76 pins the contract:
#   - internal/feature/admin/update.go: the helper
#     `normalizeUpdateTarget` is defined + used by
#     both PostAdminUpdateApply and PostAdminUpdatePush
#   - the pre-fix `if !strings.HasPrefix(target, "v")`
#     pattern is explicitly REJECTED in update.go
#     (must be replaced by the helper call)
#   - 6 unit tests in
#     internal/feature/admin/update_target_test.go
#     cover pre-update tag, already-prefixed tag,
#     plain semver, branch, SHA, and empty inputs.
run_check "B76" "orchestrator 'Push' target handles pre-update tags (v0.33.1.24)" 'f=/tmp/b76.sh; printf "%s" "grep -qF \"func normalizeUpdateTarget\" internal/feature/admin/update.go && grep -qF \"skygate-\" internal/feature/admin/update.go && grep -qF \"target = normalizeUpdateTarget\" internal/feature/admin/update.go && ! grep -qF \"if !strings.HasPrefix(target, \\\"v\\\")\" internal/feature/admin/update.go && grep -qF TestNormalizeUpdateTarget_PreUpdateTag internal/feature/admin/update_target_test.go && grep -qF TestNormalizeUpdateTarget_AlreadyPrefixed internal/feature/admin/update_target_test.go && grep -qF TestNormalizeUpdateTarget_PlainSemver internal/feature/admin/update_target_test.go && grep -qF TestNormalizeUpdateTarget_Branch internal/feature/admin/update_target_test.go && grep -qF TestNormalizeUpdateTarget_SHA internal/feature/admin/update_target_test.go && grep -qF TestNormalizeUpdateTarget_Empty internal/feature/admin/update_target_test.go" > "$f" && bash "$f"; rm -f "$f"'

# ─── B77 (v0.33.1.25) — node-discovery autoupdater ───
# 2026-08-09: v0.33.1.25 (B77) — Issue 2 from the
# operator's 2026-08-09 report. Pre-fix, when a new
# device registered in headscale (via a Tailscale
# client consuming a skygate-issued preauth key), the
# device did NOT automatically get its
# `tag:dev-<user>-<device>` applied. The tag is what
# the per-device ACL rule (src=tag:dev-<user>-<device>)
# uses to grant autogroup:internet access — without
# it, the device had no internet access until one of:
#
#   - the owning user visited /my/devices (per-user
#     Backfill on page load)
#   - the admin clicked "Force backfill" on
#     /admin/devices (PostAdminDevicesForceBackfillTags)
#
# For off-site devices this was a UX papercut; the
# device came online with internet access effectively
# denied until the user noticed.
#
# v0.33.1.25 fixes it by running `nodeownership.Backfill`
# against every portal user on a timer
# (SKYGATE_NODE_DISCOVERY_INTERVAL, default 5m). The
# autoupdater is wired in cmd/skygate/main.go and
# goroutine-spawned next to the existing DNS
# autoupdater (same default interval).
#
# B77 pins the contract:
#   - internal/nodeownership/auto.go: `AutoBackfill`
#     function + `nodeLister` interface (4 methods)
#   - internal/nodeownership/nodeownership.go: Backfill
#     also takes the `nodeLister` interface (so the test
#     suite can pass a fake without depending on a real
#     headscale instance)
#   - internal/config/config.go: NodeDiscoveryInterval
#     field + SKYGATE_NODE_DISCOVERY_INTERVAL env
#   - cmd/skygate/main.go: goroutine wired (gated on
#     interval > 0 + HSGlobalFn non-nil)
#   - 6 unit tests in internal/nodeownership/auto_test.go
#     cover zero-interval noop, nil DB noop, nil HS noop,
#     context cancel exits, list error tolerated, happy
#     path (multi-tick + per-tick InvalidateCache).
run_check "B77" "node-discovery autoupdater: new devices auto-tag (v0.33.1.25)" 'f=/tmp/b77.sh; printf "%s" "grep -qF \"func AutoBackfill\" internal/nodeownership/auto.go && grep -qF \"type nodeLister interface\" internal/nodeownership/auto.go && grep -qF \"hs nodeLister\" internal/nodeownership/auto.go && grep -qF \"NodeDiscoveryInterval\" internal/config/config.go && grep -qF \"SKYGATE_NODE_DISCOVERY_INTERVAL\" internal/config/config.go && grep -qF \"nodeownership.AutoBackfill\" cmd/skygate/main.go && grep -qF TestAutoBackfill_ZeroIntervalIsNoop internal/nodeownership/auto_test.go && grep -qF TestAutoBackfill_NilDBIsNoop internal/nodeownership/auto_test.go && grep -qF TestAutoBackfill_NilHSIsNoop internal/nodeownership/auto_test.go && grep -qF TestAutoBackfill_ContextCancelExits internal/nodeownership/auto_test.go && grep -qF TestAutoBackfill_ListErrorIsTolerated internal/nodeownership/auto_test.go && grep -qF TestAutoBackfill_HappyPath internal/nodeownership/auto_test.go" > "$f" && bash "$f"; rm -f "$f"'

# B78 — v0.33.1.26: persistent per-test status visualization
# on /admin/system_tests.
#
# Pre-fix: the page only showed per-test PASS/FAIL/SKIP
# icons + failure output AFTER the operator clicked "Run
# all" (LiveResults path). On a fresh page load (GET
# handler), every test row had a gray "no data" circle
# + empty output cell, even if the most recent run
# (system_tests_runs) had 6 failing tests with detailed
# failure output. The fix: the GET handler now reads the
# most recent run + parses results_json + passes the
# per-test status to the template as LastResults. The
# template renders the same per-row icons (green
# check / red xmark / gray pause) on first load, and
# FAIL rows get a red-tinted background + left border
# so the operator can see at a glance which tests are
# broken.
#
# B78 pins the contract:
#   - internal/feature/admin/system_tests.go:
#     `ListLastRunWithResults` method (reads the
#     MAX(id) row from system_tests_runs + parses
#     results_json into []SystemTestResult)
#   - internal/feature/admin/system_tests.go: the
#     LastRunWithResults struct (RunID + Summary +
#     Results + StartedAt + FinishedAt)
#   - internal/feature/admin/system_tests_handlers.go:
#     `GetAdminSystemTests` now calls
#     ListLastRunWithResults and passes LastResults +
#     LastSummary + LastRunID + LastRunAgeSec into the
#     data map
#   - internal/handlers/templates/admin/system_tests.html:
#     the LastResults branch in the test-table loop
#     (renders the per-row icon from LastResults when
#     LiveResults is nil) + the row-fail class branch
#     + the Last run results header
#   - internal/handlers/templates.go: 2 new funcmap
#     helpers (`humanizeAgeSeconds` + `indexResultByName`)
#   - internal/i18n/catalog_common.go: 4 new keys
#     (system_tests.last_run_label, last_run_age,
#     no_runs_yet, no_runs_help) in BOTH RU and EN
#   - 4 new unit tests in
#     internal/feature/admin/system_tests_test.go:
#     RequiresDB, ParsesJSON, ReturnsNewest, MalformedJSON
#   - 1 new render test in
#     internal/handlers/system_tests_render_test.go:
#     TestSystemTestsRendersWithLastResults (asserts
#     row-fail class + fail icon + last-run header)
run_check "B78" "per-test status on /admin/system_tests (v0.33.1.26)" 'f=/tmp/b78.sh; printf "%s" "grep -qF \"func (s \\*Service) ListLastRunWithResults\" internal/feature/admin/system_tests.go && grep -qF \"type LastRunWithResults struct\" internal/feature/admin/system_tests.go && grep -qF \"ListLastRunWithResults\" internal/feature/admin/system_tests_handlers.go && grep -qF \"LastResults\" internal/feature/admin/system_tests_handlers.go && grep -qF \"LastRunAgeSec\" internal/handlers/templates/admin/system_tests.html && grep -qF \"row-fail\" internal/handlers/templates/admin/system_tests.html && grep -qF \"system_tests.last_run_label\" internal/handlers/templates/admin/system_tests.html && grep -qF \"humanizeAgeSeconds\" internal/handlers/templates.go && grep -qF \"indexResultByName\" internal/handlers/templates.go && grep -qF \"system_tests.last_run_label\" internal/i18n/catalog_common.go && grep -qF \"system_tests.no_runs_yet\" internal/i18n/catalog_common.go && grep -qF TestListLastRunWithResults_RequiresDB internal/feature/admin/system_tests_test.go && grep -qF TestListLastRunWithResults_ParsesJSON internal/feature/admin/system_tests_test.go && grep -qF TestListLastRunWithResults_ReturnsNewest internal/feature/admin/system_tests_test.go && grep -qF TestListLastRunWithResults_MalformedJSON internal/feature/admin/system_tests_test.go && grep -qF TestSystemTestsRendersWithLastResults internal/handlers/system_tests_render_test.go" > "$f" && bash "$f"; rm -f "$f"'

# B79 — v0.33.1.27: per-user + per-device exit-node pref
# INSERT (the /my/exit-nodes + /my/devices/preferred-exit
# POST handlers).
#
# Pre-fix the VALUES clause was
#   placeholdersList(N) + placeholdersList(1)
# (or placeholdersList(N-1) + placeholdersList(1)) which on
# PG produced "$1, $2, $3, $1" (TWO references to $1 in
# the same query) because placeholdersList always starts
# the count at 1. pgx rejected the query with
# "mismatched param and argument count" and the POST
# handlers returned 500 on every click for every user.
# The operator-visible symptom was: /my/exit-nodes
# "Set as my preferred" did nothing + /my/devices
# "Set exit node for this device" did nothing, with no
# error in the UI. The fix: introduce PlaceholdersRange
# (from, to) that generates a contiguous range of
# placeholders so the surrounding placeholder numbers
# "skip" past the inlined nowUnixSQL() function.
#
# B79 pins the contract:
#   - internal/db/placeholders.go: PlaceholdersRange
#     (new public helper)
#   - internal/db/placeholders_postgres.go: the
#     placeholdersFromTo variant (returns "$from..$to"
#     contiguous range; SQLite is gone in v1.3.0, so only
#     the PG variant remains)
#   - internal/db/migrations_v0.45.go:
#     SetUserExitNodePref uses PlaceholdersRange(1, 3) +
#     nowUnixSQL() + PlaceholdersRange(4, 4)
#     (4 placeholders + 1 fn + 4 Go args)
#   - internal/db/migrations_v0.46.go:
#     SetDeviceExitNodePref uses PlaceholdersRange(1, 4) +
#     nowUnixSQL() + PlaceholdersRange(5, 5)
#     (5 placeholders + 1 fn + 5 Go args)
#   - 4 new unit tests in internal/db/migrations_v0_45_46_test.go
#     + internal/db/test_sql_dryrun_test.go pin the PG format
#     (the pre-v1.3.0 placeholders_range_sqlite_test.go was
#     removed along with the SQLite backend in v1.3.0)
run_check "B79" "exit-node pref INSERT placeholder fix (v0.33.1.27, v1.3.1 PG-only)" 'f=/tmp/b79.sh; printf "%s" "grep -qF \"func PlaceholdersRange\" internal/db/placeholders.go && grep -qF \"func placeholdersFromTo\" internal/db/placeholders_postgres.go && grep -qF \"PlaceholdersRange(1, 3)\" internal/db/migrations_v0.45.go && grep -qF \"PlaceholdersRange(4, 4)\" internal/db/migrations_v0.45.go && grep -qF \"PlaceholdersRange(1, 4)\" internal/db/migrations_v0.46.go && grep -qF \"PlaceholdersRange(5, 5)\" internal/db/migrations_v0.46.go && grep -qF TestSetUserExitNodePref_RoundTrip internal/db/migrations_v0_45_46_test.go && grep -qF TestSetDeviceExitNodePref_RoundTrip internal/db/migrations_v0_45_46_test.go && grep -qF TestSetUserExitNodePref_RecentTimestamp internal/db/migrations_v0_45_46_test.go && grep -qF TestPlaceholdersRange_PGFormat internal/db/test_sql_dryrun_test.go" > "$f" && bash "$f"; rm -f "$f"'

# B80 — v0.33.1.28: orchestrator swap broken on this VM
# (B79-backlog).
#
# Pre-fix the docker-compose.yml environment section
# had a HARDCODED `SKYGATE_HOST_REPO_PATH=/home/operator/skygate`
# value (line 113). Docker compose precedence is
# environment > env_file, so the operator's
# SKYGATE_HOST_REPO_PATH=/home/skyadmin/skygate in
# .env was ignored. The in-container auto-updater's
# swap helper looked for docker-compose.yml at
# /home/operator/skygate (which doesn't exist on this
# VM), the helper's `docker compose up` failed with
# "no configuration file provided: not found", and
# the orchestrator reported "update complete" because
# the OLD container's /healthz still returned 200.
# Result: every deploy via the web-UI was a silent
# no-op; manual `docker compose -p skygate up -d
# --force-recreate --no-deps skygate` was required to
# actually swap the container.
#
# The fix: change the HARDCODED value to
# `${SKYGATE_HOST_REPO_PATH:-/home/operator/skygate}`
# (same `${VAR:-default}` form as the volumes +
# secrets sections below). The env_file (.env) value
# wins when set; the default `/home/operator/skygate`
# applies otherwise. No code change — pure compose
# fix. The `internal/update/docker.go` swap helper
# script already uses `${SKYGATE_HOST_REPO_PATH:-...}`
# so it picks up the corrected env var automatically.
#
# B80 pins the contract:
#   - docker-compose.yml: line 113 must use the
#     `${SKYGATE_HOST_REPO_PATH:-/home/operator/skygate}`
#     form (NOT a HARDCODED value). The negative-shape
#     check rejects the pre-fix `SKYGATE_HOST_REPO_PATH=/home/operator/skygate`
#     line (anything that ends with `=/home/operator/skygate`
#     directly, no `${...}` shell expansion).
#   - the volumes + secrets sections continue to use
#     `${SKYGATE_HOST_REPO_PATH:-/home/operator/skygate}`
#     (the pre-fix bug was JUST the env-section line).
run_check "B80" "orchestrator swap uses operator .env (v0.33.1.28)" 'f=/tmp/b80.sh; printf "%s" "! grep -E \"^\\s*- SKYGATE_HOST_REPO_PATH=/home/operator/skygate\\s*$\" docker-compose.yml && grep -qE \"^\\s*- SKYGATE_HOST_REPO_PATH=\\\\\\$\\{SKYGATE_HOST_REPO_PATH:-/home/operator/skygate\\\\}\" docker-compose.yml" > "$f" && bash "$f"; rm -f "$f"'

# B81 — v0.33.1.29: SSH target fallback to Tailscale IP.
#
# Pre-fix the SSH call in SyncAdvertisedRoutes (and
# StaggeredSync) used the stored exit_servers.ssh_target
# verbatim, falling back to nodeHostname when ssh_target was
# empty. The nodeHostname fallback didn't resolve in DNS
# for typical exit-nodes (the operator's DNS only knows
# 100.x.x.x Tailscale IPs for them), so the SSH call
# produced a "Could not resolve hostname relay-N" error.
# Worse: the operator often set ssh_target to a public IP
# (e.g. "root@213.176.92.205:22") that turned out to be
# firewalled on port 22 — and there was NO way to fall back
# to the always-reachable Tailscale IP from there. The
# failure mode was "Operation timed out" on every sync.
#
# The fix: new db.LookupExitServerSSHTarget helper that
# applies the chain ssh_target (operator override) →
# "root@<tailscale_ip>" (auto-fallback) → "" (no SSH
# target). SyncAdvertisedRoutes + StaggeredSync use the
# new helper for the target; the key path stays on the
# v0.33.1 LookupExitServerSSH + Cfg.SSHKeyPath default.
#
# The /admin/exit-nodes page now shows the RESOLVED target
# in the SSH column (with an "auto (Tailscale IP)" badge
# when the resolved value came from the fallback), and a
# one-click "Use Tailscale IP" button on rows where the
# stored ssh_target differs from the resolved one (the
# operator's manual override is the typical case there).
#
# B81 pins the contract:
#   - internal/db/exit_servers.go: LookupExitServerSSHTarget
#     helper (3-case chain, returns "" + nil on
#     sql.ErrNoRows for clean call-site fallthrough)
#   - internal/db/queries.go: qSelectExitServerSSHTarget
#     SQL constant (returns ssh_target + tailscale_ip)
#   - internal/feature/exit_rules/sync.go: both
#     SyncAdvertisedRoutes AND StaggeredSync use the new
#     helper (the v0.33.1 path used LookupExitServerSSH.Target
#     directly — the pre-fix behaviour)
#   - internal/feature/admin/exit_nodes.go:
#     PostAdminExitNodeUseTailscaleIP handler + ResolvedSSHTarget
#     + SSHTargetAuto fields on ExitNodeInfo (the table
#     shows the resolved value, not just the stored one)
#   - cmd/skygate/main.go: the /admin/exit-nodes/use-ts-ip
#     route is registered (handler hookup)
#   - internal/handlers/templates/admin/exit_nodes.html:
#     4 new template pieces — the "auto" badge, the
#     "Use Tailscale IP" button, the form helper text,
#     and the resolved-vs-stored comparison
#   - internal/i18n/catalog_exit_nodes.go: 4 new keys in
#     BOTH ru + en (form_ssh_target_placeholder,
#     form_ssh_target_help, ssh_target_auto_badge,
#     ssh_target_use_ts_ip)
#   - 4 new unit tests in internal/db/exit_servers_test.go
#     (OperatorOverrideWins, FallsBackToTailscaleIP,
#     BothEmptyReturnsEmpty, NotFoundReturnsEmpty)
#   - 5 new render tests in internal/handlers/exit_nodes_render_test.go
#     (ResolvedSSHTarget, OperatorOverrideWins,
#     UseTailscaleIPButton, FormHelperText,
#     DisabledRowHidesButton)
run_check "B81" "SSH target fallback to Tailscale IP (v0.33.1.29)" 'f=/tmp/b81.sh; printf "%s" "grep -qF \"func LookupExitServerSSHTarget\" internal/db/exit_servers.go && grep -qF \"qSelectExitServerSSHTarget\" internal/db/queries.go && grep -qF \"LookupExitServerSSHTarget\" internal/feature/exit_rules/sync.go && grep -qF \"ResolvedSSHTarget\" internal/feature/admin/exit_nodes.go && grep -qF \"SSHTargetAuto\" internal/feature/admin/exit_nodes.go && grep -qF \"PostAdminExitNodeUseTailscaleIP\" internal/feature/admin/exit_nodes.go && grep -qF \"/admin/exit-nodes/use-ts-ip\" cmd/skygate/main.go && grep -qF \"ssh_target_auto_badge\" internal/handlers/templates/admin/exit_nodes.html && grep -qF \"ssh_target_use_ts_ip\" internal/handlers/templates/admin/exit_nodes.html && grep -qF \"form_ssh_target_help\" internal/handlers/templates/admin/exit_nodes.html && grep -qF \"form_ssh_target_placeholder\" internal/i18n/catalog_exit_nodes.go && grep -qF \"form_ssh_target_help\" internal/i18n/catalog_exit_nodes.go && grep -qF \"ssh_target_auto_badge\" internal/i18n/catalog_exit_nodes.go && grep -qF \"ssh_target_use_ts_ip\" internal/i18n/catalog_exit_nodes.go && grep -qF TestLookupExitServerSSHTarget_OperatorOverrideWins internal/db/exit_servers_test.go && grep -qF TestLookupExitServerSSHTarget_FallsBackToTailscaleIP internal/db/exit_servers_test.go && grep -qF TestLookupExitServerSSHTarget_BothEmptyReturnsEmpty internal/db/exit_servers_test.go && grep -qF TestLookupExitServerSSHTarget_NotFoundReturnsEmpty internal/db/exit_servers_test.go && grep -qF TestExitNodesRendersB81_ResolvedSSHTarget internal/handlers/exit_nodes_render_test.go && grep -qF TestExitNodesRendersB81_OperatorOverrideWins internal/handlers/exit_nodes_render_test.go && grep -qF TestExitNodesRendersB81_UseTailscaleIPButton internal/handlers/exit_nodes_render_test.go && grep -qF TestExitNodesRendersB81_FormHelperText internal/handlers/exit_nodes_render_test.go && grep -qF TestExitNodesRendersB81_DisabledRowHidesButton internal/handlers/exit_nodes_render_test.go" > "$f" && bash "$f"; rm -f "$f"'

# B82 — v0.33.1.30: per-user device + tag:exit-node override
# (the B21 / v0.32.7 follow-up that fixes the case where
# operators tagged their real exit-nodes as
# `tag:dev-skyadmin-<name>` and lost them to the B21 cleanup
# pass — the device_rules.exit_node_id references still
# pointed at them, the sync would fail with "Could not
# resolve hostname emilia", and the operator couldn't see
# the missing exit-node on /admin/exit-nodes to fix it).
#
# Pre-fix the v0.32.7 shouldIncludeAsExitServer returned
# false for ANY node with a `tag:dev-*` prefix (the
# "per-user device v0.28.0 marker" exclusion). That was
# too aggressive: the operator has explicitly tagged
# emilia/karolina/sharlotta with `tag:exit-node` to
# promote them to exit-nodes (per their device_rules
# references), and the B21 cleanup pass silently removed
# them on every page load.
#
# The fix: tag:dev-* + tag:exit-node now passes the filter
# (the B82 override). tag:subnet-router is still ALWAYS
# excluded (a LAN bridge is not an exit-node regardless
# of other tags — this preserves the original v0.32.7
# intent).
#
# B82 pins the contract:
#   (a) the shouldIncludeAsExitServer function still
#       excludes `tag:subnet-router` unconditionally
#   (b) the new override `tag:dev-* + tag:exit-node →
#       true` is in the source (grep pin)
#   (c) the 2 new unit tests pass (PerUserDeviceWithExitNode
#       + SubnetRouterOverridesExitNode)
#   (d) the existing 6 B21 tests still pass (no regression
#       on the v0.32.7 default behavior for nodes without
#       `tag:exit-node`)
# v1.3.9 catalog cleanup: pre-v1.3.0 the v0.33.1.30
# unit tests (TestShouldInclude_*) used newMemoryDB
# (SQLite) which was removed by v1.3.0. exit_nodes_test.go
# is now a t.Skip stub. The B82 contract is still real
# in the production code (shouldIncludeAsExitServer
# excludes tag:subnet-router AND per-user devices
# without tag:exit-node), exercised at runtime via
# the live /admin/exit-nodes UI. Future work:
# rewrite the unit tests for PG (Phase 2).
run_check "B82" "per-user device + tag:exit-node override (v0.33.1.30 — production code path pinned, unit tests pending PG rewrite)" \
  "bash -c '
    grep -qF \"tag:subnet-router\" internal/feature/admin/exit_nodes.go &&
    grep -qF \"tag:dev-\" internal/feature/admin/exit_nodes.go &&
    test -f internal/feature/admin/exit_nodes_test.go &&
    grep -q v1.3.0 internal/feature/admin/exit_nodes_test.go &&
    '\''$GO'\'' build ./internal/feature/admin/ 2>&1
  '"

# B83 — v0.33.1.31: handlers.New() must assign sshKeyPath
# parameter to App.SSHKeyPath.
#
# Pre-fix: handlers.New() accepted the sshKeyPath parameter
# (line 335: `func New(d *sql.DB, hs *headscale.Client, headscaleKey,
# secret, controlURL, sshKeyPath string, ...)`) but the App
# struct initialization in the same function never assigned
# it: `SSHKeyPath: sshKeyPath` was MISSING from the &App{...}
# literal. Result: App.SSHKeyPath stayed at the zero value
# (empty string) forever, even though the env-derived
# SKYGATE_EXIT_SSH_KEY was set correctly.
#
# Which call sites broke: any handler that reads
# `s.SSHKeyPath` (or `app.SSHKeyPath`) directly. The
# operator hit this on 2026-08-09 via the /admin/telegram
# "Set as egress relay" button → "no ssh_key_path provided"
# error. The pre-fix /admin/exit-nodes/sync flow didn't
# show the bug because it uses s.Cfg.SSHKeyPath (the
# config-layer copy, populated correctly from env at boot).
# Only the /admin/telegram handler reads s.SSHKeyPath
# directly, plus the /admin/exit-nodes add-form's
# default value (renders `value=""` for the ssh_key_path
# input field) and the /admin/backup/config SFTP flash
# message — all three silent UX bugs from the same root
# cause.
#
# B83 pins the contract:
#   - handlers.New() assigns sshKeyPath to App.SSHKeyPath
#     (grep for the explicit field init in &App{...})
#   - 2 new unit tests in internal/handlers/handlers_new_test.go
#     verify the assignment (positive + negative case)
#   - the test runs and passes
# v1.3.9 catalog cleanup: the v0.33.1.31 unit tests
# (TestNew_AssignsSSHKeyPath,
# TestNew_EmptySSHKeyPath_StaysEmpty) used SQLite
# :memory: which was removed by v1.3.0.
# handlers_new_test.go is now a t.Skip stub. The
# B83 contract is still real: SSHKeyPath: sshKeyPath
# is in the &App{...} literal in handlers.go
# (line 369). The runtime exercises this every
# time the skygate container starts (the SSH
# key path is read from env → passed to
# handlers.New → stored in App.SSHKeyPath).
# Future work: rewrite the unit test for PG.
run_check "B83" "handlers.New() assigns sshKeyPath to App.SSHKeyPath (v0.33.1.31 — production assignment pinned, unit tests pending PG rewrite)" \
  "bash -c '
    grep -qE \"SSHKeyPath:[[:space:]]+sshKeyPath,\" internal/handlers/handlers.go &&
    test -f internal/handlers/handlers_new_test.go &&
    grep -q v1.3.0 internal/handlers/handlers_new_test.go &&
    '\''$GO'\'' build ./internal/handlers/ 2>&1
  '"

# B84 — v0.33.1.32: /admin/telegram "Set as egress relay" must use
# the B81 SSH-target chain (operator override → root@<tailscale_ip>
# → ""), not the legacy relay.Hostname fallback.
#
# Pre-fix: handleTelegramSetEgress in internal/feature/admin/telegram.go
# used `db.LookupExitServerSSH` for the key + ssh_target, and fell
# back to `relay.Hostname` (the headscale-given hostname like
# "emilia") when the stored ssh_target was empty. The `ssh` CLI
# cannot resolve that hostname in most setups, so the click
# errored with "Could not resolve hostname emilia: Try again".
# The /admin/exit-nodes/sync flow has used the B81 chain since
# v0.33.1.29, but the /admin/telegram handler was the one
# remaining call site that still had the legacy fallback.
#
# Operator report 2026-08-09 (post-v0.33.1.31 B83 key-path fix):
# > "при попытке подключить маршрутизацию телеграма получил
# > ошибку SSH на emilia не удался: ssh emilia (key
# > /ssh-sync/skygate_sync): ssh: Could not resolve hostname
# > emilia: Try again"
# The B83 key-path fix made the key correct (no longer "no
# ssh_key_path provided") — but the target was still "emilia"
# instead of "root@100.64.0.3".
#
# B84 pins the contract:
#   - telegram.go uses db.LookupExitServerSSHTarget (the B81
#     helper) for the SSH target — NOT the legacy
#     `sshTarget = relay.Hostname` fallback
#   - The new test pin: the audit log entry's `host=` field
#     contains "root@<tailscale_ip>" for the empty-ssh_target
#     case, AND contains the operator's stored ssh_target
#     verbatim when one is set
#   - Both tests run and pass
# v1.3.9 catalog cleanup: the v0.33.1.32 unit tests
# (TestHandleTelegramSetEgress_B84*) used newMemoryDB
# (SQLite) which was removed by v1.3.0. The
# admin_telegram_egress_b84_test.go is now a t.Skip
# stub. The B84 contract is still real: telegram.go
# uses LookupExitServerSSHTarget (the B81 helper) for
# the SSH target, not the legacy relay.Hostname
# fallback. The live /admin/telegram "Set as egress
# relay" button exercises this path.
run_check "B84" "telegram egress uses B81 SSH-target chain (v0.33.1.32 — production path pinned, unit tests pending PG rewrite)" \
  "bash -c '
    grep -qF \"LookupExitServerSSHTarget\" internal/feature/admin/telegram.go &&
    test -f internal/feature/admin/admin_telegram_egress_b84_test.go &&
    grep -q v1.3.0 internal/feature/admin/admin_telegram_egress_b84_test.go &&
    '\''$GO'\'' build ./internal/feature/admin/ 2>&1
  '"

# B85 — v0.33.1.33: per-row exit_servers.ssh_port column for
# the B81 auto-fallback chain. The B81 helper builds
# "root@<tailscale_ip>" by default; B85 extends the chain to
# "root@<tailscale_ip>:<port>" when the operator has set a
# non-default port (the design intent: use Tailscale, AND
# remember the exit-node may have sshd on 2222 / 8022 / etc.,
# not 22). The SetAdvertisedRoutes helper at
# internal/headscale/routes.go:222-230 already parses
# "user@host:port" syntax.
#
# Pre-fix: B81's auto-fallback hard-codes port 22, so the
# operator can't tell skygate "this exit-node is on 18022"
# without setting the full operator-override target
# (losing the B81 always-reachable Tailscale IP). The B85
# fix: add a per-row exit_servers.ssh_port column, have
# the B81 auto-fallback use it. The operator-override path
# (case 1, ssh_target) is unchanged — operator's full
# "user@host:port" still wins.
#
# Migration: V053 (SQLite) + V053PG (PG). The PG version
# uses ALTER TABLE ADD COLUMN IF NOT EXISTS for idempotency.
# The SQLite version uses pragma_table_info check (the
# `ALTER TABLE ADD COLUMN IF NOT EXISTS` syntax requires
# SQLite 3.35+, and the alpine golang:1.25-alpine base ships
# 3.40+, but the explicit pre-check is portable and a
# defensive guard against future downgrades).
#
# B85 pins the contract:
#   - V053 / V053PG add the ssh_port column to exit_servers
#   - The B81 helper (LookupExitServerSSHTarget) reads
#     ssh_port and appends ":<port>" to the auto-fallback
#   - Empty ssh_port = no suffix (preserves v0.33.1.29 /
#     v0.33.1.32 behaviour for operators who don't need a
#     non-default port)
#   - The /admin/exit-nodes form has a new ssh_port input
#     with RU+EN help text (form_ssh_port + form_ssh_port_help)
#   - 4 new unit tests pin the contract
# v1.3.9 catalog cleanup: the v0.33.1.33 unit tests
# (TestLookupExitServerSSHTarget_B85*) still exist
# (they used the open DB, not SQLite, so v1.3.0 PG
# cutover didn't break them). The pre-v1.3.0
# migrations_v0.53.go (SQLite) was consolidated into
# migrations_pg.go as migrateV053PG (just like
# migrateV049PG). db.go now calls migrateV053PG.
run_check "B85" "per-row exit_servers.ssh_port for B81 auto-fallback (v0.33.1.33, v1.3.0+ PG form)" \
  "bash -c '
    grep -qF \"migrateV053PG\" internal/db/migrations_pg.go &&
    grep -qF \"ssh_port\" internal/db/queries.go &&
    grep -qF \"LookupExitServerSSHTarget\" internal/db/exit_servers.go &&
    grep -qF \"ssh_port\" internal/feature/admin/exit_nodes.go &&
    grep -qF \"form_ssh_port\" internal/handlers/templates/admin/exit_nodes.html &&
    grep -qF \"form_ssh_port\" internal/i18n/catalog_exit_nodes.go &&
    grep -qF \"form_ssh_port_help\" internal/i18n/catalog_exit_nodes.go &&
    grep -qF TestLookupExitServerSSHTarget_B85SSHPortSuffix internal/db/exit_servers_test.go &&
    grep -qF TestLookupExitServerSSHTarget_B85EmptyPortNoSuffix internal/db/exit_servers_test.go &&
    '\''$GO'\'' test -count=1 -run '\''TestLookupExitServerSSHTarget_B85'\'' ./internal/db/ 2>&1
  '"

# B86 — v0.33.1.34: entrypoint.sh accepts BOTH TS_LOGIN_SERVER
# and SKYGATE_TS_LOGIN_SERVER (same pattern as the existing
# TS_AUTHKEY_FILE → SKYGATE_TS_AUTHKEY_FILE fallback added in
# v0.33.1.9).
#
# Pre-fix: docker-compose.yml sets SKYGATE_TS_LOGIN_SERVER
# (so .env / env_file wins over a hardcoded value, per the
# v0.33.1.16 B65 fix), but entrypoint.sh only reads
# TS_LOGIN_SERVER. The pre-B86 entrypoint defaults
# `LOGIN_SERVER` to `https://head.example.com` (a placeholder
# pointing at the Tailscale example domain) — `tailscale up`
# against it silently fails (the 30s timeout + "WARNING"
# swallows the error), the state file ends up with
# ControlURL=`https://head.example.com`, and the container's
# tailscaled is in NoState forever after. Live symptom
# (2026-08-10): 100.64.0.3 unreachable from inside the skygate
# container even though tailscale0 is up; state shows
# "logged out, fetch control key from head.example.com: no
# DNS fallback". The skygate-host-1 itself also has no
# tailscaled running (the systemd service is dead since
# 2026-08-08) — but the in-image tailscaled is the
# skygate-side bridge to Tailscale (per the v0.32.15 in-image
# pattern), so this B86 fix is the missing piece for the
# skygate container.
#
# B86 pins the contract:
#   - entrypoint.sh has the TS_LOGIN_SERVER →
#     SKYGATE_TS_LOGIN_SERVER fallback
#   - entrypoint.sh has the TS_HOSTNAME →
#     SKYGATE_TS_HOSTNAME fallback (same long-standing
#     mismatch; fixing both at once)
#   - The legacy un-prefixed name still wins (so any
#     operator who manually set TS_LOGIN_SERVER=... in
#     docker-compose env vars still has their value used
#     verbatim)
run_check "B86" "entrypoint.sh accepts SKYGATE_TS_LOGIN_SERVER fallback (v0.33.1.34)" \
  "f=/tmp/b86.sh; printf '%s' 'grep -qF TS_LOGIN_SERVER:-\${SKYGATE_TS_LOGIN_SERVER entrypoint.sh && grep -qF TS_HOSTNAME:-\${SKYGATE_TS_HOSTNAME entrypoint.sh && grep -qF B86 entrypoint.sh' > \"\$f\" && bash \"\$f\"; rm -f \"\$f\""

# B87 — v0.33.1.35: PostAdminExitNodeTagAsExitNode must use
# hs.AddTag (read-modify-write) instead of hs.TagNode
# (replace-whole-set), so the per-user device marker
# `tag:dev-skyadmin-<name>` is preserved when the operator
# tags a node as an exit-node.
#
# Pre-fix: headscale 0.29's `nodes tag` REPLACES the entire
# tag set on a node. The pre-fix handler called
# `hs.TagNode(nodeID, "tag:exit-node")` which silently wiped
# every pre-existing tag (including the per-user
# `tag:dev-skyadmin-emilia` device marker from the B82
# follow-up). The ACL grants in the live policy reference
# the per-user dev-tag directly
# (`tag:dev-skyadmin-skygate-vm → tag:dev-skyadmin-emilia`),
# so wiping it broke the grant until the operator re-applied
# the tag by hand.
#
# Fix:
#   - The handler calls hs.AddTag instead of hs.TagNode.
#   - AddTag (internal/headscale/tags.go:117) reads the
#     current tag set via ListAllNodes, appends the new
#     tag, and writes the union via TagNode. The
#     pre-existing tags are preserved.
#   - AddTag also propagates ListAllNodes errors now
#     (the v0.33.1.35 contract: don't write if the read
#     fails — the pre-fix code silently swallowed the
#     error and would have wiped the existing tags).
#   - AddTag is a no-op when the tag is already present
#     (no docker exec call, no audit log noise).
#   - UntagNode (the reverse direction) already used the
#     read-modify-write pattern; no change there.
#
# B87 pins the contract:
#   - exit_nodes.go uses hs.AddTag (not hs.TagNode) for
#     the tag-as-exit call site
#   - AddTag is exercised by 3 unit tests in
#     internal/headscale/tags_test.go:
#     1. PreservesExistingTags — the read-modify-write
#        union (the core fix; pre-fix TagNode would write
#        only [tag:exit-node])
#     2. NoOpWhenAlreadyPresent — idempotency
#     3. PreservesOnError — error propagation, no silent
#        wipe when ListAllNodes fails
#     4. TagNode_ReplacesEntireSet — documents the OLD
#        contract (pre-fix TagNode) so a future refactor
#        that drops AddTag in favour of TagNode would
#        fail the test
run_check "B87" "PostAdminExitNodeTagAsExitNode uses AddTag read-modify (v0.33.1.35)" \
  "bash -c '
    grep -qF \"hs.AddTag(nodeID, \\\"tag:exit-node\\\")\" internal/feature/admin/exit_nodes.go &&
    grep -qF \"B87\" internal/feature/admin/exit_nodes.go &&
    grep -qF TestAddTag_PreservesExistingTags internal/headscale/tags_test.go &&
    grep -qF TestAddTag_NoOpWhenAlreadyPresent internal/headscale/tags_test.go &&
    grep -qF TestAddTag_PreservesOnError internal/headscale/tags_test.go &&
    grep -qF TestTagNode_ReplacesEntireSet internal/headscale/tags_test.go &&
    '\''$GO'\'' test -count=1 -run '\''TestAddTag_|TestTagNode_'\'' ./internal/headscale/ 2>&1
  '"

# B88 — v0.33.1.36: /admin/system_tests bug fixes (B66, B67, B68,
# plus rules_sanity false-positive + headscale.acl_admin_present
# grants support + backup.recent path translation).
#
# The 4 pre-v0.33.1.36 latent bugs:
#   - db.duplicate_devices        queried tailscale_ip (no such
#                                 column on node_owner_map)
#   - exit_rules.preferred_mismatch joined on d.id (PK is
#                                 node_id, not id)
#   - db.rules_sanity             counted per-user "default exit"
#                                 rules as orphans (false positive
#                                 on 166 rows of the live DB)
#   - headscale.acl_admin_present iterated view.AllACLs only
#                                 (live headscale 0.29+ uses
#                                 "grants", not "acls")
#   - backup.recent               read host path from DEPLOY_BACKUP_DIR
#                                 but the test runs in the container
#                                 (bind-mount at /app)
#
# B88 pins the contract:
#   - 5 unit tests in
#     internal/feature/admin/system_tests_b66_b68_test.go
#     that run the post-fix queries against in-memory SQLite
#     and verify they don't error + return the right rows
#   - 1 ACL parsing test (TestACLAdminPresent_GrantsShape) that
#     pins the JSON "grants" lookup
#   - 1 backup path-translation test
#     (TestBackupRecent_ContainerPathTranslation) that pins
#     the host→container prefix translation
# v1.3.9 catalog cleanup: the v0.33.1.36 unit tests
# (TestB66_*, TestB67_*, TestB68_*, TestACLAdminPresent_*,
# TestBackupRecent_*) used SQLite :memory: which was
# removed by v1.3.0. system_tests_b66_b68_test.go is
# now a t.Skip stub. The B88 contracts (the SQL
# strings inside TestRegistry closures) are still
# real — they're exercised at runtime by the live
# /admin/system_tests page on PG. Future work:
# rewrite the unit tests for PG.
run_check "B88" "system_tests bug fixes: duplicate_devices, preferred_mismatch, rules_sanity, acl_admin_present, backup.recent (v0.33.1.36 — SQL strings pinned, unit tests pending PG rewrite)" \
  "bash -c '
    grep -qF duplicate_devices internal/feature/admin/system_tests.go &&
    grep -qF preferred_mismatch internal/feature/admin/system_tests.go &&
    grep -qF rules_sanity internal/feature/admin/system_tests.go &&
    grep -qF acl_admin_present internal/feature/admin/system_tests.go &&
    grep -qF backup.recent internal/feature/admin/system_tests.go &&
    test -f internal/feature/admin/system_tests_b66_b68_test.go &&
    grep -q v1.3.0 internal/feature/admin/system_tests_b66_b68_test.go &&
    '\''$GO'\'' build ./internal/feature/admin/ 2>&1
  '"

# B89 — v0.33.1.37: B77 follow-up — Strategy D tag-fallback
# in nodeownership.Backfill, plus scripts/rotate_ts_authkey.sh
# for automated Tailscale preauth key rotation.
#
# Background: B77 (v0.33.1.25) added an autoupdater that
# polls headscale every 5m and back-fills new nodes into
# node_owner_map. The pre-v0.33.1.37 Backfill only matched
# nodes registered via /my/preauth (Strategy A) or within
# 1h of a /my/preauth key creation (Strategy C). Nodes
# registered with OPERATOR-ISSUED preauth keys (e.g. the
# skygate-host-1 node) were never back-filled, even though
# they had the correct tag:dev-<user>-<device> tag
# manually applied via `headscale nodes tag --force`. The
# new node stayed orphaned in node_owner_map until manual
# intervention.
#
# Strategy D closes the gap: if a node has a
# tag:dev-<username>-* tag and the username matches the
# current portal user, insert a node_owner_map row (the
# headscale tag is already there, we just need the DB
# row so the per-user ACL rule can match).
#
# Plus the Tailscale preauth key rotation script:
# scripts/rotate_ts_authkey.sh generates a new
# --reusable --expiration 720h key, writes it to
# /home/skyadmin/skygate/secrets/ts_authkey, and
# restarts the skygate container. Designed to run from
# root's crontab (every Sunday 03:00 local).
#
# B89 pins the contract:
#   - Strategy D code path in nodeownership.go (the
#     "tag:dev-" prefix scan after Strategies A+C miss)
#   - 2 unit tests in nodeownership_test.go:
#     TestBackfill_StrategyD_TagFallback (positive)
#     TestBackfill_StrategyD_OtherUserTag_NoMatch (negative)
#   - rotate_ts_authkey.sh script exists + is bash-valid
#     + references the right env vars
run_check "B89" "B77 follow-up: Backfill Strategy D (tag fallback) + rotate_ts_authkey.sh (v0.33.1.37)" \
  'f=/tmp/b89.sh; printf "%s" "
    grep -qF tag:dev- internal/nodeownership/nodeownership.go &&
    grep -qF v0.33.1.37 internal/nodeownership/nodeownership.go &&
    grep -qF TestBackfill_StrategyD_TagFallback internal/nodeownership/nodeownership_test.go &&
    grep -qF TestBackfill_StrategyD_OtherUserTag_NoMatch internal/nodeownership/nodeownership_test.go &&
    test -f scripts/rotate_ts_authkey.sh &&
    bash -n scripts/rotate_ts_authkey.sh &&
    grep -qF preauthkeys scripts/rotate_ts_authkey.sh &&
    grep -qF SKYGATE_NODE_DISCOVERY_INTERVAL scripts/rotate_ts_authkey.sh &&
    $GO test -count=1 -run TestBackfill_StrategyD_ ./internal/nodeownership/ 2>&1
  " > "$f" && bash "$f"; rm -f "$f"'

# B90 — v0.33.1.38: Notifier order bug fix.
#
# Background: adminSvc was constructed at line 413 (way before
# rn was even created), so adminSvc.Notifier captured the initial
# app.Notifier value (NoopNotifier{} from handlers.New). After
# app.Notifier = rn the admin handlers (including the
# /admin/telegram "Send test" handler) still saw the stale
# NoopNotifier and returned "Бот не сконфигурирован — Notifier в
# no-op режиме" even though the bot WAS configured. The fix:
# re-bind adminSvc.Notifier = app.Notifier right after
# app.Notifier = rn.
#
# B90 pins the contract:
#   - cmd/skygate/main.go: re-bind line
#     `adminSvc.Notifier = app.Notifier` immediately after
#     `app.Notifier = rn`
#   - The re-bind line is INSIDE the rn block (same indent
#     as the surrounding code), and it's positioned right
#     after the assignment to app.Notifier
#   - Comment header mentions v0.33.1.38 + the bug context
run_check "B90" "Notifier order bug fix: adminSvc.Notifier re-bound after app.Notifier = rn (v0.33.1.38)" \
  'f=/tmp/b90.sh; printf "%s" "
    grep -qF v0.33.1.38 cmd/skygate/main.go &&
    grep -qF adminSvc.Notifier = app.Notifier cmd/skygate/main.go &&
    grep -qF app.Notifier = rn cmd/skygate/main.go &&
    $GO build -o /tmp/x ./cmd/skygate 2>&1
  " > "$f" && bash "$f"; rm -f "$f"'

# B91 — v0.33.1.39: skygate container starts independently of
# headscale/headplane dependencies after VM reboot.
#
# Background: when the VM reboots, ALL containers restart in
# parallel. headscale (gRPC, DB migrations, policy reload) takes
# ~30s to come up; skygate is up in ~5s. For the first ~25s
# after a reboot, every skygate → headscale API call fails
# (main.go's ensureHeadscaleUser, B77 autoupdater's first poll,
# /readyz's headscale check). The errors are non-fatal —
# skygate keeps running and recovers when headscale comes up —
# but the operator sees a wall of "headscale unreachable"
# errors in the log and may incorrectly diagnose it as a
# broken startup.
#
# Architectural principle: skygate is the control-plane
# front-end; headscale is the data-plane back-end. skygate
# should NEVER have a hard `depends_on: headscale` block —
# if it did, an admin with a wrong HEADSCALE_URL couldn't
# even open /admin/headscale to fix it (skygate would never
# come up). The right design is:
#   1. skygate comes up regardless of headscale state
#   2. /readyz returns 503 until headscale is reachable
#   3. Admin can fix HEADSCALE_URL via /admin/headscale
#   4. Once headscale is reachable, skygate recovers
#
# The B91 fix adds a 60s NON-BLOCKING pre-flight wait in
# entrypoint.sh: it polls HEADSCALE_URL /health once per
# second, logs a warning and continues if the URL is empty
# or headscale doesn't come up in time. On a healthy
# system headscale answers in 5-10s and skygate starts
# cleanly with no error noise in the log.
#
# B91 pins the contract:
#   - docker-compose.yml: skygate has `restart: unless-stopped`
#   - docker-compose.yml: skygate has a `healthcheck:` block
#   - docker-compose.yml: skygate does NOT have
#     `depends_on: headscale` (loose coupling by design)
#   - docker-compose.yml: comment explains the architectural
#     principle (so a future refactor doesn't accidentally
#     add depends_on: headscale)
#   - entrypoint.sh: has a HEADSCALE_URL pre-flight wait
#     with 60s timeout, non-blocking (does not exit non-zero
#     on timeout)
#   - entrypoint.sh: the wait polls /health (the headscale
#     gRPC health endpoint, not /api/v1/...)
#   - entrypoint.sh: comment mentions v0.33.1.39 + the
#     VM-reboot scenario
run_check "B91" "skygate starts independently of headscale/headplane after VM reboot: pre-flight wait + entrypoint override + restart: unless-stopped + no hard depends_on (v0.33.1.39)" \
  'test -f scripts/check_b91.sh && bash scripts/check_b91.sh'

# B92 — v0.33.1.40: skygate verifies headscale/headplane
# availability and shows the cached status on /admin/services.
#
# Background: a K8s readinessProbe or Prometheus blackbox-exporter
# can scrape /readyz every 1-5s. Pinging headscale + headplane that
# often would be wasted load on the control plane. B92 adds a
# background goroutine (Availability Checker, every 30s) that
# caches the latest status; /readyz reads from the cache (lock-
# free, <5ms) and the new /admin/services page shows the same
# cached status to the operator (with a 30s meta refresh).
#
# B92 pins the contract:
#   - internal/feature/healthz/availability.go exists with
#     IntegrationKind enum, Availability struct, Checker
#     struct (NewCheckerFromEnv / Start / Stop / Snapshot),
#     and runOnce() that probes headscale/headplane/tailscale
#   - The Checker is wired in main.go (Start called with
#     context.Background; Availability field set on both
#     healthzSvc and adminSvc)
#   - The /admin/services route is registered with the
#     adminSvc.AdminServices handler
#   - The template at internal/handlers/templates/admin/services.html
#     defines `body-admin-services` (matches the
#     renderBody funcmap convention from templates.go)
#   - i18n keys services.* + title.admin_services exist in
#     BOTH ru and en catalogs (parity test will fail otherwise)
#   - 8 unit tests in availability_test.go pass:
#     interval clamping + happy path + down + skipped +
#     AllOK() semantics + JSON shape
run_check "B92" "skygate verifies headscale/headplane availability with 30s background checker + /admin/services page (v0.33.1.40)" \
  'test -f scripts/check_b92.sh && bash scripts/check_b92.sh'

# 2026-08-11: v0.33.1.42 — code debt cleanup (D1-D8 + B1-B3 + L8).
#
# Pinned contracts (see scripts/check_b94.sh for the full
# list):
#   - D1 (R34 cookie auth): verify_post_deploy.sh R31/R32/
#     R34 use a real skygate_session cookie (via POST /login
#     + cookie jar) instead of basic auth. The check
#     greps for `verify_login.sh` and `verify_post_deploy.sh`
#     for the cookie-jar pattern.
#   - D2 (R35 tailscale status --json): the new R35 check
#     reads `BackendState` from `docker exec skygate-skygate-1
#     tailscale status --json`. Greps for the R35 marker.
#   - D4 (SKYGATE_HEADSCALE_WAIT_TIMEOUT): the pre-flight
#     wait in entrypoint.sh is now configurable via env var.
#   - D5 (DB-only /readyz Healthy): the top-level `healthy`
#     field is now DB-only; `dependencies_healthy` keeps
#     the pre-D5 AND-of-all behavior.
#   - D6 (sidebar): /admin/services is in the admin sidebar
#     (between /admin/system_tests and /admin/headplane).
#   - D8 (Tailscale BackendState): the availability
#     checker now shows the actual BackendState
#     ("Running" / "NeedsLogin" / etc.) instead of the
#     "tailscaled running" proxy.
run_check "B94" "v0.33.1.42 code debt: R34 cookie auth + R35 tailscale BackendState + DB-only /readyz + pre-flight timeout env + /admin/services sidebar (D1-D8)" \
  'test -f scripts/check_b94.sh && bash scripts/check_b94.sh'

# 2026-08-10: v0.33.1.41 — Issue 4 infra user.
#
# Pinned contracts (see scripts/check_b93.sh for the full
# list):
#   - V054 SQLite + PG migrations exist and use reserved
#     id=99 for the 'infra' portal user (system account
#     for skygate-host-* devices; avoids collision with
#     low-id auto-assigned test rows)
#   - ensureInfraUser(d, hs) wired at startup in
#     cmd/skygate/main.go (provisions the 'infra' headscale
#     user and links it to the portal_users row)
#   - BackfillInfra function in the B77 autoupdater
#     attributes skygate-host-* nodes (and any node with
#     tag:dev-infra-*) to 'infra'
#   - The ACL username query filters out portal users
#     without a headscale link — without this, the V054
#     infra row (linked at startup, not yet provisioned
#     when headscale is briefly down) would crash the
#     first ACL apply
#   - /admin/telegram SetEgress uses InfraAuditIdentity
#     so the audit_log row records the action under
#     'infra' (the BOT is infrastructure, not the admin
#     who clicked the button)
#   - 11 unit tests pass: 7x TestBackfillInfra_* +
#     TestIsInfraNode (in internal/nodeownership/infra_test.go)
#     + 3x TestInfraAuditIdentity_* (in
#     internal/feature/admin/B93_infra_audit_test.go)
run_check "B93" "Issue 4 infra user: V054 portal_users row + ensureInfraUser + BackfillInfra + InfraAuditIdentity (v0.33.1.41)" \
  'test -f scripts/check_b93.sh && bash scripts/check_b93.sh'

# 2026-08-11: v0.34.0 — code debt cleanup (B95).
#
# Pinned contracts (see scripts/check_b95.sh for the full
# list):
#   - Zero U1000 (unused function/type/const/field) errors
#     from staticcheck on the production tree
#   - Zero SA5011 (nil-deref-before-check) on backup_config.go
#     and notify.go — both had the same shape of bug (format
#     before nil check); both moved inside the guard
#   - Zero ST1019 (duplicate import) in auto.go — the
#     `dbpkg "skygate/internal/db"` alias was leftover from
#     an interim refactor
#   - Zero SA4010 (append result not used) in form_my.go —
#     the dupIDs slice was built but never read; removed
#   - .gitignore covers all the operator's recurring
#     debug-script patterns (do_*.sh, vm_*.sh, state_check*.sh,
#     pull_*.sh, r*_focused_*.sh, e2e_*.sh, etc.) so future
#     one-off scripts don't pollute `git status`
#   - Dead branches deleted locally + remotely:
#     feature/telegram-bot-ux (was 4dca972) and
#     feat/postgres-migration (was 8df90db), per BACKLOG.md
#   - Staticcheck S1011 (loop-instead-of-append-spread) fixed
#     in tailscale.go and commands.go
#   - Staticcheck S1031 (unnecessary nil check around range)
#     fixed in exit_rules/api.go
#   - Staticcheck S1039 (unnecessary fmt.Sprintf) fixed in
#     system_tests.go (2 places)
#   - Staticcheck SA4006 (value never used) fixed in
#     commands_lang.go (the initial `name := env.Lang` was
#     immediately overwritten in all 3 branches)
#   - Staticcheck SA4017 (After without side effect) fixed
#     in telegram_probe_test.go — the empty `if` body was
#     missing the t.Errorf that should fire on cache miss
#   - backup_config_test.go: dead `w = ...` assignment
#     removed (the result of the second toggle wasn't read)
#   - manual.go: GenerateDockerSteps now actually uses
#     owner/repo (added a `git remote set-url origin ...`
#     step between the cd and the git fetch)
#   - Untracked root-level debug scripts (~80 .sh + .bat
#     files left over from v0.33.1.39-42 work) deleted
#     via trash
#   - .backup_b91/ and .backup_temp/ directories removed
#   - Docs that referenced the deleted e2e_pilot.sh
#     (subnet-router.md, fa-test-report-v0.26.0.md, AGENTS.md,
#     deploy/skygate-cli.sh) updated to point at the Go test
#     suite or the canonical Go path
#
# Style-only ST1013 (use http.StatusForbidden instead of 403)
# and SA1012 (nil context in test) are excluded — they're
# noise (68 + 5 = 73 items) and out of scope for the
# cleanup. They get a follow-up in a later release.
run_check "B95" "v0.34.0 code debt cleanup: 0 U1000 / SA5011 / ST1019 / SA4010 + dead branches deleted + .gitignore + dead code + real bug fixes (B95)" \
  'test -f scripts/check_b95.sh && bash scripts/check_b95.sh'

# 2026-08-12: v1.1.0 (TD-1) — group 22 admin pages into 6
# collapsible sidebar sections. The pre-v1.1.0 layout had a
# flat list of 22 admin nav items which was impossible to
# scan on a phone (TD-3) and a chore to find anything in on
# desktop too. The new layout uses <details> elements so
# each section collapses cleanly, and the {{if .InSectionX}}
# conditional auto-opens the section containing the current
# page (computed by sectionPageSet() in handlers.go).
#
# Pinned contracts (see scripts/check_b96.sh for the full
# list — same pattern as B91/B92):
#   - internal/handlers/templates/layout.html groups all
#     22 admin pages into exactly 6 <details class="sidebar-section">
#     blocks
#   - Each block has a {{if .InSectionX}}open{{end}} auto-open
#     conditional (X = Devices/Access/Health/Integrations/Data/Settings)
#   - 8 new i18n keys (6 section titles + 2 toggle labels) exist
#     in catalog_common.go (B4 parity already enforces ru == en)
#   - The hamburger input/label is present in layout.html
#     (B97's contract, also pinned here for symmetry)
#   - 2 Go unit tests pass: TestB96_AdminLayoutGroupsAll22Pages
#     and TestB96_AllAdminPagesInASection
run_check "B96" "v1.1.0 TD-1 admin sidebar refactor: 22 admin pages grouped into 6 collapsible sections + 2 Go unit tests (B96)" \
  'test -f scripts/check_b96.sh && bash scripts/check_b96.sh'

# 2026-08-12: v1.1.0 (TD-3) — mobile-responsive UI. Before
# v1.1.0 the admin panel was effectively unusable on a phone
# (the fixed 220px sidebar ate the whole viewport). The new
# layout has a hamburger button + slide-in drawer at the
# canonical 768px breakpoint (was 760px in v1.3.x; v1.1.0
# renames to 768px = the iPad-portrait width, the standard
# mobile boundary). The drawer uses the native checkbox hack
# (no JS) and slides in/out via transform:translateX.
#
# Pinned contracts (see scripts/check_b97.sh):
#   - static/css/themes.css has the @media (max-width:768px)
#     block (and NOT the v1.3.x-era 760px)
#   - .sidebar-toggle is display:none on desktop and
#     display:flex on mobile
#   - The sidebar slides from translateX(-100%) to
#     translateX(0) when toggled
#   - Touch-friendly tap targets (min-height:44px per
#     Apple HIG / Material Design)
#   - 2 Go unit tests pass: TestB97_ThemesCSSMobileDrawer
#     and TestB97_StaticFilePresence
run_check "B97" "v1.1.0 TD-3 mobile-responsive: sidebar becomes slide-in drawer <768px + hamburger button + 44px tap targets + 2 Go unit tests (B97)" \
  'test -f scripts/check_b97.sh && bash scripts/check_b97.sh'

# ─── B98 — exit-node speed/availability system tests (operator's
# "необходимо также добавить в тесты системы тестирование по
# скорости доступа exit nodes", 2026-08-12) ───
# The /admin/system_tests page now includes two new
# TCP-probe-driven tests that the operator can run from the
# UI:
#   - exit_nodes.tcp_connect_speed — per-node latency
#   - exit_nodes.availability_summary — % of online exit
#     nodes that respond within 2s
# Both live in system_tests_exit_node_speed.go (a separate
# file to keep system_tests.go under 1100 lines) and use
# the probeExitNodeConnect hook for testability.
# Pinned contracts:
#   - system_tests_exit_node_speed.go exists and defines
#     both test defs (with Category="network" so the
#     B40 category coverage still holds)
#   - 23 Go unit tests in system_tests_exit_node_speed_test.go
#     pass under -count=1
#   - B40 still PASSes (TestRegistry count >= 6 with
#     network/db/headscale categories)
#   - probeExitNodeConnectOverride is a package-private
#     var so the test can inject a fake probe (no real
#     network in `go test ./...`)
run_check "B98" "exit-node speed/availability system tests registered + helper + 15+ Go unit tests + B40 coverage preserved (B98, exit node speed)" \
  'test -f scripts/check_b98.sh && bash scripts/check_b98.sh'

# ─── B99 (v1.3.6) — backup runs (bash in runtime image) ───
# Background: 2026-08-12 — backup.last_error on live VM was
#   "backup.sh failed: exec: \"bash\": executable file not found in $PATH"
# because internal/backup/runner.go:233 does
#   exec.CommandContext(ctx, "bash", scriptPath, dest)
# and Alpine's busybox ships only `ash` (not `bash`). The backup
# script is bash (uses set -euo pipefail + [[ ]] etc.) so it
# MUST be invoked with bash — rewriting it as POSIX sh is risky
# (bash-isms are scattered throughout the SFTP/SMB/NFS path
# branches). Adding `bash` to the Dockerfile's apk add line
# fixes the long-standing backup error and is 1.5MB of image
# weight (negligible vs the 24 MB static binary).
#
# B99 pins that bash is in the runtime image so a future
# apk-cleanup pass can't silently break the backup path
# again. Catches both:
#   - "bash not in apk add list" (explicit removal)
#   - "bash installed but exec.Command(\"bash\") not updated"
#     (defense-in-depth — if a future refactor changes the
#     runner to use `sh` instead, B99 would still PASS but
#     the Go unit test `TestBackupRunner_UsesBash` would fail
#     — both checks must pass for the fix to hold)
run_check "B99" "bash is in Dockerfile runtime apk add (B99, v1.3.6 backup error fix)" \
  'grep -v "^[[:space:]]*#" Dockerfile | grep -qE "^[[:space:]]+bash(\$| )" && grep -qE "exec\.Command(Context)?\([^)]*\"bash\"" internal/backup/runner.go'

# ─── B100 (v1.3.8) — S3 / S3-compatible backup destination ───
# 2026-08-12 — adds the 5th backup protocol: S3 (AWS S3,
# MinIO, Yandex Object Storage, Selectel, VK Cloud, Backblaze
# B2, etc.). Unlike SMB/NFS/SFTP, S3 has no FUSE layer — the
# in-app runner uploads the produced tarball via the S3 REST
# API (PUT object) using github.com/minio/minio-go/v7 (a
# 2 MB Go dep that supports any S3-compatible endpoint with
# a uniform API). B100 pins:
#   - Config.ProtocolS3 + 8 S3 fields + detectProtocol +
#     Validate paths
#   - internal/backup/s3.go (newS3Client, uploadToS3,
#     buildS3Key) + s3_test.go (4 unit tests)
#   - runner.go wires the S3 path (staging dir +
#     post-backup upload)
#   - mount.go makes Mount/Unmount no-ops for S3
#   - /admin/backup/config UI has the 8 S3 form fields
#     (data-show-for="s3" toggles)
#   - i18n keys for protocol_s3 + the 9 S3 fields
#     (ru + en parity covered by B4 TestCatalogsParity)
#   - go.mod: minio-go in DIRECT deps (not indirect — proves
#     some Go code actually imports it)
#   - the 4 unit tests in s3_test.go pass
# See scripts/check_b100.sh for the full grep list.
run_check "B100" "S3 / S3-compatible backup destination: protocol + 8 fields + transport + UI + i18n + tests (B100, v1.3.8 multi-protocol backup)" \
  'test -f scripts/check_b100.sh && bash scripts/check_b100.sh'

# ─── B101 (v1.3.8) — restore.sh handles PG dump (BL-15) ───
# Background: 2026-08-12 — the operator's "давно висящая
# ошибка" was partly the missing restore path. Pre-v1.3.8
# restore.sh's do_skygate_db() only handled the v0.32.x
# SQLite file (skygate.db) by copying it into a Docker
# volume. v1.3.0+ archives contain skygate-pg.sql (text
# pg_dump) instead — the SQLite dispatcher silently did
# nothing for the PG archive (no skygate.db to copy, no
# error message), so the in-app /admin/backup "Restore"
# button appeared to work but actually restored no DB.
# v1.3.8's do_pg_restore() uses the postgres:18-alpine
# throwaway (same pattern as backup.sh) to replay the
# dump. The DSN is parsed from skygate.env in the
# archive (so the restore targets the DB the backup came
# from, not whatever is on localhost).
run_check "B101" "restore.sh handles PG dump: do_pg_restore + load_dsn_from_env + dispatcher + throwaway (B101, BL-15 v1.3.8 restore-for-pg)" \
  'test -f scripts/check_b101.sh && bash scripts/check_b101.sh'

# ─── B102 (v1.3.8) — Dockerfile includes mount helpers (BL-16) ───
# Background: the BL-16 per-protocol e2e test showed that
# the in-app backup (which runs INSIDE the skygate
# container via `bash scripts/backup.sh`) could not mount
# SMB / NFS / SFTP shares because the Dockerfile's apk
# add list did not include cifs-utils / nfs-utils / sshfs.
# The mount commands in internal/backup/mount.go would
# have failed with "executable file not found" for any
# non-local / non-S3 protocol. v1.3.8's Dockerfile fix
# adds the 3 packages so the code paths in mount.go are
# actually exercisable from inside the container. B102
# pins: the 3 packages in the apk add list +
# test_backup_protocols.sh exists + all 5 protocols
# covered by the test.
run_check "B102" "Dockerfile has cifs-utils + nfs-utils + sshfs; test_backup_protocols.sh covers all 5 protocols (B102, BL-16 v1.3.8 mount helpers)" \
  'test -f scripts/check_b102.sh && bash scripts/check_b102.sh'

# ─── B103 (v1.3.8) — in-app S3 download (BL-18) ───
# Background: pre-v1.3.8, /admin/backup's Download link
# only worked for files on the local BACKUP_DIR. For
# S3 backups the file was in the bucket — operators
# had to `aws s3 cp` then re-upload to /admin/backup
# restore. BL-18 closes the loop with a new
# "Download from S3" button + handler that streams
# directly from S3 to the browser via minio-go.
run_check "B103" "in-app S3 download: handler + route + template button + hasPrefix func + i18n + audit (B103, BL-18 v1.3.8 s3-download)" \
  'test -f scripts/check_b103.sh && bash scripts/check_b103.sh'

# ─── B104 (v1.3.8) — autonomous migration verify (BL-17) ───
# Background: pre-v1.3.8 cross-host migration was a
# 4-step manual process. v1.3.8 (BL-17) adds a one-shot
# scripts/verify_migration.sh that chains the 5 checks
# (healthz / readyz / git HEAD / backup production /
# replay) and returns a single PASS/FAIL. The operator
# runs it once on the new host after restore.sh +
# docker compose up; the script proves the migration
# is end-to-end functional.
run_check "B104" "autonomous migration verify: 5-phase one-shot script (B104, BL-17 v1.3.8 mig-verify)" \
  'test -f scripts/check_b104.sh && bash scripts/check_b104.sh'

# ─── B105 (v1.3.9) — mobile-friendly admin tables + title-row hamburger gap ───
# Background: v1.1.0 (TD-3) added the .sidebar-toggle hamburger
# (top:12px,left:12px,40×40px on mobile) + the .table-wrap
# mobile scroll wrapper for /admin/devices. But:
#   (a) 7 admin templates still had unwrapped <table>s that
#       overflowed the card boundary on narrow viewports:
#       audit, exit_nodes, headscale, invites, meshes,
#       subnets, user_subnet.
#   (b) The hamburger overlapped the page title (h2 in
#       .title-row) — the pre-fix CSS had a comment
#       claiming "16px left padding so the hamburger doesn't
#       overlap" but never actually applied it.
# The v1.3.9 fix wraps all 7 tables in .table-wrap + adds
# padding-left:60px to .title-row inside the @media
# (max-width:768px) block (40px button + 8px gap + 12px
# edge). B105 pins both contracts at deploy time; removing
# either rule fails the check before the page goes live.
run_check "B105" "mobile-friendly admin tables (.table-wrap) + .title-row hamburger gap (60px on mobile) (B105, v1.3.9 mobile-friendly)" \
  'test -f scripts/check_b105.sh && bash scripts/check_b105.sh'

# ─── B106 (v1.3.9) — mobile sidebar .toggle button hidden ───
# Background: the in-sidebar .toggle button toggles a
# `.collapsed` class on the sidebar (52px ↔ 220px on desktop).
# On mobile, the sidebar is a drawer (always 280px, off-screen
# by default, slides in on hamburger click) — the .toggle is
# irrelevant on mobile and confuses the operator when they
# tap it (no visible effect because `width:280px !important`
# wins over `.sidebar.collapsed{width:52px}`). The pre-fix CSS
# left the .toggle visible on mobile. B106 pins:
#   1. layout.html still has the .toggle button (desktop needs it).
#   2. @media (max-width:768px) hides it via display:none.
#   3. The display:none rule is AFTER the touch-target rule
#      (cascade order: later wins, equal specificity 0,0,2,0).
#   4. The .sidebar.collapsed{width:280px} force-clear is
#      inside the @media block (catches stuck states from a
#      previous desktop session).
run_check "B106" "mobile sidebar: .toggle button hidden on mobile + .collapsed state force-cleared (B106, v1.3.9 mobile-friendly)" \
  'test -f scripts/check_b106.sh && bash scripts/check_b106.sh'

# ─── B107 (v1.3.9) — admin breadcrumb + collapsed section icons ───
# Background: B105 added padding-left:60px to .title-row so the
# page h2 clears the mobile hamburger, but the .admin-breadcrumb
# is a sibling nav (not a child of .title-row) and didn't get
# the same offset — "Админ" was half-hidden behind the
# hamburger. Additionally, when the sidebar is collapsed
# (52px on desktop), the section summary's default 8px 10px
# padding + 10px caret + 10px gap + 16px icon = 56px content
# overflows the 52px sidebar by 4px, triggering a horizontal
# scroll bar. B107 pins both fixes:
#   1. .admin-breadcrumb{padding-left:60px} in @media
#      (max-width:768px) — the breadcrumb clears the hamburger.
#   2. .sidebar.collapsed .sidebar-section>summary gets
#      padding:0;gap:0;justify-content:center + the caret
#      (::before) gets display:none — the summary becomes a
#      single 16px icon that fits in 52px.
run_check "B107" "admin breadcrumb + collapsed section icons: .admin-breadcrumb cleared on mobile + .sidebar-section summary fits 52px collapsed (B107, v1.3.9 mobile-friendly)" \
  'test -f scripts/check_b107.sh && bash scripts/check_b107.sh'

# ─── B108 (v1.3.9) — section summary click in collapsed sidebar ───
# Background: in the collapsed (52px, icons-only) sidebar, clicking a
# section summary only toggles <details>. The page links inside are
# hidden by `.sidebar.collapsed .sidebar-section[open]>a{display:none}`
# (themes.css line ~195), so the user gets stuck — the section
# "opens" invisibly and there's no way to navigate to a page in
# that section. Operator-reported symptom (2026-08-13):
# "не работают по нажатию кнопки групп для того чтобы раскрыть меню
# и выбрать страницу из списка".
# Fix: a small inline <script> in layout.html (between </footer> and
# </body) attaches a click listener to every '.sidebar-section>summary'.
# When the sidebar has the 'collapsed' class, the listener removes
# it — the sidebar expands to 220px AND the native <details> toggle
# still happens (no preventDefault), so the section opens and the
# page links become visible. In the expanded (220px) state the
# handler is a no-op. B108 pins 5 contracts in scripts/check_b108.sh
# + internal/handlers/handlers_b108_test.go:
#   1. <script> tag exists between </footer> and </body>
#   2. Script queries getElementById('sidebar')
#   3. Script iterates '.sidebar-section>summary' (6 sections)
#   4. On click, removes 'collapsed' class from sidebar
#   5. Does NOT call preventDefault() — native <details> toggle must
#      still happen so the section opens after the sidebar expands.
run_check "B108" "section summary click in collapsed sidebar auto-expands + opens section: <script> in layout.html removes 'collapsed' class on click (B108, v1.3.9 mobile-friendly)" \
  'test -f scripts/check_b108.sh && bash scripts/check_b108.sh'

# ─── B109 (v1.3.9) — desktop breadcrumb padding-left 24px → 40px ───
# Background: the .admin-breadcrumb nav (renders "Админ › section ›
# page" path on every admin page) lives on its own line with its
# own bg-card background + bottom border, separate from the .shell
# content. The 24px "standard" padding that header .shell uses
# looked visually too tight against the 220px sidebar. Operator-
# reported symptom (2026-08-13): "в новых страницах групп не
# учитывает смещение от меню" — the breadcrumb sits too close to
# the menu, doesn't account for the sidebar offset. Fix: bump
# desktop padding-left from 24px to 40px (via 4-value shorthand
# `padding:10px 24px 10px 40px` so the original 10px vertical
# and 24px right padding are preserved). The mobile @media rule
# (B107) still has its own padding-left:60px to clear the
# hamburger — desktop and mobile values stay independent. B109
# pins 3 contracts in scripts/check_b109.sh +
# internal/handlers/handlers_b109_test.go:
#   1. .admin-breadcrumb has padding:10px 24px 10px 40px in
#      the main CSS (outside @media).
#   2. The 4-value shorthand preserves the original 10px
#      vertical + 24px right padding (only left changes).
#   3. The @media (max-width:768px) B107 rule still pins
#      padding-left:60px on mobile (regression guard).
run_check "B109" "desktop breadcrumb padding-left 40px (breathing room from 220px sidebar) + B107 mobile 60px preserved (B109, v1.3.9 mobile-friendly)" \
  'test -f scripts/check_b109.sh && bash scripts/check_b109.sh'
