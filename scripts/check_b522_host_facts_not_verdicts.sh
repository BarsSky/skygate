#!/usr/bin/env bash
# check_b522_host_facts_not_verdicts.sh
#
# B522 (2026-11-25) — A CHECK MUST NOT TURN A FACT ABOUT THE HOST INTO A VERDICT ABOUT
# THE PRODUCT (part 2: the one that survived).
#
# WHY THIS EXISTS, with the measurements that produced it. A full gate run on the
# reference install (the host that actually serves the tailnet) ended with 16 red rows.
# Fifteen of them were ONE property of that host, reported three different ways:
#
#   1. TestB458_EnvOverrideAndUnreadableConfig pointed SKYGATE_HEADSCALE_CONFIG* at
#      /nonexistent/config.yaml and demanded «unknown resolvers». The install is NATIVE,
#      so /etc/headscale/config.yaml exists; the discovery loop skipped the unreadable env
#      paths and returned the LIVE set with Source=/etc/headscale/config.yaml.
#   2. TestB502_UnresolvedHostSaysSoAndKeepsThePolicyValid set two env names that cannot
#      exist and demanded «nothing resolved». The hostname ladder's last rung is
#      os.Hostname(), the machine is called `svyatoslava`, and node_owner_map HAS that row.
#   3. Because both tests fail, every check that runs `go test ./...` or the affected
#      packages failed too: B1, B6, B176, B237.16, B237.23, B270, B275, B284, B285, B332,
#      B443, B-tailscale-grants. One machine name, twelve false verdicts.
#
# The second half is a genuine defect in scripts/lib/live_db.sh: a NON-ROOT caller was
# always routed through `sudo -n`, even when the database file was readable to it, so on
# that host (where the gate user has no passwordless sudo: «User gate is not allowed to run
# sudo on svyatoslava») every read returned an EMPTY string — and check_b488 F6 compared
# the empty string with 7 and reported the product as broken.
#
# The third is a timing guard whose margin was smaller than the host's noise: with /tmp on
# tmpfs a full migration chain costs ~51 ms for BOTH handles (fsync is free), so the 25 %
# margin was 12.8 ms and the measured gap 18 ms. It reddened a DIFFERENT carrier in 2 of 4
# full runs (B287, Phase_3_4, Phase_3_6 — all of them just run `go test ./internal/db/...`)
# while passing 5/5 standalone.
#
# The fix in every case is the same shape as B332/B479: make the premise TRUE instead of
# hoping for it, and never let «could not measure» read as «measured wrong».
set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B522: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '\033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }
has()  { grep -qF -- "$2" "$1" 2>/dev/null; }

SKY_TMP="$(mktemp -d "${TMPDIR:-/tmp}/skygate-b522.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

DNS_GO=internal/headscale/dns_config_b458.go
DNS_TEST=internal/headscale/dns_config_b458_test.go
ACL_GO=internal/acl/acl_panel_host_b502.go
ACL_TEST=internal/acl/acl_panel_host_b502_test.go
LIB=scripts/lib/live_db.sh
B488=scripts/check_b488_live_checks_install_kind.sh
B419_TEST=internal/db/fastdb_b419_test.go

hdr "B522 — a host fact is not a product verdict"

# --- A. B458: the discovery list must be a seam ----------------------------------
A=""
has "$DNS_GO" 'var tailnetDNSConfigPathList = func() []string' \
  || A="${A}the discovery list is not a seam (no `var tailnetDNSConfigPathList = func()`)"$'\n'
has "$DNS_GO" 'range tailnetDNSConfigPathList()' \
  || A="${A}TailnetDNSResolvers does not iterate the seam"$'\n'
has "$DNS_TEST" 'tailnetDNSConfigPathList = func() []string' \
  || A="${A}the test does not override the seam (it would still read the host's config)"$'\n'
has "$DNS_TEST" 'func TestB458_SeamIsTheOnlyWayToThePaths(' \
  || A="${A}the anti-bypass leg TestB458_SeamIsTheOnlyWayToThePaths is missing"$'\n'
if [ -z "$A" ]; then
  ok "A1: the config discovery list is a seam, and the test makes «nothing readable» true on any host"
else
  bad "A1: the B458 host coupling is not closed:"
  printf '%s' "$A" | sed 's/^/       /' >&2
fi

# --- B. B502: the hostname rung must be a seam -----------------------------------
B=""
has "$ACL_GO" 'var panelHostOSHostname = os.Hostname' \
  || B="${B}os.Hostname is not a variable seam"$'\n'
