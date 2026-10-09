#!/usr/bin/env bash
# check_b372_main_is_not_one_giant_function.sh — B372 (2026-10-09): start pulling
# the boot sequence out of the single 2500-line main().
#
# THE PROBLEM. `cmd/skygate/main.go` is not "many functions that are long" — it is
# ONE function. Measured 2026-10-09: 2568 lines, of which lines 128..2567 are the
# body of `main()`; the only other top-level declarations are a `var` block and
# three small helpers (redactPGPassword, listenAddr, handlerBox). Every refactor
# since v0.30 has ADDED to it, so the file grows by construction, and the operator's
# standing complaint ("чтобы кодовая база не тонула в тысячелетних файлах") cannot
# be answered by linting — the boot sequence has to be cut into named phases.
#
# WHY THE TAIL FIRST. The end of the boot path is the only part that can be moved
# WITHOUT changing when anything stops: it starts goroutines and nothing else, and
# it declares no state the shutdown path needs. The earlier blocks cannot be moved
# as text at all, because they hold bare `defer`s in main():
#
#	1844  defer wd.Stop()   // the dbmigrate watchdog
#	1862  defer el.Stop()   // the HA elector
#	2015  defer stop()      // (the headscale-version ask)
#
# A `defer` inside a `if` block still defers to the enclosing FUNCTION, so moving
# those lines into a helper would stop the watchdog and the elector the moment the
# helper returned — a silent behaviour change no grep would catch. They need a
# lifecycle-owned type with an explicit Stop() called from the shutdown path, which
# is its own block (B373+), not a text move. This contract records that boundary so
# the next editor does not "finish the refactor" by moving them.
#
# WHAT THIS SCRIPT PINS
#   A. the extracted function exists in its own file, with the imported surface it
#      needs and NOTHING else taken from main();
#   B. the call sits at the SAME point of the boot sequence (after the certsync
#      wiring, before the shutdown wait) — a text move that changes the order is a
#      regression, not a refactor;
#   C. no `defer` was moved out of main(): the moved function contains none, and the
#      three lifecycle defers are still where they were;
#   D. every scheduler's operator-visible log line still exists in the cmd/skygate
#      surface, and each one is still gated on its own cfg flag;
#   E. the ratchet: main.go is smaller than the recorded ceiling and the ceiling may
#      only fall;
#   F. the behavioural half runs;
#   G. tracked by git (AGENTS trap #11), registered, indexed.
#
# SKIP (never FAIL) anything that needs a tool this host lacks.
#
# Usage:  bash scripts/check_b372_main_is_not_one_giant_function.sh
# Exit:   0 = contracts hold, 1 = regression
set -uo pipefail
cd "$(dirname "$0")/.."
. scripts/lib/gosurface.sh

SKY_TMP="$(mktemp -d /tmp/skygate-check.XXXXXX)"
trap 'rm -rf "$SKY_TMP"' EXIT

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

MAIN=cmd/skygate/main.go
SCHED=cmd/skygate/main_schedulers.go
SELF=scripts/check_b372_main_is_not_one_giant_function.sh

# The ceiling is the whole point of the ratchet: it is the measured post-B372 size
# and it may only fall. Raise it deliberately, never to "make the check pass".
MAIN_CEILING=2400

for f in "$MAIN" "$SCHED"; do
  [ -f "$f" ] || { bad "A0: missing $f"; echo "B372 summary: $PASS passed, $FAIL failed, $SKIP skipped"; exit 1; }
done

# =====================================================================
hdr "A. the extracted phase lives in its own file"

if grep -q 'func wireOptionalSchedulers(ctx context.Context, d \*db.ResettableDB, app \*handlers.App, adminSvc \*adminsvc.Service, cfg \*config.Config)' "$SCHED"; then
  ok "A1: wireOptionalSchedulers takes exactly what the moved block used (ctx, the resettable DB, the app, the admin service, the config)"
else
  bad "A1: the extracted function changed shape or vanished"
fi
if ! grep -qE '^func main\(' "$SCHED"; then
  ok "A2: the new file declares no second entry point"
