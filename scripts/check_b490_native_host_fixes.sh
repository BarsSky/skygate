#!/usr/bin/env bash
# B490 (TD-38) — the host-side fixes of the 2026-11-15 migration must live in the REPOSITORY.
#
# The cutover created the reverse proxy, the firewall rules and the maintenance page BY HAND on the
# production panel host, and nothing in the repo could recreate them: a rebuilt host would come up
# with no public entry point, an unreachable relay and a bare proxy error page during updates, and no
# contract would notice. TD-38 registered that; the wizard half shipped in B483 (manual steps) and the
# certificate half in B482. This block is the remaining half: an installer for each artifact.
#
# Contracts:
#   A. the three files exist, are bash-clean, LF-only and git-TRACKED (trap #11: a check that only
#      proves `test -f` proves nothing about the released tag)
#   B. the Caddyfile template carries NO real hostname and no private address — the repository is
#      public and rule 6 forbids it; placeholders are the only names in it
#   C. it preserves the four MEASURED traps (they each cost real time and a "cleanup" would return
#      them): no `versions h2c` for headscale, the maintenance import INSIDE the panel site, the
#      loopback maintenance SERVER at top level and last, and the 130s streaming timeouts
#   D. rendering is behaviourally correct: 60+ lines, every placeholder resolved, and the DERP site is
#      present or absent according to --derp-host (an empty site block fails caddy validate)
#   E. the renderer REFUSES to write a file it does not own without --force (the live Caddyfile holds
#      the operator's own edits) and needs --panel-host / --headscale-host
#   F. the firewall script reports the ports the stack actually needs (443, 8443/tcp, 3478/udp), neve
#      opens the loopback services, and does nothing without --apply
#   G. `install-common.sh` installs the maintenance page as part of an install (it was the only host
#      feature with no repository path at all) and can be told to skip it
#   H. discoverable: registered in the catalog, indexed in AGENTS.md, recorded in the roadmap

set -uo pipefail

PASS=0
FAIL=0
SKIP=0
ok()   { PASS=$((PASS + 1)); echo "  PASS  $*"; }
bad()  { FAIL=$((FAIL + 1)); echo "  FAIL  $*"; }
skip() { SKIP=$((SKIP + 1)); echo "  SKIP  $*"; }

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT" || exit 1

TPL="deploy/caddy/native/Caddyfile.tmpl"
CADDY_SH="deploy/install-caddy-native.sh"
FW_SH="deploy/install-firewall.sh"
COMMON="deploy/install-common.sh"

SKY_TMP="$(mktemp -d "${TMPDIR:-/tmp}/skygate-check.XXXXXX")" || { echo "  FAIL  cannot create a scratch dir"; exit 1; }
trap 'rm -rf "$SKY_TMP"' EXIT

# ═══════════════ A. the files themselves ═══════════════
a_fail=""
for f in "$TPL" "$CADDY_SH" "$FW_SH"; do
    [ -f "$f" ] || a_fail="$a_fail $f(absent)"
done
case "$a_fail" in
    '') ok "A1: the native reverse-proxy artefact, its renderer and the firewall installer all exist" ;;
    *)  bad "A1: missing:$a_fail" ;;
esac

a_fail=""
for f in "$CADDY_SH" "$FW_SH"; do
    bash -n "$f" 2>/dev/null || a_fail="$a_fail $f"
done
case "$a_fail" in
    '') ok "A2: both scripts parse (bash -n)" ;;
    *)  bad "A2: these scripts do not parse:$a_fail" ;;
esac

a_fail=""
for f in "$TPL" "$CADDY_SH" "$FW_SH"; do
    n="$(awk 'index($0, "\r") { n++ } END { print n + 0 }' "$f" 2>/dev/null)"
    [ "${n:-1}" = "0" ] || a_fail="$a_fail $f(${n}CR)"
done
case "$a_fail" in
    '') ok "A3: every new file is LF-only (a CRLF installer dies on the target with a cryptic error)" ;;
    *)  bad "A3: these files carry CR line endings:$a_fail" ;;
esac

a_fail=""
for f in "$TPL" "$CADDY_SH" "$FW_SH"; do
    git ls-files --error-unmatch "$f" >/dev/null 2>&1 || a_fail="$a_fail $f"
done
case "$a_fail" in
    '') ok "A4: git tracks all three (trap #11 — an untracked installer is absent from the released tag)" ;;
    *)  bad "A4: these are NOT tracked by git:$a_fail" ;;
esac

# ═══════════════ B. no real hostname or private address ═══════════════
leak=""
while IFS= read -r line; do
    case "$line" in
        *skynas*|*192.168.*|*100.64.*|*10.0.0.*|*172.16.*) leak="$leak $line" ;;
    esac
done < "$TPL"
case "$leak" in
    '') ok "B1: the template names no real host and no private address (rule 6)" ;;
    *)  bad "B1: the template leaks a real name/address:$leak" ;;
