# TD-11: Cloudflare /12+/24 rule grouping — detailed plan

**Status:** DEFERRED (operator priority — LOW per PLANS.md, but
the actual operator's data already shows the cost: 46/151
rows = 30% of all rules are CDN-tracked).

**Date:** 2026-09-07

---

## What the operator actually has today (live data, 192.168.13.69)

```
TOTAL:  151 rows, 151 unique (user,device,exit,type,value) tuples
CDN:     46 rows (30% of total), 5 distinct parent_domains
MANUAL:  30 rows
```

The 5 CDN-tracked parent_domains + their row counts:

```
cdn:cloudflare:production.cloudflare.docker.com → 15 rows
cdn:cloudflare:discordapp.com                  → 15 rows
cdn:google:youtube.com                         → 10 rows
cdn:akamai:agent.minimax.io                    →  5 rows
cdn:google:gcr.io                               →  1 row
```

Sample CIDRs for `cdn:cloudflare:discordapp.com`:

```
2.16.0.0/13          23.192.0.0/11        23.32.0.0/11
104.16.0.0/12        104.24.0.0/14        103.21.244.0/22
103.22.200.0/22      103.31.4.0/22        108.162.192.0/18
131.0.72.0/22        141.101.64.0/18      162.158.0.0/15
172.64.0.0/13        173.245.48.0/20      188.114.96.0/20
190.93.240.0/20      197.234.240.0/22     198.41.128.0/17
```

The full Cloudflare published set (15 CIDRs). For each
(user, device, exit_node) tuple that has a Cloudflare-tracked
domain, the autoupdater inserts all 15 CIDRs as separate
`subnet` rows. The 5 distinct parent_domains × 15 CIDRs = 75,
but the actual 46 is from per-(user,device,exit) expansion
(operator has 1 user × 1 device × 1 exit-node + 1 user × 1
device × 2 exit-nodes combinations × 15 = 45-60).

**The cost is real**: 30% of all rules are CDN-derivable
from a single `parent_domain` marker. The data is correct
(headscale gets the same routes either way) but the storage
+ per-tick autoupdate cost is wasted.

---

## Why it's slow / wastes rows

Cloudflare (and other anycast CDNs — Fastly, Google, Akamai)
operate by advertising many disjoint supernet blocks. The
Cloudflare IPv4 set spans 12 distinct /8s:

```
2.16.0.0/13        (2.16-2.23, 512k IPs)
23.192.0.0/11 + 23.32.0.0/11  (23.192-23.255, 4M IPs)
103.21.244.0/22, 103.22.200.0/22, 103.31.4.0/22  (3 disjoint /22s in 103.x)
104.16.0.0/12 (1M IPs)  + 104.24.0.0/14 (104.24-104.27, INSIDE /12)  ← 104.24/14 is REDUNDANT
108.162.192.0/18  (108.162.192-255)
131.0.72.0/22      (131.0.72-75)
141.101.64.0/18    (141.101.64-127)
162.158.0.0/15     (162.158-162.159)
172.64.0.0/13      (172.64-172.71)
173.245.48.0/20    (173.245.48-63)
188.114.96.0/20    (188.114.96-111)
190.93.240.0/20    (190.93.240-255)
197.234.240.0/22   (197.234.240-243)
198.41.128.0/17    (198.41.128-255)
```

The 15 ranges are not contiguous — they span 12 distinct
/8 blocks. A perfect merge into a single supernet isn't
possible (would need a /4 covering 4096 /8s).

