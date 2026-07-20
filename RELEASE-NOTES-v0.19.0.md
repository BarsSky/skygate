# v0.19.0 — `exitnode.skygate-subnet-<user>` DNS record

2026-07-20

The v0.16.0+ per-user subnets roadmap's last big
feature. Each portal user with a personal subnet can
now pick a "preferred exit-node" — Skygate then
publishes a special DNS record

  exitnode.skygate-subnet-<username>.tsnet.skynas.ru

pointing to that exit-node's Tailscale IP. The user's
tailnet clients (laptop, phone, server) can use the
FQDN as their default route without remembering the
exit-node's tailnet IP. Reachable cross-subnet because
`tag:exit-node` is in every user's per-user ACL
(v0.17.0) — the IP is a relay tailnet IP (e.g.
100.64.0.2) and the user's ACL permits access to it
via the global exit-node rule.

v0.18.0 deferred this because the headscale docs said
"headscale 0.29 doesn't support per-user service
records". That's true for the headscale "services"
feature (TCP/UDP service publication), but
`dns.extra_records` works on headscale 0.20+ and is
what v0.19.0 uses. The full port-forwarding
per-user services idea (v0.19.0 in the original
roadmap) is a v0.19.1+ follow-up — headscale 0.23+ has
real service records, but those don't reach headscale
0.29 (the operator's version). v0.19.0-lite is the
DNS-only version that works on the operator's stack.

## What changed

### 1. Schema (migrations_v0.40.go)

  - New column on `user_subnets`:
    `preferred_exit_node_id TEXT NOT NULL DEFAULT ''`
  - Partial index
    `WHERE preferred_exit_node_id != ''` for the ACL
    builder's "give me every user with a choice"
    scan (avoids a full table scan at 1000+ users).
  - Idempotent: `ALTER TABLE ADD COLUMN` ignores
    "duplicate column" on re-run, same as v0.16.8
    / v0.16.6 / v0.37.0.

### 2. `internal/subnet/exit_node_choice.go` (new)

  - `GetPreferredExitNode(d, userID) → (string, error)`
  - `SetPreferredExitNode(d, userID, nodeID) → error`
    (idempotent, returns `ErrNotFound` if user has
    no subnet; refuses `""` — use Clear to unset)
  - `ClearPreferredExitNode(d, userID) → error`
  - `ListUsersWithPreferredExitNode(d) → []Choice`
    (skips users with no preference — the ACL
    builder iterates this)
  - 9 unit tests in `exit_node_choice_test.go`
    (roundtrip, overwrite, empty rejection,
    `ErrNotFound` on no-subnet, list filter on
    `WHERE != ''`, etc.)

### 3. ACL integration (`internal/acl/acl.go`)

  - `GenerateACLForPlane` and `GenerateACL` now take
    a `*headscale.Client` argument (can be nil —
    tests / dry-runs skip the dns section).
  - New `buildExtraRecords(d, hs, baseDomain)`
    helper: for every user with a choice, looks
    up the node's Tailscale IPs (A for IPv4, AAAA
    for IPv6) and emits one `dns.extra_records`
    entry. If the node ID doesn't exist in
    headscale (got deleted), the record is
    silently skipped (other users' records still
    publish).
  - The new section is appended only when there's
    at least one record; headscale treats the
    `dns` field as optional. Empty choice list
    → no `dns` section, no JSON noise.
  - 5 new ACL tests in `extra_records_test.go`
    (A record, A+AAAA for IPv6, no records when
    no choice, skip unknown node, nil hs).

### 4. Admin UI

  - New "Preferred exit-node" card on
    `/admin/users/{id}/subnet` (only shown when
    the user has a subnet). Drop-down with the
    operator's configured exit-nodes
    (from `/admin/exit-nodes`); "Set" button
    applies; "Clear" button removes the choice.
  - Live FQDN preview: when a choice is set, the
    card shows `exitnode.skygate-subnet-<user>.tsnet.skynas.ru`
    so the operator can verify the eventual
    record.
  - 9 new i18n keys (RU+EN):
    `preferred_exit_node_title`,
    `preferred_exit_node_help`,
    `preferred_exit_node_label`,
    `preferred_exit_node_none`,
    `preferred_exit_node_set`,
    `preferred_exit_node_clear`,
    `preferred_exit_node_clear_help`,
    `preferred_exit_node_active`,
    `preferred_exit_node_no_choices`.
  - 2 new routes:
    `POST /admin/users/{id}/subnet/set-exit-node`
    and `POST /admin/users/{id}/subnet/clear-exit-node`.

