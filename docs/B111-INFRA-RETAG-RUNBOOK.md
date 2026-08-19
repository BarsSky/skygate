# B111 Infra Re-Tag Runbook — операторский сценарий

**Status:** v1.3.11 (B111) deployed. Структурная часть (isInfraNode +
BackfillInfra + public-access grants) готова. Для полного фикса
нужна **Фаза 3** — re-tag 5 нод в headscale.

**Downtime:** ~5-10 мин на ноду (можно параллельно = ~5 мин total)

---

## TL;DR

```bash
# 1. Получить preauth key (24h, reusable) — re-use тот что от v1.3.10
#    или сгенерить новый. На момент написания:
#      hskey-auth-q7tFybdREJ52-RlFF6nFS_v8WGRax-jvaTGBOcU3U4zjkTIC8vbKfooblE2u4E24ECj1U_R5gGHDU

# 2. На каждой ноде выполнить:
PREAUTH_KEY=hskey-auth-... \
  bash /home/skyadmin/skygate/scripts/fix_tailnet_split.sh
# Скопировать вывод, выполнить на ноде.

# 3. После всех 5 re-auth:
docker exec skygate-skygate-1 bash /app/scripts/tailnet_probe.sh
# Expected: 10/10 peers reachable, NO "DIAGNOSIS: TAILNET SPLIT LIKELY"
```

---

## Per-node checklist (5 нод)

| # | Node | Current tag | New tag | Location |
|---|------|-------------|---------|----------|
| 1 | skygate-host-1 | `tag:dev-skyadmin-skygate-vm` | `tag:dev-infra-skygate-host-1` | skygate-vm docker container |
| 2 | emilia | `tag:dev-skyadmin-emilia,tag:exit-node,tag:private` | `tag:dev-infra-emilia,tag:exit-node,tag:private` | VPS relay |
| 3 | karolina | `tag:dev-skyadmin-karolina,tag:exit-node,tag:private` | `tag:dev-infra-karolina,tag:exit-node,tag:private` | VPS relay |
| 4 | sharlotta | `tag:dev-skyadmin-sharlotta,tag:exit-node,tag:private` | `tag:dev-infra-sharlotta,tag:exit-node,tag:private` | VPS relay |
| 5 | svyatoslava-1 | `tag:private` (only) | `tag:dev-infra-svyatoslava-1,tag:exit-node,tag:private` | VPS relay (= skygate-host-2 for HA) |

**Critical for svyatoslava-1**: this node's hostname is
`svyatoslava-1` (NOT `skygate-host-*`), so isInfraNode rule 2
(hostname prefix) does NOT match. Without `tag:exit-node`
added, isInfraNode returns false and BackfillInfra UPDATE
will not move the node to `infra`. The node would stay in
the `svyatoslava` portal-user bucket (leftover from earlier
experiments) and remain invisible to skygate-host-1.

