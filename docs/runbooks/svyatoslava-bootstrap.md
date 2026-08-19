# svyatoslava-1 HA mirror bootstrap — Operator Runbook

**Status**: NOT YET RUN. Goal-89 (Phase 7) is waiting for the operator
to provision a new VM that will become the **technical HA mirror** for
the cluster.

**Last updated**: 2026-08-19
**When to use**: provisioning a second skygate node that mirrors the
primary (active/passive HA), without adding it as an exit node.

---

## 0. Quick reference (TL;DR)

svyatoslava-1 is a **technical HA host**, NOT a traffic-routing exit
node. It exists to take over the active role if the primary dies.

| Step | Where | What |
|------|-------|------|
| 1. Provision | Your VM host panel | Ubuntu 22.04+ VM, hostname `svyatoslava-1` |
| 2. Tailscale | svyatoslava-1 | `tailscale up --login-server=<headscale> --authkey=<preauth>` |
| 3. Tag | skygate-host-1 | `headscale nodes tag -i <id> --tags tag:dev-infra-svyatoslava` |
| 4. DB | skygate PG | `INSERT INTO node_owner_map ...` (see §5) |
| 5. Binary | operator's laptop | `skygate deploy-push --target=svyatoslava-1 ...` |
| 6. Wire-up | skygate-host-1 | `/admin/ha` → "Add HA member" (hostname, priority=2, role=standby) |
| 7. Verify | both nodes | `/healthz` 200 OK, `/admin/ha` shows 2 members |
| 8. DO NOT | anywhere | DO NOT add to `exit_servers`, DO NOT advertise Tailscale exit routes |

**Anonymization rule** (locked 2026-08-18): this runbook uses
RFC 5737 placeholder IPs (`192.0.2.x`, `198.51.100.x`, `203.0.113.x`),
`100.64.0.0/10` for Tailscale, `<your-fqdn>` for hostnames, and
`<your-…>` for credentials. Never substitute real production values
into a file in this repo.

---

## 1. Prerequisites

Before you start, have these ready:

| What | Where to get it | Example value |
|------|-----------------|---------------|
| **VM spec** | Your host panel | 4 vCPU, 8 GB RAM, 50 GB SSD, Ubuntu 22.04 LTS |
| **Public IP** | Your host panel | `203.0.113.10` (placeholder) |
| **Headscale URL** | skygate-host-1's `SKYGATE_HEADSCALE_URL` | `https://headscale.example.com` |
| **Preauth key** | Generate from skygate-host-1: `headscale preauthkeys create --user <operator> --reusable --expiration 24h` | `abcdef123456...` (24h valid) |
| **S3 deploy bucket** | same as skygate-host-1 | `s3://skygate-backups/deploy/` |
| **Operator user** | headscale user that owns the cluster | `<operator-username>` |

**Recommended**: snapshot or back-up the skygate PG and headscale state
before any new node joins, so a broken bootstrap is reversible.

---

## 2. Provision the VM

In your host panel (Hetzner / DO / Vultr / etc.):

1. Create a new VM with the spec above.
2. **Set hostname to `svyatoslava-1`** (not `svyatoslava`, not
   `svyatoslava-2` — the `-1` suffix is the convention for the first
   mirror node and matches the existing tag namespace).
3. Bind the public IP to the VM. Verify with
   `curl ifconfig.me` from inside the VM — should return the IP you
   bound.
4. Update OS: `apt update && apt upgrade -y`.
5. Install base tools: `apt install -y curl wget git nano htop ufw`.
6. Open inbound port 22 (SSH) only. **Do NOT open 80/443 yet** —
   skygate will do that once it's running.

If your host panel has a "private network" option, attach svyatoslava-1
to the same private net as skygate-host-1. The Tailscale link is the
primary connectivity path, but private-net fallback is useful for
diagnostics.

---

## 3. Install Tailscale + join headscale

On svyatoslava-1:

```bash
# 1. Install Tailscale
curl -fsSL https://tailscale.com/install.sh | sh

# 2. Bring it up against the existing headscale
sudo tailscale up \
  --login-server=https://<headscale-fqdn> \
  --authkey=<preauth-key> \
  --hostname=svyatoslava-1 \
  --accept-routes=false \
  --advertise-exit-node=false

# 3. Verify Tailscale IP
ip -4 addr show tailscale0 | grep inet
# Should show something like 100.64.0.20/10 — note this IP for §6
```

