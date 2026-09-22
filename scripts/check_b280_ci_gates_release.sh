#!/usr/bin/env bash
# check_b280_ci_gates_release.sh
#
# 2026-09-22 (B280, v1.5.46) — "no tag and no release from a commit whose
# CI is not green".
#
# WHY
# ---
# The v1.5.41 → v1.5.45 cycle pushed tags after `git push --no-verify`.
# CI caught two real regressions in that window (v1.5.44 introduced an RU
# i18n parity break and a raw-http.Error leak) and the releases shipped them
# anyway, because the tag already existed and release.yml builds whatever the
# tag points at. Hooks are per-clone and `--no-verify` skips them, so the
# rule has to exist on the server too.
#
# WHAT B280 SHIPS
#   * scripts/ci_gate.sh — ONE implementation of "is CI green for this sha?"
#     (0 green / 1 not green / 2 cannot verify; --wait for a running run;
#     refuses a commit that is not an ancestor of the release branch).
#   * release.yml `preflight` job — all four publishing jobs need it, so a
#     red or unverifiable commit publishes neither images nor a Release.
#   * tag-release.yml — the sanctioned way to CREATE the tag: manual
#     workflow_dispatch, gate first, tag second, then it dispatches
#     release.yml (a tag pushed with GITHUB_TOKEN does not fire `push`).
#   * .githooks/pre-tag — the local half, now a thin caller of the same
#     script (it used to carry its own copy of the query).
#
# CONTRACTS
#   A. one implementation, called by all three consumers
#   B. release.yml: preflight + permissions + trigger + needs wiring
#   C. tag-release.yml: gate BEFORE `git tag`, dispatch AFTER
#   D. behaviour: the gate's exit codes, exercised against a stubbed gh
#   E. ancestry: off-branch commit refused; unverifiable ancestry refused
#   F. the local hook delegates and keeps its bypass + escape hatch
#   G. docs tell the operator how to release now
#   H. tracked by git (trap #11)

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B280: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

GATE=scripts/ci_gate.sh
REL=.github/workflows/release.yml
TAGWF=.github/workflows/tag-release.yml
HOOK=.githooks/pre-tag
ROOT="$PWD"

hdr "B280 (v1.5.46) — CI gates the tag and the release"

# --- A: one implementation --------------------------------------------------
if [ -f "$GATE" ]; then
  ok "A1: scripts/ci_gate.sh exists (the single CI gate)"
else
  bad "A1: scripts/ci_gate.sh is missing"
fi
for f in "$REL" "$TAGWF" "$HOOK"; do
  if grep -q 'ci_gate.sh' "$f" 2>/dev/null; then
    ok "A2: $f calls ci_gate.sh (no second copy of the CI query)"
  else
    bad "A2: $f does not call ci_gate.sh"
  fi
done
for needle in 'EXIT CODES' 'exit 1' 'exit 2' '--wait' '--allow-off-main' 'merge-base --is-ancestor'; do
  if grep -q -- "$needle" "$GATE" 2>/dev/null; then
    ok "A3: ci_gate.sh implements '$needle'"
  else
    bad "A3: ci_gate.sh has no '$needle'"
  fi
done

# --- B: release.yml wiring --------------------------------------------------
if grep -q '^  preflight:' "$REL" 2>/dev/null; then
  ok "B1: release.yml has a preflight job"
else
  bad "B1: release.yml has no preflight job — a tag pushed with --no-verify would publish"
fi
if grep -q 'actions: read' "$REL"; then
  ok "B2: release.yml grants actions: read (the gate reads the ci.yml runs)"
else
  bad "B2: release.yml does not grant actions: read — the gate's API call would 403"
fi
if grep -q '^  workflow_dispatch:' "$REL"; then
  ok "B3: release.yml also accepts workflow_dispatch (tag-release.yml needs it — GITHUB_TOKEN tag pushes do not fire 'push')"
else
  bad "B3: release.yml cannot be dispatched explicitly, so the CI-created tag would produce no release"
fi
# Every publishing job must need the gate.
for job in docker binaries; do
  if awk -v j="$job" '$0 ~ "^  "j":" {f=1} f && /needs: preflight/ {print; exit}' "$REL" | grep -q 'needs: preflight'; then
    ok "B4: $job needs: preflight"
  else
    bad "B4: $job does not need: preflight — it can still build from an untested commit"
  fi
done
if grep -q 'needs: \[preflight, binaries\]' "$REL"; then
  ok "B5: sums needs: [preflight, binaries]"
else
  bad "B5: sums does not list preflight"
