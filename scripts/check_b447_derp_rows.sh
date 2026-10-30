#!/usr/bin/env bash
# check_b447_derp_rows.sh — B447 (2026-10-30)
#
# «по derp тесты все еще не проходят при этом два релея почему то одинаковых висят но один
#  рабочий второй с ошибкой по таблице ниже не понять какой из них с ошибкой»
#
# Measured on the reference install:
#
#	derp_relays:  id=2  https://derp.skynas.ru:443    ← the relay
#	              id=3  https://derp.skynas.ru:8443   ← the LAN-direct entry point (B260.2)
#	derp_health:  ONE row for region 900 —
#	              900|1|derp.skynas.ru|https://derp.skynas.ru:443|524ms|healthy|12938 probes
#
# so the table prints the SAME hostname and the SAME verdict twice, and neither column can say
# which of the two rows failed. THREE defects, all fixed here:
#
#   1. The table named a HOST where the row's identity is host:PORT. The identity column now
#      renders the endpoint (derp.RelaysEndpoints), falling back to the host for a row that
#      carries no port.
#   2. `derp.reachable` probed `infos[0]` — the FIRST own relay — so with two rows it judged one
#      and stayed silent about the other. It now probes EVERY enabled row on its OWN port, each
#      with its own budget, and names each failure with its own URL.
#   3. `derp.relay_rows` FAILED on "same host, another port". That pair is the documented
#      LAN+public layout (B260.2 / B401), not a broken relay; only a TRUE duplicate (same host AND
#      same port) or a non-relay row is a failure now.
#
# `derp_health` is keyed by REGION and cannot answer a per-row question, so the per-row verdict
# does not go through it: internal/derphealth/relay_probe_b447.go dials one row at a time (TCP +
# TLS with the hostname as SNI — B289.1) and the relays page renders that verdict in the row.
#
# CONTRACTS: 28.

set -uo pipefail
cd "$(dirname "$0")/.." || exit 1
PASS=0; FAIL=0
ok()  { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; }
hdr() { printf '\n\033[1m%s\033[0m\n' "$*"; }

PROBE=internal/derphealth/relay_probe_b447.go
PROBE_T=internal/derphealth/relay_probe_b447_test.go
ROWS=internal/feature/admin/system_tests_derp_rows_b447.go
OLDT=internal/feature/admin/system_tests_b394_new.go
ADMIN=internal/feature/admin/derp_relays.go
HTML=internal/handlers/templates/admin/derp_relays.html
I18N=internal/i18n/catalog_derp.go
GATE=scripts/verify_pre_deploy.sh
AGENTS=AGENTS.md
for f in "$PROBE" "$PROBE_T" "$ROWS" "$OLDT" "$ADMIN" "$HTML" "$I18N" "$GATE" "$AGENTS"; do
  [ -f "$f" ] || { bad "missing source file: $f"; exit 1; }
done
printf '  DIAG B447: probe=%s rows=%s page=%s\n' "$PROBE" "$ROWS" "$HTML"

mkdir -p "$PWD/tmp" 2>/dev/null || true
WORK="$(mktemp -d "$PWD/tmp/b447.XXXXXX" 2>/dev/null || true)"
[ -n "${WORK:-}" ] && [ -d "$WORK" ] || { bad "cannot create a scratch dir under \$PWD/tmp (B340)"; exit 1; }
trap 'rm -rf "$WORK"' EXIT

has() { case "$(cat "$1" 2>/dev/null)" in *"$2"*) return 0 ;; *) return 1 ;; esac; }

hdr "A. a row is identified by host:port, not by a hostname"
GOFMT_OUT="$(gofmt -l "$PROBE" "$PROBE_T" "$ROWS" "$OLDT" "$ADMIN" 2>/dev/null)"
case "$GOFMT_OUT" in
  "") ok "A1: the touched Go files are gofmt-clean" ;;
  *)  bad "A1: not gofmt-clean: $GOFMT_OUT" ;;
