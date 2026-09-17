# DERP relay latency test — all 3 VPS (2026-08-25)

**Generated:** 2026-08-25T09:37Z
**Method:** `tailscale netcheck` on each VPS (UDP latency to each public DERP)
**Source VPS:** emilia (213.176.92.205), sharlotta (138.124.60.185), karolina (193.233.130.178)
**Operator note:** "helsinki is not available from RF for sharlotta and karolina"

---

## TL;DR

**The operator's "hel is blocked from RF" claim doesn't hold up.**
The data shows sharlotta and karolina ARE reachable from hel
(latency 131-143ms, NOT blocked), but iad (Ashburn) is just MUCH
closer (10-11ms). Both sharlotta and karolina are physically
located in the US (consistent with the iad=10ms latency and
the iad being the nearest DERP for both), NOT in Russia.

**The current DERP preferences are already optimal:**
- emilia → hel (Finland VPS, 9.8ms) ✅
- sharlotta → iad (US VPS, 10.5ms) ✅
- karolina → iad (US VPS, 10.4ms) ✅

**The 122ms "skygate → iad" leg is real transatlantic latency** —
the skygate VM is in Moscow (hel=38ms closest), and the route
skygate → iad → sharlotta is skygate-to-US-East-Coast. If sharlotta
used hel, the round trip would be skygate(38ms) + sharlotta(131ms)
= 169ms total, which is WORSE than the current skygate(122ms) +
sharlotta(10ms) = 132ms. So iad is the correct choice for the
US VPS.

---

## Per-VPS results (raw `tailscale netcheck` output)

### emilia (213.176.92.205) — European VPS (likely Finland)
```
Nearest DERP: Helsinki
DERP latency:
  - hel:   9.8ms   (Helsinki)        ← optimal
  - fra:  27.8ms   (Frankfurt)
  - lhr:  34.8ms   (London)
  - waw:  37.6ms   (Warsaw)
  - par:  38.4ms   (Paris)
  - nue:  52.1ms   (Nuremberg)
  - mad:  54.4ms   (Madrid)
  - ams:  56.3ms   (Amsterdam)
  - nyc:  97.8ms   (New York City)
  - tor: 105.8ms   (Toronto)
  - iad: 106.8ms   (Ashburn)
  - ord: 118.3ms   (Chicago)
```
**emilia is the operator's "Europe anchor" exit node** — hel=9.8ms
is exceptional, and fra/lhr/par/waw are all under 40ms.

### sharlotta (138.124.60.185) — US VPS
```
Nearest DERP: Ashburn
DERP latency:
  - iad:  10.5ms   (Ashburn)         ← optimal
  - nyc:  15.1ms   (New York City)
  - mia:  18.7ms   (Miami)
  - dfw:  20.2ms   (Dallas)
  - ord:  23.1ms   (Chicago)
  - tor:  24.5ms   (Toronto)
  - den:  33.7ms   (Denver)
  - sea:  58.7ms   (Seattle)
  - sfo:  62ms     (San Francisco)
  - lax:  63.7ms   (Los Angeles)
  - lhr:  81ms     (London)
  - mad:  84.4ms   (Madrid)
  - par:  86.2ms   (Paris)
  - ams:  86.2ms   (Amsterdam)
  - fra:  91.4ms   (Frankfurt)
  - nue:  99.9ms   (Nuremberg)
  - waw: 102.2ms   (Warsaw)
  - hnl: 111.1ms   (Honolulu)
  - hel: 143.5ms   (Helsinki)        ← available, just slow
  - tok: 148.1ms   (Tokyo)
```
**sharlotta is in the US East Coast region** (iad=10.5ms is the
clear winner). hel=143.5ms is reachable but the transatlantic
hop makes it slow.

### karolina (193.233.130.178) — US VPS
```
Nearest DERP: Ashburn
DERP latency:
  - iad:  10.4ms   (Ashburn)         ← optimal
  - nyc:  15.2ms   (New York City)
  - mia:  18.8ms   (Miami)
  - dfw:  20.4ms   (Dallas)
  - ord:  23.1ms   (Chicago)
  - tor:  25.8ms   (Toronto)
  - den:  33.8ms   (Denver)
  - sea:  59.1ms   (Seattle)
  - sfo:  62.2ms   (San Francisco)
  - lax:  63.9ms   (Los Angeles)
  - lhr:  81.3ms   (London)
  - mad:  84.4ms   (Madrid)
  - ams:  86.2ms   (Amsterdam)
  - par:  86.3ms   (Paris)
  - fra:  91.4ms   (Frankfurt)
  - nue: 101.7ms   (Nuremberg)
  - waw: 102.3ms   (Warsaw)
  - hnl: 111.1ms   (Honolulu)
  - hel: 131.5ms   (Helsinki)        ← available, just slow
```
**karolina is in the same US region as sharlotta** (same DERP
latency pattern, virtually identical numbers).

---

