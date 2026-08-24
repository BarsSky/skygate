# docs/internal/

**These files contain operator-specific data** — Tailscale IPs,
hostnames, deployment notes tied to one specific VM, personal
script names. They were moved out of the public `docs/` tree on
2026-08-06 as part of the v0.32.29 "no hardcoded personal data
in code" policy extension to documentation.

## What lives here

| File | What's inside | Why internal |
|---|---|---|
| `ha-active-router.md` | HA architecture exploration, executive summary referencing a specific workstation hostname | Documents one specific operator's HA design; the architecture is interesting but the hostname / IP aren't generic |
| `ha-architecture.md` | HA architecture overview, references one specific public IP | Same as above |
| `v0.27.0-postgres-ha.md` | PostgreSQL HA design notes, references one specific public IP | Operator's PostgreSQL HA design — public docs for the same content are in `docs/v0.33.0-pg-cutover-runbook.md` |
| `subnet-router.md` | Per-user subnet-router setup with example LAN + Tailscale IP | References the operator's specific LAN |
| `https-setup.md` | Caddy + Let's Encrypt walkthrough with example Tailscale IP | Tailscale-specific setup |
| `telegram-relay.md` | Telegram egress relay operator guide with specific relay hostnames + paths | Names 3 specific relay nodes by their operator-given hostnames |
| `wal-g-notes.md` | (moved from `deploy/pg-ha/wal-g/README.md`) Wal-G backup/restore procedure with operator-specific paths and IPs | Operator's specific backup architecture |
| `oidc-headscale.md` | B161.4 headscale.conf snippet + e2e verification (with example Tailscale client) for the skygate OIDC provider | Generic runbook, but the "4 values that must match" table is specific to the skygate + headscale pairing |
| `plans/refactor-v0.6.0.md` | v0.6.0 refactor plan, historical | Historical planning doc, not relevant to current code |

## Maintenance

- **Do not** add new public docs here. If a doc is generally useful,
  put it in `docs/` and use RFC 5737 example IPs (`192.0.2.x`,
  `198.51.100.x`, `203.0.113.x`) and `100.64.0.0/10` (Tailscale
  standard) instead of operator-specific values.
- **Do not** reference `docs/internal/` from public docs without a
  strong reason. The public release notes / CHANGELOG / AGENTS
  should stay generic.
- When a private doc becomes generally useful, scrub the
  operator-specific data and **move it back to `docs/`**, not
  symlink. Symlinks break in git-checkout workflows.

## Audit

The last personal-data sweep was 2026-08-06 (commit `73984fe`).
Pattern set: 19 regex patterns covering IPs, hostnames,
usernames, and Tailscale IP ranges. Zero hits in the new
`docs/README.md` or any of the v0.33.1.17 release materials
(`README.md`, `README.ru.md`, `CHANGELOG.md`,
`RELEASE-NOTES.md`).
