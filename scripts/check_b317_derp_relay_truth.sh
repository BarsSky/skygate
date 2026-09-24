#!/usr/bin/env bash
# check_b317_derp_relay_truth.sh
#
# 2026-09-24 (B317, v1.5.82) — one region, one verdict; a derpmap document is not a
# relay; and the "Recommended DERP" banner must be readable.
#
# OPERATOR REPORT (verbatim, with a screenshot of /admin/derp/dashboard):
#
#   «давай как предлагаешь также проблема в том что релей что сейчас рядом со skygate
#    не выбирается и не доступен для выбора как основного DERP сервера хотя при этом
#    он должен иметь минимальную задержку. Пройди инструментами по skygate.skynas.ru
#    и оцени работу и отображение DERP»
#
# The screenshot showed «Рекомендуемый DERP: region_id %!s(int=901)
# (controlplane.tailscale.com)» and a table with two identical 901 rows, while the
# operator's own relay (region 900, ~20 ms) was nowhere to be seen.
#
# WHAT THE LIVE TOOLS FOUND (read-only, on the reference host):
#
#   derp_relays: 900|is_bundled=1|enabled=1|derp.skynas.ru|https://derp.skynas.ru:443
#                900|is_bundled=1|enabled=1|derp.skynas.ru|https://derp.skynas.ru:8443  ← dead
#                901|is_bundled=0|enabled=1|controlplane.tailscale.com|…/derpmap/default
#   derp_health: 900|is_own=1|latency NULL|healthy=0|"tls dial: dial tcp …:8443: connect: connection refused"
#                901|is_own=0|96 ms|healthy=1
#   derp-probe : 900 own … — FAIL     ← and, one line later, 900 own … 20ms ok
#
# derp_health is keyed by region_id (derp_health_pkey), so the two enabled rows of
# region 900 competed for ONE verdict and the last writer owned it:
#   * FetchAllDERPs collapsed them in a `map[int]DERPInfo` loop → the stale `:8443`
#     row (sort_order 11) won, and the cron wrote its connection-refused error every
#     5 minutes → the dashboard hid the local relay and recommended a public one;
#   * `skygate derp-probe` built its own un-deduplicated list and persisted per row,
#     so it wrote 20 ms — the banner then flipped. Two views, one table, disagreeing.
# And the 901 row is the legacy `derp.external_urls` derpmap URL migrated into
# derp_relays: the health prober dialled Tailscale's CONTROL PLANE and filed it as a
# healthy public relay (the public map has 28 regions and no 901 — verified live).
#
# CONTRACTS
#   A. a region's health verdict is independent of row order: every row is probed,
#      the BEST per region is persisted, deterministically
#   B. the fetch keeps every enabled row of a region and is map-free/ordered
#   C. a row whose URL is a derpmap document is never probed as a relay, and the
#      rule matches the one the derpmap PUBLISHER already applies
#   D. the dashboard names regions with several enabled rows (and never a disabled one)
#   E. the panel filter no longer mutates the slice the recommendation reads
#   F. the "Recommended DERP" banner formats the region id as an integer
#   G. the CLI says which verdict a multi-row region kept
#   H. i18n RU+EN pairs
#   I. tests exist and pass; this script is tracked by git

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B317: cannot locate cmd/skygate/main.go from %s\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

MAP=internal/derphealth/map.go
PROBE=internal/derphealth/probe.go
DASH=internal/feature/admin/derp_dashboard.go
TPL=internal/handlers/templates/admin/derp_dashboard.html
CLI=cmd/skygate/derp_probe.go
I18N=internal/i18n/catalog_admin.go

hdr "B317 — one region, one verdict; a derpmap document is not a relay"

# --- A: the verdict ---------------------------------------------------------------
if grep -q 'func BestPerRegion(results \[\]ProbeResult) \[\]ProbeResult' "$PROBE"; then
  ok "A1: BestPerRegion exists — the collapse rule is a named, testable function"
else
  bad "A1: BestPerRegion is missing"
fi
if grep -q 'if c.Healthy != cur.Healthy {' "$PROBE" && grep -q 'return c.LatencyMs < cur.LatencyMs' "$PROBE"; then
  ok "A2: a working endpoint beats a dead one, and the faster healthy one wins"
else
  bad "A2: the ranking does not prefer healthy-then-fastest"
fi
if grep -q 'for _, best := range BestPerRegion(results) {' "$PROBE"; then
  ok "A3: ProbeAll persists the best row per region (derp_health is keyed by region_id)"
else
  bad "A3: ProbeAll still persists per row, so the last writer owns the region"
