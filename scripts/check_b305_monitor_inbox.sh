#!/usr/bin/env bash
# check_b305_monitor_inbox.sh
#
# 2026-09-23 (B305, v1.5.70) — the monitoring inbox.
#
# Operator ask: «Отдельно следует пройтись по тестам системы и расширить их давая
# возможность полностью контролировать модули проекта и получать уведомления по
# неисправности или некорректном поведении. Также необходимо добавить поле с
# уведомлениями куда будут приходить все сообщения разной важности с мониторинга
# skygate.»
#
# Before this block every monitoring signal lived somewhere else and most were
# fire-and-forget: an exit-node health crossing went to Telegram and vanished, a
# tag-reconcile failure was a metric plus an audit row, a failing system test was
# rendered once and forgotten, a degraded database was a badge on one page. Nothing
# could answer "what is wrong right now, and is it new?".
#
# B305 adds the store + the policy + the page, and wires the first two producers:
#   * v0.76 monitor_events (BOTH migration chains) with a UNIQUE fingerprint —
#     a recurring condition bumps repeats/last_seen instead of adding a row;
#   * internal/monitorinbox: four severities, dedup-aware Report that tells the
#     caller whether the event is new/reopened (the only case worth paging for),
#     Resolve for recovery, and a Telegram threshold stored in global_settings;
#   * /admin/monitor: list + severity/state/source filters + ack + ack-all + the
#     threshold form, in the sidebar's health section;
#   * producers: the system tests (a FAIL opens an event, a PASS resolves it) and
#     the tag reconciler's alert sink.
#
# CONTRACTS
#   A. the table exists in BOTH chains and both drivers register v0.76
#   B. the store dedups, resolves and acks correctly
#   C. the inbox policy: severities, threshold, no re-page for a known problem
#   D. the page exists, is routed, is in the sidebar, and is fully translated
#   E. the producers are wired
#   F. the Go tests exist and pass
#   G. this script is tracked by git

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B305: cannot locate cmd/skygate/main.go from %s\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

MIG=internal/db/migrations_v0_76_monitor_events.go
STORE=internal/db/monitor_events.go
INBOX=internal/monitorinbox/monitorinbox.go
PAGE=internal/feature/admin/monitor.go
TPL=internal/handlers/templates/admin/monitor.html
LAYOUT=internal/handlers/templates/layout.html
RU=internal/i18n/catalog_admin.go
MAIN=cmd/skygate/main.go
SYSTESTS=internal/feature/admin/system_tests.go
ADOPT=internal/feature/admin/adopt_devices.go

hdr "B305 — one monitoring inbox for every skygate signal"

# --- A: the table, both chains ------------------------------------------------
if grep -q 'monitorEventsTableSQLiteSQL' "$MIG" && grep -q 'monitorEventsTablePGSQL' "$MIG" \
   && grep -q 'func migrateV076PG' "$MIG" && grep -q 'func migrateV076SQLite' "$MIG"; then
  ok "A1: v0.76 defines both dialects"
else
  bad "A1: the migration is single-dialect (AGENTS rule 9)"
fi
if grep -q 'execSQLiteDDL(d, \[\]string{' "$MIG"; then
  ok "A2: the SQLite side goes through the DDL chokepoint"
else
  bad "A2: the SQLite side bypasses execSQLiteDDL"
fi
if grep -q 'migrateV076PG' internal/db/driver_postgres.go && grep -q 'migrateV076SQLite' internal/db/driver_sqlite.go; then
  ok "A3: both drivers register v0.76"
else
  bad "A3: v0.76 is not registered in both migration lists"
fi
if grep -q 'idx_monitor_events_fingerprint' "$MIG" && grep -q 'UNIQUE' "$MIG"; then
  ok "A4: fingerprint is UNIQUE (the dedup key is enforced by the schema)"
else
  bad "A4: nothing enforces one row per fingerprint"
fi

# --- B: the store --------------------------------------------------------------
if grep -q 'ON CONFLICT(fingerprint) DO UPDATE SET' "$STORE" \
   && grep -q 'repeats = monitor_events.repeats + 1' "$STORE"; then
  ok "B1: a repeat updates one row and bumps the counter"
else
  bad "B1: a repeat would create a second row"
fi
if grep -q 'func ResolveMonitorEvent' "$STORE" && grep -q "state = 'resolved'" "$STORE"; then
  ok "B2: recovery closes the event without deleting history"
else
  bad "B2: there is no resolution path"
fi
if grep -q "state = 'acked'" "$STORE" && grep -q 'func AckAllOpenMonitorEvents' "$STORE"; then
  ok "B3: acknowledging removes an event from the unhandled count"
else
  bad "B3: ack does not change the state (the badge would never clear)"
fi
if grep -q 'func ListMonitorEvents' "$STORE" && grep -q 'func CountMonitorEventsByState' "$STORE"; then
  ok "B4: the page's list + counter queries exist"
else
  bad "B4: the store is missing the read path"
fi

# --- C: the policy -------------------------------------------------------------
for sev in info warning error critical; do
  if grep -q "Severity$(printf '%s' "$sev" | cut -c1 | tr 'a-z' 'A-Z')$(printf '%s' "$sev" | cut -c2-)" "$INBOX"; then
    ok "C1: severity $sev exists"
  else
    bad "C1: severity $sev is missing"
  fi