fi
if grep -q 'needs: \[preflight, docker, sums\]' "$REL"; then
  ok "B6: release needs: [preflight, docker, sums]"
else
  bad "B6: the github-release job does not list preflight"
fi
if grep -q 'bash scripts/ci_gate.sh' "$REL"; then
  ok "B7: the preflight job runs the shared gate"
else
  bad "B7: the preflight job does not run scripts/ci_gate.sh"
fi

# --- C: tag-release.yml -----------------------------------------------------
if [ -f "$TAGWF" ]; then
  ok "C1: .github/workflows/tag-release.yml exists"
else
  bad "C1: .github/workflows/tag-release.yml is missing — the tag can only be created by hand"
fi
if grep -q '^  workflow_dispatch:' "$TAGWF"; then
  ok "C2: tag-release.yml is manual (workflow_dispatch) — it never auto-tags main"
else
  bad "C2: tag-release.yml is not workflow_dispatch"
fi
if grep -q 'version:' "$TAGWF" && grep -q 'required: true' "$TAGWF"; then
  ok "C3: tag-release.yml requires a version input"
else
  bad "C3: tag-release.yml has no required version input"
fi
GATE_LINE=$(grep -n 'bash scripts/ci_gate.sh' "$TAGWF" | head -1 | cut -d: -f1)
TAG_LINE=$(grep -n 'git tag -a' "$TAGWF" | head -1 | cut -d: -f1)
if [ -n "$GATE_LINE" ] && [ -n "$TAG_LINE" ] && [ "$GATE_LINE" -lt "$TAG_LINE" ]; then
  ok "C4: the gate runs BEFORE git tag (line $GATE_LINE < $TAG_LINE)"
else
  bad "C4: the gate does not run before the tag (gate=${GATE_LINE:-none} tag=${TAG_LINE:-none})"
fi
if grep -q 'gh workflow run release.yml' "$TAGWF"; then
  ok "C5: tag-release.yml dispatches release.yml (GITHUB_TOKEN tag pushes do not trigger it)"
else
  bad "C5: tag-release.yml does not dispatch release.yml — the tag would create no release"
fi
if grep -q 'git ls-remote' "$TAGWF"; then
  ok "C6: tag-release.yml refuses an already-published tag (releases are immutable)"
else
  bad "C6: tag-release.yml can overwrite an existing tag"
fi

# --- D: behaviour with a stubbed gh ----------------------------------------
STUB="$(mktemp -d 2>/dev/null || echo /tmp/b280.$$)"
mkdir -p "$STUB/bin"
cat > "$STUB/bin/gh" <<'STUBEOF'
#!/usr/bin/env bash
case "$1" in
  auth) exit 0 ;;
  api)
    case "${STUB_MODE:-green}" in
      green)     printf '%s\n' $'101\tcompleted\tsuccess\tpush\thttps://example.invalid/101' ;;
      failed)    printf '%s\n' $'102\tcompleted\tfailure\tpush\thttps://example.invalid/102' ;;
      cancelled) printf '%s\n' $'103\tcompleted\tcancelled\tpush\thttps://example.invalid/103' ;;
      timedout)  printf '%s\n' $'104\tcompleted\ttimed_out\tpush\thttps://example.invalid/104' ;;
      pending)   printf '%s\n' $'105\tin_progress\t\tpush\thttps://example.invalid/105' ;;
      mixed)     printf '%s\n' $'106\tcompleted\tsuccess\tpush\thttps://example.invalid/106' $'107\tcompleted\tfailure\tpush\thttps://example.invalid/107' ;;
      none)      : ;;
      apierr)    exit 1 ;;
      *)         exit 1 ;;
    esac
    exit 0 ;;
esac
exit 0
STUBEOF
chmod +x "$STUB/bin/gh" 2>/dev/null || true

gate_case() { # mode expected_rc label
  local mode="$1" want="$2" label="$3" rc=0
  PATH="$STUB/bin:$PATH" STUB_MODE="$mode" bash "$GATE" \
    --repo example/repo --sha 0123456789abcdef --allow-off-main --quiet >/dev/null 2>&1 || rc=$?
  if [ "$rc" = "$want" ]; then
    ok "D: $label → exit $rc"
  else
    bad "D: $label → exit $rc, want $want"
  fi
}
gate_case green   0 "all CI runs successful"
gate_case failed  1 "a failed CI run"
gate_case cancelled 1 "a cancelled CI run (concurrency cancel-in-progress)"
gate_case timedout 1 "a timed-out CI run"
gate_case pending 1 "CI still running (no --wait budget)"
gate_case mixed   1 "one success + one failure"
gate_case none    1 "no CI run for the commit at all"
gate_case apierr  2 "the GitHub API query failed (cannot verify)"

