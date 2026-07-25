# Skygate v0.28.2 — `hosts:` block workaround for headscale 0.29.2

Released 2026-07-25. Live on the operator's VM (192.0.2.1).
Tag: `v0.28.2`. Commit: `459a37c`.

## TL;DR

The v0.28.1 release shipped `GenerateACLWithViaForPlane` but
**headscale 0.29.2's policy v2 parser rejects any grant with
a CIDR+port in `dst`** — the parser runs `parseAlias()` on
the whole string (`10.0.1.0/24:*` → fail; isUser/isGroup/
isTag/isAutoGroup → fail; isHost → no such host → fail).
The v0.28.2 release ships the workaround: every CIDR
referenced by a grant is now emitted as a `host` alias in
the `hosts:` block, and the grant's `dst` references the
bare alias (no `:*` suffix). `ip: ["*"]` already covers any
port. The result: `via: ["<user's preferred exit-node>"]`
is enforced on production headscale 0.29.2.

**The v0.28.1 deploy defaults to `SKYGATE_ACL_VIA_ENABLED=false`,
which means the legacy `acls[]` policy is what reaches
headscale. The v0.28.2 deploy flips this to `true` and
verifies that every ACL write path uses the new grants[]
builder (so /my/devices backfill, bot /add_rule, etc. all
honor the env var, not just the explicit re-apply path).**

## What changed

### 1. `GenerateACLWithViaForPlane` — pre-collect every host alias

A two-pass layout:

**Pass 1** — collect every (name, cidr) pair we need into
a `[]hostEntry` slice via the `addHost` closure:
- Per-user subnets: `h-user-<uname>-subnet` → `10.0.<uid>.0/24`
- Shared (v0.17.1) + mesh (v0.22.0) CIDRs:
  `h-shared-<sanitized-cidr>` → CIDR
- Per-device rule targets (Telegram CIDRs, custom IPs):
  `h-rule-<sanitized-target>` → target

The sanitization replaces `.`, `/`, and `:` (IPv6) with
`-` / `_` so the result is a valid hostname — headscale's
hosts parser validates alias names as hostnames and rejects
`:` in particular (the v0.28.2 first attempt used `h:`
prefix and was rejected with HTTP 500 "invalid hostname").

**Pass 2** — emit the policy. The `hosts:` block is written
first (with every alias from pass 1), then the `grants:`
block references the bare alias (no `:*`) in each `dst`.

### 2. Catch-all grants — drop the `:*`

Three catch-all rules in the grants block also had `:*`
in their dst (`tag:public:*`, `tag:exit-node:*`,
`autogroup:internet`). headscale 0.29.2 rejects those
too. The v0.28.2 fix: drop the `:*` — `ip: ["*"]` already
means "any port".

### 3. `ApplyACLPipelineForPlane` — read env var at call time

The v0.28.1 release added a `useVia bool` parameter to
`ApplyACLPipeline` and `ApplyACLPipelineForPlane`. Most
call sites passed `false` (the legacy default), so any
re-apply path that didn't explicitly opt in (a /my/devices
backfill, a bot /add_rule, a /admin/exit-rules/reapply
without the env var) would silently write the legacy
`acls[]` policy and **discard the grants[] + via** the
operator just enabled.

The v0.28.2 fix: when `useVia` is `false`, treat it as
"unset" and read `SKYGATE_ACL_VIA_ENABLED` from the env.
The contract:
- `useVia=true` (caller knows the call needs grants[]):
  honored as-is, env var ignored.
- `useVia=false` (legacy default, most callers): if
  `SKYGATE_ACL_VIA_ENABLED=true`, use grants[]; else
  use acls[].

This makes the env var a global toggle. The operator
flips it once, and every ACL write path picks up the
right builder.

### 4. Deploy: pre-re-apply required

In production, the v0.28.2 deploy was:
1. Deploy v0.28.2 binary (this commit)
2. Re-apply policy (`POST /admin/exit-rules/reapply`) —
   writes grants[] + via for the first time
3. The re-apply added `tag:exit-relay-1` /
   `tag:exit-relay-2` / `tag:exit-relay-3` to
   `tagOwners` (because the per-exit-node teardown
   creates a row in `user_exit_node_prefs` for each
   user)
4. `headscale nodes tag -i <id> -t
   "tag:exit-node,tag:exit-<hostname>,tag:public" --force`
   on relay-1 / relay-2 / relay-3 — now accepted
   because `tagOwners` already references the new
   per-exit-node tags

The operator-side flow is documented in the v0.28.1
release notes (the UI for setting per-exit-node
preferences was already shipped in v0.28.1).

### 5. Tests

Three new tests pin the v0.28.2 invariants:
- `TestGenerateACLWithVia_EmitsHostsBlock` — the
  `hosts:` block is present and contains the per-user
  subnet alias
- `TestGenerateACLWithVia_GrantsReferenceHostAliases` —
  per-user grant's dst uses the bare alias (no `:*`),
  and the raw CIDR+port form is forbidden
