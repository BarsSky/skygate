# Architecture

Internal architecture documents — how skygate, headscale, the PG
database, DERP relays, and headplane fit together.

## Conventions

- These docs explain the live architecture, not aspirational
  designs. Designs that aren't built yet live in `docs/plans/`.
- Diagrams, when useful, use ASCII art (so they render in any
  text viewer and survive git diff cleanly).

## Files

| File | Topic |
|---|---|
| `modules.md` | Internal module map (`internal/<package>` per-file responsibilities) |
| `cluster-management.md` | Multi-VM cluster management (headscale control plane failover) |
| `ha-architecture.md` | High-level HA architecture overview |
| `wal-g-notes.md` | PG WAL-G backup notes + restore procedure reference |

## See also

- `docs/architecture.md` — the public-facing architecture
  overview (lower detail, RFC 5737 IPs only)
- `docs/runbooks/` — operator-facing runbooks
- `docs/internal/runbooks/` — internal runbooks (operator-specific)
