#!/usr/bin/env bash
# check_b350_client_dns_truth.sh
#
# 2026-10-04 (B350) — the client must resolve through the tailnet, and the
# panel must not talk it out of that.
#
# WHY THIS EXISTS
# ---------------
# Operator report: «youtube на cyborg всё ещё недоступен» while the SAME rule
# set carried `skyworker`. Every server-side fact was measured and green — 12
# enabled rules for the device, its tenant tag applied, 10 per-CIDR grants
# carrying `via=[tag:dev-infra-emilia]`, prefix_owner 10/10 on that relay, and
# the relay advertising 73 of 73 approved routes with all ten youtube prefixes
# among them. The device answered `curl https://www.youtube.com` →
# `Could not resolve host` and `nslookup www.youtube.com 8.8.4.4` →
# 142.251.15x.4. The missing half was a CLIENT preference: `--accept-dns=false`.
#
# skygate grants access by IP PREFIX, so a rule becomes reachable only once the
# device resolves the domain INTO that prefix. A filtered ISP resolver answers
# NXDOMAIN (or a substituted address), the destination IP never exists on the
# device, and the rule is unobservable — which is why the search went through
# rules, ACLs, routes and prefix_owner, none of which were wrong.
#
# The cause of the misconfiguration was the product itself: /my/exit-rules,
# /my/exit-rules/help, the preauth key page, the device-registration help, both
# i18n catalogues and docs/windows-client.md all printed
# `tailscale up --accept-routes --accept-dns=false`, and the B49 gate contract
# ENFORCED that spelling with a negative lookahead. B49 is renegotiated in the
# same commit (it keeps the historical "no bare short form" intent) and THIS
# script owns the DNS rule.
#
# WHAT CANNOT BE DETECTED (measured, so nobody re-invents it)
# ----------------------------------------------------------
# `--accept-dns` is a client-side preference and headscale does not carry it:
# `host_info.Services` is NOT a proxy — `peerapi-dns-proxy` was advertised by
# ALL 12 nodes, the broken one included. So there is no server-side signal to
# alert on and no page can warn about it; the only defence is a correct default
# plus a named symptom → check → fix. That is what these contracts pin.
#
# CONTRACTS
#   A. no user-facing surface tells a client to use --accept-dns=false
#   B. the i18n client commands (RU + EN) carry --accept-dns=true
#   C. the help page + admin client example carry it
#   D. every first-registration client command in a template carries both flags
#   E. the SERVER-side installers keep --accept-dns=false (the relay is not a client)
#   F. the new warning surface is wired (key defined twice, rendered, safeHTML)
#   G. the docs name the symptom and the fix
#   H. tracked + registered + indexed (AGENTS trap #11, rule 2)

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B350: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

CAT_RULES=internal/i18n/catalog_exit_rules.go
CAT_HELP=internal/i18n/catalog_help.go
CAT_MY=internal/i18n/catalog_my.go
TPL_HELP=internal/handlers/templates/exit_rules_help.html
TPL_RULES=internal/handlers/templates/exit_rules.html
TPL_PREAUTH=internal/handlers/templates/user/preauth_result.html
TPL_DEVICES=internal/handlers/templates/user/devices.html
TPL_EXITNODES=internal/handlers/templates/admin/exit_nodes.html
DOC_CLIENT=docs/windows-client.md
DOC_NET=docs/networking.md

hdr "B350 — the client resolves through the tailnet (and the panel says so)"

# --- A: the reverse guard ----------------------------------------------------
# The whole incident in one line: a user-facing surface told the operator to
# switch the tailnet DNS off. The i18n catalogues and every template are
# user-facing; the relay/module installers are NOT (contract E).
#
# A string MAY name the wrong setting while explaining the failure — the new
# diagnostic texts do exactly that — so only a line that recommends it WITHOUT
# naming the right value anywhere in the same string is a violation.
OFF="$(grep -rn -- '--accept-dns=false' internal/i18n internal/handlers/templates 2>/dev/null | grep -v -- '--accept-dns=true' || true)"
if [ -z "$OFF" ]; then
  ok "A1: no i18n catalogue or template recommends --accept-dns=false"
else
  bad "A1: a user-facing surface still recommends --accept-dns=false (it makes domain rules unobservable on a filtered network):"
  printf '%s\n' "$OFF" | sed 's/^/       /' >&2
fi

# The positive half — the flag must be PRESENT, not merely "not false": a
# deleted flag is the same failure with a different spelling. Only a
# REGISTRATION command (`--login-server` + an auth key) is in scope; prose that
# mentions the flag, `tailscale up --help`, a mobile `--exit-node=`, a re-auth
# line and the subnet-router docs are not.
NODNS="$(grep -rn 'tailscale up' internal/i18n/catalog_*.go 2>/dev/null | grep -- '--login-server' | grep -i 'authkey' | grep -v -- '--accept-dns=true' || true)"
if [ -z "$NODNS" ]; then
  ok "A2: every catalogue registration command names --accept-dns=true"