esac
if has "$ADMIN" 'st.Endpoint = fmt.Sprintf("%s:%d", derphealth.RelayHost(st.URL), derphealth.RelayPort(st.URL))'; then
  ok "A2: the identity column carries the row's own port (not just the hostname)"
else
  bad "A2: the page still names a bare host — two rows of one machine stay indistinguishable"
fi
if has "$ADMIN" 'Endpoint string'; then
  ok "A3: the field is part of the row's render model"
else
  bad "A3: no Endpoint field — the template has nothing to print"
fi
if has "$HTML" '{{if .Endpoint}}{{.Endpoint}}{{else}}{{.Host}}{{end}}'; then
  ok "A4: the template renders the endpoint and falls back to the host"
else
  bad "A4: the template does not render the endpoint"
fi
if has "$HTML" 'derp.relays_col_endpoint'; then
  ok "A5: the column header is its own i18n key"
else
  bad "A5: the column header is hardcoded or still says hostname"
fi
if has "$I18N" '"derp.relays_col_endpoint"' && has "$I18N" 'Endpoint (host:port)' && has "$I18N" 'Адрес (хост:порт)'; then
  ok "A6: the header exists in BOTH catalogues (RU + EN)"
else
  bad "A6: derp.relays_col_endpoint is missing on one side"
fi

hdr "B. every row is probed on its own address"
if has "$ROWS" 'derphealth.ProbeTargets(urls, probeHost)' && has "$ROWS" 'derphealth.ProbeRelay('; then
  ok "B1: derp.reachable probes EVERY enabled row instead of the first one"
else
  bad "B1: derp.reachable still judges one relay and stays silent about the other"
fi
if has "$OLDT" 'derp.reachable'; then
  bad "B2: the OLD derp.reachable definition still exists (two tests, one name)"
else
  ok "B2: the old one-row derp.reachable definition is gone"
fi
if has "$OLDT" 'derp.relay_rows'; then
  bad "B3: the OLD derp.relay_rows definition still exists (two tests, one name)"
else
  ok "B3: the old derp.relay_rows definition is gone"
fi
if has "$PROBE" 'port := strconv.Itoa(RelayPort(raw))'; then
  ok "B4: each target keeps the port its own URL declares"
else
  bad "B4: the port is guessed — the 8443 row would be dialled on 443"
fi
if has "$PROBE" 'ServerName:         sniHost'; then
  ok "B5: the hostname is presented as SNI, so the certificate still verifies"
else
  bad "B5: TLS is probed against an address with no SNI — the cert check would fail wrongly"
fi
if has "$PROBE" 'nothing accepts a TCP connection on' && has "$PROBE" 'but the TLS handshake fails'; then
  ok "B6: nothing listening and a broken handshake are different diagnoses"
else
  bad "B6: both failures collapse into one message — the operator's fix differs"
fi
if has "$PROBE" 'if timeout <= 0 {' && has "$PROBE" 'context.WithTimeout(ctx, timeout)'; then
  ok "B7: the budget is per target, so one dead row cannot swallow another's"
else
  bad "B7: a single dead row can consume the whole probe window"
fi
if has "$ADMIN" 'not probed — '; then
  ok "B8: a row that was NOT probed says so instead of borrowing a verdict"
else
  bad "B8: a skipped row silently shows another row's state (the reported symptom)"
fi
if has "$ADMIN" 'st.Probe = fmt.Sprintf("FAIL — %s", reach.Err)'; then
  ok "B9: the failing row carries its own error text in the row"
else
  bad "B9: the failure is not attributable to a row"
fi

hdr "C. two entry points of one machine are not a broken relay"
if has "$ROWS" 'two entry points'; then
  ok "C1: the same host on another port is reported as a second entry point"
else
  bad "C1: the alt-port pair is still treated as an anomaly"
