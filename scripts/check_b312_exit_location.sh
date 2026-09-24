#!/usr/bin/env bash
# check_b312_exit_location.sh
#
# 2026-09-23 (B312, v1.5.77) — WHERE each exit node sits, and the priority by location
# the operator asked for.
#
# OPERATOR REQUEST (verbatim, after karolina was blocked):
#
#   «если доступ никак нельзя восстановить то необходимо компенсировать правила по
#    другим exit node но предлагаю приоритет расставлять на сходный признак
#    расположения по exit node и как отдельная фича в exit node также отображать
#    локацию где расположен exit node»
#
# The live case: karolina was blocked and its 75 prefixes moved to whichever relay
# answered first — right for reachability, arbitrary for latency, because nothing in
# skygate knew that one relay sits in the same country and the other does not.
#
# CONTRACTS
#   A. the location is stored per relay (V077, both migration chains) and the chains
#      still end at the same version
#   B. "unknown" is a first-class answer: an empty row never becomes (0,0), and a
#      refusal is never stored as a place
#   C. the automatic detection is optional and offline-safe (SKYGATE_GEO_LOOKUP=off,
#      an overridable endpoint, a bounded lookup) and a manual value is never
#      overwritten
#   D. the page shows the location and lets the operator set it (the one thing a
#      lookup cannot always do), with refusals as flashes and an audit row
#   E. the assignment PREFERS the closest healthy relay when an owner is unreachable —
#      same city → same country → shorter distance — and never invents a preference it
#      cannot justify from data
#   F. the engine's own authority (manual pin, explicit majority, sticky) still wins,
#      and Assign() keeps its old behaviour bit for bit
#   G. tests exist and pass; this script is tracked by git

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B312: cannot locate cmd/skygate/main.go from %s\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

MIG=internal/db/migrations_v0_77_exit_location.go
GEO=internal/geoloc/geoloc.go
DBLOC=internal/db/exit_location.go
LOC=internal/feature/exit_rules/location_b312.go
ADMIN=internal/feature/admin/exit_node_location_b312.go
EXIT=internal/feature/admin/exit_nodes.go
TPL=internal/handlers/templates/admin/exit_nodes.html
PKG=internal/prefixowner/prefixowner.go
ROUTE=cmd/skygate/main.go
I18N=internal/i18n/catalog_exit_nodes.go

hdr "B312 — a relay's location, and the priority by location when its owner is gone"

# --- A: storage -----------------------------------------------------------------
if grep -q 'location_label\|location_checked_at' "$MIG" \
   && grep -q 'location_source' "$MIG" && grep -q 'location_lat' "$MIG"; then
  ok "A1: V077 stores the location, its source and when it was checked"
else
  bad "A1: the location columns are missing from the migration"
fi
if grep -q '{77, "v0.77 (B312)' internal/db/driver_sqlite.go \
   && grep -q '{77, "v0.77 (B312)' internal/db/driver_postgres.go; then
  ok "A2: the migration is registered in BOTH chains (rule 9)"
else
  bad "A2: one of the two migration chains does not know V077"
fi
if grep -q 'execSQLiteDDL' "$MIG"; then
  ok "A3: the SQLite side goes through the single DDL chokepoint"
else
  bad "A3: the SQLite migration bypasses execSQLiteDDL"
fi
if grep -q 'location_source.*<> .manual.' "$DBLOC"; then
  ok "A4: the automatic write refuses to touch a manual row in SQL (not just in Go)"
else
  bad "A4: the automatic write could overwrite the operator's answer"
fi

# --- B: unknown is an answer -----------------------------------------------------
if grep -q 'HasCoords' "$GEO" && grep -q 'if lat == 0 && lon == 0 {' "$GEO"; then
  ok "B1: missing coordinates are never stored as the real (0,0)"
else
  bad "B1: (0,0) could be mistaken for a real position"
fi
if grep -q 'if !strings.EqualFold(strings.TrimSpace(r.Status), "success")' "$GEO"; then
  ok "B2: a refused lookup is an error, not a place"
