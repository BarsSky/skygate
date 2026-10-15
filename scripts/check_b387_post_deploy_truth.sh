#!/usr/bin/env bash
# check_b387_post_deploy_truth.sh
#
# 2026-10-15 (B387) — "THE DATABASE DID NOT ANSWER" IS NOT "YOUR DATABASE IS CORRUPT".
#
# THE INCIDENT (measured, not hypothesised)
# -----------------------------------------
# Right after the operator applied v1.5.127 to a docker-compose host, the post-deploy
# catalog printed:
#
#   FAIL  R30 skygate.db integrity_check FAILED: psql:error:couldnottranslatehostname
#         "skygate-pg-local"toaddress:Temporaryfailureinnameresolution
#         This is the corruption that triggered the R9 SKIP on 2026-07-30.
#         Run scripts/recover_db_corruption.sh (the .recover-based recovery)
#         to extract the data and rebuild a clean DB.
#
# ...about a database that was PROVABLY healthy in the same session:
#   * pg_isready -> "accepting connections";
#   * acl_snapshots held 2071 rows;
#   * /readyz said healthy:true (db+headscale ok);
#   * /admin/exit-rules served a 611 KB page, /admin/exit-nodes read
#     "политика headscale совпадает с текущей таблицей владельцев", "владелец не
#     объявляет: 0", "3/3 здоровых";
#   * the SAME R30 query answered "ok" when run from a container on the docker network.
#
# THE MECHANISM (two lines)
# -------------------------
#   INTEGRITY=$(psql_vm "SELECT ..." 2>/dev/null | tr -d '[:space:]')
#   INTEGRITY="${INTEGRITY:-fail}"
#
# psql_vm preferred the host's psql (this host has /usr/bin/psql) and handed it the DSN
# hostname verbatim. For a docker-compose install that hostname is a COMPOSE SERVICE
# NAME ("skygate-pg-local") that resolves only INSIDE the docker network, so the host's
# resolver failed, psql's error text became INTEGRITY, and anything that was not "ok"
# was reported as database corruption. R9, R10 and R-B351 all failed the same way —
# and R10 even printed the psql error as the NAME OF A MISSING GRANT.
#
# Two of this repo's own rules were broken at once:
#   * AGENTS rule 1 — a check whose live dependency is unavailable must SKIP, not FAIL;
#   * AGENTS trap #15 — a host transient must never masquerade as a product regression.
#
# WHAT B387 CHANGES
# -----------------
#   1. psql_vm is a LADDER: (a) if the DSN host is a local container, ask THAT container
#      (its own socket — no resolver, no bridge address, no image pull); (b) else the
#      host's psql only when the host really RESOLVES the DSN host; (c) else a throwaway
#      container attached to SKYGATE's OWN network, where a service name resolves. The
#      pre-B387 fallback used --network host — the host's resolver again, i.e. the same
#      bug one rung lower. Every skipped rung names its reason on stderr.
#   2. scripts/lib/post_deploy_truth.sh classifies the read: "no answer" (non-zero rc,
#      empty, or a transport failure) => SKIP naming the reason; a real answer that is
#      not "ok" => FAIL. The corruption verdict is reachable ONLY from a real answer.
#   3. R5/R6/R7 stop asserting the reference deployment's topology. They used to pin
#      the literal 100.64.100.10 and the bridge IP 172.18.0.3, so on the operator's own
#      host (container at 100.64.0.10, headscale reachable as the compose name
#      "headscale", answering {"status":"pass"}) all three FAILed on a healthy tailnet.
#      R5 now DISCOVERS the address and checks the 100.64.0.0/10 range; R6 keys on the
#      discovered address; R7 probes the address the APP is configured with.
#   4. RESULTS_SKIP is initialised and printed. It was incremented at the R26 skip
#      without ever being set, so under `set -u` that path printed
#      "RESULTS_SKIP: unbound variable" and the summary never reported SKIPs at all.
#
# CONTRACTS
#   A. static: the pins and the `:-fail` collapse are GONE from the catalog
#   B. behavioural: the real classifier, executed against planted inputs
#   C. END-TO-END: the REAL catalog, driven against a stubbed host, must report SKIP
#      (and must not say "corrupt") when the database does not answer — and must still
#      be able to report FAIL with the recovery advice when the database DOES answer
#          D. registered, tracked, documented
#
# Scratch files live under mktemp -d (AGENTS trap #13); nothing here needs the network.
# No `producer | grep -q` site exists in this file (AGENTS trap #9 / the B386 class):
# every producer is captured first and matched with a herestring or `case`.

