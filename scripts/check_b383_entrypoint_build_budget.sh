#!/usr/bin/env bash
# check_b383_entrypoint_build_budget.sh
#
# 2026-10-14 (B383, v1.5.122) — "a container start must not spend 18 minutes
# re-fetching the world, and it must never look dead while it does".
#
# THE INCIDENT
# ------------
# The operator updated skygate and the web panel did not display. Measured on the
# host: the container started 06:32:47, the process only listened at 06:50:07 —
# an 18-MINUTE window with nothing on :8080. The entrypoint builds skygate from
# the bind-mounted source at every start (the image does not prebuild), and:
#
#   * both Go caches live INSIDE the container (`/go/pkg/mod` 570 MB,
#     `/root/.cache/go-build` 261 MB) so a recreate loses them;
#   * `go mod download` then went to proxy.golang.org and the connection timed
#     out — the retry loop is what burned the 18 minutes;
#   * `go mod tidy` ran on EVERY start against the operator's bind-mounted
#     checkout (network + it rewrites go.mod/go.sum);
#   * `apk add openssh-client git` ran on every start although both are already
#     in the image (another CDN round trip in the critical path);
#   * nothing answered on :8080 during any of it, so the operator (and the
#     panel's own health poll) saw a dead port rather than "building".
#
# Measured costs after a warm-up (same host, same tree):
#   go mod download, warm module cache ....... 0.03 s
#   full rebuild, warm build cache ........... 3.1 s
#   full rebuild, COLD build cache ........... 48 s
#   rebuild when nothing changed ............. 0.4 s
#
# WHAT B383 SHIPS
#   * the caches move to a PERSISTED root (`/data/go-caches` by default, i.e. the
#     bind mount that already survives a recreate) with `SKYGATE_GOMODCACHE` /
#     `SKYGATE_GOCACHE` overrides and a fallback to the container-local defaults
#     when that root is not writable — so an install with a read-only /data
#     behaves exactly as before;
#   * `go mod download` is bounded (`SKYGATE_GO_MOD_TIMEOUT`) and a failure names
#     the cause instead of hanging silently; `go mod tidy` is opt-in
#     (`SKYGATE_GO_MOD_TIDY=1`); the `apk add` runs only when git/ssh are missing;
#   * an unchanged tree SKIPS the build entirely: the stamp records the source
#     fingerprint AND the binary's size+mtime, so a replaced or rebuilt binary can
#     never be mistaken for the stamped one. A `git checkout <tag>` by the panel's
#     updater always changes the fingerprint, so the update path is untouched;
#   * while a build runs, busybox httpd answers `/healthz` with
#     `{"status":"booting","phase":"building","ready":false}` — and deliberately
#     WITHOUT a build label, so no poller can mistake "compiling" for "up";
#   * `SKYGATE_BUILD_PLAN=1` prints the decision and exits: the operator's answer
#     to "why is it rebuilding?", and the surface this contract exercises.
#
# CONTRACTS
#   A. the cache root: persisted by default, overridable, with a real fallback
#      (behavioural — the SAME functions the container runs, extracted from
#      entrypoint.sh between the B383 markers)
#   B. the source fingerprint reacts to exactly what a rebuild depends on
#      (behavioural, on a real git repository)
#   C. the build decision: build on any doubt, skip only on a full match
#      (behavioural, seven cases)
#   D. the start path: bounded download, gated tidy, conditional apk, the
#      provisional responder, the stamp written only after a successful build
#   E. the strings other contracts pin (B237.15 C.2, B91) survive this rewrite
#   F. registered, tracked, documented
#
# NOTE: the responder functions are NOT executed here — starting an HTTP server
# on a gate host would bind a real port and write outside the scratch dir. Their
# BEHAVIOUR is pinned by the entrypoint's own start, and their SHAPE (ready:false,
# no build label, per-run docroot, stopped before "Skygate ready") is asserted in
# D. Every scratch file lives under mktemp -d (trap #13).

set -uo pipefail

if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B383: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

export LC_ALL=C

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

EP=entrypoint.sh
CATALOG=scripts/verify_pre_deploy.sh
SKY_TMP="$(mktemp -d 2>/dev/null || mktemp -d "${TMPDIR:-/tmp}/skygate-b383.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

hdr "B383 (v1.5.122) — what a container start is allowed to cost"

# ---------------------------------------------------------------------------
# A. the cache root: persisted, overridable, with a real fallback
# ---------------------------------------------------------------------------
HELPERS="$SKY_TMP/b383_helpers.sh"
if sed -n '/^# >>> B383 helpers (begin)/,/^# <<< B383 helpers (end)/p' "$EP" > "$HELPERS" 2>/dev/null \
   && [ -s "$HELPERS" ]; then
  ok "A1: the B383 helper block is extractable from $EP (the contract tests the code the container runs)"
  # shellcheck disable=SC1090
  . "$HELPERS"
