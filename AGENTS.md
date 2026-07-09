# AGENTS.md — AI hints for Skygate

This file is for AI assistants (Hermes, Claude, Cline, Cursor, etc.) working on
or with Skygate. Read this **first** before suggesting changes or running tasks.

---

## What is Skygate?

Tailscale/headscale management portal. Stack: **Go 1.23 + SQLite + Docker + headscale 0.29 API + embedded HTML templates**.

Key features: exit-node rules with per-device accept/deny ACL, automatic DNS-driven /32 resolution for domains, multi-user, per-user rule limits, cleanup of orphaned /32, sync to exit-node advertised-routes.

User-facing pages:
- `/my/exit-rules` — user's own rules (add / delete / filter / search)
- `/my/exit-rules/help` — full help page with API reference
- `/admin/exit-rules` — admin view of all users' rules
- `/admin/exit-rules/cleanup` — admin: merge duplicate device_ids, backfill device_ip
- `/admin/exit-rules/sync` — admin: trigger advertised-routes sync
- `/my/tokens` — personal API tokens

API:
- `GET/POST /my/exit-rules/api` — list / bulk create rules (Bearer auth or cookie)
- `POST /my/exit-rules/delete` — delete one (`id=X`) or many (`ids=X&ids=Y&...`)

---

## Code structure (where to look)

```
cmd/skygate/main.go                          — entry point, HTTP routes
internal/handlers/exit_rules.go              — main handler (1779 lines, the bulk of logic)
internal/handlers/exit_rules_cleanup.go       — admin cleanup handler + template
internal/handlers/templates/exit_rules.html   — /my/exit-rules UI
internal/handlers/templates/exit_rules_help.html — /my/exit-rules/help page
internal/config/config.go                    — env-based config
internal/handlers/templates.go                — `//go:embed` for all HTML templates
deploy/{deploy,backup,validate}.sh            — deployment scripts
```

---

## Database schema (key tables)

```
device_rules(
  id, user_id, device_id, exit_node_id,
  target_type,   -- 'ip' | 'subnet' | 'domain'
  target_value,  -- "1.2.3.4" | "10.0.0.0/24" | "example.com"
  action,        -- 'accept' | 'deny'
  device_ip,     -- tailscale IP of device (backfilled)
  parent_domain, -- for /32 auto-resolved by autoupdater: original domain
  enabled, created_at
)
```

**`parent_domain` is the key field for autoupdater tracking.** If empty → manual rule. If non-empty → /32 created by autoupdater for that domain.

Other tables: `portal_users`, `node_owner_map`, `acl_snapshots`, `exit_rule_logs`, `exit_servers`, `preauth_keys`, `audit_log`, `personal_api_tokens`.

---

## Conceptual rules (read before changing exit_rules.go)

1. **User-facing vs autoupdater /32**: `target_type='subnet' AND parent_domain != ''` is autoupdater-managed. These do NOT count toward user limits (200 per device, 200 per user, 10000 system).

2. **Cascade delete**: deleting a `target_type='domain'` row MUST also delete all `/32` rows with the same `parent_domain`. Otherwise orphaned /32 rules remain for unfollowed domains.

3. **Multi-delete**: handler accepts both `id=X` (legacy single) and `ids=X&ids=Y&ids=Z` (multi). They are **unioned**, not "if one is set, the other is ignored". Always call `r.ParseForm()` explicitly — `r.Form` is lazy in Go stdlib.

4. **IP without `/32`**: when user adds `target_type=ip, target_value=5.5.5.5` (no CIDR), the handler auto-appends `/32`. headscale `approve-routes` rejects bare IPs with `no '/'` error.

5. **Shared IP dedup**: if a /32 already exists for `(user, device, exit_node, target_value)` under a different `parent_domain`, do NOT create a duplicate row. autoupdater won't delete it as long as any domain needs it.

6. **Dedup on Add**: `insertRuleUnique` checks `(user, device, exit_node, target_type, target_value)` tuple. Returns existing id if found. The "partial" redirect path (some IPs new, some exist) requires that dedup also considers parent_domain.

7. **Autoupdater runs every 5 min** (`SKYGATE_DNS_AUTO_CHECK`, default 5m). For each `target_type='domain'` row, it:
   - DNS-resolves the domain (skip IPv6)
   - Adds known subdomains (e.g. `static.rutracker.cc` for `rutracker.org`)
   - Inserts missing /32 with `parent_domain=<this domain>`
   - Deletes /32 with `parent_domain=<this domain>` whose IP is no longer in DNS

8. **Staggered sync**: `SyncAdvertisedRoutes()` collects ALL enabled IP/subnet rules per exit-node and pushes **one** `SetAdvertisedRoutes` per node. Don't split into batches — earlier "per-batch" code lost rules because the last batch overwrote previous. Commit `5287d6a` fixed this.

---

## Common gotchas

- **Container build is SLOW (1–5 min)** because of CGO sqlite. Don't cancel a restart in progress — wait for `🌐 ready at http://localhost:8080` in logs.
- **DB is a named volume** (`skygate-data` mounted at `/data` inside container). `docker cp skygate:/data/skygate.db` alone gives stale snapshot; copy `db + db-wal + db-shm` together.
- **`.git/objects/` ownership**: after `git add` from skyadmin, objects can be owned by root. Fix: `sudo chown -R skyadmin:skyadmin /home/skyadmin/skygate/.git`.
- **`//go:embed`**: templates are inlined into the binary at build time. After editing a `.html` file, `docker compose restart` will rebuild the binary and the new template will be live.
- **Go template parser hates em-dash (—, U+2014)** inside `{{/* */}}` comments — gives "comment ends before closing delimiter". Use `-` or HTML comments `<!-- -->` outside the template action syntax.
- **NPM reverse proxy caches HTML**: if you change templates and don't see updates, add `?t=` to URL.
- **Hermes sandbox masks secrets** (`hskey-...`) in subprocess stdout — don't `cat .env` via `subprocess.run([...])` and expect to see keys. Use `ssh agent 'sudo -u skyadmin bash -c "..."'` for ad-hoc commands on remote.
- **Bash escaping in `ssh`**: avoid inline `python3 -c "..."` (bash misinterprets parens). Write `.sh` + `.py` separately and source.

