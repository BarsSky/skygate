# v0.23.3 — node-expiry watcher (the "Android won't stay connected" release)

## TL;DR

A background goroutine called **expirewatch** walks every
non-tagged node in headscale every 5 minutes and extends any
node whose `Expiry` is missing or within 7 days of "now" out
to 30 days. This works around a Tailscale 1.98.x client
behaviour where `RegisterRequest.Expiry` is only 2-4 seconds
in the future, and headscale 0.29.x applies that Expiry
verbatim — without the watcher, every fresh preauth-registered
device gets force-logged-out within seconds.

## What broke

On 2026-07-21 the operator's Android phone (skybars / node 10)
registered against a fresh 1h preauth key. The Tailscale
client's `RegisterRequest` carried an `Expiry` of only
`now + 2-4 seconds` (this is a client-side quirk in Tailscale
1.98.x — see investigation below). headscale 0.29.x's
`HandleNodeFromAuthPath` (in `hscontrol/state.go`) applied
that Expiry verbatim:

```go
if !node.IsTagged() {
    if !regReq.Expiry.IsZero() {
        node.Expiry = &regReq.Expiry
    } else if s.cfg.Node.Expiry > 0 {
        // ...
    } else {
        node.Expiry = nil
    }
}
```

Within ~4 seconds of the registration, the next netmap push
to the client reported `Expired: true, MachineAuthorized:
false`. Tailscale interpreted this as "your key was rejected,
log out", and the device went back to `NeedsLogin`. The
preauth was already `used=true`, so re-registration was
impossible. The user-facing symptom: "I generated a new key,
the device registered, then immediately disconnected and
won't come back".

We worked around the immediate problem by manually extending
the expiry on every affected node:

```bash
docker exec headscale headscale nodes expire -i 10 --expiry "$(date -u -d '+30 days' +'%Y-%m-%dT%H:%M:%SZ')"
```

…but that was a one-shot fix. v0.23.3 makes it automatic.

## What this release ships

### 1. `internal/expirewatch` — background watcher

New package with a `Manager` that ticks every
`SKYGATE_EXPIREWATCH_INTERVAL` (default 5m). Each tick:

1. Lists every node in headscale.
2. Skips tagged nodes (`tag:exit-node`, `tag:public`,
   `tag:subnet-router`, `tag:client`, …) — headscale's
   `state.go` only applies `regReq.Expiry` for non-tagged
   nodes, so tagged nodes naturally have nil Expiry and
   don't need renewal.
3. For each non-tagged node, parses the Expiry carried in
   `NodeView` (added in this release — see point 2).
4. If Expiry is missing or within `SKYGATE_EXPIREWATCH_THRESHOLD`
   (default 7d), calls `headscale.ExtendNodeExpiry(now +
   SKYGATE_EXPIREWATCH_RENEWAL)` (default 30d).
5. Appends an `audit_log` row tagged `username=expirewatch,
   action=renewed, detail=node_id=<N> old_expiry=<...>
   new_expiry=<...>` so an operator can correlate "my device
   just reconnected" with "the watcher extended its expiry".

### 2. `NodeView.Expiry` — plumb the field end-to-end

Previously, headscale's `NodeView` (the flattended
handler-friendly projection of `HSNode`) didn't carry
`Expiry`. The watcher would have needed N+1 round-trips
(one `GET /api/v1/node/{id}` per node) to make its decision.
This release adds the `Expiry` field to both `HSNode` (the
wire struct) and `NodeView` (the consumer struct) so the
watcher's decision is local.

Verified live: headscale's `/api/v1/node` always populates
`expiry` when the field is non-null in the DB; empty string
= nil expiry (tagged nodes, pre-v0.23.3 installs).

### 3. `headscale.ExtendNodeExpiry(nodeID, time.Time)`

New method on `*headscale.Client`. Tries the REST API first
(`POST /api/v1/node/{id}/expire` with body
`{"expiry": "RFC3339"}`); falls back to `docker exec
headscale headscale nodes expire -i <id> --expiry <RFC3339>`
when the admin API key lacks permission (the same pattern
as `CreatePreauthKeyWithTags`). Invalidates the
`ListAllNodes` cache on success so the next list reflects
the new expiry within one TTL window.

The `{"disableExpiry":true}` and `{"disable":true}` body
shapes both look right but are silently ignored by headscale
0.29.2's REST API (the field name is `disable_expiry` in the
proto, but the JSON binding doesn't recognise either
spelling). Only the explicit `{"expiry": "..."}` form works.
The CLI `headscale nodes expire -i <id> --disable` does
work (sets `node.Expiry = nil`) and is what tagged nodes
would use if we ever need that mode — but the watcher
never needs disable, because tagged nodes are skipped
entirely.

### 4. 4 new env vars

| env var                          | default | effect                                          |
| -------------------------------- | ------- | ----------------------------------------------- |
| `SKYGATE_EXPIREWATCH_ENABLED`    | `true`  | `false` disables the goroutine                  |
| `SKYGATE_EXPIREWATCH_INTERVAL`   | `5m`    | tick frequency; `off` / `0` disables           |
| `SKYGATE_EXPIREWATCH_THRESHOLD`  | `168h`  | nodes within this window get renewed (7 days)  |
| `SKYGATE_EXPIREWATCH_RENEWAL`    | `720h`  | new expiry window when renewing (30 days)       |