has "$ACL_GO" 'panelHostOSHostname()' \
  || B="${B}panelHostCandidates does not call the seam"$'\n'
has "$ACL_TEST" 'panelHostOSHostname = func() (string, error)' \
  || B="${B}the test does not override the hostname rung"$'\n'
has "$ACL_TEST" 'func TestB502_HostnameRungIsTheSeam(' \
  || B="${B}the anti-bypass leg TestB502_HostnameRungIsTheSeam is missing"$'\n'
# The premise must be impossible by construction: RFC 2606 reserves `.invalid`.
has "$ACL_TEST" 'no-such-panel-host.invalid' \
  || B="${B}the unresolved fixture does not use a reserved (`.invalid`) name — a real hostname could satisfy it"$'\n'
if [ -z "$B" ]; then
  ok "B1: the hostname ladder is a seam, and «this host is unknown» is impossible to satisfy by accident"
else
  bad "B1: the B502 host coupling is not closed:"
  printf '%s' "$B" | sed 's/^/       /' >&2
fi

# --- C. the two repaired tests actually run --------------------------------------
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/headscale/ ./internal/acl/ -run 'TestB458_EnvOverrideAndUnreadableConfig|TestB458_SeamIsTheOnlyWayToThePaths|TestB502_UnresolvedHostSaysSoAndKeepsThePolicyValid|TestB502_HostnameRungIsTheSeam' -count=1 2>&1)"
  RC=$?
  if [ "$RC" -eq 0 ]; then
    ok "C1: the repaired tests pass without depending on what this machine is called or which configs it has"
  else
    bad "C1: the repaired tests fail (rc=$RC):"
    printf '%s\n' "$OUT" | grep -E 'FAIL|---' | head -8 | sed 's/^/       /' >&2
  fi
else
  skip "C1: no Go on PATH — the repaired tests cannot be run here"
fi

# --- D. live_db_sql: read directly, sudo only as the reserve ---------------------
D=""
has "$LIB" 'out="$(sqlite3 -readonly "$path" "$sql" 2>/dev/null)"; rc=$?' \
  || D="${D}the SQLite branch does not attempt a direct read before sudo"$'\n'
has "$LIB" 'out="$(psql "$dsn" -At -F"$sep" -c "$sql" 2>/dev/null)"; rc=$?' \
  || D="${D}the PostgreSQL branch does not attempt a direct read"$'\n'
has "$LIB" 'if [ "$rc" -eq 0 ]; then printf' \
  || D="${D}the branch does not decide on the EXIT STATUS (an empty result would be retried through sudo)"$'\n'
if [ -z "$D" ]; then
  ok "D1: a non-root caller reads the database itself, and sudo is the reserve"
else
  bad "D1: live_db_sql still forces sudo for a readable database:"
  printf '%s' "$D" | sed 's/^/       /' >&2
fi

# D2 — behavioural: with sudo made IMPOSSIBLE and a readable database, the answer must
# still arrive. This is the leg that would have failed on the reference host.
if command -v sqlite3 >/dev/null 2>&1; then
  mkdir -p "$SKY_TMP/stub"
  cat > "$SKY_TMP/stub/sudo" <<'STUB'
#!/bin/sh
echo "sudo: no passwordless sudo here" >&2
exit 1
STUB
  chmod +x "$SKY_TMP/stub/sudo"
  if sqlite3 "$SKY_TMP/read.db" 'CREATE TABLE t(a INTEGER); INSERT INTO t VALUES (7);' 2>/dev/null; then
    printf 'SKYGATE_DB=sqlite:%s\n' "$SKY_TMP/read.db" > "$SKY_TMP/read.env"
    OUT="$(SKYGATE_LIVE_ENV_FILE="$SKY_TMP/read.env" PATH="$SKY_TMP/stub:$PATH" \
           bash -c '. "$0"; live_db_sql "SELECT a FROM t;"' "$LIB" 2>&1)"
    RC=$?
    if [ "$RC" -eq 0 ] && [ "$OUT" = "7" ]; then
      ok "D2: a readable database answers even when sudo is impossible (the exact host shape that reported a defect)"
    else
      bad "D2: live_db_sql answered '$OUT' (rc=$RC) with a readable database and no sudo — it is still going through sudo"
    fi
    # D3 — the reserve still works: sqlite3 refuses, sudo must be used.
    cat > "$SKY_TMP/stub/sqlite3" <<'STUB'
