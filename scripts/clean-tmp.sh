#!/usr/bin/env bash
# ============================================================================
# scripts/clean-tmp.sh — clean up old artifacts in the repo's tmp/ directory
# ============================================================================
# 2026-09-17: when we moved the build binary + test logs out of the
# project root into tmp/{build,logs}/ (Makefile + check_b147.sh), we
# also needed a periodic cleanup so the working tree doesn't accumulate
# dead artifacts. This script does that:
#
#   1. Delete files in tmp/ older than the threshold (default: 7 days).
#   2. Delete empty subdirectories left behind (preserves tmp/build/
#      and tmp/logs/ which have .gitkeep files tracked in git).
#   3. Print a summary of what was deleted so the operator can
#      confirm the cleanup.
#
# Usage:
#   bash scripts/clean-tmp.sh                # use default 7-day threshold
#   bash scripts/clean-tmp.sh 30             # override threshold
#   make clean-tmp TMP_MAX_AGE_DAYS=30       # via Makefile target
#
# Safe to run from any CWD — uses the script's parent dir as the
# tmp/ location.
#
# Idempotent. No side effects on a clean tmp/.
# ============================================================================
set -uo pipefail

MAX_AGE_DAYS="${1:-${TMP_MAX_AGE_DAYS:-7}}"

# Resolve the repo root from the script's location (works from any cwd).
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
TMP_DIR="$REPO_ROOT/tmp"

if [ ! -d "$TMP_DIR" ]; then
    echo "clean-tmp: $TMP_DIR does not exist (nothing to clean)" >&2
    exit 0
fi

echo "clean-tmp: scanning $TMP_DIR for files older than ${MAX_AGE_DAYS} day(s) ..."

# 1. Count what we're about to delete (so the summary is accurate even
#    when the operator's `find` doesn't print the deleted paths).
DELETED=0
DELETED_KB=0
while IFS= read -r -d '' f; do
    SIZE_KB=$(( ($(stat -c%s "$f" 2>/dev/null || echo 0) + 1023) / 1024 ))
    rm -f -- "$f"
    DELETED=$(( DELETED + 1 ))
    DELETED_KB=$(( DELETED_KB + SIZE_KB ))
done < <(find "$TMP_DIR" -type f -mtime +"$MAX_AGE_DAYS" -print0 2>/dev/null)

# 2. Remove empty subdirectories left behind. Preserves tmp/build/
#    and tmp/logs/ because their .gitkeep files make them non-empty.
EMPTY_DIRS=0
while IFS= read -r d; do
    rmdir -- "$d" 2>/dev/null && EMPTY_DIRS=$(( EMPTY_DIRS + 1 ))
done < <(find "$TMP_DIR" -mindepth 1 -type d -empty 2>/dev/null)

# 3. Print a summary. Always exit 0 — clean-tmp is a best-effort sweep.
echo "clean-tmp: deleted ${DELETED} file(s) (~$(( DELETED_KB / 1024 )) MB), removed ${EMPTY_DIRS} empty dir(s)"
exit 0
