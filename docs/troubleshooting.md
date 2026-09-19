# Troubleshooting — symptom-first diagnostic playbook

This playbook is the consolidated diagnostic guide for Skygate. It is built from
real investigations that were removed in the **2026-09-18 documentation
restructure** (`tailnet-diagnostics.md`, `node-404-investigation.md` v1/v2,
`derp-latency-report.md`, `headscale-duplicate-name-behavior.md`,
`slow-exit-node.md`, `tailnet-fixes.md`, the failure sections of the Telegram
relay runbook, and the install dry-run report); the full original text stays in git
history. It is organised **symptom first** — find the heading that matches what the
operator sees. Design and procedures live in [`networking.md`](networking.md); ACL
syntax lives in [`acl-rules-reference.md`](acl-rules-reference.md).

## Placeholder legend

| Placeholder | Meaning |
|---|---|
| `<VM_HOST>` | host running the skygate container / its `tailscaled` |
| `<PROXY_HOST>` | reverse proxy (Nginx Proxy Manager) host in front of skygate + headscale |
| `<EXIT_NODE>` | a host advertising `0.0.0.0/0,::/0` |
| `<RELAY_HOST>` | a host advertising specific CIDRs |
| `<USER_SUBNET>` | the per-user subnet, shaped `10.0.<uid>.0/24` |
| `<TAILNET_IP>` | a node's tailnet address (shared-address range) |
| `head.example.com`, `derp.example.com`, `skygate.example.com`, `tsnet.example.com` | control plane / relay / portal / tailnet base domain |
| `192.0.2.0/24`, `198.51.100.0/24`, `203.0.113.0/24` | RFC 5737 documentation addresses |

---

## Table of contents

