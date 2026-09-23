#!/usr/bin/env bash
# check_b299_catalog_cannot_hang.sh
#
# 2026-09-23 (B299) — «гейт не должен уметь встать навсегда».
#
# LIVE INCIDENT (measured, not theorised). A `verify_pre_deploy.sh` run inside
# WSL Ubuntu — launched through `C:\WINDOWS\system32\bash.exe`, i.e. NOT Git
# Bash — reached `scripts/check_b_admin_user_sync.sh`, whose first live probe is
# a bare `sudo docker info`. Inside that distro `sudo` requires a password
# (`sudo -n true` → "interactive authentication is required"), so it opened
# /dev/tty to ask. The reader was not in the terminal's foreground process
# group, the kernel answered with SIGTTIN, and the WHOLE GROUP stopped:
#
#   PID 18051  T  timeout 900 bash -c … check_b_admin_user_sync.sh
#   PID 18052  T  bash scripts/check_b_admin_user_sync.sh
#   PID 18053  T  sudo docker info
#
# A stopped process never runs its SIGALRM handler, so the per-check budget
# could not fire: `timeout` was stopped along with its child. The catalog sat
# there for 54 minutes (16:18 → 17:12) and the session that launched it waited
# for a process that would never finish; it had to be killed by hand.
#
# Two independent defects, one contract each:
#   A. the runner must give every check NO CONTROLLING TERMINAL (so a password
#      prompt fails fast instead of stopping the world) and a deadline that can
#      KILL (so a TERM-ignoring check cannot outlive its budget);
#   B. a check must never ASK: every executed `sudo` in the gate is `sudo -n`,
#      so a check that needs root either works or reports SKIP/FAIL immediately.
#
# CONTRACTS
#   A. the runner: no controlling terminal, killable deadline, rc 137 = TIMEOUT
#   B. no EXECUTED bare `sudo` anywhere the gate runs (detector + self-test)
#   C. behaviour: a self-stopping check, a tty-reading check, a password-hungry
#      sudo shim and orphaned grandchildren are all bounded by the deadline
#   D. the live culprit is named and can never come back
#   E. registered, documented, tracked

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B299: cannot locate cmd/skygate/main.go from %s\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

VP=scripts/verify_pre_deploy.sh
CULPRIT=scripts/check_b_admin_user_sync.sh

hdr "B299 — the guarantee catalog cannot be parked forever"

# --- A: the runner ------------------------------------------------------------
if grep -q 'setsid --wait timeout -k 10 "\$budget"' "$VP"; then
  ok "A1: every check runs with NO controlling terminal (setsid --wait) and a killable deadline"
else
  bad "A1: $VP does not run checks under 'setsid --wait timeout -k' — a sudo prompt can stop the group again"
fi
if grep -q 'command -v setsid' "$VP" && grep -q 'timeout -k 10' "$VP"; then
  ok "A2: the runner degrades explicitly when setsid is absent (Git Bash/macOS) and still uses -k"
else
  bad "A2: the runner has no documented fallback for a host without setsid"
fi
if grep -q '\[ "\$rc" -eq 137 \]' "$VP"; then
  ok "A3: a KILLed check (rc=137) is reported as TIMEOUT, so it is NAMED instead of silent"
else
  bad "A3: rc=137 is not treated as TIMEOUT — a killed check would look like a mystery FAIL"
fi
if grep -q '< /dev/null 2>&1' "$VP"; then
  ok "A4: the B281 stdin guard is still in place"
else
  bad "A4: the checks lost '< /dev/null' (the B281 hang class is back)"
fi

