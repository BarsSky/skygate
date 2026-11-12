#!/usr/bin/env bash
# ============================================================================
# check_b460_acl_plane_truth.sh — B460
#
# THE ACL IMPORT MUST SAY WHAT IT IS PUSHING TO — AND MUST REPORT EVERY PLANE.
#
# Measured 2026-10-29, two defects in one page:
#
#  1. /admin/acls/import's dry-run compared the imported policy with the current
#     one and stopped there. It never named the planES that the apply pushes to
#     (acl.SetACLForAllPlanes walks every distinct control plane), let alone what
#     each of them RUNS — while headscale 0.29.x is not uniform across releases
#     (approve_routes left REST at 0.29.1, the REST expire path broke at 0.29.2,
#     grants[] replaced acls[] at 0.29.0-beta.4, 0.29.2 rejects wildcards in
#     tagOwners — internal/headscale/version_b297.go:30-37 states all of it).
#
#  2. PostAdminACLsImportApply answered `http.Error(...); return` on the FIRST
#     failing plane, although the planes BEFORE it had already been changed by
#     SetPolicy and are never rolled back. The operator saw one error and no sign
#     that the run was half-applied.
#
# This block INFORMS and never gates: every capability difference above is handled
# by a ladder in the client, so refusing an import on a version number would block
# work that would have succeeded.
#
# Contracts:
#   A. the plane URL reaches the result (acl.ApplyResult.PlaneURL, both writers)
#   B. the handlers pass the plane view and never abort on the first failure
#   C. the template renders the plane table, the mismatch warning and the report
#   D. all new i18n keys exist in BOTH catalogues (rule 10)
#   E. the behavioural tests exist
#   F. the detector can fail: planted violations are caught
# ============================================================================
set -uo pipefail

SKY_TMP="$(mktemp -d "${TMPDIR:-/tmp}/skygate-b460.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

PASS=0
FAIL=0
fails=()

ok()  { printf '  \033[32m\xe2\x9c\x93\033[0m %s\n' "$1"; PASS=$((PASS+1)); }
bad() { printf '  \033[31m\xe2\x9c\x97\033[0m %s\n' "$1"; FAIL=$((FAIL+1)); fails+=("$1"); }

ACL_APPLY=internal/acl/acl_apply.go
ACL_SET=internal/acl/acl_set.go
IMPORT=internal/feature/admin/acl_import.go
PLANES=internal/feature/admin/acl_import_planes_b460.go
SERVICE=internal/feature/admin/service.go
TPL=internal/handlers/templates/admin/acls_import.html
CATALOG=internal/i18n/catalog_admin.go
PLANE_FN=cmd/skygate/plane_version_b460.go
ADMIN_TEST=internal/feature/admin/acl_import_planes_b460_test.go
RENDER_TEST=internal/handlers/acl_import_planes_b460_render_test.go

for f in "$ACL_APPLY" "$ACL_SET" "$IMPORT" "$PLANES" "$SERVICE" "$TPL" "$CATALOG" "$PLANE_FN" "$ADMIN_TEST" "$RENDER_TEST"; do
  if [ ! -f "$f" ]; then
    echo "FAIL: $f not found (run from the repository root)" >&2
    exit 1
  fi
done

has() { case "$(cat "$1")" in *"$2"*) return 0;; *) return 1;; esac; }

