#!/usr/bin/env bash
# check_b356_1_telegram_relay_fallback.sh — B356.1 (2026-10-07).
#
# THE BOT API MUST FALL BACK TO A RELAY TUNNEL WHEN THE PORTAL'S OWN EGRESS IS BLOCKED.
#
# The live incident (measured on the reference deployment, 2026-10-07): the container
# logged this every ~47 seconds forever and EVERY Telegram notification was lost:
#
#   telegram: getUpdates error: Get "https://api.telegram.org/bot<TOKEN>/getUpdates":
#     context deadline exceeded (Client.Timeout exceeded while awaiting headers)
#
# api.telegram.org:443 is blocked from the VM host AND from inside the container (TCP
# connect times out; DNS resolves fine to 149.154.166.110), while three relays on the
# same tailnet (karolina, emilia, sharlotta) reach it. This is the B353 class of
# problem, and the fix is the same shape: a LAST RUNG, reached only when the direct
# path fails.
#
# CONTRACTS
#   A. the fallback is an SSH TUNNEL, not a command run on the relay
#   B. direct first: the fallback is never used while a direct call succeeds
#   C. the existing safety machinery is reused and nothing unvalidated reaches an argv
#   D. observable (ok_relay_tunnel) and switchable (env vars, documented)
#   E. the bot token is redacted on every error path this block touches
#   F. the Go tests actually run
#   G. registration / documentation
#
# Everything that needs the live deployment (a real relay, a real SSH key, a blocked
# egress path) SKIPs here and is verified on the VM; this script never FAILs for a
# missing live dependency.
#
# Usage:  bash scripts/check_b356_1_telegram_relay_fallback.sh
# Exit:   0 = no real failure, 1 = at least one real failure

set -uo pipefail

if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B356.1: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

# AGENTS trap #13: a check NEVER writes a fixed /tmp path (sudo on the reference VM has
# no capabilities, so a stale file owned by another user is read-only and the check
# would report a phantom product failure).
SKY_TMP="$(mktemp -d /tmp/skygate-check.XXXXXX)"
trap 'rm -rf "$SKY_TMP"' EXIT

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

FALLBACK=internal/telegram/egress_fallback.go
DIAL=internal/telegram/relay_dial.go
ARGV=internal/headscale/ssh_tunnel_b356.go
NOTIFY=internal/telegram/notify.go
PROBE=internal/feature/admin/telegram_probe.go
TEMPLATE=internal/handlers/templates/admin/telegram.html
CSS=internal/staticfs/static/css/themes.css
OPERATIONS=docs/operations.md

hdr "B356.1 - the Bot API falls back to a relay tunnel when the portal's egress is blocked"

# --- A: a TUNNEL, not "run curl on the relay" --------------------------------
if [ -f "$FALLBACK" ] && [ -f "$DIAL" ]; then
  ok "A1: the fallback lives in its own files (egress_fallback.go + relay_dial.go)"
else
  bad "A1: the fallback files are missing ($FALLBACK / $DIAL)"
fi

if grep -q 'exec.Command("ssh", argv...)' "$DIAL"; then
  ok "A2: the tunnel is one ssh child process, and the argv comes from the builder"
else
  bad "A2: relay_dial.go no longer starts ssh from the validated argv"
fi

# The relay must NEVER be asked to run a command that mentions the destination: the
# whole point is end-to-end TLS from this process (the token stays out of the relay's
# process list, argv and logs).
if grep -qE '\bcurl\b|\bwget\b|sh -c' "$DIAL" || grep -qE '\bcurl\b|\bwget\b|sh -c' "$ARGV"; then
  bad "A3: the relay path mentions curl/wget/sh -c - the request must be tunnelled, not re-issued on the relay"
else
  ok "A3: no curl/wget/sh -c anywhere on the relay path"
fi

if grep -q 'args = append(args, "-W", dest, "--", relayHost)' "$ARGV"; then
  ok "A4: -W (an OPTION) precedes --, so ssh tunnels instead of running a remote command"
else
  bad "A4: the tunnel argv no longer places -W before -- (ssh would run -W ON the relay)"
fi

for want in '"-o", "ProxyCommand=none"' '"-o", "IdentitiesOnly=yes"' '"-o", "BatchMode=yes"'; do
  if grep -qF "$want" "$ARGV"; then
    ok "A5: the tunnel argv carries the B266 hardening ($want)"
  else
    bad "A5: the tunnel argv lost $want"
  fi