set -uo pipefail

if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B387: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

SKY_TMP="$(mktemp -d 2>/dev/null || mktemp -d "${TMPDIR:-/tmp}/skygate-b387.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

VP="scripts/verify_post_deploy.sh"
LIB="scripts/lib/post_deploy_truth.sh"

for f in "$VP" "$LIB"; do
  [ -f "$f" ] || { printf 'B387: %s is missing\n' "$f" >&2; exit 2; }
done

# Non-comment lines of the catalog (the contracts are about CODE, and the header
# deliberately QUOTES the removed pins to explain why they went).
code_only() { grep -vE '^[[:space:]]*#' "$1"; }

# B387's own trap, paid for by CI on the first push of this block: the assertions below
# used `code_only "$VP" | grep -qF …`, i.e. a producer piped into `grep -q` under
# `set -o pipefail`. `grep -q` exits at the FIRST match and closes the pipe; if the
# producer is still writing, it dies with `grep: write error: Broken pipe` (SIGPIPE) and
# pipefail makes the PIPELINE fail — so a pattern that IS present is reported as absent.
# verify_post_deploy.sh is now ~110 KB, well past the 64 KB pipe buffer, and the A4-A8
# patterns sit near its TOP, so Linux CI failed all five while MSYS accepted them. That is
# AGENTS trap #9, in the check that exists to enforce honesty.
#
# The fix is B386's: capture the producer ONCE and match it without a pipe — `case` for a
# literal, a herestring for a regex. Nothing below pipes into grep -q any more, and A14
# asserts exactly that about this file.
VP_CODE="$(code_only "$VP")"

hdr "B387 — a plumbing failure must never be reported as a product verdict"

# ---------------------------------------------------------------------------
# A. static: the two defects are gone
# ---------------------------------------------------------------------------
A1=$(grep -cE '100\.64\.100\.10|172\.18\.0\.3' <<< "$VP_CODE" || true)
if [ "${A1:-0}" -eq 0 ]; then
  ok "A1: no code line still pins the reference topology (100.64.100.10 / 172.18.0.3)"
else
  bad "A1: ${A1} code line(s) still assert the reference deployment's addresses — R5/R6/R7 will FAIL on any other host"
  grep -nE '100\.64\.100\.10|172\.18\.0\.3' <<< "$VP_CODE" | head -5 >&2
fi

A2=$(grep -cE '\$\{[A-Za-z_]*:-fail\}' <<< "$VP_CODE" || true)
if [ "${A2:-0}" -eq 0 ]; then
  ok "A2: no database answer is defaulted to 'fail' (the corrupt-database collapse)"
else
  bad "A2: ${A2} site(s) still turn a missing answer into 'fail'"
  grep -nE '\$\{[A-Za-z_]*:-fail\}' <<< "$VP_CODE" | head -5 >&2
fi

A3=$(grep -cE 'vpd_query_reached_db|db_expect_ok_verdict' <<< "$VP_CODE" || true)
if [ "${A3:-0}" -ge 3 ]; then
  ok "A3: the DB contracts classify the read (${A3} classification sites: R9/R10/R30/R-B351)"
else
  bad "A3: only ${A3} classification site(s) — the DB contracts are not all covered"
fi

