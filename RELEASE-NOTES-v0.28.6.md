# v0.28.6 — Guarantee catalog (B1-B10 build + R1-R25 runtime)

After-action of the v0.28.5 incident: three independent bugs (migration
re-backfill, tagged-device ACL gap, stale Tailscale exit-node state)
shipped through `make test` + `make smoke`. The guarantee catalog is the
contract — every build must pass `make verify-pre` (10 build-time checks)
and every deploy must pass `make verify-post` (25 runtime checks via SSH
to the VM). If a check fails, the build/deploy is broken — do not push
or roll forward until it's fixed or the check is updated to reflect a
deliberate design change.

## What's new

### Build-time verification — `make verify-pre`

| # | Guarantee | How |
|---|-----------|-----|
| B1 | `go test ./...` exits 0 | `go test ./...` |
| B2 | `go vet ./...` exits 0 | `go vet ./...` |
| B3 | `go build ./cmd/skygate` produces a binary | `go build -o /tmp/x ./cmd/skygate` |
| B4 | i18n: ru and en key sets match | `go test ./internal/i18n/ -run TestCatalogsParity` |
| B5 | migration v0.47 idempotent (3 tests) | `go test ./internal/db/ -run TestMigrateV047` |
| B6 | ACL: per-device grant ordering + via opt-in + tagged-device loose | `go test ./internal/acl/...` |
| B7 | templates: all embed.FS templates parse | `go test ./internal/handlers/ -run TestLoadTemplates` |
| B8 | smoke RU+EN 83/83 each (VM only) | `make test` on VM; skipped on Windows |
| B9 | `RELEASE-NOTES.md` has an entry for the new version | `grep vX.Y.Z RELEASE-NOTES.md` |
| B10 | no `.env` / `*.key` / `*.pem` in git tracked paths | `git ls-files` filtered |

### Runtime verification — `make verify-post`

| # | Guarantee | What it catches |
|---|-----------|-----------------|
| R1 | `/healthz` 200, `status:ok` | Process dead |
| R2 | `/readyz` 200 (DB + headscale OK) | Dependency down |
| R3 | skygate build label = HEAD commit | Wrong binary deployed |
| R4 | `tailscaled` running inside skygate-host-1 | TUN missing |
| R5 | skygate-host-1 tailnet IP = 100.64.100.10 | Node not registered |
| R6 | skygate-host-1 does NOT use an exit-node (status line shows `linux  -`) | Stale exit-node in state → Docker bridge unreachable |
| R7 | Docker bridge 172.18.0.0/16 reachable from skygate-host-1 | Network namespace broken |
| R8 | headscale `/api/v1/policy` returns non-empty policy | Auth/connectivity broken |
| R9 | Live policy `updatedAt` ≈ last applied snapshot (`acl_snapshots.applied_success=1`) | Reapply needed |
| R10 | 4 per-user grants, `src=user@`, `dst` includes `autogroup:internet` | v0.28.3 minimum shape |
| R11 | ≥5 per-device loose grants (`src=tag:dev-*`, `dst=autogroup:internet`, NO `via`) | v0.28.5b tagged-device fix |
| R12 | No catch-all `src=*` → `autogroup:internet` | v0.28.3 bypass fix regression |
| R13 | `*` → `tag:public` AND `*` → `tag:exit-node` catch-alls present | SSH reachability to relays |
| R14 | `tagOwners` contains `tag:public`, `tag:exit-node`, `tag:private`, `tag:subnet-router` | Parser accepts policy |
| R15 | No per-device grant has `via` for `via_enabled=0` row | Migration re-backfill regression |
| R16 | Per-user grant `via` matches `user_exit_node_prefs.via_enabled` | Same regression |
| R17 | relay-1, relay-2, relay-3 online in headscale | Relay outage |
| R18 | Each exit-node advertises `0.0.0.0/0` | Real exit-node, not stub |
| R19 | DB: all per-user `via_enabled` match live policy | Cross-check |
| R20 | Migration v0.47 idempotent at runtime | (Same as B5; covered by build test) |
| R21 | `tailscaled.state` on disk has no stale `ExitNodeID` | Won't re-trigger the 504 path |
| R22 | `https://skygate.example.com/healthz` → 200 | HTTPS path works end-to-end |
| R23 | TLS cert is Let's Encrypt, > 7 days valid | Cert renewal gap |
| R24 | openresty upstream (`localhost:8080`) returns 200 | Not 504 |
| R25 | skygate-host-1 pings `8.8.8.8` with 0% loss | Direct internet works |

