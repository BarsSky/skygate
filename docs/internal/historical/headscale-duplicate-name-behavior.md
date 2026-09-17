# headscale 0.29.1 — duplicate-name behavior (and what happened with SkyBars)

**Generated:** 2026-08-25 (12:21Z)
**Operator question:** "как поведет себя headscale если устройство с такимже именем уже есть"
**Sources:** headscale 0.29.1 source (`commit 636f660c`, build 2026-06-18)
at `hscontrol/state/node_store.go:resolveGivenName` + `hscontrol/state/state.go:UpdateNodeFromMapRequest`

---

## TL;DR

headscale 0.29.1 has **two distinct paths** that handle duplicate
GivenName — and they behave **differently**:

1. **Client-driven registration** (the Tailscale client sends its
   Hostname): headscale **auto-renames** with `-1`, `-2`, etc.
   suffix. The first-collision-wins logic is in
   `resolveGivenName` (`node_store.go:365-388`).

2. **Admin-driven rename** (`headscale nodes rename` or via the
   headplane/gRPC API): headscale **rejects with `ErrGivenNameTaken`**
   (HTTP 409). The admin must delete the conflicting node first, or
   pick a different name. Logic in `setName` branch of
   `NodeStore.applyBatch` (`node_store.go:243-271`).

3. **Re-registering same node** (same NodeKey, same machine): the
   GivenName is **preserved** — only the Hostname is updated. The
   GivenName is only re-derived if it currently matches what
   `dnsname.SanitizeHostname` of the OLD Hostname would produce
   (possibly with a `-N` collision bump). Source comment in
   `UpdateNodeFromMapRequest`:
   > "Preserve an admin-renamed GivenName: only auto-derive when
   > the current GivenName is still what SanitizeHostname of the old
   > Hostname would produce (possibly with a "-N" collision bump)."

---

## The exact auto-rename logic (from headscale 0.29.1 source)

```go
// hscontrol/state/node_store.go:resolveGivenName (lines 365-388)
//
// resolveGivenName returns a unique DNS label for the node identified
// by self, based on the caller-supplied base label. If base is empty
// it falls back to [fallbackGivenName] ("node"). The label's own holder
// (self) is excluded from the collision scan so an idempotent write
// keeps the current label.
//
// On collision the label is bumped as base, base-1, base-2, …, first
// unused wins. Must be called from the [NodeStore] writer goroutine
// (inside [NodeStore.applyBatch]) so the nodes map reflects all earlier
// ops in the batch and no other writer can interleave.
func resolveGivenName(nodes map[types.NodeID]types.Node,
                      self types.NodeID, base string) string {
    if base == "" {
        base = fallbackGivenName  // "node"
    }

    taken := make(map[string]struct{}, len(nodes))
    for id, n := range nodes {
        if id == self {
            continue
        }
        taken[n.GivenName] = struct{}{}
    }

    candidate := base
    for i := 1; ; i++ {
        if _, busy := taken[candidate]; !busy {
            return candidate
        }
        candidate = base + "-" + strconv.Itoa(i)
    }
}
```

Called from:
- `applyBatch` `put` case: every new node registration
- `applyBatch` `updateMulti` case: every multi-node update that changes GivenName

NOT called from `setName` case — admin renames go through a different
path that checks `taken` and returns `ErrGivenNameTaken` if conflict.

```go
// hscontrol/state/node_store.go:setName case (lines 248-271)
case setName:
    n, exists := nodes[w.nodeID]
    if !exists { /* ErrNodeNotFound */ }

    if dnsname.ValidLabel(w.name) != nil { /* ErrGivenNameInvalid */ }

    taken := false
    for id, other := range nodes {
        if id != w.nodeID && other.GivenName == w.name {
            taken = true
            break
        }
    }
    if taken { /* ErrGivenNameTaken */ }

    n.GivenName = w.name
    nodes[w.nodeID] = n
```

---

## What this means for the SkyBars duplicate (live evidence)

Current state in headscale (from `/api/v1/node`):

| id | Hostname | GivenName | user.name | registerMethod | preauth_id | tags |
|----|----------|-----------|-----------|----------------|------------|------|
| 3  | emilia | emilia | tagged-devices | AUTH_KEY | 8 | tag:dev-infra-emilia,tag:exit-node,tag:private |
| 4  | sharlotta | sharlotta | tagged-devices | AUTH_KEY | 9 | tag:dev-infra-sharlotta,tag:exit-node,tag:private |
| 6  | nothing-phone-2 | nothing-phone-2 | tagged-devices | AUTH_KEY | 19 | tag:dev-michail-nothing-phone-2,tag:private |
| ... | ... | ... | ... | ... | ... | ... |
| 34 | **SkyBars** | **skybars** | tagged-devices | AUTH_KEY | 204 | tag:dev-skyadmin-skybars,tag:private |
| 35 | **SkyBars** | **skybars-1** | tagged-devices | **OIDC** | — | tag:dev-skyadmin-skybars,**tag:dev-skyadmin-skybars-1**,tag:private |

