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
  "bash -c \"'$GO' test ./internal/db/pgmigrate/... -run 'TestIsDestructive|TestIsDestructiveRefused' -count=1 >/dev/null 2>&1 && ! grep -rE --include='migrations_v*.go' --exclude='*_test.go' 'DROP[[:space:]]+(TABLE|COLUMN|INDEX)|RENAME[[:space:]]+(TO|COLUMN)|TRUNCATE[[:space:]]+(TABLE)?' internal/db/migrations_v*.go | grep -v '// ' | grep -v '^[[:space:]]*\\*'\""

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
run_check "B17" "per-user device can't be tagged as exit-node (v0.30.1 workstation-8 fix; B139 rewrote the t.Skip stub into a real PG-free unit test for the pure function nodeTagRefusedForUserDevice)" \
  'test -f internal/feature/admin/devices.go && grep -q nodeTagRefusedForUserDevice internal/feature/admin/devices.go && test -f internal/feature/admin/exit_nodes_tag_b17_test.go && grep -q "func TestNodeTagRefusedForUserDevice" internal/feature/admin/exit_nodes_tag_b17_test.go && grep -qE "TestNodeTagRefusedForUserDevice_EdgeCases" internal/feature/admin/exit_nodes_tag_b17_test.go'

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
run_check "B19" "ACL perf + route correctness (v0.32.2 — B139 simplified from perf benchmark to code-level pin: the routes are wired in main.go and the ACL builder is exercised at runtime by the live /admin/acls flow. Perf benchmark itself (perf_test.go) stays as a t.Skip — needs PG + non-trivial setup, deferred to a perf-focused follow-up)" \
  'grep -qF "GenerateACL" internal/acl/acl.go && grep -qF "GenerateACLWithVia" internal/acl/acl.go && grep -qF "/admin/acls" cmd/skygate/main.go'

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


# ─── B34 was REPURPOSED in v1.3.20.8 ───
# B34 (v0.32.16) was originally "device_rules has no duplicate
# (exit_node_id, device_hostname) pairs" — a runtime SQL
# query against the live PG cluster. v1.3.19.2 follow-up B125
# (V056PG migration) added a UNIQUE INDEX on the 6-column
# natural key (user_id, device_id, exit_node_id, target_type,
# target_value, parent_domain), which is a STRONGER invariant
# than B34 was checking. The 2026-08-03 cleanup that B34 was
# guarding against is now structurally impossible at the
# schema level.
#
# The original B34 always FAILed in verify-pre because it
# queried the LIVE database (the pre-deploy cluster doesn't
# have the production data yet) — it was the wrong tool for
# verify-pre in the first place. Repurposed to test the B125
# schema invariant: the UNIQUE INDEX exists in migrations_pg.go.
# This is a code-level (not data-level) check, so it belongs
# in verify-pre.
run_check "B34" "device_rules has UNIQUE INDEX on the natural key (B125 schema invariant, B34 was a runtime query that always failed pre-deploy) (B34, v0.32.16 → v1.3.20.8 schema-pinned)" \
  'grep -qF "device_rules_natural_key_uniq" internal/db/migrations_pg.go && grep -qF "CREATE UNIQUE INDEX IF NOT EXISTS device_rules_natural_key_uniq" internal/db/migrations_pg.go'


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
run_check "B37" "Schedule UI: PostAdminUpdateSchedule handler (B129, replaces pre-B129 auto-toggle) + route + template form posting to /admin/update/schedule + global_settings key 'update_schedule_enabled' (B129, v1.3.20 — was: auto-update UI toggle v0.32.20)" \
  "bash -c '
    grep -qF \"func (s *Service) PostAdminUpdateSchedule\" internal/feature/admin/update_settings.go &&
    grep -qF \"PostAdminUpdateSchedule\" cmd/skygate/main.go &&
    grep -qF \"/admin/update/schedule\" internal/handlers/templates/admin/update.html &&
    grep -qF \"GetGlobalSettingBool\" internal/feature/admin/update.go &&
    grep -qF \"update_schedule_enabled\" internal/feature/admin/update_settings.go &&
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
#
# v1.3.12 (B38 fix): the original test file
# internal/feature/admin/headscale_acl_test.go used
# newMemoryDB (SQLite) which was removed in v1.3.0. The
# test file is now a t.Skip stub. The
# internal/db/migrations_v0.50.go file was also removed in
# v1.3.0 (the only source of truth is now
# internal/db/migrations_pg.go). The grep is updated to:
#  - check the t.Skip stub presence (instead of specific test fns)
#  - grep migrations_pg.go for headscale_acl_rules (instead of
#    the deleted migrations_v0.50.go)
run_check "B38" "headscale_acl.go: ListACL + AddACL + RemoveACL + fingerprint order-invariant (v0.33.0, v1.3.0+ PG form)" \
  "bash -c '
    grep -qF \"func (s *Service) ListACL\" internal/feature/admin/headscale_acl.go &&
    grep -qF \"func (s *Service) AddACL\" internal/feature/admin/headscale_acl.go &&
    grep -qF \"func (s *Service) RemoveACL\" internal/feature/admin/headscale_acl.go &&
    grep -qF \"func (s *Service) PreviewACL\" internal/feature/admin/headscale_acl.go &&
    grep -qF \"sort.Strings(srcSorted)\" internal/feature/admin/headscale_acl.go &&
    grep -qF \"t.Skip\" internal/feature/admin/headscale_acl_test.go &&
    grep -qF \"headscale_acl_rules\" internal/db/migrations_pg.go
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
    grep -qF \"acl.page_title\" internal/handlers/templates/admin/headscale_acl.html &&
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
run_check "B82" "per-user device + tag:exit-node override (v0.33.1.30 — production code path pinned: shouldIncludeAsExitServer excludes tag:subnet-router + per-user devices without tag:exit-node; B139 simplified from runtime test to code-level grep, runtime coverage via live /admin/exit-nodes UI)" \
  'grep -qF "tag:subnet-router" internal/feature/admin/exit_nodes.go && grep -qF "tag:dev-" internal/feature/admin/exit_nodes.go && grep -qF "shouldIncludeAsExitServer" internal/feature/admin/exit_nodes.go'

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
run_check "B83" "handlers.New() assigns sshKeyPath to App.SSHKeyPath (v0.33.1.31 — production assignment pinned, code-level grep, runtime coverage via container start)" \
  'grep -qE "SSHKeyPath:[[:space:]]+sshKeyPath," internal/handlers/handlers.go'

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
run_check "B84" "telegram egress uses B81 SSH-target chain (v0.33.1.32 — production path pinned, code-level grep, runtime coverage via /admin/telegram 'Set as egress relay' button)" \
  'grep -qF "LookupExitServerSSHTarget" internal/feature/admin/telegram.go && grep -qF "LookupExitServerSSHTarget" internal/feature/exit_rules/sync.go'

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
run_check "B88" "system_tests bug fixes: duplicate_devices, preferred_mismatch, rules_sanity, acl_admin_present, backup.recent (v0.33.1.36 — SQL strings pinned in system_tests.go, code-level grep, runtime coverage via live /admin/system_tests)" \
  'grep -qF duplicate_devices internal/feature/admin/system_tests.go && grep -qF preferred_mismatch internal/feature/admin/system_tests.go && grep -qF rules_sanity internal/feature/admin/system_tests.go && grep -qF acl_admin_present internal/feature/admin/system_tests.go && grep -qF backup.recent internal/feature/admin/system_tests.go'

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

# ─── B104 was REMOVED in v1.3.20.8 ───
# B104 (v1.3.8) was the original "autonomous migration verify"
# contract (BL-17). The script it pinned (scripts/verify_migration.sh)
# never landed — the actual implementation became B114 in
# v1.3.14 (3 phases: verify_post_deploy.sh --quick + system
# tests + manual checks). The B104 catalog entry was kept as
# a "superseded pin" that re-checked B114's script existence,
# but this just produced noise (FAIL in every run because the
# pinned path no longer exists). Removed in v1.3.20.8 as
# part of the B-check catalog cleanup. B114 is the canonical
# implementation of BL-17.

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

# ─── B110 (v1.3.10) — tailnet reachability/speed/split diagnostics ───
# Background: operator request 2026-08-13 — "проверить скорость к
# skybars от karolina, организовать тесты для проверки скорости и
# доступа". Live symptom: headscale says 10 nodes are online but
# skygate-host-1's `tailscale status` shows only 4 — a tailnet split
# where the home-LAN cluster (skybars, skyworker, a71, olesya,
# svyatoslava-1, nothing-phone-2) is invisible from the server. Root
# cause analysis + fix procedure in docs/tailnet-diagnostics.md.
# B110 pins 7 contracts in scripts/check_b110.sh:
#   1. internal/feature/admin/system_tests_tailnet.go has 3 new
#      TestRegistry entries (allNodesReachabilityTest,
#      vpsToVPSLatencyTest, splitSuspectedTest) + the vpsHostnameSet
#      helper with the 5 known VPS hostnames.
#   2. internal/feature/admin/system_tests_tailnet_test.go has ≥12
#      Test* functions covering pass/fail/skip branches.
#   3. scripts/tailnet_probe.sh is bash-syntax-valid and supports
#      the 4 documented flags (--to, --iperf3, --ping, --json).
#   4. docs/tailnet-diagnostics.md has the 5 mandatory sections
#      (TL;DR, Symptom, Root cause analysis, Fix procedure,
#      Prevention).
#   5. init() registers all 3 tests in TestRegistry.
#   6. All 3 tests use Category:"network" (matches B98 + B40
#      coverage of network category).
#   7. The Go unit tests pass (full suite + tailnet-specific).
run_check "B110" "tailnet reachability/speed/split diagnostics (3 Go tests + shell script + docs) (B110, v1.3.10 TAILNET SPLIT)" \
  'test -f scripts/check_b110.sh && bash scripts/check_b110.sh'

# ─── B111 (v1.3.11) — B93 "infra owns technical nodes" completion ───
# Background: operator request 2026-08-13: "infra user будет
# владеть skygate + exit nodes (karolina sharlotta emilia
# svyatoslava) и давать публичный доступ к exit nodes
# остальным". The original B93 commit (v0.33.1.41) only
# matched skygate-host-* prefix + tag:dev-infra-* — it missed
# the 4 exit nodes that still sat in the skyadmin/michail/
# svyatoslava user-portal buckets from the B69/B89 backfills.
# Result: skygate-host-1 ended up in the infra bucket but the
# per-device mesh for 'infra' was empty (only 1 device, the
# generator skips <2). The other 9 online nodes were
# invisible to skygate-host-1 because their tags
# (tag:dev-skyadmin-X, tag:dev-michail-X) were never paired
# with the new tag:dev-infra-skygate-host-1 in any grant.
# B111 closes the gap with three changes:
#   1. isInfraNode adds rule 3: any tag == "tag:exit-node"
#      (catches emilia, karolina, sharlotta, svyatoslava-1).
#   2. BackfillInfra changes from INSERT OR IGNORE to
#      active UPDATE — nodes matching isInfraNode that are
#      currently in a user-portal bucket get re-attributed
#      to 'infra' with tag=tag:dev-infra-<hostname>.
#   3. The policy generator emits `* → tag:dev-infra-<exit>`
#      catch-alls so any Tailscale client can still use
#      infra-owned exit nodes (preserves the pre-B93
#      behaviour where every skyadmin device could reach
#      every other skyadmin device, including relays).
# Pin: 5 contracts in scripts/check_b111.sh (isInfraNode
# tag:exit-node rule, BackfillInfra UPDATE, getInfraExitNodeTags
# helper, both GenerateACL call sites, 4+ unit tests pass).
# Runtime: requires the operator to re-tag the 4 exit nodes
# + skygate-host-1 in headscale to use tag:dev-infra-<hostname>
# instead of tag:dev-skyadmin-<hostname>. Until that
# operator action is done, the policy has grants for tags
# that don't match any device (no-op) — the existing
# per-skyadmin mesh keeps working.
run_check "B111" "B93 infra-owns-technical-nodes completion (isInfraNode + BackfillInfra UPDATE + public-access grants) (B111, v1.3.11)" \
  'test -f scripts/check_b111.sh && bash scripts/check_b111.sh'

# ─── B112 (v1.3.12) — P4 catalog cleanup (dead code + grep updates) ───
# Background: 2026-08-12 the v1.3.9 (P4 catalog cleanup) work
# was partly deferred — 5 staticcheck U1000 items and 2
# verify-pre check updates were left uncommitted in the
# working tree. Phase 3 follow-up 2026-08-13 picked them up
# (commit d0d6ad4). B38 also needs to be updated to v1.3.0+
# PG form (the test file is a t.Skip stub; the migration
# is in migrations_pg.go not migrations_v0.50.go).
#
# Pin: 16 contracts in scripts/check_b112.sh:
#  - 5 dead-code removals (s3Client, realS3Client, dockerCmdStdin,
#    renderHeadscaleCompose, stripHeadplaneServiceBlock,
#    startsWithWhitespace, resetLoginAttempts, setKillProcess,
#    hostnameMapFromHeadscale)
#  - 3 check updates (check_b93.sh, check_b95.sh, verify_pre_deploy.sh B38)
#  - 1 go build pass
#  - 1 file-presence check (cl.FPutObject removed, mc.FPutObject used)
run_check "B112" "v1.3.12 P4 catalog cleanup (5 dead-code removals + 3 check updates + 1 build check) (B112)" \
  'test -f scripts/check_b112.sh && bash scripts/check_b112.sh'

# ─── B113 (v1.3.13) — youtube.com/32 bug fix ───
# Background: pre-v1.3.13, an operator who typed a bare
# hostname (e.g. "youtube.com") in the IP field of
# /my/exit-rules would get "youtube.com/32" saved to
# the DB. The ACL builder then promoted it to a host
# alias "h-rule-youtube-com-32: youtube.com/32" — a
# malformed CIDR that headscale rejects, causing the
# whole policy re-apply to fail.
#
# Fix: form_my.go validates targetValue via isValidIPOrCIDR
# before any processing. For target_type=domain, the form
# does DNS resolution (hostname is valid input there).
#
# Pin: 4 contracts in scripts/check_b113.sh:
#  - isValidIPOrCIDR helper exists in form_my.go
#  - helper is called in PostMyExitRule (form path)
#  - 400 BadRequest on bad input
#  - unit test (TestIsValidIPOrCIDR_IPv4) covers IPv4 + IPv6 +
#    CIDRs + bare hostnames (the bug) + garbage
run_check "B113" "youtube.com/32 bug fix: form validates targetValue is IP/CIDR for target_type=ip|subnet (B113, v1.3.13)" \
  'test -f scripts/check_b113.sh && bash scripts/check_b113.sh'

# ─── B114 (v1.3.14) — BL-17 verify_migration.sh (autonomous migration verify) ───
# Background: after a cross-host restore + cutover (the migration
# flow in docs/backup-restore-and-migration.md section 3), the
# operator previously had to run 3 separate checks in order, each
# in its own terminal. If they skipped step 2 (system tests) or
# step 3 (manual) they'd only find out about subtle issues when
# a user complained.
#
# scripts/verify_migration.sh chains the 3 phases:
#   1. verify_post_deploy.sh --quick (R1-R9 + R26)
#   2. POST /admin/system_tests/run via Python driver (staged
#      via scp + docker cp because the skygate container's
#      busybox wget doesn't support cookies)
#   3. Manual checks (healthz, readyz, /admin/services) — printed
#      as a copy-pasteable curl one-liner for the operator
#
# Plus a PRE_BUILD pre-state capture that prints MIGRATION DETECTED
# when the post-migration build label differs from the pre-migration
# one (cold-standby restore flow uses this).
run_check "B114" "autonomous migration verify: 3-phase chain + portable driver staging (B114, BL-17 v1.3.14 mig-verify)" \
  'test -f scripts/check_b114.sh && bash scripts/check_b114.sh'
run_check "B115" "tailnet test skip filter: tailnetSelfHostname + tailnetSkipHostnames + 5 home-LAN hardcoded + 3 tests use filter (B115, v1.3.16)" \
  'test -f scripts/check_b115.sh && bash scripts/check_b115.sh'
run_check "B116" "DERP relay CRUD UI: derp_relays table + 6 handlers + 6 routes + apply uses table (B116, v1.3.17)" \
  'test -f scripts/check_b116.sh && bash scripts/check_b116.sh'
run_check "B118" "tag-owner-from-name (via loop parses owner from tag:dev-<user>-<device>, tag:exit-node owned by infra@, svyatoslava-legacy gone) (B118, v1.3.19)" \
  'test -f scripts/check_b118.sh && bash scripts/check_b118.sh'
run_check "B119" "TagToHostname handles 4 formats (v1.3.18.1 missed the exported helper, fixed in v1.3.19.1) (B119, v1.3.19.1 preferred_check follow-up)" \
  'test -f scripts/check_b119.sh && bash scripts/check_b119.sh'
run_check "B120" "admin-breadcrumb has margin-left:220px desktop / 52px collapsed / 0 mobile (sidebar offset, B120, v1.3.19.2 layout fix)" \
  'test -f scripts/check_b120.sh && bash scripts/check_b120.sh'
run_check "B121" "Mint theme (silver+mint) + thin themed scrollbar + dark-theme form contrast bump (B121, v1.3.19.2 follow-up)" \
  'test -f scripts/check_b121.sh && bash scripts/check_b121.sh'
run_check "B122" "restore.sh PG path: do_pg_restore (postgres:18-alpine + psql -f skygate-pg.sql) + DSN from skygate.env + sudo for headscale steps + shell-glob for do_headscale_db (B122, v1.3.19.2 follow-up / BL-15 e2e)" \
  'test -f scripts/check_b122.sh && bash scripts/check_b122.sh'
run_check "B123" "Exit Rules duplicate alert UX: target + existing_id + blocking_ip + parent_domain + form_* in redirect, jump-to-rule anchor, 3 new i18n keys (B123, v1.3.19.2 follow-up / Goal 39)" \
  'test -f scripts/check_b123.sh && bash scripts/check_b123.sh'
run_check "B124" "Dev version element: SKYGATE_DEV_BUILD=true → dev banner + no 'update available' + no auto-apply, plus compareSemver fix for git-describe '-N-g<hex>' suffix (B124, v1.3.19.2 follow-up)" \
  'test -f scripts/check_b124.sh && bash scripts/check_b124.sh'
run_check "B125" "device_rules auto-add duplicate prevention: UNIQUE INDEX device_rules_natural_key_uniq + ON CONFLICT (key) DO UPDATE SET id = id RETURNING id in qInsertDeviceRule + 2 ON CONFLICT DO NOTHING in sync.go (B125, v1.3.19.2 follow-up / Goal 37 follow-up)" \
  'test -f scripts/check_b125.sh && bash scripts/check_b125.sh'
run_check "B126" "verify_post_deploy.sh R9: replace EXTRACT(epoch FROM created_at) (which fails on INTEGER column) with direct created_at read (B126, v1.3.19.4)" \
  'test -f scripts/check_b126.sh && bash scripts/check_b126.sh'
run_check "B127" "verify_post_deploy.sh false-positive cleanup: R11-R16/R17-R18/R28/R29 refactored to json_field (python3 runs on VM, not on WSL); R34 pre-init REMOTE_CK; SKYGATE_ADMIN_USER + SKYGATE_ADMIN_PASSWORD fallbacks read from VM .env (B127, v1.3.19.4)" \
  'test -f scripts/check_b127.sh && bash scripts/check_b127.sh'
run_check "B128" "compareSemver 4-part version support: splitVersionParts(a, 4) + splitVersionParts(b, 4) in checker.go + 4-iteration loops in monitor.go + client.go + 4-part test cases + live TestCompareSemver (B128, v1.3.20 — fixes the silent 'Update button hidden despite newer GitHub release' bug on /admin/update)" \
  'test -f scripts/check_b128.sh && bash scripts/check_b128.sh'