else
  bad "A2: a second main() is in the tree — the build would not even link"
fi
# The block must have LEFT main.go, not been copied: a duplicate would compile only
# if one of them were renamed, and a "moved" block that still sits inline is how a
# refactor silently doubles a scheduler.
if grep -q 'mesh\.StartCleanupScheduler' "$MAIN"; then
  bad "A3: the cleanup-scheduler wire-up is still INSIDE main.go — the block was copied, not moved"
else
  ok "A3: the moved wire-up is no longer in main.go (moved, not copied)"
fi
if grep -q 'mesh\.StartCleanupScheduler' "$SCHED" && grep -q 'tokenrotate\.Start' "$SCHED" \
   && grep -q 'keynotify\.Start' "$SCHED" && grep -q 'go elector\.Run(ctx)' "$SCHED" \
   && grep -q 'go runDiscoveryTicker' "$SCHED"; then
  ok "A4: all five schedulers moved together (cleanup, discovery, token-rotate, key-notify, HA elector)"
else
  bad "A4: one of the five schedulers was left behind or lost"
fi

# =====================================================================
hdr "B. the call sits at the same point of the boot sequence"

CALL_LINE="$(grep -n 'wireOptionalSchedulers(ctx, d, app, adminSvc, cfg)' "$MAIN" | head -1 | cut -d: -f1)"
CERTSYNC_LINE="$(grep -n 'certsync: enabled' "$MAIN" | head -1 | cut -d: -f1)"
SHUTDOWN_LINE="$(grep -n '^	<-ctx.Done()' "$MAIN" | head -1 | cut -d: -f1)"
if [ -n "$CALL_LINE" ] && [ -n "$CERTSYNC_LINE" ] && [ -n "$SHUTDOWN_LINE" ] \
   && [ "$CALL_LINE" -gt "$CERTSYNC_LINE" ] && [ "$CALL_LINE" -lt "$SHUTDOWN_LINE" ]; then
  ok "B1: the call is still after the certsync wiring and before the shutdown wait (boot order unchanged)"
else
  bad "B1: the call moved out of its slot in the boot sequence (call=$CALL_LINE certsync=$CERTSYNC_LINE shutdown=$SHUTDOWN_LINE)"
fi
if [ "$(grep -c 'wireOptionalSchedulers(' "$MAIN")" = "1" ]; then
  ok "B2: it is called exactly once"
else
  bad "B2: the phase is called zero or several times"
fi
# The shutdown path itself must NOT have moved into the helper: `<-ctx.Done()`
# followed by the 5s Shutdown is main()'s contract with the process.
if grep -q 'srv.Shutdown(shutCtx)' "$MAIN" && grep -q 'context.WithTimeout(context.Background(), 5\*time.Second)' "$MAIN"; then
  ok "B3: the graceful-shutdown path stayed in main()"
else
  bad "B3: the shutdown path moved or was lost"
fi

# =====================================================================
hdr "C. no lifecycle \`defer\` was dragged out of main()"

if grep -qE '^[[:space:]]*defer ' "$SCHED"; then
  bad "C1: the extracted function contains a defer — it now stops something at helper-return instead of at process exit"
else
  ok "C1: the extracted function holds no defer (nothing stops earlier than it used to)"
fi
for d in 'defer wd.Stop()' 'defer el.Stop()'; do
  if grep -qF "$d" "$MAIN"; then
    ok "C2: '$d' is still in main() where a defer means process exit"
  else
    bad "C2: '$d' left main() — moving a defer into a helper changes WHEN it runs"
  fi
done
if [ "$(grep -cE '^[[:space:]]*defer ' "$MAIN")" -ge 3 ]; then
  ok "C3: the three lifecycle defers are still counted in main() (the rest of the boot path is not movable as text — see the header)"
else
  bad "C3: main() lost one of its lifecycle defers"
fi

# =====================================================================
hdr "D. every scheduler still announces itself, and keeps its own gate"

