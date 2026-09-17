# Current design plans

In-flight design plans — proposals that have been written down but
not yet (or only partially) implemented. Each plan carries a
**Status** header that says where it stands.

When a plan is implemented, the plan file moves to
`docs/internal/historical/` and the implementation is documented
in `AGENTS.md` under the corresponding B-fix entry.

## Files

| File | Topic | Status |
|---|---|---|
| `pg-migration-handling.md` | DB migration handling for PostgreSQL (driver split, runtime checks, CI scaffolding) | Phase 1 partial (B11/B12 in `verify_pre_deploy.sh`, helper scaffolding in `internal/db/pgmigrate`); R26 + testcontainers deferred until v0.27.0 PG driver lands |
| `td-11-cloudflare-grouping.md` | Cloudflare /12+/24 rule grouping (collapse the 30% of rules that hit CDN-tracked hosts) | DEFERRED (operator priority LOW per PLANS.md) |

## See also

- `docs/internal/historical/` — plans that have been implemented or
  superseded (kept for archaeology)
- `BACKLOG.md` — abandoned / blocked / in-progress features (coarser
  granularity than plans)
- `superpowers/plans/` — B-mod plans (release flow + pre-deploy
  hooks)
