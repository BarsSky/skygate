#!/usr/bin/env bash
# check_b401_derp_truth.sh
#
# 2026-10-20 (B401) — THE RELAY LIST MUST SAY WHAT EACH ROW IS, AND A LOOPBACK
# RESOLUTION MUST NAME THE FIX THAT DOES NOT NEED A COMPOSE EDIT.
#
# WHY (operator, verbatim)
#   «derp в форме пишет что не может skygate воспринимать его информацию нужен debug
#    mode, также в релеи висят еще два не рабочих derp что это и к чему»
#
# MEASURED on his host the same day:
#   derp_relays: 3 rows, only ONE is a relay
#     https://derp.skynas.ru:443                          the relay (healthy, 5 ms)
#     https://derp.skynas.ru:8443                         the LAN-direct HTTP port
#     https://controlplane.tailscale.com/derpmap/default  a DERP MAP DOCUMENT (B265)
#   derp.reachable: FAIL — "derp.skynas.ru" resolves to 127.0.0.1 INSIDE the container
#     because the HOST's /etc/hosts maps its own relay name to loopback (trap #2) and
#     Docker's resolver forwards that into the container
#   metrics bridge: not running, so /debug/vars stays 403 and the tiles read «—»
#
# CONTRACTS
#   A. one classifier decides "is this row a relay", and it is used everywhere
#   B. the page labels the two non-relay shapes (catalogue / alternative port)
#   C. the add/edit forms refuse to store a catalogue URL
#   D. derp.reachable names derp.probe_host FIRST and extra_hosts second
#   E. a system test guards the table, and the whole thing is measured + documented
#
# Scratch under mktemp -d (AGENTS trap #13); no `producer | grep -q` (trap #9).

set -uo pipefail

if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f internal/derphealth/relaykind.go ] || {
  printf 'B401: cannot locate the tree from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

SKY_TMP="$(mktemp -d 2>/dev/null || mktemp -d "${TMPDIR:-/tmp}/skygate-b401.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

KIND=internal/derphealth/relaykind.go
T_KIND=internal/derphealth/relaykind_b401_test.go
REL=internal/feature/admin/derp_relays.go
T_TPL=internal/handlers/templates/admin/derp_relays.html
ST=internal/feature/admin/system_tests_b394_new.go
ANN=internal/feature/admin/system_tests_diagnostics_b394.go
CAT=internal/i18n/catalog_derp.go
CATC=internal/i18n/catalog_common.go

has() { case "$2" in *"$1"*) return 0 ;; *) return 1 ;; esac; }
src() { cat "$1" 2>/dev/null || true; }

KIND_SRC="$(src "$KIND")"; TKIND_SRC="$(src "$T_KIND")"; REL_SRC="$(src "$REL")"
TPL_SRC="$(src "$T_TPL")"; ST_SRC="$(src "$ST")"; ANN_SRC="$(src "$ANN")"
CAT_SRC="$(src "$CAT")"; CATC_SRC="$(src "$CATC")"

hdr "B401 — the relay list must say what each row is"

# ---------------------------------------------------------------------------
# A. one classifier
# ---------------------------------------------------------------------------
hdr "A — one place decides «is this a relay»"

if has 'func ClassifyRelayURL(' "$KIND_SRC"; then
  ok "A1: ClassifyRelayURL exists (kind + reason)"
else
  bad "A1: no shared classifier — three copies can drift again"
fi
for k in RelayKindNode RelayKindDerpMap RelayKindOtherPath; do
  if has "$k" "$KIND_SRC"; then ok "A2: the $k kind is named"; else bad "A2: $k is missing"; fi
done
if has 'func ClassifyAltPort(' "$KIND_SRC"; then
  ok "A3: the same-host/other-port shape has its own predicate (data, not notes text)"
else
  bad "A3: the `:8443` shape cannot be recognised"
fi
if has 'derphealth.ClassifyRelayURL' "$REL_SRC"; then
  ok "A4: the relays page uses it instead of a local copy"
else
  bad "A4: the page still decides on its own"
fi
if has 'func RelayPort(' "$KIND_SRC" && has 'func RelayHost(' "$KIND_SRC"; then
  ok "A5: port and host extraction live with the classifier"
else
  bad "A5: hosts/ports are parsed in yet another place"
fi

# ---------------------------------------------------------------------------
# B. the page labels the two non-relay shapes
# ---------------------------------------------------------------------------
hdr "B — the operator can SEE what a row is"

if has 'Kind      derphealth.RelayKind' "$REL_SRC" && has 'AltPort   bool' "$REL_SRC"; then
  ok "B1: the page model carries the kind and the alternative-port flag"
else
  bad "B1: the page has no way to render the classification"
fi
if has 'derp.relays_kind_derpmap' "$TPL_SRC" && has 'derp.relays_kind_altport' "$TPL_SRC"; then
  ok "B2: both badges are rendered in the relay table"
