#!/usr/bin/env bash
# check_b503_boot_browser_page.sh — B503 (2026-11-20)
#
# The operator applied an update and the browser landed on /admin/update showing a RAW
# JSON document:
#
#	{"build":"v1.5.246","phase":"headscale-client","phase_ms":121,"ready":false,
#	 "stage":"boot","startup_stage":"boot","status":"ok","uptime_ms":155}
#
# That is B269 working as designed: the socket is bound at the TOP of main so an
# updater can verify the build of a process that is still booting, and until the real
# mux takes the listener over, EVERY request — including a browser navigation — got
# the provisional /healthz document. The update had actually succeeded
# (/healthz later reported v1.5.246+<sha>); the page just answered a question nobody
# asked, and a long boot (a migration, a slow headscale) would look like a dead panel.
#
# THE FIX, and its boundary:
#   * a browser navigation (GET/HEAD + Accept: text/html + not a machine path) gets a
#     small self-refreshing HTML page naming the phase, the phase time, the total time
#     and the build;
#   * /healthz, /readyz, /metrics, /api/*, /oidc/*, /.well-known/* keep JSON even when
#     requested WITH a text/html Accept header — that is the updater's rollback
#     decision, and a page-shaped response would be unparseable;
#   * a client that does not ask for HTML (curl sends */*, the updater sends nothing)
#     keeps the JSON, byte-identical;
#   * a FATAL boot keeps 503, names the stored error, and does NOT auto-refresh.
#
# Contracts:
#   A. the negotiation exists and is a pure, testable function
#   B. machine endpoints cannot be talked into HTML
#   C. the page is honest: phase, elapsed, build, and a way to the journal
#   D. a fatal boot does not loop forever
#   E. behaviour: the package tests run (SKIP when there is no Go toolchain)
#   F. discoverable: registered, tracked, indexed, roadmap, release notes

set -uo pipefail

PASS=0
FAIL=0
SKIP=0
ok()   { PASS=$((PASS + 1)); echo "  PASS  $*"; }
bad()  { FAIL=$((FAIL + 1)); echo "  FAIL  $*"; }
skip() { SKIP=$((SKIP + 1)); echo "  SKIP  $*"; }

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT" || exit 1

PAGE=internal/startup/boot_page_b503.go
TEST=internal/startup/boot_page_b503_test.go
HANDLER=internal/startup/startup.go
DOC=docs/troubleshooting.md
AGENTS=AGENTS.md
NOTES=RELEASE-NOTES.md
GATE=scripts/verify_pre_deploy.sh

for f in "$PAGE" "$TEST" "$HANDLER" "$DOC" "$GATE"; do
  [ -f "$f" ] || { bad "missing source file: $f"; echo "B503 summary: $PASS passed, $FAIL failed, $SKIP skipped"; exit 1; }
done

# ── A. the negotiation ───────────────────────────────────────────────────────
if grep -q 'func WantsBootPage(r \*http.Request) bool' "$PAGE"; then
  ok "A1: WantsBootPage is a pure predicate (testable without a server)"
else
  bad "A1: no pure WantsBootPage predicate"
fi
case "$(cat "$HANDLER")" in
  *'if WantsBootPage(r) {'*'serveBootPage(w, r)'*)
    ok "A2: the provisional handler consults it before writing JSON" ;;
  *) bad "A2: StageHandler does not negotiate — a browser still gets the raw JSON" ;;
esac
case "$(cat "$HANDLER")" in
  *'"application/json; charset=utf-8"'*)
    ok "A3: the JSON branch still sets the machine content type" ;;
  *) bad "A3: the JSON branch lost its content type" ;;
esac
# Only navigations: a POST that happens to carry text/html must keep JSON.
case "$(cat "$PAGE")" in
  *'r.Method != http.MethodGet && r.Method != http.MethodHead'*)
    ok "A4: only GET/HEAD can ever be answered with a page" ;;
  *) bad "A4: the method gate is missing" ;;
esac
case "$(cat "$PAGE")" in
  *'text/html'*) ok "A5: the Accept header is what selects the page" ;;
  *) bad "A5: the negotiation ignores the Accept header" ;;
esac

