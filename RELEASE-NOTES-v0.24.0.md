# v0.24.0 — subnet-router setup tooling (2026-07-21)

The "operator guide for getting a per-user subnet-router
running end-to-end" release. Backend (`sidecar.SyncOnce`,
`GeneratePreauth`, `BuildPreauthInfo`) has been in place
since v0.16.7, but no operator-facing tooling existed for
it — admins had to read Go source to figure out what to
tell users to do. v0.24.0 ships the two missing pieces:

1. **`deploy/subnet-router/setup.sh`** — runs on the user's
   subnet-router host (RPi, NAS, mini-PC), takes a preauth
   key from the admin, and runs `tailscale up` with the
   correct flags. Sanity-checks tailscale CLI / tailscaled
   state first, then prints the next steps (admin must
   wait ~30s for auto-approval).
2. **`docs/subnet-router.md`** — full operator/user guide
   in plain language: when you need a subnet-router, the
   5-step setup, troubleshooting, security notes.

A third file ships as a one-off: **`deploy/subnet-router/
allocate-existing-users.sh`** — allocates per-user subnets
for users that were created before the v0.20.0 auto-allocate
feature (or for whom auto-allocate didn't fire). Already
exercised on the production VM: michail → `10.0.6.0/24`,
guest → `10.0.9.0/24`, daniil → `10.0.10.0/24`. Skyadmin
already had `10.0.1.0/24 active` from the v0.16.6 pilot.

## What did NOT change

- **No Go code touched.** The sidecar package is unchanged.
  The `PostAdminUserSubnetAllocate` / `…Provision` / `…Test`
  handlers and the `/admin/users/{id}/subnet` UI are
  unchanged.
- **No new env vars**, no schema migration, no new i18n
  keys, no `/admin/*` pages.
- **No breaking changes.** Existing preauth keys, route
  approvals, ACL rules, mesh memberships all work exactly
  as before.
- **Same defaults.** 256 users max in the /16 (one /24 per
  user), status semantics unchanged (pending ⇔ 0 devices
  + no router, active ⇔ ≥1 device, router_active ⇔ + a
  tag:subnet-router is up).

## What users will see

After this release, when a user is allocated a per-user
subnet, the admin can hand them:

> 1. SSH to the host that will be your subnet-router.
> 2. Run:
>
>    ```
>    PREAUTH_KEY=tskey-auth-XXXXX \
>    SUBNET_ROUTER_HOSTNAME=skygate-subnet-<username> \
>    SUBNET_CIDR=10.0.<uid>.0/24 \
>    sudo -E ./deploy/subnet-router/setup.sh
>    ```
>
> 3. Within ~30s the status pill on
>    `/admin/users/<id>/subnet` flips from `pending` to
>    `router_active`.
> 4. From any tailnet client: `ping skygate-subnet-<username>`.

This is a substantial UX improvement over "here's a
`tailscale up` command, figure out the rest".

## End-to-end flow (now)

1. Admin opens `/admin/users/<id>/subnet` (auto-allocated
   for new users since v0.20.0; for existing users, run
   `deploy/subnet-router/allocate-existing-users.sh`).
2. Admin clicks "Issue preauth key" → gets a single-use
   1h-TTL key + the rendered `tailscale up` command.
3. User (or admin on the user's behalf) runs the command
   on the subnet-router host. They can either paste it
   directly, or use `deploy/subnet-router/setup.sh` with
   the same env vars.
4. `sidecar.SyncOnce` (30s tick) sees the new
   `tag:subnet-router` node, parses the username from the
   hostname, auto-approves the per-user CIDR, and flips
   `user_subnets.status` to `active` (then
   `subnet.SyncStatus` flips it to `router_active` on the
   next `/my/devices` load).
5. ACL re-apply: the per-user rule already covers
   `tag:subnet-router` in `tagOwners` (since v0.17.0), so
   no policy churn is needed.
6. Tailnet clients with `tailscale up --accept-routes` see
   the new route within ~60s (the route push interval).
7. From any client: `ping skygate-subnet-<username>` works
   via MagicDNS; `ping 10.0.<uid>.1` works to the gateway
   IP on the user's LAN.

## Files

- `deploy/subnet-router/setup.sh` (177 lines) — the script.
  `chmod +x` in git.
- `deploy/subnet-router/allocate-existing-users.sh` (109
  lines) — one-off for backfilling existing users.
  `chmod +x` in git.
- `docs/subnet-router.md` (320 lines) — the operator guide.

## Production state after this release

```
$ sqlite3 /data/skygate.db \
  "SELECT id, username, subnet_cidr, subnet_status FROM portal_users"
1|skyadmin|10.0.1.0/24|active
6|michail|10.0.6.0/24|pending
9|guest|10.0.9.0/24|pending
10|daniil|10.0.10.0/24|pending
```

3 new subnets allocated, all `pending` (no live
subnet-router registered yet). When the corresponding
users run `setup.sh` on their routers, the sidecar flips
them to `router_active` within ~30s.

## What comes next

- Per-user bot routing (v0.12.1 follow-up) — small
  backfill, ~30 lines in `internal/telegram/notify.go`.
  Carried over from the v0.12 backlog.
- v0.19.1 (`exitnode.skygate-subnet-<user>` DNS record) —
  still blocked on headscale 0.30+ for
  `dns.extra_records` support. mavis cron
  `headscale-milestone-16-check` watches headscale
  milestone #16 (DNS Work) weekly.
- butler voice v4 (deferred since v0.15.5, no user demand
  yet).
- Caddy ↔ openresty documentation (the operator's
  deployment runs openresty, not Caddy as AGENTS.md used
  to say).