else
  bad "B2: a refusal could be stored as a location"
fi
if grep -q 'func (l Location) Empty() bool' "$GEO" && grep -q 'func (l ExitLocation) Known() bool' "$DBLOC"; then
  ok "B3: 'unknown' is expressible on both the value and the stored row"
else
  bad "B3: 'unknown' has no representation"
fi

# --- C: optional, offline-safe, manual wins --------------------------------------
if grep -q 'func LookupDisabled()' "$GEO" && grep -q 'SKYGATE_GEO_LOOKUP' "$GEO"; then
  ok "C1: the lookup can be turned off by the operator"
else
  bad "C1: an install cannot refuse the external lookup"
fi
if grep -q 'SKYGATE_GEO_LOOKUP_URL' "$GEO" && grep -q 'strings.Contains(tpl, "%s")' "$GEO"; then
  ok "C2: the endpoint is overridable (an install behind a block can point it at its own service)"
else
  bad "C2: the lookup endpoint is hardcoded"
fi
if grep -q 'context.WithTimeout(ctx, 4\*time.Second)' "$GEO"; then
  ok "C3: the lookup is bounded (it can never hold a sync tick)"
else
  bad "C3: the lookup has no timeout"
fi
if grep -q 'IsPrivate()' "$GEO" && grep -q 'v4\[0\] == 100 && v4\[1\] >= 64' "$GEO"; then
  ok "C4: LAN and tailnet addresses are not geolocated (they say nothing about the relay's place)"
else
  bad "C4: a private/tailnet address could be geolocated"
fi
if grep -q 'func (l ExitLocation) Manual() bool' "$DBLOC" \
   && grep -q 'source := "manual"' "$DBLOC" \
   && grep -q 'location_source = \$5' "$DBLOC"; then
  ok "C5: a manual value is marked and protected"
else
  bad "C5: the manual/auto distinction is missing"
fi
if grep -q 'ExitLocationRefresh' "$LOC" && grep -q 'locationRefreshInterval' "$LOC"; then
  ok "C6: the refresh is cached (no lookup per page render or per tick)"
else
  bad "C6: the lookup could run on every pass"
fi

# --- D: the page ------------------------------------------------------------------
if grep -q 's.RefreshExitNodeLocations()' internal/feature/exit_rules/sync.go; then
  ok "D1: the sync path refreshes due locations before it assigns"
else
  bad "D1: nothing ever fills the locations"
fi
if grep -q 'db.ListExitLocations(s.dbc())' "$EXIT" && grep -q 'LocationKnown' "$EXIT"; then
  ok "D2: the exit-nodes page carries each relay's location"
else
  bad "D2: the page does not show the location"
fi
if grep -q 'exit_nodes.location.title' "$TPL" && grep -q '/admin/exit-nodes/location' "$TPL"; then
  ok "D3: the template renders the column AND the form that sets it"
else
  bad "D3: the location cannot be set from the page"
fi
if grep -q 'POST /admin/exit-nodes/location' "$ROUTE"; then
  ok "D4: the route is registered"
else
  bad "D4: the route is missing"
fi
if grep -q 'Backend.Audit(c.UserID, c.Username, action, detail)' "$ADMIN"; then
  ok "D5: setting a location is audited"
else
  bad "D5: the change is not audited"
fi
if grep -q 'parseLocationCoords' "$ADMIN" && grep -q 'err_coords_range' "$ADMIN"; then
  ok "D6: coordinates are validated (a typo is refused, not silently dropped)"
else
  bad "D6: coordinate validation is missing"
fi
for k in title unknown auto manual help field field_ph country coords save clear saved cleared err_hostname err_unknown_relay err_toolong err_coords_partial err_coords_format err_coords_range; do
  c="$(grep -cF "\"exit_nodes.location.$k\"" "$I18N" 2>/dev/null || true)"
  if [ "${c:-0}" -eq 2 ]; then
    ok "D7: i18n key present exactly once per map: exit_nodes.location.$k"
  else
    bad "D7: i18n key exit_nodes.location.$k appears ${c:-0} time(s), want 2 (RU+EN)"
  fi
