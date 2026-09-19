#!/usr/bin/env bash
# scripts/lib/go_build.sh — shared helper for the `skygate <verb> --help`
# contracts (scripts/check_b211.sh … check_b214.sh).
#
# Why this exists (2026-09-19): each of those four contracts LINKS the whole
# binary into a temp dir to run `<subcommand> --help`. The reference VM has
# 2 vCPU / 1.8 GB RAM, and a full gate run puts docker + PostgreSQL + several
# `go build ./...` invocations back to back; the link step needs a few hundred
# MB and occasionally dies under that pressure while the packages themselves
# compile fine. The symptom was exactly one rotating FAIL per gate run
# (B212 contract T, B213 contract S) although each check passes 20/20 standalone.
#
# This helper retries the link ONCE and prints the real error. A genuine build
# failure fails both attempts — nothing is hidden — while a resource blip no
# longer turns the gate red. Not executable on purpose: it is source-only
# (same rule as scripts/lib/db_credentials.sh).
go_build_bin() {
    local out="$1"
    shift
    local go_bin="${GO:-go}"
    local err
    local log="${SKYGATE_GO_BUILD_LOG:-/tmp/skygate-go-build.log}"
    if err="$("$go_bin" build -o "$out" "$@" 2>&1)"; then
        return 0
    fi
    {
        printf '[%s] go build -o %s %s FAILED (attempt 1)\n' "$(date -u +%FT%TZ)" "$out" "$*"
        printf '%s\n' "$err"
    } >> "$log" 2>/dev/null || true
    echo "[warn] go build -o $out $* failed; retrying once (small host / resource pressure?)" >&2
    printf '%s\n' "$err" | tail -5 >&2
    sleep 1
    if err="$("$go_bin" build -o "$out" "$@" 2>&1)"; then
        echo "[warn] the retry succeeded (details in $log)" >&2
        return 0
    fi
    {
        printf '[%s] go build -o %s %s FAILED (attempt 2 — giving up)\n' "$(date -u +%FT%TZ)" "$out" "$*"
        printf '%s\n' "$err"
    } >> "$log" 2>/dev/null || true
    echo "[error] go build -o $out $* failed twice (details in $log):" >&2
    printf '%s\n' "$err" | tail -10 >&2
    return 1
}