# ── B. machine endpoints cannot be talked into HTML ─────────────────────────
for m in /healthz /readyz /metrics /api/ /oidc/ /.well-known/; do
  if grep -qF "\"$m\"" "$PAGE"; then
    ok "B1: $m is pinned as a machine endpoint"
  else
    bad "B1: $m is not pinned — a browser Accept header could turn it into HTML"
  fi
done
case "$(cat "$TEST")" in
  *'with Accept: text/html answered'*)
    ok "B2: a test asserts the machine endpoints stay JSON even with a text/html Accept" ;;
  *) bad "B2: nothing tests the case that would break the updater" ;;
esac
case "$(cat "$TEST")" in
  *'"*/*"'*) ok "B3: a curl-shaped request (*/*) is pinned to JSON" ;;
  *) bad "B3: no test for a non-browser client" ;;
esac

# ── C. the page is honest ────────────────────────────────────────────────────
for needle in 'Панель перезапускается' 'headscale-client' '/healthz' 'journalctl'; do
  case "$(cat "$PAGE")" in
    *"$needle"*) ok "C: the page names $needle" ;;
    *) bad "C: the page does not mention $needle" ;;
  esac
done
case "$(cat "$PAGE")" in
  *'http-equiv="refresh"'*) ok "C1: the page refreshes itself" ;;
  *) bad "C1: the page does not refresh — the operator must guess when to reload" ;;
esac
# Escaping: the fatal reason comes from an error string and must never be raw HTML.
case "$(cat "$PAGE")" in
  *'html.EscapeString(fatal)'*) ok "C2: the startup error is escaped before rendering" ;;
  *) bad "C2: the fatal reason is interpolated unescaped" ;;
esac
# Bilingual on purpose: i18n catalogues are not loaded yet at this point of the boot.
case "$(cat "$PAGE")" in
  *'The panel is starting up'*) ok "C3: the page carries EN as well as RU (no i18n yet at boot)" ;;
  *) bad "C3: the page is single-language" ;;
esac

# ── D. a fatal boot does not loop forever ───────────────────────────────────
if grep -q 'if fatal == "" {' "$PAGE"; then
  ok "D1: the refresh meta is emitted only while the boot is still progressing"
else
  bad "D1: a fatal boot would auto-refresh forever"
fi
case "$(cat "$PAGE")" in
  *'w.WriteHeader(StatusCode())'*)
    ok "D2: the page keeps the 503/200 status the updater reads" ;;
  *) bad "D2: the page invents its own status code" ;;
esac

# ── E. behaviour ─────────────────────────────────────────────────────────────
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/startup/ -count=1 2>&1)"
  if grep -qE '^(ok|PASS)' <<< "$OUT"; then
    ok "E1: internal/startup tests are green (negotiation, machine paths, fatal boot, HEAD/POST)"
  else
    bad "E1: go test ./internal/startup/ failed:
$(printf '%s\n' "$OUT" | tail -20)"
  fi
else
  skip "E1: no Go toolchain on this host"
fi
case "$(cat "$DOC")" in
  *'"ready":false'*|*'phase_ms'*)
    ok "E2: the documentation quotes the shape the operator actually saw" ;;
  *) bad "E2: $DOC does not show the JSON document the operator was shown" ;;
esac

# ── F. discoverable ─────────────────────────────────────────────────────────
if grep -q 'check_b503_boot_browser_page.sh' "$GATE"; then
  ok "F1: registered in the pre-deploy catalog"
else
  bad "F1: not registered in $GATE"
fi
if git ls-files --error-unmatch scripts/check_b503_boot_browser_page.sh >/dev/null 2>&1; then
  ok "F2: git tracks this script (trap #11)"
else
  bad "F2: this script is not tracked by git (trap #11)"
fi
if grep -q 'B503' "$AGENTS"; then
  ok "F3: the AGENTS block index carries its line"
else
  bad "F3: no B503 line in $AGENTS"
fi
for rd in docs/ROADMAP.md docs/ru/ROADMAP.md; do
  if grep -q 'B503' "$rd"; then
    ok "F4: $rd records the block"
  else
    bad "F4: $rd does not mention B503"
  fi
done
if grep -q 'B503' "$NOTES"; then
  ok "F5: the release notes describe it"
else
  bad "F5: no B503 section in $NOTES"
fi

echo
echo "B503 summary: $PASS passed, $FAIL failed, $SKIP skipped"
[ "$FAIL" -eq 0 ]