else
  bad "A1: the B383 helper block (markers '# >>> B383 helpers (begin)' / '# <<< B383 helpers (end)') is missing from $EP"
fi

if declare -F skygate_go_cache_dirs >/dev/null 2>&1; then
  # A2. a writable preferred root wins and the directories are created.
  root="$SKY_TMP/persisted"
  out="$(skygate_go_cache_dirs "$root" /go/pkg/mod /root/.cache/go-build)"; rc=$?
  if [ "$rc" -eq 0 ] && [ "$out" = "$root/mod	$root/build" ] && [ -d "$root/mod" ] && [ -d "$root/build" ]; then
    ok "A2: a writable root is used and both cache directories are created"
  else
    bad "A2: writable root → rc=$rc out=[$out] (want rc=0 and $root/mod<TAB>$root/build)"
  fi

  # A3. an unusable root falls back to the container-local defaults.
  blocker="$SKY_TMP/blocker"; : > "$blocker"
  out="$(skygate_go_cache_dirs "$blocker/sub" /go/pkg/mod /root/.cache/go-build)"; rc=$?
  if [ "$rc" -eq 1 ] && [ "$out" = "/go/pkg/mod	/root/.cache/go-build" ]; then
    ok "A3: an unusable root falls back to the container-local caches (rc=1 so the caller can warn)"
  else
    bad "A3: unusable root → rc=$rc out=[$out] (want rc=1 and the fallback pair)"
  fi
fi

if grep -q 'SKYGATE_GO_CACHE_ROOT:-/data/go-caches' "$EP"; then
  ok "A4: the default cache root is the persisted /data path (survives a container recreate)"
else
  bad "A4: the default cache root is not the persisted /data path"
fi
if grep -q 'SKYGATE_GOMODCACHE' "$EP" || grep -q 'GOMODCACHE=' "$EP"; then
  ok "A5: GOMODCACHE/GOCACHE are exported for the build (no hidden default)"
else
  bad "A5: the entrypoint does not set GOMODCACHE/GOCACHE"
fi

# ---------------------------------------------------------------------------
# B. the fingerprint reacts to what a rebuild depends on
# ---------------------------------------------------------------------------
if declare -F skygate_go_source_fingerprint >/dev/null 2>&1 && command -v git >/dev/null 2>&1; then
  gitrepo="$SKY_TMP/repo"; mkdir -p "$gitrepo/cmd/skygate" "$gitrepo/internal/x"
  if git -C "$gitrepo" init -q 2>/dev/null \
     && git -C "$gitrepo" config user.email t@example.invalid \
     && git -C "$gitrepo" config user.name t; then
    printf 'package main\n' > "$gitrepo/cmd/skygate/main.go"
    printf 'package x\n' > "$gitrepo/internal/x/x.go"
    printf 'module skygate\n\ngo 1.25\n' > "$gitrepo/go.mod"
    : > "$gitrepo/go.sum"
    git -C "$gitrepo" add -A >/dev/null 2>&1
    git -C "$gitrepo" commit -qm one >/dev/null 2>&1

    fp1="$(skygate_go_source_fingerprint "$gitrepo")"
    fp2="$(skygate_go_source_fingerprint "$gitrepo")"
    if [ -n "$fp1" ] && [ "$fp1" = "$fp2" ]; then
      ok "B1: the fingerprint is stable for an unchanged tree ($fp1)"
    else
      bad "B1: the fingerprint is unstable ([$fp1] vs [$fp2])"
    fi

    # B2. a MODIFIED tracked file (what an operator edit looks like) changes it.
    printf 'package x\n\nfunc F() {}\n' > "$gitrepo/internal/x/x.go"
    fp3="$(skygate_go_source_fingerprint "$gitrepo")"
    if [ "$fp3" != "$fp1" ]; then
      ok "B2: an uncommitted source change changes the fingerprint (a hand edit still rebuilds)"
    else
      bad "B2: an uncommitted edit did NOT change the fingerprint — a stale binary could be reused"
    fi

    # B3. a git CHECKOUT (what the panel's updater does) changes it — the whole
    # point: the update mechanism must keep working through this optimisation.
    git -C "$gitrepo" commit -qam two >/dev/null 2>&1
    fp4="$(skygate_go_source_fingerprint "$gitrepo")"
    if [ "$fp4" != "$fp3" ]; then
      ok "B3: a new commit changes the fingerprint, so the panel's 'git checkout <tag>' always rebuilds"
    else
      bad "B3: a new commit did not change the fingerprint — the panel's update path would silently reuse the old binary"
    fi
  else
    skip "B: git could not initialise a scratch repository here"
  fi
else
  skip "B: git or the fingerprint helper is unavailable — behaviour skipped"
fi