done

# --- E: the priority --------------------------------------------------------------
if grep -q 'func NearestHealthyRelay(' "$LOC"; then
  ok "E1: the nearest-relay choice exists"
else
  bad "E1: nothing prefers a nearby relay"
fi
if grep -q 'geoloc.SameArea(origin, loc)' "$LOC" && grep -q 'geoloc.DistanceKM(origin, loc)' "$LOC"; then
  ok "E2: the order is city → country → distance (the operator's «сходный признак»)"
else
  bad "E2: the tiering is missing"
fi
if grep -q 'if bestTier <= geoloc.AreaUnknown {' "$LOC"; then
  ok "E3: with no shared fact the engine decides (no invented preference)"
else
  bad "E3: a preference could be invented from nothing"
fi
if grep -q 'func (s \*Service) nearestRelayPreference()' "$LOC" \
   && grep -q 'prefixowner.ReconcileWithPreference(s.dbc(), healthyExitRelaysForAssignment(s.dbc()), s.nearestRelayPreference())' internal/feature/exit_rules/sync.go; then
  ok "E4: the assignment is wired to the preference (on top of the B309 healthy set)"
else
  bad "E4: the preference is not wired into the assignment"
fi
if grep -q 'the previous owner %s is unreachable and %s is the closest healthy relay' "$LOC"; then
  ok "E5: the move is logged with its reason"
else
  bad "E5: a location-driven move would be silent"
fi

# --- F: the engine keeps its authority --------------------------------------------
if grep -q 'func AssignWithPreference(' "$PKG" && grep -q 'return AssignWithPreference(claims, healthy, existing, nil)' "$PKG"; then
  ok "F1: Assign() delegates with no preference (every existing caller is unchanged)"
else
  bad "F1: Assign() no longer keeps its old behaviour"
fi
if grep -q 'func ReconcileWithPreference(' "$PKG" && grep -q 'return ReconcileWithPreference(d, healthyRelays, nil)' "$PKG"; then
  ok "F2: Reconcile() keeps its signature"
else
  bad "F2: Reconcile() was changed"
fi
if grep -q 'healthySet\[want\]' "$PKG"; then
  ok "F3: a preference may never pin a prefix to an unhealthy relay"
else
  bad "F3: the preference could override health"
fi
if grep -q 'if e, ok := prev\[p\]; ok && e.ExitNode != "" && !healthySet\[e.ExitNode\] && prefer != nil {' "$PKG"; then
  ok "F4: the preference is consulted only for a prefix that is being reassigned"
else
  bad "F4: the preference could reshuffle healthy owners"
fi

# --- G: tests + git ----------------------------------------------------------------
for f in internal/geoloc/geoloc_test.go internal/prefixowner/prefixowner_b312_test.go \
         internal/feature/exit_rules/location_b312_test.go internal/feature/admin/exit_node_location_b312_test.go; do
  if [ -f "$f" ]; then ok "G1: $f exists"; else bad "G1: $f is missing"; fi
done
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/geoloc/ ./internal/prefixowner/ ./internal/feature/exit_rules/ ./internal/feature/admin/ ./internal/db/ -run 'B312|Location|Preference|Nearest|SQLiteSchema' -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT" && ! grep -q 'FAIL' <<< "$OUT"; then
    ok "G2: the B312 tests pass"
  else
    bad "G2: the B312 tests failed:"
    printf '%s\n' "$OUT" | tail -20 | sed 's/^/       /' >&2
  fi
else
  skip "G2: go not on PATH — run the B312 tests on the VM"
fi
if git ls-files --error-unmatch scripts/check_b312_exit_location.sh >/dev/null 2>&1; then
  ok "G3: scripts/check_b312_exit_location.sh is tracked by git"
else
  bad "G3: scripts/check_b312_exit_location.sh is NOT tracked"
fi

printf '\n\033[1mB312 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