case "$VP_CODE" in
  *'. "$(dirname "$0")/lib/post_deploy_truth.sh"'*)
    ok "A4: the catalog sources the shared classifier (one implementation, not a copy)" ;;
  *)
    bad "A4: the catalog does not source scripts/lib/post_deploy_truth.sh" ;;
esac

if grep -qE '^RESULTS_SKIP=0' <<< "$VP_CODE"; then
  ok "A5: RESULTS_SKIP is initialised (it was incremented under set -u without ever being set)"
else
  bad "A5: RESULTS_SKIP is still never initialised — the R26 skip path aborts with 'unbound variable'"
fi
if grep -qE 'SKIP..NC\}: \$RESULTS_SKIP' <<< "$VP_CODE"; then
  ok "A5b: the summary prints the SKIP count (a run must not hide how much went unverified)"
else
  bad "A5b: the summary does not print RESULTS_SKIP"
fi

# The ladder: the three rungs, in the right ORDER, and no --network host fallback.
# The patterns are the file's LITERAL text: inside psql_vm's double-quoted ssh string the
# remote expansions are escaped, so the source reads \"\$PH\" — the backslash is part of it.
case "$VP_CODE" in
  *'if docker inspect \"\$PH\" >/dev/null 2>&1; then'*)
    ok "A6: rung 1 — the DSN host as a local container is asked first (no resolver involved)" ;;
  *)
    bad "A6: psql_vm does not try the DSN host as a local container" ;;
esac
case "$VP_CODE" in
  *'getent hosts \"\$PH\" >/dev/null 2>&1'*)
    ok "A7: rung 2 — host psql is used only when the host can actually RESOLVE the DSN host" ;;
  *)
    bad "A7: host psql is still used without checking that the host can resolve the DSN host" ;;
esac
case "$VP_CODE" in
  *'NetworkSettings.Networks'*)
    ok "A8: rung 3 — the throwaway container joins skygate's OWN network (where a service name resolves)" ;;
  *)
    bad "A8: the last-resort path does not attach to the docker network that knows the service name" ;;
esac
if grep -qE 'docker run --rm.*--network host' <<< "$VP_CODE"; then
  bad "A9: the throwaway container still uses --network host — the host's resolver, i.e. the same failure one rung lower"
else
  ok "A9: no --network host in the throwaway path"
fi

# A10/A11: the same class, in the credential fetch. It used `ssh -i "$SSH_KEY"` BEFORE
# SSH_KEY was assigned (under `set -u` the substitution dies with "SSH_KEY: unbound
# variable"), so the password was never read and R31/R32/R34 FAILed on a host where
# they should pass — measured 2026-10-15 as exactly two such lines plus a hardcoded
# compose-generated container name.
#
# A10 is about the GUARD, not about file position: SSH_KEY is only assigned ~50 lines
# below, so the credential fetch must use the order-independent `${SSH_KEY:+-i …}` form
# (the idiom ssh_vm already uses). Asserting "no -i \"$SSH_KEY\" above line N" would be
# wrong — `json_field` and the R10 users-file copy legitimately carry the unguarded form
# inside FUNCTION BODIES, which only run long after SSH_KEY exists.
CRED=$(grep -nE 'SKYGATE_ADMIN_(PASS|USER)_FROM_ENV=\$\(ssh' "$VP")
CRED_UNGUARDED=$(printf '%s\n' "$CRED" | grep -c 'ssh -i "\$SSH_KEY"' || true)
if [ -n "$CRED" ] && [ "${CRED_UNGUARDED:-0}" -eq 0 ] \
   && [ "$(printf '%s\n' "$CRED" | grep -c '\${SSH_KEY:+-i "\$SSH_KEY"}')" -eq 2 ]; then
  ok "A10: both credential fetches guard SSH_KEY with \${SSH_KEY:+-i …} (order-independent, no 'unbound variable')"
else
  bad "A10: the credential fetch uses SSH_KEY before it is assigned (unguarded sites: ${CRED_UNGUARDED:-?})"
  printf '        %s\n' "$CRED" >&2
fi

