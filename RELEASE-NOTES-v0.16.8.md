# v0.16.8 — UI: Subnet column + button in /admin/users

2026-07-17

v0.16.6 shipped `/admin/users/{id}/subnet` (4 routes:
allocate/disable/test, full template) but the page was
unreachable from the UI — no link from `/admin/users`, no
sidebar entry, no "Subnet" column. Operator reported
"where are the buttons?".

## Fix

1. `internal/db/db.go` — extend `User` struct with the 3
   v0.16.6 denorm fields: `SubnetCIDR`, `SubnetStatus`,
   `SubnetRouterNodeID`. Same columns `/mysubnet` bot reads.

2. `internal/db/queries.go` — extend `qSelectAllPortalUsers`
   from 6 to 9 columns. `GetAllPortalUsers` populates the
   denorm fields, so `/admin/users` can show
   "10.0.42.0/24 · active" inline without a JOIN.

3. `internal/db/portal_users.go` — scan the new columns.
   `subnet_router_node_id` is `TEXT` in SQLite (so empty is
   "" not 0), parsed via `strconv.ParseInt` with the
   `sql.NullString` "Valid" guard. Empty string + parse
   error → 0 (default).

4. `internal/handlers/templates/admin/users.html`:
   - New "Subnet" column (5th) between Role and Created.
     Each cell is a link to `/admin/users/{id}/subnet`.
     Shows CIDR + status pill:
       - green  `tag-success`   "active"
       - amber  `tag-warning`   "pending"
       - muted  plain           "disabled"
       - dim    "—"             (no subnet allocated)
   - "Subnet" link in the per-user `<details>` menu
     (alongside Reset password + Delete).

5. `internal/i18n/catalog.go` — 6 new keys × 2 langs:
   `user_subnet.column_header`, `cell_none`, `cell_pending`,
   `cell_active`, `cell_disabled`, `open_button`.
   Parity test green.

6. `internal/db/portal_users_test.go` — 2 new tests:
   `TestGetAllPortalUsers_PopulatesSubnetDenorm` and
   `TestGetAllPortalUsers_EmptyDenormDefaults`. Regression
   guard for column-count mismatches in
   `qSelectAllPortalUsers`.

## Verification

- 12/12 packages green on Windows
- Smoke 118/118 on VM
- Manual check: `/admin/users` page now has 7 columns
  (was 6), every row has a "Subnet" cell with status
  pill, every `<details>` menu has a "Subnet" link

Deployed to VM, live at build `3fc44a2`.