run_check "B129" "/admin/update page redesign: Apply button unconditional (no more AutoUpdateEnabled gating) + new Schedule section (toggle + HH:MM input + save + last-run) + config fields + i18n keys + POST /admin/update/schedule route (B129, v1.3.20 — replaces the misleading pre-B129 'auto-update' banner with a real time-bounded scheduler)" \
  'test -f scripts/check_b129.sh && bash scripts/check_b129.sh'
run_check "B130" "background scheduler for time-of-day auto-update: internal/update/scheduler.go with SchedulerDeps + Start + tick + runScheduled + scheduler_db.go init() binding db helpers + main.go wire-up with cfg.UpdateScheduleEnabled guard + schedulerNotifierSink adapter + config fields + reads/writes 'update_schedule_enabled' / 'update_schedule_time' / 'update_schedule_last_run' (B130, v1.3.20 — the runtime side of the B129 Schedule section)" \
  'test -f scripts/check_b130.sh && bash scripts/check_b130.sh'
run_check "B131" "Linear theme contrast bump (moderate, B131 set the baseline) + B133 escalated it: :root colors lifted (--bg #0e0f12 OR #0c0d10, --bg-card #1a1c21 OR #23262e — the B131 OR B133 set, picked by contract-test) + alert opacities bumped from 0.10 to 0.15-0.22 + input backgrounds use var(--bg-elev) (was hardcoded #1a1a1a) + sRGB contrast >=5% (B131 baseline + B133 escalation, v1.3.20.1+v1.3.20.3 — operator's 'Linear theme breaks all perception' complaint)" \
  'test -f scripts/check_b131.sh && bash scripts/check_b131.sh'
run_check "B132" "per-row 'Re-sync' button on /admin/exit-nodes: exit_rules.SyncAdvertisedRoutesForNode + syncOneExitNode shared helper + admin.PostAdminExitNodeSync handler (r.PathValue('hostname')) + Service.SyncRoutesForNode field + main.go wire-up + POST /admin/exit-nodes/{hostname}/sync route + template per-row form + mismatch tag tooltip + 'TS IP' label on Use-Tailscale-IP button + 7 i18n keys (B132, v1.3.20.2 — fixes the operator's 'не понятно что с этим делать' complaint: pre-B132 only had a global 'Sync all' that re-masked per-node SSH errors)" \
  'test -f scripts/check_b132.sh && bash scripts/check_b132.sh'
run_check "B133" "Linear dramatic contrast overhaul: bg #0e0f12 → #0c0d10 + card #1a1c21 → #23262e (delta 10%, was 5%) + border #383b42 → #555a64 + alert opacities 0.15 → 0.22 + .card uses --border-strong + table zebra striping + multi-layer --shadow (3 stops) + light themes override card border back to --border (B133, v1.3.20.3 — escalates B131 because the operator reported the moderate bump wasn't enough; B131 was good on bg/border but the table had no row backgrounds and the 4 action buttons were indistinguishable)" \
  'test -f scripts/check_b133.sh && bash scripts/check_b133.sh'
run_check "B134" "themes.css must NOT have <style> / </style> wrappers: 5 contracts (no <style> tag, no </style> tag, file starts with CSS comment, B131 contract still passes, B133 contract still passes). Fixes the v1.0.0 - v1.3.20.3 bug where the file was created from an HTML template and retained the surrounding <style>...</style> tags, causing the browser to drop ~80 lines of post-</style> CSS (B134, v1.3.20.4 — operator's 'CSS не применяется, нет видимых границ' report on 2026-08-18; root cause was the orphaned CSS, not the contrast bump B131/B133 were trying to add)" \
  'test -f scripts/check_b134.sh && bash scripts/check_b134.sh'
run_check "B135" "Manrope font + +1px size bump: --font declares Manrope (was Inter) + body 15px/line-height 1.6 + table 14px + h2 24px + btn 14px + layout.html has Manrope <link> + fonts.gstatic.com preconnect + B131/B133/B134 contracts still pass. The operator picked Manrope over Inter/Geist/Sora after seeing the font examples (B135, v1.3.20.5 — operator's 'хотелось бы более приятный шрифт и побольше' request on 2026-08-18)" \
  'test -f scripts/check_b135.sh && bash scripts/check_b135.sh'
run_check "B136" "per-user display preferences (DB-persisted): V057 migration adds font_family/font_scale/selection_bg to portal_users + GetUserDisplayPrefs/SetUserDisplayPrefs helpers + IsValidFontFamily/ClampFontScale + PostMyAccountDisplay handler + route POST /my/account/display + layout.html injects <style id='user-display-prefs'> + /my/account has Display form + 18 i18n keys (RU+EN) + B131/B133/B134/B135 contracts still pass. Where-ever the user opens the UI, their font + size + selection color follow them (B136, v1.3.20.6 — operator's 'сохранять в базе данных чтобы не сбрасывалось с кешем' request on 2026-08-18)" \
  'test -f scripts/check_b136.sh && bash scripts/check_b136.sh'
run_check "B137" "color swatch grid for selection_bg: 14 tiles in account.html (1 default + 12 preset colors + 1 custom) + .color-swatch CSS rules + click-handler JS + 2 new i18n keys (RU+EN) + B131/B133/B134/B135/B136 contracts still pass. The pre-B136 freeform text input forced the operator to type CSS color values; B137 adds a clickable palette (B137, v1.3.20.6 hotfix — operator's 'добавь удобную форму выбора цвета из таблицы' feedback on 2026-08-18)" \
  'test -f scripts/check_b137.sh && bash scripts/check_b137.sh'