done

if grep -q 'func (c \*tunnelConn) Read(' "$DIAL" && grep -q 'func (c \*tunnelConn) SetDeadline(' "$DIAL"; then
  ok "A6: the ssh child's stdio is a real net.Conn (TLS terminates here, deadlines are honoured)"
else
  bad "A6: tunnelConn is not a net.Conn - the TLS session would not belong to this process (or a deadline would be a no-op)"
fi

if grep -q 'tunnelCloseWait' "$DIAL" && grep -q 'Process.Kill()' "$DIAL"; then
  ok "A7: a wedged ssh is killed after a bounded wait, so a tunnel failure cannot hang"
else
  bad "A7: nothing bounds the ssh child's lifetime"
fi

# --- B: direct first ---------------------------------------------------------
if grep -q 'func newTelegramHTTPClient(' "$FALLBACK" && grep -q 'newTelegramHTTPClient(d, loadEgressConfig())' "$NOTIFY"; then
  ok "B1: the notifier's HTTP client is built with the egress ladder"
else
  bad "B1: NewRealNotifier does not wire the egress ladder into its client"
fi

if grep -q 't.shouldTryDirect(req.Context()' "$FALLBACK"; then
  ok "B2: the direct path is consulted BEFORE any relay attempt"
else
  bad "B2: RoundTrip no longer asks the direct-first predicate"
fi

if grep -q 'if !t.cfg.Enabled {' "$FALLBACK" && grep -q 'return t.directFull.RoundTrip(req)' "$FALLBACK"; then
  ok "B3: the off switch means the direct path only (pre-B356.1 behaviour)"
else
  bad "B3: the off switch no longer short-circuits to the direct transport"
fi

if grep -q 'func selectRelayAttempts(' "$FALLBACK" && grep -q 'RelayCooldown' "$FALLBACK"; then
  ok "B4: relay attempts are selected with a cooldown, so a failing relay is not hot-looped"
else
  bad "B4: selectRelayAttempts/RelayCooldown are gone - a broken relay would be retried in a loop"
fi

if grep -q 'DirectReprobeInterval' "$FALLBACK" && grep -q 'noteDirectSuccess' "$FALLBACK"; then
  ok "B5: a recovered direct path is re-probed and wins again (the tunnel is dropped)"
else
  bad "B5: nothing re-probes the direct path, so a recovered network would keep using a relay"
fi

# --- C: reuse of the existing safety machinery -------------------------------
if grep -q 'IsSafeSSHTarget(hop)' "$ARGV" && grep -q 'SSHTunnelKeyProblem(key)' "$ARGV"; then
  ok "C1: the argv builder gates the relay target (B266) and the key path (B353) itself"
else
  bad "C1: the argv builder no longer validates its inputs"
fi

if grep -q 'func jumpProxyCommandAllowed(' internal/headscale/routes.go \
   && grep -q 'jumpProxyCommandAllowed(p)' "$ARGV"; then
  ok "C2: the key-path gate is the B353 implementation itself, reused (not copied)"
else
  bad "C2: SSHTunnelKeyProblem no longer reuses jumpProxyCommandAllowed"
fi

if grep -q 'headscale.IsSafeSSHTarget(target)' "$DIAL" && grep -q 'headscale.SSHTunnelKeyProblem' "$DIAL"; then
  ok "C3: no candidate enters the dialer without passing both shape gates"
else
  bad "C3: the relay inventory can hand an unvalidated candidate to ssh"
fi

if grep -q 'func orderRelayCandidates(' "$DIAL" && grep -q 'Proven' "$DIAL" && grep -q 'relay_apply_state:' "$DIAL"; then
  ok "C4: candidates are ordered proven-first off the B309 apply record (B353's rule)"
else
  bad "C4: the proven-first ordering or its evidence is missing"
fi

if grep -q 'db.ListExitServers(d)' "$DIAL"; then
  ok "C5: the relay inventory is the same exit_servers table the rest of the project uses"
else
  bad "C5: the inventory no longer reads exit_servers through the db accessor"
fi

# --- D: observable + switchable ---------------------------------------------
if grep -q 'func (n \*RealNotifier) EgressSnapshot()' "$FALLBACK" && grep -q 'telegram.EgressSnapshot' "$PROBE"; then
  ok "D1: the egress state is exported and read by the /admin/telegram probe"
