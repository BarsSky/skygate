# headscale versions: what skygate can do, and how to move forward (B495)

**Measured 2026-11-18.** Answers here are measurements, not assumptions — the point of this page is
that "blocked on headscale 0.30" was carried as folklore for months while nothing in the tree said
which version unlocks which capability.

## What is running, and what exists

| Where | Version | How it was measured |
|---|---|---|
| The production panel host | **v0.29.3** | `headscale version` on the host (`commit 5aff68b5…`, built 2026-07-29) |
| Upstream, newest release | **v0.29.7** (2026-10-30) | `gh api repos/juanfont/headscale/releases/latest` |
| Upstream, the "0.30" line | **does not exist yet** | the release list ends at `v0.29.7`; there is no `v0.30*` tag |

So the per-user DNS work (TD-5 / RR-5) is still genuinely blocked — but on a fact that can be
re-checked in one command, not on a rumour:

```bash
gh api repos/juanfont/headscale/releases/latest --jq '.tag_name'   # v0.30.x would unlock RR-5
headscale version                                                   # what THIS host runs
```

## The capability table (in code, not prose)

`internal/headscale/compat_b495.go` answers "what can this version do" as a pure function, and the
panel logs it at boot (`headscale-capabilities: …`):

| Capability | Minimum | What it unlocks |
|---|---|---|
| `PolicyDatabase` | **0.26** | the REST policy write path; below that skygate can only write the policy file through the privileged applier (B272) |
| `ExtraRecords` | **0.30** | `dns.extra_records`, which is what a per-user `exitnode.<user>.<domain>` record needs (**TD-5 / RR-5**) |

Rules the table enforces:

* an **unparseable** version claims **nothing** (`Known=false`) — "I could not read it" must never
  mean "assume the newest", because that is how a panel enables a feature on an old daemon;
* a **pre-release** of a line (`0.30.0-rc.1`) counts as that line;
* a later minor or major cannot lose a capability;
* patch releases never change the answer.

## Upgrading 0.29.3 → 0.29.7 (available now, no capability change)

This is the routine move and it does **not** unlock RR-5. It is still worth doing: it carries upstream
fixes in the same API surface skygate drives.

1. **Snapshot first.** `headscale nodes list -o json`, `headscale users list -o json`,
   `headscale policy get -o json` and the panel's `acl_snapshots` row (B295 already stores the last
   compared policy) — those four are the reference for "did anything change".
2. **Stop, replace the binary, start.** Native: stop `headscale`, replace `/usr/local/bin/headscale`
   with the release tarball binary, `systemctl start headscale`, then `headscale version`.
3. **Re-run the panel's own view of the world** — `bash scripts/live_gate.sh` — and compare with the
   snapshot: `dup-users`, `orphans`, `tag-owners`, `attribution` must be unchanged, and `b188.2`
   (the live per-CIDR `via` read) is the one that will catch a policy-format change.
4. **Rollback** is the previous binary plus the config the host already had; the database schema of
   0.29.7 is the 0.29 schema, so nothing is migrated and nothing is lost. Keep the old binary until
   the live gate is green.

## When 0.30 lands: the plan (TD-5 / RR-5)

Prepared now so the transition is a checklist, not a scramble:

1. **Re-check the release** (`gh api …/releases/latest`). If the newest release is still 0.29.x, stop:
   nothing in this page changes.
2. **Read the 0.30 upgrade notes before touching the host.** The project's own lesson (B272) is that
   0.29.x already broke `tagOwners` wildcards and moved route approval out of REST — a minor bump has
   historically changed the policy surface skygate drives.
3. **Upgrade the daemon on a non-production host first** (the LAN box is fine), then run
   `bash scripts/live_gate.sh --strict` there. `--strict` is the point: a SKIP must fail the run, so
   "we could not check" cannot pass for "we checked".
4. **Write the per-user records behind the gate.** The feature asks
   `CapabilitiesFor(version).ExtraRecords`; on 0.29.x it must stay off (the policy would be rejected),
   on 0.30+ it writes `dns.extra_records` and then verifies the record with a DNS query, not by
   trusting the write.
5. **Keep 0.29 support.** skygate ships ONE binary for both lines: every 0.30-only field is additive
   and gated, and the policy generator must emit a document that 0.29.x still accepts when the
   capability is absent. A contract pins that both branches are reachable from the same code path.
6. **Rollback** is the previous headscale binary plus its own config; the panel tolerates the absence
   of a capability by design (that is what `Known=false` / `ExtraRecords=false` mean).

## What this page deliberately does not promise

* A version number that has never been released. The table names `0.30` because that is the plan the
  project has carried; the constant moves when the release exists and the upgrade notes have been
  read, not before.
* That a capability implies a working feature. `ExtraRecords` says the daemon accepts the field; the
  feature still has to verify the resulting DNS answer.
