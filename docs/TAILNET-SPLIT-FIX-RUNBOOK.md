# TAILNET SPLIT FIX — operator runbook (v1.3.10, 2026-08-13)

**Status:** v1.3.10 deployed (commit `7dc975d`). skygate-host-1
already re-authed with the new preauth key. **14 nodes still
need re-auth** (4 VPS + 10 home devices).

---

## TL;DR

```bash
# 1. Get the preauth key (one-time, valid 24h)
ssh skyadmin@192.168.13.69 "docker exec headscale headscale preauthkeys create --user 1 --reusable --expiration 24h"
# → hskey-auth-XXXX (save this)

# 2. For EACH node (VPS first, then home), run:
ssh <user>@<node> "curl -sL https://raw.githubusercontent.com/BarsSky/skygate/main/scripts/fix_tailnet_split.sh -o /tmp/fix.sh && PREAUTH_KEY=hskey-auth-XXXX bash /tmp/fix.sh"
# Copy the printed `sudo tailscale up ...` command, run it on the node.

# 3. Verify (from skygate-vm):
docker exec skygate-skygate-1 bash /app/scripts/tailnet_probe.sh
# Expected: "16/16 peers reachable" + NO "DIAGNOSIS: TAILNET SPLIT LIKELY"

# 4. Revoke the key (cleanup)
ssh skyadmin@192.168.13.69 "docker exec headscale headscale preauthkeys list -o json | python3 -c 'import json,sys; d=json.load(sys.stdin); [print(p[\"id\"]) for p in d if p[\"used\"] and p[\"reusable\"]]' | xargs -I{} docker exec headscale headscale preauthkeys expire --user 1 --key {}"
```

---

## Per-device checklist (17 nodes total)

### Already done (in this commit, automated)
- [x] **skygate-host-1** (100.64.0.18) — re-authed via VM SSH

### VPS nodes (4) — operator SSHs from laptop
- [ ] **emilia** (100.64.0.3, VPS 213.176.92.205)
- [ ] **karolina** (100.64.0.2, headscale alias for svyatoslava-1, VPS 193.233.130.178)
- [ ] **sharlotta** (100.64.0.4, VPS)
- [ ] **svyatoslava-1** (100.64.0.15, if separate host from karolina)

### Home devices (10 online + 3 offline) — operator does manually
- [ ] **skyworker** (100.64.0.1, online, home desktop)
- [ ] **skybars** (100.64.0.5, online, home desktop)
- [ ] **a71** (100.64.0.19, online, home android)
- [ ] **olesya** (100.64.0.16, online, home android)
- [ ] **nothing-phone-2** (100.64.0.6, online, mobile)
- [ ] **base** (100.64.0.7, OFFLINE — turn it on first)
- [ ] **skybars-1** (100.64.0.8, OFFLINE)
- [ ] **desktop-cuo0tfb** (100.64.0.9, OFFLINE)
- [ ] **msi** (100.64.0.11, OFFLINE)
- [ ] **svyatoslava-legacy** (100.64.0.12, OFFLINE)
- [ ] **cyborg** (100.64.0.13, OFFLINE)
- [ ] **basic** (100.64.0.14, OFFLINE)
- [ ] **cyborg** (100.64.0.13, OFFLINE)

Total to-do: **13 home devices** + **3 VPS** (if karolina≠svyatoslava-1) = 16 manual actions.

---

## Step-by-step on each node

### 1. SSH to the node
```bash
ssh <user>@<node-ip-or-name>
```

### 2. Run the fix script
```bash
# Download the script (or scp it from /home/skyadmin/skygate on skygate-vm)
curl -sL https://raw.githubusercontent.com/BarsSky/skygate/main/scripts/fix_tailnet_split.sh -o /tmp/fix.sh

# Set the preauth key (paste yours)
export PREAUTH_KEY=hskey-auth-q7tFybdREJ52-RlFF6nFS_v8WGRax-jvaTGBOcU3U4zjkTIC8vbKfooblE2u4E24ECj1U_R5gGHDU

# Run it (no sudo needed for the script itself)
bash /tmp/fix.sh
```