# --- B: nobody asks for a password -------------------------------------------
# Detector: executed `sudo` with a non-option argument. Comment lines, heredoc
# bodies (operator instructions legitimately show plain `sudo`), quoted message
# text (echo/ok/bad/run_check descriptions, grep patterns) and `sudo -n` are all
# ignored. Built with sprintf("%c") for the quote characters so the awk program
# survives being single-quoted by the shell.
find_executed_bare_sudo() {
  awk '
    BEGIN {
      dq = sprintf("%c", 34); sq = sprintf("%c", 39); bs = sprintf("%c", 92)
      hre = "<<-?[ \t]*[" sq dq bs "]?[A-Za-z_][A-Za-z0-9_]*"
    }
    # remove quoted regions so a `sudo` inside a message/pattern is invisible,
    # while a `sudo` inside $( … ) around a quoted argument still shows up.
    function unquote(s,   out, i, c, q) {
      out = ""; q = ""
      for (i = 1; i <= length(s); i++) {
        c = substr(s, i, 1)
        if (q == "") { if (c == dq || c == sq) { q = c; continue } ; out = out c }
        else if (q == dq && c == bs) { i++ ; continue }
        else if (c == q) { q = "" }
      }
      return out
    }
    function bare(s,   i, off, abs, prev, nxt) {
      off = 1
      while ((i = index(substr(s, off), "sudo ")) > 0) {
        abs = off + i - 1
        prev = (abs > 1) ? substr(s, abs - 1, 1) : ""
        nxt  = substr(s, abs + 5, 1)
        # a real command name starts with a letter/digit/dot/slash/tilde/dollar;
        # `sudo -n`, `sudo >/dev/null` and `sudo` at end of line are not prompts
        if (nxt ~ /[A-Za-z0-9_.\/~$]/ && prev != bs) return 1
        off = abs + 5
      }
      return 0
    }
    {
      line = $0; sub(/^[[:space:]]+/, "", line)
      if (line ~ /^#/) next
      if (here != "") {
        t = $0; sub(/^[[:space:]]+/, "", t)
        if (t == here) here = ""
        next
      }
      if (match($0, hre)) {
        s = substr($0, RSTART, RLENGTH)
        sub(/^<<-?[ \t]*/, "", s)
        gsub(/[^A-Za-z0-9_]/, "", s)
        here = s
        next
      }
      if (bare(unquote($0))) printf "%s:%d: %s\n", FILENAME, FNR, $0
    }
  ' "$@"
}

BARE_OUT="$(find_executed_bare_sudo "$VP" scripts/check_*.sh scripts/verify_post_deploy.sh 2>/dev/null || true)"
if [ -z "$BARE_OUT" ]; then
  ok "B1: no executed bare 'sudo' in the gate scripts (every one is sudo -n)"
else
  bad "B1: these gate lines can stop the catalog on a password prompt:"
  printf '%s\n' "$BARE_OUT" | head -10 | sed 's/^/       /' >&2
fi
# Self-test: the detector must FIND a planted violation and IGNORE a heredoc —
# otherwise "B1 passed" could just mean "the detector is broken".
TMPD="$(mktemp -d 2>/dev/null || echo /tmp/b299.$$)"
mkdir -p "$TMPD"
cat > "$TMPD/planted.sh" <<'PLANT'
#!/usr/bin/env bash
if ! sudo docker info >/dev/null 2>&1; then
  echo "skip"
fi
cat <<'NOTE'
  Rollback: sudo systemctl enable --now derper
NOTE
echo "hint: sudo docker exec headscale headscale users list"
PLANT
SELF_OUT="$(find_executed_bare_sudo "$TMPD/planted.sh" || true)"
if printf '%s' "$SELF_OUT" | grep -q 'if ! sudo docker info' \
   && ! printf '%s' "$SELF_OUT" | grep -q 'Rollback'; then
  ok "B2: the detector finds a planted bare sudo and ignores heredoc/echo mentions"
else
  bad "B2: the detector is unreliable (found: $(printf '%s' "$SELF_OUT" | tr '\n' ' '))"
fi
rm -rf "$TMPD"

# --- C: behaviour under the runner's own shape --------------------------------
RUNNER=""
if command -v timeout >/dev/null 2>&1; then
  if command -v setsid >/dev/null 2>&1 && setsid --help >/dev/null 2>&1; then
    RUNNER="setsid --wait timeout -k 10"
  else
    RUNNER="timeout -k 10"
  fi
fi
if [ -n "$RUNNER" ]; then
  # C1: a check that STOPS ITSELF must still end. (Before B299 the stop reached
  # the timeout process too, and nothing could ever fire again.)
  C1_START=$(date +%s)
  $RUNNER 3 bash -c 'kill -STOP $$' >/dev/null 2>&1
  C1_RC=$?
  C1_ELAPSED=$(( $(date +%s) - C1_START ))
  if { [ "$C1_RC" -eq 124 ] || [ "$C1_RC" -eq 137 ]; } && [ "$C1_ELAPSED" -le 8 ]; then
    ok "C1: a self-stopped check still ends at its budget (rc=$C1_RC, ${C1_ELAPSED}s)"
  else
    bad "C1: a self-stopped check was not bounded (rc=$C1_RC after ${C1_ELAPSED}s)"
  fi
  # C2/C3 need the no-tty guarantee, which only setsid provides.
  case "$RUNNER" in
    setsid*)
      # C2: reading the controlling terminal must fail fast, never stop.
      $RUNNER 3 bash -c 'read -r x < /dev/tty' >/dev/null 2>&1
      C2_RC=$?
      if [ "$C2_RC" -ne 124 ]; then
        ok "C2: a check cannot open a controlling terminal (rc=$C2_RC, no SIGTTIN stop)"
      else
        bad "C2: a terminal read still hangs the check — the gate can be stopped again"
      fi
      # C3: the live shape — a sudo that wants a password. The shim mimics
      # `sudo` reading /dev/tty; with no tty it must die immediately.
      C3_DIR="$(mktemp -d 2>/dev/null || echo /tmp/b299c.$$)"
      mkdir -p "$C3_DIR"
      cat > "$C3_DIR/sudo" <<'SHIM'
