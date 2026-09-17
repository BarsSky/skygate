# Historical

Older reports, investigations, plans, and design docs that are
**superseded by current docs**. Kept in the repo so:
- New operators can read the history and understand WHY the
  current shape is the way it is.
- Search-driven archaeology (grep for a now-deleted feature)
  still finds the original discussion.

## Conventions

- These docs are **read-only**. If you find an inaccuracy in
  a historical doc, do NOT edit it in place — add a "Correction
  note" section at the top pointing at the current doc + the
  commit that superseded it.
- Don't delete historical docs without a B-fix. The git history
  is a backup; the in-repo copy is a search index.

## Files

| File | Topic | Last updated |
|---|---|---|
| `bot-message-style.md` | Telegram bot message style guide (old) | v0.15.2 |
| `derp-latency-report.md` | DERP relay latency analysis from a past incident | 2026-08-25 |
| `fa-test-plan.md` | Functional-acceptance test plan for v0.x (deprecated) | pre-v0.27 |
| `fa-test-report-v0.26.0.md` | v0.26.0 FA test report (superseded by CI B-checks) | v0.26.0 |
| `headscale-duplicate-name-behavior.md` | Investigation of headscale 0.x duplicate-name semantics | 2026-08-25 |
| `install-dry-run-report.md` | First-ever clean-install dry-run on a real VM | 2026-09 |
| `node-404-investigation.md` | "Node 404" symptom — initial investigation | 2026-08-25 |
| `node-404-investigation-v2.md` | "Node 404" symptom — follow-up with the resolved root cause | 2026-08-25 |
| `refactor-v0.6.0-plan.md` | Plan for v0.6.0 refactor (executed) | pre-v0.6.0 |
| `skygate-as-shell.md` | "skygate as a unix shell" design exploration | 2026-07 |
| `sync.md` | Old "sync protocol" notes (replaced by the live-deploy sync) | pre-v0.20 |
| `tailnet-diagnostics.md` | Tailnet diagnostic procedures (pre-B170 hint classifier) | pre-v1.5.2 |
| `v0.16.0-open-questions.md` | Open questions at the v0.16.0 design checkpoint | v0.16.0 |

## See also

- `docs/internal/audits/` — recent audits (still actionable)
- `docs/internal/postmortems/` — incident postmortems
- `docs/plans/` — current design plans (not historical)
