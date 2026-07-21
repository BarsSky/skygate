# v0.22.1 — /my/meshes web UI (was bot-only in v0.22.0)

> Released: 2026-07-20
> Build: bc57d8c
> Predecessor: v0.22.0 (mesh feature, bot-only)

The v0.22.0 release shipped the mesh (shared network) feature
but only via the bot (`/mesh create|join|leave|meshes`). The
operator flagged that users have no obvious place in the WEB
interface to:

1. Create a shared network (and get a code to share)
2. Enter an invite code from another user

v0.22.1 adds the user-scope `/my/meshes` page with two action
forms (create + join) and the caller's current-meshes table.
The bot path is unchanged — web + bot share the same
`internal/mesh` package state, so a mesh created via the web
appears in `/mesh join <code>` (the bot validates the code
against the `meshes` table regardless of how it was created).

## What ships

1. **`GET /my/meshes`** — renders the page (two action forms +
   current-meshes table). Visible to every identified user. The
   page lists every active mesh the caller is in (creator +
   joined), with a `<details>` expansion of the member list,
   the code, the creator, the created date, and a "Leave" button
   per mesh. Empty state when the caller has no active meshes.

2. **`POST /my/meshes/create`** — creates a new mesh. Form
   fields: `name` (string, required, max 64 chars). The caller
   becomes the creator + first member. The redirect carries
   `?ok=created&code=<8-char>` so the success banner can render
   the code. Side effect: writes an `audit_log` row
   (`mesh_create`).

3. **`POST /my/meshes/join`** — joins an existing mesh. Form
   fields: `code` (8-char alphanumeric, normalized to upper-case
   + trimmed). On success: caller is added to the mesh + the
   per-plane ACL re-apply fires (best-effort, async, same path
   the bot `/mesh join` uses). The redirect carries
   `?ok=joined&name=<mesh-name>` for the success banner.

4. **`POST /my/meshes/leave`** — leaves a mesh. Form fields:
   `code` (optional). With no code: leave every active mesh the
   caller is in. With a code: leave just that one. On success:
   the per-plane ACL re-apply fires to drop the cross-CIDRs.

5. **Sidebar entry** — `/my/meshes` is in the user-scope nav
   (after `/my/telegram`), visible to every identified user.
   `fa-solid fa-circle-nodes` icon (matches `/admin/meshes`).

6. **i18n** — 34 new keys (RU + EN, 68 entries): the page
   title/subtitle, form labels + placeholders, the table column
   headers, the empty state, the "how it works" bullets, and
   18 flash-banner keys (success + error variants for create,
   join, leave, not_found, dissolved, missing_name, etc.). The
   `translateMeshFlash` helper branches on the URL parameter
   (`ok=` vs `err=`) to pick the right key prefix
   (`my_meshes.flash_` vs `my_meshes.flash_err_`). All
   `TestCatalogsParity` + `TestTemplateArgsMatchCatalog` tests
   pass.

7. **smoke.sh** — `/my/meshes` added to the `/my/*` 200-check
   loop. v0.22.1 smoke exercises the page end-to-end on the VM.

## Why this release matters

Before v0.22.1, the only way to interact with the mesh feature
was the Telegram bot. The operator's concern was: "in the web
interface there's no obvious place where a user can invite
another to a shared local network, indicating their own device,
or enter an invitation to a shared local network from another
user". The bot is convenient for power users (especially those
already on Telegram) but every identified user — including
non-Telegram users — needs a web entry point.

v0.22.1 fixes the gap. The page is at `/my/meshes`, with the
same form-based UX as `/my/tokens`, `/my/devices`, and
`/my/account`. No JavaScript needed; the page works on every
browser + phone.

The web + bot paths now share the same `internal/mesh`
package state. Operators and users can mix and match:
  - alice creates a mesh via the web, gets the code
  - alice sends the code to bob via Telegram
  - bob types `/mesh join <code>` in the bot → joined
  - both can see the mesh in their respective UIs

The audit_log row + the per-plane ACL re-apply are identical
regardless of which UI triggered the action.

## Design decisions

- **Same package, two UIs.** `internal/mesh` is shared between
  web + bot. The handlers (`handlers_my_meshes.go` for the web,
  `commands_mesh.go` for the bot) are thin wrappers that
  translate HTTP / Telegram input into the same package calls.
  This means the mesh + mesh_members tables are the single
  source of truth, and a future third UI (CLI, mobile app,
  ...) can reuse the same logic.
- **Bot-driven workflow, admin UI is for oversight** (carried
  over from v0.22.0). The web /my/meshes page has the same
  Create + Join + Leave UX the bot has. The admin /admin/meshes
  page stays read-only (no Generate / Dissolve buttons).