else
  bad "A2: a catalogue registration command does not say how DNS is resolved:"
  printf '%s\n' "$NODNS" | sed 's/^/       /' >&2
fi

# --- B: the i18n client commands, both languages -----------------------------
# Five keys x 2 languages. A half-edit (RU only) is the realistic mistake.
B_MISS=""
for f in "$CAT_RULES"; do
  for k in how_li3_win win_flow_li3 quick_li4 client_win_cmd client_win_cmd_after; do
    n="$(grep -c "\"exit_rules\.$k\".*--accept-dns=true" "$f" 2>/dev/null || true)"
    [ "${n:-0}" -ge 2 ] || B_MISS="${B_MISS}exit_rules.$k (--accept-dns=true found ${n:-0}x, expected 2: RU+EN) in $f"$'\n'
  done
done
if [ -z "$B_MISS" ]; then
  ok "B1: all five exit_rules client-command keys carry --accept-dns=true in RU and EN"
else
  bad "B1: a client-command key is fixed in one language only (or not at all):"
  printf '%s' "$B_MISS" | sed 's/^/       /' >&2
fi

# The help catalogue key that carries the full command.
H_WIN="$(grep -c 'howworks_win_lin.*--accept-dns=true' "$CAT_HELP" 2>/dev/null || true)"
if [ "${H_WIN:-0}" -ge 2 ]; then
  ok "B2: help.exit_rules_help.howworks_win_lin carries --accept-dns=true in RU and EN"
else
  bad "B2: help.exit_rules_help.howworks_win_lin mentions --accept-dns=true ${H_WIN:-0}x (expected 2: RU+EN)"
fi

# The preauth hint is the first thing a new user reads before copying the key.
PRE_HINT="$(grep -c 'preauth\.linux_accept_routes_hint.*--accept-dns=true' "$CAT_MY" 2>/dev/null || true)"
if [ "${PRE_HINT:-0}" -ge 2 ]; then
  ok "B3: preauth.linux_accept_routes_hint explains --accept-dns=true in RU and EN"
else
  bad "B3: preauth.linux_accept_routes_hint mentions --accept-dns=true ${PRE_HINT:-0}x (expected 2: RU+EN)"
fi

# --- C: the hardcoded surfaces ----------------------------------------------
if grep -q -- '--accept-routes --accept-dns=true' "$TPL_HELP"; then
  ok "C1: the help page's hardcoded client command carries --accept-dns=true"
else
  bad "C1: $TPL_HELP still hands out a client command without --accept-dns=true"
fi
if grep -q -- 'tailscale up --accept-routes --accept-dns=true' "$TPL_EXITNODES"; then
  ok "C2: the admin exit-node tutorial shows the client command with --accept-dns=true"
else
  bad "C2: $TPL_EXITNODES shows a client example without --accept-dns=true"
fi

# --- D: every first-registration client command ------------------------------
# A line that (a) runs `tailscale up`, (b) registers against our control server
# and (c) is not a relay/router install, must name BOTH flags. Re-auth lines
# (no --login-server) and server roles (--advertise-*) are out of scope: a relay
# must keep the resolver of the host it forwards for.
D_MISS="$(grep -rn 'tailscale up' internal/handlers/templates --include='*.html' 2>/dev/null | grep -- '--login-server' | grep -i 'authkey' | while IFS= read -r l; do
  case "$l" in *--advertise-exit-node*|*--advertise-routes*) continue ;; esac
  case "$l" in *--accept-routes*--accept-dns=true*) ;; *) printf '%s\n' "$l" ;; esac
done)"
if [ -z "$D_MISS" ]; then
  ok "D1: every first-registration client command carries --accept-routes and --accept-dns=true"
else
  bad "D1: a registration command would leave the device resolving through its ISP:"
  printf '%s\n' "$D_MISS" | sed 's/^/       /' >&2
fi

# Both halves must survive a template edit: a command with the DNS flag but
# without --accept-routes receives no prefix at all.
D2_MISS="$(grep -rn 'tailscale up' internal/handlers/templates --include='*.html' 2>/dev/null | grep -- '--login-server' | grep -i 'authkey' | while IFS= read -r l; do
  case "$l" in *--advertise-exit-node*|*--advertise-routes*) continue ;; esac
  case "$l" in *--accept-routes*) ;; *) printf '%s\n' "$l" ;; esac
done)"
if [ -z "$D2_MISS" ]; then
  ok "D2: …and every one of them carries --accept-routes"
else
  bad "D2: a registration command names DNS but not the routes (nothing would be installed):"
  printf '%s\n' "$D2_MISS" | sed 's/^/       /' >&2
fi

# --- E: the server side keeps its own DNS -----------------------------------
# Reversing the guard must not sweep the relay/LAN-exit/standby installers: the
# machine that FORWARDS for the tailnet must resolve with its own resolver.
E_MISS=""
for f in deploy/scripts/install-tailscale.sh internal/module/tailscale/install.go scripts/bootstrap_standby.sh internal/feature/admin/cluster_onboard_b342.go; do
  if [ ! -f "$f" ]; then
    E_MISS="${E_MISS}${f} (missing)"$'\n'
    continue
  fi
  grep -q -- '--accept-dns=false' "$f" || E_MISS="${E_MISS}${f} (no --accept-dns=false)"$'\n'
