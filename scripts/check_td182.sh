#!/usr/bin/env bash
# check_td182.sh — TD-18.2 contract check.
#
# TD-18.2 (2026-08-31): fix the silent regression on
# /admin/derp/dashboard where the page rendered with no
# content + the theme reset to default + a 500 error
# at the bottom of the page.
#
# Root cause: the B189 handler GetAdminDerpDashboard
# (internal/feature/admin/derp_dashboard.go:33) and its
# POST sibling passed `nil` for the JWT claims argument
# to s.Backend.RenderWithLayout. Every other admin
# handler (GetAdminAudit, GetAdminACLsImport, etc)
# extracts claims via `c := s.Backend.CurrentUser(r)`
# and passes `c` instead. When c is nil, renderWithLayout
# (internal/handlers/handlers.go:464) skips the
# notification auto-inject block at line 500-532 —
# specifically, it does NOT set data["UnreadCount"].
# The layout template (layout.html:197) then evaluates
# `{{if gt .UnreadCount 0}}` on a missing key. Go's
# `gt` builtin fails with "invalid type for comparison"
# when called on a nil interface{}, which is exactly
# the error the operator saw: "render template:
# layout.html:197:15: executing 'layout' at <error
# calling gt: invalid type for comparison>". The error
# halts template execution, which means the rest of the
# body (DERP table) never renders AND the `<head>`-level
# theme CSS injection (which depends on .Theme and is
# also downstream of the failing line) doesn't run —
# so the user sees the default theme instead of the
# silver+mint B121 theme.
#
# This script pins:
#   A.  derp_dashboard.go GetAdminDerpDashboard passes
#       `c` (not nil) to RenderWithLayout.
#   B.  derp_dashboard.go PostAdminDerpDashboardRefresh
#       passes `c` (not nil) to RenderWithLayout.
#   C.  Both handlers call s.Backend.CurrentUser(r) to
#       extract the claims (matches every other admin
#       handler in the project).
#   D.  AGENTS.md mentions TD-18.2.
#   E.  verify_pre_deploy.sh references check_td182.sh.
#   F.  This script is executable.
#   G.  go build ./... passes (the handler compiles
#       after the fix).
#   H.  go test -count=1 -short ./internal/feature/admin/...
#       passes (the handler unit tests pass).

set -u
PASS=0
FAIL=0
ok() { PASS=$((PASS+1)); printf '  PASS  %s\n' "$1"; }
bad() { FAIL=$((FAIL+1)); printf '  FAIL  %s\n' "$1"; }

# ---------------------------------------------------------------------------
# A. GetAdminDerpDashboard passes `c` (not nil) to RenderWithLayout
# ---------------------------------------------------------------------------
# The handler is at internal/feature/admin/derp_dashboard.go.
# Pre-fix the line was: `s.Backend.RenderWithLayout(w, r, "admin/derp_dashboard.html", nil,`
# Post-fix it should be: `s.Backend.RenderWithLayout(w, r, "admin/derp_dashboard.html", c,`
# We check that no `nil` argument appears in any
# RenderWithLayout call inside derp_dashboard.go.
if grep -E 'RenderWithLayout\(w, r, "admin/derp_dashboard\.html", nil' internal/feature/admin/derp_dashboard.go >/dev/null 2>&1; then
    bad "contract A: GetAdminDerpDashboard / PostAdminDerpDashboardRefresh still pass nil for claims"
else
    ok "contract A: no nil claims arg in derp_dashboard.go RenderWithLayout calls"
fi

# ---------------------------------------------------------------------------
# B. The handler extracts claims via s.Backend.CurrentUser(r) — matches
#    the pattern used by every other admin handler.
# ---------------------------------------------------------------------------
if grep -qE 'c\s*:=\s*s\.Backend\.CurrentUser\(r\)' internal/feature/admin/derp_dashboard.go; then
    ok "contract B: derp_dashboard.go calls s.Backend.CurrentUser(r) for claims"
else
    bad "contract B: derp_dashboard.go does NOT call s.Backend.CurrentUser(r)"
fi

# ---------------------------------------------------------------------------
# C. The PostAdminDerpDashboardRefresh handler ALSO passes c
# ---------------------------------------------------------------------------
# (sanity check — the POST handler was the second nil call site)
if grep -E 'PostAdminDerpDashboardRefresh' internal/feature/admin/derp_dashboard.go >/dev/null \
    && grep -B2 -A2 'PostAdminDerpDashboardRefresh' internal/feature/admin/derp_dashboard.go | grep -E 'CurrentUser\(r\)' >/dev/null; then
    ok "contract C: PostAdminDerpDashboardRefresh also calls CurrentUser(r)"
else
    bad "contract C: PostAdminDerpDashboardRefresh missing CurrentUser(r)"
fi

# ---------------------------------------------------------------------------
# D. AGENTS.md mentions TD-18.2
# ---------------------------------------------------------------------------
if grep -qF "TD-18.2" AGENTS.md; then
    ok "contract D: AGENTS.md mentions TD-18.2"
else
    bad "contract D: AGENTS.md does NOT mention TD-18.2"
fi

# ---------------------------------------------------------------------------
# E. verify_pre_deploy.sh references check_td182.sh
# ---------------------------------------------------------------------------
if grep -qF "check_td182.sh" scripts/verify_pre_deploy.sh; then
    ok "contract E: verify_pre_deploy.sh references check_td182.sh"
else
    bad "contract E: verify_pre_deploy.sh does NOT reference check_td182.sh"
fi

# ---------------------------------------------------------------------------
# F. This script is executable
# ---------------------------------------------------------------------------
if [ -x scripts/check_td182.sh ]; then
    ok "contract F: scripts/check_td182.sh is executable"
else
    bad "contract F: scripts/check_td182.sh is NOT executable"
fi

# ---------------------------------------------------------------------------
# G. go build ./... passes
# ---------------------------------------------------------------------------
if command -v go >/dev/null 2>&1; then
    if go build ./... >/dev/null 2>&1; then
        ok "contract G: go build ./... passes"
    else
        bad "contract G: go build ./... failed"
    fi
else
    bad "contract G: go not on PATH"
fi

# ---------------------------------------------------------------------------
# H. go test -count=1 -short ./internal/feature/admin/... passes
# ---------------------------------------------------------------------------
if command -v go >/dev/null 2>&1; then
    if go test -count=1 -short ./internal/feature/admin/... >/dev/null 2>&1; then
        ok "contract H: go test ./internal/feature/admin/... passes"
    else
        bad "contract H: go test ./internal/feature/admin/... failed"
    fi
else
    bad "contract H: go not on PATH"
fi

# ---------------------------------------------------------------------------
echo
echo "=== TD-18.2 summary: $PASS pass, $FAIL fail ==="
if [ "$FAIL" -gt 0 ]; then
    exit 1
fi
echo "all contracts satisfied"