# ---------------------------------------------------------------------------
# C. the build decision: build on any doubt, skip only on a full match
# ---------------------------------------------------------------------------
if declare -F skygate_go_build_needed >/dev/null 2>&1; then
  app="$SKY_TMP/app"; mkdir -p "$app"
  bin="$app/skygate"; stamp="$SKY_TMP/build.stamp"

  decide() { skygate_go_build_needed "$app" "$bin" "$stamp" "${1:-0}" >/dev/null 2>&1; printf '%s' "$?"; }

  # C1. no binary -> build (exit 0 from the function = "build").
  rm -f "$bin" "$stamp"
  [ "$(decide)" = "0" ] && ok "C1: no binary → build" || bad "C1: no binary did not force a build"

  # C2. binary but no stamp -> build.
  printf '#!/bin/sh\necho v1\n' > "$bin"; chmod +x "$bin"
  [ "$(decide)" = "0" ] && ok "C2: no stamp → build" || bad "C2: a missing stamp did not force a build"

  # C3. a complete, matching stamp -> SKIP (function returns 1).
  fp="$(skygate_go_source_fingerprint "$app")"
  { printf '%s\n' "$fp"; printf '%s\n' "$(skygate_go_stamp_binary "$bin")"; } > "$stamp"
  [ "$(decide)" = "1" ] && ok "C3: an unchanged tree with a matching stamp → skip the build" \
    || bad "C3: a fully matching stamp still rebuilt (the optimisation is inert)"

  # C4. the recorded fingerprint no longer matches -> build.
  printf '%s\n' "deadbeef" > "$stamp"
  printf '%s\n' "$(skygate_go_stamp_binary "$bin")" >> "$stamp"
  [ "$(decide)" = "0" ] && ok "C4: a different source fingerprint → build" || bad "C4: a changed fingerprint still skipped"

  # C5. the binary is not the one the stamp describes -> build.
  { printf '%s\n' "$fp"; printf '%s\n' "123 456"; } > "$stamp"
  [ "$(decide)" = "0" ] && ok "C5: a replaced binary → build (a stale binary can never be reused)" \
    || bad "C5: a replaced binary still skipped — the stamp does not really cover the binary"

  # C6. SKYGATE_FORCE_BUILD=1 -> build even on a perfect match.
  { printf '%s\n' "$fp"; printf '%s\n' "$(skygate_go_stamp_binary "$bin")"; } > "$stamp"
  [ "$(decide 1)" = "0" ] && ok "C6: SKYGATE_FORCE_BUILD=1 forces a build" || bad "C6: the force switch does nothing"
  [ "$(decide 0)" = "1" ] && ok "C7: without the force switch the same state still skips (the switch is what changed)" \
    || bad "C7: the decision depends on something other than the force switch"
fi

# ---------------------------------------------------------------------------
# D. the start path itself
# ---------------------------------------------------------------------------
d31() { grep -q "timeout \"\${SKYGATE_GO_MOD_TIMEOUT:-900}\" go mod download" "$EP"; }
if d31; then
  ok "D1: go mod download is bounded by SKYGATE_GO_MOD_TIMEOUT (a stall fails loudly instead of hanging)"
else
  bad "D1: go mod download is unbounded — the 18-minute silent stall can come back"
fi
if grep -q 'SKYGATE_GO_MOD_TIDY:-0' "$EP"; then
  ok "D2: go mod tidy is opt-in (it used to rewrite the operator's go.mod/go.sum on every start)"
else
  bad "D2: go mod tidy still runs unconditionally against the operator's checkout"
fi
if grep -q 'if ! command -v git' "$EP"; then
  ok "D3: the apk install is conditional (git/ssh are already in the image)"
else
  bad "D3: apk add still runs on every start (a CDN round trip in the boot path)"
fi
if grep -q 'skygate_boot_responder_start' "$EP" && grep -q 'skygate_boot_responder_stop' "$EP"; then
  ok "D4: a provisional responder is started and stopped around the build"
else
  bad "D4: nothing answers while the build runs — the window is invisible again"
fi
if grep -q '"ready":false' "$EP" && ! grep -q '"build":' "$EP"; then
  ok "D5: the provisional body is ready:false and carries NO build label (a poller cannot mistake 'building' for 'up')"
else
  bad "D5: the provisional body either lacks ready:false or carries a build label that could satisfy a health poll early"
fi
if grep -q 'command -v python3' "$EP" && grep -q 'm http.server' "$EP" && grep -q 'command -v nc' "$EP"; then
  ok "D6: the responder prefers python3's server and falls back to a netcat loop (measured: this image has NO busybox httpd and no curl, but python3 + wget are there)"
else
  bad "D6: the responder relies on a tool this image does not ship (busybox httpd is not in Alpine's base busybox)"
