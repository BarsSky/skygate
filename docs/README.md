# Skygate documentation

This directory holds the user-facing skygate documentation.
Operator-specific docs (with real LAN IPs, real secrets paths)
live in `docs/internal/`.

## Conventions

- All example values use RFC 5737 IPs (`192.0.2.1`,
  `198.51.100.1`, `203.0.113.1`) and the `example.com`
  placeholder domain.
- Code blocks show realistic commands, but every host/IP/secret
  in them is a placeholder. Replace before applying.
- If a doc has both a public + an internal version, the
  internal version is the source of truth — the public version
  is a redacted excerpt.

## Layout

```
docs/
├── README.md                       ← you are here
├── api.md                          REST API reference
├── architecture.md                 public architecture overview
├── backup-restore-and-migration.md backup + restore + cross-version migration
├── db-schema.md                    public DB schema (PG tables, columns, FKs)
├── deploy.md                       deploy guide (fresh install + updates)
├── derp.md                         DERP relay setup + operations
├── disaster-recovery.md            DR scenarios + procedures
├── features.md                     implemented feature catalogue
├── headplane.md                    headplane UI integration
├── sidecar-mode.md                 sidecar mode (skygate as a sidecar process)
├── TELEGRAM.md                     Telegram bot commands + integration
├── windows-client.md               Windows Tailscale client setup
├── BACKLOG.md                      abandoned / blocked / in-progress features
├── PLANS.md                        in-flight design plans
│
├── acl-rules-reference.md          end-user ACL rules reference (current logic)
│
├── runbooks/                       operator-facing runbooks (RFC 5737 IPs)
│   ├── README.md
│   ├── clean-install.md
│   ├── infra-retag.md
│   ├── issues-closeout.md
│   ├── pg-cutover.md
│   ├── pg-failover.md
│   ├── svyatoslava-bootstrap.md
│   ├── tailnet-split-fix.md
│   └── v1.5.0-ha-and-deploy.md
│
├── plans/                          current design plans
│   ├── pg-migration-handling.md
│   ├── td-11-cloudflare-grouping.md
│   ├── self-update-v0.29.md
│   └── refactor-v0.30.md
│
├── superpowers/plans/              superpowers B-mod plans
│   ├── 2026-09-11-b-mod-first-run-adoption.md
│   ├── 2026-09-11-b-mod-sqlite-pg-bidi.md
│   └── 2026-09-11-b-mod-static-embed.md
│
└── internal/                       operator-specific docs (real IPs)
    ├── README.md
    ├── audits/                     read-only audit reports
    ├── runbooks/                   internal runbooks
    ├── architecture/               internal architecture docs
    ├── postmortems/                incident postmortems
    └── historical/                 superseded historical docs
```

## Reading order for new operators

1. `features.md` — what the project does
2. `architecture.md` — how the pieces fit together
3. `deploy.md` — how to install
4. `runbooks/` — how to recover when things go wrong
5. `BACKLOG.md` — what's intentionally NOT done

## Reading order for new contributors

1. `AGENTS.md` (project root) — the AI-agent instructions
2. `architecture.md` + `docs/internal/architecture/` — how the
   code is organised
3. `BACKLOG.md` + `PLANS.md` — what direction we're heading
4. `features.md` — what to preserve when refactoring

## Internationalisation

Documentation is primarily **English** (in this directory). Russian
translations live in `docs/ru/` and follow the same path structure:

| English (primary) | Russian translation |
|---|---|
| `README.md` | `docs/ru/README.md` |
| `<topic>.md` (when translated) | `docs/ru/<topic>.md` |

If a topic only exists in `docs/<topic>.md` (no `docs/ru/<topic>.md`),
the English version is the only one — Russian is best-effort, English
is authoritative. The README at the repo root keeps its
`README.md` + `docs/ru/README.md` convention for GitHub's language switcher.