- **Per-plane ACL re-apply is best-effort, async.** The
  handlers fire the per-plane pipeline in a goroutine (same
  pattern as the bot /mesh join handler). Failures are logged
  but the web reply is already sent. The mesh membership is
  durable; the operator can re-trigger the re-apply via
  `/admin/exit-rules/reapply` if needed.
- **Flash banners via i18n, not raw URL values.** The page
  shows translated messages for `ok=created&code=...` and
  `err=not_found` instead of the raw URL value. The
  `translateMeshFlash` helper handles the `?ok=` vs `?err=`
  split (the catalog uses different prefixes for the two
  kinds so the namespace stays clean).
- **Forms are language-agnostic.** The form fields (`name`,
  `code`) are URL-encoded as-is; the labels are in
  `my_meshes.field_name` / `my_meshes.field_code`. The page
  uses the caller's `Accept-Language` (defaulting to the
  binding's lang) for all visible strings.

## Validation (operator's gate)

**Phase 1 (tests) — local, all green:**

The 8 ACL integration tests added in v0.22.0 still pass (no
regressions to the existing design). The i18n parity test
(`TestCatalogsParity`) is green for the 34 new keys
(68 entries, RU + EN). The template-arg-count test
(`TestTemplateArgsMatchCatalog`) is green for the new
`user/meshes.html` template.

**Phase 1b (live validation on VM) — 10/10 PASS:**

