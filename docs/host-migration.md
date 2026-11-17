# Migrating an installation to a new host: analysis and procedure

This document is both the analysis of a real migration (2026-11-15/16, from an agent VM to a
VPS) and the procedure that repeats it **from the panel**: `/admin/migrate` asks the
questions and applies the steps itself. The document comes first: the wizard implements
exactly what is described here, with the same commands.

Looking for "how to update the panel"? That is [`UPDATE.md`](UPDATE.md). For a fresh
install, see [`INSTALL.md`](INSTALL.md). This page is about moving an **already working**
installation to another machine.

---

## 1. What an installation consists of

What travels is not "the panel" but six groups of artefacts, and each group travels
differently.

| # | Group | What exactly | How it moves |
|---|---|---|---|
| 1 | **skygate panel** | binary `/usr/local/bin/skygate`, unit, `/etc/skygate/skygate.env`, data dir `/var/lib/skygate` (DB, `oidc-keys`, `certs`, `ssh`, `modules`) | copy 1:1 + rebuild the env for the new host |
| 2 | **Database** | SQLite file `skygate.db` (the default) **or** a PostgreSQL DSN | file copy with the panel stopped; changing the engine is only `skygate db-migrate --from <dsn> --to <dsn>` (never on the panel host) |
| 3 | **headscale** | binary, unit, `/etc/headscale/config.yaml`, `policy.hujson`, **noise private key**, `db.sqlite` | copy 1:1; the key and the policy must be byte-identical |
| 4 | **DERP** | `derper` binary, unit, certificate directory, ports 8443/tcp and 3478/udp | binary + certificate (see §3, P4) |
| 5 | **Proxy and names** | Caddyfile (panel/headscale/DERP sites), ACME certificates, DNS A records | config copied, certificates re-issued, DNS via the provider or by hand |
| 6 | **Keys and secrets** | `SKYGATE_SECRET_KEY`, OIDC keys, the sync SSH key (`skygate_sync`), the host's tailscale state | some are copied verbatim, some must be **re-issued** |

The one rule: `headscale`'s noise key, `policy.hujson`, `SKYGATE_JWT_SECRET`,
`SKYGATE_SECRET_KEY`, the OIDC key directory, the sync SSH key and the panel database are copied
**verbatim**. If `SKYGATE_SECRET_KEY` differs, the panel starts but cannot decrypt the stored
secrets (tokens, DNS provider passwords) — which looks like "the settings are gone". If
`SKYGATE_JWT_SECRET` differs, every session is invalidated. If headscale's noise key differs, every
client sees a **new control plane** and demands re-registration (and OIDC re-registration drops
tags, see §2.3). If the OIDC key set differs, OIDC login breaks (the JWKS no longer matches issued
tokens).

---

## 2. What the migration measured

The chronology matters not for its own sake: every item cost real time and must therefore be
a step of the procedure instead of a surprise.

1. **The old host was a docker-compose install** (`skygate` + `headscale` + `headplane` +
   `derper` + a local PostgreSQL, all containers); the new one is **native (systemd)**:
   `skygate`, `headscale`, `derper`, `caddy`, with `headplane` left in docker. The install kind
   changed mid-flight, and half the existing scripts assumed docker.
2. **The engine did not change (SQLite → SQLite), but the move was not a directory copy either.**
   A ~206 MB database (48 tables) travelled, so the order was: a snapshot taken with the panel
   stopped (`skygate.db.pre-migr-*`), a prepared copy (`skygate.db.staged-*`), then
   `skygate migrate up` on the new host to `applied=63 pending=0` and a row-count comparison for the
   key tables. PostgreSQL existed on the old host **only as a container for the gate** and is not
   installed on the new host at all (§2.7); changing the engine, if it is ever needed, is
   `skygate db-migrate --from … --to …`, and it must not run on the panel host.
3. **headscale OIDC re-authentication drops tags.** On re-registration headscale removes **all**
   tags, and the tag API replaces the whole set, so two writers (the `tag:private` backfill and
   the dev-tag reconciler) clobbered each other — for 2.5 minutes the node matched no ACL rule.
4. **The DERP certificate.** In native mode `derper` resolves the certificate by SNI and
   **verifies the chain**; trimming it to "leaf + intermediate" breaks verification
   (`unable to get local issuer certificate`), so the **full chain** is copied and Caddy's ACME
   plus a daily `derper-cert-sync.timer` renews it.
5. **HTTP/2 cannot carry a DERP upgrade.** A liveness probe that negotiated h2 receives `426`
   and paints a healthy relay as "stopped"; clients behind an h2 proxy "lose" the relay. DERP is
   therefore published under its own name on its own port (HTTP/1.1), not only through the
   shared proxy.
6. **`policy.mode: database` versus `file`.** The old host kept the policy in a file and
   `PUT /api/v1/policy` answered `500 update is disabled for modes other than database`; the new
   one runs `database`. That changes both how the ACL is applied and who owns the policy file.
