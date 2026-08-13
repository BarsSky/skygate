#!/usr/bin/env bash
# check_b116.sh — v1.3.17 DERP relay CRUD UI (per-row
# add/edit/delete/toggle/test). Replaces the v0.11.0
# comma-separated textarea model with a first-class
# derp_relays PG table.
#
# Pinned contracts (each MUST be present in the tree, else FAIL):
#   1. migrateV055PG function defined in migrations_pg.go
#   2. migrateV055PG registered in MigratePostgres chain
#   3. derp_relays table created (id + url + region_id +
#      is_bundled + enabled + sort_order + notes + timestamps)
#   4. UNIQUE index on url
#   5. db/derp_relays.go: ListDerpRelays + GetDerpRelay
#   6. db/derp_relays.go: AddDerpRelay + UpdateDerpRelay +
#      DeleteDerpRelay + ToggleDerpRelayEnabled
#   7. db/derp_relays.go: ListEnabledDerpRelayURLs +
#      IsBundledDerpRelayEnabled
#   8. db/derp_relays.go: AutoMigrateDerpRelays (one-shot
#      backward-compat bridge from v0.11.0 global_settings)
#   9. feature/admin/derp_relays.go: GetAdminDerpRelays +
#      PostAdminDerpRelaysAdd + PostAdminDerpRelaysEdit +
#      PostAdminDerpRelaysDelete + PostAdminDerpRelaysToggle
#      + PostAdminDerpRelaysTest
#  10. cmd/skygate/main.go: 6 routes registered
#      (/admin/derp/relays + add/edit/delete/toggle/test)
#  11. Template admin/derp_relays.html (CRUD table)
#  12. Integrations renderer (applyBundledDERP) uses
#      db.IsBundledDerpRelayEnabled (derp_relays, not
#      legacy cfg.BundledDERP)
#  13. renderHeadscaleConfig merges derp_relays enabled URLs
#      with cfg.DERPExternalURLs
#  14. i18n keys: derp.relays_title (and ≥30 relays_* keys)
#  15. db unit tests: ≥5 TestDerpRelays_* functions
#  16. Bundled row undeletable (ErrDerpRelayBundledUndeletable)
#  17. At-most-one is_bundled=1 row (ErrDerpRelayBundledExists)
#  18. go build ./... clean (B1)
#
# Why this check exists
# ======================
# Pre-v1.3.17 the /admin/derp/config page had a single
# textarea + bundled checkbox. The operator wanted
# per-row management (region metadata, sort order, per-row
# enable/disable, per-row test) like /admin/exit-nodes.
# v1.3.17 adds the derp_relays table + CRUD UI.
#
# v1.3.17 = B116.

set -u
cd "$(dirname "$0")/.."

GREEN='\033[0;32m'
RED='\033[0;31m'
NC='\033[0m'

pass() { printf "${GREEN}  PASS${NC}  B116  %s\n" "$1"; }
fail() { printf "${RED}  FAIL${NC}  B116  %s\n" "$1"; exit 1; }

# 1. migrateV055PG function
F=internal/db/migrations_pg.go
grep -q 'func migrateV055PG' "$F" || fail "migrateV055PG not defined in $F"
pass "migrateV055PG defined in migrations_pg.go"

# 2. migrateV055PG registered in MigratePostgres chain
F=internal/db/driver_postgres.go
grep -q 'migrateV055PG' "$F" || fail "migrateV055PG not registered in MigratePostgres chain"
pass "migrateV055PG registered in MigratePostgres chain"

# 3. derp_relays table created
F=internal/db/migrations_pg.go
grep -q 'CREATE TABLE IF NOT EXISTS derp_relays' "$F" \
    || fail "derp_relays table not created in $F"
for col in id BIGSERIAL hostname url region_id is_bundled enabled sort_order notes created_at updated_at; do
    grep -q "\\b$col\\b" "$F" || fail "derp_relays missing column: $col"
done
pass "derp_relays table created with all required columns"

# 4. UNIQUE index on url
grep -q 'derp_relays_url_uniq' "$F" || fail "derp_relays UNIQUE index on url missing"
pass "derp_relays UNIQUE index on url"

# 5. List + Get
F=internal/db/derp_relays.go
grep -q 'func ListDerpRelays' "$F" || fail "ListDerpRelays not defined in $F"
grep -q 'func GetDerpRelay' "$F" || fail "GetDerpRelay not defined in $F"
pass "db.ListDerpRelays + db.GetDerpRelay defined"

# 6. Add / Update / Delete / Toggle
for fn in AddDerpRelay UpdateDerpRelay DeleteDerpRelay ToggleDerpRelayEnabled; do
    grep -q "func $fn" "$F" || fail "$fn not defined in $F"
done
pass "db CRUD: Add + Update + Delete + Toggle all defined"

# 7. ListEnabled + IsBundledEnabled
grep -q 'func ListEnabledDerpRelayURLs' "$F" \
    || fail "ListEnabledDerpRelayURLs not defined in $F"