fi
if grep -q 'if persist != nil {' "$PROBE" && ! grep -q '_ = persist(ctx, d, lat, healthy, err)' "$PROBE"; then
  ok "A4: the per-goroutine persist is gone (no more write race inside a region)"
else
  bad "A4: the in-goroutine persist call is still there"
fi
# A5/A6 — a region that leaves the map must not keep a verdict on the page. The
# region-901 derpmap row is the live example: no longer probed, but its last
# "public, healthy, 117 ms" would sit in derp_health for ever.
if grep -q 'func PruneMissingRegions(ctx context.Context, d \*sql.DB, derps \[\]DERPInfo) (int, error)' internal/derphealth/prune.go; then
  ok "A5: PruneMissingRegions exists"
else
  bad "A5: PruneMissingRegions is missing"
fi
if grep -q 'PruneMissingRegions(ctx, db, derps)' internal/derphealth/cron.go \
   && [ "$(grep -c 'PruneMissingRegions(ctx, db, derps)' internal/derphealth/cron.go)" -ge 2 ]; then
  ok "A6: both the cron tick and the manual re-probe prune the table"
else
  bad "A6: the prune is not wired into runOnce + RunOnceNow"
fi
if grep -q 'if d == nil || len(derps) == 0 {' internal/derphealth/prune.go; then
  ok "A7: an empty probe list is a no-op (a failed map fetch must not empty the dashboard)"
else
  bad "A7: the prune can wipe the table when the map fetch fails"
fi
if grep -q 'PruneMissingRegions' internal/derphealth/probe.go; then
  bad "A8: ProbeAll prunes — a filtered CLI run (-own-only) would delete every public row"
else
  ok "A8: ProbeAll does not prune (only the full-list callers do)"
fi

# --- B: the fetch -----------------------------------------------------------------
if grep -q 'ownRegions\[d.RegionID\] = true' "$MAP" && grep -q 'if !ownRegions\[d.RegionID\] {' "$MAP"; then
  ok "B1: every own row survives the fetch; a public row only fills a region nobody owns"
else
  bad "B1: the own rows are still collapsed (a stale row can win the region)"
fi
if grep -q 'sort.SliceStable(out, func(i, j int) bool {' "$MAP"; then
  ok "B2: the probe list order is deterministic (no map iteration)"
else
  bad "B2: the order still depends on map iteration"
fi
if grep -q 'ORDER BY is_bundled DESC, sort_order ASC, region_id ASC, url ASC' "$MAP"; then
  ok "B3: the SQL order is total (ties broken by url), so the list is reproducible"
else
  bad "B3: the SQL order is not total"
fi
if grep -q 'byID :=' "$MAP"; then
  bad "B4: the map[int]DERPInfo collapse is still in FetchAllDERPs"
else
  ok "B4: the map-based collapse is gone"
fi

# --- C: derpmap documents are not relays ------------------------------------------
if grep -q 'func urlIsRelayNode(rawURL string) bool' "$MAP"; then
  ok "C1: urlIsRelayNode exists"
else
  bad "C1: urlIsRelayNode is missing"
fi
if grep -q 'if !urlIsRelayNode(d.URL) {' "$MAP" && grep -q 'skipping region %d row' "$MAP"; then
  ok "C2: a derpmap-document row is skipped AND named in the journal"
else
  bad "C2: the prober still dials derpmap documents"
fi
if grep -q 'case "", "/derp", "/derp/probe", "/derp/latency-check":' "$MAP"; then
  ok "C3: the accepted paths are a closed set (the same shape the publisher uses)"
else
  bad "C3: the relay-path rule does not match the publisher's"
fi
if grep -q 'mirrors internal/feature/admin.isDerpMapURL' "$MAP" \
   && grep -q 'func isDerpMapURL(rawURL string) bool' "$DASH"; then
  ok "C4: the two halves of the claim are cross-referenced (prober + publisher)"
else
  bad "C4: the prober and the publisher are not linked"
fi

# --- D: the page names the competing rows -----------------------------------------
if grep -q 'func enabledRelayRowsByRegion(db \*sql.DB) (map\[int\]\[\]string, error)' "$DASH"; then
  ok "D1: the dashboard collects the ambiguous regions"
else
  bad "D1: enabledRelayRowsByRegion is missing"
fi
if grep -q 'WHERE enabled = 1' "$DASH" && grep -q 'if len(urls) > 1 {' "$DASH"; then
  ok "D2: only ENABLED regions with more than one row are reported"
else
  bad "D2: the duplicate query does not filter on enabled + count > 1"
fi
if grep -q '"DuplicateRegions": duplicateRegions' "$DASH"; then
  ok "D3: the page receives the duplicate map"