if grep -qF 'docker exec skygate-skygate-1' <<< "$VP_CODE"; then
  bad "A11: a remote docker exec still hardcodes the compose-generated name skygate-skygate-1"
  grep -nF 'docker exec skygate-skygate-1' <<< "$VP_CODE" | head -3 >&2
else
  ok "A11: remote docker exec calls use the resolved container, not a compose-generated name"
fi

# A12/A13: the same class once more, in the HTTPS pair. R22/R23 probed the literal
# placeholder `skygate.example.com` with no override, so every deployment that is not the
# reference one carried a permanent `FAIL R22 … → 000` (and rule 6 forbids committing the
# operator's real name, so the answer is an env var or a SKIP — never a hardcoded host).
# The contract is about a PROBE, not about the string: the SKIP message names the
# placeholder on purpose, so that the operator can see what used to be asserted.
PROBE=$(grep -nE '(curl|openssl|--connect|-connect).*skygate\.example\.com' <<< "$VP_CODE" || true)
if [ -z "$PROBE" ]; then
  ok "A12: no code line PROBES the placeholder public hostname (curl/openssl targets are all variables)"
else
  bad "A12: a code line still probes the placeholder public hostname:"
  printf '        %s\n' "$PROBE" >&2
fi
if grep -qF 'SKYGATE_PUBLIC_HOST' <<< "$VP_CODE" && grep -qE 'SKIP..NC\}: \$RESULTS_SKIP' <<< "$VP_CODE"; then
  ok "A13: R22/R23 take the public hostname from SKYGATE_PUBLIC_HOST and SKIP (counted) when it is unset"
else
  bad "A13: the HTTPS checks are not behind an overridable public hostname with a counted SKIP"
fi

# A14 — THIS FILE. The first CI run of B387 failed A4-A8 with `grep: write error: Broken
# pipe`: the assertions piped a ~110 KB producer into `grep -q`, whose early exit killed the
# producer (SIGPIPE) and pipefail turned that into "the pattern is absent". The token below
# is ESCAPED (`\|`) because in ERE a bare `|` is an alternation with an empty branch, which
# matches every line; the message text deliberately avoids the literal sequence so this
# contract cannot match its own output.
PIPE_TOKEN='\|'
GQ_PAT='grep[[:space:]]+-[A-Za-z]*q'
# Resolve this file's own path robustly (the script may have cd'd to the repo root).
SELF_FILE="$0"
[ -r "$SELF_FILE" ] || SELF_FILE="scripts/check_b387_post_deploy_truth.sh"
SELF_HITS=$(grep -nE "$PIPE_TOKEN[[:space:]]*$GQ_PAT" <<< "$(code_only "$SELF_FILE")" || true)
SELF_PIPES=$(printf '%s' "$SELF_HITS" | grep -c . || true)
if [ "${SELF_PIPES:-0}" -eq 0 ]; then
  ok "A14: this check never pipes a producer into grep -q (the SIGPIPE class that failed its first CI run)"
else
  bad "A14: ${SELF_PIPES} producer-piped site(s) remain IN THIS CHECK — an early match in a large producer reports a present pattern as absent"
  printf '        %s\n' "$SELF_HITS" >&2
fi

# ---------------------------------------------------------------------------
# B. behavioural: the real classifier, against planted inputs
# ---------------------------------------------------------------------------
hdr "B — the classifier itself (the shipped code, executed)"

# shellcheck disable=SC1090
. "./$LIB" || { bad "B0: cannot source $LIB"; }

b_expect() { # b_expect <label> <want-rc 0|1> <rc> <answer>
  local label="$1" want="$2" rc="$3" ans="$4" got=1
  vpd_query_reached_db "$rc" "$ans" && got=0
  if [ "$got" -eq "$want" ]; then
    ok "$label"
  else
    bad "$label (vpd_query_reached_db rc=$rc answer='$(printf '%s' "$ans" | head -c 60)' -> $got, want $want)"
  fi
}