**What IS mergeable** (per-CDN, pairwise):
- `104.16.0.0/12 + 104.24.0.0/14` → `104.16.0.0/12` (the /14 is
  already inside the /12, so it's REDUNDANT). Saves 1 CIDR.
- `23.192.0.0/11 + 23.32.0.0/11` → `23.32.0.0/11` (the /11 covers
  23.32-23.63, but 23.192-23.223 is OUTSIDE 23.32.0.0/11. So
  they're adjacent but not overlapping. A /10 covering 23.32-23.255
  would work but the prefix length is then /10, not /11.) — actually
  this is a case where the merge COULD happen: 23.192-23.223 and
  23.32-23.63 are disjoint. /10 covering 23.32-23.255 would
  include 23.32-23.63 AND 23.192-23.223? No, 23.32-23.63 is
  the .32-.63 range, 23.192-23.223 is .192-.223. A /9 covering
  23.0-23.127 would work but that's massive.

Bottom line: per-CIDR merging saves at most 1-2 rows per CDN.
The real savings come from NOT storing 15 separate rows at all.

---

## The 3 possible approaches

### Approach A: One-time merge on existing data (lowest scope)

**What**: A new tool `scripts/merge_cdn_cidrs.sh` (or a one-time
`migrateV065PG`) that:
1. For each (user, device, exit_node, parent_domain) tuple
   that has ≥ 2 rows with overlapping or redundant CIDRs,
   replace them with the smallest supernet.
2. E.g. for `104.16.0.0/12 + 104.24.0.0/14`, keep only
   `104.16.0.0/12` (saves 1 row per tuple).

**Estimated savings**: 5-10 rows total (the operator's
discordapp.com has 1 redundant /14, and there may be a
few more across the 5 CDN domains).

**Cost**: ~30 min. One SQL migration, no new code.

**Drawback**: doesn't help the next autoupdate tick. The
autoupdater still inserts 15 CIDRs per (user, device, exit)
tuple. The savings are one-shot.

### Approach B: CDN marker on the DOMAIN rule + on-the-fly expansion at SyncAdvertisedRoutes time

**What**: Restructure so that the autoupdater doesn't insert
15 per-CIDR rows. Instead it:
1. Sets the `parent_domain` on the existing DOMAIN rule
   to `cdn:cloudflare:discordapp.com`.
2. The `SyncAdvertisedRoutes` step (which is the function
   that actually talks to headscale) reads the DOMAIN rules,
   sees the CDN marker, and expands the marker to the
   15 published CIDRs at the moment of pushing to headscale.

**Estimated savings**: 14 rows per (user, device, exit_node)
tuple per CDN domain. For the operator: 14 × 6 (user,device,exit
tuples for the 5 CDN domains) = ~84 rows. Down from 46 to ~5 rows.

**Cost**: ~1 day. This is the refactor that PLANS.md's
"~1 day" estimate matches.

**Files to change**:
- `internal/feature/exit_rules/sync.go` — change
  `SyncAdvertisedRoutes` to expand CDN markers before
  sending to headscale.
- `internal/feature/exit_rules/sync.go` — change
  `DomainAutoUpdater` to set the DOMAIN rule's
  `parent_domain` to the CDN marker, NOT insert per-CIDR
  rows. (This is the bigger behavior change.)
- `internal/feature/exit_rules/cdn.go` — extract the
  `cdnParentMarker` + `cdnCIDRs` lookup as a public helper
  (currently inline in sync.go:473).
- `internal/db/migrations_pg.go` — add `migrateV065PG`
  to convert existing per-CIDR rows back to per-DOMAIN
  rows (one-time data fix).
- `internal/handlers/templates/admin/exit_nodes.html` —
  the rule table now shows the CDN marker for grouped
  rules, not 15 per-CIDR rows.
- `internal/feature/admin/exit_nodes.go` — same.

**Drawback**:
- More complex to debug. When the operator sees
  "discordapp.com rule" in /admin/exit-rules, they
  expect the headscale ACL to have 15 CIDRs. If the
  autoupdate is broken, they see only 1 rule. The
  /admin/exit-nodes page needs to show the EXPANDED
  CIDRs (read-only, computed on the fly) so the
  operator can verify "yes, the marker is being expanded
  to the right set".
- The /admin/exit-rules page already shows the per-CIDR
  rows. After B187, the rule list will show "discordapp.com
  (CDN: cloudflare, 15 ranges)" instead of 15 rows. This
  is a UX change.
- The headscale ACL still gets the 15 CIDRs. The "save"
  step's accounting (the audit log "added 15 rules")
  needs updating to "added 1 grouped rule (expands to
  15 headscale routes)".

### Approach C: Composite-type rule + binary expansion