7. **PostgreSQL is not installed on a small host.** The operator's instruction: the panel VPS
   cannot carry a database server — it lives elsewhere or is not needed at all (SQLite). Half of
   the gate's PostgreSQL checks therefore run in CI rather than on the host (B481).
8. **The old host can become unreachable mid-flight.** It did (SSH timed out for good): anything
   needed from the old host must be copied **before** the DNS switch, and the procedure needs an
   explicit "from here on, without the old host" point.
9. **Routing is rebuilt after the move.** Prefix ownership (`prefix_owner`), the ACL (`294`
   grants carrying `via=[…]`), the relays' route advertisements and the clients' `accept-routes`
   are a separate verification step, not something that fixes itself.
10. **Order beats speed: the ACL and the tags go first, DNS last.** Switching the names before the
    first successful `skygate acl-apply` (and a `tag-reconcile` pass) leaves untagged devices
    outside the rules exactly during the switch — that window cost more time than anything else in
    the migration (B477 — tags dropped on re-authentication, B476 — an ACL that silently was not
    applied).

---

## 3. Phases

Legend: **A** — the wizard does it; **C** — it does it after asking for confirmation; **M** — it
only shows the exact command/instruction (a human or a browser is required).

### P0. Prerequisites and access

| Step | What | Verification | Idempotent | Kind |
|---|---|---|---|---|
| P0.1 | SSH access to the new host: a key (path on this machine or a pasted private key) **or** an OS login+password | `ssh -o BatchMode=yes … true` answers | yes | A |
| P0.2 | New host: Linux, systemd, x86_64/arm64, ≥ 2 GB RAM, ≥ 10 GB disk, root or passwordless sudo | `uname -m`, `systemctl --version`, `df -h /`, `sudo -n true` | yes | A |
| P0.3 | The new host's ports are free: 80/443 (ACME), 8080 (panel) or a custom one, 8443/tcp, 3478/udp | `ss -ltnup` does not list them | yes | A |
| P0.4 | No previous skygate/headscale on the new host | no `/etc/skygate`, `/etc/headscale`, no units | yes | A |
| P0.5 | **Inventory of the old host**: what is actually needed (see §1) with sizes | the table in the plan | yes | A |

If P0.2/P0.3/P0.4 fail, the wizard **stops at the plan** and touches nothing.

### P1. The new host's base

| Step | What | Verification |
|---|---|---|
| P1.1 | Package update, `curl`, `ca-certificates`, `sqlite3`, `rsync`, `ufw` | `command -v` for each |
| P1.2 | Timezone and NTP (otherwise TTL/ACME/tokens drift) | `timedatectl` |
| P1.3 | `skygate` user + directories `/var/lib/skygate` (0700), `/etc/skygate` (0750) | `id skygate`, permissions |
| P1.4 | Firewall: 22, 80, 443, 8080 (panel), 8443/tcp, 3478/udp | `ufw status` |
| P1.5 | Tailscale on the new host (when the panel belongs in the tailnet): install + login | `tailscale status` |

### P2. headscale

| Step | What | Verification | Note |
|---|---|---|---|
| P2.1 | Install **the same** headscale version | `headscale version` | the version is part of the plan; a different one means a different DB schema |
| P2.2 | Copy `/etc/headscale/config.yaml` and `policy.hujson` | diff against the source | `policy.mode` is the operator's decision (usually `database`) |
| P2.3 | Copy `/var/lib/headscale/db.sqlite` **and** the noise key | `headscale nodes list` shows the same nodes | stop headscale before copying |
| P2.4 | `systemctl enable --now headscale`, port 50444 on loopback | `/health` answers | with a changed key the nodes need re-registration (**M**: browser confirmation) |

### P3. The skygate panel

| Step | What | Verification |
|---|---|---|
| P3.1 | Install **the same build** (`/usr/local/bin/skygate`, compare `version`) | `/healthz` reports the expected `build` |
| P3.2 | Copy `/etc/skygate/skygate.env`, **keeping** `SKYGATE_SECRET_KEY`, and fix host-specific values (`SKYGATE_PORT`, addresses, `SKYGATE_HEADSCALE_URL`) | `/readyz` = `healthy` |
| P3.3 | Copy the data dir (DB, `oidc-keys`, `certs`, `ssh`) | `skygate migrate status` → `pending=0` |
| P3.4 | When the engine changes: prepare the snapshot, run `migrate up`, compare row counts of the key tables | a before/after row report |
| P3.5 | `systemctl enable --now skygate`; no `/admin/update` needed (the binary is already the right version) | `/healthz`, `/readyz` |

### P4. DERP

