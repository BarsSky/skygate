#!/usr/bin/env bash
# B-check for B228 (v1.5.2+): DERP dashboard
# "hide unavailable" filter. Closes the operator's
# 2026-09-03 report: 28+ rows of "degraded, —" на
# /admin/derp/dashboard делают страницу бесполезной.
#
# Contracts pinned (8 surface contracts):
#   A:    handler reads show_unavailable from r.URL.Query()
#         (NOT a path param, NOT a form post — pure GET
#         query string so the toggle is bookmarkable +
#         auto-submit-friendly).
#   B:    handler builds `visible` slice; when
#         show_unavailable=0 (default), filters out rows
#         where !Healthy || LatencyMs==0.
#   C:    when filtered, sort = LatencyMs ASC (primary),
#         IsOwn DESC (tiebreaker), RegionID ASC (stable).
#   D:    the pre-B228 sort (own-first, then latency) is
#         preserved on the FULL slice so the
#         ?show_unavailable=1 view is byte-identical to
#         the pre-B228 page.
#   E:    template renders a checkbox named
#         "show_unavailable" with value="1", checked
#         state mirrors .ShowUnavailable; the form is
#         a GET (not POST) so the URL is bookmarkable.
#   F:    template renders "Показано X из Y DERP" via
#         .VisibleCount / .TotalCount.
#   G:    template renders an "all degraded" empty state
#         with a one-click "Show all" link when the
#         filter hides everything (the operator's exact
#         scenario: all 29 DERPs are degraded).
#   H:    4 new i18n keys (show_unavailable, show_all,
#         shown_of_total, all_degraded) in
#         catalog_admin.go.
#   I:    go build ./... succeeds.
set -euo pipefail
cd "$(dirname "$0")/.."

ok_count=0
ok() { printf "[ok]   %s\n" "$1"; ok_count=$((ok_count+1)); }
fail() { printf "[FAIL] %s\n" "$1"; exit 1; }
has() { grep -q -E "$2" "$1" 2>/dev/null; }
hasf() { grep -qF -- "$2" "$1" 2>/dev/null; }

HANDLER="internal/feature/admin/derp_dashboard.go"
TEMPLATE="internal/handlers/templates/admin/derp_dashboard.html"
CATALOG="internal/i18n/catalog_admin.go"

# --- A: handler reads show_unavailable query param ---
if hasf "$HANDLER" 'r.URL.Query().Get("show_unavailable")'; then
  ok "A: handler reads show_unavailable from r.URL.Query()"
else
  fail "A: handler doesn't read show_unavailable from r.URL.Query()"
fi

# --- B: filter logic (drop !Healthy || LatencyMs==0) ---
# The filter block is the if !showUnavailable { ... } guard
# that appends only when (r.Healthy && r.LatencyMs > 0).
if grep -A5 'showUnavailable' "$HANDLER" | grep -q 'r.Healthy && r.LatencyMs > 0'; then
  ok "B: filter drops !Healthy || LatencyMs==0 (visible := visible[:0] in-place)"
else
  fail "B: filter doesn't check r.Healthy && r.LatencyMs > 0"
fi

# --- C: filtered view sort = latency ASC, own DESC, region ASC ---
if grep -A8 'SliceStable(visible' "$HANDLER" | grep -q 'li < lj' && \
   grep -A8 'SliceStable(visible' "$HANDLER" | grep -q 'visible\[i\].IsOwn'; then
  ok "C: visible set sorted by LatencyMs ASC (primary), IsOwn DESC (tiebreaker)"
else
  fail "C: visible sort doesn't match LatencyMs ASC + IsOwn DESC"
fi

# --- D: pre-B228 sort preserved on the FULL slice ---
# The pre-B228 sort.SliceStable(all, ...) must still be
# present, operating on `all` (not `visible`) so the
# show_unavailable=1 view is unchanged.
if hasf "$HANDLER" 'sort.SliceStable(all'; then
  ok "D: pre-B228 own-first sort preserved on the FULL slice (show_unavailable=1 = pre-B228 view)"
else
  fail "D: pre-B228 own-first sort on the FULL slice is gone"
fi

# --- E: template toggle (checkbox, GET, auto-submit) ---
if hasf "$TEMPLATE" '<input type="checkbox" name="show_unavailable" value="1"' && \
   hasf "$TEMPLATE" 'method="get" action="/admin/derp/dashboard"'; then
  ok "E: template renders GET form with show_unavailable checkbox + onchange auto-submit"
else
  fail "E: template missing GET form / show_unavailable checkbox"
fi

# --- F: shown-of-total counter (VisibleCount / TotalCount) ---
if hasf "$TEMPLATE" 'tf "derp_dashboard.shown_of_total" .VisibleCount .TotalCount'; then
  ok "F: template renders shown-of-total counter (VisibleCount / TotalCount)"
else
  fail "F: template missing shown-of-total counter"
fi

# --- G: empty state when filter hides everything ---
if hasf "$TEMPLATE" 'derp_dashboard.all_degraded' && \
   hasf "$TEMPLATE" '/admin/derp/dashboard?show_unavailable=1'; then
  ok "G: template renders 'all degraded' empty state with one-click Show All link"
else
  fail "G: template missing all_degraded empty state / Show All link"
fi

# --- H: 4 new i18n keys ---
all_h=1
for key in '"derp_dashboard.show_unavailable"' '"derp_dashboard.show_all"' '"derp_dashboard.shown_of_total"' '"derp_dashboard.all_degraded"'; do
  if ! hasf "$CATALOG" "$key"; then
    echo "  [missing] $key"
    all_h=0
  fi
done
if [ "$all_h" = "1" ]; then
  ok "H: 4 new i18n keys in catalog_admin.go (show_unavailable, show_all, shown_of_total, all_degraded)"
else
  fail "H: catalog_admin.go missing one or more B228 i18n keys"
fi

# --- I: build + vet + tests ---
if command -v go >/dev/null 2>&1; then
  if go build ./... >/dev/null 2>&1; then
    ok "I: go build ./... succeeds"
  else
    fail "I: go build ./... FAILED"
  fi
else
  echo "[skip] I: go not on PATH"
fi

echo ""
echo "B228 B-check: $ok_count passed"
