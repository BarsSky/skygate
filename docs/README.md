# Skygate documentation

This directory holds Skygate's documentation. **Files here are
public** — they describe Skygate in generic terms, using RFC 5737
example IPs (`192.0.2.x`, `198.51.100.x`, `203.0.113.x`) and the
Tailscale standard range (`100.64.0.0/10`) instead of any
operator-specific value. Operator-specific deployment notes live
under [docs/internal/](internal/README.md).

## Public docs

| File | What it covers | Audience |
|---|---|---|
| [api.md](api.md) | Every HTTP endpoint, query/response shapes, curl examples, rate-limit headers | API consumers (AI agents, scripts, integrations) |
| [architecture.md](architecture.md) | Component map, data flow, deployment topologies | Operators, new contributors |
| [BACKLOG.md](BACKLOG.md) | Deferred / back-burner items, blocked features, long-term plans | Contributors, operators |
| [bot-message-style-v0.15.2.md](bot-message-style-v0.15.2.md) | Telegram bot message formatting (HTML / markdown / i18n) | Bot UI maintainers |
| [db-schema.md](db-schema.md) | All DB tables + columns + foreign keys + indices | DBAs, migration authors |
| [deploy.md](deploy.md) | Full deployment guide (env vars, deploy.sh, restore, DERP) | Operators doing fresh install |
| [derp.md](derp.md) | DERP relay integration (bundled + existing modes) | Operators |
| [disaster-recovery.md](disaster-recovery.md) | DR runbook: backup, restore, single-VM recovery | Operators |
| [fa-test-plan.md](fa-test-plan.md), [fa-test-report-v0.26.0.md](fa-test-report-v0.26.0.md) | Functional-acceptance test plan + the v0.26.0 report | QA, release managers |
| [headplane.md](headplane.md) | Headplane sidecar integration contract (version pinning, optional mode) | Operators |
| [refactor-v0.30-postmortem.md](refactor-v0.30-postmortem.md) | What refactor-v0.30 did, lessons learned | Contributors |
| [runbooks/pg-failover.md](runbooks/pg-failover.md) | PostgreSQL HA failover runbook | On-call operators |
| [runbooks/v1.5.0-ha-and-deploy.md](runbooks/v1.5.0-ha-and-deploy.md) | v1.5.0 HA chain + certsync + /admin/{ha,certificates,deploy} + skygate deploy CLI runbook | On-call operators |
| [skygate-as-shell.md](skygate-as-shell.md) | Long-term roadmap for pluggable Headscale / multi-control-plane / ACL import | Architects |
| [SYNC.md](SYNC.md) | AI agent sync workflow (how to bring an agent up to speed) | AI assistants |
| [TELEGRAM.md](TELEGRAM.md) | Telegram bot config + every command + the i18n catalog | Bot operators, integrators |
| [v0.16.0-open-questions.md](v0.16.0-open-questions.md) | The 8 design decisions for v0.16.0 per-user subnets (resolved) | Architects, contributors |
| [v0.33.0-pg-cutover-runbook.md](v0.33.0-pg-cutover-runbook.md) | The PostgreSQL cutover runbook (v0.32.x → v0.33.0) | Operators doing the PG cutover |
| [windows-client.md](windows-client.md) | Tailscale Windows client setup + metrics + exit-node UX | Windows admins |

## Public design / plan docs

| File | What it covers |
|---|---|
| [plans/pg-migration-handling.md](plans/pg-migration-handling.md) | The v0.27.0 → v0.31.0 PG migration plan (per-driver placeholders, build-tag dispatch) |
| [plans/refactor-v0.30.md](plans/refactor-v0.30.md) | The v0.30 refactor plan (handlers split into `internal/feature/*`) — succeeded |
| [plans/self-update-v0.29.md](plans/self-update-v0.29.md) | The v0.29 self-update orchestrator plan — succeeded |

## Operator-specific docs (move to `docs/internal/`)

If you write a doc with operator-specific IPs, hostnames, or paths
(`/home/<user>/...`, Tailscale `100.64.100.x`, real public IPs),
put it under [docs/internal/](internal/README.md), **not** here.
Public docs must be portable across operators — a fresh user on a
fresh VM should be able to follow the same doc without a "replace
this IP with yours" step.

The rule of thumb: **if the doc works on a different VM without
changes, it's public. If it has "this is for our VM at X", it's
internal.**

## Personal-data sweep

The last sweep was 2026-08-06 (commit `73984fe`). The sweep
checked all `.md` and `.go` files in the tree against 19 regex
patterns (operator IPs, hostnames, Tailscale IPs, usernames,
specific workstation names). Zero hits in the public tree
(`README.md`, `README.ru.md`, `CHANGELOG.md`,
`RELEASE-NOTES.md`, all files in this directory except
`docs/internal/`).