### How to extend the catalog

If you add a new invariant (e.g. a new migration, a new exit-node, a new
TLS SAN, a new required i18n key), add the check to
`scripts/verify_pre_deploy.sh` (build-time) and/or
`scripts/verify_post_deploy.sh` (runtime) **in the same PR** as the
change. The catalog is the test — code that ships without a check is
code that will silently regress.

If a check legitimately needs to be removed (e.g. a feature being
deprecated), remove the check in the same PR as the feature removal
and add a one-line note in the commit message explaining why.

## Cross-platform fixes during testing

The scripts work on Windows (Git Bash / WSL2), Linux, and macOS without
modification. Notable fixes during the dev cycle:

- **WSL2 + Go in "Program Files"** (path has a space): bash word-splitting
  breaks `export PATH`. Fixed by capturing absolute path in `$GO` and
  single-quoting inside `bash -c`.
- **SSH key on WSL2**: bash sees `/home/knaga` (WSL's HOME), not
  `/mnt/c/Users/knaga` (Windows HOME). Script searches multiple HOME
  candidates and uses `-i $SSH_KEY -o IdentitiesOnly=yes` explicitly.
- **headscale API not reachable from operator's machine**: all API calls
  go through SSH to the VM (`ssh_vm` / `curl_vm` helpers).
- **skygate container has no sqlite3, alpine image has no sqlite3**: copy
  DB out via `docker cp` and run sqlite3 on the VM host.
- **readyz response shape**: `{healthy:true,...}` not `{status:ok}`.
- **Tailscale status line trailing spaces**: `linux\s+-$` regex misses
  the trailing dash. Use `awk '{print $NF}' == "-"` instead.
- **`created_at` in `acl_snapshots` is Unix epoch int**, not ISO string.
  Convert with `date -u -d "@$epoch"`.
- **`.mode json` in `sqlite3 -c`**: newlines in the args get split by
  bash. Use a heredoc `<<'SQL'` instead.

## Operator workflow

```bash
# 1. Edit code on Windows
# 2. Local pre-deploy verification (Windows / Linux / Mac)
make verify-pre
# Should print: 9 PASS, 0 FAIL, 1 SKIP (B8 on Windows)

# 3. Commit and push
git add -p
git commit -m "..."
git push

# 4. On the VM, pull and restart
ssh admin@192.0.2.1 "cd /home/admin/skygate && git pull && docker compose up -d skygate"

# 5. Post-deploy verification
make verify-post
# Should print: 26 PASS, 0 FAIL
```

The `make verify` target runs both. The pre-deploy pass is the gate for
`git push`; the post-deploy pass is the gate for the new release being
declared green.

## Verification (this commit, live)

- `make verify-pre` → 9 PASS, 0 FAIL, 1 SKIP (B8 on Windows)
- `make verify-post` → 26 PASS, 0 FAIL
- All per-user `via_enabled=0` (Android-friendly default)
- 13 per-device loose grants (covers all tagged devices)
- 4 per-user grants with `autogroup:internet`
- skygate-host-1 pings `8.8.8.8` via relay-1 with 0% loss
- `https://skygate.example.com/healthz` → 200
- `tailscaled.state` has no stale `ExitNodeID` (v0.28.5c fix held)

## Refs

- v0.28.5 — explicit opt-in for `via` constraint (Android-friendly)
- v0.28.5a — migration v0.47 idempotency
- v0.28.5b — loose per-device grant for tagged devices
- v0.28.5c — entrypoint always clears stale Tailscale exit-node
- AGENTS.md "v0.28.5 guarantee catalog (B1-B10 + R1-R25)" — full table + how to extend

## Files

- `scripts/verify_pre_deploy.sh` (new) — 5.2 KB, 10 build-time checks
- `scripts/verify_post_deploy.sh` (new) — 23.5 KB, 25 runtime checks via SSH
- `Makefile` — `verify-pre`, `verify-post`, `verify` targets + `SHELL=bash` fix
- `AGENTS.md` — guarantee catalog section with both tables
- `RELEASE-NOTES.md` — v0.28.6 index entry
- `.gitignore` — `verify_*.sh` removed (now production, used by Makefile)
