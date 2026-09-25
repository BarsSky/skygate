#!/usr/bin/env bash
# check_b325_i18n_regressions.sh
#
# 2026-09-25 (B325, v1.5.90) — the panel's localization must not silently regress.
#
# OPERATOR REPORT (verbatim):
#
#   «также по всему проекту страдает локализация — большинство новых описаний не переведено
#    на русский»
#
# WHAT WAS MEASURED (docs/i18n-audit.md, committed with the audit):
#
#   * 311 hardcoded user-visible English strings across 40 templates;
#   * 621 RU catalogue values that are not Russian (ASCII-only, or identical to the EN value);
#   * 6 keys USED in templates but defined in NEITHER catalogue — those pages render the raw
#     key (`admin.subnets.total_devices`) or a raw status token (`user.subnet.cell_disabled`,
#     a family typo) instead of a translation;
#   * `derp.help_title` declared twice with different values, the admin copy silently
#     overridden by the derp catalogue.
#
# The parity test (`TestCatalogsParity`) cannot see any of this: it only compares the KEY SETS,
# so a Russian map full of English passes it happily.
#
# CONTRACTS
#   A. no key may be USED in a template without being defined (that renders the raw key)
#   B. RU values without a single Cyrillic letter stay at or below the frozen budget
#   C. hardcoded user-visible English text nodes stay at or below the frozen budget
#   D. no catalogue key may be declared more than twice (a third copy shadows another silently)
#   E. the parity test and the template parse test pass; both catalogues exist
#   F. this script is tracked by git
#
# The budgets in B/C are a RATCHET: they started at the measured value (196 RU-ASCII values,
# 21 hardcoded strings) and were driven to **0** by the B325.1 sweep. Never raise them: a value
# above 0 on either line means a new untranslated RU string or a new hardcoded English string
# just landed. Remaining (editorial, not gate-visible) localization debt lives in
# docs/i18n-audit.md — that file is the backlog, this script is the floor.

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B325: cannot locate cmd/skygate/main.go from %s\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '\033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

# Frozen budgets (measured 2026-09-25 after the B325.1 sweep; lower, never raise).
# Both are 0 as of v1.5.92: every RU catalogue value carries Cyrillic except the values that are
# deliberately byte-identical to EN (commands and code samples, excluded by the awk filter
# below), and every hardcoded English text node in a template has been replaced by a key.
# 0 means "the next untranslated value or hardcoded string fails the gate immediately".
RU_ASCII_BUDGET="${B325_RU_ASCII_BUDGET:-0}"
HARDCODED_BUDGET="${B325_HARDCODED_BUDGET:-0}"

hdr "B325 — localization must not regress silently"

# --- A: a key used in a template must be defined --------------------------------------
USED=$(grep -rhoE '\{\{-?[[:space:]]*tf?[[:space:]]+"[^"]+"' internal/handlers/templates 2>/dev/null \
  | sed -E 's/.*"([^"]+)"$/\1/' | grep -E '[A-Za-z0-9]' | sort -u)