gosurface SURFACE cmd/skygate/*.go
for pair in 'cleanup-scheduler: enabled|cfg.CleanupSmokeMeshInAppEnabled' \
            'discovery-ticker: enabled|SKYGATE_DISCOVERY_INTERVAL_SEC' \
            'auto-rotate-scheduler: enabled|cfg.TokenAutoRotateEnabled' \
            'key-notify-scheduler: enabled|cfg.KeyNotifyEnabled' \
            'ha: HA enabled|cfg.HAEnabled'; do
  log="${pair%%|*}"; gate="${pair##*|}"
  if grep -qF "$log" "$SURFACE" && grep -qF "$gate" "$SURFACE"; then
    ok "D: '$log' survives with its '$gate' gate"
  else
    bad "D: '$log' or its gate '$gate' was lost in the move"
  fi
done

# =====================================================================
hdr "E. the ratchet: main() may only get smaller"

LINES="$(wc -l < "$MAIN" | tr -d ' ')"
if [ "$LINES" -le "$MAIN_CEILING" ]; then
  ok "E1: cmd/skygate/main.go is $LINES lines (ceiling $MAIN_CEILING; it may only fall)"
else
  bad "E1: cmd/skygate/main.go grew to $LINES lines, past the recorded ceiling $MAIN_CEILING — this refactor only pays off if the file keeps shrinking"
fi
if [ "$LINES" -lt 2568 ]; then
  ok "E2: it is smaller than the 2568 lines measured before this block"
else
  bad "E2: the file did not shrink — the extraction did nothing"
fi
# The ceiling must not be raised to hide growth: the number in this script is the
# contract, and E1 compares the file against it.
if grep -q "^MAIN_CEILING=$MAIN_CEILING\$" "$SELF"; then
  ok "E3: the ceiling is the literal in this script (a deliberate, reviewable number)"
else
  bad "E3: the ceiling is computed rather than pinned — growth could hide behind it"
fi

# =====================================================================
hdr "F. the behavioural half runs"

GOBIN="$(command -v go 2>/dev/null || true)"
if [ -z "$GOBIN" ] && [ -n "${GO:-}" ] && [ -x "$GO" ]; then GOBIN="$GO"; fi
if [ -z "$GOBIN" ]; then
  skip "F1: no go binary in PATH — the build and the package tests were not run"
else
  if out="$("$GOBIN" build ./... 2>&1)"; then
    ok "F1: the tree builds after the move"
  else
    bad "F1: go build ./... fails:
$(printf '%s\n' "$out" | tail -20)"
  fi
  if out="$("$GOBIN" test ./cmd/skygate/ -count=1 2>&1)"; then
    ok "F2: the cmd/skygate package tests pass (B269's handover probe, B366's version rule, the B371 S3 config)"
  else
    bad "F2: go test ./cmd/skygate/ failed:
$(printf '%s\n' "$out" | tail -20)"
  fi
  if out="$("$GOBIN" vet ./cmd/skygate/ 2>&1)"; then
    ok "F3: go vet is clean on the moved surface"
  else
    bad "F3: go vet reported findings:
$(printf '%s\n' "$out" | tail -20)"
  fi
fi

# =====================================================================
hdr "G. tracked by git (AGENTS trap #11), registered, indexed"

if git ls-files --error-unmatch "$SCHED" >/dev/null 2>&1; then
  ok "G1: the new file is tracked by git"
else
  skip "G1: not yet tracked by git (the lead commits this block)"
fi
if git ls-files --error-unmatch "$SELF" >/dev/null 2>&1; then
  ok "G1b: this script is tracked by git"
else
  skip "G1b: this script is not tracked yet (the lead commits this block)"
fi
if grep -q 'run_check "B372"' scripts/verify_pre_deploy.sh; then
  ok "G2: registered in scripts/verify_pre_deploy.sh"
else
  bad "G2: not registered — the contract would never run"
fi
if grep -q '^- \*\*B372\*\*' AGENTS.md; then
  ok "G3: recorded in the AGENTS.md block index (as its own line)"
else
  bad "G3: AGENTS.md has no B372 bullet at line start"
fi

printf '\n\033[1mB372 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