| Step | What | Verification |
|---|---|---|
| P4.1 | Install `derper`, `/etc/derper/…`, the unit | `systemctl is-active derper` |
| P4.2 | Certificate: the full ACME chain into derper's directory + the daily sync | `openssl s_client -connect <derp>:8443 -servername <derp>` → `verify return:1` |
| P4.3 | Ports 8443/tcp and 3478/udp open | `ss -ltnup`, `ufw status` |
| P4.4 | A `derp_relays` row (region, host, url) and a check from the panel | `/admin/derp` — status, probe latency |
| P4.5 | `derp.probe_host` / `derp.debug_url` / `derp_relays.url` point at the **new** host | the panel's probe reaches the new address |

### P5. Names and DNS

| Step | What | Verification |
|---|---|---|
| P5.1 | Proxy sites for the panel/headscale names (and DERP separately when it has its own port) | `curl -I https://<PANEL_HOSTNAME>` |
| P5.2 | ACME issued the certificates (only after DNS already points at the new host) | `openssl s_client` → `notAfter`, issuer |
| P5.3 | Switch the A records to the new IP via the provider (the panel's API) **or** keep a stub/manual mode | `dig +short <name>` from an external resolver |
| P5.4 | Account for TTL and caches; a name can "bounce" between hosts until the TTL expires | `dig` from several resolvers |

### P6. Verifying the installation

| Step | What | How to check |
|---|---|---|
| P6.1 | The panel is alive and is the expected version | `/healthz` → `build` |
| P6.2 | All dependencies | `/readyz` → `db/headscale/headplane/tailscale` |
| P6.3 | The schema is caught up | `skygate migrate status` → `pending=0` |
| P6.4 | headscale sees the nodes | `headscale nodes list`, the `online` count |
| P6.5 | Node tags survived | nodes carry `tag:dev-…`/`tag:private` (otherwise the ACL matches nothing) |
| P6.6 | Relay routes | relays advertise their prefixes, routes are approved, clients run `accept-routes` |
| P6.7 | Prefix ownership | `/admin/exit-nodes`: live owners, no dead pins |
| P6.8 | DERP | `/admin/derp`: `stopped` is not shown, probe latency is sane |
| P6.9 | One real client | a device comes online, rules apply, the operator's sites open |

### P7. The old host

| Step | What | Note |
|---|---|---|
| P7.1 | Stop the old host's services (without deleting) | the rollback window, until DNS settles |
| P7.2 | A day later, decommission (remove volumes, containers, the tunnel) | **M** — after confirming everything works |
| P7.3 | Remove the old host from the tailnet | `headscale nodes … delete`, otherwise dead nodes linger |

---

## 4. Decision points: what the wizard asks and how an answer changes the plan

| Question | Options | What changes |
|---|---|---|
| Where are we moving to | IP or name | the SSH transport address |
| OS access | key path / pasted private key / login+password | the transport; the password is **never stored in clear**, it is encrypted and rendered masked |
| SSH port and user | 22 / another; root / a sudo user | a `sudo -n true` check when the user is not root |
| Mode | **one-to-one** (files + DB + keys + certificates) / **with another DB** (snapshot + migrations, a different engine on the new host) / **database only** (the panel stays, the database moves) | the step set P2–P4; "database only" skips P4 and P5 |
| The new host's engine | same as now (SQLite) / PostgreSQL (DSN) | P3.4; PostgreSQL adds a warning about the database server's weight and a connection check |
| The new host's name | the tailnet name, current value pre-filled | `node_owner_map`, tags and ACL rules refer to the hostname — the plan contains a rename step and a tag re-apply |
| DNS | the same provider (switch via the API) / a temporary stub (leave the names alone, reach the host by IP) / manual | P5.3; with a stub ACME cannot issue until the name is switched, and the plan says so |
| The old host | keep / stop the services / decommission | P7 |

---

## 5. What the wizard cannot do, and why

1. **Confirm a node registration.** headscale 0.29.3 requires a click in the browser for an
   OIDC registration (`POST /register/confirm/<id>`) — that is a human and their OIDC session.
2. **Wait out a TTL and ACME.** DNS caches and certificate issuance live by time, not by
   command; the wizard shows what to wait for instead of pretending the switch happened.
3. **Secrets it does not have.** The old host's password, the DNS registrar's key (when the panel
   has not stored it), the PostgreSQL password — the wizard asks, or it stops.
4. **The irreversible.** Deleting the old host, dropping a database, revoking keys — only on an
   explicit confirmation and as a separate step.

---

## 6. The rollback rule

- Until DNS is switched, rollback means "change nothing": the old host is alive, the new one can
  be wiped.
- After the switch, rollback means pointing the A records back at the old IP (provider or by
  hand) and restarting the old services; data created on the new host in the meantime has to be
  moved back, which is why the rollback window is bounded and stated in the plan.
- If the migration **changed the engine**, rollback is impossible without the dump taken in step
  P3.4: the run report keeps the snapshot's path.