**Why svyatoslava-1 is in the infra bucket (operator's design)**: this
is the future **skygate-host-2** (the HA partner of skygate-host-1,
per the B93/v1.3.11 design). The `svyatoslava` portal user
(id=11) is a leftover from earlier per-user-egress experiments
and is NOT needed — after the Phase 3 re-tag + BackfillInfra
UPDATE, svyatoslava-1 moves to `infra` and the `svyatoslava`
portal user becomes dormant (0 nodes). It can be left in
`portal_users` for audit history, or deleted in a follow-up
cleanup.

**Why svyatoslava-1 needs `tag:exit-node`**: skygate-host-1
needs to see this node as a potential egress target for the
Telegram bot. Without `tag:exit-node`, the
`* → tag:exit-node` catch-all doesn't include svyatoslava-1,
and skygate-host-1 can't route Telegram API traffic through
it. Adding `tag:exit-node` makes the catch-all match AND
satisfies isInfraNode rule 3 (so BackfillInfra UPDATE fires).

**NOT to re-tag** (остаются под `tag:dev-skyadmin-X` / `tag:dev-michail-X`):
- skyworker, skybars, skybars-1, a71, cyborg, desktop-cuo0tfb,
  msi, svyatoslava-legacy, base, basic (skyadmin's user devices
  + offline devices)
- nothing-phone-2, olesya (michail's user devices)

---

## Step-by-step on each node

### 1. SSH to the node
```bash
ssh <user>@<node-ip-or-name>
```

### 2. Run the fix script
```bash
# Download or scp the script
curl -sL https://raw.githubusercontent.com/BarsSky/skygate/main/scripts/fix_tailnet_split.sh -o /tmp/fix.sh

# Set the preauth key
export PREAUTH_KEY=hskey-auth-q7tFybdREJ52-RlFF6nFS_v8WGRax-jvaTGBOcU3U4zjkTIC8vbKfooblE2u4E24ECj1U_R5gGHDU

# Run it
bash /tmp/fix.sh
```

The script prints:
```
sudo tailscale up \
    --login-server=https://head.<your-domain> \
    --hostname=<this-node> \
    --advertise-tags=<its-tags> \
    --auth-key=hskey-auth-XXXX
```

### 3. Add the new infra tag to the printed command

**For each of the 5 infra nodes**, the printed command will have
`--advertise-tags=tag:dev-skyadmin-<hostname>` (or no tags for
svyatoslava-1). **Replace** with the new infra tag:

| Node | Replace | With |
|------|---------|------|
| skygate-host-1 | `--advertise-tags=tag:dev-skyadmin-skygate-vm` | `--advertise-tags=tag:dev-infra-skygate-host-1` |
| emilia | `--advertise-tags=tag:dev-skyadmin-emilia,tag:exit-node,tag:private` | `--advertise-tags=tag:dev-infra-emilia,tag:exit-node,tag:private` |
| karolina | `--advertise-tags=tag:dev-skyadmin-karolina,tag:exit-node,tag:private` | `--advertise-tags=tag:dev-infra-karolina,tag:exit-node,tag:private` |
| sharlotta | `--advertise-tags=tag:dev-skyadmin-sharlotta,tag:exit-node,tag:private` | `--advertise-tags=tag:dev-infra-sharlotta,tag:exit-node,tag:private` |
| svyatoslava-1 | `--advertise-tags=tag:private` (no exit-node tag yet — and no infra tag) | `--advertise-tags=tag:dev-infra-svyatoslava-1,tag:exit-node,tag:private` |

**The other `tag:*` tags MUST be preserved** (tag:exit-node,
tag:private) so the node still functions as an exit node.

### 4. Run the modified command

```bash
sudo bash -c "tailscale up --login-server=https://head.<your-domain> --hostname=<this-node> --advertise-tags=<NEW-TAGS> --auth-key=hskey-auth-XXXX"
```

### 5. Verify
```bash
tailscale status
# Should show the node connected to headscale
# Peer count should reflect the new tag
```

---

## Order of operations (minimize downtime)

1. **skygate-host-1 first** (1 нода, ~5 мин). Это убирает главный
   split-symptom (skygate-host-1 в mesh с остальными skyadmin).
2. **VPS-side next** (4 ноды, можно параллельно через разные
   SSH-сессии). Каждая занимает 3-5 мин.

Если делать параллельно — суммарный downtime **~5 мин**.

---

## Verification (after all 5 re-auth + policy reapply)

```bash
# 1. From skygate-vm:
docker exec skygate-skygate-1 bash /app/scripts/tailnet_probe.sh
# Expected: 10/10 peers reachable from 100.64.0.18
#           (no "DIAGNOSIS: TAILNET SPLIT LIKELY")

# 2. Trigger policy reapply (B111 design now has 5 infra devices,
#    so writePerDeviceGrants generates the per-device mesh in
#    the infra bucket for the first time):
#    - Login to /admin/exit-rules and click "Reapply policy"
#    - OR: docker exec skygate-skygate-1 <reapply CLI>

# 3. From a logged-in admin browser:
#    /admin/system_tests → tailnet.split_suspected
#    Expected: PASS with "10 online / 10 reachable (100%)"
```

---

## What changes in headscale after the re-tag

| Metric | Before B111 | After B111 (operator re-tag done) |
|--------|-------------|----------------------------------|
| skygate-host-1 headscale tag | `tag:dev-skyadmin-skygate-vm` | `tag:dev-infra-skygate-host-1` |
| emilia headscale tag | `tag:dev-skyadmin-emilia` | `tag:dev-infra-emilia` |
| karolina headscale tag | `tag:dev-skyadmin-karolina` | `tag:dev-infra-karolina` |
| sharlotta headscale tag | `tag:dev-skyadmin-sharlotta` | `tag:dev-infra-sharlotta` |
| svyatoslava-1 headscale tag | `tag:private` | `tag:dev-infra-svyatoslava-1` |
| `node_owner_map` for these 5 | mixed skyadmin/michail/svyatoslava | `username='infra'` (via BackfillInfra UPDATE on next skygate restart) |
| Policy grants for `tag:dev-infra-*` | 0 (no matching devices) | mesh (5 nodes × 4 peers) + 4 `* → tag:dev-infra-<exit>` catch-alls |

---

## If something goes wrong

### tailscale up fails with "tag not found"
The new tag `tag:dev-infra-<hostname>` must be registered with
headscale first. After BackfillInfra UPDATE (which runs on
skygate restart), the tagOwners entry is added to the policy.
Re-apply the policy, then re-run tailscale up.

### Map still shows 4 peers after re-tag
The `tailscaled` cache may be stale. Restart tailscaled:
```bash
sudo systemctl restart tailscaled
# Wait 10s
tailscale status
```

### Some peers still invisible
Check `node_owner_map` for the affected node — BackfillInfra
may not have run yet. Restart skygate container to trigger
auto-discovery:
```bash
cd /home/skyadmin/skygate
docker compose restart skygate
```

---

## Estimated time

- 5 нод × 3-5 мин = 15-25 мин sequential
- 5 мин parallel (через 5 SSH-сессий)
- Plus 5 мин verification

**Total: ~10-15 мин**
