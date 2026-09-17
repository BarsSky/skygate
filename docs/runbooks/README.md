# Public runbooks

Operator-facing runbooks that don't reference real deployment
details (no real IPs, no real hostnames, no real secrets).
Real-deployment runbooks live in `docs/internal/runbooks/`.

## Files

| File | Topic |
|---|---|
| `clean-install.md` | Clean-install walkthrough (what to expect on a fresh VM) |
| `infra-retag.md` | Re-tagging existing infrastructure nodes (B111) |
| `issues-closeout.md` | GitHub issues close-out deployment log |
| `pg-failover.md` | PG failover + recovery procedure |
| `svyatoslava-bootstrap.md` | First-time bring-up of the svyatoslava VM |
| `tailnet-split-fix.md` | Tailnet split-fix procedure (when the tailnet appears partitioned) |
| `v1.5.0-ha-and-deploy.md` | v1.5.0 HA + deploy runbook (top-level entry point) |

**Note:** `pg-cutover.md` was moved to
`docs/internal/historical/pg-cutover-runbook.md` in 2026-09-17
because the PG cutover event completed in v1.3.x. The historical
copy is kept for reference but is no longer an active runbook.

## See also

- `docs/internal/runbooks/` — operator-specific runbooks (real
  LAN IPs, real secret paths)
- `docs/features.md` — user-facing feature documentation
- `docs/deploy.md` — high-level deploy guide