**What**: Add a new `target_type='cdn'` (or
`target_type='cidr-set'`) that stores a single row with
the CIDR list in a JSON column. The autoupdater inserts 1
row per (user, device, exit, domain). The SyncAdvertisedRoutes
step reads the JSON and pushes the expanded list to headscale.

**Estimated savings**: same as B (~84 rows).

**Cost**: ~1.5-2 days. New column + new SQL path + new
handler logic + new migration to convert existing rows.

**Drawback**:
- Most invasive of the 3. The headscale ACL builder
  (`internal/acl/`) reads `device_rules` to build the ACL
  config; it would need to be updated to handle the JSON
  column.
- More breaking changes to the rule storage model.
- Higher risk of bugs (the JSON column needs to be
  sanitized, validated, etc.).

---

## Recommendation: Approach B (CDN marker on DOMAIN rule + on-the-fly expansion)

Approach B is the right balance:
- ~1 day of work (matches the PLANS.md estimate).
- 84-row savings (vs Approach A's 5-10).
- Doesn't change the storage model (still SQL rows).
- Easy to roll back (revert + re-run autoupdate; rows
  re-emerge).
- The "CDN marker on the DOMAIN rule" pattern is already
  in use (parent_domain = "cdn:cloudflare:..."); we're
  just moving the marker from the per-CIDR row to the
  per-domain row.

The UX change (15 rows → 1 row in the rule list) is a
feature, not a regression. The /admin/exit-nodes page can
show the expanded CIDR list read-only so the operator
can verify.

---

## Step-by-step plan (Approach B)

### Phase 1: New migration (data + schema)
1. New `migrateV065PG` that:
   - For each (user, device, exit_node, parent_domain) that
     has ≥ 1 row in `device_rules` where parent_domain LIKE
     'cdn:%', DELETE all the per-CIDR rows.
   - UPDATE the DOMAIN rule (target_type='domain',
     target_value=domain) to set its `parent_domain` to
     `cdn:cloudflare:<domain>` (so the autoupdater's
     short-circuit at sync.go:439-448 matches).
   - This is a 1-time data fix. New deploys would skip
     this migration (the autoupdater won't insert per-CIDR
     rows anymore).
2. Add a helper `db.CountDeviceRulesByCDN(d, user, device, exit, parent)`
   that returns the count of CIDR rows for a CDN marker
   (used by the audit log message).

### Phase 2: Change the autoupdate path
1. In `internal/feature/exit_rules/sync.go:473-518` (the
   `detectCDN` branch in `DomainAutoUpdater`):
   - REMOVE the per-CIDR INSERT loop (lines 489-500).
   - INSTEAD: UPDATE the existing DOMAIN rule's
     `parent_domain` to the CDN marker (e.g. via
     `db.UpdateDeviceRuleParentDomain(d, id, marker)`).
   - REMOVE the legacy /32 cleanup loop (lines 501-515)
     — the per-CIDR cleanup was only needed because the
     per-CIDR rows were being inserted; without that, no
     cleanup is needed.
2. The `existingMarker` check (sync.go:439-448) stays —
   it now also matches the case where the DOMAIN rule
   itself has the marker (not just the per-CIDR rows).
   The query needs to be widened to also check the DOMAIN
   rule's parent_domain.
3. Update the audit log message from "CDN detected:
   cloudflare — using 15 published ranges" to
   "CDN detected: cloudflare — grouped as 1 rule
   (expands to 15 published ranges at headscale sync time)".

### Phase 3: Change the SyncAdvertisedRoutes path
1. In `internal/feature/exit_rules/sync.go:SyncAdvertisedRoutes`
   (the function that pushes to headscale), add a new
   step that:
   - For each DOMAIN rule with parent_domain LIKE 'cdn:%',
   - Expand the marker to the published CIDR list (call
     `cdn.go`'s `detectCDN` helper, or a new
     `expandCDNMarker(marker) []string` helper that maps
     the CDN name back to its CIDR list).
   - Add the expanded list to the ACL config (alongside
     any other rules the device/exit has).
2. The current code reads `device_rules` and builds the
   ACL config from there. The new step needs to happen
   BETWEEN the SQL read and the headscale push.

### Phase 4: UX — /admin/exit-rules + /admin/exit-nodes
1. In `internal/handlers/templates/my/exit_rules.html`:
   - Add a row group separator: "discordapp.com
     [CDN: cloudflare, expands to 15 ranges] → button to
     show/hide the expanded list".
   - The 15 per-CIDR rows are NOT shown (they don't
     exist anymore in device_rules).
2. In `internal/handlers/templates/admin/exit_nodes.html`:
   - The per-device rule list shows "discordapp.com
     [CDN: cloudflare, 15 ranges]". The per-CIDR list is
     GONE.
3. In `internal/feature/exit_nodes.go:AdminExitNodes`:
   - When the operator's "rules to advertise" panel
     shows the routes, the CDN marker is expanded on
     the fly (call `expandCDNMarker`) so the operator
     still sees the 15 actual routes that headscale will
     receive.

### Phase 5: Tests
1. New unit test: `TestDomainAutoUpdater_CDN_GroupsAsOneRule`
   in `internal/feature/exit_rules/cdn_test.go` (or a new
   file). Asserts that after a tick, there's 1 DOMAIN row
   + 0 per-CIDR rows for a Cloudflare-tracked domain.
2. New unit test: `TestSyncAdvertisedRoutes_ExpandsCDNMarkers`
   in `internal/feature/exit_rules/sync_test.go`. Asserts
   that the ACL config sent to headscale contains the
   15 expanded CIDRs.
3. New B-check: `scripts/check_b237_22.sh` (or similar)
   pins the contract: no per-CIDR rows for CDN markers,
   the DOMAIN rule's parent_domain has the cdn: prefix,
   the SyncAdvertisedRoutes step has the expansion logic.

### Phase 6: Docs + migration rollback
1. Update `docs/deploy.md` § 11 (autoupdate + CDN section)
   to describe the new grouped-rule model.
2. Update `AGENTS.md` B237.xx block.
3. Add a one-time rollback script in `scripts/rollback_cdn_grouping.sh`
   that converts the grouped rules back to per-CIDR rows
   (in case the operator wants to revert).

---

## Total effort estimate

- Phase 1 (migration + helper): 30 min
- Phase 2 (autoupdate change): 1-2 hours
- Phase 3 (SyncAdvertisedRoutes expansion): 1-2 hours
- Phase 4 (UX changes): 1-2 hours
- Phase 5 (tests): 1-2 hours
- Phase 6 (docs + rollback): 30 min

**Total**: ~5-6 hours, 1 day.

This matches the PLANS.md estimate of "~1 day".

---

## Why I'm recommending B over the others

**A is too narrow**: saves 5-10 rows once, doesn't address
the per-tick cost. Not worth a release on its own.

**C is too invasive**: the JSON column + new ACL builder
path is a bigger refactor. Better done as a v2.0 (when
the rule model gets a redesign anyway).

**B matches the existing architecture**: the `cdnParentMarker`
format is already in use. We're just consolidating where
the marker lives (on the DOMAIN rule, not the per-CIDR
rows) and where the expansion happens (at SyncAdvertisedRoutes
time, not at autoupdate time).

**B is reversible**: a one-time rollback script can
re-expand the markers into per-CIDR rows. The operator
can A/B test in staging before promoting to production.

---

## When to do this

This is a LOW-priority item per PLANS.md. The operator's
data has 46 CDN rows / 151 total (30%), but the per-tick
cost is small (the autoupdater runs every 5 minutes and
the B125 UNIQUE INDEX prevents duplicate inserts). The
"cosmetic" framing in PLANS.md is accurate: nothing is
broken, the row count is just higher than necessary.

**Recommendation**: do this after Phase 10 (v1.5.2 release)
is fully deployed + the operator has had a week to confirm
the B146 live test + the deployment variants work. The
TD-11 work can ship as v1.5.3 (or whatever the next
sub-patch is).

If the operator wants to do it sooner (because the rule
count bothers them), the work is well-scoped and can land
in a single B-block.
