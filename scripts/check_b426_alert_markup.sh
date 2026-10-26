#!/usr/bin/env bash
# check_b426_alert_markup.sh — B426 (2026-10-26)
#
# «В АЛЕРТЕ ПЕЧАТАЕТСЯ СЫРОЙ MARKDOWN». The operator's screenshot of the update notification:
#
#	[#5] 🔔 Skygate update available
#	Running: v1.5.170
#	Latest:  v1.5.171
#	Released: 2026-10-26T16:33:11Z
#
#	## v1.5.171 — the update-available alert arrived ~4 hours late (B425)
#
# Two changes collided: FormatAlert was written when alerts went out as a FENCED PLAIN-TEXT
# block (where `##` at least looked like text), and B421 moved the alert transport to
# parse_mode=HTML — where Markdown is not markup at all, so it arrives as characters. The
# alert also dumped the release body raw and printed the timestamp in RFC 3339.
#
# B426 turns the update notification into a COMPOSED alert (the B391 composer): an i18n
# title, the three facts an operator acts on, and the release body as DETAILS after a pure
# cleaner (no headings/bold/backticks/code fences, links kept as `text (url)`, capped lines
# and characters), with a per-tag dedup key. A sink that only knows SendAlert keeps the legacy
# sentence, so Noop/tests lose nothing.
#
# CONTRACTS (18)
#   A  the composed alert exists and carries title/facts/cleaned details/dedup
#   B  the cleaner is a pure function that drops the shapes this repo publishes
#   C  the monitor prefers the composed path and falls back
#   D  i18n pairs + behaviour + registration
#
# A note on ordering, learned the hard way while shipping this block: the three
# registrations B424/B425/B426 had been appended to the END of the catalog, i.e.
# AFTER the verdict block, so a failing B425 in that very run still produced
# "GATE PASSED ... 0 failed" AND "id(s) not found in the catalog: B424 B425 B426".
# B413 contracts F1/F2 now pin the source order and prove the detector fires.

set -uo pipefail
cd "$(dirname "$0")/.." || exit 1
. "$(dirname "$0")/lib/gosurface.sh"
PASS=0; FAIL=0; SKIP=0
ok()  { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
hdr() { printf '\n\033[1m%s\033[0m\n' "$*"; }

ALERT=internal/release/alert_b426.go
RUN=internal/release/monitor_runner.go
CAT=internal/i18n/catalog_bot.go
GATE=scripts/verify_pre_deploy.sh
for f in "$ALERT" "$RUN" "$CAT" "$GATE"; do
  [ -f "$f" ] || { bad "missing source file: $f"; exit 1; }
done

hdr "A. the update notification is a composed alert"
grep -qF 'func BuildUpdateAlert(' "$ALERT" && ok "A1: BuildUpdateAlert is the one constructor (pure, testable)" || bad "A1: no BuildUpdateAlert"
grep -qF 'TitleKey: UpdateAlertTitleKey' "$ALERT" && ok "A2: the title comes from the catalogue (i18n), not a hardcoded English sentence" || bad "A2: hardcoded title"
n=$(grep -c 'alert.update_available.fact_' "$ALERT"); [ "$n" -eq 3 ] && ok "A3: three facts (running / latest / released), each labelled from the catalogue" || bad "A3: $n fact label(s), expected 3"
grep -qF 'Details:  strings.TrimSpace(cleanReleaseNotes(' "$ALERT" && ok "A4: the body reaches the phone only after cleaning" || bad "A4: the raw body is still used as details"
grep -qF 'DedupKey: "release.update_available." + latest.TagName' "$ALERT" && ok "A5: a per-tag dedup key (a repeated announce of the same tag is suppressible)" || bad "A5: no per-tag dedup"

hdr "B. the cleaner drops the shapes this repo publishes"
for token in 'inFence' 'stripHeading(' 'replaceMarkdownLinks(' 'strings.NewReplacer("**", "", "__", "",'; do
  if grep -qF "$token" "$ALERT"; then ok "B: handles $token"; else bad "B: missing handler for $token"; fi
done
grep -qF 'maxNoteLines' "$ALERT" && grep -qF 'maxNoteChars' "$ALERT" && ok "B5: the excerpt is bounded by lines AND characters" || bad "B5: no bounds"
if awk '/^func cleanReleaseNotes\(/,/^}/' "$ALERT" | grep -qE '\bos\.|http\.|time\.Now'; then
  bad "B6: the cleaner does I/O or reads the clock — it must stay pure"
else
  ok "B6: the cleaner is pure (no clock, no files, no network)"
fi

hdr "C. the monitor prefers composed, keeps the fallback"
grep -qF 'type updateAlertSender interface' "$RUN" && ok "C1: the composed entry point is an optional capability of the sink" || bad "C1: no updateAlertSender"
grep -qF 'm.Notifier.(updateAlertSender)' "$RUN" && ok "C2: the monitor type-asserts before sending" || bad "C2: no type assertion"
grep -qF 'm.Notifier.SendAlert(FormatAlert(current, r))' "$RUN" && ok "C3: a SendAlert-only sink still gets the legacy sentence (no silent loss)" || bad "C3: no fallback"

hdr "D. i18n, behaviour, registration"
pairs=0
for k in alert.update_available.title alert.update_available.fact_running alert.update_available.fact_latest alert.update_available.fact_released; do
  c=$(grep -c "\"$k\"" "$CAT"); if [ "$c" -lt 2 ]; then bad "D1: $k missing in one language"; else pairs=$((pairs+1)); fi
done
[ "$pairs" -eq 4 ] && ok "D1: all four keys exist in RU and EN"
if OUT="$(go test ./internal/release/ -run B426 -count=1 2>&1)"; then
  ok "D2: the cleaner table, the composed shape and the fallback all pass"
else
  bad "D2: go test ./internal/release -run B426: $(printf '%s' "$OUT" | tail -3 | tr '\n' ' ')"
fi
grep -q 'run_check "B426"' "$GATE" && ok "D3: registered in the catalog" || bad "D3: not registered"
grep -q '\*\*B426\*\*' AGENTS.md && ok "D4: AGENTS.md block index carries B426" || bad "D4: no AGENTS entry"

printf '\n\033[1mB426 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
