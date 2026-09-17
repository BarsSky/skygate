# Audits

Read-only audit reports — what was the state at a moment in time,
what did we find, what did we recommend.

## Conventions

- Audits are immutable once written. If a recommendation is
  acted on, the action is documented in the matching B-fix
  section in AGENTS.md (or in a follow-up audit if the change
  was complex enough to warrant its own analysis).
- Each audit's filename is a topic slug, not a date. The
  "Last updated" header inside the doc carries the date.

## Files

| File | Topic | When |
|---|---|---|
| `tailnet-fixes.md` | Tailnet issues found during pre-1.5.0 stabilisation | 2026-09-04 |
| `skygate-adoption.md` | Adoption flow when skygate ships next to an existing headscale | 2026-09-11 |
| `doc-audit.md` | Documentation audit: README ↔ code ↔ i18n ↔ scripts ↔ templates | 2026-09-14 |
| `slow-exit-node.md` | "Slow exit node" symptom root-cause analysis | 2026-09-14 |

## See also

- `docs/internal/postmortems/` — incident postmortems (what broke,
  how we fixed it, what we learned)
- `docs/internal/historical/` — older investigations and reports
  kept for reference, but superseded by current docs
