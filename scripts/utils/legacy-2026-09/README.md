# Legacy debug scripts (2026-09)

These ~50 scripts were created during the 2026-08..2026-09
debugging sessions for:

- **svi polygon** (svyatoslava-1) — the B-mod series
  install / live-verify target. Reinstalled by the
  operator on 2026-09-09 and 2026-09-10 (twice), so most
  of these scripts reference hostnames / IPs / Tailscale
  state that no longer exist. Kept here for historical
  reference and for re-use if svi comes back with the
  same topology.
- **agent** (192.168.13.69) — dev/staging skygate.
  Was running bypass-PG (Patroni standalone) at the time
  the scripts were created. Most scripts are SSH-based
  paramiko probes to agent + svi.
- **headscale / headplane** — various ACL push / node
  tag / preauth-key probes during the B188 / B194 /
  B221 / B232 / B235.3 / B237 series.

## What's in here

| Category | Files | Purpose |
|---|---|---|
| **svi SSH / network** | `jump_svi.sh`, `add_svi_ssh_config.sh`, `ping_svyat.sh`, `resolve_svyat.sh`, `try_svi.sh`, `trace_svi.sh`, `sync_svi.sh`, `setup_jump_svi.sh` | SSH jump + host reachability for svi via karolina |
| **svi diagnostic** | `svi_check.sh`, `svi_diag2.sh`, `svi_full_diag.sh`, `svi_peer_test.sh`, `phase0_*.sh`, `phase0_on_svi.sh`, `phase0_tailscale_diag.sh`, `phase0_tailscale_diag2.sh`, `svyat_diagnosis.sh` | B-mod Phase 0 — verify svi prerequisites (Tailscale mesh, DNS, headplane, etc.) |
| **headscale probes** | `inspect_hs2.sh`, `inspect_hs_db.sh`, `kill_hs.sh`, `restart_hs.sh`, `audit_iptables.sh`, `audit_nft.sh`, `audit_subnet.sh`, `get_grants.sh`, `analyze_grants.sh` | headscale / headplane / iptables audit |
| **policy parsing** | `parse_policy.py`, `parse_policy2.py`, `policy2.json`, `policy_get.sh`, `policy_probe.sh`, `regen_policy.sh` | ACL policy JSON parsing + regen |
| **portscan / probe** | `portscan.sh`, `probe_13_20.sh`, `probe_13_20_v2.sh`, `probe_access.sh`, `probe_svyat.sh`, `multi_probe.sh`, `more_probe.sh`, `deep_probe.sh`, `derp_probe.sh` | Network reachability + derp probes |
| **install / restart** | `restart_svi_tailscale.sh`, `move_svi_user.sh`, `chmod_vm.sh` | Tailscale + skygate user/restart on svi |
| **cluster / mesh** | `check45.sh`, `count_runcheck.sh`, `quickcheck.sh`, `recheck.sh`, `recheck2.sh` | Cluster health micro-tests |
| **commit drafts** | `commit_msg.txt`, `commit_plan.txt`, `commit_plan2.txt` | Drafts of commits I never made |

## Why they're here, not deleted

1. **Audit trail** — these were the actual scripts I
   ran during the B-mod-core re-merge + svi polygon
   install (2026-09-09..10). Deleting them without a
   record loses the "what was tried" history.
2. **Reuse** — if svi comes back online, the `phase0_*`
   scripts are the first thing to re-run. The
   `svi_check.sh` is a 5-minute smoke test.
3. **Reference** — `parse_policy.py` is the only
   script that knows the ACL JSON shape; useful for
   future ACL audits.

## What I cleaned up (kept in repo root, not moved)

- `internal/`, `cmd/`, `deploy/`, `docs/`, `scripts/check_b_*.sh` — the real B-blocks
- `AGENTS.md`, `README.md` — operator-facing docs
- `go.mod`, `go.sum` — Go module files
- `.gitignore` — ignore rules
- `secrets/`, `*.env` — operator's credentials (NEVER in git)

## Date

Archived: 2026-09-10 (B-mod-bcheck housekeeping).
Author: Mavis (mavis@minimax.local).