done
if grep -q 'func Normalize(raw string) Severity' "$INBOX" && grep -q 'func SeverityRank' "$INBOX"; then
  ok "C2: producer spellings normalise onto the four levels"
else
  bad "C2: severity normalisation is missing (a typo would create a fifth level)"
fi
if grep -q 'SettingNotifyMinSeverity' "$INBOX" && grep -q 'db.GetGlobalSetting(in.DB.Current(), SettingNotifyMinSeverity' "$INBOX"; then
  ok "C3: the Telegram threshold is a stored setting (changeable without a restart)"
else
  bad "C3: the push threshold is hardcoded"
fi
if grep -q 'if !created || SeverityRank(out.Severity) < SeverityRank(in.minNotify())' "$INBOX"; then
  ok "C4: only new/reopened events at or above the threshold are pushed"
else
  bad "C4: the notify decision does not consider dedup + severity"
fi
if grep -q 'func (in \*Inbox) Resolve' "$INBOX" && grep -q 'func FormatAlert' "$INBOX"; then
  ok "C5: Resolve + the alert text exist"
else
  bad "C5: the inbox API is incomplete"
fi

# --- D: the page ---------------------------------------------------------------
for route in 'GET /admin/monitor' 'POST /admin/monitor/{id}/ack' 'POST /admin/monitor/ack-all' 'POST /admin/monitor/settings'; do
  if grep -qF "$route" "$MAIN"; then
    ok "D1: route registered: $route"
  else
    bad "D1: route missing: $route"
  fi
done
if grep -q 'href="/admin/monitor"' "$LAYOUT" && grep -q 'monitor.nav' "$LAYOUT"; then
  ok "D2: the sidebar links to the inbox"
else
  bad "D2: the page is unreachable from the navigation"
fi
if grep -q '"admin/monitor"' internal/handlers/handlers.go; then
  ok "D3: the sidebar section opens on this page"
else
  bad "D3: the health section does not include admin/monitor"
fi
if grep -q '{{define "body-admin-monitor"}}' "$TPL"; then
  ok "D4: the template defines the body block the renderer looks for"
else
  bad "D4: the template's define block is missing"
fi
for k in nav title subtitle ack ack_all count_open filter_state filter_min empty notify_title notify_min save list_error; do
  cnt="$(grep -cF "\"monitor.$k\"" "$RU" 2>/dev/null || echo 0)"
  if [ "$cnt" -ge 2 ]; then
    ok "D5: i18n key present (RU+EN): monitor.$k"
  else
    bad "D5: i18n key monitor.$k only in $cnt/2 maps (need RU+EN)"
  fi
done
# No raw error pages on this page (the B303 rule).
if ! grep -q 'http.Error(w, err.Error()' "$PAGE"; then
  ok "D6: refusals are flashes, not raw error pages"
else
  bad "D6: a raw error page came back on /admin/monitor"
fi
if grep -q 'monitorDBHandle()' "$PAGE"; then
  ok "D7: the page survives a Service without a database"
else
  bad "D7: the page would panic without a DB source"
fi

# --- E: producers --------------------------------------------------------------
if grep -q 'func (s \*Service) ReportRunToMonitor' "$SYSTESTS" \
   && grep -q 's.ReportRunToMonitor(results)' "$SYSTESTS"; then
  ok "E1: system-test runs report into the inbox"
else
  bad "E1: system tests do not reach the inbox"
fi
if grep -q 'case SystemTestPass:' "$SYSTESTS" && grep -q 'in.Resolve(ev)' "$SYSTESTS"; then
  ok "E2: a passing test resolves its event (the list shows what is broken NOW)"
else
  bad "E2: a passing test leaves its event open forever"
fi
if grep -q 'monitorTagAlertSink' "$ADOPT" && grep -q 'nodeownership.NewTagAlertSink(monitorTagAlertSink' "$ADOPT"; then
  ok "E3: tag-reconcile failures also land in the inbox"
else
  bad "E3: tag failures bypass the inbox"
fi

# --- F: tests ------------------------------------------------------------------
for f in internal/db/monitor_events_b305_test.go internal/monitorinbox/monitorinbox_b305_test.go internal/feature/admin/monitor_b305_test.go; do
  if [ -f "$f" ]; then ok "F1: $f exists"; else bad "F1: $f is missing"; fi
done
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/db/ ./internal/monitorinbox/ ./internal/feature/admin/ -run 'B305' -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT" && ! grep -q 'FAIL' <<< "$OUT"; then
    ok "F2: the B305 Go tests pass"
  else
    bad "F2: the B305 Go tests failed:"
    printf '%s\n' "$OUT" | tail -20 | sed 's/^/       /' >&2
  fi
else
  skip "F2: go not on PATH — run the B305 tests on the VM"
fi

# --- G: git --------------------------------------------------------------------
if git ls-files --error-unmatch scripts/check_b305_monitor_inbox.sh >/dev/null 2>&1; then
  ok "G1: scripts/check_b305_monitor_inbox.sh is tracked by git"
else
  bad "G1: scripts/check_b305_monitor_inbox.sh is NOT tracked"
fi

printf '\n\033[1mB305 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