**`--accept-routes=false` and `--advertise-exit-node=false` are
CRITICAL**. svyatoslava-1 is a passive mirror, NOT an exit node.
Advertising routes or exit-node capability would make Tailscale try
to route user traffic through it, which is what we explicitly do not
want (see §9).

---

## 4. Apply the headscale tag

On **skygate-host-1** (the existing primary), once svyatoslava-1 has
joined:

```bash
# 1. Find svyatoslava-1's headscale node ID
headscale nodes list --user <operator-username> | grep svyatoslava-1
# Output: <id>  svyatoslava-1  <ip>  ...
# Note the numeric ID, e.g. 31

# 2. Apply the dev-infra tag
headscale nodes tag -i 31 --tags tag:dev-infra-svyatoslava

# 3. Verify
headscale nodes list --user <operator-username> | grep svyatoslava-1
# Now shows: 31  svyatoslava-1  100.64.0.20  tag:dev-infra-svyatoslava  ...
```

**Why `tag:dev-infra-svyatoslava` and not `tag:public`**: the
`tag:dev-infra-*` namespace is for technical infrastructure
(skygate hosts, HA mirrors, infra tooling). `tag:public` is reserved
for public-facing services that should be reachable by every
Tailscale user. Mixing them would (a) leak your infra node into the
public ACL and (b) cause `acl_perdevice_b118` to flag the ACL as
malformed.

---

## 5. Add to `node_owner_map` (DB)

Connect to the skygate PG (via `psql` from skygate-host-1 or any
admin box that can reach the DB):

```sql
-- 1. Confirm the table schema
\d node_owner_map
-- Should have: node_id (text PK), owner_username (text), tag (text),
--              created_at (bigint epoch), updated_at (bigint epoch nullable)

-- 2. Insert the new entry
INSERT INTO node_owner_map
  (node_id, owner_username, tag, created_at)
VALUES
  ('31', 'infra', 'tag:dev-infra-svyatoslava', EXTRACT(epoch FROM now())::bigint)
ON CONFLICT (node_id) DO UPDATE
  SET tag = EXCLUDED.tag,
      owner_username = EXCLUDED.owner_username,
      updated_at = EXTRACT(epoch FROM now())::bigint;

-- 3. Verify
SELECT node_id, owner_username, tag, created_at
FROM node_owner_map
WHERE tag = 'tag:dev-infra-svyatoslava';
-- Should return: 31 | infra | tag:dev-infra-svyatoslava | <epoch>
```

**Why `owner_username = 'infra'`**: the existing convention is
`infra` for technical infrastructure nodes (emilia, karolina,
sharlotta, skygate-host-1 all use `infra`). `skyadmin` and `michail`
are for operator/personal devices. Mixing them would cause
`acl_perdevice_b118` to flag the ACL as malformed.

---

## 6. Install the skygate binary

Two options — pick the one that matches your workflow:

### Option A: Push from operator's laptop (recommended)

This is what `B150` was built for. From your laptop, with the
`skygate` CLI in `$PATH`:

```bash
# 1. Build (or download) the binary
cd C:\Projects\skygate
go build -o ./bin/skygate ./cmd/skygate
git describe --tags --dirty > ./bin/meta.json
# (or use the pre-built artifact from your CI)

# 2. Push to s3://skygate-backups/deploy/svyatoslava-1/
skygate deploy-push \
  --target=svyatoslava-1 \
  --binary=./bin/skygate \
  --meta=./bin/meta.json

# 3. On svyatoslava-1, pull + swap + restart
ssh <user>@<svyatoslava-1-public-ip> 'skygate deploy-pull && sudo systemctl restart skygate'

# 4. Verify
ssh <user>@<svyatoslava-1-public-ip> 'systemctl status skygate'
# Should be: active (running)
```

### Option B: Pull from s3 directly on svyatoslava-1

If you can't run `skygate deploy-push` from your laptop (e.g.
firewall blocks s3):