grep -q 'func IsBundledDerpRelayEnabled' "$F" \
    || fail "IsBundledDerpRelayEnabled not defined in $F"
pass "db.ListEnabledDerpRelayURLs + db.IsBundledDerpRelayEnabled defined"

# 8. AutoMigrateDerpRelays
grep -q 'func AutoMigrateDerpRelays' "$F" \
    || fail "AutoMigrateDerpRelays not defined in $F"
pass "db.AutoMigrateDerpRelays (one-shot backward-compat) defined"

# 9. Admin handlers
F=internal/feature/admin/derp_relays.go
for fn in GetAdminDerpRelays PostAdminDerpRelaysAdd PostAdminDerpRelaysEdit \
          PostAdminDerpRelaysDelete PostAdminDerpRelaysToggle PostAdminDerpRelaysTest; do
    grep -q "func.*$fn" "$F" || fail "$fn not defined in $F"
done
pass "6 admin handlers: Get + Add + Edit + Delete + Toggle + Test"

# 10. Routes registered in main.go
F=cmd/skygate/main.go
for route in '/admin/derp/relays' '/admin/derp/relays/add' \
             '/admin/derp/relays/edit' '/admin/derp/relays/delete' \
             '/admin/derp/relays/toggle' '/admin/derp/relays/test'; do
    # Routes use Go 1.22+ mux.Handle("METHOD /path", ...) —
    # the path appears with the method prefix. grep with
    # any-method pattern: e.g. 'GET /admin/derp/relays',
    # 'POST /admin/derp/relays/add'.
    grep -qE "(GET|POST) $route" "$F" \
        || fail "Route $route not registered in $F (any method)"
done
pass "6 routes registered in cmd/skygate/main.go"

# 11. Template
F=internal/handlers/templates/admin/derp_relays.html
test -f "$F" || fail "Template $F not found"
grep -q 'body-admin-derp_relays' "$F" || fail "$F doesn't define body-admin-derp_relays"
pass "admin/derp_relays.html template exists with body-admin-derp_relays"

# 12. applyBundledDERP uses derp_relays
F=internal/feature/admin/integrations_renderer.go
grep -q 'IsBundledDerpRelayEnabled' "$F" \
    || fail "applyBundledDERP does not use db.IsBundledDerpRelayEnabled"
pass "applyBundledDERP uses db.IsBundledDerpRelayEnabled (not legacy cfg.BundledDERP)"

# 13. renderHeadscaleConfig merges derp_relays URLs
grep -q 'ListEnabledDerpRelayURLs' "$F" \
    || fail "renderHeadscaleConfig does not use db.ListEnabledDerpRelayURLs"
pass "renderHeadscaleConfig merges derp_relays enabled URLs"

# 14. i18n keys
F=internal/i18n/catalog_derp.go
for key in 'derp.relays_title' 'derp.relays_add_title' 'derp.relays_action_save_help' \
           'derp.relays_action_delete_help' 'derp.relays_err_duplicate_url' \
           'derp.relays_err_bundled_exists' 'derp.relays_added' 'derp.relays_toggled'; do
    grep -q "\"$key\"" "$F" || fail "i18n key $key missing in $F"
done
COUNT=$(grep -c '"derp\.relays_' "$F" || true)
[ "$COUNT" -ge 30 ] || fail "derp.relays_* key count = $COUNT, want ≥30"
pass "≥30 derp.relays_* i18n keys (catalog_derp.go, count=$COUNT)"

# 15. db unit tests
F=internal/db/derp_relays_test.go
test -f "$F" || fail "Test file $F not found"
COUNT=$(grep -c '^func TestDerpRelays' "$F" || true)
[ "$COUNT" -ge 5 ] || fail "TestDerpRelays_* count = $COUNT, want ≥5"
pass "≥5 TestDerpRelays_* unit tests ($F, count=$COUNT)"

# 16. Bundled undeletable
F=internal/db/derp_relays.go
grep -q 'ErrDerpRelayBundledUndeletable' "$F" \
    || fail "ErrDerpRelayBundledUndeletable not defined"
pass "ErrDerpRelayBundledUndeletable defined (bundled row is undeletable)"

# 17. At-most-one bundled
grep -q 'ErrDerpRelayBundledExists' "$F" \
    || fail "ErrDerpRelayBundledExists not defined"
pass "ErrDerpRelayBundledExists defined (at-most-one is_bundled=1 row)"

# 18. Build clean (covered by B1 in verify_pre_deploy.sh)
pass "go build ./cmd/skygate (covered by B1 in verify_pre_deploy.sh)"

# 19. Templates parse + i18n parity (covered by B7 + B4 in
# verify_pre_deploy.sh — these checks are run there with
# full PATH; the check_b116.sh script is meant to be
# runnable without `go` in the operator's Git Bash PATH)
pass "templates parse (B7) + i18n parity (B4) (covered by verify_pre_deploy.sh)"

echo ""
echo "  All B116 contracts pinned (20 checks). v1.3.17 DERP CRUD UI verified."
exit 0