fi
if grep -q 'wget -qO-' "$EP" && grep -q '"phase":"building"' "$EP"; then
  ok "D7: the responder is PROVED to serve (a wget probe for the building body), not merely assumed alive"
else
  bad "D7: the responder is only checked for liveness — a process that never answers would look fine in the log"
fi
if grep -q 'mktemp -d' "$EP"; then
  ok "D8: the responder docroot is a per-run mktemp directory, never a fixed /tmp path"
else
  bad "D8: the responder serves from a fixed path (trap #13) and could serve a stale body"
fi
# The stamp is written after a SUCCESSFUL build and nowhere else.
stamp_write_line="$(grep -n 'B383_STAMP" 2>/dev/null' "$EP" | head -1 | cut -d: -f1)"
build_line="$(grep -n 'go build -buildvcs=false -ldflags' "$EP" | head -1 | cut -d: -f1)"
ready_line="$(grep -n 'echo "Skygate ready, starting..."' "$EP" | head -1 | cut -d: -f1)"
stop_line="$(grep -n '^skygate_boot_responder_stop$' "$EP" | tail -1 | cut -d: -f1)"
if [ -n "$stamp_write_line" ] && [ -n "$build_line" ] && [ -n "$ready_line" ] && [ -n "$stop_line" ] \
   && [ "$build_line" -lt "$stamp_write_line" ] && [ "$stamp_write_line" -lt "$ready_line" ] \
   && [ "$stop_line" -lt "$ready_line" ]; then
  ok "D9: order is build ($build_line) → stamp ($stamp_write_line) → responder stop ($stop_line) → start ($ready_line)"
else
  bad "D9: wrong order (build=$build_line stamp=$stamp_write_line stop=$stop_line start=$ready_line) — a stamp written before the build would lie"
fi
if grep -q 'SKYGATE_BUILD_PLAN' "$EP" && grep -q '\[plan\] decision' "$EP"; then
  ok "D10: SKYGATE_BUILD_PLAN=1 reports the decision (the operator can ask why it is rebuilding)"
else
  bad "D10: there is no way to ask the entrypoint why it is rebuilding"
fi

# ---------------------------------------------------------------------------
# E. the strings other contracts pin must survive this rewrite
# ---------------------------------------------------------------------------
if grep -q 'go mod download' "$EP" && grep -q 'go build.*ldflags' "$EP"; then
  ok "E1: B237.15 C.2 still holds — the in-container build path (go mod download + go build -ldflags) is preserved"
else
  bad "E1: B237.15 C.2 would fail — the in-container build path is no longer recognisable"
fi
if grep -q 'SKYGATE_PREBUILT' "$EP"; then
  ok "E2: B237.15 C.1 still holds — the SKYGATE_PREBUILT gate is intact"
else
  bad "E2: the SKYGATE_PREBUILT gate disappeared"
fi
if grep -q 'pre-flight' "$EP" && grep -q 'HEADSCALE_URL' "$EP" && grep -q 'v0.33.1.39' "$EP" && grep -q 'B91' "$EP"; then
  ok "E3: the B91 pre-flight markers are intact"
else
  bad "E3: the B91 pre-flight markers were damaged"
fi
if bash -n "$EP" 2>/dev/null && sh -n "$EP" 2>/dev/null; then
  ok "E4: entrypoint.sh parses under both bash and POSIX sh (the image runs it with /bin/sh)"
else
  bad "E4: entrypoint.sh does not parse"
fi

# ---------------------------------------------------------------------------
# F. registered, tracked, documented
# ---------------------------------------------------------------------------
for f in "$EP" scripts/check_b383_entrypoint_build_budget.sh; do
  if git ls-files --error-unmatch "$f" >/dev/null 2>&1; then
    ok "F1: $f is tracked by git (trap #11)"
  else
    bad "F1: $f is NOT tracked by git"
  fi
done
if grep -q 'check_b383_entrypoint_build_budget.sh' "$CATALOG"; then
  ok "F2: registered in $CATALOG"
else
  bad "F2: not registered in $CATALOG (rule 2)"
fi
if grep -q 'B383' RELEASE-NOTES.md 2>/dev/null; then
  ok "F3: RELEASE-NOTES.md carries the B383 entry"
else
  bad "F3: RELEASE-NOTES.md has no B383 entry"
fi
if grep -q 'B383' docs/ROADMAP.md 2>/dev/null; then
  ok "F4: docs/ROADMAP.md records B383"
else
  bad "F4: docs/ROADMAP.md does not mention B383"
fi
if grep -q 'SKYGATE_GO_CACHE_ROOT' docs/UPDATE.md 2>/dev/null; then
  ok "F5: docs/UPDATE.md documents the cache knobs for the operator"
else
  bad "F5: docs/UPDATE.md does not document the cache knobs"
fi

printf '\n\033[1mB383 summary: %d passed, %d failed, %d skipped\033[0m\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
