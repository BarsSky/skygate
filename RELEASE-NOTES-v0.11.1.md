# Skygate v0.11.1 — runtime renderer (Apply + Test URL)

**Date:** 2026-07-15
**Branch:** `feature/v0.10.12-bot-ux`
**Previous:** v0.11.0 — `/admin/integrations` web UI for DERP/Headplane

## What this release adds

v0.11.0 added the form to **save** the DERP / Headplane config to SQLite,
but applying the change still required running `./deploy/deploy.sh` on the
host. v0.11.1 closes that loop: the same form now has an **Apply** button
that re-renders the headscale config, pushes it into the running headscale
container, and SIGHUPs the process — no shell-out, no `docker compose
restart`, no downtime. The DERP form also gets a **Test all URLs** button
that probes each external URL (5s timeout) and shows the per-URL status +
latency inline.

## What changed for the operator

### 1. `/admin/derp/config` — three buttons instead of one

The form now has three actions in the same row:

| Button | Effect |
| --- | --- |
| **Save** | Persist the form fields to SQLite. Does **not** touch headscale / derper containers. Same as v0.11.0. |
| **Test all URLs** | Probe each external DERP URL with a 5s GET, then re-render the page with a per-URL results table (URL / status / latency). Save runs first so the test results persist with the form data. |
| **Apply** | Save + push to headscale + SIGHUP. If the bundled derper toggle changed, start or stop the `derper` container via `docker start` / `docker stop`. Re-renders the page with the full apply trace inline (each step = "ok" or "fail: …"). |

The apply trace is a small `<ul>` of one-line steps so the operator can see
exactly what the renderer did, and where it failed if anything went wrong:

```
ok: render headscale-config.yaml
ok: pushed config.yaml to headscale container
ok: SIGHUP headscale (config reloaded)
ok: derper already stopped
```

### 2. `/admin/headplane` — Save + Apply

Same pattern: **Save** persists the mode + URL; **Apply** also starts or
stops the `headplane` container to match the mode. `mode=off` /
`mode=external` stops the local sidecar AND removes the container
(frees the 50445 host port); `mode=bundled` starts the sidecar.

### 3. `/admin/integrations` — explainer card

A new "what v0.11.1 changed" card sits at the top of the landing page so
operators visiting from the v0.11.0 release notes see the new capability
without reading the docs.

## Architecture

### New module: `internal/handlers/admin_integrations_renderer.go`

Go-side port of the Python one-liner in `deploy/deploy.sh`'s
`render_template()` function. Pure Go, no shell-out, no Python dependency
at runtime. Three concerns:

1. **Template rendering** — `${VAR}` is expanded from `os.Getenv` (missing
   vars preserved as `${VAR}` so the operator notices a broken env in the
   file, not a silent empty value). `__HEADSCALE_DERP_URLS__` and
   `__HEADSCALE_AUTO_APPROVE_ROUTES__` markers are replaced with the
   canonical indented YAML list.