# B339 — a contract must read the PACKAGE SURFACE, not one file: main.go has been
# split (B372 moved the boot tail into main_schedulers.go, and B460's own wiring
# lives in plane_version_b460.go), so grepping main.go alone would go red on a pure
# code move and blind to the behaviour landing in a sibling file.
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/gosurface.sh"
gosurface PLANE_SURFACE cmd/skygate/*.go

echo "=== B460: the ACL import names its planes and reports all of them ==="

# --- A: the plane URL reaches the result ---
if has "$ACL_APPLY" "PlaneURL string"; then
  ok "A1: acl.ApplyResult carries PlaneURL"
else
  bad "A1: acl.ApplyResult does not carry the plane it belongs to"
fi

set_sites=$(grep -c 'PlaneURL: p.URL' "$ACL_SET" 2>/dev/null || true); set_sites=${set_sites:-0}
if [ "$set_sites" -ge 3 ]; then
  ok "A2: SetACLForAllPlanes tags all ${set_sites} results with their plane"
else
  bad "A2: SetACLForAllPlanes tags only ${set_sites} of its results (expected >=3)"
fi

if has "$ACL_APPLY" 'r.PlaneURL = p.URL'; then
  ok "A3: ApplyACLForAllPlanes tags the pipeline result with its plane"
else
  bad "A3: ApplyACLForAllPlanes drops the plane URL"
fi

# --- B: the handlers ---
if has "$IMPORT" '"PlaneVersions"'; then
  ok "B1: the import page is given the per-plane view"
else
  bad "B1: the import page is not given the plane view"
fi

if has "$IMPORT" 'http.Error(w, "set policy: '; then
  bad "B2: the apply still answers with http.Error on the first failing plane"
else
  ok "B2: the apply no longer aborts on the first failing plane"
fi

if has "$IMPORT" '"ApplyReport"' && has "$IMPORT" '"ApplyFailed"'; then
  ok "B3: the apply renders a report for every plane it walked"
else
  bad "B3: the apply does not render the per-plane report"
fi

if has "$IMPORT" 'cannot list the control planes'; then
  ok "B4: an unlistable plane set is reported as a reason, not as an empty table"
else
  bad "B4: an unlistable plane set would render as a silent empty table"
fi

if has "$SERVICE" 'PlaneVersionFn func(planeURL string) (version, via string, err error)'; then
  ok "B5: admin.Service exposes the version seam"
else
  bad "B5: admin.Service has no PlaneVersionFn seam"
fi

if has "$PLANE_FN" 'func planeVersionFn('; then
  ok "B6: the seam is a named helper outside main.go (the boot file keeps shrinking, B372)"
else
  bad "B6: the seam is not a named helper"
fi

# NOT one literal spanning the colon: gofmt ALIGNS struct-literal values, so the real
# line is `PlaneVersionFn:         planeVersionFn(app, cfg),` and a single-space
# literal silently stops matching after any reformat (measured — this contract went
# red on its own author for exactly that reason). Two independent substrings pin the
# same thing and cannot be broken by alignment.
if has "$PLANE_SURFACE" 'PlaneVersionFn:' && has "$PLANE_SURFACE" 'planeVersionFn(app, cfg)'; then
  ok "B7: the package wires the helper into admin.Service"
else
  bad "B7: nothing in cmd/skygate wires planeVersionFn into admin.Service"
fi

if has "$PLANE_SURFACE" 'PlaneVersionAnswer('; then
  ok "B8: the wiring shares the tested precedence (detected, then the declared pin)"
else
  bad "B8: the wiring re-implements the precedence instead of using PlaneVersionAnswer"
fi

# --- C: the template ---
for field in '.PlaneVersions' '.PlaneMismatch' '.ApplyReport' '.Known'; do
  if has "$TPL" "$field"; then
    ok "C1: the template renders $field"
  else
    bad "C1: the template does not render $field"
  fi
done

wrap_count=$(grep -c 'class="table-wrap"' "$TPL" 2>/dev/null || true); wrap_count=${wrap_count:-0}
if [ "$wrap_count" -ge 2 ]; then
  ok "C2: both new tables are wrapped for mobile scroll (B105)"
else
  bad "C2: expected >=2 .table-wrap wrappers, found ${wrap_count}"
fi

# --- D: i18n in both catalogues ---
B460_KEYS="acls.import_planes_title acls.import_planes_help acls.import_planes_url acls.import_planes_version acls.import_planes_primary acls.import_planes_unknown acls.import_planes_unknown_hint acls.import_planes_mismatch acls.import_report_banner acls.import_report_hint acls.import_report_status acls.import_report_ok acls.import_report_fail"
missing=""
for k in $B460_KEYS; do
  c=$(grep -c "\"$k\"" "$CATALOG" 2>/dev/null || true); c=${c:-0}
  if [ "$c" -lt 2 ]; then missing="$missing $k(${c})"; fi
done
if [ -z "$missing" ]; then
  ok "D1: all 13 new keys exist in RU and EN"
else
  bad "D1: keys missing from one catalogue:${missing}"
fi

# --- E: behavioural tests ---
admin_tests=$(grep -cE '^func Test[A-Za-z0-9_]*B460' "$ADMIN_TEST" 2>/dev/null || true); admin_tests=${admin_tests:-0}
render_tests=$(grep -cE '^func Test[A-Za-z0-9_]*B460' "$RENDER_TEST" 2>/dev/null || true); render_tests=${render_tests:-0}
if [ "$admin_tests" -ge 5 ] && [ "$render_tests" -ge 3 ]; then
  ok "E1: ${admin_tests} pure tests + ${render_tests} render tests"
else
  bad "E1: expected >=5 pure and >=3 render tests, found ${admin_tests} + ${render_tests}"
fi

# --- F: prove the detectors can fail ---
# F1: the "no first-error abort" predicate must flag a re-introduced http.Error.
cp "$IMPORT" "$SKY_TMP/import_mutated.go"
printf '\nfunc b460MutationProbe() { http.Error(w, "set policy: boom", 500) }\n' >> "$SKY_TMP/import_mutated.go"
if has "$SKY_TMP/import_mutated.go" 'http.Error(w, "set policy: '; then
  ok "F1: the first-error detector fires on a planted http.Error"
else
  bad "F1: the first-error detector cannot see a planted violation"
fi

# F2: the i18n predicate must flag a key removed from one catalogue.
sed 's/"acls\.import_report_ok"/"acls.import_report_ok_REMOVED"/g' "$CATALOG" > "$SKY_TMP/catalog_mutated.go"
mut=$(grep -c '"acls.import_report_ok"' "$SKY_TMP/catalog_mutated.go" 2>/dev/null || true); mut=${mut:-0}
if [ "$mut" -lt 2 ]; then
  ok "F2: the i18n parity detector fires when a key is dropped from one catalogue"
else
  bad "F2: the i18n parity detector cannot see a dropped key"
fi

# F3: the template predicate must flag a missing field. A plain substring match is
# NOT enough — ".PlaneVersions_REMOVED" still CONTAINS ".PlaneVersions", so the
# mutation would pass and the detector would look green while seeing nothing
# (the exact shape B460 exists to remove). Match the bare field instead.
sed 's/\.PlaneVersions/.PlaneVersions_REMOVED/g' "$TPL" > "$SKY_TMP/tpl_mutated.html"
if grep -qE '\.PlaneVersions([^_A-Za-z]|$)' "$SKY_TMP/tpl_mutated.html"; then
  bad "F3: the template detector cannot see a removed field"
else
  ok "F3: the template detector fires when .PlaneVersions is removed"
fi

# --- summary ---
echo ""
if [ "$FAIL" -eq 0 ]; then
  printf '\033[32mB460: %d passed, 0 failed\033[0m\n' "$PASS"
else
  printf '\033[31mB460: %d passed, %d FAILED\033[0m\n' "$PASS" "$FAIL"
  for f in "${fails[@]}"; do printf '  - %s\n' "$f"; done
fi
# B322: the LAST statement is the script's exit status, and it must depend on FAIL.
[ "$FAIL" -eq 0 ]