# A catalogue entry may put whitespace between the key and the colon (the maps are aligned by
# hand), so the pattern must tolerate it — otherwise every aligned key looks undefined.
DEFINED=$(grep -rhoE '"[A-Za-z0-9_.]+"[[:space:]]*:' internal/i18n/*.go 2>/dev/null \
  | sed -E 's/^"//; s/"[[:space:]]*:$//' | sort -u)
MISSING=$(comm -23 <(printf '%s\n' "$USED") <(printf '%s\n' "$DEFINED"))
if [ -z "$MISSING" ]; then
  ok "A1: every key used in a template is defined in a catalogue ($(printf '%s\n' "$USED" | wc -l | tr -d ' ') used)"
else
  bad "A1: key(s) used in templates but defined NOWHERE (the page renders the raw key):"
  printf '%s\n' "$MISSING" | sed 's/^/       /' >&2
fi

# --- B: RU values that are not Russian -------------------------------------------------
# Only the RU maps count: an `ru…` map literal, tracked line by line, and a value with no
# Cyrillic and at least two ASCII words (a one-word value is usually a protocol name).
RU_ASCII=$(awk '
  /^var ru[A-Za-z0-9_]* = map\[string\]string\{/ { inru = 1; next }
  /^var en[A-Za-z0-9_]* = map\[string\]string\{/ { inru = 0; next }
  /^\}/ { inru = 0 }
  inru && /"[^"]+":[[:space:]]*"/ {
    v = $0
    sub(/^[^:]*:[[:space:]]*"/, "", v)
    sub(/",?[[:space:]]*$/, "", v)
    # A copy-paste COMMAND is not untranslated text: keep it byte-identical for the operator
    # (appending a Russian comment would break `exit_rules.client_win_cmd`, a Windows command
    # where `#` is not a comment). Command-shaped values are therefore out of scope here.
    if (v ~ /(^|[[:space:]])(&&|\|\||--[a-z][a-z-]*)/) next
    if (v ~ /^(sudo|echo|tailscale|ssh|scp|systemctl|rc-service|curl|wget|tskey|docker|kubectl|openssl)([[:space:]]|$)/) next
    if (v ~ /^\/[a-z]/) next
    if (v !~ /[А-Яа-яЁё]/ && v ~ /[A-Za-z]+[[:space:]][A-Za-z]+/) n++
  }
  END { print n + 0 }
' internal/i18n/*.go)
if [ "${RU_ASCII:-0}" -le "$RU_ASCII_BUDGET" ]; then
  ok "B1: RU values without Cyrillic = $RU_ASCII (budget $RU_ASCII_BUDGET; see docs/i18n-audit.md §2)"
else
  bad "B1: RU values without Cyrillic = $RU_ASCII, over the frozen budget $RU_ASCII_BUDGET — new untranslated Russian text"
fi

# --- C: hardcoded English text nodes ---------------------------------------------------
HARDCODED=$(grep -rnoE '>[[:space:]]*[A-Z][a-z]+([[:space:]][A-Za-z,.!?:;-]+)+' internal/handlers/templates --include='*.html' 2>/dev/null \
  | grep -v '{{' | grep -vE '>(RSS|HTTP|OIDC|DERP|API|IP|DNS|SSH|URL|UUID|JSON|SQL|Docker|Tailscale|PostgreSQL|Kubernetes|Linux|Windows|macOS|Telegram|Headscale)[< ]' | wc -l | tr -d ' ')
if [ "${HARDCODED:-0}" -le "$HARDCODED_BUDGET" ]; then
  ok "C1: hardcoded English text nodes = $HARDCODED (budget $HARDCODED_BUDGET; audit §1)"
else
  bad "C1: hardcoded English text nodes = $HARDCODED, over the frozen budget $HARDCODED_BUDGET"
  grep -rnoE '>[[:space:]]*[A-Z][a-z]+([[:space:]][A-Za-z,.!?:;-]+)+' internal/handlers/templates --include='*.html' 2>/dev/null \
    | grep -v '{{' | head -8 | sed 's/^/       /' >&2
fi

# --- D: no key declared more than twice (a third copy shadows another) -----------------
DUP=$(grep -rhoE '"[A-Za-z0-9_.]+":' internal/i18n/*.go 2>/dev/null | tr -d '":' | sort | uniq -c \
  | awk '$1 > 2 { print "       " $1 "x " $2 }')
if [ -z "$DUP" ]; then
  ok "D1: no catalogue key is declared more than twice (RU+EN), so nothing is shadowed"
else
  bad "D1: key(s) declared more than twice — the later copy silently wins:"
  printf '%s\n' "$DUP" >&2
fi

# --- E: the tests that DO exist must pass, and both catalogues must be present ---------
if [ -f internal/i18n/catalog_admin.go ] && [ -f internal/i18n/catalog_tailscale.go ]; then
  ok "E1: the split catalogues are present"
else
  bad "E1: a catalogue file is missing"
fi
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/i18n/ -run TestCatalogsParity -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT"; then
    ok "E2: the RU+EN key-set parity test passes"
  else
    bad "E2: parity failed:"; printf '%s\n' "$OUT" | tail -8 | sed 's/^/       /' >&2
  fi
  OUT="$(go test ./internal/handlers/ -run TestLoadTemplates -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT"; then
    ok "E3: every template still parses"
  else
    bad "E3: template parse failed:"; printf '%s\n' "$OUT" | tail -8 | sed 's/^/       /' >&2
  fi
else
  skip "E2/E3: go not on PATH — run them on the VM"
fi

# --- F: git ---------------------------------------------------------------------------
if git ls-files --error-unmatch scripts/check_b325_i18n_regressions.sh >/dev/null 2>&1; then
  ok "F1: this script is TRACKED by git (AGENTS trap #11)"
else
  bad "F1: this script is NOT tracked by git"
fi

printf '\n\033[1mB325 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
printf '  measured: RU-without-Cyrillic=%s (budget %s), hardcoded-English=%s (budget %s)\n' \
  "$RU_ASCII" "$RU_ASCII_BUDGET" "$HARDCODED" "$HARDCODED_BUDGET"
[ "$FAIL" -eq 0 ] || exit 1
