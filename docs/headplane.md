# Headplane integration

> **Status**: optional sidecar UI, version-pinned. 2026-07-14,
> Этап 14 v11.

## What is Headplane?

[Headplane](https://github.com/tale/headplane) is a feature-complete
web UI for [Headscale](https://github.com/juanfont/headscale),
maintained by [tale](https://github.com/tale). It is a separate
project from Skygate — Skygate does not vendor or fork it, and
there is no code-level integration. The two services talk to
Headscale independently.

Skygate ships a Headplane sidecar in its docker-compose stack
because the same Headscale instance serves both. They share
`HEADSCALE_URL` and `HEADSCALE_API_KEY`; they do not share a
database, a session, or any application-level state.

## What Headplane does that Skygate doesn't

Skygate's per-user exit-rule CRUD writes a hand-built ACL via
`internal/acl/acl.go:GenerateACL()`. That covers the per-user
scenario well. It does NOT cover:

- **Visual ACL editing** — `CodeMirror`-based editor with
  HuJSON syntax highlighting, diff view, and validation.
  Skygate has no such UI; `/admin/acls` is a read-only viewer.
- **Machine management** — renaming machines, expiring them,
  changing ownership, approving advertised routes, drag-and-drop
  user assignment. Skygate's `/admin/devices` covers some of this
  but Headplane is the canonical tool.
- **OIDC** — Headplane supports OpenID Connect (Google, Auth0,
  etc.) for the admin UI login. Skygate's admin login is local
  password only (`SKYGATE_ADMIN_PASS`).
- **Browser SSH** (experimental) — Headplane ships a WASM
  terminal that can SSH into tailnet nodes via the agent. Skygate
  has no equivalent.
- **Custom DNS records** — split DNS, extra records, AAAA
  support, all via UI.

In other words: **Headplane is the operator's cockpit; Skygate
is the user's self-service portal.** They complement each other.

## Optional, not required

Skygate can run without Headplane. Set `HEADPLANE_ENABLED=false`
in `.env` and re-run `./deploy/deploy.sh`. The deploy script
strips the `headplane` service from `docker-compose.yml` and
skips the readiness check. No other Skygate functionality
depends on Headplane.

The default is `true` (the sidecar deploys by default) for
backward compat with existing installations. New deployments
can choose either mode.

## Use an existing Headplane (2026-07-15, v0.10.12)

If you already run Headplane somewhere (e.g. on a separate
VM, on Kubernetes, on another docker host) you can point
Skygate at it instead of starting a second instance. Two
env vars control this mode:

| Env var | What | Required |
|---------|------|----------|
| `HEADPLANE_EXTERNAL_URL` | Public URL of the existing Headplane UI (e.g. `https://headplane.example.com`) | yes, when `HEADPLANE_ENABLED=true` |
| `HEADPLANE_ENABLED` | set to `true` AND `HEADPLANE_EXTERNAL_URL` to use the existing one; set to `false` to skip entirely | no (default `true`) |

When `HEADPLANE_EXTERNAL_URL` is set, `deploy/deploy.sh`:

- Skips the `headplane` service block in the rendered
  `docker-compose.yml` (no second container).
- Skips the readiness check for `:50445`.
- Saves `HEADPLANE_EXTERNAL_URL` in the backup manifest so
  a `deploy.sh --from-path` restore on another host can
  reproduce the same wiring.

The `/admin/acls` view in Skygate links to
`HEADPLANE_EXTERNAL_URL` (when set) instead of the local
sidecar URL, so the operator clicks through to the existing
Headplane instance.

If you don't set `HEADPLANE_EXTERNAL_URL`, the default
behaviour holds: the local sidecar is deployed and
`/admin/acls` links to it. The two modes are interchangeable
at runtime — flipping the env var and re-running
`./deploy/deploy.sh` is the only step to migrate.

> **Note**: Skygate does not call the Headplane API
> directly — the only integration is the link from
> `/admin/acls`. The "existing Headplane" is therefore
> just a different URL for the same UI; the credentials
> and ACL data live on whichever Headscale the existing
> Headplane is pointed at, and Skygate talks to the same
> Headscale via `HEADSCALE_URL` + `HEADSCALE_API_KEY`.

## Version pin policy

`HEADPLANE_IMAGE` in `.env` is the only place the version lives.
The default is `ghcr.io/tale/headplane:0.6.3` (the latest
stable release as of 2026-04-09). A Skygate upgrade never
silently bumps the dependency — to upgrade Headplane, edit
the env var and re-run `./deploy/deploy.sh`.

The deploy script uses `${HEADPLANE_IMAGE}` in the compose
template, so the image tag flows through from `.env` → template
→ rendered `docker-compose.yml` → `docker compose up -d`. The
backup script uses the same env var to save the right image to
`headplane-image.tar`.

## Compatibility matrix

| Skygate | Headscale | Headplane | Notes |
|---------|-----------|-----------|-------|
| v0.10.10+ | 0.29.x | 0.6.3 | current recommended |
| v0.10.10+ | 0.28.x | 0.6.2 | older Headscale still works |
| v0.10.x | 0.27.x | 0.6.1+ | minimum supported Headplane per upstream |
| v0.6.0+ | 0.26.x | 0.6.0 | Headplane 0.6.0 requires Headscale 0.26.0+ |

The matrix is checked on every Skygate release. If you need a
newer Headplane (e.g. 0.7.x once stable), bump `HEADPLANE_IMAGE`
in your `.env` and re-run deploy — Skygate doesn't care which
version runs as long as it talks to the same Headscale API.

## Configuration

Headplane reads `deploy/templates/headplane-config.yaml` (copied
to `${DEPLOY_HEADSCALE_DIR}/headplane/config.yaml` on deploy).
Secrets (API key, cookie secret) are injected via env vars
prefixed with `HEADPLANE_`. The full set:

| Env var | What | Required |
|---------|------|----------|
| `HEADPLANE_ENABLED` | `true` / `false` — gate the whole sidecar | no (default `true`) |
| `HEADPLANE_IMAGE` | Docker image tag (pinned, e.g. `:0.6.3`) | no (default `:0.6.3`) |
| `HEADPLANE_HEADSCALE__URL` | Headscale API URL (matches `HEADSCALE_URL`) | yes |
| `HEADPLANE_HEADSCALE__INSECURE` | `true` / `false` — disable TLS for self-signed | no |
| `HEADPLANE_HEADSCALE__API_KEY` | Headscale API key (matches `HEADSCALE_API_KEY`) | yes |
| `HEADPLANE_SERVER__HOST` | bind host (use `0.0.0.0` in docker) | no |
| `HEADPLANE_SERVER__PORT` | UI port (default `50445`) | no |
| `HEADPLANE_SERVER__COOKIE_SECURE` | `false` for HTTP dev / self-signed | no |
| `HEADPLANE_SERVER__COOKIE_SECRET` | session cookie secret | yes |

Generate the cookie secret with `openssl rand -hex 16`.

## Backup / restore

`deploy/backup.sh` saves the Headplane config, data volume,
and Docker image alongside Skygate's other state. When
`HEADPLANE_ENABLED=false`, the backup skips Headplane entirely
(no `headplane-config.yaml`, no `headplane-data/`, no
`headplane-image.tar` in the archive). The manifest records
the env var so a restore on a different host can reproduce the
deployment.

`deploy/restore.sh` (run as part of `deploy.sh --from-path`)
re-creates the container with the saved image. The deploy
script's `headplane service` block is idempotent — restoring
into an existing deployment just re-pulls the pinned image.

## Upgrading Headplane

1. Check the [Headplane changelog](https://headplane.net/CHANGELOG)
   for breaking changes (e.g. ACL format changes, config renames).
2. Bump `HEADPLANE_IMAGE` in `.env` to the new tag.
3. Re-run `./deploy/deploy.sh`. The script does
   `docker compose pull headplane && docker compose up -d` which
   re-creates the container with the new image.
4. Verify the UI loads at `https://your-host:50445/admin/`.

Headplane and Headscale are independent projects, so
upgrading Headplane does NOT require a Headscale upgrade or a
Skygate upgrade.

## Why Skygate doesn't replace Headplane

Tempting to consider, but:

- Headplane has 100+ GitHub issues of feature work
  (browser SSH, OIDC, custom DNS, role-based access) that Skygate
  has no business duplicating.
- Headplane is actively maintained by tale with a public
  roadmap. Skygate is a self-service portal; it's not a UI
  framework.
- The user's task is "issue a preauth key for my new laptop
  and route my Telegram traffic through the exit node".
  That's Skygate's surface area. The operator's task is
  "review this ACL change before it goes to production,
  approve the route, and SSH into the relay to check the
  service". That's Headplane's surface area.

A custom ACL editor in Skygate is on the long-term roadmap
(see `AGENTS.md` "Not done yet" → "Headplane replacement") but
the explicit plan is to **keep `GenerateACL()` as a fallback
and have Headplane own the policy editor for non-trivial
configs**. v0.10.10 makes that boundary explicit by
documenting the integration contract here.

## See also

- `docs/deploy.md` — full deploy story
- `docs/telegram-relay.md` — Headplane UI for route approval
- [Headplane repo](https://github.com/tale/headplane) — upstream
- [Headplane changelog](https://headplane.net/CHANGELOG) — releases