Two nodes with **identical OS hostname** "SkyBars" (Tailscale
preserves case for the Hostname field) but **different GivenNames**
"skybars" vs "skybars-1" (the canonical DNS label, always lowercase,
must be unique per tailnet).

**What happened step by step:**

1. **id=34** was the original SkyBars device, registered with a
   preauth key (id=204) sometime in the past. Hostname sent by
   client: "SkyBars" (mixed case). `dnsname.SanitizeHostname`
   lowercased it to "skybars". No collision → GivenName = "skybars".

2. **id=35** is the B174 OIDC test result. The user's Tailscale
   client re-registered the same device (or a different device with
   the same hostname) via OIDC. Hostname sent: "SkyBars" again.
   `resolveGivenName` saw "skybars" was already taken (by id=34) and
   auto-bumped to "skybars-1".

3. The Tailscale CLIENT didn't get told about the rename. It still
   thinks its name is "skybars" (or the OS thinks its hostname is
   "SkyBars"), but headscale stored it as "skybars-1". The actual
   network address (100.64.0.18 + fd7a:115c:a1e0::13) is what
   matters for routing — the GivenName only affects the DNS label.

4. **Why does skygate's /my/devices show "⏳ pending"?** The
   `node_owner_map` row in skygate's DB has hostname "skybars-1"
   (which matches id=35's GivenName), but the B175 Strategy E
   autoupdater doesn't match id=35 because the user.name is
   "tagged-devices" (synthetic) not "skyadmin". So the dev-tag
   backfill can't find the row to update. The dev-tag WAS applied
   manually in B176 verification (now showing in the tags list
   above), but the UI says "pending" because the node_owner_map row
   wasn't updated.

---

## What headscale does in each duplicate-name scenario

### Scenario 1: New node with already-taken GivenName (client-driven)

```
Client A: hostname=foo, NodeKey=keyA
  → headscale stores id=1, Hostname=foo, GivenName=foo

Client B: hostname=foo, NodeKey=keyB (DIFFERENT key)
  → headscale calls resolveGivenName(nodes, self=2, base="foo")
  → "foo" is taken by id=1, tries "foo-1"
  → "foo-1" is free, returns "foo-1"
  → headscale stores id=2, Hostname=foo, GivenName=foo-1
```

**Result:** Both nodes registered. id=1 has GivenName "foo",
id=2 has GivenName "foo-1". Both have Hostname "foo" (or whatever
case the client sent). No error to client.

### Scenario 2: Admin renames a node to an already-taken GivenName

```
$ headscale nodes rename 2 foo
Error: given name already in use by another node
```

**Result:** Rejected. The admin must:
- `headscale nodes delete 1` first, then `headscale nodes rename 2 foo`
- Or pick a different name: `headscale nodes rename 2 bar`
- Or rename to the existing auto-bumped name: `headscale nodes rename 2 foo-1` (already taken by id=2 itself, so this would be a no-op anyway)

### Scenario 3: Same node re-registers (same NodeKey)

```
Client A re-registers: hostname=foo, NodeKey=keyA (SAME key)
  → UpdateNodeFromMapRequest: existingNodeSameUser matches by NodeKey
  → Hostname updated to "foo" (no change)
  → GivenName preserved: "foo" (unchanged)
  → If client changes hostname: "bar" → resolveGivenName is called for
    "bar", no conflict, GivenName = "bar"
  → If admin had renamed to "bar" previously: isAutoDerivedGivenName
    returns false (the current GivenName doesn't match what would be
    auto-derived), so GivenName STAYS as "bar" even if client changes
    its hostname back to "foo"
```

**Result:** GivenName is sticky once admin-renamed. The client
hostname change updates the Hostname field but doesn't reset the
admin's GivenName.

### Scenario 4: OIDC node with pre-existing preauth-key node (the SkyBars case)

Same as Scenario 1 — the GivenName uniqueness check is global, not
per-user. The OIDC node gets the auto-bumped suffix.

**Subtlety:** for OIDC nodes, the user is created from the OIDC
`name` claim (B174 sets `name = entry.Username` = "skyadmin"). If
the headscale has a user "skyadmin" already, the OIDC node might be
assigned to the existing user (depends on how headscale matches
OIDC users — typically by `sub` claim or `email`).

In SkyBars's case, id=35's user.name in the API is "tagged-devices"
(the synthetic user), not "skyadmin". This is because id=35 has tags
(tag:dev-skyadmin-skybars, tag:dev-skyadmin-skybars-1) — headscale
moves tagged nodes to the `tagged-devices` user regardless of who
created them.

---

## Implications for skygate

### For /my/devices UX (the "⏳ pending" display)

The DB row in `node_owner_map` has `hostname = "skybars-1"`, which
matches id=35's GivenName. But the B175 Strategy E autoupdater looks
for `n.UserName == portalUsername`, and id=35's user.name is
"tagged-devices" (not "skyadmin"). So Strategy E doesn't match.

**Two ways to fix this:**

1. **Strategy F (legacy orphan handler)**: in the autoupdater, add a
   fallback that handles the case where `n.UserName == "tagged-devices"`
   AND the node has `tag:dev-<portalUser>-<hostname>*` in its tags.
   This would be a 5-line addition to `Backfill` after Strategy E.

2. **Cleanup the duplicate**: delete id=34 (the old "skybars"
   preauth-key node). Then id=35 can be admin-renamed from
   "skybars-1" to "skybars" via `headscale nodes rename 35 skybars`
   (no conflict now). The DB row would then be consistent.

### For the audit log / node_owner_map

The `node_owner_map` table tracks `(hostname, user_id, headscale_id)`.
For OIDC nodes, the hostname stored in this table SHOULD be the
GivenName (the canonical unique name), not the Hostname. The
autoupdater should use the same.

### For the dev-tag policy

The dev-tag policy uses `tag:dev-<user>-<hostname>` (lowercase per
B176). The hostname here is the GivenName. So id=35 has TWO dev-tags:
- `tag:dev-skyadmin-skybars` (from B176 manual `headscale nodes tag`)
- `tag:dev-skyadmin-skybars-1` (from the autoupdater, when it tried
  the GivenName "skybars-1" as a Strategy E match — but that
  didn't actually happen because Strategy E didn't match; the
  "skybars-1" tag was probably added manually too)

---

## Cleanup options (operator's call)

### Option A: Delete id=34, rename id=35

```bash
# Verify which is the live device first (id=35 is online)
docker exec headscale headscale nodes list 2>&1 | grep -E "SkyBars|skybars"
# → id=34 offline (last seen ???), id=35 online (last seen recent)

# Delete the old preauth-key node
docker exec headscale headscale nodes delete 34

# Rename id=35 to the canonical name
docker exec headscale headscale nodes rename 35 skybars

# Now id=35 is "skybars", DB row matches
```

**Pros:** clean state, DB row is consistent, /my/devices stops
showing pending.
**Cons:** if id=34 was a real device, that device loses its
Tailscale identity (would need to re-register).

### Option B: Keep the duplicate, fix Strategy F

Add a Strategy F to `Backfill` that handles the `user.name ==
"tagged-devices"` case by looking at the existing tags. ~5 lines
in `nodeownership.go`.

**Pros:** no destructive action, future duplicates are handled.
**Cons:** the UI still shows "⏳ pending" for the existing SkyBars
node (until the DB row is updated by the next autoupdater tick).

### Option C: Leave as-is

The 404 fix from earlier (iptables block on 192.168.13.67) didn't
touch the SkyBars node. The duplicate is functional — both nodes
work, the dev-tags are correct. The "⏳ pending" UI is a cosmetic
issue that doesn't affect routing.

---

## Reusable lessons (cross-project)

1. **Tailscale's Hostname vs GivenName is a per-field distinction**
   worth documenting for any future Tailscale integration. The
   Hostname is what the client reports (case-preserved, can
   collide); the GivenName is the canonical DNS label
   (DNS-normalized, must be unique, may be auto-bumped).

2. **headscale's auto-rename is client-driven only.** Admin renames
   are strict. This is the right design (clients shouldn't have to
   know about each other; admins should), but it means migrations
   need to delete the conflicting client first or use a different
   name.

3. **Tagged nodes are moved to the "tagged-devices" synthetic user**
   regardless of who created them. This is invisible to admins but
   important for any code that joins on `n.UserName`. Skygate's
   Strategy E needs a fallback for this case.

4. **The `node_owner_map` table should store GivenName, not
   Hostname.** Headscale's TailscaleMapRequest sends GivenName as
   the canonical name. If skygate stores Hostname, it will be
   inconsistent with what headscale reports.
