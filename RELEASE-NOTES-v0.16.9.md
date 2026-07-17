# v0.16.9 — sidebar username + login remember me

2026-07-17

Two operator-reported issues, one underlying regression.

## 1. Sidebar username empty on /admin/users/{id}/subnet

Operator reported: when clicking the Subnet button (from
v0.16.8's new `<details>` menu), the new page rendered with
empty username in the sidebar and no admin nav links.

Root cause: v0.16.6's `renderUserSubnetPage` helper was
called with `c=nil` in all 4 handlers. The
`renderWithLayout` function only sets `data["Username"]` and
`data["IsAdmin"]` when c is non-nil, so the layout template
saw `Username=""` and `IsAdmin=false`. The empty user-name
span and the hidden admin nav (gated on `{{if .IsAdmin}}`)
made the page look like styles had broken.

Fix: pass the real `c` (already fetched via `currentUser`)
to `renderUserSubnetPage` in all 4 handlers. The helper
signature already takes c — this is just plumbing the value
through.

Test: `TestGetAdminUserSubnet_PopulatesSidebarUsername`
checks the rendered HTML for `class="user-name">skyadmin`
and `href="/admin/users"`. Verified the test fails when
reverted to c=nil.

## 2. Login "remember me" + browser autofill

Operator reported: "на странице login добавь обратно
возможность запоминать пароль и пользователя" — add back
the ability to remember password and username on /login.

The current login form had `autocomplete="off"` on the
username field and two dummy hidden inputs to actively
suppress browser autofill. Both removed.

### Changes

a) `internal/handlers/templates/login.html`:
   - Removed the two hidden dummy inputs
   - username input: `autocomplete="off"` → `"username"`
     (browser saves + pre-fills)
   - password input: already `autocomplete="current-password"`
   - Added a "Remember me" checkbox (default off). When
     checked, the session cookie lifetime is extended.
   - Added `value="{{.LastUsername}}"` on the username
     input — pre-fills from a long-lived `last_username`
     cookie even after the user logs out.

b) `internal/handlers/handlers_auth.go`:
   - `GetLogin`: read `last_username` cookie, populate
     `data["LastUsername"]` for template pre-fill
   - `PostLogin`:
     - Read `remember` form value
     - If set: `sessionHours = 30*24` (was `SessionHours`
       default 24); else `SessionHours`
     - Set `last_username` cookie (365 days, not HttpOnly,
       no credential material — just the username)

c) `internal/i18n/catalog.go`:
   - `login.remember_label` RU+EN
   - "Запомнить меня (30 дней)" / "Remember me (30 days)"

## Security

- `last_username` cookie is NOT HttpOnly (template needs
  the value server-side; could be HttpOnly + read via JS
  later if we want stricter)
- Holds only the username, no credential material
- Same SameSite=Lax policy as the session cookie
- "Remember me" extends the SESSION cookie lifetime, not
  the JWT itself — the server still rotates the JWT
  normally; only MaxAge changes

## Tests

- 12/12 packages green on Windows
- `TestGetAdminUserSubnet_PopulatesSidebarUsername` (new):
  pins the c=nil fix
- Existing `TestLoadTemplates`, `TestCatalogsParity`,
  `TestHTMLSafeCatalog`, `TestTemplateArgsMatchCatalog`
  all still green

## Manual verification on VM

- `/admin/users/1/subnet` shows "skyadmin" in sidebar
- `/admin/users/1/subnet` shows admin nav links
  (Пользователи, ACL, DERP, Audit, etc.)
- `/login` shows "Запомнить меня" checkbox
- After login + logout, `/login` pre-fills username from
  cookie
- After login with checkbox, session cookie MaxAge = 30
  days (2,592,000 seconds)

Deployed to VM, live at build `e555698`.