#!/bin/sh
exit 1
STUB
    cat > "$SKY_TMP/stub/sudo" <<'STUB'
#!/bin/sh
[ "${1:-}" = "-n" ] && shift
case "$*" in
  *"-readonly"*) echo 'sudo-answer' ;;
  *) exit 1 ;;
esac
STUB
    chmod +x "$SKY_TMP/stub/sqlite3" "$SKY_TMP/stub/sudo"
    OUT="$(SKYGATE_LIVE_ENV_FILE="$SKY_TMP/read.env" PATH="$SKY_TMP/stub:$PATH" \
           bash -c '. "$0"; live_db_sql "SELECT a FROM t;"' "$LIB" 2>&1)"
    RC=$?
    if [ "$RC" -eq 0 ] && [ "$OUT" = "sudo-answer" ]; then
      ok "D3: when the caller truly cannot read it, the sudo reserve still answers"
    else
      bad "D3: the sudo reserve did not answer (rc=$RC): '$(printf '%s' "$OUT" | tail -1)'"
    fi
  else
    skip "D2/D3: could not build a real sqlite fixture on this host"
  fi
else
  skip "D2/D3: no sqlite3 on this host, so the direct-read leg cannot be exercised"
fi

# D4 — the B488 contract must tell «could not answer» from «answered wrong».
D4=""
has "$B488" 'real_rc=$?' \
  || D4="${D4}check_b488 F6 does not capture the transport's exit status"$'\n'
has "$B488" 'could not answer here' \
  || D4="${D4}check_b488 F6 has no SKIP branch for an unanswerable transport"$'\n'
if [ -z "$D4" ]; then
  ok "D4: B488's real-file leg SKIPs when the transport cannot answer, instead of accusing the product"
else
  bad "D4: B488 still reports an unavailable privilege as a product defect:"
  printf '%s' "$D4" | sed 's/^/       /' >&2
fi

# --- E. B419: the cost guard needs a noise floor ---------------------------------
E=""
has "$B419_TEST" 'const noiseFloor = 40 * time.Millisecond' \
  || E="${E}the cost guard has no absolute noise floor (a tmpfs host decides its verdict)"$'\n'
has "$B419_TEST" 'perFast > perSlow+tolerance' \
  || E="${E}the comparison does not use the computed allowance"$'\n'
has "$B419_TEST" 'PRAGMA journal_mode' \
  || E="${E}the behavioural pragma test is gone — it is the guard that matters on a fast filesystem"$'\n'
if [ -z "$E" ]; then
  ok "E1: the timing guard keeps 25% AND gains a 40 ms floor, so noise cannot decide it"
else
  bad "E1: the timing guard is still decided by the filesystem:"
  printf '%s' "$E" | sed 's/^/       /' >&2
fi

if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/db/ -run 'TestB419' -count=10 2>&1)"
  RC=$?
  if [ "$RC" -eq 0 ]; then
    ok "E2: ten consecutive runs of the cost guard pass (the flake is gone, not silenced)"
  else
    bad "E2: the cost guard failed within ten runs (rc=$RC):"
    printf '%s\n' "$OUT" | grep -E 'B419:|FAIL' | head -6 | sed 's/^/       /' >&2
  fi
else
  skip "E2: no Go on PATH"
fi

# --- F. bookkeeping --------------------------------------------------------------
F=""
for f in "$DNS_GO" "$DNS_TEST" "$ACL_GO" "$ACL_TEST" "$LIB" "$B419_TEST"; do
  git ls-files --error-unmatch "$f" >/dev/null 2>&1 \
    || F="${F}$f is NOT tracked by git (AGENTS trap #11)"$'\n'
done
git ls-files --error-unmatch scripts/check_b522_host_facts_not_verdicts.sh >/dev/null 2>&1 \
  || F="${F}this script is NOT tracked by git"$'\n'
has scripts/verify_pre_deploy.sh 'check_b522_host_facts_not_verdicts.sh' \
  || F="${F}verify_pre_deploy.sh does not register B522"$'\n'
has AGENTS.md '**B522**' \
  || F="${F}AGENTS.md's block index has no B522 entry (AGENTS rule 2)"$'\n'
if [ -z "$F" ]; then
  ok "F1: touched files tracked, script registered, block indexed"
else
  bad "F1: the block bookkeeping is incomplete:"
  printf '%s' "$F" | sed 's/^/       /' >&2
fi

printf '\n\033[1mB522 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
