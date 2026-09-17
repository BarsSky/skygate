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
| `pg-cutover.md` | PG cutover runbook (v0.33.0) |
| `pg-failover.md` | PG failover + recovery procedure |
| `svyatoslava-bootstrap.md` | First-time bring-up of the svyatoslava VM |
| `tailnet-split-fix.md` | Tailnet split-fix procedure (when the tailnet appears partitioned) |
| `v1.5.0-ha-and-deploy.md` | v1.5.0 HA + deploy runbook (top-level entry point) |

## See also

- `docs/internal/runbooks/` — operator-specific runbooks (real
  LAN IPs, real secret paths)
- `docs/features.md` — user-facing feature documentation
- `docs/deploy.md` — high-level deploy guide
