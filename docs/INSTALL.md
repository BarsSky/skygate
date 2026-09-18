# Installing Skygate

**Every supported install path, in one place.** Pick a row from the table,
follow that section, then do the shared post-install steps ([§10](#10-post-install-steps-every-method)).

Russian version: [`docs/ru/INSTALL.md`](ru/INSTALL.md).
Updating an existing install: [`docs/UPDATE.md`](UPDATE.md).
Operations (backup, restore, upgrades, HA): [`docs/operations.md`](operations.md).

---

## 1. Which method?

| # | Method | Use when | Service manager | DB default |
|---|---|---|---|---|
| A | [Docker Compose, self-build](#a-docker-compose-self-build) | you want the whole stack (skygate + headscale + headplane + DERP) built from this repo | docker | PostgreSQL (`local-pg` profile) or SQLite |
| B | [Docker Compose, prebuilt image](#b-docker-compose-prebuilt-image-ghcr) | fast install/update, no Go toolchain on the host | docker | whatever the image is given |
| C | [Docker Compose, SQLite single container](#c-docker-compose-sqlite-single-container) | small self-host, no external PG, one container | docker | SQLite |
| D | [Podman Compose](#d-podman-compose) | rootless containers | podman | as A/B |
| E | [Native systemd — Debian/Ubuntu](#e-native-systemd--debianubuntu) | skygate as a host service, no docker | systemd | SQLite |
| F | [Native systemd — RHEL family](#f-native-systemd--rhel-family) | as E on RHEL/Rocky/Alma/Fedora/Amazon | systemd | SQLite |
| G | [Native OpenRC — Alpine](#g-native-openrc--alpine) | Alpine / OpenRC hosts | OpenRC | SQLite |
| H | [Bare binary](#h-bare-binary-no-service-manager) | your own supervisor (nohup, supervisord, runit, s6) | none | SQLite |
| I | [Release tarball by hand](#i-release-tarball-by-hand-linux-macos) | air-gapped / custom layout | yours | SQLite or PG |
| J | [Windows](#j-windows) | Windows host + headscale reachable over the network | Windows service | SQLite |

**Docker image architecture:** the published image is **linux/amd64 only** (a
deliberate decision — see `AGENTS.md`). ARM hosts use the Go binary tarballs
(method I) or `docker buildx build --platform linux/arm64` from source.

---

## 2. Prerequisites (all methods)

* **A headscale control plane** reachable from the skygate host, plus an API
  key for it. Skygate drives headscale's API; without it the process exits
  with `config: HEADSCALE_API_KEY is required`.
* **A DB choice**: SQLite (single host, zero dependencies) or PostgreSQL
  (prod / HA). SQLite needs nothing extra.
* **A secret** for session JWTs. The native installers generate one into the
  env file; for docker put it in `.env`.
* Network: outbound HTTPS to GitHub if you want the update checker /
  self-update to work (air-gapped hosts use a mirror — see `UPDATE.md`).
* Ports: `8080` (web UI), `50444` (headscale API, if you run headscale here).

Env-var reference (name, default, meaning): [`docs/deploy.md` §1](deploy.md#1-environment).

---

## A. Docker Compose, self-build

Builds skygate inside the container on every start (`entrypoint.sh` runs
`go build` against the bind-mounted repo).

```bash
git clone https://github.com/BarsSky/skygate.git
cd skygate
cp .env.example .env          # then edit: HEADSCALE_URL, HEADSCALE_API_KEY, SKYGATE_JWT_SECRET, …
sudo bash deploy/deploy.sh    # renders configs, builds, brings the stack up
curl -fsS http://127.0.0.1:8080/healthz
```

* `deploy/deploy.sh` walks the whole stack (directories/network, headscale
  config, optional Caddy TLS, containers). `--from-path <dir>` points it at an
  existing checkout.
* Compose profiles: `local-pg` opts IN to a bundled PostgreSQL container;
  `caddy` opts IN to the bundled TLS terminator (skip it if an external proxy
  such as Nginx Proxy Manager terminates TLS).
* **Always run `docker compose` from the project directory.** With
  `docker compose -f /path/compose.yml` from elsewhere, `.env` is NOT loaded
  for interpolation and placeholders silently fall back to their defaults —
  this has bitten the reference deployment (see `docs/LESSONS.md`).

## B. Docker Compose, prebuilt image (ghcr)

No Go toolchain needed; the container runs the published image.

```bash
git clone https://github.com/BarsSky/skygate.git && cd skygate
cp .env.example .env          # HEADSCALE_URL, HEADSCALE_API_KEY, SKYGATE_JWT_SECRET, SKYGATE_DB, …
# pin a version (recommended) — the tag must be LOWERCASE:
echo 'SKYGATE_IMAGE=ghcr.io/barssky/skygate:v1.5.9' >> .env
docker compose -f docker-compose.ghcr.yml up -d
```

* `docker-compose.ghcr.yml` uses `${SKYGATE_IMAGE:-ghcr.io/barssky/skygate:latest}`.
* The image is linux/amd64. On ARM, build from source instead.
* This is the variant the "Pull image" button on `/admin/update` expects.

## C. Docker Compose, SQLite single container

One container, one SQLite file, no external database.

```bash
cp .env.example .env          # HEADSCALE_URL, HEADSCALE_API_KEY, SKYGATE_JWT_SECRET
docker compose -f docker-compose.sqlite.yml up -d
```

`docker-compose.sqlite.yml` pins
`SKYGATE_DB=${SKYGATE_DB:-sqlite:/var/lib/skygate/skygate.db}` and stores the
database in a named volume. `docker-compose.lite.yml` is the same idea with a
leaner service set.

> **Note for releases ≤ v1.5.8:** the `sqlite:<path>` DSN form could not be
> opened at all (fixed in v1.5.9). On an older binary use a bare path
> (`SKYGATE_DB=/var/lib/skygate/skygate.db`) or upgrade.

## D. Podman Compose

Supported, not exercised by CI. Same files as A/B:

```bash
podman compose -f docker-compose.ghcr.yml up -d
```

Watch for the usual rootless differences: bind-mount ownership (use
`:Z`/`:U` where needed), and `podman` instead of `docker` in every command
this documentation shows.

## E. Native systemd — Debian/Ubuntu

One command, or the standalone script:

```bash
# via the dispatcher (auto-detects the distro)
sudo bash deploy/install.sh --db-type=sqlite
# or standalone
sudo bash deploy/install-debian.sh --db-type=sqlite
```

Flags: `--db-type=sqlite|postgres`, `--import-existing=true|false`,
`--install-kind=systemd|bare|openrc`.

What it writes:

```
/usr/local/bin/skygate                             the binary (from the release tarball)
/etc/systemd/system/skygate.service                the unit (User=skygate, ProtectSystem=strict)
/etc/skygate/skygate.env                           the config you edit (PRESERVED on re-run)
/etc/systemd/system/skygate-update.{path,service}  the privileged self-update helper
/usr/local/lib/skygate/skygate-apply-update.sh     the applier it runs
/etc/skygate/update-helper.conf                    the applier's root-owned config
/var/lib/skygate/                                  data (SQLite DB, ts/, update/)
```

Env overrides: `SKYGATE_VERSION` (`latest` or a tag), `SKYGATE_PORT`,
`SKYGATE_USER`, `SKYGATE_DATA_DIR`, `SKYGATE_ETC_DIR`, `SKYGATE_BIN`,
`SKYGATE_SKIP_VERIFY=1` (skip the checksum download — needed for releases
that do not publish `SHA256SUMS`, i.e. ≤ v1.5.8), `SKYGATE_TS_*` (Tailscale
module, opt-in).

Then: fill `/etc/skygate/skygate.env` and

```bash
sudo systemctl restart skygate
systemctl is-active skygate && curl -fsS http://127.0.0.1:8080/healthz
```

## F. Native systemd — RHEL family

Same shape as E, different package manager and useradd tooling:

```bash
sudo bash deploy/install-rh.sh --install-kind=systemd
```

RHEL notes: `firewalld` does not open `8080` for you (most operators front
skygate with a proxy), and on older systems SELinux may need
`semanage port -a -t http_port_t -p tcp 8080`.

## G. Native OpenRC — Alpine

```bash
sudo bash deploy/install-alpine.sh --install-kind=openrc
```

Differences from E:

* the service is `/etc/init.d/skygate` + `/etc/conf.d/skygate`
  (which sources `/etc/skygate/skygate.env`), managed with
  `rc-service skygate start|stop|restart` and `rc-update add skygate default`;
* there is no systemd path unit, so the self-update helper is triggered
  through a narrowly-scoped `/etc/sudoers.d/skygate-update` drop-in and
  restarts the service with `rc-service` (`SKYGATE_UPDATE_MODE=openrc`);
* `SKYGATE_INSTALL_KIND=openrc`, `SKYGATE_UPDATE_STATE_PATH` and
  `SKYGATE_UPDATE_DIR` are exported from `/etc/conf.d/skygate`, so
  `/admin/update` knows the platform without guessing.

## H. Bare binary (no service manager)

```bash
sudo bash deploy/install-bare.sh --install-kind=bare
```

Installs the binary + env file + the update helper (sudoers drop-in) and
prints the three ways to run it: `nohup`, `systemd-run --unit=skygate`, or a
`supervisord` snippet. You own start/stop, restart-on-crash and log rotation.

`SKYGATE_UPDATE_MODE=bare` makes the self-update helper stop the recorded PID
and start the process again with the command baked into
`/etc/skygate/update-helper.conf` (`SKYGATE_UPDATE_BARE_START`).

## I. Release tarball by hand (Linux, macOS)

Assets per release (see the release page):

```
skygate-vX.Y.Z-linux-amd64.tar.gz      skygate-vX.Y.Z-darwin-amd64.tar.gz
skygate-vX.Y.Z-linux-arm64.tar.gz      skygate-vX.Y.Z-darwin-arm64.tar.gz
skygate-vX.Y.Z-windows-amd64.zip       SHA256SUMS   (attached from v1.5.9 on)
```

```bash
curl -fsSLO https://github.com/BarsSky/skygate/releases/download/v1.5.9/skygate-v1.5.9-linux-amd64.tar.gz
curl -fsSLO https://github.com/BarsSky/skygate/releases/download/v1.5.9/SHA256SUMS
sha256sum -c --ignore-missing SHA256SUMS
tar -xzf skygate-v1.5.9-linux-amd64.tar.gz     # flat: ./skygate + ./skygate.sha256
sudo install -m 0755 skygate /usr/local/bin/skygate
```

Run it under your supervisor, or point an existing systemd/OpenRC unit at it,
then do §10. **Verify the checksum before installing** — if a release has no
`SHA256SUMS` (≤ v1.5.8), fetch the digest from the GitHub Releases API
instead of skipping verification.

## J. Windows

```powershell
# elevated PowerShell
.\deploy\Setup-Skygate-Win.ps1 -Version v1.5.9
# parameters: -Version -InstallDir ("C:\Program Files\Skygate") -DataDir ("C:\ProgramData\Skygate")
#             -GitHubOwner -GitHubRepo
```

It downloads the `windows-amd64` zip, installs to `-InstallDir`, writes the
env file under `-DataDir`, and registers a Windows service. headscale must be
reachable over the network (`HEADSCALE_URL` cannot be a container name).

---

## 10. Post-install steps (every method)

1. **Point skygate at headscale.** `HEADSCALE_URL` (e.g.
   `http://headscale:50444` inside a compose network, `http://127.0.0.1:50444`
   on a host) and `HEADSCALE_API_KEY`. Both are required; the process refuses
   to start without the key.
2. **Choose the DB.** `SKYGATE_DB=sqlite:/var/lib/skygate/skygate.db` (or a
   bare path) for self-host; `SKYGATE_DB=postgres://user:pass@host:5432/db?sslmode=disable`
   for PG. `SKYGATE_DB_DSN` is the legacy name (honoured, lower priority).
3. **Set the admin credentials / JWT secret** (`SKYGATE_JWT_SECRET`,
   `SKYGATE_ADMIN_USER`, `SKYGATE_ADMIN_PASS`).
4. **Restart and verify:**
   ```bash
   curl -fsS http://127.0.0.1:8080/healthz          # process alive + build label
   curl -fsS http://127.0.0.1:8080/readyz           # db + headscale + headplane + tailscale
   ```
5. **Open the UI** (`/login`), then check `/admin/update`:
   * it shows the **install kind** and the **platform panel**
     (`systemctl` / `rc-service` / `docker` / helper presence);
   * on native installs it should say the update helper is installed. If it
     says "not installed", re-run the installer (idempotent, preserves
     `skygate.env`).
6. **Optional:** Tailscale module (`deploy/scripts/install-tailscale.sh`),
   OIDC login (`docs/oidc.md`), public HTTPS (`docs/https.md`), own DERP relay
   (`docs/derp.md` + `deploy/derp-init.sh`), HA/cluster (`docs/ha.md`).

## 11. Updating

See [`docs/UPDATE.md`](UPDATE.md) for every path (compose rebuild, image pull,
scheduled auto-update, `/admin/update`, native self-update with rollback,
mirrors for air-gapped hosts, and the per-kind manual steps).

## 12. Uninstalling

```bash
sudo bash deploy/scripts/cleanup-skygate.sh              # asks for confirmation
sudo bash deploy/scripts/cleanup-skygate.sh --yes --dry-run
sudo bash deploy/scripts/cleanup-skygate.sh --keep-data --keep-config
```

It stops/disables the service, removes the binary, the service user and the
runtime dirs, and preserves `/var/lib/skygate` unless you ask otherwise.
Docker installs: `docker compose down` (add `-v` only if you really want the
volumes gone).

---

## 13. Troubleshooting the install itself

| Symptom | Cause / fix |
|---|---|
| `config: HEADSCALE_API_KEY is required` (restart loop) | the env file still has the placeholder — fill `HEADSCALE_URL` + `HEADSCALE_API_KEY`, then `systemctl restart skygate` |
| `docker compose` ignores `.env` values | you ran it from another directory (or with a path); `cd` into the project first, or pass `--env-file` |
| Installer stops right after "installed: /usr/local/bin/skygate" | pre-v1.5.9: missing `xxd` on a minimal host aborted `write_env_file`; upgrade the installer or install `xxd`/`openssl` |
| `read auth key: … no such file` from `/admin/tailscale` | container Tailscale mode with no key file; paste the key on the page or set `SKYGATE_TS_AUTHKEY_FILE=/dev/null` to disable it deliberately |
| Checksum verification fails / `SHA256SUMS` 404 | releases ≤ v1.5.8 do not publish `SHA256SUMS`; use the GitHub asset digest or `SKYGATE_SKIP_VERIFY=1` knowingly |
| `no matching manifest for linux/amd64` | you pulled an ARM tag; the image is amd64-only |
| `docker pull` refuses the tag | GHCR tags are case-sensitive and must be lowercase (`ghcr.io/barssky/…`) |
| Page loads but every device shows offline | a host firewall/DOCKER-USER rule is blocking the proxy → headscale path (see `docs/LESSONS.md`) |