b_expect "B1: a non-zero rc means NO answer (never a verdict)" 1 1 "ok"
b_expect "B2: an empty answer means NO answer" 1 0 ""
b_expect "B3: the MEASURED failure text is recognised as a transport failure" 1 0 'psql: error: could not translate host name "skygate-pg-local" to address: Temporary failure in name resolution'
b_expect "B4: a refused connection is a transport failure" 1 0 "psql: error: connection to server at \"127.0.0.1\", port 5432 failed: Connection refused"
b_expect "B5: an authentication failure is a transport failure" 1 0 'psql: error: connection to server failed: FATAL:  password authentication failed for user "skygate"'
b_expect "B6: a missing DSN is a transport failure" 1 0 "psql_vm: SKYGATE_DB_DSN not set in .env"
b_expect "B7: a missing psql is a transport failure" 1 0 "psql: command not found"
b_expect "B8: an unreadable docker socket is a transport failure" 1 0 "Cannot connect to the Docker daemon at unix:///var/run/docker.sock"
b_expect "B9: a REAL answer is accepted" 0 0 "ok"
b_expect "B10: the real answer 'fail' is accepted (it is a finding, not a plumbing fault)" 0 0 "fail"
b_expect "B11: an answer that merely CONTAINS 'ok' is accepted as an answer" 0 0 "notok"

# The corruption verdict must require the literal "ok" as the answer.
if vpd_ok_answer "ok" && ! vpd_ok_answer "fail" && ! vpd_ok_answer "psql: error: could not translate host name \"x\""; then
  ok "B12: only the literal answer 'ok' is a PASS — 'fail' and error text are not"
else
  bad "B12: vpd_ok_answer accepts something other than the literal 'ok'"
fi

# The reason line must distinguish "the checker could not read" from "the DB is wrong".
R_EMPTY=$(vpd_no_answer_reason 1 "")
R_RESOLV=$(vpd_no_answer_reason 0 'psql: error: could not translate host name "skygate-pg-local" to address: Temporary failure in name resolution')
case "$R_EMPTY" in
  *EMPTY*|*empty*) ok "B13: an empty read is reported as a fact about the CHECKER (rc named)" ;;
  *) bad "B13: an empty read is not named as a checker-side fact: $R_EMPTY" ;;
esac
case "$R_RESOLV" in
  *"container name"*|*"docker network"*) ok "B14: the resolution failure names the container-name cause and says it is NOT a database verdict" ;;
  *) bad "B14: the resolution failure is not explained: $R_RESOLV" ;;
esac

# The tailnet address check: the RANGE, not somebody's octet.
tn_ok=1
for a in 100.64.0.10 100.64.100.10 100.127.255.254; do vpd_is_tailnet_ipv4 "$a" || tn_ok=0; done
if [ "$tn_ok" -eq 1 ]; then
  ok "B15: 100.64.0.10 / 100.64.100.10 / 100.127.255.254 are all inside the tailnet range (the operator's address is accepted)"
else
  bad "B15: a valid tailnet address was rejected"
fi
tn_bad=1
for a in 100.63.0.1 100.128.0.1 10.0.0.1 192.168.13.1 "" "not-an-ip" 100.64.0; do
  vpd_is_tailnet_ipv4 "$a" && tn_bad=0
done
if [ "$tn_bad" -eq 1 ]; then
  ok "B16: non-tailnet and malformed values are rejected (100.63/100.128/10.x/empty/garbage)"
else
  bad "B16: vpd_is_tailnet_ipv4 accepted a value outside 100.64.0.0/10"
fi

PICK=$(vpd_pick_self_ipv4 <<< $'\n100.64.0.10\nfd7a:115c:a1e0::a\n')
if [ "$PICK" = "100.64.0.10" ]; then
  ok "B17: the self address is discovered from 'tailscale ip -4' output (blank lines and IPv6 skipped)"
else
  bad "B17: vpd_pick_self_ipv4 returned '$PICK', want 100.64.0.10"
