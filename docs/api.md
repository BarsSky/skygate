# HTTP API

This is the full HTTP surface of Skygate. Every route registered in
`cmd/skygate/main.go`, with auth requirements, the handler that
services it, and a curl example.

> All examples assume Skygate at `http://localhost:8080`. The
> `session` cookie is whatever your `/login` POST returns; the
> `Authorization: Bearer` token is from `/my/tokens`.

## Conventions

- **Auth scopes:** `public` (no auth), `cookie` (any logged-in user,
  JWT cookie), `api` (cookie OR `Authorization: Bearer`), `admin`
  (logged-in user with `is_admin=1`).
- **Form data:** `application/x-www-form-urlencoded` unless noted.
- **i18n:** Pass `Accept-Language: ru` or `Accept-Language: en` to
  switch response language for HTML pages and `Content-Type:
  text/html` errors. JSON responses are language-agnostic.
- **Rate limits** (in-memory, single-instance only):
  - `POST /login`: 5 attempts per username per 15s, 20 per IP per 30s
  - `/api` endpoints: 30 requests per IP per 60s
  - 429 + `Retry-After: <seconds>` on block

## Public

### `GET /login`

Login page. If already logged in, redirects to `/dashboard`.

```bash
curl -I http://localhost:8080/login
# HTTP/1.1 200 OK
```

### `POST /login`

Authenticate. Sets `session` cookie. Rate-limited (see above).

```bash
curl -i -c cookies.txt -X POST http://localhost:8080/login \
  -d "username=skyadmin&password=$SKYGATE_ADMIN_PASS"
# HTTP/1.1 302 Found
# Location: /dashboard
# Set-Cookie: session=eyJhbGciOiJIUzI1NiIs...; HttpOnly; SameSite=Lax
```

Errors: 401 with `Wrong username or password` (also writes
`audit_log` row `action='login_fail'`).

### `POST /lang`

Switch UI language. Body: `lang=en` or `lang=ru`. Sets the `lang`
cookie for 1 year.

```bash
curl -b cookies.txt -c cookies.txt -X POST http://localhost:8080/lang \
  -d "lang=ru"
# 204 No Content
```

### `POST /logout`

Clears the session cookie.

```bash
curl -b cookies.txt -X POST http://localhost:8080/logout
# 204 No Content
```

### `GET /favicon.ico` / `GET /favicon.svg`

Static favicons. Cached.

### `GET /static/*`

Static files (CSS). Path: `/static/css/themes.css` etc.

### `GET /`

Redirects to `/dashboard` (or `/login` if not authed).

### `GET /settings/theme`, `POST /settings/theme`

Theme switcher. Body: `theme=linear|classic|solar|mono`. Sets
`theme` column on `portal_users`.

```bash
curl -b cookies.txt -X POST http://localhost:8080/settings/theme \
  -d "theme=solar"
# 204 No Content
```

## Authenticated — common

### `GET /dashboard`

User's home page. Shows recent rules, preauth key stats, and a
device summary. (admin sees admin-specific widgets.)

### `GET /help`

Full help page. Linked from `/my/exit-rules/help` and the sidebar.

## Authenticated — user self-service (`/my/*`)

### `GET /my/devices`

Your devices (filtered by `node_owner_map`).

### `GET /my/exit-nodes`

List of available exit-nodes (from `exit_servers`).

### `POST /my/preauth`

Issue a 1-hour single-use preauth key for **your** headscale user.

```bash
curl -b cookies.txt -X POST http://localhost:8080/my/preauth
# {"key":"tskey-auth-kXYZ1234...","expires_at":1785153600,"headscale_preauth_id":"5"}
```

### `GET /my/keys`

List your preauth keys with status (fresh / used / expired).

### `POST /my/keys/{id}/expire`

Mark a key as expired in the local DB. (Headscale-side expiry
happens via `headscale preauthkeys expire`.)

### `GET /my/tokens`