esac

placeholders=0
for p in PANEL_HOST HEADSCALE_HOST PANEL_PORT HEADSCALE_PORT MAINTENANCE_DIR; do
    if grep -q "@@${p}@@" "$TPL" 2>/dev/null; then
        placeholders=$((placeholders + 1))
    fi
done
if [ "$placeholders" -eq 5 ]; then
    ok "B2: every tunable is a placeholder (5 of 5), so one renderer serves any host"
else
    bad "B2: only $placeholders of 5 expected placeholders are present"
fi

# ═══════════════ C. the measured traps stay ═══════════════
# Comments explain the traps and therefore NAME them ("no h2c", "maintenance.caddy"), so every
# assertion here looks at CODE only: a contract that greps the whole file punishes the very
# documentation that keeps the trap from returning (measured while writing this block).
code_only() { grep -v '^[[:space:]]*#' "$1" 2>/dev/null | grep -v '^[[:space:]]*$'; }

case "$(code_only "$TPL")" in
    *'versions h2c'*) bad "C1: the template uses h2c for headscale — Caddy then answers 502 to every request (trap 1)" ;;
    *) ok "C1: headscale stays a plain HTTP/1.1 proxy (trap 1: h2c answers 502)" ;;
esac

# The maintenance import must sit INSIDE the panel site block: in the CODE stream the panel site
# must open first, the import must follow it, and the headscale site must come after the import.
code_lines="$(code_only "$TPL")"
panel_rel="$(printf '%s\n' "$code_lines" | grep -n '@@PANEL_HOST@@' | head -1 | cut -d: -f1)"
maint_rel="$(printf '%s\n' "$code_lines" | grep -n 'import .*maintenance.caddy' | head -1 | cut -d: -f1)"
head_rel="$(printf '%s\n' "$code_lines" | grep -n '@@HEADSCALE_HOST@@' | head -1 | cut -d: -f1)"
if [ -n "$panel_rel" ] && [ -n "$maint_rel" ] && [ -n "$head_rel" ] \
   && [ "$maint_rel" -gt "$panel_rel" ] && [ "$maint_rel" -lt "$head_rel" ]; then
    ok "C2: the maintenance page is imported INSIDE the panel site (trap 4)"
else
    bad "C2: the maintenance import is not inside the panel site block (panel=$panel_rel import=$maint_rel head=$head_rel)"
fi

last_code="$(code_only "$TPL" | tail -1)"
case "$last_code" in
    *'maintenance-server.caddy'*)
        ok "C3: the maintenance static server is the LAST top-level statement (trap 4)" ;;
    *)  bad "C3: the template does not end with the top-level maintenance server import (found: $last_code)" ;;
esac

if grep -q 'read_timeout 130s' "$TPL" 2>/dev/null && grep -q 'write_timeout 130s' "$TPL" 2>/dev/null; then
    ok "C4: the panel block keeps the 130s streaming timeouts"
else
    bad "C4: the panel block lost the streaming timeouts — admin actions and SSE get cut off"
fi

if grep -q 'tls_server_name' "$TPL" 2>/dev/null; then
    ok "C5: the DERP hop pins tls_server_name (trap 3: the relay's own certificate)"
else
    bad "C5: the DERP hop has no tls_server_name — the internal hop stops verifying"
fi

# ═══════════════ D. the renderer actually renders ═══════════════
out="$(bash "$CADDY_SH" --panel-host panel.example.com --headscale-host head.example.com \
    --derp-host derp.example.com --acme-email admin@example.com --dry-run 2>&1)"; rc=$?
lines="$(printf '%s\n' "$out" | wc -l)"
if [ "$rc" -eq 0 ] && [ "$lines" -ge 60 ]; then
    ok "D1: a dry run renders a full file ($lines lines) and exits 0"
else
    bad "D1: the dry run produced $lines line(s) with rc=$rc"
fi
case "$out" in
    *'@@'*'@@'*) bad "D1b: a placeholder SURVIVED the render (Caddy would reject it)" ;;
    *) ok "D1b: no placeholder survives the render" ;;
esac
case "$out" in
    *derp.example.com*) ok "D2: --derp-host produces the relay site" ;;
    *) bad "D2: --derp-host did not produce a relay site" ;;
esac
out_noderp="$(bash "$CADDY_SH" --panel-host panel.example.com --headscale-host head.example.com --dry-run 2>&1)"
case "$out_noderp" in
    *'DERP site omitted'*) ok "D3: without --derp-host the site is omitted, not left empty" ;;
    *) bad "D3: no --derp-host still produced a relay site (an empty site block fails validate)" ;;
esac
case "$out" in
    *'email admin@example.com'*) ok "D4: --acme-email becomes the global ACME option" ;;
    *) bad "D4: --acme-email was not rendered as a global option" ;;
esac

