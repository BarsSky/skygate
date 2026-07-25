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
  bash -c "grep -q 'v0.28.5' RELEASE-NOTES.md 2>&1"

# --- B10: no committed secrets ---
run_check "B10" "no .env / secret file in git tracked paths" \
  bash -c "! git ls-files | grep -E '\\.(env|key|pem)$|^secrets/' 2>&1"

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