fi
if ! vpd_pick_self_ipv4 <<< $'fd7a:115c:a1e0::a'; then
  ok "B18: an IPv6-only feed yields nothing (so the caller SKIPs instead of inventing an address)"
else
  bad "B18: an IPv6-only feed produced an address"
fi

H1=$(vpd_headscale_health_url "")
H2=$(vpd_headscale_health_url "http://headscale:50444/")
H3=$(vpd_headscale_health_url "http://headscale:50444/health")
H4=$(vpd_headscale_health_url "http://127.0.0.1:5000")
if [ "$H1" = "http://headscale:50444/health" ] && [ "$H2" = "http://headscale:50444/health" ] \
   && [ "$H3" = "http://headscale:50444/health" ] && [ "$H4" = "http://127.0.0.1:5000/health" ]; then
  ok "B19: the headscale probe URL follows the app's configured address (empty -> compose name; no doubled /health)"
else
  bad "B19: vpd_headscale_health_url: empty='$H1' slash='$H2' health='$H3' haproxy='$H4'"
fi

# ---------------------------------------------------------------------------
# C. end-to-end: the REAL catalog against a stubbed host
# ---------------------------------------------------------------------------
hdr "C — end-to-end: the real catalog, driven against a stubbed host"

mkdir -p "$SKY_TMP/bin"

# The stub answers the remote commands the catalog issues. It exists to reproduce the
# MEASURED host exactly: /usr/bin/psql present, the DSN host a name the host cannot
# resolve, psql printing an error AND exiting 0 (which is what the live host did).
cat > "$SKY_TMP/bin/ssh" <<'STUB'
#!/usr/bin/env bash
cmd="$*"
# The DSN host as a local container: rung 1's probe must succeed so the ladder takes it.
case "$cmd" in
  *pg_tables*) printf '%s' "$VPD_FAKE_DB_ANSWER"; exit 0 ;;
  *applied_success=1*) echo "1760546400"; exit 0 ;;
  *acl_snapshots*) echo "1760546400 1"; exit 0 ;;
  *portal_users*) echo "skyadmin"; exit 0 ;;
  *"COUNT(*) FROM device_rules"*) echo "0"; exit 0 ;;
esac
case "$cmd" in
  *"ip -4"*) echo "100.64.0.10"; exit 0 ;;
  *"pgrep tailscaled"*) exit 0 ;;
  *"docker exec"*status*) echo "100.64.0.10  skygate-host  tagged-devices  linux  -  "; exit 0 ;;
  *wget*) echo '{"status":"pass"}'; exit 0 ;;
  *healthz*) echo "{\"build\":\"vTEST+${VPD_FAKE_SHA}\",\"status\":\"ok\"}"; exit 0 ;;
  *readyz*)  echo '{"healthy":true,"db":"ok","headscale":"ok"}'; exit 0 ;;
  *"api/v1/policy"*) echo '{"policy":"{\"grants\":[]}","updatedAt":"2026-10-15T00:00:00Z"}'; exit 0 ;;
  *"api/v1/node"*)   echo '{"nodes":[]}'; exit 0 ;;
  *"docker ps"*)     echo "0123456789abcdef"; exit 0 ;;
  *"tailscale status --json"*) echo '{"BackendState":"Running"}'; exit 0 ;;
esac
exit 0
STUB
chmod +x "$SKY_TMP/bin/ssh"
# scp must not reach out: fail fast, the catalog tolerates it.
printf '#!/usr/bin/env bash\nexit 1\n' > "$SKY_TMP/bin/scp"
chmod +x "$SKY_TMP/bin/scp"

FAKE_SHA=$(git rev-parse --short HEAD 2>/dev/null || echo "0000000")
MEASURED='psql: error: could not translate host name "skygate-pg-local" to address: Temporary failure in name resolution'

