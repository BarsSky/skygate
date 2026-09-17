# Postmortems

Incident postmortems — what broke, how it was detected, what the
root cause was, how it was fixed, and what we learned.

## Conventions

- Postmortems follow the "5 Whys" structure: symptom → detection
  → root cause → fix → prevention. The fix is implemented in a
  B-commit; the prevention is usually a test + a B-check.
- Files are named by the version that contained the fix (or the
  version in which the incident was first observable), not by
  date. The "Last updated" header inside the doc carries the
  date.

## Files

| File | Topic | Version |
|---|---|---|
| `v0.30-refactor.md` | Postmortem of the v0.30 refactor (what was learned about the codebase structure) | v0.30 |
| `v0.27.0-postgres-ha.md` | PG-HA initial bring-up: what went wrong, how it was stabilised | v0.27.0 |

## See also

- `docs/internal/audits/` — pre-event audits (what's the state,
  what's broken) — not the same as a postmortem (what happened)
- `AGENTS.md` — B-fix entries reference postmortems when the
  change was non-trivial