done
if [ -z "$E_MISS" ]; then
  ok "E1: the relay / native-exit / standby installers still use --accept-dns=false"
else
  bad "E1: a SERVER-side installer lost its --accept-dns=false (a router that accepts tailnet DNS resolves through the nodes it serves):"
  printf '%s' "$E_MISS" | sed 's/^/       /' >&2
fi
# And the server-side policy must not leak back into the client docs.
if grep -q -- '--accept-dns=false' "$DOC_CLIENT"; then
  bad "E2: $DOC_CLIENT mentions --accept-dns=false again — the client guide is the canonical client reference"
else
  ok "E2: $DOC_CLIENT contains no --accept-dns=false"
fi

# --- F: the new warning surface is actually wired ----------------------------
F_MISS=""
[ "$(grep -c '"exit_rules\.client_dns_warn"' "$CAT_RULES" 2>/dev/null || echo 0)" -ge 2 ] \
  || F_MISS="${F_MISS}exit_rules.client_dns_warn is not defined in both catalogues"$'\n'
[ "$(grep -c '"help\.exit_rules_help\.pitfall_client_dns"' "$CAT_HELP" 2>/dev/null || echo 0)" -ge 2 ] \
  || F_MISS="${F_MISS}help.exit_rules_help.pitfall_client_dns is not defined in both catalogues"$'\n'
grep -q 'exit_rules.client_dns_warn' "$TPL_RULES" \
  || F_MISS="${F_MISS}exit_rules.client_dns_warn is defined but never rendered (my/exit-rules)"$'\n'
grep -q 'pitfall_client_dns' "$TPL_HELP" \
  || F_MISS="${F_MISS}pitfall_client_dns is defined but never rendered (help page)"$'\n'
# A pitfall value carries <b>/<code>: rendered without safeHTML it prints the
# tags literally, which is how the four older pitfalls looked.
grep -q 'pitfall_dns_fail" | safeHTML' "$TPL_HELP" \
  || F_MISS="${F_MISS}the pitfalls list renders HTML without safeHTML (literal <code> on the page)"$'\n'
if [ -z "$F_MISS" ]; then
  ok "F1: the DNS warning is defined in both languages, rendered, and HTML-rendered safely"
else
  bad "F1: the DNS warning surface is incomplete:"
  printf '%s' "$F_MISS" | sed 's/^/       /' >&2
fi

# --- G: the docs name the symptom and the way out ---------------------------
G_MISS=""
grep -q -- '--accept-dns=true' "$DOC_CLIENT" || G_MISS="${G_MISS}$DOC_CLIENT does not show --accept-dns=true"$'\n'
grep -q 'CorpDNS' "$DOC_CLIENT" || G_MISS="${G_MISS}$DOC_CLIENT does not name the client-side check (tailscale debug prefs → CorpDNS)"$'\n'
grep -q 'tailscale set --accept-dns=true' "$DOC_CLIENT" || G_MISS="${G_MISS}$DOC_CLIENT does not give the one-pref fix (tailscale set --accept-dns=true)"$'\n'
grep -q 'CorpDNS' "$DOC_NET" || G_MISS="${G_MISS}$DOC_NET's symptom table has no client-DNS row"$'\n'
if [ -z "$G_MISS" ]; then
  ok "G1: the client guide and the symptom table carry symptom → check → fix"
else
  bad "G1: the documentation does not let an operator diagnose this without the source:"
  printf '%s' "$G_MISS" | sed 's/^/       /' >&2
fi

# --- H: tracked, registered, indexed, and the lesson recorded ---------------
H_MISS=""
git ls-files --error-unmatch scripts/check_b350_client_dns_truth.sh >/dev/null 2>&1 \
  || H_MISS="${H_MISS}this script is NOT tracked by git (.gitignore can eat it silently — AGENTS trap #11)"$'\n'
grep -q 'check_b350_client_dns_truth.sh' scripts/verify_pre_deploy.sh \
  || H_MISS="${H_MISS}verify_pre_deploy.sh does not register B350 — the contract would never run"$'\n'
grep -q 'B350' AGENTS.md \
  || H_MISS="${H_MISS}AGENTS.md's block index has no B350 entry (AGENTS rule 2)"$'\n'
grep -q 'L-58' docs/LESSONS.md \
  || H_MISS="${H_MISS}docs/LESSONS.md has no L-58 entry (the incident would be re-learned)"$'\n'
if [ -z "$H_MISS" ]; then
  ok "H1: tracked, registered in the catalog, indexed in AGENTS.md, recorded as L-58"
else
  bad "H1: the block bookkeeping is incomplete:"
  printf '%s' "$H_MISS" | sed 's/^/       /' >&2
fi

printf '\n\033[1mB350 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