# gh missing entirely → 2.
rc=0
PATH="$(dirname "$(command -v bash)"):/usr/bin:/bin" bash "$GATE" \
  --repo example/repo --sha 0123456789abcdef --allow-off-main --quiet >/dev/null 2>&1 || rc=$?
if [ "$rc" = "2" ]; then
  ok "D: no gh on PATH → exit 2 (cannot verify)"
elif command -v gh >/dev/null 2>&1; then
  # /usr/bin still had a real gh; skip rather than report a false FAIL.
  skip "D: 'no gh' case skipped (a real gh is reachable from /usr/bin)"
else
  bad "D: no gh on PATH → exit $rc, want 2"
fi

# --- E: ancestry ------------------------------------------------------------
ANC="$STUB/anc"; mkdir -p "$ANC"
if git -C "$ANC" init -q 2>/dev/null; then
  git -C "$ANC" config user.email t@example.invalid
  git -C "$ANC" config user.name t
  echo a > "$ANC/a"; git -C "$ANC" add . >/dev/null 2>&1; git -C "$ANC" commit -qm one >/dev/null 2>&1
  base="$(git -C "$ANC" rev-parse HEAD)"
  echo b >> "$ANC/a"; git -C "$ANC" commit -qam two >/dev/null 2>&1
  head="$(git -C "$ANC" rev-parse HEAD)"
  git -C "$ANC" update-ref refs/remotes/origin/main "$base"

  rc=0
  ( cd "$ANC" && PATH="$STUB/bin:$PATH" STUB_MODE=green bash "$ROOT/$GATE" \
      --repo example/repo --sha "$head" --quiet >/dev/null 2>&1 ) || rc=$?
  if [ "$rc" = "1" ]; then
    ok "E1: a commit that is not an ancestor of origin/main → exit 1"
  else
    bad "E1: off-branch commit → exit $rc, want 1"
  fi

  # No origin/main at all → cannot verify → 2.
  git -C "$ANC" update-ref -d refs/remotes/origin/main >/dev/null 2>&1 || true
  rc=0
  ( cd "$ANC" && PATH="$STUB/bin:$PATH" STUB_MODE=green bash "$ROOT/$GATE" \
      --repo example/repo --sha "$head" --quiet >/dev/null 2>&1 ) || rc=$?
  if [ "$rc" = "2" ]; then
    ok "E2: origin/main not fetched → exit 2 (cannot verify, so refuse)"
  else
    bad "E2: missing origin/main → exit $rc, want 2"
  fi
else
  skip "E: git not usable — ancestry contracts skipped"
fi
rm -rf "$STUB" 2>/dev/null || true

# --- F: the local hook ------------------------------------------------------
if grep -q 'scripts/ci_gate.sh' "$HOOK" && grep -q 'SKIP_PRE_TAG_CHECK' "$HOOK"; then
  ok "F1: .githooks/pre-tag delegates to ci_gate.sh and keeps the escape hatch"
else
  bad "F1: .githooks/pre-tag does not delegate to ci_gate.sh"
fi
if grep -q '\^v\[0-9\]' "$HOOK"; then
  ok "F2: .githooks/pre-tag still bypasses non-version tags"
else
  bad "F2: .githooks/pre-tag lost the non-version-tag bypass"
fi
if ! grep -q 'gh run list' "$HOOK"; then
  ok "F3: the hook no longer carries its own CI query (one implementation)"
else
  bad "F3: the hook still queries CI by itself — the copies can disagree"
fi

# --- G: docs ----------------------------------------------------------------
if grep -q 'tag-release' docs/operations.md 2>/dev/null; then
  ok "G1: docs/operations.md documents the CI-gated tag path"
else
  bad "G1: docs/operations.md does not mention tag-release — the operator will keep tagging by hand"
fi
if grep -q 'ci_gate.sh' docs/operations.md 2>/dev/null; then
  ok "G2: docs/operations.md names the gate script"
else
  bad "G2: docs/operations.md does not name scripts/ci_gate.sh"
fi

# --- H: tracked by git (trap #11) -------------------------------------------
for f in "$GATE" "$TAGWF" scripts/check_b280_ci_gates_release.sh; do
  if git ls-files --error-unmatch "$f" >/dev/null 2>&1; then
    ok "H1: $f is tracked by git"
  else
    bad "H1: $f is NOT tracked — .gitignore can eat it silently"
  fi
done

printf '\n\033[1mB280 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
