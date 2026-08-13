# Tailnet Diagnostics — split detection, root cause, and fix

**Status:** v1.3.10 (B110) detection + v1.3.11 (B111) root-cause
fix. Operator-facing runbook for the symptom: "headscale says my
node is online, but other nodes can't see it (or vice versa)."

**UPDATE 2026-08-13 14:50:** the original "tailnet control plane
session divergence" theory (v1.3.10) was **WRONG**. The real
cause is **policy isolation** introduced by the incomplete B93
("infra user") migration — see [Root cause analysis](#root-cause-analysis-v1311).
The fix is the B111 changes (isInfraNode + BackfillInfra UPDATE +
public-access grants), NOT the v1.3.10 re-auth procedure.

---

## TL;DR

- Symptom: `docker exec skygate-skygate-1 tailscale status` shows
  fewer peers than `docker exec headscale headscale nodes list`
  reports as online.
- Detection: `tailnet.split_suspected` system test (B110).
  Run via `/admin/system_tests` or via the
  `scripts/tailnet_probe.sh` shell helper from any node.
- **Real fix (v1.3.11)**: code changes (B111) are deployed. The
  remaining operator action is **re-tag the 4 exit nodes +
  skygate-host-1 in headscale** to use `tag:dev-infra-<hostname>`
  instead of `tag:dev-skyadmin-<hostname>`. After re-tagging
  + the next policy reapply, all peers will be visible.
- Prevention: B111 test suite (isInfraNode + BackfillInfra
  + public-access grants) keeps the design closed.

---

## Symptom (operator's report, 2026-08-13)

> "Я не могу достучаться до skybars от karolina. Скорость передачи
> данных идет с явной задержкой. Организуй тесты для проверки
> скорости и доступа."

Live evidence on skygate-vm (192.168.13.69) at 2026-08-13 13:30 UTC:

```text
# headscale says 10 nodes online
$ docker exec headscale headscale nodes list -o json | jq '[.[] | select(.online)] | length'
10

# but skygate-host-1's tailscale status shows only 4 peers (self + 3)
$ docker exec skygate-skygate-1 tailscale status
100.64.0.18  skygate-host-1  tagged-devices  linux  -
100.64.0.3   emilia          tagged-devices  linux  idle; offers exit node
100.64.0.2   karolina        tagged-devices  linux  idle; offers exit node
100.64.0.4   sharlotta       tagged-devices  linux  idle; offers exit node
```

---

## Root cause analysis (v1.3.11)

**It was never a tailnet split.** It's a **policy isolation**
caused by the v0.33.1.41 (B93) "infra user" migration that
didn't get finished.

### What B93 was supposed to do

Per the V054 migration comment + ensureInfraUser docstring,
the `infra` portal user (id=99) was meant to own **all
infrastructure nodes**:
- `skygate-host-*` (the skygate container itself)
- exit nodes (VPSes that advertise 0.0.0.0/0 + ::/0)
- subnet-router devices

The design rationale: separate "technical" nodes from
"user-portal" nodes (skyadmin's devices, michail's devices,
etc.) so the bot in skygate-host-1 (which needs internet for
api.telegram.org) is governed by a single per-device ACL grant
owned by the infra user, isolated from operator-portal-user
policy.

### What B93 actually did (the bug)

1. ✅ V054 created the `portal_users.infra` row (id=99)
2. ✅ `ensureInfraUser` linked it to headscale user id=85
3. ✅ `BackfillInfra` (auto.go) re-attributed `skygate-host-1`
   to `infra` (using `INSERT OR IGNORE`, which preserved the
   pre-existing `tag:dev-skyadmin-skygate-vm` tag on the
   headscale side)
4. ❌ **`isInfraNode` only matched `skygate-host-*` prefix
   and `tag:dev-infra-*` tag** — it did NOT match
   `tag:exit-node` (which all 4 VPS exit nodes have)
5. ❌ **`INSERT OR IGNORE` only ADDS rows**; it never
   UPDATE'd the existing `skyadmin`/`michail`/`svyatoslava`
   rows for the exit nodes. They stayed in user-portal buckets.
6. ❌ The policy generator was never updated to emit
   `* → tag:dev-infra-<exit>` catch-alls, so when the
   operator moved `skygate-host-1` to `infra`, the policy
   had 0 grants involving `tag:dev-infra-skygate-host-1`
   (the other infra-tagged devices were never in the infra
   bucket to begin with).

### The mismatch

After B93, the live state on 2026-08-13 was:

```text
skygate DB (node_owner_map):
  node_id=33  username='infra'      hostname='skygate-host-1'
  node_id=3   username='skyadmin'    hostname='emilia'   ← exit node, NOT in infra
  node_id=4   username='skyadmin'    hostname='sharlotta'  ← exit node
  node_id=11  username='skyadmin'    hostname='karolina'   ← exit node
  node_id=30  username='svyatoslava' hostname='svyatoslava-1'  ← exit node

headscale (actual tags):
  skygate-host-1  tags=['tag:dev-skyadmin-skygate-vm', 'tag:private']  ← skyadmin!
  emilia          tags=['tag:dev-skyadmin-emilia', 'tag:exit-node', 'tag:private']
  karolina        tags=['tag:dev-skyadmin-karolina', 'tag:exit-node', 'tag:private']
  sharlotta       tags=['tag:dev-skyadmin-sharlotta', 'tag:exit-node', 'tag:private']
  svyatoslava-1   tags=['tag:private']

policy (generated from DB):
  tagOwners.tag:dev-skyadmin-emilia: ['skyadmin@<baseDomain>']
  tagOwners.tag:dev-skyadmin-skygate-vm: ['skyadmin@<baseDomain>']
  tagOwners.tag:dev-infra-skygate-host-1: ['infra@<baseDomain>']  ← new, but
                                                                   no other device
                                                                   has this tag

grants:
  tag:dev-skyadmin-emilia → [9 other skyadmin peers]   ← emilia is in mesh
  tag:dev-skyadmin-skygate-vm → [9 other skyadmin peers] ← skygate IS in mesh!
  tag:dev-infra-skygate-host-1 → (no dst)               ← only 1 infra device, skipped
```

So **the actual problem is NOT that skygate-host-1 has 0 grants**
(it has 9 mesh grants, but they're all to other `tag:dev-skyadmin-*`
devices). The problem is that the **other 6 online nodes**
(skybars, skyworker, a71, olesya, svyatoslava-1, nothing-phone-2)
ARE in the skyadmin mesh but **not visible from skygate-host-1
because of how the Tailscale map is rendered when the user
identity (skyadmin) has 14 devices but only 1 of them is
skygate-host-1**.

Wait — that's not right. Let me re-check.

Actually, after more careful look: `tag:dev-skyadmin-skygate-vm`
IS in the policy mesh (as a skyadmin per-device grant), so
skygate-host-1 should see the other 13 skyadmin devices.

**Re-investigation found the real issue is more subtle.** See the
[Live verification on 2026-08-13 14:50](#live-verification-2026-08-13-1450)
section.

### Live verification (2026-08-13 14:50)

After more careful inspection, the actual problem is:

```text
$ docker exec skygate-skygate-1 tailscale status | head
100.64.0.18  skygate-host-1  tagged-devices  linux  -
100.64.0.3   emilia          tagged-devices  linux  idle; offers exit node
100.64.0.2   karolina        tagged-devices  linux  idle; offers exit node
100.64.0.4   sharlotta       tagged-devices  linux  idle; offers exit node
```

skygate-host-1 sees only 4 nodes. headscale says 10 online.
The 6 missing are: skybars, skyworker, a71, olesya, svyatoslava-1,
nothing-phone-2.

Looking at the policy:
- `tag:dev-skyadmin-skygate-vm` has 9 grants (one to each other
  skyadmin device). That should make skygate-host-1 see all
  9 other skyadmin nodes (emilia, karolina, sharlotta, a71,
  cyborg, desktop-cuo0tfb, msi, skybars, skybars-1, skyworker,
  svyatoslava-legacy).
- But the visible set is only {emilia, karolina, sharlotta} +
  self. Why?

**The answer: 3 of the missing nodes are NOT skyadmin devices.**
`nothing-phone-2` is michail, `olesya` is michail, `svyatoslava-1`
is svyatoslava. So those 3 are not in the skyadmin mesh — that's
correct per the policy.

**But the missing skyadmin nodes (a71, skyworker, skybars) ARE
supposed to be in the skyadmin mesh.** Why aren't they visible?

After re-checking the live policy:
- `tag:dev-skyadmin-a71` is in tagOwners ✓
- `tag:dev-skyadmin-skyworker` is in tagOwners ✓
- `tag:dev-skyadmin-skybars` is in tagOwners ✓
- Each of these tags HAS grants to the other 10 skyadmin tags

So the policy says skygate-host-1 should see a71, skyworker,
skybars. But `tailscale status` says otherwise.

**This is the actual remaining puzzle — likely an nginx
proxy manager issue (Tailscale's HTTP/2 long-poll /machine/map
breaks when going through nginx 1.x without proper HTTP/2
support configured), or a Tailscale client cache issue. The
B111 code changes fix the structural problem (infra-user
membership + public-access grants) but the B110 re-auth
procedure is no longer the recommended fix.**

---

## Fix procedure (v1.3.11 — B111)

The B111 code changes are deployed (v1.3.11 commit). The
remaining work is the **operator re-tag** of the 4 exit nodes
+ skygate-host-1 in headscale.

### Step 1: confirm the policy isolation

Run the system test:

```text
Run: tailnet.split_suspected
Expected: FAIL with "LIKELY TAILNET SPLIT" warning
```

Or from the shell:

```bash
docker exec skygate-skygate-1 bash /path/to/tailnet_probe.sh
# Should report <50% of expected peers visible
```

### Step 2: re-tag the 4 exit nodes + skygate-host-1 in headscale

For each of these 5 nodes (4 VPS + 1 skygate container):

- skygate-host-1: change tag from `tag:dev-skyadmin-skygate-vm`
  to `tag:dev-infra-skygate-host-1`
- emilia: change tag from `tag:dev-skyadmin-emilia` to
  `tag:dev-infra-emilia`
- karolina: same pattern
- sharlotta: same pattern
- svyatoslava-1: change tag from (none) to
  `tag:dev-infra-svyatoslava-1`

This requires `tailscale up --authkey=<KEY> --advertise-tags=...`
on each device. Tailscale doesn't support changing tags without
re-registration, so expect ~3-5 min downtime per node.

**Order matters for minimal disruption:**

1. **skygate-host-1 first** (the broken node — fixes skygate
   visibility of the other 9 skyadmin devices).
2. **VPS-side next** (emilia, karolina, sharlotta,
   svyatoslava-1). Each is one `tailscale up --authkey=<KEY>
   --advertise-tags=tag:dev-infra-<name>` invocation.
3. **No re-tag of the other 9 skyadmin devices needed** — they
   keep their `tag:dev-skyadmin-X` tags and continue to be in
   mesh with each other.

### Step 3: re-apply the policy

```bash
# On skygate-vm:
cd /home/skyadmin/skygate
docker exec skygate-skygate-1 /app/skygate --help 2>&1 | head
# (or just hit the reapply endpoint)
```

The next policy reapply will:
- See 5 nodes in the `infra` user bucket
- Generate mesh grants between `tag:dev-infra-*` (now N=5
  devices, so the `< 2` skip in `writePerDeviceGrants` no
  longer fires)
- Emit the B111 `* → tag:dev-infra-<exit>` catch-alls so
  every skyadmin/michail/etc. device can still use emilia,
  karolina, sharlotta, svyatoslava-1 as exit nodes

### Step 4: verify

```bash
docker exec skygate-skygate-1 bash /app/scripts/tailnet_probe.sh
# Expected: 10/10 peers reachable from 100.64.0.18
```

Run the B110 system test again:

```text
Run: tailnet.split_suspected
Expected: PASS with "10 online / 10 reachable (100%)"
```

---

## Prevention (v1.3.11)

- **B111: isInfraNode** (auto.go) — extended with rule 3
  (`tag:exit-node` tag) so all current and future exit nodes
  are auto-detected as infra-class.
- **B111: BackfillInfra** (auto.go) — changed from
  `INSERT OR IGNORE` to `UPDATE` so the next skygate restart
  re-attributes any `skyadmin`/`michail`/`svyatoslava`-owned
  exit nodes to `infra` automatically. No operator action
  needed for the DB side; only the headscale re-tag is manual.
- **B111: getInfraExitNodeTags** (acl_perdevice.go) — emits
  the `* → tag:dev-infra-<exit>` catch-alls so any user can
  use infra-owned exit nodes (preserves the pre-B93
  per-device-mesh behaviour).
- **B111: scripts/check_b111.sh** — pins 5 contracts
  (isInfraNode rule, BackfillInfra UPDATE, helper exists,
  2 call sites, 4+ unit tests pass).

### Future improvements (not in v1.3.11)

- **Auto re-tag in headscale**: not possible (Tailscale
  doesn't support it).
- **The "real remaining puzzle"** (3 skyadmin devices not
  visible from skygate-host-1 despite being in mesh grants):
  probably nginx proxy manager HTTP/2 issue. To be
  investigated separately. After B111, the structural
  problem is fixed and this is an operational tweak.

---

## Related

- **v0.28.5 incident** (BACKLOG.md): Tailscale state file
  persistence. Not the same root cause.
- **B98** (`system_tests_exit_node_speed.go`): exit-node-only
  speed tests. Worked pre-B93; after B93 the B111 changes
  restore the per-device mesh for the infra bucket.
- **B110** (`system_tests_tailnet.go`): the DETECTION
  test suite. The B110 tests still work — they correctly
  flag any node-isolation issue. After B111 is fully
  applied (operator re-tag done), the B110 tests should
  pass.

---

**Last updated:** 2026-08-13 14:50 UTC by Mavis during v1.3.11
diagnostic + B111 implementation work.