run_catalog() { # run_catalog <db-answer> -> log path
  local ans="$1" log="$2"
  PATH="$SKY_TMP/bin:$PATH" \
  VPD_FAKE_DB_ANSWER="$ans" \
  VPD_FAKE_SHA="$FAKE_SHA" \
  SSH_HOST="vpdstub@vpd.invalid" \
  timeout 300 bash "$VP" --quick > "$log" 2>&1
  return 0
}

# C1-C3: the three planted database answers.
run_catalog "$MEASURED" "$SKY_TMP/unreachable.log"
run_catalog "ok"       "$SKY_TMP/healthy.log"
run_catalog "fail"     "$SKY_TMP/corrupt.log"

LOG_UNREACH="$(cat "$SKY_TMP/unreachable.log")"
LOG_HEALTHY="$(cat "$SKY_TMP/healthy.log")"
LOG_CORRUPT="$(cat "$SKY_TMP/corrupt.log")"

r30_row() { # r30_row <log-text> -> the R30 verdict line, stripped of colour
  printf '%s\n' "$1" | sed -e 's/\x1b\[[0-9;]*m//g' | grep -E '^  (PASS|FAIL|SKIP) +R30' | head -1
}

ROW_UNREACH=$(r30_row "$LOG_UNREACH")
ROW_HEALTHY=$(r30_row "$LOG_HEALTHY")
ROW_CORRUPT=$(r30_row "$LOG_CORRUPT")

case "$ROW_UNREACH" in
  *SKIP*R30*) ok "C1: an UNREACHABLE database is reported as SKIP — not as corruption   [$ROW_UNREACH]" ;;
  *) bad "C1: the unreachable-database run did not SKIP R30 (row: '$ROW_UNREACH')" ;;
esac
case "$LOG_UNREACH" in
  *recover_db_corruption*)
    bad "C1b: the run still tells the operator to rebuild the database (recover_db_corruption) although the DB never answered" ;;
  *) ok "C1b: no database-recovery advice is emitted when the database never answered" ;;
esac
case "$LOG_UNREACH" in
  *"NOT a corruption verdict"*) ok "C1c: the SKIP says in words that it is not a corruption verdict" ;;
  *) bad "C1c: the SKIP does not disclaim the corruption reading" ;;
esac
case "$LOG_UNREACH" in
  *"could not translate host name"*) ok "C1d: the SKIP names the reason it could not read (the resolver), so the operator can act" ;;
  *) bad "C1d: the SKIP does not name the cause of the failed read" ;;
esac

case "$ROW_HEALTHY" in
  *PASS*R30*) ok "C2: a healthy database still PASSes   [$ROW_HEALTHY]" ;;
  *) bad "C2: the healthy run did not PASS R30 (row: '$ROW_HEALTHY')" ;;
esac

case "$ROW_CORRUPT" in
  *FAIL*R30*) ok "C3: a database that really ANSWERS 'fail' is still a FAIL   [$ROW_CORRUPT]" ;;
  *) bad "C3: a genuinely incomplete schema was not reported as FAIL (row: '$ROW_CORRUPT')" ;;
esac
case "$LOG_CORRUPT" in
  *recover_db_corruption*) ok "C3b: the corruption verdict (with the recovery script) is still reachable — from a real answer only" ;;
  *) bad "C3b: the real-corruption case lost its recovery advice" ;;
esac

# C4: the summary must report SKIPs (it counted them and never printed the count).
SUM_UNREACH=$(printf '%s\n' "$LOG_UNREACH" | sed -e 's/\x1b\[[0-9;]*m//g' | grep -E '^  SKIP *: [0-9]+' | head -1)
case "$SUM_UNREACH" in
  "  SKIP: "*) ok "C4: the summary reports the SKIP count   [$SUM_UNREACH]" ;;
  *SKIP*) ok "C4: the summary reports the SKIP count   [$SUM_UNREACH]" ;;
  *) bad "C4: the summary does not report SKIPs (got: '$SUM_UNREACH')" ;;
esac