```bash
# 1. Install skygate via package manager (preferred), OR download
#    the matching artifact manually from the GitHub release
sudo apt install ./skygate_<version>_<arch>.deb
# OR:
sudo curl -L -o /usr/local/bin/skygate https://github.com/.../releases/.../skygate
sudo chmod +x /usr/local/bin/skygate

# 2. Configure /etc/skygate.env (mirror the file from skygate-host-1,
#    change SKYGATE_SELF_HOSTNAME=svyatoslava-1)
sudo cp <from-skygate-host-1> /etc/skygate.env
sudo sed -i 's/^SKYGATE_SELF_HOSTNAME=.*/SKYGATE_SELF_HOSTNAME=svyatoslava-1/' /etc/skygate.env
sudo chmod 600 /etc/skygate.env

# 3. Start the service
sudo systemctl enable --now skygate
sudo systemctl status skygate
```

Either way, the binary version on svyatoslava-1 should match the
version on skygate-host-1. Mismatched versions in an HA pair are
unsupported (the elector's JSON shape contracts drift).

---

## 7. Wire up as an HA member

This step makes svyatoslava-1 visible to the v1.5.0 HA chain.

On **skygate-host-1**, open the `/admin/ha` page in your browser:

1. Sign in as `skyadmin` (or any user with admin role).
2. Go to `/admin/ha` → "Cluster topology" section.
3. Click **"Add HA member"**.
4. Fill the form:
   - **Hostname**: `svyatoslava-1`
   - **Priority**: `2` (P1 is skygate-host-1)
   - **Tailscale IP**: `100.64.0.20` (from §3)
   - **Public IP**: `203.0.113.10` (from §1)
   - **Role**: `standby` (always start as standby; the elector will
     promote to `active` only if P1 dies)
5. Click **Save**.

The B145 elector picks up the new chain on its next 5s tick and
starts heartbeating svyatoslava-1.

**Verify in the UI**:
- `/admin/ha` should now show 2 members in the chain.
- The "Last failover" stat updates on every transition.
- The audit log shows the "chain member added" event.

**Verify via the chain directly** (read-only):

```bash
# On skygate-host-1, query the global_settings row
psql -h <db-host> -U skygate -d skygate \
  -c "SELECT value FROM global_settings WHERE key='ha_chain';"
# Should return JSON with 2 members: skygate-host-1 (P1) and svyatoslava-1 (P2)
```

---

## 8. Verify the full chain

Run through this checklist before considering the bootstrap done:

| Check | Command | Expected |
|-------|---------|----------|
| svyatoslava-1 Tailscale OK | `ssh svyatoslava-1 'tailscale status'` | shows `svyatoslava-1` with its 100.64.x.x IP |
| svyatoslava-1 skygate health | `curl -s http://100.64.0.20:8080/healthz` | `ok` (200) |
| svyatoslava-1 skygate ready | `curl -s http://100.64.0.20:8080/readyz` | `healthy` (200) |
| skygate-host-1 sees svyatoslava-1 | `curl -s http://100.64.0.1:8080/admin/ha/chain` (if you exposed a debug endpoint) | 2 members |
| Both nodes agree on active | `curl -s http://100.64.0.1:8080/admin/ha/active` and same on svyatoslava-1 | both report `skygate-host-1` |
| DR drill (dry-run only) | `/admin/deploy` → "Test-failover" | shows svyatoslava-1 as the predicted next active |

If all 6 PASS, the bootstrap is complete. If any FAIL, see §10.

**Real DR drill (NOT in this runbook)**: requires killing skygate on
skygate-host-1 and watching svyatoslava-1 take over. Schedule a
maintenance window (Goal-91 / Phase 9). Never run this without
operator approval.

---

## 9. DO NOT — the negative section

Things you should explicitly **NOT** do during this bootstrap, with
reasons:

| Don't | Why |
|-------|-----|
| **DO NOT add svyatoslava-1 to `exit_servers`** | It's a technical HA host, not a traffic-routing node. Adding it would make the `acl_perdevice` and `exit_rules` system treat it as a user-routable exit. It would also make the B118 tag-owner test FAIL. |
| **DO NOT apply `tag:exit-node`** | Same reason. The `tag:exit-node` ACL entry would cause Tailscale users to see it as a potential exit in their client. |
| **DO NOT `advertise-exit-node`** on svyatoslava-1's Tailscale | Same reason. `tailscale up --advertise-exit-node=false` is the default in §3. |
| **DO NOT promote svyatoslava-1 to P1** | P1 is reserved for the operator's preferred-primary. Use P2 or higher. |
| **DO NOT run `skygate ha-promote` on svyatoslava-1** unless skygate-host-1 is confirmed dead | The `ha-promote` verb is a one-shot override, not a permanent role change. Use `/admin/ha`'s "Force promote" only for DR scenarios. |
| **DO NOT add `tag:public`** to svyatoslava-1 in headscale | The `tag:public` ACL is for end-user-facing services. Mixing infra into it would leak the node's existence to every Tailscale user. |
| **DO NOT skip the B118 regression test** after the bootstrap | Run `scripts/check_b118.sh` to confirm the new tag-owner-from-name logic still passes. |

The summary: svyatoslava-1 is a **passive mirror**. It exists to
take over the active role when skygate-host-1 dies. It should never
be the source of outbound user traffic.

---

## 10. Rollback

If the bootstrap goes wrong (svyatoslava-1 won't join, cert errors,
ACL malformed, etc.), the rollback is:

```sql
-- 1. Remove from node_owner_map
DELETE FROM node_owner_map WHERE node_id = '31';
```

```bash
# 2. Remove from HA chain (via /admin/ha, click "Remove member" on svyatoslava-1)
# OR directly in the DB:
psql -h <db-host> -U skygate -d skygate \
  -c "UPDATE global_settings SET value = jsonb_set(value, '{members}',
       (SELECT jsonb_agg(elem) FROM jsonb_array_elements(value->'members') elem
        WHERE elem->>'hostname' != 'svyatoslava-1'))
     WHERE key = 'ha_chain';"
```

```bash
# 3. Remove from headscale (force-delete is safe if you also wipe the node from /admin/ha)
headscale nodes delete --force -i 31

# 4. On svyatoslava-1, uninstall Tailscale + skygate
sudo tailscale logout
sudo systemctl disable --now skygate
sudo apt remove --purge skygate

# 5. Destroy the VM (or keep it powered off until you debug)
```

**Important**: do these steps IN ORDER. Removing from the HA chain
BEFORE removing from headscale would leave the elector trying to
heartbeat a node that no longer exists in headscale — it would mark
it `unreachable` after 15s and re-attempt every 5s. Harmless but
spammy in the audit log.

---

## 11. Common issues

### 11.1. svyatoslava-1 doesn't appear in `headscale nodes list`

- Check the preauth key expiration (default 24h, may be shorter).
- Check the `--login-server` URL is reachable from svyatoslava-1's
  public IP. Firewalls often block outbound 443 from new VMs.
- Check `tailscaled` is running: `sudo systemctl status tailscaled`.

### 11.2. `/healthz` returns 503 on svyatoslava-1

- Check the skygate logs: `sudo journalctl -u skygate -n 50`.
- Most common: DB connection failure. Verify `/etc/skygate.env` has
  the right `SKYGATE_PG_*` vars.
- Second most common: the binary version on svyatoslava-1 is older
  than the DB schema. Re-pull + restart.

### 11.3. `/admin/ha` shows svyatoslava-1 as `unreachable`

- The elector heartbeat is failing. Tailscale connectivity check:
  `tailscale ping svyatoslava-1` from skygate-host-1.
- If Tailscale is OK, check that skygate is listening on its
  Tailscale IP: `ss -tlnp | grep :8080` on svyatoslava-1 should
  show the listener bound to `0.0.0.0` or the Tailscale IP.
- After 3 missed heartbeats (15s default) the node is marked
  `unreachable`; the elector retries every 5s.

### 11.4. `acl_perdevice_b118` regression test FAILs

- Most common: the `tag:dev-infra-svyatoslava` owner is not `infra@`
  in the live policy. The B118 test requires all `tag:dev-infra-*`
  tags to be owned by `infra@`.
- Verify: `headscale nodes list -o json | jq '.[] | select(.name=="svyatoslava-1") | .forcedTags'`
  should show `tag:dev-infra-svyatoslava`. Then in the skygate ACL,
  look for the per-user loop in `internal/acl/acl.go` — the
  `tag:dev-infra-` prefix should parse the owner as `infra@`.

### 11.5. Tailscale exit-node flag accidentally enabled

If you ran `tailscale up --advertise-exit-node=true` and now the node
is in headscale with that capability:

```bash
# Remove the routes from the node
sudo tailscale set --advertise-exit-node=false
# Then in headscale, the routes are auto-removed (headscale enforces)
headscale nodes list | grep svyatoslava-1
```

Then re-apply the tag in §4 — the `--advertise-exit-node=false`
change is independent of the headscale tag, so the fix is just the
tailscale side.

### 11.6. Cert errors after swap to svyatoslava-1

If the certsync scheduler (B147) is running on svyatoslava-1 but the
cert in `/var/lib/skygate/certs/cert.pem` is the old one from before
the bootstrap:

- Check `SKYGATE_CERTSYNC_ENABLED=true` in `/etc/skygate.env`.
- Check S3 connectivity: `aws s3 ls s3://skygate-backups/certs/` (or
  whatever s3cmd / mc equivalent you use).
- Force a pull: `skygate certsync-pull-now` (or restart skygate; the
  certsync scheduler runs on every tick).

---

## 12. Cross-references

- **`docs/runbooks/v1.5.0-ha-and-deploy.md`** — the v1.5.0 master
  runbook. Sections 2 (B145 HA chain), 3 (B147 certsync), 5 (B149
  /admin/ha), 6 (B150 /admin/deploy + skygate CLI) are the
  background for this runbook.
- **`docs/internal/ha-v1.5.0-execution.md`** — the BL-2 master
  tracker. §6 has the B145/B147/B148/B149/B150 status log.
- **`scripts/check_b145.sh`** — the 40-contract B-check that pins
  the HA chain + pluggable DNS provider behavior.
- **`scripts/check_b149.sh`** — the 56-contract B-check for /admin/ha.
- **`scripts/check_b150.sh`** — the 54-contract B-check for
  /admin/deploy + the `skygate deploy-*` and `skygate ha-*` CLI
  verbs.
- **`internal/ha/chain.go`** — the `HaChain` / `HaMember` /
  `HARole` types and the chain JSON shape contract.
- **`internal/ha/elector.go`** — the 5s heartbeat loop, the
  3-missed-heartbeat → `unreachable` rule, and the role-transition
  logic.
- **`internal/deploy/push.go`** — the `RunPush` function used by
  `skygate deploy-push` (Option A in §6).
- **`internal/deploy/pull.go`** — the `RunPull` function used by
  `skygate deploy-pull` (Option A in §6).
- **`internal/deploy/subcommand.go`** — the CLI dispatch (7 verbs:
  deploy-push, deploy-pull, deploy-sync, deploy-status, ha-promote,
  ha-demote, ha-reclaim).

---

## 13. Anonymization checklist (operator self-audit)

Before committing this runbook (or any change to it), run the
personal-data sweep on the file:

```powershell
# In C:\Projects\skygate, from PowerShell:
$patterns = @(
  '192\.0\.2\.',                # RFC 5737 — should NOT appear (this is the placeholder)
  '198\.51\.100\.',             # RFC 5737 — should NOT appear
  '203\.0\.113\.',              # RFC 5737 — should NOT appear
  '@example\.com',              # example.com — should NOT appear
  'skygate\.<your-domain>',     # placeholder check — should NOT appear
  '<your-[a-z-]+>'              # any un-substituted placeholder
)
Get-Content docs/runbooks/svyatoslava-bootstrap.md |
  Select-String -Pattern $patterns
# Should return ZERO matches.
```

If any pattern matches, replace with the RFC 5737 placeholder
(`192.0.2.x`, `198.51.100.x`, `203.0.113.x`, `100.64.0.0/10`,
`<your-fqdn>`, `<your-username>`) and re-run.

**Operator-specific patterns** (e.g. your public IP, your domain,
your Tailscale IP range, your hostname) must be added separately by
each operator — do NOT commit them to the public runbook.

The same sweep is run on the rest of the public tree by the
`docs/README.md` "Personal-data sweep" section. This file is included
in that sweep.