---

## What to do when user asks for a rule change

If user says "add rules for X" via AI:
1. Get a token from `/my/tokens` (or use existing).
2. Use `POST /my/exit-rules/api` with JSON body `{"rules": [...]}` for bulk.
3. For domains, the server auto-resolves via DNS and creates /32 rows. Autoupdater refreshes every 5 min.
4. Don't try to compute /32 from domain — the server does it. Just send `target_type=domain, target_value=example.com`.

If user says "delete rule X":
1. Find id via `GET /my/exit-rules/api`.
2. POST to `/my/exit-rules/delete` with `ids=123&ids=456` for multi, or `id=123` for single.
3. If X is a domain, cascade will clean up its /32 children.

If user says "why can't I add rules? limit says exceeded":
- Check `parent_domain` distribution: `SELECT COUNT(*) FROM device_rules WHERE enabled=1 AND (target_type!='subnet' OR COALESCE(parent_domain,'')='');`
- The "user-facing" count should be < 200 (per-device default). If higher, user has too many manual /32 rules.
- Solution: either remove manual /32 or raise `SKYGATE_USER_MAX_RULES` in `.env` (format: `user1:500,user2:1000`).

---

## What NOT to do

- Don't add new "per-domain rule" tables — domain rules are just rows in `device_rules` with `target_type='domain'`.
- Don't store resolved IPs at Add time only — autoupdater needs to refresh them. Always let the autoupdater own the /32 lifecycle.
- Don't bypass cascade delete for "speed" — orphaned /32 break advertised-routes and confuse autoupdater.
- Don't change the advertised-routes sync to per-batch — see commit `5287d6a`. One `SetAdvertisedRoutes` per node, aggregated.
- Don't add `//go:embed` for individual HTML files — they're already embedded via `templates.go:15`.

---

## Testing changes

```bash
# After editing source on 13.69, restart and watch for build:
ssh agent 'cd /home/skyadmin/skygate && docker compose restart skygate'
ssh agent 'while pgrep -f "go build" >/dev/null 2>&1; do sleep 3; done'
ssh agent 'docker logs --tail 10 skygate 2>&1 | tail -5'

# Live test multi-delete (must work both as id= and ids= form):
ssh agent '. /home/skyadmin/skygate/.env; rm -f /tmp/ck; curl -s -c /tmp/ck -X POST --data-urlencode "username=skyadmin" --data-urlencode "password=${SKYGATE_ADMIN_PASS}" http://localhost:8080/login -o /dev/null; curl -s -i --max-redirs 0 -b /tmp/ck -X POST http://localhost:8080/my/exit-rules/delete --data-raw "ids=1&ids=2&ids=3" | head -3'

# Verify advertised-routes sync (no CIDR-parse errors):
ssh agent 'docker logs --since 5m skygate 2>&1 | grep -c "no /"'
```

The verification script `/tmp/hermes-verify-exit-rules-v3.sh` (runnable via `scp` + `sudo -u skyadmin bash`) covers all the above — 20 PASS / 0 FAIL expected.
