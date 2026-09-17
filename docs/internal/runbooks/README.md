# Internal runbooks

Operator-facing runbooks for the skygate stack. These are the
step-by-step recipes for working on a live deployment.

## Conventions

- All example values use RFC 5737 IPs (`192.0.2.1`, `198.51.100.1`,
  `203.0.113.1`), the `example.com` placeholder domain, and
  usernames like `<USER>`, `<VM_HOST>`, `<HEADSCALE_HOST>`.
- Secrets (OIDC client_secret, headscale API keys, DERP TLS
  certs) live in `/etc/skygate-secrets/` on the operator's hosts
  (chmod 0600). Runbooks reference them by path, never paste
  values into the repo.

## Files

| File | Topic |
|---|---|
| `auto-deploy-baseline.md` | Baseline state captured before auto-deploy changes (B-mod-first-run-adoption) |
| `auto-deploy-test-plan.md` | Test plan for the auto-deploy pipeline |
| `derp-cert-sync.md` | How DERP server TLS certs are synced + rotated (B252 + certsync) |
| `exit-rules-reconciler.md` | How /admin/exit-rules reconciles state across nodes |
| `ha-active-router.md` | Active/passive HA router setup for the skygate VM |
| `ha-v1.5.0-execution.md` | v1.5.0 HA chain execution plan (status log + open questions) |
| `https-setup.md` | HTTPS termination options (Caddy, Nginx Proxy Manager, etc.) |
| `oidc-headscale.md` | Wiring headscale as the OIDC RP against the skygate OIDC IdP (B161.4) |
| `subnet-router.md` | Per-device subnet-router setup + ACL grant pattern |
| `tailnet-advertised-routes.md` | How routes are advertised + the `auto_update` toggle |
| `telegram-relay.md` | Telegram bot relay integration |

## See also

- `docs/runbooks/` — public-facing runbooks (deploy + recovery)
- `docs/internal/audits/` — read-only audit reports (what was, what is)
- `docs/internal/architecture/` — internal architecture docs