1. [All Tailscale devices show `offline` / `last_seen` frozen](#1-all-tailscale-devices-show-offline--last_seen-frozen)
2. [headscale returns `node not found` (404) for an orphan client](#2-headscale-returns-node-not-found-404-for-an-orphan-client)
3. [Device stuck with no dev-tag, `tagged-devices`, or uppercase-tag rejection](#3-device-stuck-with-no-dev-tag-tagged-devices-or-uppercase-tag-rejection)
4. [Exit node slow or unreachable](#4-exit-node-slow-or-unreachable)
5. [DERP relay not used / high latency / relay reports stopped while running](#5-derp-relay-not-used--high-latency--relay-reports-stopped-while-running)
6. [Duplicate node names in headscale](#6-duplicate-node-names-in-headscale)
7. [The UI shows stale or contradictory state after a change](#7-the-ui-shows-stale-or-contradictory-state-after-a-change)
8. [Install / upgrade-time failures](#8-install--upgrade-time-failures)
   * [8.0 Self-update rolled back: "healthz did not report build …" (B268)](#80-self-update-rolled-back-healthz-did-not-report-build--b268)
   * [8.0.1 The unit is `active` but nothing listens on the port (B269)](#801-the-unit-is-active-but-nothing-listens-on-the-port-b269)
   * [8.0.2 The update rolls back every time even though the service is healthy (B270)](#802-the-update-rolls-back-every-time-even-though-the-service-is-healthy-b270)
   * [8.0.3 The journal repeats `SQL logic error: no such function: pg_…` every 30 s (B271)](#803-the-journal-repeats-sql-logic-error-no-such-function-pg-every-30-s-b271)
   * [8.0.4 Devices show a tag that headscale does not have (B272)](#804-devices-show-a-tag-that-headscale-does-not-have-b272)
   * [8.0.5 `git checkout` blocks the image update on an untracked file (B272.4)](#805-git-checkout-blocks-the-image-update-on-an-untracked-file-b2724)
9. [Telegram relay silently not delivering](#9-telegram-relay-silently-not-delivering)
10. [General diagnostics kit](#10-general-diagnostics-kit)

---

## 1. All Tailscale devices show `offline` / `last_seen` frozen

### TL;DR

When **every** device flips to `online=false` at the same moment and `last_seen`
freezes at that instant, the control plane is down — not the devices. The three
classes are: (a) a firewall rule on the skygate host blocking the reverse proxy's
legitimate path to headscale, (b) headscale itself being down or unreachable,
(c) the reverse-proxy → headscale route (`/machine/map`) not being proxied. A
slower-burning variant (peer-count mismatch while `last_seen` still advances) is
**policy isolation** and is fixed in the ACL, not the firewall.

### Symptom

Operator-visible report, verbatim: "почему все устройства offline в чем конкретно
причина бага?" — *why are all devices offline?*

- `/admin/devices`, `/my/devices` and `/admin/exit-nodes` show **every** node as
  offline; `last_seen` is frozen at one timestamp and never advances.
- `headscale nodes list -o json` reports `online=false` for all nodes while the
  nodes themselves are up and running Tailscale (`online=0, state=offline` for
  every exit node at once).
- Peer-count variant: `docker exec skygate tailscale status` lists fewer peers than
  headscale reports as online (e.g. 4 visible vs 10 online).

### Root cause analysis

**Class A — firewall / `DOCKER-USER` rule (most common).** A broad rule added to
silence `node not found` noise (see [§2](#2-headscale-returns-node-not-found-404-for-an-orphan-client))
also blocked the **legitimate** proxy→headscale traffic:

```bash
iptables -I DOCKER-USER 1 -s <PROXY_HOST> -p tcp --dport 50444 -j DROP
```

The proxy then returns `504` to every client fetching `/key` or pushing
`/machine/map`, so no client can update `last_seen` and all devices freeze at the
instant the rule was applied. A rule in `INPUT` has the same effect. `DOCKER-USER`
is the classic trap: it is evaluated for traffic entering through the Docker bridge,
which is exactly how the proxy reaches headscale.

**Class B — headscale reachability.** headscale is stopped, crashed, or out of
disk/WAL space. A `docker exec` CLI call hangs and `curl` to `:50444` returns
`000`/timeout instead of `401` (a `401` proves the listener is alive).

**Class C — proxy → headscale path.** If `/machine/*` is not among the proxy's
forwarded locations, a client's `POST /machine/map` hangs until the context
deadline (`PollNetMap: … connection attempts aborted by context: context deadline
exceeded`) — including skygate's own in-container `tailscaled`. In the proxy error
log: `upstream timed out (110: Connection timed out)` = app slow/hung;
`connect() failed (111: Connection refused)` = process dead;
`SSL_do_handshake() failed (wrong version number)` = HTTPS sent to an HTTP backend.

**Class D — policy isolation.** Devices are online in headscale but a node cannot
see its peers because its tag has no grants — typically a node moved to an "infra"
bucket while the other exit nodes stayed in a user bucket, so the infra tag has no
`dst` peers and the per-device mesh is skipped. A peer-count mismatch, not a frozen
`last_seen`.

### Fix procedure

1. **Confirm the class** (three commands):

   ```bash
   # Expect 401, not 000/504:
   curl -sS -o /dev/null -w '%{http_code}\n' --max-time 5 \
     -H 'Authorization: Bearer test' http://127.0.0.1:50444/api/v1/node
   # Over-broad rule present?
   sudo iptables -L DOCKER-USER -n -v; sudo iptables -L INPUT -n -v
   sudo grep -nE 'DOCKER-USER.*<PROXY_HOST>.*DROP' /etc/iptables/rules.v4
   # Proxy failing?
   docker logs headscale --tail 100 | grep -E 'status=(504|000)|node not found'
   ```

2. **Class A** — delete the over-broad rules and persist:

   ```bash
   sudo iptables -D DOCKER-USER -s <PROXY_HOST> -p tcp --dport 50444 -j DROP
   sudo iptables -D INPUT       -s <PROXY_HOST> -p tcp --dport 50444 -j DROP
   sudo netfilter-persistent save      # or: iptables-save > /etc/iptables/rules.v4
   ```

   Re-check that headscale answers `401`; clients recover on their next poll.

3. **Class B** — restart headscale (`docker restart headscale` /
   `systemctl restart headscale`, per install kind), then re-run step 1.

4. **Class C** — add `/machine/*` (and `/key`, `/ts2021`) to the proxy's locations
   for the headscale backend, then restart the proxy.

5. **Class D** — inspect the policy for that tag and re-apply:

   ```bash
   docker exec headscale headscale policy get | python3 -c "
   import sys, json
   d = json.load(sys.stdin)
   for t in ['tag:dev-infra-<EXIT_NODE>', 'tag:exit-node']:
       print(t, 'grants:', len([x for x in d.get('grants', []) if t in (x.get('src') or [])]))"
   # After adjusting grants in /admin/acls:
   POST /admin/exit-rules/reapply
   ```

   If a node should be infra-class but still carries a per-user tag, re-tag it
   (Tailscale cannot change tags without re-registration:
   `tailscale up --authkey=… --advertise-tags=…`, brief per-node outage).

6. **Verify**: count online nodes via `headscale nodes list -o json`, then load
   `/admin/devices` and confirm `last_seen` advances.

### Prevention

- **Never block a source IP from headscale to silence log noise.** Fix the noise at
  its source (see [§2](#2-headscale-returns-node-not-found-404-for-an-orphan-client)).
- **Audit `DOCKER-USER` and `INPUT`** after any firewall change or deploy, and keep
  a persisted `/etc/iptables/rules.v4` you can grep for the proxy host.
- **Treat the reverse proxy as part of the control plane**: any change to its
  locations must preserve `/machine/*`, `/key`, `/ts2021`.
- **Prevent policy isolation** by keeping tags consistent — every
  `tag:dev-infra-*` node needs a matching grant set, and `tagOwners` must include
  the exit-node and subnet-router tags before nodes are tagged with them.
- **Regression guard**: `scripts/check_b179.sh` pins that no `DOCKER-USER`/`INPUT`
  block for the proxy host exists and that headscale still answers on `:50444`.

---

## 2. headscale returns `node not found` (404) for an orphan client

### Symptom

`docker logs headscale` repeats every 20–40 s:

```
ERR user msg: node not found code=404
INF http request bytes=15 elapsed=0.2ms method=POST path=/machine/map proto=HTTP/2.0 remote=<PROXY_HOST>:44726 status=404
```

`HEAD /machine/ping-response` may appear every ~9 s from varying ephemeral ports.
Operator wording: "на <PROXY_HOST> нет tailscale клиента" — *there is no Tailscale
client on that host*.

### What it actually means

A process on `<PROXY_HOST>` speaks the Tailscale control protocol with a **stale
NodeKey** headscale no longer recognises. Tell-tale signs:

- The 404 comes from a **fixed** source port (one long-lived HTTP/2 connection),
  while a second connection from the same host returns `200` in 100–700 ms — i.e.
  **two** Tailscale clients (or one client with two node keys): one valid, one dead.
- The 404 body is tiny (`bytes=15`), far too small for a real `MapRequest`
  (50–200+ bytes) — a degraded or custom client retrying with a dead key.
- The host has no entry in `headscale nodes list`.

It is **not** a skygate bug and **not** a headscale bug. headscale is correctly
rejecting an invalid key. The impact is noise plus CPU; it becomes an outage only if
someone "fixes" it with an over-broad firewall rule — exactly how
[§1](#1-all-tailscale-devices-show-offline--last_seen-frozen) starts.

### Confirm it

```bash
# 1. Fixed port = one stale long-lived stream.
docker logs headscale --tail 100 | grep -A2 'node not found'
# 2. Both 200 and 404 from the same host?
docker logs headscale --tail 200 | grep '<PROXY_HOST>' | tail -20
# 3. Not a registered node?
docker exec headscale headscale nodes list -o json | \
  python3 -c "import sys,json;print([n['name'] for n in json.load(sys.stdin)])"
```

### Fix

Pick one, in order of cleanliness:

1. **Re-auth the client** (proper fix), on the offending host:
   `sudo tailscale logout && sudo tailscale up --login-server=https://head.example.com`.
   The stale connection dies and the 404s stop.
2. **Find and kill the offending process** — look on the proxy host for a custom
   app holding a cached NodeKey in a config file or database.
3. **Block the stale client** (also blocks the valid one on the same host, so only
   when neither is wanted):
   ```bash
   iptables -I INPUT -s <PROXY_HOST> -p tcp --dport 50444 -j DROP
   netfilter-persistent save
   ```
   Revisit this rule whenever the host is added to the tailnet.
4. **Ignore it** — the key is invalid, no active node is affected, and the cost is a
   few log lines per minute.

### Permanent guard

Do not let log noise drive firewall changes. Give the host a valid registration if
it should run Tailscale; otherwise remove the process holding the stale key. Track
every source-IP block and re-test the legitimate path (`curl` for `401`) after any
iptables change.

---

## 3. Device stuck with no dev-tag, `tagged-devices`, or uppercase-tag rejection

### Symptom

- `/my/devices` shows "⏳ pending" indefinitely while the device otherwise works.
- The device has no `tag:dev-<user>-<device>`, so per-device ACL grants never match
  and it cannot use any exit node.
- headscale rejects a tag operation: `Error: tag should be lowercase`, or
  `InvalidArgument: requested tags are invalid or not permitted`.

### What it actually means

- **Uppercase rejection**: headscale 0.29 rejects uppercase tags. Skygate derives
  the tag from the hostname (`tag:dev-<user>-<hostname>`), so a mixed-case hostname
  produced a mixed-case tag and every `nodes tag` failed with
  `Error: tag should be lowercase`. The fix lowercases at every construction site.
- **`tagged-devices`**: headscale moves any tagged node into the synthetic
  `tagged-devices` user regardless of creator, so code joining on
  `n.UserName == <portal user>` misses OIDC/tagged nodes and the auto-tagger never
  applies the dev-tag — "pending" forever.
- **No dev-tag after a rename**: the autoupdater renamed a node (e.g.
  `<device-old>` → `<device-new>`) and did `UntagNode(old)` **before**
  `AddTag(new)`. headscale rejected the new tag (never whitelisted in `tagOwners`)
  and the old tag was already gone, leaving the node with **no** dev-tag and the DB
  row out of sync. **B177** is the defensive fix: `AddTag(new)` runs first and
  `UntagNode(old)` only fires on success, with the `node_owner_map` update inside
  the `AddTag` success branch; the warning now says "keeping existing tags as
  fallback". **B176** (lowercases the dev-tag at all six construction sites) and
  **B175** (OIDC matching, tags OIDC nodes on the first tick) are its siblings.

### Confirm it

```bash
# 1. What headscale actually has:
docker exec headscale headscale nodes list -o json | python3 -c "
import sys, json
for n in json.load(sys.stdin):
    if '<device>' in n.get('name','').lower():
        print(n['id'], n.get('name'), n.get('givenName'), n.get('user',{}).get('name'), n.get('tags'))"
# 2. What skygate believes:
sqlite3 /var/lib/skygate/skygate.db \
  "SELECT node_id, username, hostname, tag FROM node_owner_map WHERE hostname LIKE '%<device>%';"
# 3. Was the tag rejected?
docker logs skygate-skygate-1 --since 30m 2>&1 | grep -Ei 'tag should be lowercase|invalid or not permitted|keeping existing tags'
```

### Fix

1. **Whitelist the tag pattern** in `/admin/acls` → *tagOwners* before tagging:
   `tag:dev-<user>-*` must be owned by `<user>@tsnet.example.com`. A brand-new
   `tag:dev-*` never seen in `tagOwners` is rejected on first use even if the pattern
   looks right — precisely what **B177** hardens against, since newer builds add the
   new tag first and keep the old one as a fallback instead of stripping the node.
2. **Re-apply the missing tag manually** (headscale does not re-tag existing nodes
   when a pattern changes):
   ```bash
   docker exec headscale headscale nodes tag --force <NODE_ID> tag:dev-<user>-<hostname>
   ```
   Then confirm `node_owner_map` matches, or let the next autoupdater tick repair it.
3. **Legacy nodes already on `tagged-devices` with no live dev-tag are skipped by all
   backfill strategies** — apply the tag manually as above. New registrations are
   covered (the OIDC strategy tags on the first tick, before the node can transition
   to `tagged-devices`).
4. **Uppercase hostname**: rename or re-tag with the lowercase tag; do not create the
   uppercase tag.

### Permanent guard

Keep dev-tags lowercase everywhere and never build them from raw hostnames. Never
remove an old tag before the new one is confirmed added. Keep `tagOwners` ahead of
tag creation. Regression guards pin all three rules; legacy `tagged-devices` orphans
remain a documented manual step.

---

## 4. Exit node slow or unreachable

### Symptom

A client through an exit node is slow, or the exit node is not used at all:
specific destinations (e.g. YouTube) fail on some devices while other rules work;
`/admin/exit-nodes` shows a reachable node as `offline`; `tailscale status` shows
`relay: <region>` instead of a direct path; the connection "keeps trying the wrong
exit node" despite a preferred-exit setting.

### What it actually means

Latency/behaviour comes from four layers; skygate controls three:

| Layer | Component | Controlled by |
|---|---|---|
| 1 | ACL policy (`SetPolicy`) | skygate |
| 2 | Tag autoupdater (dev-tags) | skygate |
| 3 | Direct vs DERP P2P path | Tailscale + network (**not** skygate) |
| 4 | Advertised-routes sync (SSH → relay → approve) | skygate |

Four root causes, most likely first:

1. **Stale preferred exit node** — `device_exit_node_prefs` points at a renamed host
   or a deleted exit node; tailscaled asks for a node that no longer exists and
   falls back. The reconciler fixes it only on its tick (up to
   `SKYGATE_PREFERRED_RECONCILE_INTERVAL`, default **1h**).
2. **Stale domain/IP rules** — a `target_type='domain'` rule becomes concrete `/32`
   rows only on the `SKYGATE_DNS_AUTO_CHECK` tick (default **5m**), after which a
   staggered route sync pushes them. In between, the ACL allows the destination but
   no exit node knows the IP.
3. **DERP fallback on the client network** — CGNAT/mobile often cannot establish
   direct P2P; `tailscale status` reports `relay: <region>` and latency is a
   function of DERP proximity, not a skygate setting.
4. **New device without a dev-tag** — the per-device grant
   `src=tag:dev-<user>-<device>` does not match until the tag autoupdater runs
   (default every `SKYGATE_NODE_DISCOVERY_INTERVAL`, 5 min).

### Confirm it

```bash
# 1. Stale preferred exit node?
SELECT u.username, p.device_hostname, p.exit_node_tag, p.via_enabled
FROM device_exit_node_prefs p JOIN portal_users u ON u.id = p.user_id
WHERE u.username = '<user>';
docker exec headscale headscale nodes list -o json | \
  python3 -c "import sys,json;print([n.get('givenName') for n in json.load(sys.stdin)])"
# 2. Fresh domain /32 rows vs the relay's advertised routes?
SELECT id, target_value, parent_domain, exit_node_id FROM device_rules
WHERE enabled=1 AND target_type IN ('domain','subnet') AND parent_domain IS NOT NULL;
# 3. On the client: direct or relayed?
tailscale status                        # `relay: <region>` on the exit-node line
tailscale ping <EXIT_NODE_TAILNET_IP>   # >200 ms ⇒ network path is the bottleneck
```

### Fix

- **Stale pref** — `skygate headscale-users-reconcile --user <user>` (or the admin
  page), and/or re-set the preferred exit node in `/my/devices`. If it recurs, lower
  `SKYGATE_PREFERRED_RECONCILE_INTERVAL` to `2m`–`5m` in `.env` and restart skygate
  (cheap — the headscale API is local).
- **Stale DNS rules** — `POST /admin/exit-rules/sync` forces an immediate
  advertised-routes + approve cycle; if speed improves right after, this was it.
- **DERP fallback** — confirm with `tailscale netcheck` on both ends, then choose a
  relay minimising `(client → DERP) + (exit node → DERP)`. The nearest relay to one
  side is often wrong for the other; measure, do not assume.
- **New device** — wait one discovery tick or force the backfill.
- **Client flags** — the client must run with `--accept-routes` (and select the exit
  node with `--exit-node=<node>`); without `--accept-routes` a subnet route is never
  installed, so the traffic silently takes the LAN gateway instead.
- **MTU / MSS stalls** — `tailscale ping` succeeds but large TCP transfers hang:
  small packets pass, large ones are dropped. Typical on exit nodes behind another
  tunnel or a reduced-MTU uplink. Confirm with `ping -M do -s 1400 <dst>` versus a
  small ping, then clamp MSS
  (`iptables -t mangle -A FORWARD -p tcp --tcp-flags SYN,RST SYN -j TCPMSS
  --clamp-mss-to-pmtu`) or lower the path MTU.
- **Node offline while healthy** — the `last_seen` trap: an idle exit node has stale
  `last_seen` but headscale still says `Online=true`. Trust `n.Online`; consult
  `SKYGATE_EXIT_NODE_OFFLINE_AFTER` (default `10m`) only when headscale says offline.
- **SSH route sync failing** — e.g.
  `Не удалось применить маршруты: ssh <RELAY_HOST> (key …): ssh: Could not resolve
  hostname <RELAY_HOST>: Name or service not known` (*failed to apply routes*), or
  `SetAdvertisedRoutes(...): no ssh_key_path provided`. Fix
  `exit_servers.ssh_target` / `ssh_key_path` on `/admin/exit-nodes`, or set
  `SKYGATE_EXIT_SSH_KEY`. `ssh` on the command line does **not** accept `host:port` —
  use `user@host` plus a separate port.

### Permanent guard

`SKYGATE_ACL_VIA_ENABLED=true` must be set, or re-applied grants carry no `via:`
clause and every preferred-exit setting is decorative. Keep
`exit_servers.ssh_target` + `ssh_key_path` filled in and testable via the per-row
*Re-sync*. Document the four intervals and their freshness trade-off (see
[`networking.md`](networking.md)). Never override headscale's `Online` with a
`last_seen` heuristic.

---

## 5. DERP relay not used / high latency / relay reports stopped while running

### Symptom

- `/admin/derp` shows `DERPER-SERVICE: stopped`, `:8443`, `:3478 closed`,
  `0 active connections` while the derper is demonstrably running on `:443` with a
  valid certificate, a working WebSocket upgrade (`101 Switching Protocols`) and
  STUN listening on `:3478/udp`.
- Clients relay through a far region (`iad`) instead of the nearby one (`hel`).
- The `Public IP` field shows a bridge address instead of the address clients dial.

### What it actually means

- **`stopped` while running, case 1 — wrong URL/scheme.** The collector used a
  hard-coded RFC 5737 address and plain HTTP, so probes silently failed
  (`Client sent an HTTP request to an HTTPS server`). The fix builds
  `https://<host>:<port>` with a TLS-aware client.
- **Case 2 — debug endpoint disabled.** `--debug` is disabled in production
  (`/active-conn`, `/all-recent` leak client info), so `/debug/vars` returns `403`
  + plain text, parsing fails, and `Running` stays false. The liveness fallback is a
  WebSocket upgrade probe against `/derp`, returning `101 Switching Protocols` for
  any functional derper regardless of `--debug`.
- **Case 3 — container DNS.** The host resolver maps the derper's own hostname to
  `127.0.0.1`; the container inherits it and probes its own loopback.
- **Port flip on refresh** — duplicate `is_bundled=1` rows plus a non-deterministic
  `LIMIT 1`; the page bounces between `:443` and `:8443`.
- **Wrong port** — a stale `DERP_HTTP_PORT` in `.env` shadows the DB-derived port
  (the pre-v1.5.8 default `8443` is the usual culprit).
- **High latency** — relay choice is the sum of both ends' distance to the relay; a
  US VPS using a European relay is worse than using the US relay even when the
  skygate VM is much closer to Europe. Physics, not misconfiguration.
- **Wrong `Public IP`** — the egress fallback (UDP-dial local address = the
  container's Docker-bridge source) was used instead of the hostname's DNS A record.

### Confirm it

```bash
# 1. Is the derper serving? Expect 101 (WebSocket) and STUN listening.
curl -sS -o /dev/null -w 'https=%{http_code}\n' https://derp.example.com/
curl -sS -i --max-time 5 -H 'Connection: Upgrade' -H 'Upgrade: websocket' \
  -H 'Sec-WebSocket-Version: 13' -H 'Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==' \
  https://derp.example.com/derp | head -1
ss -lunp | grep 3478
# 2. Container DNS: does it resolve to loopback?
docker exec skygate getent hosts derp.example.com
docker exec skygate curl -sS -o /dev/null -w '%{http_code}\n' https://derp.example.com/
# 3. Stale env port / duplicate bundled rows?
grep -n '^DERP_HTTP_PORT=' .env
SELECT id, url, is_bundled, enabled FROM derp_relays WHERE is_bundled=1 ORDER BY id;
# 4. Measured latency:
tailscale netcheck
```

### Fix

- **Container DNS** — pin the hostname in the skygate service's `extra_hosts` with
  an operator-managed `.env` value, then **recreate** from the project directory:
  ```yaml
  services:
    skygate:
      extra_hosts:
        - "derp.example.com:${SKYGATE_DERP_PROBE_HOST:-127.0.0.1}"
  ```
  ```bash
  cd <project dir>
  sudo docker compose up -d --force-recreate --no-deps skygate
  ```
- **Stale env port** — remove/correct `DERP_HTTP_PORT` in `.env` (the DB row is
  authoritative), then restart skygate.
- **Duplicate bundled rows** — delete the extra `is_bundled=1` rows; resolution is
  `ORDER BY id ASC LIMIT 1` and the `AddDerpRelay` guard prevents new duplicates.
- **Debug disabled** — upgrade to a build with the `/derp` WebSocket liveness probe;
  with `--debug` you additionally get the STUN/connections/bytes panels.
- **Wrong `Public IP`** — set `SKYGATE_DERP_HOSTNAME=derp.example.com`; the resolver
  then uses the DNS A record and annotates the source (`dns:env` / `dns:derper` /
  `egress`).
- **High latency** — measure both legs and choose accordingly; prefer the next-best
  measurable region rather than assuming a hard block.
- **Re-apply the relay list to headscale** —
  `POST /admin/derp/relays/apply-headscale` (idempotent, audited).

### Confirmed live case (B265, 2026-09-19) — `:3478 closed` on a healthy relay

The reference VM showed a red `STUN UDP :3478 closed` tile while STUN was
provably fine (`*:3478` bound, `stun.counter_requests.success = 33709`, a remote
VPS measuring the relay via `netcheck`, clients showing `relay "mow"`).

Root cause: pre-B265 `STUNListening` had exactly one source — the
`stun.counter_requests.success` counter from `GET /debug/vars` — and upstream
derper answers every `/debug/*` request from a non-loopback, non-Tailscale
source with `403 debug access denied` (`tsweb.AllowDebugAccess`). The skygate
container is always such a source. The old comment claimed the zeros were
"honest"; they were a false negative.

```bash
# what the page used (from the skygate container):
docker exec skygate sh -c 'wget -qO- https://derp.example.com/debug/vars' \
  # → 403 debug access denied
# a real STUN round trip (B265 does this in-process, UDP):
#   STUN Binding Request (RFC 5389) → Binding Success Response with the
#   matching transaction id. It works regardless of derper's --debug flag.
```

B265 replaced the counter read with that UDP round trip, so the tile now
reflects reachability from the skygate path, prints the RTT and reflexive
address, and names the failure reason when it is red. The same 403 also means
the traffic/client/byte panels are unavailable on a hardened (no `--debug`)
derper — the page now says so instead of drawing zeros.

### Confirmed live case (B265) — dashboard "Recommended DERP" points at the wrong region

`/admin/derp/dashboard` printed `Recommended DERP: region 901
(controlplane.tailscale.com)` and the main-page hero showed 108 ms while
`derp_health` measured the operator's own relay (region 900, `derp.example.com`)
at 12 ms. Two defects:

1. `internal/derphealth/map.go` had the ownership mapping **inverted**
   (`d.IsOwn = isBundled == 0`). `is_bundled = 1` is the operator's own local
   derper (`EnsureBundledDerpRelay`); `is_bundled = 0` is an external row —
   including the region-901 row that `AutoMigrateDerpRelays` created from the
   legacy `derp.external_urls` value, which is a **derpmap document**
   (`https://controlplane.tailscale.com/derpmap/default`), not a relay. The
   dashboard's `is_own DESC` ordering and `bestHealthyDERP()` therefore picked 901.
2. `GET /admin/derp/relays/derpmap.json` published every enabled row as a relay
   node, so every client's map contained a phantom region 901 pointing at
   `controlplane.tailscale.com:443` (the control-plane API host) — plus a dead
   `derp.example.com:8443` node (nothing listening on 8443) with the same node
   name as the live one.

B265 fixes the mapping, skips derpmap-document rows, probes each node's DERP
port before advertising it, and de-duplicates node names inside a region.

Operator cleanup for that data:

```sql
SELECT id, region_id, hostname, url, is_bundled, enabled FROM derp_relays ORDER BY id;
-- a region-901 row whose URL ends in /derpmap/... is a MAP, not a relay: disable
-- or delete it (headscale already merges the public map via derp.urls);
-- and keep exactly ONE bundled row per relay (the ORDER BY id ASC LIMIT 1
-- resolution hides duplicates but the derpmap used to publish both).
```

### Permanent guard

Keep `extra_hosts` + `SKYGATE_DERP_PROBE_HOST` and remember the recreate requirement
and CWD-relative compose interpolation. Keep the DB the single source of truth for
hostname + port. Treat `/admin/derp` as a *probe*: `stopped` plus a working WebSocket
upgrade means the probe path is broken, not the relay. A red STUN tile must name its
reason — a bare "closed" on a relay clients are actively using is a bug in the page,
not in the relay. Do not "fix" latency with policy changes, and never add a derpmap
URL as a `derp_relays` row.

---

## 6. Duplicate node names in headscale

### Symptom

Two nodes share an OS hostname (`<Device>`) in `/admin/devices` and headscale, one
with a dev-tag and one showing "⏳ pending". Operator question, verbatim: "как
поведет себя headscale если устройство с такимже именем уже есть" — *what does
headscale do when a device with the same name already exists?*

### What it actually means

headscale distinguishes **Hostname** (what the client reports; case-preserved; may
collide) from **GivenName** (canonical DNS label; lowercased; must be unique):

| Path | Behaviour |
|---|---|
| Client-driven registration | headscale **auto-renames** with `-1`, `-2`, … — first unused label wins, no error to the client |
| Admin-driven rename (CLI, headplane, API) | **rejected** with `given name already in use by another node` (HTTP 409); delete the conflicting node or pick another name |
| Same node re-registers (same NodeKey) | GivenName **preserved**; only Hostname updates, and an admin rename is sticky |

The uniqueness scan is global, not per-user, so an OIDC node colliding with a
preauth-key node gets the bumped name (`<device>` vs `<device>-1`). The client is
never told, so the OS still thinks it is `<Device>`. Tagged nodes then move to the
synthetic `tagged-devices` user, which is why the autoupdater's
`n.UserName == <portal user>` matching misses one.

### Confirm it

```bash
docker exec headscale headscale nodes list -o json | python3 -c "
import sys, json
for n in json.load(sys.stdin):
    print(n['id'], repr(n.get('name')), repr(n.get('givenName')),
          n.get('user',{}).get('name'), n.get('tags'))"
```

Look for two rows with the same `name` and different `givenName`.

### Fix

1. **Delete the stale duplicate, then rename the live one** (clean state):
   ```bash
   docker exec headscale headscale nodes list | grep -Ei '<hostname>'
   docker exec headscale headscale nodes delete <OLD_ID>
   docker exec headscale headscale nodes rename <LIVE_ID> <canonical-name>
   ```
   Trade-off: if the old node is a real device, it must re-register.
2. **Teach the matching to handle the synthetic user** — a fallback recognising
   `n.UserName == "tagged-devices"` plus an existing
   `tag:dev-<portalUser>-<hostname>*` tag lets the autoupdater adopt the node.
   Until then the UI keeps showing "pending".
3. **Leave it** — both nodes work; the "pending" marker is cosmetic. Acceptable for
   a legacy orphan.

### Permanent guard

Store the **GivenName** (canonical, unique) in `node_owner_map`, not the raw
Hostname. When migrating, delete the conflicting client first or pick a different
name — `rename` will not auto-bump. Remember that tagged nodes land in
`tagged-devices` regardless of creator, and make any `n.UserName` join defensive.

---

## 7. The UI shows stale or contradictory state after a change

### Symptom

After deleting a device, changing a preference, or syncing routes, the page still
shows the old value: a deleted device still listed; a removed route still on
`/admin/exit-nodes`; the subnet status pill on `/admin/users/{id}/subnet` stuck on
the old value until you click around; or a per-row action dumping raw JSON into the
browser instead of returning to the page.

### What it actually means

Skygate caches headscale-derived state for a short TTL (≈5 s) so page loads are not
one API call per row. Mutations must explicitly invalidate that cache; if they do
not, the next render serves the pre-change snapshot. The same applies to
**denormalised** columns (`portal_users.subnet_status` et al.), refreshed only when
a particular page is loaded.

Separately, a POST handler returning JSON instead of redirecting makes the browser
render the JSON body as a page — the operator sees "the page broke" when the
backend actually succeeded.

### Confirm it

```bash
# 1. Live API vs rendered page:
docker exec headscale headscale nodes list -o json | head -40
curl -fsS http://127.0.0.1:8080/healthz
# 2. Did the change land in the DB?
sqlite3 /var/lib/skygate/skygate.db \
  "SELECT node_id, username, hostname FROM node_owner_map WHERE hostname='<device>';"
sqlite3 /var/lib/skygate/skygate.db \
  "SELECT action, substr(detail,1,80) FROM audit_log ORDER BY id DESC LIMIT 10;"
# 3. Redirect or JSON body?
curl -sS -i -X POST -b /tmp/c.txt http://127.0.0.1:8080/admin/exit-nodes/<node>/sync | head -5
```

### Fix

- **Re-load after the cache TTL** (a few seconds). If it clears, fix the handler so
  successful mutations call `hs.InvalidateCache`. Device deletion is the reference
  implementation: it cleans `node_owner_map`, `device_exit_node_prefs` and orphaned
  `device_rules`, regenerates the ACL, invalidates the cache, and writes an audit
  row — so the next page load sees the new state.
- **Denormalised subnet columns** — load `/my/devices` (or wait a sidecar tick).
- **Raw JSON in the browser** — the POST handler must `http.Redirect` with a flash
  query parameter (`?ok=…` / `?err=…`) like every other admin POST handler, instead
  of `json.NewEncoder(...).Encode(...)`. This affects per-row buttons that are plain
  `<form method="post">` submissions; JS `fetch()` buttons tolerate JSON, form
  buttons do not.
- **Ordering bugs inside a render** — a derived value attached to a struct *after*
  it is copied into a page-level grouping map is lost; annotate before grouping.

### Permanent guard

Every mutation path invalidates the cache; every deletion path also cleans dependent
tables and regenerates the ACL. Admin POST handlers redirect with a flash, never dump
JSON at a form submission. The read-only companion UI (headplane) needs no explicit
call — it reads headscale directly and refreshes on its next page load.

---

## 8. Install / upgrade-time failures

### 8.0 Self-update rolled back: "healthz did not report build …" (B268)

**Symptom.** `/admin/update` shows `СТАТУС ОБНОВЛЕНИЯ: FAILED` and the job log ends
with

```
installed the new binary over /usr/local/bin/skygate
restarting (systemd)
ROLLBACK: restoring the previous binary from /var/lib/skygate/update/skygate.prev
verdict: rolled_back (healthz did not report build 'v1.5.11' within 90s (last build: none))
```

**What that sentence actually covers.** It is emitted whenever the verify step
(`SKYGATE_UPDATE_HEALTH_URL`, default `http://127.0.0.1:8080/healthz`) never returns
a body whose `build` starts with the target tag. That is true for at least four very
different causes, and before B268 the applier told you none of them:

1. **the unit never started** (masked/disabled, a crashing binary, a missing env
   value in `/etc/skygate/skygate.env`);
2. **another instance owns the port** — very common on a host that also runs the
   Docker deployment: `docker-proxy` holds `:8080`, so the native unit cannot bind,
   and the health URL happily keeps serving the *container's* build (which is not
   your target, and on a different release train it can also be the wrong version);
3. **the service listens on another port** than `SKYGATE_UPDATE_HEALTH_URL`;
4. **the artifact cannot execute on this host** (wrong arch, truncated download,
   missing loader).

**What B268 changed** (`deploy/skygate-apply-update.sh`) — read `apply.log` again
after upgrading to v1.5.12+; it now contains:

* `pre-swap health baseline: … reports build 'X' (unit state: …)` and a `WARN` when
  that build is not `FROM_VERSION`, followed by the listener on the port
  (`DIAG`-style `listener on port 8080: …`) — this is case 2, and it is reported
  **before** anything is swapped;
* `smoke test OK: the new binary runs and reports '…'` — or a hard stop with
  `refusing to swap in a binary that does not run` (case 4). The installed binary is
  left untouched in that case;
* on failure, a `DIAG:` block: `unit state`, `listener on port`, `binary on disk`
  and the last 15 `journalctl -u <service>` lines;
* a verdict that distinguishes `the service did not come up: … never returned a
  healthy body within Ns` from `healthz did not report build X … (last build: Y)`,
  both carrying the unit state.

**Operator commands for the four cases** (native install):

```bash
systemctl status skygate --no-pager          # 1: active/failed/masked?
journalctl -u skygate -n 50 --no-pager       # 1: the real startup error
sudo ss -ltnp | grep ':8080'                 # 2/3: who actually owns the port
docker ps --format '{{.Names}} {{.Ports}}'   # 2: a container on the same port?
sudo grep -E 'HEALTH_URL|BINARY|SERVICE' /etc/skygate/update-helper.conf
```

Fix for case 2 on a host that runs both: keep exactly one serving instance. Either
remove/disable the unused native unit (`systemctl disable --now skygate`) or point the
native install at another port (`SKYGATE_PORT=8081` in `/etc/skygate/skygate.env`,
default `8080`) **and** update `SKYGATE_UPDATE_HEALTH_URL` in the root-owned helper
conf (`/etc/skygate/update-helper.conf`) to match — the applier verifies that URL, not
the listening socket.

**Recovery if the applier already rolled back:** nothing is broken — the previous
binary was restored and restarted; the update simply did not apply. Fix the cause
above, then press “Update now” again. If the verdict was `failed` with
`MANUAL INTERVENTION REQUIRED`, the rollback restarted a binary that cannot bind
either (case 2): free the port, then `systemctl restart skygate` and confirm
`curl -fsS http://127.0.0.1:8080/healthz`.

### 8.0.1 The unit is `active` but nothing listens on the port (B269)

**Symptom.** `systemctl is-active skygate` prints `active`, the journal shows
skygate's background work (the `dbmigrate-watchdog` ticks, `db_health` samples,
DERP probes), but

```bash
sudo ss -ltnp | grep ':8080'
# (nothing)
curl -sS http://127.0.0.1:8080/healthz
# curl: (7) Failed to connect
```

and every consumer of `/healthz` — the update applier, a reverse proxy, the
operator's browser — reports a timeout. On a **pre-B269** binary this is the
signature of a `main()` that never reached `http.ListenAndServe`: the listener was
created **last**, so config parsing, the DB open (5 attempts with backoff ≈ 42 s),
migrations, the headscale client, the ACL/Telegram/exit-rules wiring or the route
table could stall or fail first and leave a mute, portless process that systemd
still calls healthy. The self-updater said only
`healthz did not report build '…' within 90s (last build: none)`.

**What B269 changed.** From v1.5.12+:

* the socket is bound at the **top** of `main()`, right after the config loads —
  before the DB, before migrations, before any service. A **port conflict** is now
  the first thing reported (`listen: bind :8080: address already in use`) and the
  process exits non-zero instead of looking healthy for minutes;
* the bound listener immediately serves a **provisional** `/healthz` and is handed
  to the real router when routes are complete (one bind, no gap);
* every boot phase is written to the journal as
  `startup: phase=<name> +Nms (total=Nms)`.

**Diagnose it in one command** — the last phase line is where it stopped:

```bash
sudo journalctl -u skygate --since "10 min ago" --no-pager | grep 'startup:'
```

| Last `startup:` line | Meaning / next step |
|---|---|
| `phase=config` | The config was not accepted — read the `config:`/`HEADSCALE_API_KEY is required`/`SKYGATE_JWT_SECRET is required` line right after it. |
| `phase=db-open+migrate` | The DB is unreachable or a migration is running. Check `SKYGATE_DB` / `SKYGATE_DB_PATH` in `/etc/skygate/skygate.env` and that the DB file/dir is writable by the service user. |
| `phase=headscale-client` | headscale is down or `HEADSCALE_URL` is wrong; boot continues, so pair it with the next phase line. |
| `phase=services+telegram`, `phase=routes` | A service constructor or the route table is stuck — the line after it names the culprit. |
| `phase=handover` | The router was swapped in; if `/healthz` still fails, the port is being answered by something else (`sudo ss -ltnp | grep ':8080'`). |
| `startup: ready` | Boot finished. If `/healthz` fails anyway, you are probing the wrong port/host. |

While the process is still booting, `/healthz` answers `200` with
`"status":"ok"`, the build string, and diagnostics (`phase`, `stage`, `ready`,
`timeline`):

```bash
curl -sS http://127.0.0.1:8080/healthz
# {"status":"ok","build":"v1.5.12","phase":"db-open+migrate","stage":"boot",
#  "ready":false,"uptime_ms":3036,...,"timeline":[{"name":"config","ms":43}]}
```

`"ready":false` means **the process is up but not finished starting**. The update
applier refuses such a body as proof of a successful swap, so a boot that never
completes still rolls back honestly instead of being declared deployed.

A `panic` during boot is now reported with its stack and phase and exits `3`:

```
startup: PANIC in phase=services+telegram: runtime error: invalid memory address …
goroutine 1 [running]:
…
```

### 8.0.2 The update rolls back every time even though the service is healthy (B270)

**Symptom.** `/admin/update` always ends in a rollback with
`healthz did not report build X … (last build: none)`, while the service is up,
serving, and reachable on its real port. Two independent traps produce exactly
this; both were found on one host in the same incident.

**Trap 1 — the port in the health URL is not the port the service listens on.**
The privileged applier verifies `SKYGATE_UPDATE_HEALTH_URL` (default
`http://127.0.0.1:8080/healthz`), but the search for *why should be* your first
question, not the build string. Check both numbers:

```bash
sudo grep -E 'SKYGATE_PORT' /etc/skygate/skygate.env          # what the service listens on
sudo grep -E 'HEALTH_URL'    /etc/skygate/update-helper.conf  # what the applier polls
sudo ss -ltnp | grep -E '8080|8082'                           # what is actually bound
```

If they disagree (live case: the service ran `SKYGATE_PORT=8082` while the
applier polled `:8080`), **no build verification can ever succeed** and every
update rolls back on a perfectly healthy instance. Since B270 the applier says so
itself, in the pre-swap log:

```
WARN: PORT MISMATCH — health verification polls http://127.0.0.1:8080/healthz (port 8080)
      but the service is configured with SKYGATE_PORT=8082 in /etc/skygate/skygate.env
WARN: the post-restart build check can NEVER succeed while they disagree; every update
      will roll back even though the service is healthy
```

Fix one side: set `SKYGATE_PORT=<the same port>` in `/etc/skygate/skygate.env`, or
point `SKYGATE_UPDATE_HEALTH_URL` at the port the service really uses in the
root-owned `/etc/skygate/update-helper.conf` (the applier verifies that URL, not
the listening socket). Restart the unit after editing the env file.

**Trap 2 — an optional feature kills the whole process (B270).** A native install
whose working directory was not the data dir resolved the OIDC key directory
(`SKYGATE_OIDC_KEY_DIR`, default was the relative `./data/oidc-keys`) against `/`,
so `NewService` failed with

```
oidc: SKYGATE_OIDC_ISSUER not set — OIDC routes will return 503 until configured
oidc: init failed: oidc: mkdir ./data/oidc-keys: mkdir ./data: permission denied
```

and that error was **fatal** — the process exited before it ever bound its HTTP
port. The unit stayed `active` with nothing listening, and OIDC was not even
configured. Since B270:

* the key-store failure is **not fatal**: the process boots, the journal carries
  `oidc: KEY STORE UNAVAILABLE (…) — the process keeps running and the OIDC
  routes answer 503; fix SKYGATE_OIDC_KEY_DIR (needs a directory writable by the
  service user, e.g. /var/lib/skygate/oidc-keys) and restart`, and every OIDC
  route answers `503` with the reason;
* the default key dir is **absolute and data-dir anchored**
  (`<dir of skygate.db>/oidc-keys`, or `/var/lib/skygate/oidc-keys` for
  PostgreSQL), so a relative path cannot come back through the default;
* the installers create `<data_dir>/oidc-keys` (`0700`, owned by the service
  user), so a fresh native install has a usable private key directory.

If you hit a leftover relative path, fix it explicitly and restart:

```bash
sudo install -d -m 0700 -o skygate -g skygate /var/lib/skygate/oidc-keys
# only needed if your env file pins the old relative value:
sudo sed -i 's|^SKYGATE_OIDC_KEY_DIR=.*|SKYGATE_OIDC_KEY_DIR=/var/lib/skygate/oidc-keys|' /etc/skygate/skygate.env
sudo systemctl restart skygate
sudo journalctl -u skygate -n 20 --no-pager | grep -E 'startup:|oidc:'
curl -sS http://127.0.0.1:<your port>/healthz
```

### 8.0.3 The journal repeats `SQL logic error: no such function: pg_…` every 30 s (B271)

**Symptom.** On a **SQLite** install the journal fills with the same four or five
errors every 30 seconds, and `/db/health` (and the availability badge) looks
degraded even though the database is fine:

```
db_health: tick: db_health: 4 query error(s): [
  server: SQL logic error: no such function: pg_is_in_recovery (1)
  database.size: SQL logic error: no such function: current_database (1)
  maintenance: SQL logic error: no such table: pg_stat_user_tables (1)
  xlog.current: SQL logic error: no such function: pg_current_wal_lsn (1)]
```

**Cause.** The `/db/health` background sampler was written for PostgreSQL and ran
its catalog queries against whatever backend was configured — including SQLite,
where none of those functions exist. Each failed statement still touched the
database, and the operator saw a permanently "degraded" DB on a healthy install.

**Fix (B271).** The sampler is dialect-aware:

* `dialect=sqlite` — the collector uses `PRAGMA page_count` × `PRAGMA page_size`
  for the size, `SELECT sqlite_version()` for the version, `PRAGMA quick_check`
  for integrity (a non-`ok` answer is surfaced as the sample error) and
  `PRAGMA journal_mode` (logged). The PostgreSQL-only panels (replication, WAL
  position) stay empty instead of reporting invented values.
* `dialect=postgres` (the default, so nothing changes by omission) — the previous
  behaviour, bit for bit.

Verify after upgrading:

```bash
sudo journalctl -u skygate --since "5 min ago" --no-pager | grep -c 'no such function'
# 0
curl -sS http://127.0.0.1:<your port>/healthz   # still 200
curl -sS http://127.0.0.1:<your port>/db/health | head -c 400
```

The startup line now states which dialect the sampler uses:

```
db-health: started (interval=30s, query-timeout=3s, dialect=sqlite)
```

### 8.0.4 Devices show a tag that headscale does not have (B272)

**Symptom.** `/my/devices` (or `/admin/devices`) shows a per-device tag such as
`tag:dev-daniil-workpc`, but the device has **no** per-device ACL rule and
`/my/exit-rules` rules never match. Checking headscale directly shows the tag is
not there:

```bash
sudo headscale nodes list | grep -A2 "^2"
# node 2  user=daniil  dev=-  all=-      ← no tags, while skygate shows one
```

Nothing in skygate reports an error: `audit_log` has no
`tag.autoupdate_failed` row and `skygate_tag_autoupdate_failures_total` is 0.

**Two independent causes, both fixed in v1.5.13 (B272).**

**Cause 1 — headscale keeps its policy in a file.** If `config.yaml` has

```yaml
policy:
  mode: file
  path: /etc/headscale/policy.hujson
```

then `PUT /api/v1/policy` answers `500 {"code":2,"message":"update is disabled for
modes other than database"}` — skygate cannot add the tag to `tagOwners`, and
without that entry headscale refuses the tag itself. Before v1.5.13 skygate only
fell back to the file path on 404/405, so it never wrote it, and the fallback it
did have used a hardcoded docker volume that a native install does not have.

**Cause 2 — tag writes went through `docker exec`.** On a native (systemd)
install there is no docker, so every tag write failed with
`tag: exec: "docker": executable file not found in $PATH` — visible only in the
audit row of a manual Adopt or in stderr (`warn: auto-apply dev tag …`).

**Verify after upgrading to v1.5.13+:**

```bash
# the autoupdater reconciles every 5 min; force one tick by restarting skygate
sudo systemctl restart skygate
sudo journalctl -u skygate -f | grep -E 'tag-reconcile|node-discovery|auto-apply'

# a repair looks like this:
#   tag-reconcile: applied "tag:dev-daniil-workpc" to node 2 (workpc) — database
#     said it was owned by "daniil", headscale had []
#   tag-reconcile: checked=2 applied=1 failed=0 missing=0 unattributed=1

# confirm in headscale
sudo headscale nodes list | grep -A2 "^2"
```

If the reconciliation **fails**, the reason is now in three places at once — the
journal, `/admin/audit` (`tag.autoupdate_failed`, with `reason=` and the raw
headscale error) and, when a bot is configured, Telegram:

| `reason` | Meaning | Fix |
|---|---|---|
| `acl_reject` | headscale refused the tag (not permitted / policy did not accept it) | add the tag to `tagOwners` (see below) |
| `tag_missing` | the database says the tag exists, headscale disagrees — this pass tried to repair it and failed | read the error text in the audit row |
| `no_strategy` | the node belongs to no portal user at all (never registered through skygate and never adopted) | `/admin/devices` → **Adopt**, or register the device through skygate |
| `rpc_error` | transient (network / 5xx) | nothing; the next tick retries |

**If skygate cannot write the policy file itself** (the service user has no write
access to `/etc/headscale/`), it says so and prints the policy to apply by hand:

```bash
sudo grep -A3 '^policy:' /etc/headscale/config.yaml     # find policy.path
# add the missing entries to tagOwners, then:
sudo systemctl restart headscale
sudo headscale policy get | head -20
```

#### The file must be readable by headscale

With `policy.mode: file` the API answers **500** until headscale itself can read
the file — and then nothing else can work either:

```
{"code":2, "message":"reading policy from path \"/etc/headscale/policy.hujson\":
                       open /etc/headscale/policy.hujson: permission denied"}
```

Check both sides (they are different users — `headscale` serves, `skygate`
writes):

```bash
systemctl show headscale -p User -p Group          # usually headscale:headscale
sudo -u headscale test -r /etc/headscale/policy.hujson && echo "headscale CAN read"
sudo -u skygate   test -w /etc/headscale/policy.hujson && echo "skygate CAN write"
```

A file owned `root:skygate 0660` satisfies **only** skygate. Give it to
headscale and keep skygate in the group (either command set works):

```bash
# owner = headscale (it only needs to read), group = skygate (it must write)
sudo chown headscale:skygate /etc/headscale/policy.hujson
sudo chmod 0640 /etc/headscale/policy.hujson

# or the other way round
sudo chown root:headscale /etc/headscale/policy.hujson && sudo chmod 0640 /etc/headscale/policy.hujson
sudo usermod -aG headscale skygate && sudo systemctl restart skygate
```

`/etc/headscale` itself must be traversable by both (`drwxr-xr-x root root` is
enough). Verify and continue:

```bash
sudo systemctl restart headscale && sleep 3
sudo headscale policy get | head -5                # must return JSON, not an error
sudo journalctl -u skygate -f | grep tag-reconcile # the next tick repairs the tags
```

After this, skygate writes `tagOwners` itself (B272.1), applies the missing tags,
and `tag-reconcile` reports `applied=N failed=0`.

#### skygate reports this for you (v1.5.15+)

You do not have to work it out from a 500 body any more — the same audit runs in
three places:

* **at boot** — the journal gets a warning with the fix commands:

  ```
  ⚠️  headscale policy: the headscale service user (headscale) cannot read
      /etc/headscale/policy.hujson — its policy API answers 500 and NO node tag
      can ever be permitted (unreadable_by_headscale)
      fix: # owner = headscale (it must READ the policy), group = skygate (it must WRITE it)
      fix: sudo chown headscale:skygate /etc/headscale/policy.hujson
      fix: sudo chmod 0640 /etc/headscale/policy.hujson
      fix: sudo systemctl restart headscale
      fix: # verify: sudo -u headscale test -r /etc/headscale/policy.hujson && sudo -u skygate test -w …
  ```

* **on `/admin/derp`** — a red banner with the status, the resolved path, who can
  do what (`headscale: headscale`, `skygate: rw`) and the same command block.
  The verdict for the headscale account is a **real probe**
  (`sudo -n -u headscale test -r …`) when sudo is available, otherwise a
  conservative model built from the file's owner/group/mode — so the page never
  claims readability it cannot prove.

* **through the metric-visible failure path** — the reconciliation tick keeps
  reporting `failed=N` with the nested reason until the permissions are fixed.

Statuses the audit can report:

| `status` | Meaning |
|---|---|
| `ok` | readable by headscale, writable by skygate |
| `not_applicable` | headscale uses `policy.mode: database` — no file involved |
| `unreadable_by_headscale` | the live case: the API answers 500, no tag can be permitted |
| `unreadable_by_skygate` | skygate cannot compute the new `tagOwners` |
| `missing` | `policy.path` is configured but the file does not exist |
| `api_error` | skygate could read it, but the policy API itself failed |

#### Even with correct permissions skygate cannot write into `/etc/headscale`

**Symptom (v1.5.15 and earlier):**

```
tag-reconcile: cannot make "tag:dev-…" permitted …:
  write policy file /etc/headscale/policy.hujson:
  open /etc/headscale/policy.hujson.skygate.tmp: read-only file system
```

The skygate unit runs with

```
ProtectSystem=strict
ReadWritePaths=/var/lib/skygate /etc/skygate
```

so `/etc/headscale` is **read-only for skygate by design** — that is the sandbox
doing its job, not a permission slip. Making the file `0660` cannot help.

**Two supported ways out (v1.5.16+ automates the handoff):**

```bash
# 1. install the privileged policy applier (root-owned, same model as the
#    self-update applier) — skygate then applies tagOwners itself
sudo install -m 0755 -o root -g root deploy/skygate-apply-policy.sh /usr/local/lib/skygate/
#    plus the skygate-policy.path / skygate-policy.service pair
#    (deploy/install-common.sh writes both; a re-run of the installer is enough)
sudo systemctl daemon-reload
sudo systemctl enable --now skygate-policy.path
```

With the helper installed, skygate writes a **data-only** request
(`<data_dir>/update/policy.request.props`) and the root path unit applies it:
absolute path, cross-checked against headscale's own `policy.path`, atomic write,
previous policy kept in `policy.prev` and restored if headscale does not come
back. Nothing is ever sourced or evaluated.

```bash
# 2. apply the policy by hand (works without the helper)
#    skygate logs the exact policy it wanted — copy the JSON from the
#    "apply this policy by hand: { … }" part of the error, then:
sudo headscale policy get > /tmp/policy.current.json     # keep a copy
sudo $EDITOR /etc/headscale/policy.hujson                # add the tagOwners entry
sudo systemctl restart headscale
sudo headscale policy get | head -20                     # verify
```

### 8.0.5 `git checkout` blocks the image update on an untracked file (B272.4)

**Symptom.** `/admin/update` fails at the checkout phase:

```
[debug] $ git checkout v1.5.14
error: The following untracked working tree files would be overwritten by checkout:
        scripts/skygate-move-to-infra.sh
Please move or remove them before you switch branches.
[error] phase failed: git checkout: exit status 1
[error] FAILED: git checkout: exit status 1
```

and the updater rolls back. **Every** future image update fails the same way until
the file is removed.

**Why it happens.** The file exists in the working tree but not in the index (a
leftover from an earlier manual `git bundle` transfer, a cherry-pick or a
copy-paste), while the target revision *does* track a file with that name. Git
refuses to overwrite untracked data — correctly, in general.

**What v1.5.16+ does.** Before giving up, the updater computes the conflict set
(untracked files ∩ paths the target revision writes) and, if it is non-empty:

1. copies each file to `<update_dir>/checkout-stash/<timestamp>/…` **with its
   permission bits**,
2. removes the working-tree copy,
3. retries the checkout and logs

   ```
   untracked file scripts/skygate-move-to-infra.sh would block the checkout of
   v1.5.14 — backed up to /data/update/checkout-stash/20260919T211500Z/… and
   replaced with the tracked version
   ```

A **modified tracked** file (your `docker-compose.yml`, `go.mod`, `go.sum`, any
edited source file) is a different class: its content is real data, so the
checkout still refuses and the update still rolls back, untouched.

**If you are on v1.5.15 or earlier**, unblock it manually:

```bash
cd /path/to/skygate                       # the repo the updater checks out
git status --short | grep '^??'           # what is untracked
git diff --no-index scripts/skygate-move-to-infra.sh <(git show v1.5.15:scripts/skygate-move-to-infra.sh) && echo "identical — safe to delete"
sudo mv scripts/skygate-move-to-infra.sh /root/skygate-move-to-infra.sh.bak
# then retry the update; the tracked version arrives with the checkout
```

To let skygate do it automatically on a native host, either grant the service user
write access to that directory or run headscale with `policy.mode: database`:

```bash
# /etc/skygate/skygate.env
SKYGATE_HEADSCALE_POLICY_PATH=/etc/headscale/policy.hujson
SKYGATE_HEADSCALE_UNIT=headscale
```

Unattributed devices are also counted, so a Prometheus alert is possible:

```
skygate_tag_unmatched_total{node_id="3",hostname="laptop"} 1
```

### 8.1 Container cannot reach a hostname that resolves to `127.0.0.1`

**Symptom.** Containerised skygate cannot reach a host that is up and reachable from
the host: `/admin/derp` reports `stopped`, a probe times out, and the container
resolves the target hostname to `127.0.0.1`.

**What it actually means.** The host resolver (`systemd-resolved` or anything honouring
`/etc/hosts`) returns `127.0.0.1` for the host's own hostname/aliases. Docker inherits
that resolver, so inside the container `127.0.0.1` is the container's **own** loopback.

**Confirm it.**

```bash
getent hosts <hostname>                        # on the host
docker exec skygate getent hosts <hostname>    # inside the container
docker exec skygate curl -sS -o /dev/null -w '%{http_code}\n' http://<hostname>:<port>/
```

**Fix.** Add `extra_hosts` with the address in an operator-managed `.env` variable,
then recreate the container:

```yaml
services:
  skygate:
    extra_hosts:
      - "<hostname>:${SKYGATE_DERP_PROBE_HOST:-127.0.0.1}"
```

```bash
cd <project dir>
sudo docker compose up -d --force-recreate --no-deps skygate
```

A plain `restart` does **not** re-render `/etc/hosts`/`extra_hosts`, and
`docker compose -f /path/to/compose.yml` outside the project does not load the
project `.env`, so the placeholder silently takes its default. Always `cd` into the
project (or pass `--env-file`).

**Permanent guard.** Keep the `extra_hosts` block and `.env` variable; make recreate
(not restart) the documented step for any DNS/volume change; run compose from the
project directory.

### 8.2 `sqlite:` DSN issues

**Symptom.** A native SQLite install prints its banner and then no listener and no
further logs; systemd shows it restarting. A foreground run gives, after ~40 s:

```
db: unreachable after retries: 5 attempts: dialect open
"sqlite:/var/lib/skygate/skygate.db": sqlite ping ...:
unable to open database file: out of memory (14)
```

**What it actually means.** The `sqlite:` scheme prefix was not understood: only bare
paths and `file:` URIs were converted, and DSNs starting with `sqlite:` were skipped.
SQLite then tried to create a **relative file literally named `sqlite:/var/...`**,
failed with `SQLITE_CANTOPEN` (14), and the driver surfaced it as "out of memory".
`SKYGATE_DB=sqlite:/path` is exactly what the installer writes, so affected releases
were dead on arrival on SQLite. Tests missed it because SQLite tests used `:memory:`
and the two tests mentioning `sqlite:/...` only asserted dialect classification.

**Confirm it.**

```bash
grep -n '^SKYGATE_DB' /etc/skygate/skygate.env
ls -la /var/lib/skygate/          # look for a stray file named `sqlite:...`
journalctl -u skygate -n 50 --no-pager
```

**Fix.** Upgrade to a build that strips the `sqlite:` prefix at the top of the SQLite
open path (covering web, CLI and `db-migrate`), then verify all three accepted shapes:

```bash
/usr/local/bin/skygate migrate-only
curl -fsS http://127.0.0.1:8080/readyz
```

**Permanent guard.** Keep real-file open tests for `sqlite:<path>`, bare `<path>` and
`file:<uri>`, and assert no stray `sqlite:`-named file appears. Releases predating the
idempotency fixes cannot run on SQLite at all (`v44 add user_name: duplicate column
name`, fresh or existing DB) — a native SQLite host needs a newer release.

### 8.3 Migration aborts mid-upgrade

**Symptom.** The update stops before the binary swap and the service stays on the old
version; `/admin/update` reports a migration failure and the helper log contains
`migrations failed (the old binary is still installed)`. On SQLite:
`v44 add user_name: duplicate column name`.

**What it actually means.** The update runs the **new** binary's migration step as the
service user *before* swapping the binary, so an un-appliable migration aborts with the
old binary intact — a safe stop, not a bricked host. It happens when the release's
migration chain cannot handle the current DB (a partially applied schema change, or a
release that predates idempotency fixes).

**Confirm it.**

```bash
cat <data_dir>/update/apply.log
tail -n 200 <data_dir>/update/result.props
journalctl -u skygate-update -n 200 --no-pager
sudo env $(grep -v '^#' /etc/skygate/skygate.env | xargs) ./skygate migrate-only   # native
```

**Fix.** Do not force the swap. Read the migration error and move to a compatible
release. Back up the DB first (`cp /data/skygate.db /data/skygate.db.pre-migration` or
`pg_dump`) and retry. Manual fallback: `migrate-only` is a **subcommand** —
`--migrate-only` prints `unknown command`.

**Permanent guard.** Always keep the pre-migration backup and the pre-swap
`migrate-only` gate. Automatic rollback is the designed safety net on native installs;
on Docker the old image tag is the rollback target.

### 8.4 Service in a crash-restart loop needing `systemctl reset-failed`

**Symptom.** The unit will not start even after the underlying problem is fixed;
`systemctl start` returns immediately and `status` shows `failed`. A container shows a
restart loop with the same error each cycle (a missing auth-key file is common:
`read auth key: open /data/ts/authkey: no such file or directory`).

**What it actually means.** systemd keeps a unit's `failed` state and, once the
start-limit is hit, refuses further automatic restarts until it is cleared. The loop is
the symptom; the real error is in the journal. Containers are the same: a missing key
file, a bad DSN, or a missing required env var (`HEADSCALE_API_KEY` is required and the
process refuses to start without it) keeps them cycling.

**Confirm it.**

```bash
systemctl is-active skygate; systemctl is-failed skygate
journalctl -u skygate -n 100 --no-pager       # find the FIRST real error, not the last
docker logs --tail 100 skygate                # container installs
```

**Fix.**

```bash
sudo systemctl reset-failed skygate           # native systemd, after fixing the cause
sudo systemctl start skygate
systemctl is-active skygate && curl -fsS http://127.0.0.1:8080/healthz
rc-service skygate restart                    # native OpenRC (Alpine)
```

For containers, fix the underlying file/env/DSN problem and recreate
(`--force-recreate` when DNS/`extra_hosts`/volumes changed). If the auth-key file is
missing, re-paste it via `/admin/tailscale` or restore it at the configured path — the
page distinguishes "configured but missing" from "enabled", so check which state it
reports before acting.

**Permanent guard.** Read the *first* error, not the restart-loop tail. Keep
`reset-failed` in the manual-update procedure after any crash loop. Validate required
env vars and key files during install, and surface the distinct states in the UI.

---

## 9. Telegram relay silently not delivering

### Symptom

The bot does not deliver; `/admin/telegram` says **unreachable** (sometimes with the
resolved IPs shown next to the verdict) or, on a host where the API should be blocked,
says **reachable (direct internet)**. The *Egress relay* card flashes
`Не удалось применить маршруты: ssh <RELAY_HOST> (key …): ssh: Could not resolve
hostname <RELAY_HOST>: Name or service not known` (*failed to apply routes*) or
`no ssh_key_path provided`. Polling may work but late.

### What it actually means

Skygate reaches `api.telegram.org` through **subnet routes** advertised by a relay, not
through an exit node: only the Telegram CIDRs are re-routed, everything else (headscale
API, Docker network) stays direct. "Silently not delivering" is always one of:

1. The relay advertises the ranges but **headscale has not approved** them.
2. The resolved Telegram IPs are **not in the relay's advertised list** (Telegram added
   a block; the advertisement is static until refreshed).
3. tailscaled in the container is **not running** or did not accept the routes
   (`tailscale status` reports "Tailscale is stopped").
4. The chosen egress relay is disabled/misconfigured (`exit_servers.enabled=0`, bad
   `ssh_target`, missing key). A failed apply leaves the previous selection in place,
   so the UI can look configured while nothing is applied.
5. The relay is slow/far, so polling succeeds but late.

### Confirm it

```bash
# 1. tailscaled + routes in the container:
docker exec skygate tailscale status          # must show a Subnets section
docker exec skygate tailscale netcheck
docker logs skygate 2>&1 | grep -iE 'tailscale|tailscaled'
# 2. Routes approved in headscale?
docker exec headscale headscale nodes list -o json | python3 -c "
import sys, json
for n in json.load(sys.stdin):
    if n.get('givenName') == '<RELAY_HOST>':
        print('available:', n.get('availableRoutes')); print('enabled  :', n.get('enabledRoutes'))"
# 3. Last egress attempt:
sqlite3 /data/skygate.db \
  "SELECT id, action, substr(detail,1,80) FROM audit_log
   WHERE action LIKE 'telegram_egress%' ORDER BY id DESC LIMIT 10"
```

### Fix

- **Not approved** — run the `headscale nodes approve-routes` command printed by the
  relay setup script (or tick the routes in headplane) and wait ~60 s.
- **IPs not covered** — run the relay's route-update script
  (`/usr/local/bin/skygate-update-telegram-routes`, or *Apply* on the Egress relay
  card), then approve the new ranges. The script refuses an empty list (that would wipe
  the advertisement and break the bot).
- **tailscaled stopped** — start it from `/admin/tailscale`, pasting a headscale
  preauth key first if the page reports the auth-key file missing; `--accept-routes`
  must be on.
- **Egress card errors** — set `exit_servers.ssh_target` to `user@host` (optionally
  with a separate port) and provide `ssh_key_path` or `SKYGATE_EXIT_SSH_KEY`. The
  dropdown lists only `exit_servers WHERE enabled=1`; an empty dropdown means "No
  enabled exit-nodes" — add one on `/admin/exit-nodes`.
- **Failover** — several relays may advertise the same routes; Tailscale picks by
  metric. To force one, apply it on the egress card or drop the routes on the unwanted
  relay, then wait ~60 s.
- **High latency** — choose a closer relay; not a skygate bug.

### Permanent guard

Keep the weekly route-refresh cron on the relay so new Telegram blocks are picked up,
and re-approve after every refresh. Keep at least two relays able to advertise the
ranges. Remember a failed egress apply does **not** update the stored selection, so
"currently selected: X" does not prove the routes were applied. Use `/admin/telegram`
as the single check: unreachable *with* IPs listed means the IPs are not covered;
unreachable *without* them means no relay route is approved.

---

## 10. General diagnostics kit

Work top-down: is the process alive, can it reach its dependencies, what does the
control plane think, what does the data plane think, what did the code last do.

```bash
# 1. Process + dependencies
curl -fsS http://127.0.0.1:8080/healthz        # alive + build label
curl -fsS http://127.0.0.1:8080/readyz         # db + headscale + headplane + tailscale

# 2. Control plane
docker exec headscale headscale nodes list
docker exec headscale headscale nodes list -o json
docker exec headscale headscale policy get | python3 -m json.tool | head -80

# 3. Logs (first real error, not the loop tail)
journalctl -u skygate -n 200 --no-pager
journalctl -u skygate-update -n 200 --no-pager
docker compose logs --tail 200 skygate
docker logs headscale --tail 200

# 4. Data plane (only if the sidecar is enabled)
docker exec skygate tailscale status
docker exec skygate tailscale netcheck
tailscale status        # on an operator client: direct vs relay
tailscale netcheck      # per-region DERP latency

# 5. DB truth (PostgreSQL: same queries via psql -U skygate_admin -d skygate)
sqlite3 /var/lib/skygate/skygate.db "SELECT node_id, username, hostname FROM node_owner_map;"
sqlite3 /var/lib/skygate/skygate.db \
  "SELECT user_id, device_hostname, exit_node_tag, via_enabled FROM device_exit_node_prefs;"
sqlite3 /var/lib/skygate/skygate.db \
  "SELECT id, status, router_node_id, router_hostname FROM user_subnets;"
sqlite3 /var/lib/skygate/skygate.db \
  "SELECT id, created_at, username, action, substr(detail,1,80) FROM audit_log ORDER BY id DESC LIMIT 30;"

# 6. Network / firewall
sudo iptables -L DOCKER-USER -n -v; sudo iptables -L INPUT -n -v
sudo grep -nE 'DROP|REJECT' /etc/iptables/rules.v4
getent hosts <host>; docker exec skygate getent hosts <host>

# 7. Scripted checks
bash scripts/tailnet_probe.sh                  # peer visibility
bash scripts/check_subnet_router.sh <username> # subnet-router end-to-end
bash scripts/derp_relay_latency_test.sh        # DERP latency per node
bash scripts/verify_post_deploy.sh             # post-deploy smoke checks
```

**Admin pages that answer specific questions:** `/admin/devices` (online state, tags,
`last_seen`), `/admin/exit-nodes` (exit-node health + per-row *Re-sync*),
`/admin/exit-rules` (rules, preferred host, `Applicable`, reapply button),
`/admin/derp` + `/admin/derp/dashboard` (relay status, ports, public-IP provenance,
per-region latency), `/admin/telegram` (bot reachability + egress relay selector),
`/admin/tailscale` (in-container tailscaled start/stop, auth-key state),
`/admin/system_tests` (`tailnet.split_suspected` and other live probes),
`/admin/audit` (every mutation + reconciler change), `/admin/update` (install kind,
platform panel, apply log).

**Triage order that resolves most reports:** (1) `readyz` — if a dependency is not ok,
stop there; (2) `headscale nodes list` — if the node is online in headscale, the
problem is policy or data plane; (3) `audit_log` — did skygate actually do what was
asked?; (4) the relevant DB table — does skygate's stored state agree with headscale?;
(5) the firewall — only after the four steps above, since `DOCKER-USER` is the classic
self-inflicted outage.
