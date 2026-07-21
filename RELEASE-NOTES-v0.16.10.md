# v0.16.10 — chmod+x for check_https.py + /admin/subnets overview

2026-07-17

Two small follow-ups bundled together as v0.16.10:

## 1. scripts/check_https.py — fix the pre-existing chmod+x mismatch

The Makefile guarded `check-https` with
`[ -x scripts/check_https.py ]` but the file was committed
as 100644 in v0.15.0, so `make test` always failed at
the check-https step. The earlier workaround was
`chmod +x` on the VM after every `git reset --hard`. Now
the file is committed as 100755 so the guard passes
without manual intervention.

The script itself is unchanged; only the executable bit
in git is fixed.

## 2. /admin/subnets overview page

v0.16.6+ shipped the per-user `/admin/users/{id}/subnet`
page but no at-a-glance view. Operators running a
multi-user tailnet had to click into each user to see
who has a subnet allocated and what its status is.
v0.16.10 adds a flat list at `/admin/subnets` with:

- One row per `user_subnets` row, sorted by `user_id`
- Status pills (active/pending/disabled) reusing the
  v0.16.8 `tag-success` / `tag-warning` classes
- Clickable username → `/admin/users/{id}/subnet`
- Status filter chips (All / Pending / Active / Disabled)
  with per-status counts, `?status=...` query param
- "Last sync" timestamp from the `sidecar.Manager`
  (v0.16.7) so the operator can see if the
  auto-approver is still ticking
- "How it works" 3-step explainer (Allocate → Issue
  preauth → tailscale up → auto-approve)

Sidebar link added in the admin section (under "Users"),
so the page is reachable from the main admin nav.

## Files

  - `internal/handlers/admin_subnets.go` — new handler
    + `subnetsForOverview()` + `formatSyncStats()` helpers
  - `internal/handlers/templates/admin/subnets.html` —
    new template, extends the standard layout
  - `internal/handlers/admin_subnets_test.go` — 3 new
    tests (empty/populated, status filter, non-admin
    forbidden)
  - `internal/handlers/templates/layout.html` — sidebar
    link to /admin/subnets
  - `internal/i18n/catalog.go` — 16 new keys
    (`admin.subnets.*`) × 2 langs (RU+EN). Parity test
    green.
  - `cmd/skygate/main.go` — `GET /admin/subnets` route
  - `scripts/check_https.py` — git mode 100644 → 100755

## Tests

  - 12/12 packages
  - 3 new admin handler tests
  - `TestCatalogsParity` green (new keys present in
    both languages)
  - `TestTemplateArgsMatchCatalog` green (new keys used
    via `{{t}}` / `{{tf}}` with matching arg counts)

## Out of scope (deferred)

  - **Pagination**: the list is ≤ portal user count, so
    a single SELECT is fine. Add LIMIT/OFFSET if/when
    skygate crosses ~50 subnets (v0.17.0+).
  - **Bulk actions**: "disable all pending" or "re-sync
    all" buttons. Single-user actions stay via the
    per-user page.

## Live verification on VM

  - `/admin/subnets` renders 8060 bytes, title + status
    filter chips + how-it-works hint all visible.
  - `?status=pending` narrows the list to pending rows
    only (verified with 2 seeded subnets: 1 active, 1
    pending).
  - Sidebar link visible in the admin section.
  - Smoke 118/118.
  - `make test` now runs the full check-https script
    instead of failing on the chmod guard. (The
    pre-existing openresty/Caddy HSTS-on-/login
    failure is unchanged — that's a v0.15.0
    infrastructure mismatch, not v0.16.10's scope.)

Deployed to VM, live at build `333079b`.
