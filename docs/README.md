# Skygate documentation

Catalogue of every document in this directory. Start here, then follow the link
for your role.

Russian translations live in [`docs/ru/`](ru/) — `README`, `INSTALL`, `UPDATE` and
`ROADMAP` are maintained in both languages; for everything else the English file is
authoritative.

---

## 1. I want to…

| I want to… | Read |
|---|---|
| **Install Skygate** (docker, systemd, OpenRC, bare binary, Windows, tarball) | [`INSTALL.md`](INSTALL.md) · [`ru/INSTALL.md`](ru/INSTALL.md) |
| **Update an existing install** (in-app, scheduled, native self-update with rollback, mirrors, manual) | [`UPDATE.md`](UPDATE.md) · [`ru/UPDATE.md`](ru/UPDATE.md) |
| See **what is planned / blocked / owed** | [`ROADMAP.md`](ROADMAP.md) · [`ru/ROADMAP.md`](ru/ROADMAP.md) |
| See **what shipped in a release** (one canonical file, newest first) | [`../RELEASE-NOTES.md`](../RELEASE-NOTES.md) |
| Understand **why something is the way it is** (incidents, root causes, traps) | [`LESSONS.md`](LESSONS.md) |
| **Run a release**, deploy, migrate PostgreSQL, bootstrap a host | [`operations.md`](operations.md) |
| Set up **high availability** (active/passive, failover, clustering) | [`ha.md`](ha.md) |
| Put Skygate behind **HTTPS / a reverse proxy** | [`https.md`](https.md) |
| Wire **OIDC** between headscale and Skygate | [`oidc.md`](oidc.md) |
| Work on **subnet routers, exit nodes, DERP** | [`networking.md`](networking.md), [`derp.md`](derp.md) |
| **Diagnose a live problem** (devices offline, 404s, stuck tags, slow exit node) | [`troubleshooting.md`](troubleshooting.md) |
| Change **the code** (package map, invariants, B-check contracts) | [`internals.md`](internals.md), [`architecture.md`](architecture.md) |
| Look up **every environment variable** | [`deploy.md`](deploy.md) |
| Look up a **DB table / column** | [`db-schema.md`](db-schema.md) |
| Call the **REST API** | [`api.md`](api.md) |
| Understand **ACL rules** | [`acl-rules-reference.md`](acl-rules-reference.md) |
| **Back up / restore / move** a database | [`backup-restore-and-migration.md`](backup-restore-and-migration.md), [`disaster-recovery.md`](disaster-recovery.md) |
| Configure the **Telegram bot** | [`TELEGRAM.md`](TELEGRAM.md) |
| Set up the **Windows client** | [`windows-client.md`](windows-client.md) |
| Run **headplane** or **sidecar mode** | [`headplane.md`](headplane.md), [`sidecar-mode.md`](sidecar-mode.md) |
| See **what the project does**, feature by feature | [`features.md`](features.md) |
| Get **AI-agent instructions / conventions** | [`../AGENTS.md`](../AGENTS.md) |

---

## 2. Layout

Flat on purpose: one file per topic, no nested plan/runbook trees. Planning lives
in a single roadmap, operational procedures live in a single handbook, and history
lives in git.

```
docs/
├── README.md                        ← you are here (EN catalogue)
├── ru/                              Russian translations
│   ├── README.md                    RU front page
│   ├── INSTALL.md                   RU install guide
│   ├── UPDATE.md                    RU update guide
│   └── ROADMAP.md                   RU roadmap
│
├── INSTALL.md                       every install method (10 paths)
├── UPDATE.md                        every update path (in-app → manual → mirror)
├── ROADMAP.md                       current work / next / blocked / tech debt
├── LESSONS.md                       incidents, root causes, recurring traps
├── operations.md                    release + deploy + PG cutover + bootstrap
├── gate-regression-power.md        what the B-check catalog can and cannot catch
├── gate-audit-b001-b100.md      per-slice regression-detection audit
├── gate-audit-b101-b199.md      per-slice regression-detection audit
├── gate-audit-b200-b262.md      per-slice regression-detection audit
├── gate-audit-b263-b290.md      per-slice regression-detection audit
├── gate-audit-b291-b320.md      per-slice regression-detection audit
├── internals.md                     package map, invariants, B-check system
├── ha.md                            HA topology, failover, open questions
├── https.md                         TLS, reverse proxies, certificates
├── oidc.md                          OIDC: skygate ⇄ headscale
├── networking.md                    subnet routers, exit nodes, DERP
├── troubleshooting.md               symptom-driven diagnostics
├── deploy.md                        env-var + deployment reference
├── architecture.md                  component/data-flow overview
├── api.md                           REST API reference
├── features.md                      implemented feature catalogue
├── db-schema.md                     database schema
├── acl-rules-reference.md           ACL generation rules
├── backup-restore-and-migration.md  backup, restore, cross-backend moves
├── disaster-recovery.md             DR tiers and procedures
├── derp.md                          DERP relay setup + operations
├── headplane.md                     headplane integration
├── sidecar-mode.md                  Skygate as a sidecar process
├── TELEGRAM.md                      Telegram bot
├── windows-client.md                Windows Tailscale client
└── scripts/                         first-time client setup scripts
```

**Removed in the 2026-09-18 restructure:** the `plans/`, `runbooks/`, `internal/`
and `superpowers/` trees plus `BACKLOG.md` and `PLANS.md`. Their durable content is
folded into `ROADMAP.md`, `LESSONS.md`, `operations.md`, `ha.md`, `https.md`,
`oidc.md`, `networking.md`, `troubleshooting.md` and `internals.md`; the original
text is one `git log --diff-filter=D` away.

---

## 3. Reading order

**New operator**

1. [`INSTALL.md`](INSTALL.md) — get it running with the method that fits your host.
2. [`deploy.md`](deploy.md) — the environment variables you will actually tune.
3. [`UPDATE.md`](UPDATE.md) — how updates work *before* you need one.
4. [`troubleshooting.md`](troubleshooting.md) — keep it bookmarked.
5. [`operations.md`](operations.md) + [`backup-restore-and-migration.md`](backup-restore-and-migration.md)
   + [`disaster-recovery.md`](disaster-recovery.md) — day-2 duties.
6. [`ROADMAP.md`](ROADMAP.md) — what is intentionally not done yet.

**New contributor**

1. [`../AGENTS.md`](../AGENTS.md) — conventions, invariants, the block index.
2. [`internals.md`](internals.md) — where the code lives and what it guarantees.
3. [`architecture.md`](architecture.md) + [`features.md`](features.md) — the shape
   and the behaviour to preserve.
4. [`LESSONS.md`](LESSONS.md) — the traps that already cost time.
5. [`ROADMAP.md`](ROADMAP.md) — where the project is heading.

---

## 4. Conventions

* Every example uses placeholder values: `<VM_HOST>` for the operator's own server,
  `<PUBLIC_HOST>` / `example.com` for hostnames, RFC 5737 addresses
  (`192.0.2.1`, `198.51.100.1`, `203.0.113.1`) for IPs, and obviously-fake secrets.
  **Never commit a real private IP, hostname or credential** — a pre-commit hook
  rejects the known leak patterns.
* Commands are written for a bash-compatible shell on Linux unless a section says
  otherwise (PowerShell for Windows).
* If a document and the code disagree, the code wins — please fix the document in
  the same change.
* Operational procedures state their verification step and their rollback; a
  procedure without a rollback is incomplete.
* Version-specific behaviour is called out inline (for example "v1.5.9 and newer"),
  so a reader on an older release knows what does not apply to them.