# C5-C7: the topology contracts must PASS on a host that is NOT the reference VM.
ROW_R5=$(printf '%s\n' "$LOG_UNREACH" | sed -e 's/\x1b\[[0-9;]*m//g' | grep -E '^  (PASS|FAIL|SKIP) +R5 ' | head -1)
ROW_R6=$(printf '%s\n' "$LOG_UNREACH" | sed -e 's/\x1b\[[0-9;]*m//g' | grep -E '^  (PASS|FAIL|SKIP) +R6 ' | head -1)
ROW_R7=$(printf '%s\n' "$LOG_UNREACH" | sed -e 's/\x1b\[[0-9;]*m//g' | grep -E '^  (PASS|FAIL|SKIP) +R7 ' | head -1)
case "$ROW_R5" in
  *PASS*100.64.0.10*) ok "C5: R5 PASSes on a host whose tailnet address is 100.64.0.10 (not the pinned 100.64.100.10)   [$ROW_R5]" ;;
  *) bad "C5: R5 does not accept a non-reference tailnet address (row: '$ROW_R5')" ;;
esac
case "$ROW_R6" in
  *PASS*) ok "C6: R6 keys on the DISCOVERED address   [$ROW_R6]" ;;
  *) bad "C6: R6 did not pass with a discovered address (row: '$ROW_R6')" ;;
esac
case "$ROW_R7" in
  *PASS*headscale*) ok "C7: R7 probes headscale at the address the APP uses (the compose name), not a bridge IP   [$ROW_R7]" ;;
  *) bad "C7: R7 did not pass against the app's own headscale address (row: '$ROW_R7')" ;;
esac

# C8: the check must FAIL the run for a real FAIL — otherwise the fix could pass by
# silencing everything. The corrupt run's rc must be non-zero.
PATH="$SKY_TMP/bin:$PATH" VPD_FAKE_DB_ANSWER="fail" VPD_FAKE_SHA="$FAKE_SHA" \
  SSH_HOST="vpdstub@vpd.invalid" timeout 300 bash "$VP" --quick >/dev/null 2>&1
RC_CORRUPT=$?
if [ "$RC_CORRUPT" -ne 0 ]; then
  ok "C8: the catalog still exits non-zero when a real check FAILs (rc=$RC_CORRUPT)"
else
  bad "C8: the catalog exited 0 on a run containing a real FAIL — the SKIP change silenced the verdict"
fi

# ---------------------------------------------------------------------------
# D. registered, tracked, documented
# ---------------------------------------------------------------------------
hdr "D — the block is wired in"

if git ls-files --error-unmatch "$LIB" >/dev/null 2>&1; then
  ok "D1: $LIB is TRACKED by git (trap #11 — a file the check needs cannot be merely on this disk)"
else
  bad "D1: $LIB is not tracked by git (it may be gitignored and absent from the release)"
fi
if git ls-files --error-unmatch "scripts/check_b387_post_deploy_truth.sh" >/dev/null 2>&1; then
  ok "D2: this check script is tracked by git"
else
  bad "D2: scripts/check_b387_post_deploy_truth.sh is not tracked by git"
fi
if grep -qF 'check_b387_post_deploy_truth.sh' scripts/verify_pre_deploy.sh; then
  ok "D3: registered in scripts/verify_pre_deploy.sh (rule 2)"
else
  bad "D3: not registered in scripts/verify_pre_deploy.sh — the gate will never run it"
fi
if grep -qE '^- \*\*B387\*\*' AGENTS.md; then
  ok "D4: one-line index entry in AGENTS.md (rule 2)"
else
  bad "D4: no B387 entry in the AGENTS.md block index"
fi
B387_DESC=$(grep -F 'run_check "B387"' scripts/verify_pre_deploy.sh || true)
case "$B387_DESC" in
  *'`'*) bad "D5: the run_check description contains a backtick — it is evaluated by bash (TD-15)" ;;
  "")    bad "D5: no run_check \"B387\" line found" ;;
  *)     ok "D5: the run_check description carries no backtick (TD-15)" ;;
esac

echo
printf 'B387: %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
