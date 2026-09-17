# Internal documentation

This directory holds skygate documentation that's specific to a
particular deployment (real LAN IPs, real LAN hostnames, real
secrets locations). Public-facing docs live one level up at
`docs/`.

## Conventions

- All example values in these docs use RFC 5737 IPs
  (`192.0.2.1`, `198.51.100.1`, `203.0.113.1`) for any IP shown
  in a code block or diagram, but the prose often references
  operator-specific paths (`/home/skyadmin/skygate/`,
  `/etc/skygate-secrets/`). Don't conflate the two — the IPs
  are placeholders, the paths are real conventions.
- Runbooks that need an operator's secrets point at
  `/etc/skygate-secrets/<name>` rather than embedding values.
- Secrets never enter the repo, even as placeholders.

## Layout

| Subdir | Purpose |
|---|---|
| `audits/` | Read-only audit reports — what was the state, what we found, what we recommended |
| `runbooks/` | Operator-facing runbooks — step-by-step recipes for working on a live deployment |
| `architecture/` | Internal architecture docs — how skygate, headscale, PG, DERP, headplane fit together |
| `postmortems/` | Incident postmortems — what broke, root cause, fix, lessons |
| `historical/` | Older investigations, reports, plans — superseded by current docs but kept for archaeology |

See each subdir's README for its file index.

## See also

- `docs/README.md` — public-facing docs index (no real IPs, no
  secrets, RFC 5737 / example.com only)
- `AGENTS.md` — project-wide AI assistant instructions + the
  per-B-fix history
- `BACKLOG.md` — abandoned / blocked / in-progress features