# ═══════════════ E. it refuses what it does not own ═══════════════
bash "$CADDY_SH" --headscale-host head.example.com --dry-run >/dev/null 2>&1; rc=$?
[ "$rc" -eq 1 ] && ok "E1: missing --panel-host is a usage error (exit 1)" || bad "E1: missing --panel-host exited $rc, expected 1"

bash "$CADDY_SH" --panel-host panel.example.com --headscale-host head.example.com --nonsense >/dev/null 2>&1; rc=$?
[ "$rc" -eq 1 ] && ok "E2: an unknown flag is rejected" || bad "E2: an unknown flag exited $rc, expected 1"

foreign="$SKY_TMP/Caddyfile"
printf '# the operator own hand-written config\n' > "$foreign"
if [ "$(id -u)" -eq 0 ]; then
    bash "$CADDY_SH" --panel-host panel.example.com --headscale-host head.example.com \
        --caddyfile "$foreign" --no-maintenance --no-reload >/dev/null 2>&1; rc=$?
    if [ "$rc" -eq 3 ]; then
        ok "E3: a FOREIGN Caddyfile is refused without --force (exit 3) — the operator's edits win"
    else
        bad "E3: a foreign Caddyfile exited $rc, expected 3"
    fi
    if grep -q 'hand-written config' "$foreign"; then
        ok "E4: the refused file was left untouched"
    else
        bad "E4: the refused file was modified anyway"
    fi
else
    skip "E3/E4: overwrite refusal needs root to reach (the script stops at the root check otherwise)"
fi

# ═══════════════ F. the firewall plan ═══════════════
fw="$(bash "$FW_SH" --ssh-port 22 2>&1)"
case "$fw" in
    *443/tcp*) ok "F1: the plan opens 443/tcp" ;;
    *) bad "F1: the plan does not open 443/tcp" ;;
esac
case "$fw" in
    *8443/tcp*) ok "F2: the plan opens the DERP TLS port" ;;
    *) bad "F2: the plan does not open the DERP TLS port" ;;
esac
case "$fw" in
    *3478/udp*) ok "F3: the plan opens STUN/UDP for the bundled relay" ;;
    *) bad "F3: the plan does not open STUN" ;;
esac
case "$fw" in
    *'NOT opened on purpose: 8080'*) ok "F4: the loopback services are explicitly NOT exposed" ;;
    *) bad "F4: the plan does not name the loopback services it refuses to expose" ;;
esac
case "$fw" in
    *'dry run — nothing changed'*) ok "F5: without --apply nothing is changed" ;;
    *) bad "F5: the script did not announce that it changed nothing" ;;
esac
fwj="$(bash "$FW_SH" --json 2>&1)"
case "$fwj" in
    *'"apply":false'*) ok "F6: --json reports apply=false when only asked" ;;
    *) bad "F6: --json did not report apply=false" ;;
esac

# ═══════════════ G. the installer installs the page ═══════════════
if grep -q '^install_maintenance_page()' "$COMMON" 2>/dev/null; then
    ok "G1: install-common.sh defines install_maintenance_page()"
else
    bad "G1: install-common.sh has no maintenance-page step"
fi
if awk '/^enable_and_start_service\(\)/,/^}/' "$COMMON" 2>/dev/null | grep -q 'install_maintenance_page'; then
    ok "G2: the install path CALLS it (every native install gets the page, not just one host)"
else
    bad "G2: install_maintenance_page() is defined but never called"
fi
if awk '/^install_maintenance_page\(\)/,/^}/' "$COMMON" 2>/dev/null | grep -q 'SKYGATE_INSTALL_MAINTENANCE_PAGE'; then
    ok "G3: the step can be switched off (SKYGATE_INSTALL_MAINTENANCE_PAGE=0)"
else
    bad "G3: the step cannot be disabled — a host without Caddy has no way out"
fi
if awk '/^install_maintenance_page\(\)/,/^}/' "$COMMON" 2>/dev/null | grep -q 'command -v caddy'; then
    ok "G4: it checks for Caddy before touching /etc/caddy"
else
    bad "G4: it writes to /etc/caddy without checking that Caddy is there"
fi

# ═══════════════ H. discoverability ═══════════════
if grep -q 'check_b490_native_host_fixes.sh' scripts/verify_pre_deploy.sh 2>/dev/null; then
    ok "H1: the block is registered in scripts/verify_pre_deploy.sh"
else
    bad "H1: the block is not registered in the catalog"
fi
if grep -q '^\- \*\*B490\*\*' AGENTS.md 2>/dev/null; then
    ok "H2: the block index carries its line"
else
    bad "H2: AGENTS.md has no B490 index line"
fi
if grep -q 'B490' docs/ROADMAP.md 2>/dev/null; then
    ok "H3: the roadmap records the block (TD-38 advancement)"
else
    bad "H3: the roadmap does not mention B490"
fi

echo
echo "  B490: $PASS passed, $FAIL failed, $SKIP skipped"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