List your personal API tokens (label + last_used_at).

### `POST /my/token`

Create a new personal API token. Body: `label=My%20CLI`.

```bash
curl -b cookies.txt -X POST http://localhost:8080/my/token \
  -d "label=OpenCode%20CLI"
# {"id":3,"token":"skyg_pat_…","label":"OpenCode CLI"}
# The token is shown ONCE — store it now.
```

### `POST /my/token/{id}/revoke`

Revoke a token. Idempotent.

### `GET /my/account`

Self-service password change form.

### `POST /my/account/password`

Change your own password. Body: `current=…&new=…&confirm=…`.

```bash
curl -b cookies.txt -X POST http://localhost:8080/my/account/password \
  -d "current=oldpass&new=newpass123&confirm=newpass123"
# 302 Found /my/account (with flash)
```

### `GET /my/exit-rules`

Your exit-rules UI (filter, search, multi-delete, cascade).

### `POST /my/exit-rules`

Add an exit-rule. Body: `device_id=…&exit_node_id=…&target_type=domain|ip&target_value=…&action=accept|deny`.

```bash
curl -b cookies.txt -X POST http://localhost:8080/my/exit-rules \
  -d "device_id=8&exit_node_id=2&target_type=domain&target_value=google.com&action=accept"
# 302 Found /my/exit-rules (with flash)
```

### `POST /my/exit-rules/delete`

Delete one or many rules. Body: `id=42` (single) **or**
`ids=1&ids=2&ids=3` (many) **or** a union of both.

```bash
# single
curl -b cookies.txt -X POST http://localhost:8080/my/exit-rules/delete \
  -d "id=42"
# multi
curl -b cookies.txt -X POST http://localhost:8080/my/exit-rules/delete \
  -d "ids=1&ids=2&ids=3"
# 302 Found /my/exit-rules
```

### `GET /my/exit-rules/api` (Bearer or cookie)

Public REST API. Returns your rules as JSON.

```bash
curl -H "Authorization: Bearer $SKYGATE_PAT" \
  http://localhost:8080/my/exit-rules/api
# [
#   {"id":42,"device_id":8,"exit_node_id":"2","target_type":"domain",
#    "target_value":"google.com","action":"accept","enabled":1,"created_at":1785153600},
#   …
# ]
```

### `POST /my/exit-rules/api` (Bearer or cookie)

Bulk create rules. JSON body. **Returns `{added, duplicates, errors,
ids: [N1, N2, ...]}` so the client can clean up.**

```bash
curl -X POST -H "Authorization: Bearer $SKYGATE_PAT" \
  -H "Content-Type: application/json" \
  -d '[
    {"device_id":8,"exit_node_id":"2","target_type":"domain",
     "target_value":"google.com","action":"accept"},
    {"device_id":8,"exit_node_id":"2","target_type":"ip",
     "target_value":"1.1.1.1","action":"accept"}
  ]' \
  http://localhost:8080/my/exit-rules/api
# {"added":2,"duplicates":0,"errors":[],"ids":[101,102]}
```

### `GET /my/exit-rules/help`

Full help page with API reference and curl examples.

## Authenticated — admin (`/admin/*`)

> All `/admin/*` routes require `is_admin=1` in the JWT claims.
> Non-admin users get 403.

### `GET /admin/users`

List portal users.

### `POST /admin/users`

Create a portal user. Body: `username=…&password=…&is_admin=0|1`.

```bash
curl -b cookies.txt -X POST http://localhost:8080/admin/users \
  -d "username=alice&password=alice123&is_admin=0"
# 302 Found /admin/users
```

### `POST /admin/users/{id}/delete`

Delete a user. Cascades: drops their preauth keys, device_rules,
node_owner_map, telegram_bindings.

### `POST /admin/users/{id}/reset-password`

Admin-initiated password reset. Body: `password=…`.

### `GET /admin/devices`

List all nodes across all namespaces, with tag/un-tag buttons.