- `TestGenerateACLWithVia_HostsBlockIsRequiredEvenWhenEmpty`
  — the `_placeholder` entry is always present

All 18 Go packages green. 7/7 v0.28.x ACL tests PASS
(4 v0.28.0 + 4 v0.28.1 + 3 v0.28.2). i18n parity green.
Bilingual smoke green: 83/83 EN + 83/83 RU.

## Live verification (this release)

- `version=v0.28.1-6-g459a37c` (built 2026-07-25T06:19:44Z)
- `/healthz=ok`, `/readyz=ok` (db=ok, headscale=ok)
- `headscale policy get`:
  - `grants: 249` (no more acls[])
  - `grants with via: 3`
    - `admin@...` → `via: ["tag:exit-relay-1"]`
    - `user1@...` → `via: ["tag:exit-relay-2"]`
    - `user2@...` → `via: ["tag:exit-relay-3"]`
  - `tagOwners`:
    - `tag:exit-relay-1: ['admin@...']`
    - `tag:exit-relay-2: ['admin@...']`
    - `tag:exit-relay-3: ['admin@...']`
  - `hosts: 212` (4 per-user + 208 per-device rule targets)
- `headscale nodes list -o json`:
  - `relay-1 (id=3): tags=['tag:exit-node', 'tag:exit-relay-1', 'tag:public', 'tag:dev-admin-relay-1']`
  - `relay-2 (id=4): tags=['tag:exit-node', 'tag:exit-relay-2', 'tag:public', 'tag:dev-admin-relay-2']`
  - `relay-3 (id=11): tags=['tag:exit-node', 'tag:exit-relay-3', 'tag:public', 'tag:dev-admin-relay-3']`
- `make test` (EN) → 83 pass, 0 fail
- All 21 admin/user pages return 200

## How to verify on a live deployment

```bash
# 1. Build is on v0.28.2
docker logs skygate | grep version= | head -1
# expect: v0.28.1-6-g459a37c

# 2. Policy is grants[] (not acls[])
docker exec headscale headscale policy get 2>/dev/null | \
  python3 -c 'import json,sys; d=json.load(sys.stdin); \
  print("acls:", len(d.get("acls",[]))); \
  print("grants:", len(d.get("grants",[]))); \
  print("grants with via:", sum(1 for g in d.get("grants",[]) if "via" in g))'
# expect: acls=0  grants=249  grants with via=3

# 3. TagOwners has per-exit-node tags
docker exec headscale headscale policy get 2>/dev/null | \
  python3 -c 'import json,sys; d=json.load(sys.stdin); \
  print([k for k in d.get("tagOwners",{}).keys() if "exit-" in k])'
# expect: ['tag:exit-node', 'tag:exit-relay-1', 'tag:exit-relay-3', 'tag:exit-relay-2']

# 4. relay-1 / relay-2 / relay-3 have per-exit-node tags applied
docker exec headscale headscale nodes list -o json | \
  python3 -c 'import json,sys; data=json.load(sys.stdin); \
  [print(n["givenName"], n["tags"]) for n in data if n.get("givenName") in ("relay-1","relay-2","relay-3")]'
# expect: each shows tag:exit-<hostname> in tags

# 5. Per-user preference is in DB
docker cp skygate:/data/skygate.db /tmp/check.db
sqlite3 /tmp/check.db "SELECT username, exit_node_tag FROM user_exit_node_prefs"
# expect: 3 rows (admin/user1/user2)
```

## Known limitations

- **No per-device `via`** — the v0.28.2 release has per-user
  preferred exit-nodes only. A future v0.29.x can extend
  `user_exit_node_prefs` to `(user_id, device_id) UNIQUE`
  keyed on the per-device tag the v0.28.0 backfill already
  provides. Per-device `via` is one extra line in the
  grants loop.
- **No IPv6 `via` test coverage** — the production
  deployment only uses IPv4 exit-nodes. The `via` field
  works with any tag, so IPv6 exit-nodes should work
  out of the box, but there's no end-to-end test.
- **No runbook for the `via` reapply / AddTag dance** —
  the v0.28.2 release is the second half of the v0.28.1
  rollout. The first half (UI + data model) shipped
  in v0.28.1. A `deploy/scripts/add_per_exit_node_tags.sh`
  helper is planned for v0.28.3.

## What's next

- **v0.28.3**: cleanup the 21+ legacy `device_ip` rules
  that pre-date v0.28.0, plus the
  `add_per_exit_node_tags.sh` deploy helper.
- **v0.29.0**: Provisioning UI (DERP / storage config)
  per the v0.28.0 plan document.
- **v0.30.0** (when headscale 0.30+ lands): re-evaluate
  whether the `hosts:` workaround is still needed. If
  the v2 parser's `parseAlias` is fixed to split
  alias:port, we can drop the `hosts:` block and emit
  the raw CIDR+port in `dst` again. Until then, the
  v0.28.2 workaround is the canonical headscale 0.29.x
  shape.