2. **Headplane service stripping** — when `HeadplaneMode != "bundled"`, the
   `headplane:` service block and the `headplane_data:` volume are
   stripped from the rendered compose. Matches `deploy.sh`'s `if
   HEADPLANE_ENABLED=false || [ -n HEADPLANE_EXTERNAL_URL ]` branch.
3. **Docker orchestration** — `docker exec -i headscale sh -c "cat > ..."`
   pushes the rendered config into the headscale container (the
   container's `/etc/headscale` is bind-mounted from the host's
   `${DEPLOY_HEADSCALE_DIR}/config/`, so this is the on-disk file too).
   `docker kill -s HUP headscale` reloads the config without a restart.
   `docker start` / `docker stop` / `docker rm` toggle the derper and
   headplane containers.

### Why SIGHUP works for headscale config reload

Headscale supports `SIGHUP` for reloading the config without a full
restart. The 5-10s graceful drain keeps the API responsive; clients see
the new DERP map list within one UDP probe. The downside: SIGHUP only
picks up `derp.urls` and `log-level` changes — anything else still needs
a `docker compose restart headscale`, which the v0.11.1 renderer doesn't
do (the operator runs `deploy.sh` for those cases; the new code only
covers the DERP / Headplane use cases explicitly).

### First-time install still needs `deploy.sh`

`Apply` handles the *toggle* (start / stop / restart / push config), not
the *first-time create*. The `derper` and `headplane` containers need an
entry in the bind-mounted `derper-compose.yml` / `docker-compose.yml` to
be created the first time, and those files live on the host filesystem
(outside the skygate container's view). The renderer's docs say so
explicitly: "First-time install of derper / headplane containers still
requires `./deploy/deploy.sh`."

### Why the integration config has the env-var fallback

`db.LoadIntegrations` still falls back to the deploy-time env vars
(`DERP_EXTERNAL_URLS`, `HEADPLANE_EXTERNAL_URL`, `HEADPLANE_ENABLED`) when
the DB row is missing — that bridge from the v0.10.12 deploy-time model
to the v0.11.0/v0.11.1 runtime model is preserved. An operator who has
only ever used `deploy.sh` sees the same effective config in
`/admin/integrations` without having to visit the UI first.

## Files

**New:**

- `internal/handlers/admin_integrations_renderer.go` (Go-side renderer:
  template expansion, YAML list rendering, headplane service stripping,
  docker orchestration, URL probe)
- `internal/handlers/admin_integrations_renderer_test.go` (18 tests:
  template substitution, multi-URL YAML list, headplane block strip,
  `applyHeadscale` push + SIGHUP, `applyBundledDERP` start/stop/no-op,
  `applyHeadplane` start/stop/remove, `applyAll` short-circuit on
  failure, `probeDerpURL` OK/500/empty body, `probeAllDerps` per-URL
  order, `PostAdminDerpConfig` action=apply/test end-to-end,
  `PostAdminHeadplane` action=apply end-to-end)

**Modified:**

- `internal/handlers/admin_integrations.go` — `PostAdminDerpConfig`
  switches on `action` (save / test / apply); `PostAdminHeadplane` adds
  the apply path. Old `probeDerpmapURL` removed (the renderer file owns
  the probe now, and the new `probeDerpURL` returns latency too).
- `internal/handlers/templates/admin/derp_config.html` — three buttons
  (Save / Test all / Apply), inline test results table, inline apply
  trace, help banner pointing at the new buttons.
- `internal/handlers/templates/admin/headplane.html` — Save + Apply
  buttons, inline apply trace, help banner.
- `internal/handlers/templates/admin/integrations.html` — v0.11.1
  explainer card at the top.
- `internal/i18n/catalog.go` — 20 new keys (`derp.config_apply`,
  `derp.config_applied`, `derp.config_apply_failed`,
  `derp.config_apply_result`, `derp.config_apply_help`,
  `derp.config_test_all`, `derp.config_test_results`,
  `derp.config_test_url`, `derp.config_test_status`,
  `derp.config_test_latency`, `headplane.config_apply`,
  `headplane.config_applied`, `headplane.config_apply_failed`,
  `headplane.config_apply_result`, `headplane.config_apply_help`,
  `integrations.apply_help`) × 2 languages. Existing
  `derp.config_apply_required` / `headplane.config_apply_required`
  updated to point at the Apply button instead of the
  "v0.11.1 will add…" message.

**No deploy script change.** The Apply button covers the 95% case; the
first-time install (and the v0.10.12 deploy model) keeps working through
`deploy.sh`. The renderer is a runtime-only path; deploy.sh and the
Python one-liner are still the source of truth for cold installs.

## Tests

12/12 packages green, 18 new tests:

- `TestRenderHeadscaleConfig_BasicSubstitution` — `${VAR}` expanded
- `TestRenderHeadscaleConfig_PreservesMissingEnv` — `${VAR}` kept on miss
- `TestRenderHeadscaleConfig_MultipleDERPURLs` — YAML list, one per line, in order
- `TestRenderHeadscaleCompose_KeepsHeadplaneWhenBundled` — bundled → keep block
- `TestRenderHeadscaleCompose_StripsHeadplaneWhenOff` — off → strip service + volume
- `TestRenderHeadscaleCompose_StripsHeadplaneWhenExternal` — external → strip
- `TestStripHeadplaneServiceBlock_NoOpWhenAbsent` — pure helper no-op
- `TestApplyHeadscale_PushesAndSIGHUPs` — exactly 2 docker calls in order, stdin contains rendered body
- `TestApplyBundledDERP_StartsWhenEnabled` — inspect + start
- `TestApplyBundledDERP_StopsWhenDisabled` — inspect + stop
- `TestApplyBundledDERP_NoOpWhenStateMatches` — inspect only
- `TestApplyHeadplane_RemovesWhenExternal` — inspect + stop + rm
- `TestApplyAll_PropagatesFailure` — headscale failure short-circuits the rest
- `TestApplyAll_SuccessPath` — all three sub-applies in order
- `TestProbeDerpURL_OK` — 200 + non-empty body → OK
- `TestProbeDerpURL_500` — 500 → fail with `HTTP 500` in error
- `TestProbeDerpURL_EmptyBody` — 200 + empty body → fail
- `TestProbeAllDerps` — per-URL results in order
- `TestPostAdminDerpConfig_ActionApply` — handler end-to-end with fake docker
- `TestPostAdminDerpConfig_ActionTest` — handler end-to-end with httptest server
- `TestPostAdminHeadplane_ActionApply` — handler end-to-end with fake docker

## VM verification

`make test` on the VM (`skyadmin@192.168.13.69`): 12/12 packages green,
smoke 118/118 PASS, `bash scripts/smoke.sh 2>&1 | grep -E "SUMMARY|FAIL"`
prints two `SUMMARY: 59 pass, 0 fail` lines (ru + en).

## GitHub

https://github.com/BarsSky/skygate/releases/tag/v0.11.1

## Deferred (not in this release)

- **v0.12.0** — pluggable headscale per portal user (per
  `docs/skygate-as-shell.md`). `portal_users` gets `headscale_url` +
  `headscale_api_key` columns.
- **v0.13.0** — ACL import/export with dry-run preview.
- **Butler voice v3** (urgency marks) — deferred until user feedback
  on v2 lands.
- **Personal API token rotation** (admin override) — TTL +
  auto-rotate field for 24h / 7d / 30d tokens.