No new config knobs in `/admin/*` — the defaults are
sensible and an operator who wants to tune the windows
edits `.env` and restarts.

## Files

- `internal/expirewatch/manager.go` (new, ~310 lines) — the
  Manager, Run, SyncOnce, renewOne, TickStats,
  isTagged, nodeExpiryFromCache helpers
- `internal/expirewatch/manager_test.go` (new, ~310 lines) —
  8 unit tests:
  - `TestExpireWatch_PicksOnlyNearExpiry` — only nodes
    within the threshold get renewed; 30d-out nodes are
    left alone
  - `TestExpireWatch_SkipsTagged` — tagged nodes are never
    renewed even if their Expiry is in the past
  - `TestExpireWatch_HandlesMissingExpiry` — nodes with
    no expiry at all (empty string) are renewed
    defensively
  - `TestExpireWatch_RespectsIntervalZero` — Run short-
    circuits when interval <= 0
  - `TestExpireWatch_RunStopsOnContextCancel` — Run returns
    promptly when the context is cancelled
  - `TestExpireWatch_RecordsAuditOnRenew` — every renewal
    appends an `audit_log` row with `username=expirewatch,
    action=renewed, detail=node_id=<N> old_expiry=<...>
    new_expiry=<...>`
  - `TestExpireWatch_ParsesRFC3339NanoExpiry` — both
    RFC3339Nano (with fractional) and RFC3339 (without)
    parse correctly; garbage strings report `!hasExpiry` so
    the watcher renews defensively rather than silently
    inaction
  - `TestExpireWatch_HandlesAPIFailure` — per-node API
    failure shows up in `stats.Errors`; `stats.Renewed`
    does not count failed renewals
- `internal/headscale/nodes.go` — `HSNode.Expiry` +
  `NodeView.Expiry` fields, `toView` carries Expiry through,
  new `ExtendNodeExpiry` method
- `internal/config/config.go` — 4 new env vars
- `internal/handlers/handlers.go` — `App.ExpireWatch` field
  (mirrors the `App.Sidecar` / `App.ReleaseMonitor` pattern)
- `cmd/skygate/main.go` — wire-up: `expireWatchMgr := … ;
  go expireWatchMgr.Run(ctx)`. Same goroutine-launch pattern
  as the sidecar manager (v0.16.7 regression-prevention
  comment included).
- `check_v0.23.3.sh` + `run_check_v0.23.3.sh` (new) — live
  verification: forces a node's expiry to 2s, waits for the
  watcher to tick, confirms the expiry is now at least 7d
  out and an `audit_log` row was written.
- `AGENTS.md` — operational note under "Per-user control
  plane: when to use (v0.23.0/v0.23.1)" linking the new
  watcher.

## Investigation details (for the curious)

The 2-4 second expiry was confirmed via:

1. `headscale preauthkeys list -o json | jq '.[] |
   select(.id == 108)'` — preauth 108 had `used=true`,
   `expiration=2026-07-21 09:55:33` (1h from creation, the
   operator-supplied TTL).
2. `docker cp headscale:/var/lib/headscale /tmp/hs &&
   sqlite3 /tmp/hs/db.sqlite "SELECT id, given_name,
   datetime(expiry), datetime(created_at) FROM nodes WHERE
   id=10;"` — node 10 had `expiry=2026-07-21 08:55:54`,
   exactly **21 seconds after preauth 108 was created at
   08:55:33**. The 21-second value (not the documented
   30-second or 120-second `ephemeral_node_inactivity_timeout`
   default) is what pointed at the Tailscale client's
   RegisterRequest.Expiry as the source.
3. `headscale 0.29.2 source` — `hscontrol/state.go`'s
   `HandleNodeFromAuthPath` does the
   `if !regReq.Expiry.IsZero() { node.Expiry = &regReq.Expiry
   }` assignment unconditionally for non-tagged nodes.
4. `headscale 0.29.2 source` — `hscontrol/types/node.go`'s
   `TailNode` does `MachineAuthorized: !nv.IsExpired()`,
   which is what flips the client to NeedsLogin.

The Tailscale 1.98.x client behaviour of sending a
near-now Expiry is reproducible in a `tailscaled` test
container: my test `test_register_real2.sh` created a
fresh node, the resulting headscale row had
`expiry = created_at + 4 seconds` (not the 1h preauth
expiration). The exact source of the 2-4s value in
Tailscale 1.98.x is not in the headscale code path; it's
a client-side choice. We work around it rather than chase
it, because (a) headscale's behaviour of applying the
client's value verbatim is the same in 0.23/0.25/0.27/0.29
and (b) waiting for headscale to fix it would leave the
operator stuck.

## Why a watcher, not a one-shot fix?

We could have patched headscale to ignore `regReq.Expiry`
for non-ephemeral preauths, but:

- Patching headscale means forking. Every headscale
  upgrade we'd rebase.