The script prints:
```
sudo tailscale up \
    --login-server=https://head.skynas.ru \
    --hostname=<this-node> \
    --advertise-tags=<its-tags> \
    --auth-key=hskey-auth-XXXX
```

### 3. Run the printed command
```bash
sudo bash -c "tailscale up --login-server=https://head.skynas.ru --hostname=<this-node> --advertise-tags=<its-tags> --auth-key=hskey-auth-XXXX"
```

Or just copy-paste the exact lines.

### 4. Verify
```bash
tailscale status | wc -l
# Should be 17+ (peers + header)
```

If you see "Error: changing settings via 'tailscale up' requires
mentioning all non-default flags" — copy the EXACT command the
error message suggests and run that instead. Different nodes have
different non-default flags (e.g. `--ssh`, `--shields-up`).

---

## Order of operations (minimize downtime)

1. **VPS-side first** (4 nodes). These are the "anchors" of the
   new map. Once they re-auth, the new map starts propagating.
2. **skygate-host-1** next. Already done in this round (commit 7dc975d).
3. **Home devices** last. The home router at 192.168.13.67 is
   the source of the old session — re-authing home devices kills
   that session.
4. **Wait 60 seconds** after the last node before re-checking
   `tailnet_probe.sh`. The map needs time to propagate.

---

## Verification (after all 16 re-auth)

Run from skygate-vm:
```bash
docker exec skygate-skygate-1 bash /app/scripts/tailnet_probe.sh
```

**Expected output:**
```
emilia               100.64.0.3      tcp=OK XXms
karolina             100.64.0.2      tcp=OK XXms
sharlotta            100.64.0.4      tcp=OK XXms
skybars              100.64.0.5      tcp=OK XXms
skyworker            100.64.0.1      tcp=OK XXms
a71                  100.64.0.19     tcp=OK XXms
olesya               100.64.0.16     tcp=OK XXms
svyatoslava-1        100.64.0.15     tcp=OK XXms
nothing-phone-2      100.64.0.6      tcp=OK XXms
... (one line per online peer)

Summary: 10/10 peers reachable from 100.64.0.18
(NO "DIAGNOSIS: TAILNET SPLIT LIKELY" line)
```

Then run the new B110 system test:
```bash
# From a logged-in admin browser:
# /admin/system_tests → tailnet.split_suspected
# Expected: PASS with "10 online / 10 reachable (100%)"
```

---

## If something goes wrong

### Tailscale refused re-auth with "node exists" error
The old NodeKey from the previous session is still registered
to headscale under the same NodeID. This is normal. The new
auth will re-link the same NodeID. Wait 5-10 seconds after the
error — headscale will sync the new state automatically.

### Map still split after all 16 re-auth
This means the headscale database itself is in a divergent state.
Fix:
```bash
docker restart headscale
# After restart, the control plane reloads from disk and all
# /machine/map sessions reset. All nodes will re-poll within
# 60s and the new map will be uniform.
```

### Preauth key expired before all re-auth done
Generate a new one (24h, reusable) and continue:
```bash
docker exec headscale headscale preauthkeys create --user 1 --reusable --expiration 24h
```

---

## Cleanup (after verification)

```bash
# List all used + reusable preauth keys
docker exec headscale headscale preauthkeys list -o json

# Expire the one we just used
docker exec headscale headscale preauthkeys expire --user 1 --key <KEY_ID>
```

The preauth key self-destructs after 24h anyway, but manual
expiry is the operator's choice for hygiene.

---

**Estimated time:** 30-45 min for all 16 devices (3-5 min per device, parallelizable across multiple SSH sessions).

**Downtime:** None (each node's re-auth takes 3-5s; nodes stay reachable via DERP during the brief transition).