### 5. Bot

  - `/mysubnet exit-node` — show current choice +
    the list of available exit-nodes (with IPs
    so the user knows what they're picking).
  - `/mysubnet exit-node set <name>` — pick an
    exit-node by hostname (case-insensitive,
    matches `GivenName` and `Hostname`).
  - `/mysubnet exit-node clear` — drop the
    choice.
  - The ACL is re-pushed after every state change
    (best-effort, same pattern as v0.17.1
    share/revoke — the record is in the DB
    immediately, the push is best-effort).
  - 11 new i18n keys (RU+EN):
    `exit_node_usage`, `exit_node_set_usage`,
    `exit_node_no_choices`,
    `exit_node_no_choice_yet`,
    `exit_node_current`, `exit_node_available`,
    `exit_node_list_error`,
    `exit_node_not_found`,
    `exit_node_set_error`, `exit_node_set_ok`,
    `exit_node_no_choice`,
    `exit_node_clear_error`,
    `exit_node_clear_ok`.

## Architecture note

The DNS record is published as a headscale
`dns.extra_records` entry — NOT as a per-user Tailscale
service record. headscale 0.29 (the operator's
version) supports `dns.extra_records` natively
(since 0.20); per-user service records need
headscale 0.23+ with a feature flag that's not yet
on 0.29. `dns.extra_records` is the right primitive
for the operator's stack today and works without any
headscale config beyond what's already in
`headscale-config.yaml`.

Access control: the `* → tag:exit-node:*` rule
(already in the policy since v0.12.0.1) permits
every user to reach any node tagged `tag:exit-node`.
The `exitnode.skygate-subnet-<user>` FQDN resolves
to the chosen exit-node's Tailscale IP (e.g.
100.64.0.2), which is in the relay's tailnet, which
is tagged `tag:exit-node`. So the user's tailnet
client can:

  1. DNS-resolve `exitnode.skygate-subnet-michail.tsnet.skynas.ru`
  2. Get 100.64.0.2 (the exit-node's tailnet IP)
  3. Use it as the default route
  4. ACL permits the traffic (100.64.0.2 is
     tag:exit-node, * → tag:exit-node:* allows it)

No new ACL rules needed.

## Live verification

  - `make test` — 12/12 packages green, smoke
    118/118 (en 59 + ru 59, both 0 fail),
    check_exit_nodes PASS, check_https PASS.
  - `/admin/users/1/subnet` (skyadmin) — new
    "Preferred exit-node" card with the 3
    configured exit-nodes (emilia, sharlotta,
    karolina) in the drop-down.
  - `headscale policy get` — when a user has a
    choice, the policy includes:
    ```
    "dns": {
      "extra_records": [
        {"name": "exitnode.skygate-subnet-michail.tsnet.skynas.ru", "type": "A", "value": "100.64.0.2"}
      ]
    }
    ```
  - `tailscale dns` from any tailnet client —
    `exitnode.skygate-subnet-michail.tsnet.skynas.ru`
    resolves to the chosen exit-node's IP.

12/12 packages green, smoke 118/118, live at the
v0.19.0 build.

## Open backlog after v0.19.0

  - Butler voice v4 (per-reply inline color marks
    for status) — needs operator feedback on v3.
  - Auto-tag exit-nodes background goroutine
    (opt-in via `SKYGATE_AUTO_TAG_EXIT_NODES=true`)
    — partial via the v0.18.1 button; full
    auto-tag is a v0.19.1+ follow-up.
  - Per-user port-forwarding services (the
    original v0.19.0 roadmap item, deferred to
    v0.19.1+ when headscale's per-user service
    records feature is usable).