### `POST /admin/nodes/{id}/tag`

Manually tag a node. Body: `tag=tag:private|tag:public|tag:exit-node`.
Goes via `docker exec headscale headscale nodes tag` (admin API
lacks the permission).

### `POST /admin/nodes/{id}/untag`

Manually untag. Same path.

### `GET /admin/audit`

Audit log. Optional filters: `?action=login_fail&user=alice`.

```bash
curl -b cookies.txt "http://localhost:8080/admin/audit?action=login_fail&user=alice"
```

> Date filter is not yet implemented (TODO in roadmap).

### `GET /admin/acls`

Read-only view of the live headscale ACL.

### `GET /admin/derp`, `GET /admin/derp/refresh`

DERP relay status (peers, conn summary). `refresh` re-fetches from
the local derper debug endpoints.

### `GET /admin/backup`

ACL backup/restore page.

### `POST /admin/backup/save`

Snapshot the current headscale ACL into `acl_snapshots`.

### `POST /admin/backup/restore`

Restore a snapshot. Body: `snapshot_id=N`.

### `GET /admin/backup/download`

Download the latest `acl_snapshots.config` blob as JSON.

### `GET /admin/settings`, `POST /admin/settings`

Per-user rule limits, max total rules, DNS auto-update toggle.

### `GET /admin/telegram`, `POST /admin/telegram`

Telegram bot config UI. See [docs/TELEGRAM.md](TELEGRAM.md).

### `GET /admin/exit-rules`

Cross-user hierarchical view (User → Device → Exit-Node → Rules).

### `POST /admin/exit-rules/rollback`

Restore a previous ACL snapshot. Body: `snapshot_id=N`.

```bash
curl -b cookies.txt -X POST http://localhost:8080/admin/exit-rules/rollback \
  -d "snapshot_id=42"
```

### `GET /admin/exit-rules/sync`

Re-trigger advertised-routes sync to all exit-nodes.

### `GET /admin/exit-rules/nodes`

JSON dump of nodes for cleanup / dedup.

### `GET /admin/exit-rules/cleanup`

Cleanup page (merge duplicate `device_id`s).

### `POST /admin/exit-rules/cleanup/apply`

Apply the cleanup. Body: `merge_from=…&merge_to=…`.

### `GET /admin/exit-nodes`, `POST /admin/exit-nodes/add`, `POST /admin/exit-nodes/delete`, `POST /admin/exit-nodes/sync`

Exit-node CRUD + manual sync trigger.

## Error responses

| Status | When | Body |
|---|---|---|
| 400 | Bad form input | `Bad Request: <details>` (text/plain) or JSON `{"error":"…"}` |
| 401 | No session cookie, or cookie expired/invalid | Redirect to `/login` (HTML) or `401 Unauthorized` (JSON) |
| 403 | Logged in but not admin (for `/admin/*`); or trying to delete another user's rule | `Forbidden` (text/plain) |
| 404 | Unknown route | HTML 404 page |
| 429 | Rate-limited | `429 Too Many Requests` + `Retry-After: <seconds>` |
| 500 | Internal error | `Internal Server Error` (text/plain) + `audit_log` row |

## Static route audit

`scripts/audit_routes.py` cross-checks every `mux.HandleFunc(...)`
in `cmd/skygate/main.go` against the actual `func (a *App) Foo(...)`
declarations in `internal/handlers/*.go`. It runs in CI
(`.github/workflows/ci.yml`) and as part of `make test`.

If you add a route and forget to register a handler (or vice versa),
the script fails and CI goes red. Always run it locally before
pushing.

## See also

- [docs/architecture.md](architecture.md) — request flow
- [docs/db-schema.md](db-schema.md) — what gets read/written
- [docs/TELEGRAM.md](TELEGRAM.md) — bot commands (not HTTP, but same auth model)
- [scripts/smoke.sh](../scripts/smoke.sh) — the bilingual HTTP smoke test