else
  bad "B2: the table still shows an unexplained row"
fi
n1="$(grep -c '"derp.relays_kind_derpmap"' "$CAT" 2>/dev/null || true)"
n2="$(grep -c '"derp.relays_kind_altport"\|"derp.relays_kind_altport_help"' "$CAT" 2>/dev/null || true)"
if [ "${n1:-0}" -ge 2 ] && [ "${n2:-0}" -ge 4 ]; then
  ok "B3: both labels and their help exist in RU + EN ($n1 / $n2 entries)"
else
  bad "B3: the labels are not in both catalogues ($n1 / $n2)"
fi

# ---------------------------------------------------------------------------
# C. the forms refuse a catalogue URL
# ---------------------------------------------------------------------------
hdr "C — a catalogue URL cannot be stored as a relay"

add_block="$(grep -c 'derphealth.ClassifyRelayURL(row.URL)' "$REL" 2>/dev/null || true)"
if [ "${add_block:-0}" -ge 2 ]; then
  ok "C1: BOTH add and edit refuse a non-node URL ($add_block call sites)"
else
  bad "C1: only $add_block of the two forms validate the URL"
fi
if has 'B401: a DERP MAP DOCUMENT is not a relay' "$REL_SRC"; then
  ok "C2: the refusal carries the reason (the live row it prevents is named)"
else
  bad "C2: the refusal is unexplained"
fi

# ---------------------------------------------------------------------------
# D. derp.reachable names the cheap fix first
# ---------------------------------------------------------------------------
hdr "D — the loopback failure names the fix that needs no compose edit"

if has 'derp.probe_host, B296' "$ST_SRC"; then
  ok "D1: probe_host is offered FIRST (it is a panel field, no recreate)"
else
  bad "D1: the message still only asks for an extra_hosts edit"
fi
if has 'docker compose up -d --force-recreate skygate' "$ST_SRC"; then
  ok "D2: the extra_hosts path names --force-recreate (a restart keeps the frozen hosts file, trap #3)"
else
  bad "D2: the restart trap is not mentioned"
fi
if has 'TLS SNI' "$ST_SRC"; then
  ok "D3: it says why probe_host keeps working (the probes still speak the hostname as SNI)"
else
  bad "D3: the SNI reason is missing"
fi

# ---------------------------------------------------------------------------
# E. guard, tests, documentation
# ---------------------------------------------------------------------------
hdr "E — the table is guarded and the block is documented"

if has 'Name:        "derp.relay_rows"' "$ST_SRC"; then
  ok "E1: a system test guards the relay table"
else
  bad "E1: nothing stops such a row from coming back"
fi
if has 'derphealth.ClassifyRelayURL' "$ST_SRC" && has 'derphealth.ClassifyAltPort' "$ST_SRC"; then
  ok "E2: the test asks the SAME classifier (no second definition of \"relay\")"
else
  bad "E2: the test measures something else"
fi
if has '"derp.relay_rows"' "$ANN_SRC" && has 'st.fix.derp_relay_rows' "$ANN_SRC"; then
  ok "E3: annotated (severity + remediation), so it cannot render bare"
else
  bad "E3: the annotation is missing (the B394 coverage test will fail)"
fi
m="$(grep -c '"st.fix.derp_relay_rows"' "$CATC" 2>/dev/null || true)"
if [ "${m:-0}" -ge 2 ]; then
  ok "E4: the remediation text exists in RU + EN ($m entries)"
else
  bad "E4: the remediation text is not in both catalogues ($m)"
fi
for n in TestB401_ClassifyRelayURL TestB401_RelayPortAndHost TestB401_AltPortIsTheSameHostOnAnotherPort TestB401_RealRelayIsStillARelay; do
  if has "func $n(" "$TKIND_SRC"; then ok "E5: $n pins a shape"; else bad "E5: $n is missing"; fi
done
if git ls-files --error-unmatch scripts/check_b401_derp_truth.sh >/dev/null 2>&1; then
  ok "E6: this script is tracked by git (trap #11)"
else
  bad "E6: this script is not tracked"
fi
if grep -q 'check_b401_derp_truth' scripts/verify_pre_deploy.sh 2>/dev/null; then
  ok "E7: registered in scripts/verify_pre_deploy.sh"
else
  bad "E7: not registered in the catalog"
fi
if grep -q '^\- \*\*B401\*\*' AGENTS.md 2>/dev/null; then
  ok "E8: one-line entry in the AGENTS.md block index"
else
  bad "E8: no B401 entry in the AGENTS block index"
fi
if grep -q 'B401' docs/ROADMAP.md 2>/dev/null && grep -q 'B401' docs/ru/ROADMAP.md 2>/dev/null; then
  ok "E9: both roadmaps record the block (rule 12)"
else
  bad "E9: a roadmap does not record the block"
fi

printf '\n\033[1mB401: %d PASS / %d FAIL / %d SKIP\033[0m\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