else
  bad "D1: the admin probe cannot see which path the bot is using"
fi

if grep -q 'ProbeOKRelayTunnel' "$PROBE" && grep -q 'ok_relay_tunnel' "$PROBE"; then
  ok "D2: the probe vocabulary gained the honest state ok_relay_tunnel"
else
  bad "D2: the ok_relay_tunnel state is missing from the probe"
fi

if grep -q 'ok_relay_tunnel' "$TEMPLATE" && grep -q 'probe_ok_relay_tunnel_label' "$TEMPLATE"; then
  ok "D3: the page renders the new state (and its i18n label)"
else
  bad "D3: the template does not render ok_relay_tunnel"
fi

if grep -q 'probe-ok-relay-tunnel' "$CSS"; then
  ok "D4: the state has styling (green, its own icon tint)"
else
  bad "D4: no CSS for probe-ok-relay-tunnel - the badge would render unstyled"
fi

for key in probe_ok_relay_tunnel_label; do
  if [ "$(grep -c "telegram.$key\":" internal/i18n/catalog_telegram.go)" -eq 2 ]; then
    ok "D5: the label exists in BOTH i18n catalogues ($key)"
  else
    bad "D5: telegram.$key is not defined twice (RU + EN)"
  fi
done

if grep -q 'SKYGATE_TELEGRAM_RELAY_FALLBACK' "$FALLBACK" && grep -q 'SKYGATE_TELEGRAM_RELAY_FALLBACK' "$OPERATIONS"; then
  ok "D6: the off switch is named in code AND in docs/operations.md"
else
  bad "D6: the off switch is missing from the code or from the documentation"
fi

if grep -q 'SKYGATE_TELEGRAM_PREFERRED_RELAY' "$FALLBACK" && grep -q 'SKYGATE_TELEGRAM_PREFERRED_RELAY' "$OPERATIONS"; then
  ok "D7: the preferred-relay knob is named in code AND in docs/operations.md"
else
  bad "D7: the preferred-relay knob is missing from the code or from the documentation"
fi

if grep -q 'func (t \*egressTransport) logOnce(' "$FALLBACK" && grep -q 'logOnce("relay-in-use:' "$FALLBACK"; then
  ok "D8: the fallback reports itself once per window, not once per attempt"
else
  bad "D8: the fallback's log lines are no longer rate-limited"
fi

if grep -q 'docs/operations.md' "$FALLBACK" || grep -q '8.5' "$FALLBACK"; then
  ok "D9: the code points the operator at the runbook section"
else
  bad "D9: the code does not point at docs/operations.md"
fi

# --- E: token redaction ------------------------------------------------------
if grep -q 'func RedactToken(' "$FALLBACK" && grep -q 'telegramBotTokenRe' "$FALLBACK"; then
  ok "E1: RedactToken removes both the known token and the <bot_id>:<secret> pattern"
else
  bad "E1: RedactToken is missing"
fi

if grep -q 'getUpdates error: %v", redactError(err, token)' "$NOTIFY"; then
  ok "E2: the getUpdates log line (the one that leaked in the incident) redacts"
else
  bad "E2: the getUpdates log line prints the raw error again - it embeds the token"
fi

for site in 'sendMessage HTTP failed: %v", redactError(err, token)' \
            'editMessageText HTTP failed: %v", redactError(err, token)' \
            'answerCallbackQuery failed: %v", redactError(err, token)' \
            'POST %s failed: %v", RedactToken(endpoint, token), redactError(err, token)'; do
  if grep -qF "$site" "$NOTIFY"; then
    ok "E3: redacted error path ($(printf '%.40s' "$site"))"
  else
    bad "E3: an error path lost its redaction: $site"
  fi
done

if grep -q 'redactError(err, token)' internal/telegram/rich.go \
   && grep -q 'redactError(err, token)' internal/telegram/commands_set.go; then
  ok "E4: the rich-message and setMyCommands transport errors are redacted too"
else
  bad "E4: a transport error path still prints the URL (and the token)"
fi

if grep -q 'telegram.RedactToken(err.Error(), token)' "$PROBE"; then
  ok "E5: the admin probe's own error message is redacted (it is rendered on the page)"