fi
FAIL_LINES="$(grep -n 'return SystemTestFail' "$ROWS" 2>/dev/null)"
case "$FAIL_LINES" in
  *altPorts*)
    bad "C2: an alt-port pair is turned into a failure — the LAN port is a second entry point" ;;
  *)
    if has "$ROWS" 'are NOT DERP endpoints' && has "$ROWS" 'name the SAME host and port'; then
      ok "C2: a row only fails when it is NOT a relay, or when it repeats the SAME host AND port"
    else
      bad "C2: the failure branches no longer name what is actually wrong"
    fi ;;
esac
if has "$ROWS" 'derphealth.RelayHost(r.url), derphealth.RelayPort(r.url)'; then
  ok "C3: duplicate detection compares host:port, not the host alone"
else
  bad "C3: duplicate detection compares hostnames — the LAN port looks like a duplicate"
fi
if has "$ROWS" 'says whether each one answers'; then
  ok "C4: the pass message points at derp.reachable for the per-row answer"
else
  bad "C4: the operator is left without a way to tell the two rows apart"
fi

hdr "D. the probe-host knob still applies (B296 NAT layout)"
if has "$PROBE" 'func ProbeTargets(urls []string, probeHost string) []RelayProbe'; then
  ok "D1: the dial host stays a parameter, not a constant"
else
  bad "D1: the probe-host knob was dropped while fixing the rows"
fi
if has "$PROBE" 'dialHost = h'; then
  ok "D2: a configured probe host replaces the address to dial while the SNI stays the name"
else
  bad "D2: the configured probe host is ignored — a NATed container fails every row again"
fi
if has "$ADMIN" 'probeHost := derpcfg.DialHost(s.dbc())'; then
  ok "D3: the page uses the same resolved probe host as the rest of the DERP checks"
else
  bad "D3: the page invented its own probe-host resolution"
fi

hdr "E. pinned by tests"
if ! command -v go >/dev/null 2>&1; then
  skip "E1: no go toolchain on PATH — cannot run TestB447 here (CI is authoritative)"
elif go test ./internal/derphealth ./internal/feature/admin -run 'TestB447' -count=1 >"$WORK/t.log" 2>&1; then
  ok "E1: TestB447 green (per-row targets, SNI, dead row isolation, skips, loopback pre-check wired)"
else
  bad "E1: TestB447 failed — $(tail -3 "$WORK/t.log" | tr '\n' ' ')"
fi
if ! command -v go >/dev/null 2>&1; then
  skip "E2: no go toolchain on PATH — cannot run the packages here"
elif go test ./internal/derphealth ./internal/feature/admin ./internal/handlers -count=1 >"$WORK/p.log" 2>&1; then
  ok "E2: the touched packages are green"
else
  bad "E2: a package suite failed — $(tail -3 "$WORK/p.log" | tr '\n' ' ')"
fi

hdr "F. registered, tracked, and free of the TD-15 trap"
if git ls-files --error-unmatch scripts/check_b447_derp_rows.sh >/dev/null 2>&1; then
  ok "F1: this script is tracked by git"
else
  bad "F1: this script is not tracked by git"
fi
if grep -qF 'check_b447_derp_rows' "$GATE" 2>/dev/null; then
  ok "F2: registered in scripts/verify_pre_deploy.sh"
else
  bad "F2: not registered in the catalog"
fi
DESC="$(grep -A3 'run_check "B447"' "$GATE" 2>/dev/null)"
case "$DESC" in
  *'`'*) bad "F3: the B447 catalog description contains a backtick (TD-15)" ;;
  "")    bad "F3: the B447 catalog entry was not found" ;;
  *)     ok "F3: the B447 catalog description has no backtick (TD-15)" ;;
esac
if grep -qF '**B447**' "$AGENTS" 2>/dev/null; then
  ok "F4: indexed in AGENTS.md"
else
  bad "F4: no one-line index entry in AGENTS.md"
fi

printf '\n\033[1mB447 summary:\033[0m %d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