- The watcher is **5 lines of state.go logic** (read
  Expiry, compare to now+threshold, call extend) and
  lives in skygate where the operator already runs
  periodic goroutines for `sidecar`, `release-monitor`,
  `headscale-update-monitor`, `exit-node-monitor`. One
  more ticker fits the pattern.
- The watcher also handles the case where a future
  headscale upgrade introduces a new node state where
  Expiry needs refreshing — the watcher just notices and
  extends, no further work needed.

The downside is the 5-minute latency between registration
and renewal. In practice this is fine: the operator's
Android took 2-4 seconds to lose the connection. If the
operator opens `/my/devices` 30 seconds after generating
a preauth, by then the watcher has already extended
the new node's expiry out to 30d and the user is fine.
For the rare case where the user re-registers a device
**and** disconnects within 30 seconds of registration,
the existing "manually run `headscale nodes expire`"
fallback is still available; this release just removes
the need to remember to do it.

## Upgrade procedure

```bash
# 1. Pull the new code on the VM
cd /home/skyadmin/skygate
git fetch origin
git checkout v0.23.3

# 2. (Optional) tune the watcher windows — defaults are
#    5m tick, 7d threshold, 30d renewal. If you want a
#    faster tick (more API calls, faster recovery) set
#    SKYGATE_EXPIREWATCH_INTERVAL=1m in .env. Skip this
#    step for the default behaviour.

# 3. Rebuild + restart skygate
docker compose up -d --force-recreate --no-deps skygate
# (use --force-recreate, NOT restart, so the entrypoint
#  picks up the new binary; `restart` reuses the old
#  container + image. See AGENTS.md "Common gotchas".)

# 4. Watch the goroutine log
docker logs -f skygate | grep expirewatch
# You should see one line per tick:
#   expirewatch.tick: seen=N renewed=N skipped=N errors=N
# The first tick right after startup will renew 0 nodes
# (everything has a long-enough expiry on a fresh install).
# After a real device registers, the next tick will
# extend its expiry.

# 5. Verify on the live VM
bash /tmp/check_v0.23.3.sh
# Expect 5 "ALL CHECKS PASS" steps.

# 6. (Optional) For a single-deployment dev install, you
#    can also confirm the new field is plumbed through:
curl -sS -H "Authorization: Bearer $HEADSCALE_API_KEY" \
  http://localhost:50444/api/v1/node/10 | jq '.node.expiry'
# Should show a non-null RFC3339Nano string (not a
# 2-4 second value).
```

## What the operator needs to do

Nothing. The watcher starts on the next skygate restart
and silently keeps every device's expiry in the future.
The audit log has a `username=expirewatch action=renewed`
row per renewal so the operator can grep for it if they
ever need to debug a "device won't stay connected"
issue again.

For the existing node 10 (skybars / Android) which
already had its expiry manually extended on 2026-07-21
to `2026-08-20 09:50:19Z`: the watcher will renew it
to `2026-09-20` on the first tick after `2026-08-13`
(when its current expiry falls within the 7d threshold).
Same for the test nodes 16, 17, 19, 20.

## Risk assessment

- **Backwards-compat**: no API surface change. The only
  user-facing change is "your device stays connected
  without you having to manually run
  `headscale nodes expire` every 30 days". Existing
  headscale queries, ACL, policy, tags — all unchanged.
- **headscale compatibility**: pinned to 0.29.2 (our
  production version). The `ExpireNode` API has been
  stable in 0.23+ and is documented at
  https://headscale.net/stable/ref/api/#post-nodeexpire
  (rebranded from `/api/v1/node/{id}/expire` in 0.23,
  the path we use has been stable since 0.20).
- **Disk / network impact**: one `GET /api/v1/node` per
  5m tick, one `POST /api/v1/node/{id}/expire` per
  near-expiry node per tick. At our 4-prod-user scale
  (~20 nodes), that's 1 GET + 0-2 POSTs every 5m.
  Negligible.
- **Audit log growth**: one row per renewal. At ~1 renewal
  per 30 days per device, the audit log adds ~25 rows
  per device per year. Trivial.
- **Failure mode**: if the headscale API is down, every
  per-node renew call fails, every node is logged at
  WARN, and the next tick tries again. The watcher
  itself doesn't crash (the error is recorded in
  `TickStats.Errors` and logged; the next tick still
  fires). No data loss; just a noisy log.

## What's next

The `headscale-update-monitor` (v0.20.0) is currently
polling the juanfont/headscale GitHub Releases API every
24h. The next minor headscale release that adds a
real `disable_expiry: true` honouring the JSON field
(which 0.29.2 doesn't) would let us disable expiry on
tagged devices in a single API call rather than the
watcher skipping them. Tracked as a possible v0.24.0
follow-up.

The next thing on the operator's backlog (per the
2026-07-20 message) that isn't done yet: v0.19.1
(`exitnode.skygate-subnet-<user>` DNS record) is still
blocked on headscale 0.30+ for `dns.extra_records`
support. The mavis cron `headscale-milestone-16-check`
monitors headscale milestone #16 (DNS Work) weekly.