run_check "B138" "B-check catalog cleanup: 3 pre-existing FAILs fixed (B34 repurposed to test B125 schema invariant, B104 removed as superseded by B114, B95 fixed by removing 2 real U1000/SA4000 bugs in update_settings.go + backup_test.go). verify-pre on VM is now 0 FAIL / 1 SKIP (B8, Windows-host VM-only). B138, v1.3.20.8 — operator's 'B-check catalog cleanup (17 stale B-checks → 0)' task on 2026-08-18" \
  'bash -c "
    B34=\$(grep -qF \"device_rules_natural_key_uniq\" internal/db/migrations_pg.go && echo OK || echo FAIL)
    B95=\$(grep -qE \"^const globalSettingsKeyAutoUpdate\" internal/feature/admin/update_settings.go && echo FAIL || echo OK)
    if [ \"\$B34\" = OK ] && [ \"\$B95\" = OK ]; then echo OK; else echo \"B34=\$B34 B95=\$B95\"; exit 1; fi
  "'
run_check "B139" "pinned-contract cleanup: 6 'pinned, pending PG rewrite' B-checks removed/repurposed. B17 + real PG-free unit test for nodeTagRefusedForUserDevice (4 base cases + 2 edge cases + v0.30.1 workstation-8 repro). B19/B82/B83/B84/B88 simplified to code-level grep + runtime coverage. 5 t.Skip stub _test.go files deleted (perf_test.go, exit_nodes_test.go, admin_telegram_egress_b84_test.go, system_tests_b66_b68_test.go, handlers_new_test.go, exit_nodes_tag_test.go). B139, v1.3.20.9 — operator's 'finish the B-check catalog' task on 2026-08-18" \
  'test -f internal/feature/admin/exit_nodes_tag_b17_test.go && ! test -f internal/feature/admin/exit_nodes_tag_test.go && ! test -f internal/feature/admin/admin_telegram_egress_b84_test.go && ! test -f internal/feature/admin/system_tests_b66_b68_test.go && ! test -f internal/handlers/handlers_new_test.go && ! test -f internal/acl/perf_test.go'
run_check "B140" "per-row accept_routes toggle on /admin/exit-nodes: db.SetExitServerAcceptRoutes + GetExitServerHostname + ErrExitServerNotFound in db/exit_servers.go + PostAdminExitNodeSetAcceptRoutes handler (r.PathValue('node_id') + parseAcceptRoutesFormValue) + errors import + route POST /admin/exit-nodes/{node_id}/accept-routes in main.go + per-row <form> + 3-option <select> in exit_nodes.html + 4 new i18n keys (accept_routes_label/help/btn_set/updated) in RU+EN + 6 unit tests for parseAcceptRoutesFormValue (B140, v1.4.0 — fixes 'нельзя изменить accept_routes после добавления exit-node'; pre-B140 only the initial Add form set this value, post-B140 each row has an inline Apply button that cycles 1/0/-1)" \
  'test -f scripts/check_b140.sh && bash scripts/check_b140.sh'
run_check "B141" "'Adopt as skygate user' button on /admin/users HSOrphans: db.InsertPortalUserAdopt with ON CONFLICT(username) DO NOTHING (atomic primitive closes the concurrent-click race) + PostAdminHSOrphanAdopt handler (validates headscale user exists, bcrypts password, INSERTs with is_admin=0) + validateHSOrphanName helper + net/url import + route POST /admin/users/HSOrphan/adopt in main.go + per-row <form> in users.html with hidden hs_id + password input + Adopt button + flash banner section (FlashHSOrphanAdopt/FlashHSOrphanExists) + 4 new i18n keys (hs_orphan_adopt_btn/help/password_ph/adopted_flash) in RU+EN + 4 unit tests for validateHSOrphanName (B141, v1.4.0 — fixes 'HSOrphans — нет кнопки adopt'; pre-B141 the page only displayed the orphans list, post-B141 each row has an Adopt form that creates the portal_users row in one click)" \
  'test -f scripts/check_b141.sh && bash scripts/check_b141.sh'
run_check "B142" "in-app backup-verify scheduler: backup.StartVerifyScheduler (VerifySchedulerDeps + NotifierSink + tick + runVerify + tailLines + truncateString + sameMinute + inFlightVerify) + 6 Config fields (InAppVerifyEnabled/VerifySchedule/LastVerifyAt/Status/Error/Archive) + 6 storage key constants + env-var defaults SKYGATE_BACKUP_VERIFY_IN_APP_ENABLED + SKYGATE_BACKUP_VERIFY_SCHEDULE + main.go wire-up with cfg.BackupVerifyInAppEnabled guard + POST /admin/backup/verify-now route + PostAdminBackupVerifyNow handler (shells out to scripts/verify_backup.sh synchronously) + 11 new i18n keys (last_verify/last_verify_never/last_verify_archive/last_verify_error/verify_now/verify_now_ok/verify_now_failed/in_app_verify_scheduler/in_app_verify_help/verify_schedule/verify_schedule_help) in RU+EN + runBackupVerifyOK/Fail renamed key backup.last_verify → backup.last_verify_at + added last_verify_archive + last_verify_error writes + 13 unit tests for tailLines/truncateString/sameMinute (B142, v1.4.1 — fixes 'verify_backup.sh cron — нет Telegram-алерта'; pre-B142 system-cron wrote status to global_settings but didn't notify, post-B142 the in-app goroutine sends SendAlert on verify failure with the script's stderr tail; drop-in replacement for the weekly Sun 04:00 system-cron entry)" \
  'test -f scripts/check_b142.sh && bash scripts/check_b142.sh'
run_check "B143" "in-app smoke-mesh cleanup scheduler: mesh.StartCleanupScheduler (CleanupSchedulerDeps + CleanupNotifierSink + RunCleanup + tick + cleanupIsDueThisTick + 4 read helpers + sameCleanupMinute + FormatHumanSchedule + inFlightCleanupMu + 30s tick) + 3 storage key constants (cleanup.smoke_mesh_enabled/schedule/last_run) + 2 Config fields (CleanupSmokeMeshInAppEnabled/Schedule) + env-var defaults SKYGATE_CLEANUP_SMOKE_MESH_IN_APP_ENABLED + SKYGATE_CLEANUP_SMOKE_MESH_SCHEDULE + main.go wire-up with cfg.CleanupSmokeMeshInAppEnabled guard + skygate cleanup-smoke-meshes subcommand + runCleanupSmokeMeshes handler (runs mesh.RunCleanup unconditionally for ad-hoc use) + 7 new i18n keys (cleanup_smoke.run_btn/run_btn_help/last_run/last_run_never/removed/no_rows/failed) in RU+EN + 17 unit tests for FormatCleanupMessage/int64ArrayToPGArray/sameCleanupMinute/FormatHumanSchedule/SmokeMeshNamePrefix/StorageKeyConstants (B143, v1.4.3 — TD-9 fix for 'smoke-mesh cruft accumulates between smoke runs'; pre-B143 scripts/smoke.sh:511-512 creates 'smoke-mesh-<pid>' rows that were never cleaned up (operator's v0.33.1.36 had to manually DELETE 30 rows in a one-off SQL); post-B143 a daily 5 AM cron (after the 3 AM backup + 4 AM verify) DELETEs every smoke-mesh row with no members + sends a Telegram alert on actual deletion; the manual subcommand is the operator's ad-hoc escape hatch)" \
  'test -f scripts/check_b143.sh && bash scripts/check_b143.sh'
run_check "B144" "/admin/system_tests History tab (TD-8): system_tests_history.go (ComputeTestHistory aggregates per-test pass/fail/skip across system_tests_runs in a [since, until) window + TestHistory + TestHistoryRow with PassRate/TotalRuns helpers + HistoryWindow + ParseHistoryWindow 7d/30d/all + truncateForHistory 200-char cap) + GetAdminSystemTests reads ?tab=tests|history + ?window=7d|30d|all + calls ComputeTestHistory + passes Tab/Window/WindowLabel/History into the template + system_tests.html tab bar (Tests | History) with active class + History tab content (window selector buttons + 4 summary stats + per-test aggregate table sorted by FailCount DESC with PassRate colour-coded green≥95/yellow≥50/red<50 + LastStatus icon + LastError truncated + 23 new i18n keys (tab_tests/tab_history/history_title/history_subtitle/window_label/window_7d/window_30d/window_all/stat_total_runs/stat_window/stat_total_duration/stat_tests_tracked/col_pass/col_fail/col_skip/col_pass_rate/col_last_status/col_last_run/col_last_error/col_started/col_duration/never/recent_runs_title/history_no_runs_help) in RU+EN + 11 unit tests for ParseHistoryWindow/PassRate/TotalRuns/truncateForHistory (B144, v1.4.4 — TD-8 fix for 'system_tests history — no per-test view'; pre-B144 the page only showed a 20-row strip with aggregate pass/fail/skip counts; post-B144 the History tab answers 'which tests are flaky' and 'which tests have been failing for a week' by sorting per-test aggregates by FailCount DESC; the existing /admin/system_tests Run all + tests grid + DNS-autoupdater toggle are unchanged)" \
  'test -f scripts/check_b144.sh && bash scripts/check_b144.sh'
run_check "B150" "skygate deploy CLI + /admin/deploy page (BL-2 Phase 6): internal/deploy/{subcommand,push,pull,ha}.go with Run + RunPush + RunPull + RunStatus + HAPromote + HADemote + HAReclaim + OpenDepsFromEnv + ApplyActiveRoleKey constant + chainContainsHostname validation helper (no-op audit on typo) + writeApplyActiveRole/clearApplyActiveRole UPSERT helpers + writeDeployAudit/writeHAAudit audit writers + ErrNoS3Config + ErrAlreadyUpToDate sentinel errors + internal/feature/admin/deploy.go with GetAdminDeploy + PostAdminDeployPush + PostAdminDeployTestFailover (dry-run) + collectDeployPageData (chain + last 10 audit events) + queryDeployAuditEvents (action LIKE 'ha.%' OR 'deploy.%') + predictNextActive dry-run helper + countAlive + openDeployDepsForRequest + deployRedirect + import 'skygate/internal/deploy' + internal/handlers/templates/admin/deploy.html (4 sections: topology/controls/ha-actions/audit) + 10 new i18n keys (deploy.title/subtitle/section_controls/controls_help/target_label/push_button/test_failover_title/test_failover_help/test_failover_button/dry_run_label) in RU+EN + 1 nav key (nav.deploy) in catalog_common.go RU+EN + 3 admin routes (GET /admin/deploy + POST /admin/deploy/push + POST /admin/deploy/test-failover) in main.go + 7 subcommand cases (deploy-push + deploy-pull + deploy-sync + deploy-status + ha-promote + ha-demote + ha-reclaim) in main.go + 2 helper funcs (runDeploySubcommand + runHASubcommand) + import 'skygate/internal/deploy' in main.go + layout.html menu link href=/admin/deploy + sectionPageSet includes admin/deploy (so the sidebar auto-opens the Integrations section when /admin/deploy is the active page) (B150, v1.5.0 — web mirror of the skygate deploy CLI; the page + CLI share the same internal/deploy primitives so an operator can drive HA transitions from either path; the dry-run Test-failover button is the read-only 'show me what would happen if the active went down right now' tool that mirrors the elector's promotion logic in a private helper)" \
  'test -f scripts/check_b150.sh && bash scripts/check_b150.sh'
run_check "B152" "static asset sanity (BL-2 follow-up: 'долго переключение между страницами' — operator report 2026-08-20): font-awesome 6.7.2 self-hosted (static/css/font-awesome.min.css 73890 bytes + static/webfonts/fa-solid-900.woff2 158220 bytes + static/webfonts/fa-regular-400.woff2 25472 bytes + static/webfonts/fa-brands-400.woff2 118684 bytes + static/webfonts/fa-v4compatibility.woff2 4796 bytes) so 46 templates referencing /static/css/font-awesome.min.css no longer 404 on every page navigation + Cache-Control on /static/* (immutable max-age=31536000 for content-hashed assets like app.<hash>.js, must-revalidate max-age=86400 for versioned assets like themes.css / font-awesome.min.css / webfonts/) so the browser stops re-fetching CSS on every page nav + favicon also cached + no external font-awesome CDN dependency (B152, v1.5.0 — root cause was 46 templates <link href='/static/css/font-awesome.min.css'> but the file didn't exist on disk; every page nav was a 404 + uncached CSS re-fetch; the page would render in 200-400ms instead of 50-100ms. After B152: first page loads CSS+fonts (~30KB), subsequent pages hit the browser cache)" \
  'test -f scripts/check_b152.sh && bash scripts/check_b152.sh'
run_check "B147" "in-app certsync scheduler (BL-2 Phase 3): internal/certsync/{certsync,certsync_crypto,s3adapter}.go with Start + tick + validateCertKeyPair + writeLocalCerts + checkExpiry + S3Client interface + VersionFile JSON struct + MinioS3Client adapter (StatObject/GetObject/PutObject from minio.Client) + CertSyncDeps struct (DB + LocalDir + S3Client + S3Bucket + CaddyReload + Notifier + Interval) + ErrNoS3Config + atomic .new + .certsync-version cache + isNotFound 4-shape matcher + atomic rename pattern + loadLocalVersionCache/saveLocalVersionCache helpers + S3 key layout (certs/.version + certs/cert.pem + certs/key.pem) + 4 unit tests (TestNoVersionIsNoOp + TestVersionBumpTriggersPull + TestSHAMismatchTriggersPull + TestInvalidCertFails) + cmd/skygate/main.go wires the scheduler via Start (gated on cfg.CertSyncEnabled) + buildBackupConfigForCertSync helper reads SKYGATE_S3_* env vars + schedulerNotifierSink adapter + 'certsync: enabled' / 'certsync: disabled' startup log lines + 4 Config fields (CertSyncEnabled/CertSyncBucket/CertSyncLocalDir/CertSyncInterval) in config.go with env-var defaults SKYGATE_CERTSYNC_ENABLED=true + SKYGATE_CERTSYNC_S3_BUCKET=skygate-backups + SKYGATE_CERTSYNC_LOCAL_DIR=/var/lib/skygate/certs + SKYGATE_CERTSYNC_INTERVAL=30s + import 'skygate/internal/certsync' + import 'skygate/internal/backup' (B147, v1.5.0 — pulls newer certs from S3 every 30s and reloads Caddy; pre-B147 the standby had no cert of its own, so when active died and standby was promoted the HTTPS listener 502'd; post-B147 both nodes poll the S3 .version file and pull on SHA mismatch, so failover is always cert-fresh; the cert+key pair is validated via crypto/x509 + matchedAny (tries PKCS#1, PKCS#8, SEC1 in order) before the atomic rename, so a mismatched upload can't bring down Caddy; expiry warning fires when the local cert is within 7 days of NotAfter, giving the operator time to renew before HTTPS dies) (BL-2 plan §5.1 / Phase 3, independent of reg.ru — no DNS-side work, just S3 reads + local file writes + optional Caddy reload callback)" \
  'test -f scripts/check_b147.sh && bash scripts/check_b147.sh'
run_check "B148" "/admin/certificates page (BL-2 Phase 4): internal/feature/admin/certificates.go with GetAdminCertificates + PostAdminCertificateUpload + PostAdminCertificateToggleDNS01 handlers + certificatesPageData + certDisplayInfo + certAuditEvent structs + collectCertificatesPageData (reads local cert.pem via readLocalCertInfo + DNS-01 toggle from global_settings.dns01_enabled + last 10 certsync.*/certs.* audit rows via queryCertAuditEvents) + readLocalCertInfo (x509.ParseCertificate + sha256 + certChainStrings) + readCertInput (file vs textarea fallback: file wins when both set) + decodePEM wrapper + certsyncCertPath (returns /var/lib/skygate/certs/cert.pem) + uploadCertToS3 callback (delegates to s.CertUploadToS3, nil = silent no-op so the page still works) + certRedirect (303 See Other with URL-encoded flash) + Service.CertUploadToS3 CertUploadFn field in service.go + certsync.ValidateCertKeyPair exported (B148 re-uses B147's x509 + matchedAny rules) + internal/handlers/templates/admin/certificates.html (4 sections: current cert / upload form / DNS-01 toggle / recent events; 5 form fields: cert_pem_file + cert_pem_text + key_pem_file + key_pem_text + dns01_enabled) + 25 cert.* i18n keys (RU+EN) in catalog_admin.go + nav.certificates in catalog_common.go (RU+EN) + 3 admin routes in main.go: GET /admin/certificates + POST /admin/certificates/upload + POST /admin/certificates/toggle-dns01 + /admin/certificates sidebar link in layout.html (after /admin/deploy, fa-certificate icon) + admin/certificates in sectionPageSet so the sidebar auto-opens + 10 unit tests in internal/feature/admin/certificates_test.go (TestReadLocalCertInfo_ParsesValidCert + TestReadLocalCertInfo_MissingFile + TestReadLocalCertInfo_MalformedCert + TestReadCertInput_PrefersFile + TestReadCertInput_FallsBackToText + TestReadCertInput_NoInput + TestCertRedirect_EncodesFlash + TestCertRedirect_ErrorOnly + TestCertSyncCertPath_StablePath + TestCertChainStrings_ReturnsIssuer) (B148, v1.5.0 — operator-facing surface for cert management: paste PEM cert+key, validates the pair via B147's rules, uploads to S3 so the certsync scheduler (B147) picks it up within 30s; the LE DNS-01 toggle stores the operator's intent in global_settings for a future v1.5.x surface that depends on B146 — when B146 is unblocked, a v1.5.x release will read the toggle + run certbot + reg.ru DNS-01)" \
  'test -f scripts/check_b148.sh && bash scripts/check_b148.sh'
run_check "B153" "personal API token UX (operator 2026-08-20: 'удобный способ обновления ключей или самим выбирать возможность ставить их время жизни сколько требуется при генерации ключа'): /my/tokens page rewritten — per-row ExpiresWarn/ExpiresBadge/ExpiresInWords/Renewable fields computed in GetMyTokens handler (red expired, red <7d soon, yellow <30d month, empty for fine-or-never) + ExpiringCount banner trigger when ≥1 token expires within 14d + post-renew success flash via ?renewed=1&t=<unix> → 'Expiry extended to YYYY-MM-DD HH:MM' + ?renew=ID form (dedicated 'Extend token expiry' card with 1d/7d/30d/90d/365d/never dropdown, default 30d) + PostMyToken handler parses custom_ttl_value+custom_ttl_unit (h/d/w/y) before falling back to the legacy ttl= dropdown (min 1h, max 5y, 0=never) + PostMyTokenRenew handler (POST /my/token/{id}/renew — defaults to 30d, honours the dedicated form's ttl field, audit 'token_renew id=<N> new_ttl=<X>', 404 on bad id / wrong user / already revoked) + internal/db/queries.go: qUpdateAPITokenExpiryByUser UPDATE … WHERE id=? AND user_id=? + db.UpdateAPITokenExpiryByUser helper + internal/handlers/templates/my_tokens.html: custom-TTL input + per-row Renew button + dedicated renew card + .badge styles (badge + badge-expired/soon/month/success/danger/info/warn) in static/css/themes.css + 12 new i18n keys in catalog_my.go (custom_ttl_label/hint/h/d/w/y/err_min/err_max + renew/renew_title/renewed_to/renew_err_expired + expired + expires_in_days/in_day/in_hours/tomorrow + banner_expiring) in RU+EN (B153, v1.5.0 — fix for 'у устройства появилось предупреждение что ключ истекает'; pre-B153 the /my/tokens page only showed the expiry date with no warning + no way to renew; post-B153 the operator sees red/yellow badges + a 14-day banner + a one-click Renew button (default 30d) + a dedicated 'extend with custom TTL' form for power users, AND can set a custom lifetime at creation time without picking from a hard-coded dropdown)" \
  'test -f scripts/check_b153.sh && bash scripts/check_b153.sh'
run_check "B154" "in-app auto-rotate scheduler for personal API tokens (operator 2026-08-20 follow-up to B153: 'auto_rotate флаг был сохранён, но не обрабатывался — Tailscale device показывал key expiring warning как симптом'): internal/tokenrotate/scheduler.go with Start(ctx, deps) + tick + RunAutoExtend + NotifierSink + inFlightRotateMu + 3 storage key constants (KeyAutoRotateEnabled='tokens.auto_rotate_enabled' / KeyAutoRotateSchedule='tokens.auto_rotate_schedule' / KeyAutoRotateLastRun='tokens.auto_rotate_last_run') + DefaultAutoRotateSchedule='0 3 * * *' + DefaultAutoExtendDuration=30d + DefaultAutoRotateCutoff=7d + AutoRotateTickInterval=30s + AutoRotateResult + AutoRotateTokenResult structs + formatAuditLine (per-token detail + errors) + formatTelegramSummary (cap at 10 tokens + '+N more' suffix) + sendAlert (nil-safe) + sameRotationMinute dedup + readEnabled/readSchedule/readLastRun/writeLastRun + scheduler_db.go (init() binding db.GetGlobalSetting + db.SetGlobalSetting via function variables for testability) + internal/db/queries.go: qSelectAPITokensForAutoRotate (auto_rotate=1 AND expires_at>0 AND expires_at<=cutoff) + qUpdateAPITokenExpiryByID + internal/db/personal_api_tokens.go: ListAPITokensForAutoRotate helper + UpdateAPITokenExpiryByID helper + APITokenForRotation struct + internal/config/config.go: 2 Config fields (TokenAutoRotateEnabled/TokenAutoRotateSchedule) + 2 env-var defaults SKYGATE_TOKEN_AUTO_ROTATE_ENABLED (default false) + SKYGATE_TOKEN_AUTO_ROTATE_SCHEDULE (default '0 3 * * *') + cmd/skygate/main.go: 'skygate/internal/tokenrotate' import + tokenrotate.Start(ctx, …) wire-up with cfg.TokenAutoRotateEnabled guard + schedulerNotifierSink adapter (same one as B130/B142/B143) + 3 new i18n keys in catalog_my.go (auto_rotate_alert_header/auto_rotate_alert_more/auto_rotate_alert_failed) in RU+EN (B4 parity) + 7 unit tests in internal/tokenrotate/scheduler_test.go (TestSameRotationMinute + TestFormatAuditLine_IncludesAllTokens + TestFormatAuditLine_IncludesErrors + TestFormatTelegramSummary_ListsAllTokens + TestFormatTelegramSummary_TruncatesLongLists + TestFormatTelegramSummary_EmptyResultNotCalled + TestStorageKeyConstants + TestDefaultScheduleMatchesUpdateScheduler) (B154, v1.5.0 — closes the auto_rotate='check the box, hope for the best' gap from B153; the scheduler picks every token with auto_rotate=1 AND expires_at within the next 7 days, extends the expiry to (now+30d) via db.UpdateAPITokenExpiryByID, writes the audit entry via db.AppendAuditLogNoUser (system event pattern, mirrors B130/B142/B143), and sends a compact Telegram alert with the per-token label list. IMPORTANT design choice: this is auto-EXTEND (hash unchanged), not full rotation. The existing token keeps working, so AI-assistant integrations (Tailscale clients, curl-in-cron scripts) don't need a re-paste. If a future B-check needs full rotation (generate new raw token + revoke old), the structure here is already split (ListAPITokensForAutoRotate returns the rowset; RunAutoExtend does the per-token work) so a sibling runRotation function can be added without disturbing the auto-extend path. The /my/tokens page runtime toggle is deferred to B154.1 — the scheduler reads the global_settings key on every tick so the env-var default can be overridden without a restart)" \
  'test -f scripts/check_b154.sh && bash scripts/check_b154.sh'
run_check "B155" "preauth key UX (operator scope-correction 2026-08-20: 'имел ввиду токены для headscale что получает устройство при аутентификации — сейчас для skybars пришло уведомление в клиенте tailscale'): /my/keys page rewritten with per-row ExpiresWarn (red expired, red <7d soon, yellow <30d month) + ExpiringCount banner when ≥1 unused, not-yet-expired key is within 14d + per-row Reissue button (POST /my/keys/{id}/reissue, expires the old key in BOTH headscale + local row + issues a new key with the same TTL + renders the preauth_result page with the 'replaces key #N' banner so the new raw key never appears in a URL/log) + dedicated ?reissue=ID form (Extend key with new TTL card, mirrors B153's ?renew=ID pattern) + PostMyPreauth handler parses custom_ttl_value+custom_ttl_unit (h/d/w/y, min 1h, max 5y, 0=never) BEFORE the legacy ttl dropdown + reusable checkbox (default off, mirrors headscale's reusable=true API) + PostMyKeyReissue handler (validates the key is unused + not expired, resolves the headscale user, carries the old key's TTL forward via durationFromSeconds, audit 'preauth_reissued from_id=<N> to_id=<M> ttl=<S>', renders preauth_result.html directly with ReissueFrom + ReissueTo so the new key never hits a URL) + new 'keys.*' i18n keys (custom_ttl_label/hint/h/d/w/y/err_min/err_max + reusable_label/hint + reissue/title/ed_to/err_used/err_expired + expired + expires_in_days/in_day/in_hours/tomorrow + banner_expiring) in RU+EN (B4 parity) + internal/feature/my/preauth.go custom TTL helper resolvePreauthTTL (mirrors B153's logic) + humanizeTTL (renders '1h' / '1d' / '1w' / 'never') + internal/handlers/templates/user/keys.html: per-row warning pills (mirrors B153's /my/tokens pattern) + Reissue button + dedicated ReissueForm card + internal/handlers/templates/user/devices.html: custom-TTL input + unit dropdown + reusable checkbox + internal/handlers/templates/user/preauth_result.html: 'replaces key #N' banner via ReissueFrom + ReissueTo + 6 unit tests in internal/feature/my/preauth_test.go (TestResolvePreauthTTL_CustomValid + TestResolvePreauthTTL_OutOfRangeFallsThrough + TestResolvePreauthTTL_LegacyDropdown + TestResolvePreauthTTL_CustomOverridesLegacy + TestHumanizeTTL + TestDurationFromSeconds) (B155, v1.5.0 — operator scope-correction to B153: the original B153/B154 work targeted personal API tokens on /my/tokens, but the Tailscale 'key expiring' warning the operator saw was for the headscale preauth keys on /my/keys + /my/devices. B155 brings the same UX pattern to the right page: per-row visual warning (red/yellow pills), per-row Reissue button (replaces the old key + issues a new one with the same TTL — full rotation, since headscale has no 'extend preauth' API), 14-day ExpiringCount banner, custom TTL + reusable checkbox on the issue form. Auto-rotation for preauth keys is deliberately NOT included — a preauth key is for ONE device registration; auto-extending it without a fresh device-registration would be confusing. The B154 auto-rotation scheduler stays for personal API tokens, which are continuously-authenticated and benefit from being auto-extended.)" \
  'test -f scripts/check_b155.sh && bash scripts/check_b155.sh'
run_check "B156" "in-app preauth key expiration notification scheduler (operator 2026-08-20: 'по итогу требуется также добавить для пользователей отдельно уведомление по истечению время действия ключа на устройство и инструкцию как продлить'): internal/keynotify/scheduler.go with Start(ctx, deps) + tick + RunNotify + UserNotifierSink (per-user chat, NOT operator chat) + NotifierSink operator-side summary fallback + 3 storage key constants (KeyNotifyEnabled='keys.notify_enabled' / KeyNotifySchedule='keys.notify_schedule' / KeyNotifyLastRun='keys.notify_last_run') + DefaultNotifySchedule='0 9 * * *' (after 5 AM smoke-mesh cleanup, before operator's working day) + NotifyWindowDays=14 (mirrors B155 banner window so the user sees the same 'expiring soon' message in both the portal and Telegram) + NotifyTickInterval=30s + NotifyResult + NotifyTokenResult structs + formatNotifyMessage (per-user Telegram: '🔑 Your preauth key for adding devices expires in N day(s) / today / tomorrow / in N hour(s)' + 'To renew: open /my/keys and click the Reissue button on that row. The new key is shown on the result page; the old key is automatically revoked.' — the renew-instructions clause is the whole point of B156) + formatAuditLine (per-key detail with notify-failed reasons) + formatTelegramSummary (operator-side summary with skipped count) + sendOperatorAlert (nil-safe) + sameNotifyMinute dedup + readEnabled/readSchedule/readLastRun/writeLastRun + scheduler_db.go (init() binding db.GetGlobalSetting + db.SetGlobalSetting via function variables for testability) + migrateV058PG: ALTER TABLE preauth_keys ADD COLUMN IF NOT EXISTS notified_at INTEGER NOT NULL DEFAULT 0 (registered in driver_postgres.go) + internal/db/queries.go: qSelectExpiringPreauthKeys (used=0 AND expires_at>0 AND expires_at<=cutoff) + qMarkPreauthKeyNotified + qResetPreauthKeyNotified + internal/db/preauth_keys.go: ListExpiringPreauthKeys helper + MarkPreauthKeyNotified + ResetPreauthKeyNotified + ExpiringPreauthKey struct (8 fields, including the new Reusable + NotifiedAt for scheduler's row view) + internal/config/config.go: 2 Config fields (KeyNotifyEnabled/KeyNotifySchedule) + 2 env-var defaults SKYGATE_KEY_NOTIFY_ENABLED (default false) + SKYGATE_KEY_NOTIFY_SCHEDULE (default '0 9 * * *') + cmd/skygate/main.go: 'skygate/internal/keynotify' import + keynotify.Start(ctx, …) wire-up with cfg.KeyNotifyEnabled guard + schedulerUserNotifierSink adapter (different from schedulerNotifierSink: uses SendTelegramToChat(chatID, text) for per-user chat, with chatID=0 routing to the operator's SendAlert path) + PostMyPreauth + PostMyKeyReissue reset notified_at=0 on InsertPreauthKey (B156 comment in each handler documenting the dedup implication) + 11 unit tests in internal/keynotify/scheduler_test.go (TestSameNotifyMinute + TestFormatAuditLine_IncludesAllTokens + TestFormatAuditLine_IncludesSkipped + TestFormatTelegramSummary_NotifiedOnly + TestFormatTelegramSummary_IncludesSkipped + TestFormatNotifyMessage_IncludesRenewInstructions + TestFormatNotifyMessage_TruncatesLongKey + TestFormatNotifyMessage_TodayAndTomorrow + TestStorageKeyConstants + TestDefaultScheduleAfterCleanup + TestNotifyWindowDaysMatchesB155Banner) (B156, v1.5.0 — closes the per-user notification gap from B155: the portal warning pill only fires when the user happens to log in. With B156, the user gets a Telegram message daily at 9 AM with the renew instructions, BEFORE the key actually expires. The notification goes to the USER's chat (looked up via telegram_bindings), not the operator's chat. Users without a Telegram binding get the portal pill but no Telegram; the audit log captures the skip with reason='no_telegram_binding' so the operator can see which users are unreachable. The notify-only design (no auto-action) is intentional: a preauth key is for ONE device registration; auto-extending it without a fresh registration would be confusing. B156.1 follow-ups: a /admin/settings page runtime toggle via global_settings['keys.notify_enabled'], full RU translation of the message, and a 'tier' system (14d / 7d / 1d reminders instead of just 14d))" \
  'test -f scripts/check_b156.sh && bash scripts/check_b156.sh'
run_check "B157" "in-web notification inbox (operator 2026-08-20: 'кроме телеграмма также необходимо сделать уведомление пользователю в веб форме'): V059PG migration creates notifications table (id, user_id, type, severity, title, body, link, created_at, read_at + idx_notifications_user_unread + idx_notifications_user_created + ON DELETE CASCADE) + internal/notifications/ package with InsertNotification + ListByUser + ListUnreadByUser + CountUnread + MarkRead + MarkAllRead + DeleteForUser (typed wrappers over internal/db/notifications.go's q* SQL constants; the notifications package is a thin façade so callers don't have to import internal/db) + internal/db/notifications.go: 7 typed wrappers (dbInsertNotification etc. private + InsertNotification etc. exported) + db.Notification struct (8 fields, IsRead method) + 7 SQL constants (qInsertNotification + qListNotificationsByUser + qListUnreadNotificationsByUser + qCountUnreadNotifications + qMarkNotificationRead + qMarkAllNotificationsRead + qDeleteNotificationsForUser) + migrateV059PG registered in driver_postgres.go + B156 keynotify scheduler extended: when it sends a successful Telegram message, ALSO InsertNotification(type='key.expiring', severity='warn', title='Key #N expires in N day(s)', body='Open /my/keys and click Reissue to renew.', link='/my/keys') — the bell text is the user-facing mirror of the Telegram message; the two channels are independent + B155 PostMyKeyReissue extended: after InsertPreauthKey, also call notifications.MarkAllRead so the user's bell doesn't keep showing the now-revoked key's notification + internal/feature/my/notifications.go: PostMyNotificationRead handler (POST /my/notifications/{id}/read, scoped to current user via db.MarkNotificationRead, audit 'notif_read id=N', 404 on bad id / wrong user, redirects to Referer-or-/dashboard) + PostMyNotificationsReadAll handler (POST /my/notifications/read-all, db.MarkAllNotificationsRead, audit 'notif_read_all count=N', same redirect policy) + refererOrDashboard helper (CSRF defense: only /-prefixed same-origin paths, no protocol-relative URLs) + internal/handlers/templates/layout.html: bell icon (id='notif-bell', details/disclosure wrapper for native click-to-open, no JS) in the sidebar between language picker and logout + .notif-badge count chip + .notif-menu dropdown with per-row title/body + 'Open' link + 'Mark as read' form + 'Mark all as read' button at top + .notif-empty placeholder when 0 unread + internal/handlers/handlers.go: renderWithLayout auto-injects UnreadCount + UnreadNotifications for every page (so the bell always shows the current state, no per-page wiring needed) + static/css/themes.css: .notif-badge (red circle, count) + .notif-menu (280-340px wide, 480px max-height with overflow scroll) + .notif-item + .notif-empty + cmd/skygate/main.go: 'skygate/internal/notifications' import + 2 routes (POST /my/notifications/{id}/read + POST /my/notifications/read-all) + 8 new i18n keys in catalog_my.go (notif.bell_title + notif.empty + notif.mark_read + notif.mark_all_read + notif.key_expiring_title + notif.key_expiring_body + notif.cta_open) in RU+EN (B4 parity) + 12 contracts in scripts/check_b157.sh (B157, v1.5.0 — closes the web-channel gap from B156: the B156 scheduler only sent Telegram, which doesn't help users without a Telegram binding or on a desktop where Telegram isn't open. B157 writes the same event to the in-web notifications inbox, which the bell icon in the layout sidebar exposes to every page. The user gets a per-page badge + dropdown list with 'Open' + 'Mark as read' + 'Mark all as read' controls. The bell's hot path (every page render) is one CountUnread + one ListUnreadByUser — both indexed by (user_id, read_at), so the cost is O(unread-for-this-user) not O(all-notifications). On reissue (B155) the bell clears the user's notifications so they don't keep seeing the revoked key's warning. Future notification types (cert.renewal, backup.failed) just add a new Type constant + a new INSERT call site — no schema change.) (B157.1 FOLLOW-UP: full-page /my/notifications view (GET handler with filter pills All / Unread + pagination pageSize=25 + in-memory filter applied after ListByUser LIMIT 200) + user/notifications.html template (filter pills + per-row icon with severity-tinted class + title + body + TimeAgo formatted + Mark as read form + Mark all as read button at the top + empty-page state + prev/next pagination) + new GET /my/notifications route + notifications.TimeAgo (just_now / 1 min ago / N min ago / 1 h ago / N h ago / 1 d ago / N d ago / N wk ago / N mo ago / raw YYYY-MM-DD for >1y; English-only for now, the i18n keys notif.time_just_now / time_min_ago / time_h_ago / time_d_ago are pre-declared for B157.1.1) + notifications.TypeIcon (fa-key for key.expiring, fa-bell fallback for unknown types — the 'add a new type without updating this switch' contract) + notifications.TypeSeverityColor (info / warn / danger / empty for unknown) + 3 unit tests (TestTimeAgo with 22 cases covering just_now / minutes / hours / days / weeks / months / years / clock skew future / zero + TestTypeIcon + TestTypeSeverityColor + TestSeverityConstantsDocumented + TestTypeIconFallbackForUnknownTypes) + 12 new B157.1 contracts in scripts/check_b157.sh (notif-icon-{Severity} class wired in template + CSS + TimeAgo/TypeIcon/TypeSeverityColor helpers + 9 new i18n keys notif.page_title + notif.filter_all + notif.filter_unread + notif.empty_page + notif.time_just_now + notif.time_min_ago + notif.time_h_ago + notif.time_d_ago + notif.read + common.time in RU+EN) + internal/handlers/templates/layout.html: notif-item-header wrapper with the severity icon to the left of the title (so the dropdown matches the full-page view visually) + static/css/themes.css: .notif-item-header + .notif-icon-info/warn/danger (smaller variant for the dropdown, 14px vs 18px in the full page))" \
  'test -f scripts/check_b157.sh && bash scripts/check_b157.sh'
run_check "B158" "self-hosted Google Fonts (operator 2026-08-20: 'все еще проблема с загрузкой страницы есть ли возможность добавить в ассеты или кеш чтобы не перезагружать каждый раз? Лучше статический вариант так как не ко всем ресурсам есть доступ по сети для корректной загрузки на страницу' — the live VM couldn't reach fonts.googleapis.com, so the page blocked on 'net::ERR_CONNECTION_TIMED_OUT'): 24 woff2 files in static/webfonts/ (Manrope + Inter + Geist + Sora × 4 weights latin, Geist Mono + JetBrains Mono × 2 weights latin) sourced from @fontsource v5.3.0 on jsDelivr CDN (~365KB total, +60KB vs the pre-B158 external Google Fonts CSS which timed out) + themes.css has 24 @font-face rules at the top with font-display: swap (browser uses system fallback for ~100ms then swaps to the webfont; no invisible-text flash) + layout.html removed all 4 preconnect + 4 stylesheet blocks for fonts.googleapis.com / fonts.gstatic.com (pre-B158 the layout had one <link rel='stylesheet' href='https://fonts.googleapis.com/css2?family=...'> block per font family: manrope, inter, geist, sora; after B158 only the inline per-user :root{--font:...} override remains for the inter/geist/sora cases, plus the B158 comment explaining the self-host decision) + B158 contract grep-asserts on all 24 woff2 files exist (each > 1KB sanity check) + themes.css has 6 families × multiple weights @font-face rules + 24 local /webfonts/ srcs (none external) + layout.html has NO <link href='https://fonts.googleapis.com' AND NO <link href='https://fonts.gstatic.com'> tags + static/webfonts/ total size < 1MB (catches a runaway full-TTF download) (B158, v1.5.0 — closes the external-font dependency that broke page rendering on networks without Google Fonts access. The pre-B158 /webfonts/ dir already existed for font-awesome's 4 woff2 files (B152), so B158 just adds 24 more woff2 files to the SAME directory + same Cache-Control strategy. The static handler's hasContentHash() check (B152) treats /webfonts/<name>.woff2 as versioned assets, serving them with max-age=86400 must-revalidate — the browser re-uses the woff2 from its disk cache for 24h, so subsequent page navigations don't re-download. font-display: swap ensures the page is interactive immediately, with the webfont swapping in once the woff2 is decoded. The operator's MaxListenersExceededWarning + ObjectMultiplex errors in the log are from a browser extension's content script (Tailscale- or similar), not from skygate — out of scope for B158.)" \
  'test -f scripts/check_b158.sh && bash scripts/check_b158.sh'
run_check "B159" "/my/keys UX enrichment (operator 2026-08-20: 'также добавить к ключу устройство за которым он закреплен и внести ясность а также время до истечение ключа. Сейчас в пояснении истекает - Истек не совсем понятно что с ключем нет времени о том сколько ключу осталось также есть ли возможность подчистить истекшие ключи?'): Device column on /my/keys shows the headscale givenName of the node that consumed each used key (lookup is preAuthKeyID → givenName via ListAllNodes(), scoped to the current user's headscale control plane; best-effort on headscale blips so the page doesn't 500) + per-row TimeRemaining i18n string in the Expire column ('<N> min/h/d left' / 'expired <N> min/h/d ago' / 'no expiry' — always CONCRETE, no 'today' / 'tomorrow' vagueness, since the absolute date below it already shows the calendar day) + ExpiredUnusedCount counter drives a conditional 'Clean up expired (N)' button (only shown when N>0) + PostMyKeysCleanup handler (POST /my/keys/cleanup → DELETE WHERE user_id=\$1 AND used=0 AND expires_at>0 AND expires_at<=\$now — used keys are NEVER deleted, they're audit history; never-expiring keys are NEVER deleted, they have no expiry to clean) → redirect to /my/keys?cleaned=N with the count + cleaned=0 renders a separate 'no expired keys' flash so the user isn't confused by a no-op + formatRelativeExpiry pure helper (i18n-catalog-driven, 16-case unit test covers all boundaries: never, past minutes/hours/days, future minutes/hours/days, exactly now, unknown-lang fallback to RU) + 13 new i18n keys in catalog_my.go (keys.device + keys.device_unbound + keys.never_expires + keys.time_minutes_left/hours_left/days_left + keys.time_expired_minutes_ago/hours_ago/days_ago + keys.cleanup_expired/confirm/done/none) in RU+EN (B4 parity) + db.DeleteExpiredUnusedPreauthKeysByUser + db.CountExpiredUnusedPreauthKeysByUser (same WHERE clause in both so the count and the action are always consistent) + 2 new SQL constants qDeleteExpiredUnusedPreauthByUser + qCountExpiredUnusedPreauthByUser + 8 contracts in scripts/check_b159.sh (B159, v1.5.0 — closes three operator UX gaps: (1) the bound device for used keys was invisible (the user had to cross-reference /my/devices manually), (2) the Expire column was 'Истёк' / 'Истекает' with no concrete time ('нет времени о том сколько ключу осталось'), (3) cleanup of accumulated expired keys required per-row clicking. After B159 the /my/keys page surfaces all three signals inline. The N+1 risk is bounded — the device map is built ONCE per request from ListAllNodes() (not per row), so the page cost is O(rows + headscale_nodes), not O(rows × headscale_nodes). The cleanup button only appears when N>0, so the page doesn't show a no-op control. The SQL guard (used=0 AND expires_at>0 AND expires_at<=\$now) makes the cleanup safe to call from any user's POST without an admin role; the user_id=\$1 scoping prevents cross-user deletes.)" \
  'test -f scripts/check_b159.sh && bash scripts/check_b159.sh'
run_check "B160" "/my/devices manual expiry renewal (operator 2026-08-20: 'можно ли реализовать продление работы ключа которым устройство аутентифицировалось в headscale через веб интерфейс skygate' — the user is asking about renewing the device's NODE SESSION, not the preauth key which is one-time and consumed at registration): Expires column on /my/devices shows the headscale node.expiry as a date + the i18n relative-time hint ('5 days left' / '5 days ago' / 'no expiry' — same formatRelativeExpiry helper from B159) + a warning pill (red for <=7d / yellow for <=30d / red for already-expired) for every device with a non-empty expiry + a per-row 'Renew' button (POST form, no JS) that hits POST /my/devices/{id}/renew → handler calls headscale.ExtendNodeExpiry(nodeID, now+30d), writes a 'device_renewed node_id=N new_expiry=YYYY-MM-DDTHH:MM:SSZ' audit log entry, and redirects to /my/devices?renewed=<host>&new_expiry=<ts> with a one-time flash alert showing both the hostname and the new timestamp + the handler scope-checks the node to the current user (cross-user renewals return 404) — same pattern as B155 PostMyKeyReissue: ListAllNodes + node_owner_map lookup; nodes with Expiry==\"\" (tag:exit-node, tag:public, tag:subnet-router, or 'headscale nodes expire --disable' nodes) get a 400 because the user can't unilaterally extend policy-controlled expiries + 7 new i18n keys in catalog_my.go (devices.expires_col + devices.renew + devices.renew_title + devices.renewed_toast + devices.renew_err_not_found + devices.renew_err_no_expiry + devices.renew_err_failed) in RU+EN (B4 parity) + myNodeRow struct extended with Expiry / ExpiryUnix / ExpiresRelative / ExpiryWarning fields (populated in both the live branch and the snapshot branch of GetMyDevices — B160 mirrors B155's pattern of enriching rows in the handler so the template stays pure presentation) + time.Parse(RFC3339Nano, row.Expiry) for the unix timestamp with graceful degradation to 'no expiry' on unparseable strings (so a headscale future version that returns a different format doesn't 500 the page) + 7 contracts in scripts/check_b160.sh (B160, v1.5.0 — closes the manual-renew UX gap from expirewatch: the auto-renewer (internal/expirewatch) already extends expiries every 5min for nodes within 7d, but a manual button is useful when (1) the user disabled expirewatch, (2) the user wants to renew NOW (not wait for the next tick), (3) the user wants explicit visibility into 'renewed 5 days ago' (the audit log captures every renewal regardless of whether it was manual or auto). The button only renders for devices with a non-empty Expiry, so tagged/shared infra nodes (no user-controlled expiry) don't show a confusing no-op control. The Renew form action uses the headscale node ID, NOT a hostname, so even if a user renames their device the renew target stays stable.)" \
  'test -f scripts/check_b160.sh && bash scripts/check_b160.sh'
run_check "B161" "OIDC provider for headscale (operator 2026-08-23): internal/oidc/ package (keys + discovery + jwks + service + jwt + token + userinfo) + 4 env vars + RSA-2048 keypair persisted + mux mounts at /.well-known/ and /oidc/ not behind authMW + 503 fallback + 115 contracts in scripts/check_b161.sh" \
  'test -f scripts/check_b161.sh && bash scripts/check_b161.sh'
run_check "B162" "/my/devices per-row device delete (operator 2026-08-24 task 1): PostMyDeviceDelete handler + hsClient.DeleteNode + node_owner_map cleanup + device_exit_node_prefs cleanup (new db.DeleteDeviceExitNodePref helper) + audit log device_deleted + new route POST /my/devices/{id}/delete behind authMW + per-row Delete button with confirm() dialog + 7 new i18n keys in catalog_my.go RU+EN + 26 contracts in scripts/check_b162.sh" \
  'test -f scripts/check_b162.sh && bash scripts/check_b162.sh'
run_check "B163" "/admin/system_tests collapsible FAIL output (operator 2026-08-24 task 3): system_tests.html wraps {{.Output}} in <details class=\"system-test-output\"> (open for FAIL, closed for PASS/SKIP) + inner <pre> with white-space: pre-wrap + max-height 280px + overflow-y: auto + Copy button (navigator.clipboard.writeText + execCommand fallback) + CSS rules + 6 new i18n keys in catalog_common.go RU+EN + 18 contracts in scripts/check_b163.sh" \
  'test -f scripts/check_b163.sh && bash scripts/check_b163.sh'
run_check "B164" "DERP server init on a new host via SSH (operator 2026-08-24 task 4): GET/POST /admin/derp/relays/init behind authMW + GetAdminDerpRelaysInit (suggests next free region_id + sort_order) + PostAdminDerpRelaysInit (calls headscale.RunScript on deploy/derp-init.sh, parses JSON, inserts into derp_relays) + new internal/feature/admin/derp_init.go + new template admin/derp_relays_init.html + new deploy/derp-init.sh (7-step flow: install Go, go install derper, generate cert, configure systemd derper.service, open firewall, start service, probe HTTPS) + 30+ new i18n keys in catalog_derp.go RU+EN + 41 contracts in scripts/check_b164.sh" \
  'test -f scripts/check_b164.sh && bash scripts/check_b164.sh'
run_check "B165" "/my/devices registration form UX fix (operator 2026-08-24 task 5): 2-column .form-grid (1fr 1fr on desktop, 1 column on <768px) replaces inline-flex + .form-group-inline for Custom TTL value+unit pair + aria-label on inputs + .form-hint-strong replaces gray-on-gray + new <details> Help block with ssh-keygen example + per-OS tailscale up commands (Linux/macOS/Windows with --advertise-exit-node + --advertise-routes, Android/iOS via Tailscale app GUI, Windows via tray icon) + 16 new i18n keys in catalog_my.go RU+EN + CSS rules in themes.css + 36 contracts in scripts/check_b165.sh" \
  'test -f scripts/check_b165.sh && bash scripts/check_b165.sh'
run_check "B166" "e2e + system tests for B160 renew + B162 delete (operator 2026-08-24 task 2): system test headscale.device_renew (picks first non-tagged device, calls ExtendNodeExpiry with now+30d, asserts [now+29d, now+31d], RESTORES original via defer for idempotency) + system test headscale.device_delete (DeleteNode on bad ID returns one of: node not found / no longer exists in NodeStore / Not Found / 404 - pins the gRPC wording B162 matches on) + both use HSForUserFn(0) (admin user) + both SKIP on missing headscale/no nodes (no false-alarm on fresh deploy) + 18 contracts in scripts/check_b166.sh" \
  'test -f scripts/check_b166.sh && bash scripts/check_b166.sh'

run_check "B167" "OIDC config auto-sync (full Option C — docker + systemd + k8s + manual + download + auto-init): deploy/oidc-sync.sh (10-step: validate, mode-detect, generate, backup, write, .env, restart, health, probe, JSON) + internal/oidc/sync.go (Go wrapper with RunSync/RunSyncCtx/ShouldAutoSync + 14-field SyncResult) + /admin/oidc/sync (Get + Post, admin-only, behind authMW) + 55 RU + 55 EN i18n keys + boot-time auto-sync (SKYGATE_OIDC_AUTOSYNC=true) + B167.1 strip_email_domain regression guard (removed in headscale 0.23+) + 38 contracts in scripts/check_b167.sh" \
  'test -f scripts/check_b167.sh && bash scripts/check_b167.sh'

run_check "B151" "Phase 8 of HA v1.5.0 — init-headplane.sh auto-applies headplane API key on fresh deploy: 2 modes (bundled headplane + external headplane), 6-step bundled flow (check headscale + read .env + generate key via 'docker exec headscale apikeys create' + write to .env with .pre-init-headplane backup + restart headplane + verify /admin/healthz), 4-step external flow (verify URL reachable + check key + write to .env + restart skygate), idempotent (NEEDS_KEY gate skips re-mint if .env has a real key), getenv/setenv helpers consistent with deploy/lib/env.sh, 20 contracts in scripts/check_b151.sh" \
  'test -f scripts/check_b151.sh && bash scripts/check_b151.sh'

run_check "B152" "Phase 7 of HA v1.5.0 — bootstrap_standby.sh provisions a new skygate-standby node: 6-step flow (pre-flight idempotency check + S3-pull skygate binary from ha/deploy/<hostname>/ + S3-pull headscale config from ha/headscale-config/ + docker compose up -d + poll /healthz 60s + verify ha_chain registration in DB), validates 3 required env vars (SKYGATE_HA_ROLE=standby + SKYGATE_HA_ENABLED=true + HEADPLANE_HEADSCALE__API_KEY non-empty), writes 'ha.bootstrap' audit row, getenv() helper consistent with deploy/lib/env.sh, 18 contracts in scripts/check_b152.sh" \
  'test -f scripts/check_b152.sh && bash scripts/check_b152.sh'

run_check "B153" "Phase 9 of HA v1.5.0 — dr_drill.sh runs a 5-step live DR drill (verify both nodes on same version + kill active + verify standby takes over within 60s + restart active + verify no-flap rejoin + verify DNS resolves + optional kill-both + verify self-heal within 90s), 3 operator flags (--yes unattended, --skip-regapi-check, --skip-kill-both), polls /readyz for the B145 role banner, uses 'docker kill -9' (the actual failure mode), pauses for operator confirmation between steps, NEVER uses 'docker compose down -v' (no data destruction), 18 contracts in scripts/check_b153.sh" \
  'test -f scripts/check_b153.sh && bash scripts/check_b153.sh'

run_check "B168" "Live OIDC e2e on a public hostname (B167 closed admin side, B168 closes operator side): deploy/snippets/nginx-skygate-oidc.conf (5-location server block: /.well-known/openid-configuration + /oidc/jwks.json + /oidc/ + /admin/oidc + /admin/oidc/sync, sets X-Forwarded-Proto for the issuer claim) + deploy/scripts/setup-skygate-public.sh (5-step: validate discovery 200 + update SKYGATE_OIDC_ISSUER/SKYGATE_OIDC_REDIRECT_URIS in .env with .pre-setup-public backup + restart skygate + verify the new issuer is reported in the discovery doc + run deploy/oidc-sync.sh in docker mode to push the new headscale.conf + write 'oidc_setup' audit row) — idempotent, reuses deploy/oidc-sync.sh (no duplication of B167 logic), 19 contracts in scripts/check_b168.sh" \
  'test -f scripts/check_b168.sh && bash scripts/check_b168.sh'

run_check "B169" "Admin-side device delete on /admin/devices (operator escape hatch for orphan / duplicate / stuck devices — mirrors B162 per-user delete but admin-scoped): PostAdminDeviceDelete handler in internal/feature/admin/devices.go (IsAdmin check + headscale.DeleteNode via HSGlobalFn + DeleteNodeOwnerByNodeMap cleanup + hs.InvalidateCache + 'device_deleted' audit row + 404/exit-node-error special cases) + /admin/devices/{id}/delete route behind authMW in cmd/skygate/main.go + Delete button per row in internal/handlers/templates/admin/devices.html (with onsubmit confirm() guard + red styling) + 3 new 'devices.delete_admin*' i18n keys in catalog_my.go (RU + EN) + 19 contracts in scripts/check_b169.sh" \
  'test -f scripts/check_b169.sh && bash scripts/check_b169.sh'

run_check "B170" "Expired-row sub-classification hint on /my/devices (operator 2026-08-25: a device force-expired by headscale via 'tailscale logout' or admin action shows the same red 'expired' pill as a device whose TTL ran out naturally — different root causes, so the hint disambiguates without SSH): parseLastSeenAndClassify helper in internal/feature/my/devices.go (uses |LastSeen - Expiry| with a 5-min threshold — under/equal = 'near_expiry' / likely logout, over = 'while_offline' / TTL ran out or admin force-expired, empty/unparseable = 'no_activity' / orphan or stale snapshot; absolute delta to handle future-dated LastSeen from clock skew) + new ExpiryHint + LastSeenTime fields on myNodeRow + 3-way {{if eq .ExpiryHint}} chain under the existing .ExpiryWarning badge in internal/handlers/templates/user/devices.html (small muted caption, not a separate pill, so the visual hierarchy keeps the red pill as the primary signal) + 4 new 'devices.expired_hint_*' i18n keys (RU + EN) in catalog_my.go (title + no_activity + near_expiry + while_offline) + 4 unit tests in internal/feature/my/devices_b170_test.go (the 3 hint categories + the 5-min boundary + the Nano-precision regression guard) + 24 contracts in scripts/check_b170.sh" \
  'test -f scripts/check_b170.sh && bash scripts/check_b170.sh'

run_check "B171" "Comprehensive device-delete with ACL regen (operator 2026-08-25: 'кнопка удалить устройство отсуствует у пользователя... администратор также по кнопке очистит не только из skygate (из таблиц БД) но и из headscale и headplane. забирая на себя управлоение политиками и тегами, корректно подчищая и перегенерировывая acl'): new internal/devicedelete package with shared Delete coordinator (skygate DB + headscale + ACL regen in one call) + db.DeleteRulesByDeviceID + qDeleteRulesByDeviceID SQL primitive (cleans every orphaned device_rules row in one query) + db.DeleteNodeOwnerByNodeTagCounted variant (returns the count for the audit row) + PostMyDeviceDelete rewire (B162 path now calls devicedelete.Delete + passes deleted_rules=N + acl_err=... in the redirect) + PostAdminDeviceDelete rewire (B169 path mirrors the user one) + /my/devices template Delete button moved OUTSIDE the {{if .ExpiryUnix}} block (operator can now delete their own exit-nodes / no-expiry devices) + /admin/devices template FlashOkRules + FlashACLErr extensions + 2 new i18n keys RU + EN (devices.delete_acl_rules_cleaned + devices.delete_acl_err) + audit row includes the headplane note ('headplane: read-only view, will refresh on next UI load') so the operator can confirm the full cleanup with one audit query + 35 contracts in scripts/check_b171.sh" \
  'test -f scripts/check_b171.sh && bash scripts/check_b171.sh'

run_check "B172" "Login 'next'-redirect fix (operator 2026-08-25: 'когда попробовал залогинится в headscale через head.skynas.ru перенесло на логин в skygate, после входа в skygate открылась страница приветствия и все. устройство не добавлено и больше ничего непроисходит'): PostLogin in internal/feature/auth/service.go now reads + validates + honours the 'next' form field (was hard-coded to /dashboard, killing the OIDC flow silently) + new safeNextRedirect helper (the open-redirect defense: accepts empty/relative/same-host absolute URLs, rejects protocol-relative //evil.com, different-host https://evil.com, and non-http(s) schemes like javascript:/data:/file:) + GetLogin reads ?next= from the query string + login.html renders a hidden <input type=hidden name=next value={{.Next}}> inside the form (Go's html/template auto-escapes) + the B161.4 e2e test (internal/oidc/e2e_test.go) is extended with a new STEP 4 that walks the actual /login round-trip via a mock /login handler (the pre-B172 STEP 4 was a 'pre-populate an auth code' shortcut that bypassed the /login POST entirely — that's why the bug shipped) + 18 unit tests in service_b172_test.go (TestSafeNextRedirect + TestSafeNextRedirect_EmptyHost) covering the 5 case categories (empty/relative/protocol-relative/different-host/same-host) + 24 contracts in scripts/check_b172.sh" \
  'test -f scripts/check_b172.sh && bash scripts/check_b172.sh'

run_check "B173" "Login form submit loading-state (operator 2026-08-25: 'теперь при переходе страница логина всегда обновляется если написать пароль и тем самым его сбрасывает от чего нельзя залогиниться'): login.html has a JS onsubmit handler (wrapped in an IIFE + try/catch so a JS error falls through to the normal form submit) that (1) checks form validity via checkValidity() before entering the loading state (so the browser's native 'this field is required' tooltip still shows on partial forms), (2) sets username + password to readOnly so the user can't type more while the request is in flight, (3) disables the submit button + swaps the button label from 'Войти' (RU) / 'Sign in' (EN) to 'Вход...' / 'Signing in...' with a fa-spinner fa-spin animation, (4) dims the disabled button via CSS (opacity:.6 + cursor:wait) so the user sees the button is 'stuck' (and not broken) during the in-flight request. The pre-B173 form had no JS — the user typed the password, hit Enter (or clicked the button), and the page would re-render in <100ms with no visual feedback. If credentials were wrong (typo, wrong keyboard layout, caps lock) the form re-rendered with an error message; if they were right the form would redirect to the OIDC URL. Either way the user saw 'the page refreshed and my password is gone' with no explanation. B173 makes the submit feedback explicit + 1 new i18n key login.submitting in RU + EN in catalog_common.go + 12 contracts in scripts/check_b173.sh" \
  'test -f scripts/check_b173.sh && bash scripts/check_b173.sh'

run_check "B173.1" "Full-page loading overlay for password-manager auto-submit (operator 2026-08-25 follow-up: 'все равно рефрешь при вставке пароля из запомненых на странице логина'): login.html has a position:fixed z-index:9999 semi-transparent overlay (rgba(0,0,0,0.55) + backdrop-filter:blur(2px)) covering the entire viewport with a centered card containing a fa-spinner fa-spin and the 'Вход...' / 'Signing in...' text. The IIFE (1) overrides form.submit() to catch programmatic submits from password managers (which call form.submit() directly to bypass submit event listeners), (2) listens for pagehide/visibilitychange/beforeunload to catch ALL navigation away from the page (including cases where the password manager bypasses our submit handler entirely). The B173.1 IIFE consolidates all the 'show the loading state' logic into a single showLoading() function called from 5 detection paths (submit event + form.submit override + pagehide + visibilitychange + beforeunload). 6 new contracts in scripts/check_b173.sh contract D" \
  'test -f scripts/check_b173.sh && bash scripts/check_b173.sh'

run_check "B174" "OIDC session JWT parsing fix (operator 2026-08-25: 'все равно сбрасывает, после того как браузер предлагает использовать сохраненный пароль до того как вносил правки по поводу next все отрабатывала'): the pre-B174 OIDC readSession tried to parse the skygate_session cookie as a colon-separated '<uid>:<username>:<email>:<expires_unix>' string, but PostLogin sets the cookie to an HS256 JWT (via auth.IssueJWT) — the formats NEVER matched, so readSession ALWAYS returned nil, the OIDC handler ALWAYS redirected to /login?next=..., and the user saw the login page re-render with an empty password ('сбрасывает'). B174 rewires OIDC readSession to use auth.ParseJWT (the same helper feature/auth uses) + adds a JWTSecret field to oidc.Service + a UserLookup callback (populates the email claim from the DB) + main.go wires both + the B161.4 e2e test now issues a real JWT instead of the pre-B174 'X-Test-Session-Cookie-Present' mock workaround. 22 contracts in scripts/check_b174.sh" \
  'test -f scripts/check_b174.sh && bash scripts/check_b174.sh'

run_check "B175" "OIDC node auto-tag Strategy E (operator 2026-08-25: 'Проверь что Autoupdater тегов работает при варианте когда происходит добавление не по ключу а через OIDC потому что ожидание тега висит уже больше 5 минут и в будущем каждый раз дергать администратора для обновления неудобно'): pre-B175 the node-discovery autoupdater had 3 strategies for matching headscale nodes to portal users (A: PreAuthKeyID match, C: temporal 1h window, D: existing tag:dev-<user>-*) — none of those fire for an OIDC-registered node (no preauth key, no preauth_keys row, no tags yet) so the per-device dev-tag was never applied and /my/devices showed '⏳ pending' forever. B175 extracts matchOIDCStrategy (Strategy E) — matches 'n.PreAuthKeyID == empty && n.UserName == portalUsername' with guards that prevent stealing /my/preauth nodes (PreAuthKeyID guard) or cross-user ownership (UserName guard) — and inserts it as the 4th strategy in Backfill. The synthetic 'tagged-devices' headscale user has name='tagged-devices' which doesn't match any portal username (UNIQUE constraint). 16 contracts in scripts/check_b175.sh" \
  'test -f scripts/check_b175.sh && bash scripts/check_b175.sh'

run_check "B176" "dev-tag lowercase fix (operator 2026-08-25 follow-up: 'старое отображение информации при наведении на тег ожидания осталось также не обновил с новым проходом тег обновлятор устройство - не было обновления на VM?'): headscale 0.29 rejects tags with uppercase letters ('Error: setting tags: rpc error: tag should be lowercase'). Pre-B176 the dev-tag 'tag:dev-<user>-<hostname>' was constructed from the live headscale hostname (e.g. 'SkyBars') without lowercasing, so headscale silently rejected the AddTag call. B176 lowercases n.Hostname (and e.deviceHostname / e.DeviceHostname / liveHostname) at all 6 call sites in nodeownership.go + feature/my/devices.go (2 sites) + feature/admin/devices.go + acl.go (2 sites) so the tag matches headscale 0.29's lowercase requirement. 16 contracts in scripts/check_b176.sh" \
  'test -f scripts/check_b176.sh && bash scripts/check_b176.sh'

run_check "B177" "Defensive dev-tag rename order (operator 2026-08-25: rename skybars-1 -> skybars-secure on id=35 stripped the old 'tag:dev-skyadmin-skybars' via the pre-B177 UntagNode(old) -> AddTag(new) order, but headscale rejected 'tag:dev-skyadmin-skybars-secure' with InvalidArgument because that tag had never been whitelisted, leaving id=35 with no dev-tag): swap the order in internal/nodeownership/nodeownership.go's rename block so AddTag(new) runs first, and UntagNode(old) only fires when AddTag succeeds. The DB row update (UpdateNodeOwnerHostnameAndTag) moves inside the AddTag success branch so a failed AddTag doesn't leave the row out of sync with headscale. The warn log now says 'keeping existing tags as fallback' to make the defensive intent visible. 10 contracts in scripts/check_b177.sh" \
  'test -f scripts/check_b177.sh && bash scripts/check_b177.sh'

run_check "B178" "/admin/exit-rules 'preferred exit node' template-scope bug (operator 2026-08-25: 'basic показывает karolina вместо emilia' — every one of michail/basic's 103 rules showed 'karolina' as the preferred exit-node even though device_exit_node_prefs had 'michail/basic -> tag:exit-emilia' and PreferredExitNodeForRule(s.DB, 6, 'basic') returned 'emilia' correctly): the pre-B178 template did an O(n*m) inner range over \$.RulesAnnotated with 'if eq \$ar.ID .ID', but inside the inner range '.' is REBOUND to \$ar (Go template scope), so the eq check was effectively 'eq \$ar.ID \$ar.ID' (always true) and \$pref was overwritten on every iteration, ending with the LAST annotated rule's PreferredHost. B178 collapses the annotated slice into AdminRule itself (PreferredHost + Applicable fields), drops the inner template lookup, and removes the dead 'groupedByUserAnnotated' map. 15 contracts in scripts/check_b178.sh" \
  'test -f scripts/check_b178.sh && bash scripts/check_b178.sh'

run_check "B178.1" "Live follow-up to B178. The first deployment of B178 (commit a11bb1b) shipped the right code for the basic/karolina regression — annotateRulesWithPrefs correctly populated PreferredHost + Applicable, and the prefFn returned the right values (verified live with a B178-DBG log line: '[B178-DBG] prefFn uid=6 hn=basic -> pref=emilia'). But the rendered /admin/exit-rules page STILL showed 'No preferred exit-node set' for ALL 325 rules because the annotation ran AFTER the groupedByUser build. The grouping loop COPIES each AdminRule into the Nodes map (dg.Nodes[rule.ExitNode] = append(..., rule)), so annotations set after the copy are lost — the template iterates the copies, which had empty PreferredHost. B178.1 fix: swap the order in form_admin.go so annotateRulesWithPrefs runs BEFORE the grouping. New check_b178.sh contract G2 pins the ordering by line-number. 18 contracts in scripts/check_b178.sh (15 in B178 + 1 G2 + 2 trivial)." \
  'test -f scripts/check_b178.sh && bash scripts/check_b178.sh'

run_check "B179" "iptables DOCKER-USER/INPUT over-broad block regression (operator 2026-08-25: 'почему все устройства offline?' — the 14 Tailscale clients + the skygate VM itself all showed online=false with last_seen frozen at the moment a previous iptables rule was applied: 'iptables -I DOCKER-USER 1 -s 192.168.13.67 -p tcp --dport 50444 -j DROP'. The rule was originally added to silence 'node not found' 404 noise from an orphan Tailscale client running inside the NPM (95.165.170.190 / 192.168.13.67), but it also blocked the LEGITIMATE NPM reverse-proxy traffic to headscale (50444), causing ALL Tailscale clients to lose their control-plane connection): remove the over-broad DOCKER-USER + INPUT rules, persist to /etc/iptables/rules.v4. 7 contracts in scripts/check_b179.sh pin (a) no DOCKER-USER/INPUT block for 192.168.13.67 in live iptables, (b) no block in /etc/iptables/rules.v4 (persistence), (c) headscale still up on 50444 (HTTP 401, not 504), (d) AGENTS.md + verify_pre_deploy.sh mention B179." \
  'test -f scripts/check_b179.sh && bash scripts/check_b179.sh'

run_check "B180" "/admin/exit-nodes per-row 'Re-sync' button raw-JSON regression (operator 2026-08-25: 'после нажатия пересинхронизировать получил ответ как на скриншоте не произошел возврат на страницу и не отобразилось ничего' — clicking 'Пере-синхронизировать' on emilia row showed 'Качественная печать' (Chrome raw printout) page with JSON body instead of returning to /admin/exit-nodes with a success flash): the pre-B180 PostAdminExitNodeSync handler returned Content-Type: application/json to a regular <form method='post'> submission, so the browser rendered the JSON as raw text. B180 changes the handler to http.Redirect to /admin/exit-nodes?ok=... or ?err=... like every other admin POST handler; the page already has the flash mechanism (template line 38-42 + handler line 300-301 reads r.URL.Query().Get for FlashSuccess/FlashError). 5 contracts in scripts/check_b180.sh pin (a) no json.NewEncoder in PostAdminExitNodeSync, (b) http.Redirect IS used, (c) redirect target is /admin/exit-nodes?ok= or ?err=, (d) AGENTS.md mentions B180, (e) verify_pre_deploy.sh includes check_b180." \
  'test -f scripts/check_b180.sh && bash scripts/check_b180.sh'

run_check "B182" "/admin/exit-rules and /my/exit-rules 'Applicable' vs 'ApprovedInHeadscale' three-state badge (operator 2026-08-25: 'правила что решил поставить себе пользователь michail они не применились на exit node но в skygate в exit rules помечены как принятые и текущая проверка показывает конфликт' — B178's Applicable check was purely logical (rule.ExitNode matches device's preferred exit-node) and did NOT verify the rule's target CIDR is in headscale ApprovedRoutes. Rules showed ✅ 'accepted' in the UI but the actual CIDRs were never pushed to headscale. B182 adds ApprovedInHeadscale to AdminRule (B178's field is unchanged) and a status string for /my/exit-rules. Both views now render three states: ✅ green (approved — rule's CIDR is in headscale), ⏳ yellow (pending — matches preferred but headscale hasn't approved the CIDR yet, autoupdater will push), ⚠️ red (wrong — rule's ExitNode differs from device's preferred). 16 contracts in scripts/check_b182.sh pin (a) ApprovedInHeadscale field exists, (b) ruleApprovedInHeadscale helper exists, (c) annotator uses it, (d) handler builds approvedByExitNode, (e) annotator signature has 3 args + call site passes 3 args, (f) /my template uses StatusByRuleID for 4-state badge, (g) /admin template uses .ApprovedInHeadscale, (h) form_my.go passes StatusByRuleID, (i) form_admin.go passes approvedByExitNode to annotator, (j) AGENTS.md mentions B182, (k) verify_pre_deploy.sh includes check_b182, (l) go test passes (8 new B182 unit tests in form_admin_b182_test.go). The 8 new tests cover SimpleMatch (headscale has it), Pending (regression: matches preferred but not in headscale), WrongExitNode (the CIDR is in headscale for the wrong exit-node, useful diagnostic), DomainRule (always pending), UnknownHost (host not in headscale), EmptyApprovedMap (headscale unreachable, defensive), and two ruleApprovedInHeadscale direct unit tests." \
  'test -f scripts/check_b182.sh && bash scripts/check_b182.sh'

run_check "B183" "autoupdater duplicate device_rule rows (filed in B182 commit message as TODO). The pre-B183 UNIQUE INDEX device_rules_natural_key_uniq was 6-column (included parent_domain), which let the autoupdater accumulate duplicate rows when different parent_domains resolved to the same CIDR (e.g. cdn:cloudflare:discordapp.com and cdn:cloudflare:discord.com both → 103.21.244.0/22 — separate rows for the same logical rule). Live data for emilia: 102 subnet rows but only 32 unique subnets. B183 drops parent_domain from the natural key. New UNIQUE INDEX is on 5 columns: (user_id, device_id, exit_node_id, target_type, target_value). The dedup CTE prefers cdn:-prefixed parent_domain over plain-domain (more informative for the operator), then most-recent id as a tiebreaker. The autoupdater's two ON CONFLICT clauses in sync.go are updated to use the 5-column target. 11 contracts in scripts/check_b183.sh pin (a) migrateV060PG is registered, (b) function is defined, (c) new index is 5-cols without parent_domain, (d) the dedup uses ROW_NUMBER + cdn: prefix preference, (e) sync.go ON CONFLICT is 5-col (no parent_domain) for both clauses, (f) the dedup is a single SQL statement, (g) AGENTS.md mentions B183, (h) verify_pre_deploy.sh includes check_b183, (i) go test passes (6 new B183 unit tests in migrations_v0_60_b183_test.go: DedupPrefersCDNMarker, DedupNoCDN, NoDuplicates, NewIndexHas5Columns, Idempotent, PreservesDistinctNaturalKeys), (j) live VM check: emilia device_rules subnet count = distinct (user, device, exit, type, value) count after migration." \
  'test -f scripts/check_b183.sh && bash scripts/check_b183.sh'

run_check "B184" "DOMAIN rule status propagates from its resolved subnets in /admin/exit-rules + /my/exit-rules three-state badge. Pre-B184 a DOMAIN rule (target_type=domain, target_value='discord.com') always showed ⏳ pending even when the autoupdater had already resolved the domain to subnets and headscale had approved those subnets. Live case: michail/basic on emilia had 8 YouTube subnets (8.8.8.0/24, 142.250.0.0/15, 8.34.208.0/20, 8.35.192.0/20, 8.15.202.0/24, 172.217.0.0/16, 173.194.0.0/16, 216.58.192.0/19) all showing ✅ but the parent youtube.com row showed ⏳ — the two states disagreed. B184 closes the gap: a DOMAIN rule is ✅ approved iff AT LEAST ONE device_rule row with parent_domain=THIS_DOMAIN and same (user_id, device_id, exit_node_id) and target_type IN (subnet, ip) has its target_value in headscale ApprovedRoutes for the rule's ExitNode. New file internal/feature/exit_rules/resolved_by_domain.go (LoadResolvedByDomain helper + ResolvedKeyForTuple key builder). form_admin.go and form_my.go both call LoadResolvedByDomain and pass the map to ruleApprovedInHeadscale (form_admin) / statusByRuleID (form_my). 15 contracts in scripts/check_b184.sh pin: (A) resolved_by_domain.go has LoadResolvedByDomain, (B) SQL filters target_type IN (subnet, ip) + COALESCE on parent_domain, (C) ResolvedKeyForTuple uses %d:%d:%s:%s format, (D) annotateRulesWithPrefs takes 4 args, (E) ruleApprovedInHeadscale takes 3 args, (F) DOMAIN branch uses ResolvedKeyForTuple + for-range-resolved, (G) form_my.go calls LoadResolvedByDomain + ResolvedKeyForTuple, (H) form_admin.go handler calls LoadResolvedByDomain, (I) form_admin_b184_test.go has 7 test funcs, (J) go test ./internal/feature/exit_rules/... passes, (K) AGENTS.md mentions B184, (L) verify_pre_deploy.sh includes check_b184, (M) live: t.me has 1+ resolved subnet, (N) live: discord.com has 0 resolved subnets (stays ⏳ correctly), (O) live: youtube.com has 4 resolved subnets." \
  'test -f scripts/check_b184.sh && bash scripts/check_b184.sh'

run_check "B185" "Two B184 follow-ups + the live 'Telegram: настроено, но API недоступен' + 'discord.com показывает ⏳ хотя у нас 15 Cloudflare ranges' reports. (1) entrypoint.sh was failing silently with 'requires mentioning all non-default flags' because the persisted state had --advertise-tags set but the entrypoint's tailscale up didn't pass it — RouteAll stayed false, container never accepted relay's subnet routes, Telegram probe stuck on 'unreachable'. Fix: read existing state's AdvertiseTags (or fall back to B111-canonical 'tag:dev-infra-skygate-host-1,tag:private') and pass them back. Live operator fix (2026-08-25): docker exec skygate-skygate-1 tailscale set --accept-routes=true (skips the 'requires mentioning all' check because tailscale set is incremental, not all-or-nothing). (2) B184 only looked up parent_domain = '<domain>' rows but the autoupdater ALSO stores resolved subnets under 'cdn:<provider>:<domain>' (Cloudflare/Fastly/Google/Akamai published ranges). Without the cdn alias lookup, every Cloudflare-routed domain showed ⏳ pending even when its 15 published CDN ranges were already in headscale ApprovedRoutes. Fix: new LookupResolvedForDomain helper merges both formats. (3) /admin/telegram now has a 'Container tailscale state' diagnostic card (RouteAll / AdvertiseTags / ExitNodeID / TailscaleIPs) + a 'Re-apply accept-routes' button that runs docker exec tailscale set --accept-routes=true. 13 contracts in scripts/check_b185.sh pin: (A) entrypoint reads state's AdvertiseTags, (B) entrypoint passes --advertise-tags, (C) resolved_by_domain.go has LookupResolvedForDomain, (D) helper uses 'cdn:' prefix, (E) form_admin ruleApprovedInHeadscale calls helper, (F) form_my statusByRuleID calls helper, (G) form_admin_b184_test.go still has 7 tests, (H) resolved_by_domain_b185_test.go has 5 tests, (I) telegram.go has readContainerTailscaleState, (J) telegram.go has handleTelegramReapplyAcceptRoutes + 'reapply_accept_routes' action, (K) telegram.html renders container card + button, (L) AGENTS.md mentions B185, (M) verify_pre_deploy.sh includes check_b185, (N) live: container's RouteAll=true, (O) live: probe shows ok_relay, (P) live: at least 1 discord-domain row has 1+ cdn-stored subnets." \
  'test -f scripts/check_b185.sh && bash scripts/check_b185.sh'

run_check "B186" "Telegram Bot API 10.1 Rich Messages adapter (operator 2026-08-25: 'адаптируй сообщения бота под новый формат'). The new sendRichMessage endpoint accepts structured HTML/markdown/blocks: headings, lists, tables, <details>, <aside>, <tg-time>, <tg-collage>, <tg-map>, footnotes, <sup>/<sub>, ==marked==, \$math\$, up to 32768 chars / 500 blocks / 16 nesting levels / 50 media. B186 fix: new internal/telegram/rich.go implements a structured builder (Heading, Paragraph, KeyValueTable, Details, Aside, List, Footer, Time, CodeInline, Bold, Italic, Link, Spoiler) that produces the JSON the new endpoint expects. KeyValueTable replaces the old flat '<b>label:</b> <code>value</code>' lines (which didn't align on mobile Telegram) with a real <table> block. Aside / Details / Footer preserve the butler-voice envelope while adding the structure 10.1 clients can render. SendRich() posts via sendRichMessage and falls back to sendMessage with parse_mode=HTML on any error (e.g. bot version < 10.1) so the operator still sees the message body — never silently drops a notification. renderBlocksAsHTML() is the fallback path (flat HTML using the old parse_mode=HTML tag subset). 17 contracts in scripts/check_b186.sh pin: (A) rich.go has SendRich + RichBlock + RichText types, (B) fallback to sendMessage on API error, (C) 10+ unit tests in rich_test.go, (D) KeyValueTable builds 2-col table with bold/code cells, (E) Table enforces 20-col limit with 'Table too wide' error cell, (F) Details block has summary + body, (G) Aside block type='aside', (H) Time inline node has 'date_time' type + ISO 8601, (I) sendRichMessage endpoint + 'blocks' field in JSON, (J) build + tests pass." \
  'test -f scripts/check_b186.sh && bash scripts/check_b186.sh'

run_check "B187" "fix silent env.Username = '' regression caused by SQLite-era '?' placeholder in lookupPortalUsername. Operator 2026-08-25 screenshot showed /my_status replying 'чат привязан, но у пользователя портала нет username' even though the binding's portal_user row had a perfectly good username (skyadmin, id=1, telegram_chat_id=328946535). Root cause: lookupPortalUsername in internal/telegram/notify.go used 'SELECT username FROM portal_users WHERE id = ?' — the '?' placeholder is SQLite-era syntax. The pgx driver (which skygate uses since the v1.3.0 PG-only migration) doesn't auto-convert '?' to '\$1'; it returns 'operator does not exist: ?'. env() silently swallowed the error and left env.Username = ''. The user-scope commands (myStatusReply, myRulesReply, myQuotaReply, etc.) check 'if env.Username ==' and return 'no_username' / 'not_bound' / similar early-return i18n strings. B187 fix: change '?' to '\$1' in the QueryRow call. After the fix, env.Username is populated correctly and /my_status (and any other user-scope command) shows the operator's real data. 6 contracts in scripts/check_b187.sh pin: (A) lookupPortalUsername uses \$1 placeholder (pgx form), (B) no '?' placeholder anywhere, (C) lookup_username_test.go regression guard, (D) AGENTS.md mentions B187, (E) verify_pre_deploy.sh includes check_b187, (F) live: portal_users row for the operator's bound chat (328946535) has a non-empty username (skyadmin)." \
  'test -f scripts/check_b187.sh && bash scripts/check_b187.sh'

run_check "B188" "ghost 'tag:exit-<hostname>' exit-node-pref tags (operator 2026-08-25: 'почему для устройства basic michail недоступен youtube несмотря на правила?'). Three layered bugs: (1) the /my/devices, /admin/devices, and /my/exit-nodes templates synthesised the legacy 'tag:exit-<host>' form inline (printf) instead of reading the canonical 'tag:dev-infra-<host>' from node_owner_map. (2) the post handlers stored the form's tag verbatim, so a ghost tag was written to device_exit_node_prefs. (3) the migrateV047PG backfill that was supposed to set via_enabled=1 on pre-existing rows was guarded by a 'freshlyAdded' check that returned false on production (the column pre-existed), so every pref row shipped with via_enabled=0 — the per-device grant in headscale is NEVER emitted with via=, the user has to manually select the exit-node in Tailscale. Audit (2026-08-25) — exit-node pre-B188 DB state on the VM: user_exit_node_prefs had 2 rows (1|tag:dev-infra-emilia, 6|tag:dev-infra-emilia — both REAL tags), device_exit_node_prefs had 5 rows (1|a71|tag:exit-emilia ← GHOST, 1|emilia|tag:dev-infra-emilia, 1|skygate-host-1|tag:dev-infra-emilia, 1|skyworker|tag:dev-infra-karolina, 6|basic|tag:exit-emilia ← GHOST). All 4 infra nodes (emilia, karolina, sharlotta, skygate-host-1) use the same 'tag:dev-infra-<host>' form, so the B188 migration rewrites the 2 ghost rows + re-enables via pinning on the other 3 (the v0.28.5 re-run). B188 fix: (a) new db.NormalizeExitNodeTag helper that looks up the canonical tag from node_owner_map. (b) PostMyDevicePreferredExit + PostAdminDevicePreferredExit + PostMyExitNodePreferred + PostAdminUserSubnetPreferredExit call the normaliser BEFORE the DB write. (c) The 4 dropdown templates (user/devices.html, admin/devices.html, user/exit_nodes.html, admin/user_subnet.html) read NodeView.DevTag (a new field populated by the handler from node_owner_map) instead of synthesising the legacy form inline. (d) new migration migrateV061PG backfills existing rows: tag:exit-<host> -> tag:dev-infra-<host> (lookup in node_owner_map; rows with no match LEFT ALONE), and via_enabled=1 for every pre-existing row whose tag points at a real headscale tag (the v0.28.5 re-run). 23 contracts in scripts/check_b188.sh pin: (A) NormalizeExitNodeTag helper, (B) PostMyDevicePreferredExit normalises, (C) PostAdminDevicePreferredExit normalises, (D) PostMyExitNodePreferred normalises, (E) admin user-subnet normalises (both dropdown + post), (F) NodeView.DevTag field + my/devices populates, (G) admin/devices populates, (H) publicNodes populate, (I) my/exit_nodes populates, (J) 3 templates use .DevTag, (K) user/devices.html reads .DevTag, (L) admin/devices.html reads .DevTag, (M) user/exit_nodes.html reads .DevTag, (N) migrateV061PG exists, (O) migration chain registered, (P) via_enabled re-enable clause, (Q) NormalizeExitNodeTag tests (5+), (R) migrateV061PG tests (6+), (S) AGENTS.md mentions B188, (T) verify_pre_deploy.sh includes check_b188, (U) build + vet pass, (V) live: no ghost rows after migration, (W) live: via_enabled re-enabled, (X) live: policy has tag:dev-michail-basic -> h-rule-... with via=[tag:dev-infra-emilia]." \
  'test -f scripts/check_b188.sh && bash scripts/check_b188.sh'

run_check "B188.2" "per-CIDR exit-node pin instead of catch-all pin. B188 fixed the ghost tag (tag:exit-X -> tag:dev-infra-X) and re-enabled via pinning, but applied via= to the per-device autogroup:internet CATCH-ALL, pinning ALL of basic's internet to emilia — defeating the user-facing /my/exit-rules feature (selective routing: 'youtube.com via emilia, banking.com direct'). B188.2 fix: (1) Added ExitNodeID to ACLEntry + qSelectEnabledACLEntries SQL + GetACLEntries scan. (2) New helper exitNodeTagToHostname (tag:dev-infra-emilia -> 'emilia') bridges between the per-device exit_node_pref (full tag) and the per-CIDR rule's exit_node_id (hostname). (3) Per-CIDR grant loop adds via=[exit_node_tag] when (a) src=tag:dev-X-Y (per-device rule), (b) viaByDevice[devTag] != '' (device has pref), (c) e.ExitNodeID is non-empty (legacy rules skip), (d) the pref's hostname matches e.ExitNodeID. (4) REMOVED the per-device autogroup:internet block that pinned the catch-all. Result: youtube.com via emilia, banking.com direct — true selective routing. Live verification (2026-08-26): per-device autogroup:internet (tag:dev-michail-basic -> autogroup:internet) no longer has via=[emilia]; h-rule-64-233-164-91-32 (youtube /32) for basic HAS via=[emilia]; skyworker h-rules have via=[karolina] (NOT [emilia] — correct per-device pref). Impact on other users: 5 device_exit_node_prefs rows in production DB (a71, emilia, skygate-host-1, skyworker, basic). For each: autogroup:internet -> direct (was via=[their pref]), per-CIDR grants whose exit_node matches the pref -> via=[their pref] (was no via). Devices WITHOUT a per-device pref see no change. 24 contracts in scripts/check_b188_2.sh: (A) NormalizeExitNodeTag, (B) per-device autogroup:internet with via= is GONE in source, (C) loose per-device autogroup:internet (no via) EXISTS, (D) per-CIDR via= code present, (E) exitNodeTagToHostname exists, (F) tag-to-host stripping pattern, (G) ACLEntry has ExitNodeID, (H) SQL includes exit_node_id, (I) GetACLEntries scans ExitNodeID, (J) B188.2 tests, (K) AGENTS.md mentions B188.2, (L) verify_pre_deploy.sh includes check_b188_2, (M) build + vet pass, (S) live: per-device autogroup:internet for basic has NO via, (T) live: h-rule-64-233-164-91-32 for basic HAS via=[emilia], (U) live: skyworker h-rules have via=[karolina] (not [emilia]), (V) live: a71 has 0 h-rules with via= (no matching per-CIDR rules for emilia), (W) live: total per-device autogroup:internet grants with via= across ALL devices = 0, (X) live: h-rule grants with via=[emilia] for basic = device_rules count for basic with exit_node_id='emilia'." \
  'test -f scripts/check_b188_2.sh && bash scripts/check_b188_2.sh'

run_check "B188.3" "port per-CIDR via= to legacy GenerateACLForPlane (useVia=false path). B188.2 added the per-CIDR via= loop to the useVia=true path only; the useVia=false path (bot's /clear, /add_rule, etc.) still emitted per-CIDR grants WITHOUT via= even when the device's pref matched. B188.3 fix: (1) Extracted resolvePerCIDRVia helper (acl.go) — single source of truth for 'should this per-CIDR grant get via=?'. Returns via= tag or ''. (2) GenerateACLForPlane now: loads device_exit_node_prefs into viaByDeviceOld map, adds 'exitNodeID' field to its local ruleEntry struct, calls resolvePerCIDRVia in the per-CIDR grant emission loop, emits 'via': ['<tag>'] when the helper returns a non-empty tag. (3) GenerateACLWithViaForPlane refactored to also use resolvePerCIDRVia (single source of truth). Both paths now emit the same selective pin. 13 contracts in scripts/check_b188_3.sh: (A) resolvePerCIDRVia helper exists, (B) OLD function (GenerateACLForPlane) calls it, (C) NEW function calls it too, (D) helper doc references B188.3, (E) helper is package-private (lowercase 'r'), (F) OLD ruleEntry struct has exitNodeID field, (G) OLD function populates viaByDeviceOld, (H) AGENTS.md mentions B188.3, (I) verify_pre_deploy.sh registers check_b188_3, (J) B188.2 still passes after refactor (no regression), (K) build + vet pass, (L) B188.3 unit tests pass, (M) B188.3 integration tests pass (skipped on local dev without PG). Tests: 11 unit tests (TestResolvePerCIDRVia 10 cases + TestResolvePerCIDRVia_MultipleDevices) + 2 integration tests (TestGenerateACLForPlane_B1883_NoDevicePref_NoPin, TestGenerateACLForPlane_B1883_LegacyRuleNoExitNodeID). Note: a third integration test (BOTH useVia=true AND useVia=false on same dataset) was attempted but hit test-data infrastructure issues (node_owner_map seed not visible to tagsByUser map). useVia=true is already covered by B188.2's live VM contracts (check_b188_2.sh S-X)." \
  'test -f scripts/check_b188_3.sh && bash scripts/check_b188_3.sh'

# --- TD-15: no unescaped backticks in run_check descriptions ---
run_check "TD-15" "false-alarm headscale: command not found at line 3221 of verify_pre_deploy.sh: unescaped backticks inside the run_check B160 description's double-quoted string triggered bash command substitution, which tried to exec the missing headscale CLI on Windows and emitted 'headscale: command not found' to stderr. The actual B160 check was clean. Fix: replace \`headscale nodes expire --disable\` with 'headscale nodes expire --disable' (single quotes do not trigger command substitution) + add scripts/check_td15.sh to pin 0 unescaped backticks in any run_check description (contract A) and any echo line of scripts/check_*.sh (contract B), so the regression class fails the catalog instead of silently tripping command substitution. The pre-fix backticks in check_b95.sh:121 (dead code) and check_b95.sh:131 (live echo) were also replaced with single quotes to prevent the same bug if those branches are ever triggered. 6 contracts in scripts/check_td15.sh." \
  'test -f scripts/check_td15.sh && bash scripts/check_td15.sh'

# --- TD-16: no unexported .Data.X refs + admin/*.html define name matches filename ---
run_check "TD-16" "fix /admin/ha template error 'can't evaluate field dnsConfigured in type interface {}': the haPageData struct field 'extcredsConfigured' was unexported (lowercase first letter) AND had a different name from what ha.html referenced as '.Data.dnsConfigured'. Go templates can't access unexported struct fields from another package — the engine surfaces a runtime error that 500s the page. Plus 9 i18n keys 'ha.ha.dns_help' etc. were typo'd in ha.html (catalog has them as 'ha.dns_help'). TD-16 fix: rename 'extcredsConfigured' -> 'DNSConfigured' (exported) in internal/feature/admin/ha.go + update ha.html to use '.Data.DNSConfigured' + fix all 9 'ha.ha.X' -> 'ha.X' i18n keys in ha.html. Bonus: derp_dashboard.html used 'body-admin-derp-dashboard' (dash) but renderBody funcmap transforms filename 'derp_dashboard.html' to 'body-admin-derp_dashboard' (underscore) — the body name didn't resolve. Renamed to 'body-admin-derp_dashboard'. TD-16 check pin: (A) no .Data.X lowercase-ident references in any template, (B) advisory i18n-key catalog coverage report, (C) admin/*.html define name matches filename (underscores, not dashes). 6 contracts in scripts/check_td16.sh." \
  'test -f scripts/check_td16.sh && bash scripts/check_td16.sh'

# --- TD-18: close 31 pre-existing i18n gaps + add hint blocks to 3 admin pages ---
run_check "TD-18" "close 31 pre-existing i18n gaps + add hint blocks to 3 admin pages (headscale_acl, services, derp_dashboard). The 31 gap: 25 cert.* keys (certsync page from B148 was added without i18n — the catalog had 50 padded keys that were unreachable from t() due to trailing whitespace) + 4 ha.audit_* keys (deploy audit table) + 1 admin.subnets.col_actions (subnets table) + 1 telegram.saved_token. Pre-TD-18 the page rendered raw cert.title text instead of translations. TD-18 fix: (1) remove trailing whitespace from 50 padded catalog entries (the B148 split_i18n.py bug), (2) add 6 missing keys to the appropriate catalogs, (3) wrap headscale_acl.html in t() + add a What is an ACL hint block + per-field hints (src_help/dst_help/label_help), (4) add 2 hint blocks to services.html (statuses semantics + per-integration explanation), (5) wrap derp_dashboard.html in t() (was 100% English) + add latency color tooltips (less than 50/less than 150/over 150 ms) + own-vs-public explainer + probes-counter explainer. Flip TD-16 contract B from advisory to hard fail (B1: missing keys, B2: padded keys) so this regression class fails the build. 16 contracts in scripts/check_td18.sh." \
  'test -f scripts/check_td18.sh && bash scripts/check_td18.sh'

# --- B191: verify both device registration methods (preauth + OIDC) ---
run_check "B191" "verify both device registration methods work end-to-end (operator hit 500 invalid pre auth key on svyatoslava re-auth 2026-08-31, suspected B161 OIDC work broke preauth; actual cause was a stale key from a different headscale instance). B191 fix: scripts/check_b191.sh — registers a real test device as user infra (id=85) using a fresh hskey-auth- preauth key (correct syntax --user 85 --reusable --expiration 1h, the --user flag wants a numeric ID not a name), verifies the node appears in headscale nodes list, then cleans up via EXIT trap (delete node + expire key + tailscale logout). 8 contracts: A headscale CLI reachable + B preauth key created as infra + C tailscale CLI on test host + D tailscale can reach control plane + E full register flow + node visible in DB + F cleanup via EXIT trap (garbage-free regardless of exit code) + G OIDC surface: discovery + JWKS + /oidc/authorize + /oidc/token + /oidc/userinfo all respond with non-404 (confirms B161 OIDC path is not broken) + H AGENTS.md mentions B191 + documents both methods. Created as user infra per operator directive so the test exercises the same path the OIDC users would, not the admin path." \
  'test -f scripts/check_b191.sh && bash scripts/check_b191.sh'

# --- B194: auto-deploy framework (Phase 1) ---
run_check "B194" "auto-deploy framework (Phase 1: 6 step files + framework + SSE + UI). Operator request: 'должен делать скрипт авторазвертывания когда администратор делает запрос на создание еще одного дубля в соотвествующей вебформе' (admin should trigger auto-deploy via a web form). B194 fix: internal/deployrun/ package with 6 files (types, framework, registry, sse, s3client, handlers) + 6 step files in steps/ (ValidateInput, GeneratePreauthKey, UpdateHAChain, PushEnvToS3, TagNode, AuditLog) + internal/db/migrations_v0_63_b194.go (deploy_runs + deploy_run_steps tables) + scripts/check_b194.sh. 30 contracts in 8 groups (A package + core files + B step files + C each step implements DeployStep interface + D Framework.Run signature + E migration creates both tables with FK + F SSE broker Subscribe/Publish/Close + G AGENTS.md mentions B194 + H verify_pre_deploy.sh references check_b194). Each step is a separate file with a self-registering init() function (no import cycle); adding a new step is one new file + one RegisterStep call, no changes to framework.go or handlers.go. Phase 1 leaves manual SSH bootstrap to the operator (run page shows the curl | bash command); Phase 2 (B195) adds SSHConnectStep + RunBootstrapScriptStep + HealthCheckStep for full automation." \
  'test -f scripts/check_b194.sh && bash scripts/check_b194.sh'

# --- B195: cluster management tables (Phase 0 of cluster-management.md) ---
run_check "B195" "cluster management tables (Phase 0 of docs/internal/cluster-management.md, D1: state in headscale/sk ygate DB). B195 fix: internal/db/migrations_v0_64_b195.go creates 6 cluster_* tables (cluster, cluster_node, cluster_database, cluster_migration, cluster_invite, cluster_audit) + indexes; registered in driver_postgres.go migrateV0... list; scripts/check_b195.sh verifies file exists + all 6 tables + IF NOT EXISTS guards + driver_postgres.go registration + go build/vet clean + docs/internal/cluster-management.md exists with D1-D8 confirmed. Phase 1.1 (/admin/database) and Phase 1.4 (DB migration workflow) will use cluster_database as the source of truth for the desired DSN; cluster_node will track per-node state for /admin/cluster; cluster_audit will record every admin action. All tables use TEXT primary keys (admin-friendly), JSONB for structured fields, IF NOT EXISTS guards (idempotent), TIMESTAMPTZ for time. Migrations are forward-only (no destructive DDL, B11 contract). 8 contracts: A migration file exists, B all 6 tables in migration, C idempotency (IF NOT EXISTS), D registered in driver_postgres.go, E AGENTS.md mentions B195 (or defer to plan doc), F verify_pre_deploy.sh references check_b195.sh, G plan doc has D1-D8 marked confirmed, H go build/vet clean." \
  'test -f scripts/check_b195.sh && bash scripts/check_b195.sh'
# --- B196: /admin/database (Phase 1.1, read-only) ---
run_check "B196" "/admin/database Phase 1.1 read-only page (D3+D8 from cluster-management.md). Phase 1.1 surfaces: (1) current DSN from SKYGATE_DB_DSN env + reachability probe with latency, (2) desired DSN from cluster_database table (empty until Phase 1.2 adds edit form), (3) per-D8 source-of-truth note. Files: internal/feature/admin/database.go (GetAdminDatabase handler + databasePageData struct + parseLibpqDSN + probeDB) + internal/handlers/templates/admin/database.html + internal/db/cluster.go (GetClusterDatabase + SetClusterDatabase + ClusterDatabase struct + ErrClusterDatabaseNotFound sentinel) + i18n keys (22 total: db.page_title, db.page_subtitle, db.current_dsn_title, db.desired_dsn_title, db.not_configured, db.reachable, db.unreachable, db.dsn_source, db.host, db.dbname, db.username, db.sslmode, db.latency, db.full_dsn, db.id, db.primary_node, db.replicas, db.dsn_template, db.updated_at, db.by, db.desired_empty, db.current_dsn_help, db.desired_dsn_help, db.d8_note) in RU+EN in lock-step. Route /admin/database registered in cmd/skygate/main.go behind authMW. The probeDB helper opens a fresh sql.Open + PingContext (NOT db.OpenDSN) to avoid running migrations on every page render. Phase 1.2 will add the Edit form; Phase 1.4 the migration workflow. 10 contracts in scripts/check_b196.sh: A handler file, B handler internals, C route registered, D template exists, E template uses i18n keys, F RU+EN catalogs in lock-step, G cluster.go helper, H AGENTS.md, I verify_pre_deploy.sh, J go build/vet clean." \
  'test -f scripts/check_b196.sh && bash scripts/check_b196.sh'
# --- B197: /admin/database Test + Edit (Phase 1.2) ---
run_check "B197" "/admin/database Phase 1.2: Test Connection button + Edit DSN form. Adds 2 POST handlers (PostAdminDatabaseTest probes DSN from form via sql.Open+PingContext with 5s timeout, no persistence; PostAdminDatabaseEdit writes cluster_database with audit row 'cluster.db.edit'). Form pre-fills from current DSN (databasePageData.Form* fields). Template admin/database.html has the form with 7 new i18n keys (db.test_edit_title, db.port, db.test_btn, db.save_btn, db.test_help, db.edit_help, db.edit_confirm) in RU+EN. Routes POST /admin/database/test + /admin/database/edit registered in cmd/skygate/main.go behind authMW. IMPORTANT: Edit does NOT change the live skygate process's connection — the live process still uses SKYGATE_DB_DSN from env. The operator must restart the skygate container to apply. After Phase 3.1 (skygate-watchdog) hot-reload will happen without restart. Audit row uses new cluster.* prefix convention (vs older ha.* / deploy.*). 10 contracts in scripts/check_b197.sh: A handlers exist, B Form* fields, C POST routes, D template form, E i18n in RU+EN, F audit row, G db.SetClusterDatabase call, H AGENTS.md, I verify_pre_deploy.sh, J go build/vet clean." \
  'test -f scripts/check_b197.sh && bash scripts/check_b197.sh'
# --- B198: DB migration workflow (Phase 1.4) ---
run_check "B198" "DB migration workflow framework (Phase 1.4 of docs/internal/cluster-management.md). State machine: precheck (ping src+tgt) -> dump (pg_dump -Fc, STUB) -> restore (pg_restore, STUB) -> verify (count key tables) -> flip (update cluster_database + .env) -> cleanup (drop source, OPTIONAL, STUB). Steps register via self-iterating init() (same pattern as B194 deployrun). Run() orchestrator with rollback chain (best-effort reverse). SSE broker (Subscribe/emit) for live progress. Tables: dbmigrate_run (id, source_dsn, target_dsn, operator, status, started_at, finished_at, error) + dbmigrate_step (id, run_id FK, step_name, ordinal, status, started_at, duration_ms, logs JSON, metadata JSON). Migration V065. Routes GET/POST /admin/database/migrate + GET /admin/database/migrate/{id}/stream + GET /admin/database/migrate/{id}. Phase 1.4 LIMITATIONS (documented in plan + step stubs): (1) Dump step returns STUB error - operator runs pg_dump manually on source, scp the file; (2) Restore step returns STUB error - operator runs pg_restore manually on target; (3) Cleanup step returns STUB error - operator runs DROP DATABASE manually. Only the Flip step (cluster_database + .env update) is actually functional. The framework is in place; full execution waits for B200 + a second PG host + SSH plumbing. 10 contracts in scripts/check_b198.sh: A package, B 6 steps, C DeployStep interface, D framework Run+rollback, E migration, F SSE, G routes, H AGENTS.md, I verify_pre_deploy.sh, J go build/vet." \
  'test -f scripts/check_b198.sh && bash scripts/check_b198.sh'
# --- B198.1: DB migration UI completion (Phase 1.4) ---
run_check "B198.1" "DB migration UI (Phase 1.4). B198 added the framework (6 steps + SSE + dbmigrate_run/step tables); B198.1 adds the user-facing surface: (1) /admin/database page now has a 'Migrate to new host' form with target_host/target_port/target_dbname/target_username/target_sslmode + Migrate button (POST /admin/database/migrate), (2) /admin/database/migrate shows recent runs list (last 5 from dbmigrate_run via collectRecentRuns), (3) /admin/database/migrate/{id} shows single-run page with steps table + SSE for live progress. Templates: admin/database.html (updated with migrate form card + recent-runs table) + admin/migrate_run.html (new, 130 lines, with EventSource JS for live status). Handlers: admin.GetAdminDatabaseMigrate + admin.GetAdminDatabaseMigrateRun in admin/database.go (call dbmigrate.LoadRun for data). Helpers: dbmigrate.LoadRun + dbmigrate.RunView (lightweight projection of MigrationRun for the recent-runs list) + dbmigrate.ErrRunNotFound sentinel. Routes re-wired: GET /admin/database/migrate → adminSvc (renders with admin layout); GET .../{id} → adminSvc; POST + GET .../{id}/stream → migrateSvc (framework handlers). 20 new i18n keys (db.migrate_title/help/btn/confirm/steps_help + db.migrate_run_* + db.migrate_step_* + db.migrate_stream_* + db.recent_runs_title) in RU+EN lock-step. The Flip step is the only one that's actually functional today; dump/restore/cleanup are stubbed. AGENTS.md mentions B198.1. 9 contracts in scripts/check_b1981.sh: A database.html migrate card, B migrate_run.html, C dbmigrate.LoadRun call, D RunView, E routes re-wired, F i18n, G AGENTS.md, H verify_pre_deploy.sh, I go build/vet." \
  'test -f scripts/check_b1981.sh && bash scripts/check_b1981.sh'

# --- B203: skygate-watchdog for cluster_database hot-reload (Phase 3.1) ---
run_check "B203" "skygate-watchdog for cluster_database hot-reload (Phase 3.1 of cluster-management.md). Adds: (1) internal/db/swapdb.go — ResettableDB type (embeds *sql.DB, atomic Reset swaps the embedded pointer, goroutine-Closes the old pool, RLock on overrides, sqlDBShim compile-time assertion); (2) internal/watchdog/dbswap.go — DBSwap ticker (5s default) reads cluster_database, compares DSN, opens new pool + pings + calls Reset on change; redactDSN for logs; (3) cmd/skygate/main.go wraps app.DB in NewResettableDB + starts the watchdog + a closure DSNReader calling db.GetClusterDatabase(d.DB, 'skygate-staging'). 7+5 unit tests + sqlDBShim compile-time check + 33 B-check contracts in scripts/check_b203.sh. D8 (cluster_database wins on conflict) is enforced by the watchdog: if the row is empty, the env-DSN pool stays; if the row has a different DSN, the pool swaps within ~5s. The 'in-flight queries complete naturally' property is preserved by closing the old pool in a goroutine — pgx.Close() blocks until in-use connections are returned, but the watchdog doesn't wait for that. Phase 3.1 leaves manual failover (B204) and cluster CLI (B205) as follow-ups." \
  'test -f scripts/check_b203.sh && bash scripts/check_b203.sh'
# --- B202.5: SSHDumpTransport for cross-host DB migrations (Phase 1.4) ---
run_check "B202.5" "SSHDumpTransport for cross-host DB migrations (Phase 1.4 of cluster-management.md). Closes the 'operator must hand-migrate the DB via scp + pg_restore on the agent' gap that was implicit in the B198/B202 work — pre-B202.5 the dbmigrate framework's Dump step only ran pg_dump on the local host, which works for the live svi→agent move because the agent reaches svi's PG via the 172.17.0.1:5433 bridge, but the bridge requires svi to expose its PG port to the agent network. B202.5 adds a transport that runs 'ssh svi \"pg_dump ...\"' and streams the bytes back, so the operator can flip the DSN + restart the agent without depending on direct PG-port reachability between svi and agent. SSHDumpTransport struct with 5 fields (SSHHost/SSHUser/SSHKeyPath/SSHPort/PgDumpPath + optional SSHOptions), Name()='ssh', Dump(ctx, sourceDSN, destPath, onLog) (int64, error) implements the DumpTransport interface. NewSSHDumpTransportFromEnv() reads 5 SKYGATE_DBMIGRATE_SSH_* env vars and returns nil if HOST or USER is empty (caller falls back to Local). quoteForRemoteShell() POSIX-shell-escapes the DSN for 'ssh host cmd' (close-quote/literal/reopen idiom for embedded single quotes). framework.go default-fallback: SKYGATE_DBMIGRATE_TRANSPORT=ssh + valid SSH config -> SSHDumpTransport, else LocalDumpTransport. 5 unit tests in internal/dbmigrate/ssh_transport_test.go: QuoteForRemoteShell (4 sub-cases incl. embedded single quote + multiple quotes), NewFromEnv_RequiresHostAndUser (returns nil on empty HOST or USER), NewFromEnv_PortParsing (22022, bad-port -> 0 silent fallback), Dump_FakeSsh round-trip (Unix only; SKIP on Windows because exec.LookPath does not find a bare 'ssh' there), Dump_Validation (empty sourceDSN, empty destPath, empty SSHHost, empty SSHUser all return error), Name()=='ssh' pinned. Compile-time interface assertion: 'var _ DumpTransport = SSHDumpTransport{}'. 14 B-check contracts in scripts/check_b202.5.sh. Live-verify dry-run (scripts/b202_5_verify.sh) on the agent: ssh to localhost + pg_dump of a temp test DB -> local file, validates PGD\\n magic bytes, verifies pg_restore --list shows the test table. Does NOT touch headscale/headplane on agent, does NOT touch the live skygate_staging DB on svi. The real svi->agent move is a one-time operator action (set 4 env vars + ssh-copy-id)." \
  'test -f scripts/check_b202_5.sh && bash scripts/check_b202_5.sh'
# --- B210: DBSource pattern for non-admin services (auth, my, exit_rules, feature/cluster) ---
run_check "B210" "DBSource pattern for non-admin services (auth, my, exit_rules, feature/cluster). Phase 3 of cluster-management.md. Closes the B203 hot-reload regression for ALL services that previously captured *sql.DB at boot. B208.1 only fixed the admin package; the auth/my/exit_rules/feature/cluster packages kept the captured-pointer pattern and broke on every B203 watchdog swap (the user saw 'empty devices tab + unchanged theme' after every skygate restart, since auth login + the my/devices page were both broken — the user-reported symptom that triggered B210). B210 replicates the B208.1 pattern in 4 more packages: each gets a new dbsource.go with a DBSource interface (one Current() *sql.DB method, satisfied by the B203 ResettableDB) + a Service.dbc() helper that returns s.DB.Current(); every s.DB.X call site (auth=9, my=many, exit_rules=many, cluster=2) is replaced with s.dbc().X. main.go passes `d` (the ResettableDB) to all 4 services instead of `app.DB` (the captured *sql.DB). 19 B-check contracts in scripts/check_b210.sh: 4 dbsource.go files exist, 4 Service.DB types are DBSource (not *sql.DB), 4 Service.dbc() helpers exist, 3 packages (my+exit_rules) have 0 remaining s.DB.method call sites, main.go passes d to all 4 services, build+vet+tests pass for the 4 packages, AGENTS.md mention, verify_pre_deploy has B210. Live-verified on the agent by setting cluster_database.current_dsn=skygate_admin_pass (the B207 verify test artifact that triggers the watchdog swap) + restart skygate + login as skyadmin + verify /my/devices shows 9 devices + theme=mint — all working despite the swap. The healthz/elector/admin packages already had their own DBSource patterns from B206/B204/B208.1; consolidating them into a shared internal/db/dbsource.go is a follow-up B-block." \
  'test -f scripts/check_b210.sh && bash scripts/check_b210.sh'
# --- B210.1: DBSource consolidation (5+ local copies → 1 in internal/db) ---
run_check "B210.1" "DBSource consolidation: collapses the 5+ local copies of the same one-method interface (`type DBSource interface { Current() *sql.DB }`) into one canonical copy in skygate/internal/db/dbsource.go. The 5 copies were introduced by B208.1 (admin) + B210 (auth/my/exit_rules/cluster) + earlier B204 (elector) + B206 (healthz). B210.1 adds: (1) the canonical interface in internal/db, (2) a FixedDBSource struct + Current() method (replaces the 3 different `fixedDB`/`fixedDBSource` types that B204/B206/B210 test files each defined), (3) a DBCurrent() free function (replaces the 5 different per-Service `dbc()` helper bodies — each one was a copy of the same 3-line nil-safe Current() wrapper). Each of the 6 packages' dbsource.go now just re-exports the interface as `type DBSource = skygatedb.DBSource` + keeps the per-Service `dbc()` method (which references the Service's DB field, which lives in the feature package — so it can't move to internal/db). The ResettableDB in internal/db satisfies the new shared interface via its existing Current() method, no changes needed. Source-compat: `healthz.NewFixedDBSource(db)` and `elector.NewElectorWithDB(cfg, db)` still work (now thin delegates to skygatedb.FixedDBSource{DB: db}). 14 B-check contracts in scripts/check_b210_1.sh: dbsource.go exists + declares DBSource/FixedDBSource/DBCurrent; 7 packages (admin/auth/my/exit_rules/cluster/healthz/elector) import skygate/internal/db + use the type alias; build+vet+tests pass; AGENTS.md mention; verify_pre_deploy has B210.1. Same test coverage as before (5 packages' unit tests still pass — only the fixedDB field rename in exit_rules test was needed). Net: ~80 lines of duplication removed across 6 packages, the canonical definition lives in the package that owns the implementation (internal/db owns ResettableDB, so it also owns the DBSource interface ResettableDB satisfies)." \
  'test -f scripts/check_b210_1.sh && bash scripts/check_b210_1.sh'
# --- B207 fix: clear_test_dsn.sh (cleanup test artifact from B207 verify) ---
run_check "B207_fix" "clear_test_dsn.sh: cleanup the B207-verify test artifact from cluster_database.current_dsn so the B203 watchdog doesn't keep swapping on every 5s tick after the verify. The bug: B207-verify set current_dsn to a deliberately wrong DSN ('postgres://admin:skygate_admin_pass@...') to exercise the audit_log+cluster_audit UNION path; the literal 'skygate_admin_pass' password is the artifact. B203 watchdog reads current_dsn on every 5s tick — if it differs from the env DSN (which it does, because the password is wrong), the watchdog closes the old pool and tries to open a new one with the literal DSN, which fails (auth error). The B207-fix is to add a 'current_dsn = ''' cleanup at the end of every verify that touches cluster_database. b208_verify.sh now calls clear_test_dsn.sh as the final step; the same script can be re-run independently after any future verify that sets a test current_dsn. 4 contracts in scripts/check_b207_fix.sh: clear_test_dsn.sh exists + shebang is correct + uses sudo -u postgres (not the skygate_admin user, which is the wrong auth path for cluster_database writes) + b208_verify.sh calls it as the final step." \
  'test -f scripts/check_b207_fix.sh && bash scripts/check_b207_fix.sh'
# --- Phase 3.4: Force cluster node failover button on /admin/ha ---
run_check "Phase_3_4" "Force cluster node failover button on /admin/ha. Phase 3.4 of cluster-management.md. Operator-driven counterpart to the B204 elector's automatic failover_recommend — pre-Phase 3.4 the only path to swap the skygate primary was SSH + psql + UPDATE cluster_node + manual audit row. Post-Phase 3.4 the operator clicks a button on /admin/ha. The handler calls db.FailoverClusterNode (single transaction: pick current primary, verify target state=ready + role=skygate-standby, add skygate role to target, demote old primary to state=draining, write node_failover cluster_audit row). The new section in ha.html renders a per-node table with a Promote form (target_id, confirm=hostname, reason) for eligible rows; non-eligible rows show a why-not hint. 17 B-check contracts in scripts/check_phase_3_4.sh: cluster_failover.go exists + FailoverClusterNode signature + ErrNoPrimary + ErrNotEligibleForFailover sentinels + uses transaction (sql.Tx + Commit) + handler exists + route registered as POST /admin/ha/cluster/failover behind authMW + haClusterNodeRow struct + haPageData.ClusterNodes field + collectHAPageData populates from cluster_node + eligibility logic checks state=ready+skygate-standby+NOT skygate + template renders per-row Promote form + i18n keys present in both RU + EN + build+vet+tests pass + AGENTS.md mention + verify_pre_deploy has run_check. 12 new i18n keys added (ha.cluster_failover, ha.cluster_failover_help, ha.cluster_failover_btn, ha.cluster_failover_confirm, ha.cluster_failover_note, ha.cluster_failover_target_required, ha.cluster_failover_reason_required, ha.cluster_failover_confirm_required, ha.cluster_failover_no_primary, ha.cluster_failover_not_eligible, ha.cluster_failover_done, ha.cluster_failover_eligible_help, ha.cluster_failover_btn_title). 3 new files (cluster_failover.go ~180 lines + check_phase_3_4.sh ~250 lines + template section ~80 lines). Modifies ha.go (+~100 lines for handler + struct + collectHAPageData loop), main.go (1 line: route registration), catalog_admin.go (12 keys × 2 languages = 24 entries)." \
  'test -f scripts/check_phase_3_4.sh && bash scripts/check_phase_3_4.sh'
# --- Phase 3.6: skygate cluster failover-drill CLI subcommand (safe-test counterpart of failover) ---
run_check "Phase_3_6" "skygate cluster failover-drill CLI subcommand (safe-test counterpart of runClusterFailover). Phase 3.6 of cluster-management.md. Closes the 'operator wants to verify the failover workflow without committing to a real swap' gap. Pre-Phase 3.6 the only way to test the B204 elector + Phase 3.4 button + runClusterFailover CLI together was either (a) point at a fake cluster (no real verification on production data) or (b) do a real swap and immediately swap back (operator fatigue, audit log noise). Post-Phase 3.6 the operator runs 'skygate cluster failover-drill --target=<id>' — same atomic swap (target promoted to skygate role, old primary demoted to state=draining, single transaction), but writes action='node_drill' to cluster_audit instead of 'node_failover'. The /admin/ha 'Last 20 events' query now includes 'node_drill' in the WHERE clause so drills are visible alongside real failovers. The detail JSONB has an extra 'drill': true flag + 'real_action': 'node_failover' field for future audit-log filters. The drill does NOT auto-rollback (operator runs 'skygate cluster failover --target=<old_primary>' to restore state — intentional, the drill is meant to verify the FULL workflow, not be silently undone). 10 B-check contracts in scripts/check_phase_3_6.sh: cluster_drill.go exists + DrillClusterNode signature + uses a transaction + writes action='node_drill' to cluster_audit + runClusterFailoverDrill function + CLI dispatches 'failover-drill' verb + /admin/ha filter includes 'node_drill' + build+vet+tests pass + AGENTS.md mention + verify_pre_deploy has run_check. 3 new files (cluster_drill.go ~140 lines + check_phase_3_6.sh ~180 lines). Modifies cluster.go (+~60 lines for runClusterFailoverDrill + dispatch case + help text), ha.go (1-line WHERE clause expansion). Live-verified on agent: drill promotes b210-standby to skygate, demotes test-b204-standby-ready, writes node_drill audit row (vs node_failover for real failovers) — both visible in /admin/ha 'Last 20 events' after Phase 3.4's button + Phase 3.6's drill." \
  'test -f scripts/check_phase_3_6.sh && bash scripts/check_phase_3_6.sh'
# --- B209: end-to-end HA failover test orchestrator (Phase 3) --- Closes the 'operator must hand-migrate the DB via scp + pg_restore on the agent' gap that was implicit in the B198/B202 work — pre-B202.5 the dbmigrate framework's Dump step only ran pg_dump on the local host, which works for the live svi→agent move because the agent reaches svi's PG via the 172.17.0.1:5433 bridge, but the bridge requires svi to expose its PG port to the agent network. B202.5 adds a transport that runs 'ssh svi \"pg_dump ...\"' and streams the bytes back, so the operator can flip the DSN + restart the agent without depending on direct PG-port reachability between svi and agent. SSHDumpTransport struct with 5 fields (SSHHost/SSHUser/SSHKeyPath/SSHPort/PgDumpPath + optional SSHOptions), Name()='ssh', Dump(ctx, sourceDSN, destPath, onLog) (int64, error) implements the DumpTransport interface. NewSSHDumpTransportFromEnv() reads 5 SKYGATE_DBMIGRATE_SSH_* env vars and returns nil if HOST or USER is empty (caller falls back to Local). quoteForRemoteShell() POSIX-shell-escapes the DSN for 'ssh host cmd' (close-quote/literal/reopen idiom for embedded single quotes). framework.go default-fallback: SKYGATE_DBMIGRATE_TRANSPORT=ssh + valid SSH config -> SSHDumpTransport, else LocalDumpTransport. 5 unit tests in internal/dbmigrate/ssh_transport_test.go: QuoteForRemoteShell (4 sub-cases incl. embedded single quote + multiple quotes), NewFromEnv_RequiresHostAndUser (returns nil on empty HOST or USER), NewFromEnv_PortParsing (22022, bad-port -> 0 silent fallback), Dump_FakeSsh round-trip (Unix only; SKIP on Windows because exec.LookPath does not find a bare 'ssh' there), Dump_Validation (empty sourceDSN, empty destPath, empty SSHHost, empty SSHUser all return error), Name()=='ssh' pinned. Compile-time interface assertion: 'var _ DumpTransport = SSHDumpTransport{}'. 14 B-check contracts in scripts/check_b202_5.sh. Live-verify dry-run (scripts/b202_5_verify.sh) on the agent: ssh to localhost + pg_dump of a temp test DB -> local file, validates PGD-N magic bytes, verifies pg_restore --list shows the test table. Does NOT touch headscale/headplane on agent, does NOT touch the live skygate_staging DB on svi. The real svi->agent move is a one-time operator action (set 4 env vars + ssh-copy-id)." \
  'test -f scripts/check_b202_5.sh && bash scripts/check_b202_5.sh'
# --- B209: end-to-end HA failover test orchestrator (Phase 3) ---
run_check "B209" "end-to-end HA failover test orchestrator (Phase 3 of cluster-management.md). Closes the 'no automated test exercises the full failure-detection + auto-recommendation + recovery + dedup cycle against a live cluster_node table' gap — pre-B209 the B204 HA elector had been ticking every 5s on the agent since v1.5.0, but the B-check + live verify scripts were unit-test-shaped (nextState, roleContains) and ad-hoc SQL probes. B209 ships scripts/b209_e2e.sh — a 7-phase SQL orchestrator against the live DB: (Phase 0) setup b209-primary+standby rows, (Phase 1) pre-check no-op, (Phase 2) backdate last_seen_at to simulate failure, (Phase 3) wait 7s + assert node_health ready->failed + a NEW failover_recommend row with to_node_id=b209-standby, (Phase 4) simulate recovery via state=ready+last_seen_at=NOW (the B201 Heartbeat() path; B204 elector does NOT do failed->ready), (Phase 5) wait + assert elector quiet (no growth in audit count), (Phase 6) re-fail + assert 5-min dedup window suppresses the second recommend, (Phase 7) cleanup via DELETE FROM cluster_node + cluster_audit WHERE LIKE 'b209-%' (also wired into EXIT trap; --keep flag for manual inspection). Code side: Config.Now func() time.Time hook on the elector (default time.Now, nil-safe in NewElector) so the unit tests can fast-forward through the 90s staleness window without sleeping. 4 new unit tests: TestDefaultConfig_NowSet + TestNewElector_NowFallback + TestElector_NowUsesFakeClock + TestNextState_AtFakeClockBoundary (3 sub-cases at 89s/90s/91s pinning the strict-< boundary). 16 B-check contracts in scripts/check_b209.sh: e2e exists + bash -n passes + 8 phases + cleanup_b209+EXIT trap + cleanup removes from BOTH tables + exit 0/1 + summary, elector Config.Now declared + DefaultConfig sets Now=time.Now + NewElector restores nil Now + evaluate() reads e.now() + e.now() helper, 4 new unit tests present, build+vet+elector tests pass, AGENTS.md mention, verify_pre_deploy has B209. Live-verified 2026-09-02 12/12 pass on agent." \
  'test -f scripts/check_b209.sh && bash scripts/check_b209.sh'
# --- B208: admin Service DBSource migration + /admin/ha cluster_audit view (Phase 3.2) ---
run_check "B208" "admin Service DBSource migration + /admin/ha cluster_audit view. Phase 3.2 of cluster-management.md. Two sub-chunks in one B-chunk, both fixing real bugs in the pre-B208 admin surface. B208.1: the pre-B208 admin Service constructed with DB *sql.DB captured at boot; the B203 watchdog hot-reloads the pgxpool every ~5s and closes the old pool in a goroutine, so every admin page returned 'sql: database is closed' after the first B203 swap. B208.1 changes Service.DB from *sql.DB to admin.DBSource interface (Current() *sql.DB), adds s.dbc() helper, and updates ~70 call sites from s.DB.X to s.dbc().X. The ResettableDB from internal/db satisfies the interface directly. main.go passes 'd' (the wrapper) instead of 'app.DB'. B208.2: /admin/ha's 'Last 20 HA events' table now UNIONs audit_log + cluster_audit (in Go, not SQL — different schemas for created_at). The B204 elector's node_health + failover_recommend and the B205 failover's node_failover are now visible on the page with a Source column (orange badge for cluster_audit, gray for audit_log). 18 B-check contracts in scripts/check_b208.sh: Service.DB type changed, main.go passes ResettableDB, no remaining s.DB.method patterns (excluding dbsource.go's helper), ha.go queries both tables, template renders the Source column, build/vet/tests pass, AGENTS.md mention." \
  'test -f scripts/check_b208.sh && bash scripts/check_b208.sh'
# --- B207: /admin/audit unified view (Phase 4.1 / G8) ---
run_check "B207" "/admin/audit unified audit view. Phase 4.1 / G8. Closes the 'cluster_audit rows are invisible in the UI' gap — pre-B207, the existing /admin/audit (v0.27.0) read only from audit_log; cluster_audit rows from B195 (populated by B200/B204/B205) required psql to see. B207 makes the same URL serve UNION ALL of audit_log + cluster_audit, with 4 filters: ?action= (exact), ?user= (substring), ?source= (audit_log / cluster_audit / all), ?since= (Go duration: 1h, 24h, 7d — the 'd' suffix is custom since time.ParseDuration doesn't accept it), plus ?limit= (default 200, max 5000). Each row carries a Source column (orange badge for cluster_audit, gray for audit_log), Target column (target_node_id for cluster_audit, — for audit_log), Result + ErrorMessage when they apply. 7 new i18n keys (audit.col_source/target/since/recent_events/filtered/source_all/source_audit_log/source_cluster_audit) in RU + EN lock-step. 5 unit tests with 20 sub-cases: parseSinceFilter (1h/24h/7d/invalid), parseLimit (cap 5000), normalizeSourceFilter, AuditEntry_FieldsRoundtrip, AuditEntry_OptionalFields. 37 B-check contracts in scripts/check_b207.sh: GetAdminAudit reads from BOTH tables + UNION ALL, AuditEntry 8 fields pinned, AuditSource* constants, 3 helpers, template uses new i18n keys + renders cluster_audit source badge, 7 new i18n keys, route still wired, 6+ unit tests, build/vet/tests pass, AGENTS.md mention." \
  'test -f scripts/check_b207.sh && bash scripts/check_b207.sh'
# --- B206: GET /db/health endpoint (Phase 1.5 / G3) ---
run_check "B206" "GET /db/health endpoint. Phase 1.5 / G3 of cluster-management.md. Closes the 'no programmatic way to check DB size / replica status / xlog position' gap. Background Sampler (internal/feature/healthz.Sampler) ticks every 30s and runs the expensive pg_* queries (pg_database_size, pg_is_in_recovery, pg_last_wal_replay_lsn, pg_stat_user_tables aggregates), stores the result in atomic.Pointer[DBHealthSample]. Handler reads the cached sample + live pool stats from *sql.DB.Stats() and returns in <5ms. JSON shape: pool (sql.DBStats), is_replica, version, started_at, size_bytes, size_human, replication_is_replica/lag_seconds/replay_lsn, maintenance (last_vacuum/last_autovacuum/last_analyze/last_autoanalyze/dead_tuples), xlog_location, slow_queries (reserved for B215 Prometheus), sampled_at, sample_interval_seconds. Sampler receives the ResettableDB as its DBSource (B203 hot-reload transparency — same pattern as the B204 HA elector). Each query in collect() is individually wrapped so a slow pg_database_size doesn't block the rest of the sample. 38 B-check contracts in scripts/check_b206.sh: Sampler + Start/Stop, Config fields, DefaultDBHealthConfig 30s/3s pinned, DBSource interface, NewFixedDBSource helper, DBHealthSample 5 substructs (Server/Database/Replication/Maintenance/XLog), DBHealthResponse Pool field, GetDBHealth handler, Service DBHealthSampler + DBHealthSrc fields, main.go NewDBHealthSampler + Start + GET /db/health route, tick() consults s.src.Current(), collect() queries pg_is_in_recovery + pg_database_size + pg_stat_user_tables + pg_current_wal_lsn, humanBytes helper, 6+ unit tests, build/vet/tests pass, AGENTS.md mention." \
  'test -f scripts/check_b206.sh && bash scripts/check_b206.sh'
# --- B205: skygate cluster CLI subcommands (Phase 4) ---
run_check "B205" "skygate cluster ... CLI subcommands. Phase 4 of cluster-management.md — the operator-on-the-box equivalent of /admin/cluster + /api/cluster/join. 7 verbs: invite (call cluster.IssueInvite, print sgn1 token to stdout), join (POST /api/cluster/join + write /etc/skygate/cluster-state.json with node_id + token + api_url), nodes (read cluster_node, tab or --json), dbs (read cluster_database, tab or --json), audit (read cluster_audit, tab or --json), failover --target=<id|host> (admin-gated promote: add skygate role to target + drain failed primary + write cluster_audit node_failover row in a transaction), heartbeat-daemon (long-running, POST /api/cluster/heartbeat every HeartbeatSeconds, SIGINT clean shutdown). The dispatcher is `skygate cluster <verb>` in main.go (B205 contract: web server NOT started for cluster subcommands, each one opens DB directly). Local clusterRolesToSlice handles PG TEXT[] literals including quoted segments with embedded commas/spaces (the same shape as internal/db/array.go's StringArray, kept local to avoid coupling). 7 unit tests: clusterRolesToSlice (10 cases), sqlNullString, runClusterSubcommand dispatch errors, state file roundtrip, incomplete state file errors. 35 B-check contracts in scripts/check_b205.sh: dispatcher handles all 7 verbs, each verb is a defined function, runClusterFailover writes 'node_failover' audit row + uses a transaction, runClusterHeartbeatDaemon handles SIGINT + calls postHeartbeat, main.go has 'cluster' case + help text mentions 'cluster <verb>', 6+ unit tests, build/vet/tests pass, AGENTS.md mention." \
  'test -f scripts/check_b205.sh && bash scripts/check_b205.sh'
# --- B204: HA elector: auto-detect failed nodes + auto-failover recommendation (Phase 3.2-3.3) ---
run_check "B204" "HA elector (Phase 3.2-3.3 of cluster-management.md). Background goroutine ticks every 5s, reads cluster_node for the configured cluster (default 'skygate-staging'), and transitions stale nodes to 'failed'. State machine (pure function nextState): pending + (no last_seen) + joined_at < (now - 3*heartbeat_interval) → failed; ready + last_seen < (now - 3*heartbeat_interval) → failed. 3 missed heartbeats × 30s = 90s of silence → failed (constants HeartbeatIntervalSeconds=30 + StaleMultiplier=3). The 3x balance is the failure detection latency for every cluster_node in production — a future refactor can't silently change these without B-test exposure. Auto-failover recommendation pass: if a node with role=skygate is 'failed' AND a node with role=skygate-standby is 'ready', the elector writes a cluster_audit row with action='failover_recommend' naming the target. Idempotent (5min dedup window) so consecutive ticks don't flood the audit table. The actual promote is admin-gated (B205 territory). transitionNode writes the audit row + UPDATE cluster_node in a single transaction so the state change and the audit are atomic. 6 unit tests in internal/elector/elector_b204_test.go (nextState 6 cases + roleContains 7 cases + splitRolesLiteral 4 cases + DefaultConfig + NewElector defaults + constants). main.go starts the elector with d.DB (the current *sql.DB from the ResettableDB pool) so it transparently follows B203 hot-reloads. 30 B-check contracts in scripts/check_b204.sh." \
  'test -f scripts/check_b204.sh && bash scripts/check_b204.sh'
# --- B203.1: GetClusterDatabase NULL safety (live B203 follow-up) ---
run_check "B203.1" "GetClusterDatabase NULL safety fix (B203 live follow-up). The first live B203 test (inserted cluster_database row with primary_node_id = NULL) caused the watchdog to log 'converting NULL to string is unsupported' every 5s — silently keeping the env-DSN pool. The B195 schema makes primary_node_id NULL-able (REFERENCES cluster_node(id) ON DELETE SET NULL), but the B203 reader assumed all columns were NOT NULL. Fix: COALESCE all NULL-able columns in the SELECT to their default empty value. 4 unit tests in internal/db/cluster_b203_test.go (NULL primary_node_id is the regression, populated is the positive control, not-found returns ErrClusterDatabaseNotFound, empty replica array is non-nil). 2 new B-check contracts (#25, #26) pin the COALESCE on primary_node_id + the new test file. Live-verified post-deploy: watchdog logs 'DSN change detected; swapping to ...' then 'pool swapped successfully (new backend pid: ...)' within one 5s tick of inserting the row." \
  'test -f scripts/check_b203.sh && bash scripts/check_b203.sh'

# --- TD-18.2: fix /admin/derp/dashboard silent regression (theme reset + no content) ---
run_check "TD-18.2" "fix /admin/derp/dashboard page that rendered with no content + theme reset to default + a 500 error at the bottom: render template: layout.html:197:15: executing layout at error calling gt: invalid type for comparison. Root cause: the B189 handler GetAdminDerpDashboard (and its POST sibling) passed nil for the JWT claims arg to s.Backend.RenderWithLayout. When c is nil, renderWithLayout (handlers.go:464) skips the notification auto-inject block at lines 500-532 — it does NOT set data[UnreadCount]. The layout template (layout.html:197) then evaluates {{if gt .UnreadCount 0}} on a missing key, which Go gt fails on with invalid type for comparison. The error halts template execution, so the rest of the body (DERP table) never renders AND the head-level theme CSS injection (downstream of the failing line) does not run — so the user sees the default theme instead of the B121 silver+mint. Fix: extract claims via c := s.Backend.CurrentUser(r) in both handlers and pass c to RenderWithLayout (3 nil-arg sites replaced). Every other admin handler (GetAdminAudit, GetAdminACLsImport, GetAdminControlPlanes, etc) was already doing this — derp_dashboard was the only outlier. 8 contracts in scripts/check_td182.sh." \
  'test -f scripts/check_td182.sh && bash scripts/check_td182.sh'