else
  bad "E5: the probe can still show the bot token on /admin/telegram"
fi

# A bare `%v", err` in a telegram log line is the shape that leaked: assert none
# remains in the files this block touched.
RAW_ERR_LOGS="$(grep -nE 'log\.Printf\("telegram: (getUpdates error|sendMessage HTTP failed|editMessageText HTTP failed|answerCallbackQuery failed|POST |sendRichMessage transport)' \
  "$NOTIFY" internal/telegram/rich.go internal/telegram/commands_set.go 2>/dev/null \
  | grep -v 'redactError' | grep -v 'RedactToken' || true)"
if [ -z "$RAW_ERR_LOGS" ]; then
  ok "E6: no telegram log line prints a raw transport error any more"
else
  bad "E6: these log lines still print a raw error (the URL embeds the token):"
  printf '%s\n' "$RAW_ERR_LOGS" | sed 's/^/        /'
fi

# --- F: the tests actually run ----------------------------------------------
GO_BIN="$(command -v go 2>/dev/null || command -v go.exe 2>/dev/null || true)"
if [ -z "$GO_BIN" ]; then
  skip "F1: no Go toolchain - the B356.1 unit tests were not run here (they run in the gate on the VM)"
else
  OUT="$("$GO_BIN" test ./internal/telegram/ -run 'TestB356_1' -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT"; then
    ok "F1: the telegram fallback tests pass (path, ladder, argv, redaction)"
  else
    bad "F1: the B356.1 telegram tests failed: $(tail -3 <<< "$OUT" | tr '\n' ' ')"
  fi
  OUT="$("$GO_BIN" test ./internal/feature/admin/ -run 'TestB356_1' -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT"; then
    ok "F2: the admin probe tests pass (ok_relay_tunnel vocabulary + reconciliation)"
  else
    bad "F2: the B356.1 admin tests failed: $(tail -3 <<< "$OUT" | tr '\n' ' ')"
  fi
  OUT="$("$GO_BIN" test ./internal/headscale/ -run 'TestB353|TestB266' -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT"; then
    ok "F3: the reused ssh safety gates (B266/B353) still pass - the tunnel did not weaken them"
  else
    bad "F3: the B266/B353 gate tests failed: $(tail -3 <<< "$OUT" | tr '\n' ' ')"
  fi
fi

LIVE_TESTS="$(grep -c 'func TestB356_1' internal/telegram/relay_dial_test.go internal/telegram/egress_fallback_test.go internal/feature/admin/telegram_probe_b356_1_test.go 2>/dev/null | awk -F: '{s+=$2} END {print s+0}')"
if [ "$LIVE_TESTS" -ge 15 ]; then
  ok "F4: $LIVE_TESTS B356.1 unit tests pin the properties (direct-first, one attempt per candidate, argv, redaction)"
else
  bad "F4: only $LIVE_TESTS B356.1 tests found - the properties are not pinned"
fi

# --- G: registration, documentation, live state ------------------------------
if git ls-files --error-unmatch scripts/check_b356_1_telegram_relay_fallback.sh >/dev/null 2>&1; then
  ok "G1: this script is tracked by git (AGENTS trap #11)"
else
  skip "G1: this script is new and not staged yet - it MUST be committed (AGENTS trap #11: .gitignore can eat a new script silently)"
fi

if grep -q 'check_b356_1_telegram_relay_fallback.sh' scripts/verify_pre_deploy.sh 2>/dev/null; then
  ok "G2: registered in scripts/verify_pre_deploy.sh"
else
  skip "G2: not registered in scripts/verify_pre_deploy.sh yet - registration is the lead's step (the check is inert until then, not a product failure)"
fi

if grep -q '^- \*\*B356.1\*\*' AGENTS.md 2>/dev/null; then
  ok "G3: the AGENTS.md block index carries B356.1"
else
  skip "G3: the AGENTS.md block index has no B356.1 line yet - the lead adds it (TD-15: no backticks in the description)"
fi

if grep -q '^### 8.5' "$OPERATIONS" 2>/dev/null; then
  ok "G4: docs/operations.md has the Telegram relay-fallback section"
else
  bad "G4: docs/operations.md lost (or never got) the relay-fallback section"
fi

hdr "B356.1 summary"
printf 'PASS=%d FAIL=%d SKIP=%d\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
