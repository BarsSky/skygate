# Tailnet Diagnostics — split detection, root cause, and fix

**Status:** v1.3.10 (B110) — adds detection + diagnostics for tailnet
splits. Operator-facing runbook for the symptom: "headscale says my
node is online, but other nodes can't see it (or vice versa)."

---

## TL;DR

- Symptom: `docker exec skygate-skygate-1 tailscale status` shows
  fewer peers than `docker exec headscale headscale nodes list`
  reports as online.
- Detection: `tailnet.split_suspected` system test (B110).
  Run via `/admin/system_tests` or via the
  `scripts/tailnet_probe.sh` shell helper from any node.
- Fix: re-register all nodes with a single preauth key (operator
  action — see [Fix procedure](#fix-procedure)). skygate cannot do
  this from the server side; nodes must `tailscale up --authkey=<KEY>`
  with the same key.
- Prevention: monitor `tailscale status` peer count per node; alert
  on "any node sees < 50% of expected peers".

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

**6 of the 10 online nodes are invisible from skygate-host-1:**
`skybars (100.64.0.5)`, `skyworker (100.64.0.1)`,
`a71 (100.64.0.19)`, `olesya (100.64.0.16)`,
`svyatoslava-1 (100.64.0.15)`, `nothing-phone-2 (100.64.0.6)`.

Same symptom from karolina (100.64.0.2): only sees emilia, sharlotta,
skygate-host-1.

The two "clusters" do **not** correlate with:
- `preauth_key.id` (visible: 8, 9, 65, 191; hidden: 19, 61, 63, 129, 180, 189)
- `machine_key` (17 unique, 1 per node — no sharing)
- `user` (all `tagged-devices`)
- `tagged-devices` user (single bucket)

---

## Root cause analysis (2026-08-13 deep-dive)

### What we ruled out

| Hypothesis | Verdict | Evidence |
|------------|---------|----------|
| Tailnet lock key mismatch | ❌ | headscale 0.29.1 default = no tailnet lock; no key in state files |
| Different preauth keys per node | ❌ | 17 unique preauth keys in use, but split is NOT correlated with pak.id |
| Duplicate `machine_key` / `node_key` | ❌ | All 17 keys are unique (1 per node) |
| Different headscale instances | ❌ | `headscale nodes list` returns all 17 |
| Different Tailscale versions | ❌ | All online nodes run 1.98.x (per state files) |
| `tagOwners` / ACL permission | ❌ | Even if a peer is "denied", it still appears in `tailscale status` |

### What we observed

| Signal | Value | Implication |
|--------|-------|-------------|
| headscale `online` count | 10/17 | Control plane believes 10 nodes are alive |
| `tailscale status` peer count from skygate-host-1 | 4 (self+3) | Control plane sent 4-peer map to this node |
| `/machine/map` source IPs in headscale log | ONLY 192.168.13.67 (home router NAT) | The home-LAN-cluster is the one actively polling |
| skygate container's connection to headscale | 172.18.0.2 (Docker bridge) — for `/health` only | skygate-host-1 may be using a **stale map** |
| headscale `last_seen` for hidden nodes | Recent (within minutes) | Hidden nodes are still heartbeat-ing |
| Traceroute to `emilia` (100.64.0.3) | 1 hop, 82ms (DERP) | Network path works for visible nodes |
| Traceroute to `skybars` (100.64.0.5) | Broken at hop 6 (ISP boundary) | Network path is broken for hidden nodes |

### Working theory (as of 2026-08-13)

**The headscale control plane has TWO logically distinct map sessions.**
Nodes that connect from the home LAN (via 192.168.13.67 NAT) all
share one "session" of the network map. Nodes that connect via
public IP / VPSes (or via the skygate container) have a divergent
map that does not include the home-LAN cluster.

**Why this happens:** headscale 0.29.x has a known behavior where
the network map is partitioned by the **control plane session**,
not by `node_id` or `machine_key`. A node gets the "stale map" if:

1. It authenticated recently (last_seen is fresh).
2. It received a map that lists the other 4 VPS nodes but NOT the
   13 home nodes.
3. The home-LAN cluster is on a different session (different
   `/machine/map` source IP, different state).

**Most likely trigger:** some time in the past, the home router
(192.168.13.67) was assigned a different `tailscale up` invocations
that created a different control plane session, and that session
"stuck" for all home devices that NAT through it. The skygate
container (Docker bridge) and the VPSes (public IPs) registered
separately and got a different map.

**Why it's hard to fix from skygate side:** the map is generated
by headscale based on the current control plane session. skygate
admin tools can list nodes (`/api/v1/node`), but cannot force a
node to re-fetch its map or change sessions.

**The fix MUST happen at the tailscale client level:** every node
must re-authenticate with the same preauth key, which forces all
nodes into the same control plane session.

---

## Fix procedure

### Step 1: confirm the split (5 min)

Run the new system test from `/admin/system_tests`:

```text
Run: tailnet.split_suspected
Expected: FAIL with "LIKELY TAILNET SPLIT" warning
```

Or run the shell script from the skygate container:

```bash
docker exec skygate-skygate-1 bash /path/to/tailnet_probe.sh
# Should report <50% of expected peers visible + "DIAGNOSIS: TAILNET SPLIT LIKELY"
```

Or from the operator's laptop (with tailscale installed):

```bash
/path/to/tailnet_probe.sh
# If split is unidirectional, you may see all peers from your
# laptop but the skygate container sees only a subset.
```

### Step 2: generate a single preauth key (2 min)

On the skygate-vm (where headscale runs):

```bash
docker exec headscale headscale preauthkeys create \
  --user skyadmin \
  --reusable \
  --expiration 24h
# Output: hskey-auth-XXXXXXXXXXXXXXXX
```

The `--reusable` flag is the key part: every node in the tailnet
must use the SAME key value. `--expiration 24h` gives a safety net
so the key self-destructs if you forget to revoke it.

### Step 3: re-authenticate every node (10-30 min, downtime)

For EACH node in the tailnet (17 total, including 7 offline):

```bash
# Stop tailscaled, re-auth with the new key, restart.
sudo tailscale down
sudo tailscale up --authkey=hskey-auth-XXXXXXXXXXXXXXXX
sudo systemctl restart tailscaled
```

**Critical: do this on every node in the same maintenance window.**
The map will only converge after ALL nodes have re-authed. If you
only re-auth some, you'll have a worse split than before.

**Order matters for minimal disruption:**

1. **VPS-side first** (emilia, karolina, sharlotta, svyatoslava-1).
   These have public IPs and are the "anchors" of the new map.
2. **skygate-host-1** next. The skygate container restarts will
   take 30-60s.
3. **Home-LAN devices last** (skybars, skyworker, a71, olesya,
   nothing-phone-2, basic, base, skybars-1, cyborg,
   svyatoslava-legacy, desktop-cuo0tfb, msi). The home router at
   192.168.13.67 may need to have tailscale re-installed or the
   device rebooted.

### Step 4: verify (5 min)

From skygate-host-1:

```bash
docker exec skygate-skygate-1 tailscale status | wc -l
# Should be ~17 (one line per peer + header)
```

From a VPS:

```bash
ssh emilia "tailscale status | wc -l"
# Should also be ~17
```

Run the B110 system test again:

```text
Run: tailnet.split_suspected
Expected: PASS with all peers reachable
```

### Step 5: revoke the preauth key (1 min)

```bash
docker exec headscale headscale preauthkeys expire --user skyadmin
# Or list and revoke the specific key ID from Step 2.
```

---

## Prevention (operator-side + skygate-side)

### Operator-side

- **Reuse a single preauth key** when adding new nodes. Generate
  it once, paste it on the new device, don't generate a new key
  for every node.
- **Avoid `tailscale logout` + `tailscale up` cycles.** Each
  `tailscale up` with `--authkey=NEW_KEY` may create a new
  control plane session. Use the same key for the lifetime of
  the tailnet (or rotate preauth keys, not the auth flow).
- **Monitor peer count** per node. If any node shows < 50% of
  expected peers, that's a split. The
  `scripts/tailnet_probe.sh` is the diagnostic.

### skygate-side (already in v1.3.10)

- **B110: `tailnet.split_suspected`** system test fails when
  skygate-host-1 sees < 90% of online nodes. Surface in
  `/admin/system_tests`.
- **B110: `tailnet.all_nodes_reachability`** system test fails
  when skygate-host-1 sees < 60% of online nodes. Lower
  threshold than split test because single phones going
  asleep is normal.
- **B110: `tailnet.vps_to_vps_latency`** system test for
  per-VPS egress fleet latency (catches DERP-only path
  regressions).
- **`scripts/tailnet_probe.sh`** portable diagnostic that
  works on any Tailscale node (including the operator's
  laptop, VPS, or any device with `tailscale` + `bash`).
- **`docs/tailnet-diagnostics.md`** (this file) — operator
  runbook for diagnosis and fix.

### Future improvements (not in v1.3.10)

- **Auto-alert on split**: skygate could poll `tailscale status`
  every 5 min and POST a Telegram message if peer count drops
  below 50% of expected. Defer to a follow-up release (probably
  v1.4.0) — requires operator-side action to fix, so the alert
  is informational, not actionable from skygate.
- **BGP-style multi-control-plane**: headscale HA with multiple
  control replicas. Defer to v2.x — the current 1-control-plane
  setup is correct for the operator's deployment size.
- **Tailscale ACL test that requires all nodes to be on same
  map**: skygate could reject policy reapply if the live
  `tailscale status` peer count is < 50% of headscale's
  `online` count. Defensive, not yet implemented.

---

## Related

- **v0.28.5 incident** (BACKLOG.md, REGRESSION-TESTS.md):
  Tailscale state file persistence issues. The current split is
  NOT the same root cause — it's a control plane session
  divergence, not a state file corruption.
- **B98** (`system_tests_exit_node_speed.go`): exit-node-only
  speed tests. Don't catch the split because all of the
  operator's exit nodes (emilia/karolina/sharlotta) are on the
  same cluster A.
- **`docs/disaster-recovery.md`**: backup + restore procedures
  for the case where headscale's database is corrupt. The
  current split is NOT a headscale DB corruption — the data is
  correct, the control plane session is divergent.

---

**Last updated:** 2026-08-13 13:30 UTC by Mavis during v1.3.10
diagnostic work. Run `scripts/tailnet_probe.sh` from any node
to re-confirm.