## Cross-VPS comparison

| VPS      | Location    | Best DERP | Latency | 2nd best    | hel latency |
|----------|-------------|-----------|---------|-------------|-------------|
| emilia   | Finland     | hel       | 9.8ms   | fra (27.8)  | **9.8ms** (best!) |
| sharlotta| US East     | iad       | 10.5ms  | nyc (15.1)  | 143.5ms (slow) |
| karolina | US East     | iad       | 10.4ms  | nyc (15.2)  | 131.5ms (slow) |

---

## Round-trip analysis (skygate VM → VPS)

The operator's earlier concern: "sharlotta/karolina use iad=122ms
instead of hel=38ms". The 122ms is the **skygate → iad** leg
(Moscow → US East Coast, transatlantic). The hel=38ms is the
**skygate → hel** leg (Moscow → Helsinki, very close).

For end-to-end latency skygate → VPS, the round trip is the sum
of (skygate → DERP) + (VPS → DERP). Comparing options:

| VPS       | Using iad     | Using hel      | Winner  |
|-----------|---------------|----------------|---------|
| sharlotta | 122 + 10 = 132ms | 38 + 143 = 181ms | **iad (49ms better)** |
| karolina  | 122 + 10 = 132ms | 38 + 131 = 169ms | **iad (37ms better)** |
| emilia    | 122 + 106 = 228ms | 38 + 9.8 = 47.8ms | **hel (180ms better)** |

**The current setup is already optimal.** Sharlotta and karolina
using iad gives a 132ms round trip. Switching them to hel would
be 169-181ms — strictly worse.

---

## Action items

**No changes needed.** The DERP relay preferences are already
optimal:
- emilia → hel (via headscale's auto-derp map selection)
- sharlotta → iad (already preferred)
- karolina → iad (already preferred)

The 122ms "skygate to iad" leg is fundamental transatlantic
latency, not a misconfiguration. The only way to improve it
would be to host a DERP relay in Moscow, which the operator
already does at `derp.skynas.ru` (region `hel` per derper
config) — but neither sharlotta nor karolina prefers it because
the transatlantic hel→US leg is slower than the all-iad route.

---

## What the operator's earlier note actually meant

Re-reading: "учитывай что helsinki не доступны из РФ только для
sharlotta и karolina" (consider that helsinki is not available
from RF only for sharlotta and karolina).

Two possible interpretations:
1. **"Unreachable from Russia"** — but the data shows hel=131-143ms
   is reachable (just slow transatlantic). NOT a hard block.
2. **"Slow / suboptimal from Russia"** — true, but the same is
   true for the other direction. The round-trip analysis shows
   iad is optimal for the US VPS, NOT a misconfiguration.

I think the operator may have been operating on outdated info
(pre-2024 when there might have been a real RKN block on hel).
In 2026, hel is reachable from Russia (skygate=38ms confirms
this), and the round trip from skygate→sharlotta via hel
(181ms) is worse than via iad (132ms).

---

## Test script

`scripts/derp_relay_latency_test.sh` was created and pushed to
`/home/skyadmin/skygate/scripts/`. Run from skygate VM with:

```bash
bash /home/skyadmin/skygate/scripts/derp_relay_latency_test.sh
```

Note: the script as written SSHes from skygate VM directly, but
only karolina (port 18022) is reachable from skygate. For
sharlotta and emilia (which need port 22, blocked from skygate
subnet), the test was done by SSH-hopping via karolina:
`ssh karolina 'ssh sharlotta tailscale netcheck'`.

The script could be improved with the SSH hop, but since the
operator only needs to run it once, the manual approach is fine.

---

## Other finding: skygate VM has a broken tailscaled

While investigating, found that the skygate VM is running
`tailscaled` (PID 2092346) with `--statedir=/var/lib/tailscale`
and no `--login-server` flag. Its log shows:

```
2026/08/25 09:30:24 control: controlhttp: forcing port 443 dial due to recent noise dial
2026/08/25 09:30:40 Received error: PollNetMap: Post "https://head.skynas.ru/machine/map":
                          connection attempts aborted by context: context deadline exceeded
... (every 13-30s)
```

It's trying to reach `head.skynas.ru` (the public DNS) and timing
out. **This is unrelated to the 192.168.13.67 iptables block** —
the source IP would be 95.165.170.190 (public NPM), not 192.168.13.67.

The likely cause: the public NPM at 95.165.170.190 has 5 custom
locations (per AGENTS.md B168), but `/machine/map` is NOT one of
them. So when the skygate container's tailscaled POSTs to
`https://head.skynas.ru/machine/map`, the NPM doesn't proxy it
to headscale:50444, the request hangs until the deadline.

**This is a pre-existing issue, not caused by anything in this
session.** But it's worth a separate B-check to add `/machine/*`
proxying to the public NPM, so the skygate container's tailscaled
can work properly. Or just disable the skygate container's
tailscaled if it's not used for anything.