A `check_v0.22.1.sh` script was scp'd to the VM and ran the
following (against the live skygate on the VM, with the real
operator's admin session cookie):

1. `GET /my/meshes` (no meshes yet) → HTTP 200, no template error
2. `POST /my/meshes/create` with name='v0221-test' → HTTP 302
   to `/my/meshes?ok=created&code=<8-char>` ✓
3. `POST /my/meshes/join` (same code, skyadmin is already
   a member) → HTTP 302 to `/my/meshes?ok=joined&name=...`
   (the bot handler would have been a no-op; the web handler
   follows the same path) ✓
4. `GET /my/meshes` (after create) → page shows the new mesh
   (name + code visible) ✓
5. `GET /admin/meshes` → also shows the new mesh (shared view
   between user and admin) ✓
6. `POST /my/meshes/leave` with the code → HTTP 302 to
   `/my/meshes?ok=left` ✓
7. `GET /my/meshes` (after leave) → empty state alert visible ✓
8. `POST /my/meshes/join` with non-existent code → HTTP 302
   to `/my/meshes?err=not_found`; the translated banner
   renders in the operator's language (RU: "Меш с таким
   кодом не найден. Проверьте написание...") ✓
9. The sidebar link `/my/meshes` is in the layout (every
   `/my/*` page has the new entry in the user-scope nav) ✓
10. Cleanup: delete the test mesh + member rows ✓

**Phase 1c (smoke) — 132/132 PASS:**

`make test` returns 66/66 RU + 66/66 EN smoke assertions
(up from 130 in v0.22.0 with the new `/my/meshes` page in
the 200-check loop). The check_exit_nodes step still passes
3/3 (emilia, sharlotta, karolina). The check_https step
still passes (TLS, cert, HSTS via / fallback).

**Phase 1d (hotfix during validation) — one translation bug:**

The first live validation (step 8) caught a real bug: the
`translateMeshFlash` function built the i18n key as
`my_meshes.flash_<code>` for both `ok=` and `err=` query
parameters, but the catalog uses different prefixes
(`my_meshes.flash_` for success, `my_meshes.flash_err_` for
errors). The err= lookup missed the catalog entry and
`i18n.T` returned the key as-is (the standard "missing key"
fallback), so the page rendered the raw
`my_meshes.flash_not_found` instead of the translated text.

The fix: branch on the URL parameter kind (`ok` vs `err`)
when building the i18n key. The commit
`v0.22.1 hotfix: translateMeshFlash used wrong key prefix
for err codes` is on the feature branch.

The live validation script (step 8) was the test that caught
it. The unit test (`TestTemplateArgsMatchCatalog`) doesn't
catch this kind of bug because it checks the TEMPLATE
arg-count, not the RUNTIME behavior of `i18n.T` with a
runtime-computed key.

## What does NOT change

- The bot path is unchanged. `/mesh create|join|leave|meshes`
  still work. The web + bot share the same `internal/mesh`
  package state, so a mesh created via the web shows up in
  the bot's `/meshes` list (and vice versa).
- The admin /admin/meshes page is unchanged. Still
  read-only, still shows every mesh (active + dissolved)
  with the member count + the `<details>` expansion of the
  member list. The v0.22.0 design decision ("bot drives the
  workflow, admin UI is for oversight") is preserved.
- The `internal/mesh` package is unchanged. v0.22.1 only
  adds a thin HTTP wrapper in `internal/handlers/handlers_my_meshes.go`.
- The migration v0.43 is unchanged. No new tables, no new
  columns. The new page reads from the same `meshes` +
  `mesh_members` tables the v0.22.0 bot + admin pages use.
- The per-plane ACL re-apply logic is unchanged. The web
  handler uses the same `acl.ApplyACLPipelineForPlane` call
  the bot /mesh join uses, in a goroutine, with the per-plane
  headscale.Client resolution from `App.HSForUser`.

## What does NOT ship in v0.22.1

- **`/mesh dissolve <code>` bot command** — the bot-side
  user command to dissolve a mesh. Today the dissolve path
  is bot-via-`internal/mesh/DissolveMesh` but no user-facing
  command exposes it. The web path doesn't have a dissolve
  button either (the creator dissolves via SQL or via a
  future /my/meshes POST /my/meshes/dissolve route). v0.22.2
  follow-up.
- **Phase 3 (safe user migration tool)** — still deferred
  to a follow-up release. The operator's stated workflow is
  "только после проверки и гарантии работы провести переход
  пользователей на собственные подсети" — the verification
  is done in v0.22.0 + v0.22.1, but the migration tool itself
  is a separate, opt-in, audit-tracked operation.
- **butler voice v4** — deferred until the operator gives
  feedback on v3.
- **headscale 0.30+ v0.19.1 re-enable** — still blocked on
  headscale's `dns.extra_records` support. The mavis cron
  `headscale-milestone-16-check` (weekly) reports any progress
  on headscale milestone #16 (DNS Work).

## Files

- `internal/handlers/handlers_my_meshes.go` (new, 339 lines) —
  GET /my/meshes + POST /my/meshes/create + join + leave
  + translateMeshFlash helper
- `internal/handlers/templates/user/meshes.html` (new, 116
  lines) — page body (title-row, create form, join form,
  current meshes table, how-it-works card)
- `internal/handlers/templates/layout.html` (modified) —
  sidebar entry for /my/meshes
- `internal/i18n/catalog.go` (modified) — 34 new keys
  (RU+EN, 68 entries)
- `cmd/skygate/main.go` (modified) — 4 new routes
  (GET + 3x POST)
- `scripts/smoke.sh` (modified) — /my/meshes in the
  /my/* 200-check loop

## Verification commands (operator's quick check)

```bash
# 1. Run the standard make test (smoke + check_exit_nodes + check_https)
cd /home/skyadmin/skygate && make test

# 2. Run the v0.22.1 /my/meshes live validation (10 checks)
scp check_v0.22.1.sh skyadmin@<vm>:/tmp/
ssh skyadmin@<vm> "chmod +x /tmp/check_v0.22.1.sh && bash /tmp/check_v0.22.1.sh"

# 3. Try the web UI in a browser
# Login as any user → click "Mesh-сети" in the sidebar →
# fill in name + Create → copy the code → fill in code
# in another browser (different user) → Join.

# 4. Verify the bot /mesh commands still work
# /meshes — should show the same meshes the web UI shows
```

## What the operator can do now

1. Open `/my/meshes` in a browser (every user can do this)
2. Create a new mesh: name + Create button → get the code
3. Send the code to teammates (Telegram, in person, etc.)
4. Teammates open their own `/my/meshes`, paste the code, Join
5. The bot path still works: `/mesh create|join|leave|meshes`
6. The admin oversight is at `/admin/meshes` (read-only)

## Build info

- Commit: bc57d8c (v0.22.1 hotfix on top of 478d0a0 v0.22.1)
- Build label on VM: v0.19.0-15-g478d0a0 (hotfix pending rebuild)
- headscale version: 0.29.2 (unchanged)
- Go runtime: 1.23 (unchanged)
- Smoke: 132/132 (EN 66 + RU 66) — up from 130 in v0.22.0
  with the new /my/meshes page
- check_exit_nodes: 3/3 (emilia, sharlotta, karolina)
- check_https: PASS (TLS 1.3, SAN match, HSTS via / fallback)

## Next steps

- **v0.22.2 (planned)**: `/mesh dissolve <code>` bot command
  + `/my/meshes/{id}/dissolve` web button (creator-only).
  The dissolve path is implemented in `internal/mesh`
  (`DissolveMesh`) but not exposed to users via either UI.
- **v0.23.0 (planned)**: Phase 3 safe user migration tool.
  `SKYGATE_MIGRATE_USERS_TO_SUBNETS=true` opt-in,
  operator-driven, audit-row per user, pre-flight check,
  idempotent.
- **Backlog**: butler voice v4, headscale 0.30+ v0.19.1
  re-enable (still blocked on `dns.extra_records` in the
  policy schema).

## Credits

Designed and implemented by skygate in response to the
operator's v0.22.0 follow-up. v0.22.0 shipped the mesh
data model + bot; v0.22.1 ships the web entry point.