#!/usr/bin/env bash
# mimic a password-hungry sudo: it asks the terminal
if read -r pw < /dev/tty 2>/dev/null; then exec "$@"; fi
echo "sudo: a password is required" >&2
exit 1
SHIM
      chmod +x "$C3_DIR/sudo"
      C3_OUT="$(PATH="$C3_DIR:$PATH" $RUNNER 5 bash -c 'sudo docker info' 2>&1)"
      C3_RC=$?
      rm -rf "$C3_DIR"
      if [ "$C3_RC" -ne 124 ]; then
        ok "C3: a password-hungry sudo cannot park the check (rc=$C3_RC: $(printf '%s' "$C3_OUT" | head -1))"
      else
        bad "C3: a password prompt still parked the check — this is the live B299 failure"
      fi
      # C4: no orphaned grandchildren after the deadline.
      pkill -f 'sleep 987' 2>/dev/null
      $RUNNER 2 bash -c 'sleep 987 & sleep 987' >/dev/null 2>&1
      sleep 1
      C4_LEFT="$(pgrep -fc 'sleep 987' 2>/dev/null || true)"
      pkill -f 'sleep 987' 2>/dev/null
      if [ "${C4_LEFT:-0}" -eq 0 ]; then
        ok "C4: the deadline kills grandchildren too (no orphaned checks)"
      else
        bad "C4: ${C4_LEFT} grandchild process(es) survived the deadline"
      fi
      ;;
    *)
      skip "C2/C3/C4: no setsid on this host — the no-tty guarantee cannot be probed here"
      ;;
  esac
else
  skip "C1-C4: no timeout(1) on this host — run the behavioural half on the VM"
fi

# --- D: the live culprit ------------------------------------------------------
if [ -f "$CULPRIT" ]; then
  if grep -q 'sudo -n docker info' "$CULPRIT"; then
    ok "D1: $CULPRIT probes docker non-interactively (the exact line that hung the gate)"
  else
    bad "D1: $CULPRIT does not use 'sudo -n docker info' — the 54-minute hang can return"
  fi
else
  bad "D1: $CULPRIT is missing"
fi
if grep -q 'interactive authentication is required\|SIGTTIN\|54 min' "$VP"; then
  ok "D2: the runner documents the live incident it prevents"
else
  bad "D2: the runner's WHY is gone — the next reader cannot know what it defends against"
fi

# --- E: wiring, docs, tracking -----------------------------------------------
if grep -q 'run_check "B299"' scripts/verify_pre_deploy.sh; then
  ok "E1: registered in the pre-deploy catalogue"
else
  bad "E1: B299 is not registered in scripts/verify_pre_deploy.sh"
fi
if git ls-files --error-unmatch scripts/check_b299_catalog_cannot_hang.sh >/dev/null 2>&1; then
  ok "E2: this script is tracked by git (trap #11)"
else
  bad "E2: scripts/check_b299_catalog_cannot_hang.sh is NOT tracked"
fi
if grep -qE '^## v1\.5\.[0-9]+.*B299' RELEASE-NOTES.md 2>/dev/null; then
  ok "E3: RELEASE-NOTES.md has a release section carrying B299"
else
  bad "E3: RELEASE-NOTES.md has no release section mentioning B299"
fi
if grep -qi 'SIGTTIN\|park the gate\|stopped process group' docs/LESSONS.md docs/internals.md 2>/dev/null; then
  ok "E4: docs/LESSONS.md or docs/internals.md records the failure mode"
else
  bad "E4: the failure mode is not documented"
fi

printf '\n\033[1mB299 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