else
  bad "D3: the template never sees the duplicates"
fi
if grep -q 'derp_dashboard.dupes_title' "$TPL" && grep -q 'range \$region, \$urls := .DuplicateRegions' "$TPL"; then
  ok "D4: the banner lists the region and its rows"
else
  bad "D4: the template does not render the duplicate banner"
fi
if grep -q '/admin/derp/relays' "$TPL"; then
  ok "D5: the banner links to the page where the row can be disabled"
else
  bad "D5: the banner has no way forward"
fi

# --- E: the filter no longer mutates what the recommendation reads -----------------
if grep -q 'visible := make(\[\]derphealth.HealthRow, 0, len(all))' "$DASH"; then
  ok "E1: the visible list is built as a new slice"
else
  bad "E1: the panel still filters in place (visible = all[:0])"
fi
if grep -q 'visible = visible\[:0\]' "$DASH"; then
  bad "E2: the in-place filter is still present"
else
  ok "E2: the in-place filter is gone, so 'all' stays intact for the recommendation"
fi

# --- F: the banner formats an int --------------------------------------------------
if grep -q 'derp_dashboard.recommended":        "Рекомендуемый DERP: region_id <code>%d</code>' "$I18N" \
   && grep -q 'derp_dashboard.recommended":        "Recommended DERP: region_id <code>%d</code>' "$I18N"; then
  ok "F1: the recommended banner formats the region id as an integer (was %!s(int=901))"
else
  bad "F1: the banner still uses %s for an int (that is the %!s(int=901) the operator saw)"
fi
if grep -c '%s(int' "$I18N" >/dev/null 2>&1; then :; fi
if grep -q 'Recommended DERP: region_id <code>%s</code>' "$I18N"; then
  bad "F2: an %s placeholder for the region id survived"
else
  ok "F2: no %s placeholder for an integer region id remains"
fi

# --- G: the CLI explains the multi-row region --------------------------------------
if grep -q 'derphealth.BestPerRegion(results)' "$CLI" && grep -q 'has %d rows; the dashboard keeps the best one' "$CLI"; then
  ok "G1: derp-probe says which verdict a multi-row region kept"
else
  bad "G1: the CLI still hides the difference between its table and the dashboard"
fi

# --- H: i18n parity ----------------------------------------------------------------
MISSING=""
for k in derp_dashboard.recommended derp_dashboard.dupes_title derp_dashboard.dupes_body derp_dashboard.dupes_link; do
  n=$(grep -c "\"$k\":" "$I18N")
  if [ "$n" -lt 2 ]; then MISSING="$MISSING $k($n)"; fi
done
if [ -z "$MISSING" ]; then
  ok "H1: every B317 key is defined in BOTH catalogues (rule 10)"
else
  bad "H1: keys missing from one side (count in parentheses):$MISSING"
fi
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/i18n/ -run TestCatalogsParity -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT"; then
    ok "H2: the i18n parity test passes"
  else
    bad "H2: i18n parity failed:"
    printf '%s\n' "$OUT" | tail -10 | sed 's/^/       /' >&2
  fi
else
  skip "H2: go not on PATH — run the i18n parity test on the VM"
fi

# --- I: tests + git ----------------------------------------------------------------
for f in internal/derphealth/relay_rows_b317_test.go internal/derphealth/prune.go internal/feature/admin/derp_duplicate_rows_b317_test.go; do
  if [ -f "$f" ]; then ok "I1: $f exists"; else bad "I1: $f is missing"; fi
done
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/derphealth/ ./internal/feature/admin/ -run 'B317' -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT" && ! grep -q 'FAIL' <<< "$OUT"; then
    ok "I2: the B317 Go tests pass"
  else
    bad "I2: the B317 Go tests failed:"
    printf '%s\n' "$OUT" | tail -20 | sed 's/^/       /' >&2
  fi
  OUT="$(go vet ./internal/derphealth/ ./internal/feature/admin/ ./cmd/skygate/ 2>&1)"
  if [ -z "$OUT" ]; then
    ok "I3: go vet is clean on every package this block touches"
  else
    bad "I3: go vet reported:"
    printf '%s\n' "$OUT" | tail -10 | sed 's/^/       /' >&2
  fi
else
  skip "I2/I3: go not on PATH — run the B317 tests on the VM"
fi
if git ls-files --error-unmatch scripts/check_b317_derp_relay_truth.sh >/dev/null 2>&1; then
  ok "I4: scripts/check_b317_derp_relay_truth.sh is tracked by git"
else
  bad "I4: scripts/check_b317_derp_relay_truth.sh is NOT tracked (AGENTS trap #11)"
fi

printf '\n\033[1mB317 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
