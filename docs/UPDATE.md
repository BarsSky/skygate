# Updating Skygate

**Every supported update path in one place**, from a one-click in-app update to a
fully manual binary swap on an air-gapped host.

Russian version: [`docs/ru/UPDATE.md`](ru/UPDATE.md).
Installing from scratch: [`docs/INSTALL.md`](INSTALL.md).
Operations (backup, restore, disaster recovery, HA): [`docs/operations.md`](operations.md).

---

## 1. Which update path applies to you?

> **Routine workflow (2026-09-21, AGENTS.md §13):** the maintainer pushes a tag and
> publishes the GitHub release — that is the end of the maintainer's job. The operator
> triggers the update from `/admin/update` in the running instance. **SSH + `git pull`
> + `restart` is the emergency / break-glass path, not the routine.** Every install
> kind listed below supports `/admin/update`; the operator never has to log into the
> host for a routine release.

| Install kind (see `INSTALL.md`) | In-app `/admin/update` | Automated / scheduled | Manual fallback |
|---|---|---|---|
| Docker compose, built from source (A) | **Update**, **Push update** (git pull + rebuild + recreate) | ✅ scheduler (see [§4.4](#44-scheduled-auto-update-docker-only)) | [§6.1](#61-docker-compose-source-checkout) |
| Docker compose, prebuilt image (B/C/D) | **Pull image**, **Update** | ✅ scheduler | [§6.2](#62-docker-compose-prebuilt-image) |
| Native systemd (E/F) | **Update** → privileged helper ([§5](#5-native-installs-systemd--openrc--bare)) | ❌ never automatic (scheduler skips native kinds) | [§6.3](#63-native-systemd) |
| Native OpenRC / Alpine (G) | **Update** → privileged helper | ❌ | [§6.4](#64-native-openrc-alpine) |
| Bare binary (H/I) | **Update** → privileged helper | ❌ | [§6.5](#65-bare-binary) |
| Windows service (J) | ❌ (no updater; the page shows `InstallUnknown`-style guidance) | ❌ | [§6.6](#66-windows) |

The update page always shows **which path it believes you are on** — install kind,
`systemctl`/`rc-service`/`docker` presence, and whether the privileged helper is
installed. Check that panel first: if it says *"helper not installed"* on a native
install, re-run the installer (idempotent, `skygate.env` is preserved).

---

## 2. Before you update

1. **Back up the database.** SQLite: copy the file (or use the in-app backup).
   PostgreSQL: `pg_dump`. See [`backup-restore-and-migration.md`](backup-restore-and-migration.md).
2. **Note the current build label** — `/healthz` and the update page both show it
   (e.g. `v1.5.8-39-g2fb8235`). You need it for a rollback.
3. **Read the release notes** for the target tag (migrations, breaking changes).
4. **Check version compatibility.** A native SQLite host must be on **v1.5.9 or
   newer**: releases up to and including v1.5.8 cannot run on SQLite at all
   (`migration v44 … duplicate column name: user_name`). The applier refuses such
   a swap *before* it touches the binary, so attempting it is safe but useless.
5. **Choose a maintenance window** for native updates: the service is restarted by
   the helper, so the portal is briefly unavailable (seconds to ~1 minute,
   plus the migration time).
6. **Prefer a pinned tag over `latest`.** `latest` silently follows the newest
   release; pinning makes a rollback a one-line change.

---

## 3. Version and build labels

| Label | Example | Meaning / what you can do with it |
|---|---|---|
| Release tag | `v1.5.9` | Published GitHub release with assets. **The only thing the native updater can install.** |
| Build label with commit | `v1.5.9+4a6bbbe3` | A release tag plus the commit it was built from. `+…` is stripped automatically. |
| Dev build (git describe) | `v1.5.8-39-g2fb8235` | 39 commits after v1.5.8. Perfectly valid for the Docker path (it is a git ref) but GitHub publishes **no release assets** for it, so the native updater refuses it with an actionable message and shows the manual steps. |

`SKYGATE_DEV_BUILD=true` marks a build so the UI stops nagging about updates —
useful on a VM that tracks `main`.

---

## 4. Docker installs

### 4.1 Source checkout (compose from the repo)

The container's `entrypoint.sh` runs `go build` on every start, so a
`git pull` + restart is a complete update for source changes.

```bash
cd /path/to/skygate                 # ALWAYS the project dir (see the trap below)
git pull --ff-only
sudo docker compose up -d --force-recreate --no-deps skygate
# optional but recommended: migrate before the container starts
sudo docker compose exec skygate /app/skygate migrate-only
until curl -fsS http://127.0.0.1:8080/healthz | grep -q "$(git describe --tags)"; do sleep 3; done
```

`docker compose build` is only needed when the **Dockerfile** changes.

> **Trap — `docker compose` interpolation is CWD-relative.** Running
> `docker compose -f /path/docker-compose.yml …` from another directory does **not**
> load the project's `.env`, so every `${VAR:-default}` silently renders its
> default. This has already broken the reference deployment once. Always `cd` into
> the project directory (or pass `--env-file /path/.env`).
>
> **Trap — `--force-recreate` is required** after any change to `extra_hosts`,
> volumes or the image tag; a plain `restart` keeps the old container network
> config.

### 4.2 Docker compose, prebuilt image

```bash
cd /path/to/skygate
sed -i 's|^SKYGATE_IMAGE=.*|SKYGATE_IMAGE=ghcr.io/barssky/skygate:v1.5.9|' .env   # or edit by hand
sudo docker compose -f docker-compose.ghcr.yml pull skygate
sudo docker compose -f docker-compose.ghcr.yml up -d --force-recreate --no-deps skygate
curl -fsS http://127.0.0.1:8080/healthz        # confirm the new build string
```

* Tags are **case-sensitive and must be lowercase** (`ghcr.io/barssky/…`).
* The published image is **linux/amd64 only**. On ARM use the Go tarball
  (`INSTALL.md` method I) or build locally with `--platform linux/arm64`.
* The equivalent in-app action is **Pull image** ([§4.3](#43-in-app-buttons)).

### 4.3 In-app buttons

All of these live on **`/admin/update`** (admin session required). The page
auto-refreshes while a job is running and folds in the privileged helper's log for
native installs.

| Button / control | Route | What it does | Available on |
|---|---|---|---|
| **Check now** | `POST /admin/update/check-now` | Queries GitHub Releases (or the configured channel) and caches the newest version | all kinds |
| **Update** | `POST /admin/update/apply` | Docker: pull the repo/tag, rebuild the image, recreate the container, poll `/healthz`. Native: stage `request.props` for the privileged helper | all detected kinds |
| **Push update** | `POST /admin/update/push` | Same orchestrator, but it always runs and targets the **currently running** version (or a `target` you post) — the tool for "rebuild exactly this commit" and for retrying after a failure | all detected kinds |
| **Pull image** | `POST /admin/update/pull-image` | Pulls a registry image + tag (`ghcr.io/barssky/skygate` by default) and recreates the container — much faster than the git+build path (~5–10 s warm) | Docker only |
| **Rollback now** | `POST /admin/update/rollback` | Cancels the running job; on native installs it re-runs the helper with the **previous release tag** from the job state. A missing previous tag means "use the manual steps" | all kinds |
| **Dismiss** | `POST /admin/update/dismiss` | Hides the update banner | all kinds |
| **Auto-update schedule** | `POST /admin/update/schedule` | Time-of-day scheduler (HH:MM) + enable/disable, stored in the DB and re-read on every tick | Docker only (native kinds are skipped by the scheduler) |

Environment defaults for the above (the page can override the schedule at runtime):

| Variable | Default | Meaning |
|---|---|---|
| `SKYGATE_UPDATE_CHECK` | `true` | Poll for new releases at all. `false` for air-gapped hosts |
| `SKYGATE_UPDATE_CHECK_INTERVAL` | `24h` | Poll interval; `off`/`0` disables |
| `SKYGATE_UPDATE_CHANNEL` | `stable` | `all` also considers prereleases |
| `SKYGATE_AUTO_UPDATE_ENABLED` | `false` | Shows the "Apply" affordance; **never** applies anything on its own |
| `SKYGATE_UPDATE_SCHEDULE_ENABLED` | `false` | Enables the time-of-day scheduler |
| `SKYGATE_UPDATE_SCHEDULE_TIME` | `03:00` | Local time, 24-hour `HH:MM` |
| `SKYGATE_GITHUB_TOKEN` | *(unset)* | Optional; raises the GitHub API rate limit from 60/h to 5000/h |

### 4.4 Scheduled auto-update (Docker only)

With `SKYGATE_UPDATE_SCHEDULE_ENABLED=true` (or the checkbox on the page) the
scheduler applies the newest detected release at the configured `HH:MM`, guarded by
the usual safety rails (a job already in flight, a failed detection, or a
non-Docker install kind ⇒ the tick is skipped, not queued).

**Native installs are never updated automatically** — systemd/OpenRC/bare require
the explicit **Update** click, because the helper restarts the service.

---

## 5. Native installs (systemd / OpenRC / bare)

Native self-update exists since **v1.5.9**. Before that, the buttons on a native
install failed with *"auto-updater for systemd not yet implemented"*.

### 5.1 Why a privileged helper (the security model)

The service runs unprivileged under `ProtectSystem=strict` and cannot write
`/usr/local/bin/skygate` or restart its own unit (restarting from inside the unit
kills the whole cgroup, so a `setsid` child dies with it). Instead of running
skygate as root, the update is split in two:

```
unprivileged skygate ──writes──> <data_dir>/update/request.props   (data only: TAG, JOB_ID, …)
                                              │
                                   root-owned skygate-update.path  (watches the file)
                                              │
                                   root-owned skygate-update.service (oneshot)
                                              │
                    /usr/local/lib/skygate/skygate-apply-update.sh  (the only root actor)
                       backup → download → verify SHA256 → migrate-only
                       → atomic rename swap → restart → poll /healthz build string
                       → on failure restore skygate.prev + restart
                                              │
                                  writes result.status / result.build /
                                  result.error / apply.log
                                              │
                       skygate folds the verdict into the update state on the
                       next /admin/update render (update.ConfirmNativeSwap)
```

Key properties worth knowing:

* `request.props` carries **no paths** — it is parsed as data and never sourced.
  Every path (binary, service name, env file, health URL, update dir, allowed
  owner/repo) comes from the **root-owned** `/etc/skygate/update-helper.conf`. A
  compromised skygate can ask for "install official release tag X" and nothing
  else. The target must match `^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`, so shell
  metacharacters and `..` never reach the helper.
* `/etc/skygate/skygate.env` is owned by the *service user* and is **parsed, then
  passed via `env(1)`** — never sourced by root.
* The binary is swapped with an **atomic `rename`** (writing over a running
  executable fails with `ETXTBSY`), and the previous binary is kept at
  `<data_dir>/update/skygate.prev`.
* Success is defined as `/healthz` returning `200` **and** the build string of the
  target tag — not merely "something is listening".
* The helper always exits 0 when it wrote a verdict; the verdict itself lives in
  `result.status` (`done` | `rolled_back` | `failed`).

### 5.2 Triggering it and watching it

1. `/admin/update` → **Update** (or **Push update**) with a **release tag** as the
   target. A git-describe label or a raw commit SHA is refused up front, because
   GitHub has no assets for them.
2. skygate writes `request.props` into `<data_dir>/update/` and reports
   "waiting for the privileged helper".
3. systemd (or the sudoers drop-in on OpenRC/bare) fires the applier. The page
   shows the phase as the state file is updated; `apply.log` is appended to the
   page once the job settles.
4. The verdict appears on the page in the **old** process (helper bailed before the
   restart) or in the **new** one (after a successful swap).

The state file lives at `SKYGATE_UPDATE_STATE_PATH`
(default `/data/skygate-update-status.json` inside the container,
`<data_dir>/skygate-update-status.json` on native installs). If the page says
"waiting" for more than 5 minutes it also prints a staleness warning — check
`journalctl -u skygate-update` and `<data_dir>/update/apply.log`.

### 5.3 Helper configuration — `/etc/skygate/update-helper.conf`

Written by the installers (`write_update_helper` in `deploy/install-common.sh`),
root-owned, `chmod 0644`. Edit it only if your layout differs from the defaults.

| Key | Default | Meaning |
|---|---|---|
| `SKYGATE_UPDATE_MODE` | `systemd` | `systemd` \| `openrc` \| `bare` — how the helper restarts the service |
| `SKYGATE_UPDATE_DIR` | `/var/lib/skygate/update` | Request/result/log staging directory |
| `SKYGATE_UPDATE_SERVICE` | `skygate` | Unit / service name |
| `SKYGATE_UPDATE_BINARY` | `/usr/local/bin/skygate` | Binary to swap |
| `SKYGATE_UPDATE_RUN_USER` | `skygate` | User that runs `migrate-only` and the service |
| `SKYGATE_UPDATE_ENV_FILE` | `/etc/skygate/skygate.env` | Parsed for `SKYGATE_DB` (never sourced) |
| `SKYGATE_UPDATE_HEALTH_URL` | `http://127.0.0.1:8080/healthz` | Post-swap verification endpoint |
| `SKYGATE_UPDATE_OWNER` / `_REPO` | `BarsSky` / `skygate` | Only this repo's releases can be installed |
| `SKYGATE_UPDATE_ASSET_ARCH` | `linux-amd64` | Release asset architecture |
| `SKYGATE_UPDATE_BARE_START` | *(empty)* | Command the helper uses to start the process in `mode=bare` |
| `SKYGATE_UPDATE_BASE_URL` | *(unset)* | Internal mirror ([§7](#7-mirrors-and-air-gapped-hosts)) |
| `SKYGATE_UPDATE_GITHUB_TOKEN` | *(unset)* | Optional token for the Releases API fallback |

Runtime-only knobs (handy for a dry run or a slow host):

| Variable | Default | Meaning |
|---|---|---|
| `SKYGATE_UPDATE_DRY_RUN=1` | `0` | Stop after the migration step, **before** the swap |
| `SKYGATE_UPDATE_HEALTH_TIMEOUT` | `90` | Seconds to wait for the target build string |
| `SKYGATE_UPDATE_HEALTH_POLL` | `1` | Poll interval |

Run the applier by hand (as root, e.g. to reproduce a failure):

```bash
sudo SKYGATE_UPDATE_DRY_RUN=1 /usr/local/lib/skygate/skygate-apply-update.sh
sudo tail -f /var/lib/skygate/update/apply.log
```

### 5.4 Where the artifact comes from, and how it is verified

The applier downloads `<base>/skygate-<TAG>-linux-amd64.tar.gz` and verifies it
against, in order:

1. **`SHA256SUMS`** published next to the asset (release asset for GitHub, mandatory
   for a mirror). This is the intended path for v1.5.9+; releases v1.5.6–v1.5.8
   shipped **no** `SHA256SUMS` asset (a packaging bug fixed in v1.5.9).
2. **GitHub Releases API per-asset `digest: sha256:<hex>`** for the same
   owner/repo/tag — the same trust root as the tarball, extracted with `awk` (no
   `jq` dependency). Used automatically when the `SHA256SUMS` asset is missing or
   does not list the asset.
3. **Nothing left ⇒ fail closed.** If neither source yields a checksum the update
   is refused *before* the swap; the helper never installs an unverified binary.

For a mirror (`SKYGATE_UPDATE_BASE_URL` set) step 2 does not exist: the mirror
**must** publish `SHA256SUMS`, and the applier fails if the file is missing or does
not list the asset.

### 5.5 Automatic rollback, and rolling back on purpose

* **Automatic**: any failure after the swap (service will not start, `/healthz`
  never reports the target build, timeout) restores `skygate.prev`, restarts, checks
  that the service is serving again, and writes `result.status=rolled_back`. Live
  verification on the reference VM restored the previous binary **byte-for-byte**.
* **Manual (same version family)**: `/admin/update` → **Rollback now**. On a native
  install this re-runs the helper with the previous release tag from the job state.
* **Manual (manual steps)**: the page always renders the per-kind manual update and
  rollback command lists (`internal/update/manual.go`) — copy, paste, adapt. Treat
  those as a template and check the asset names against the release page (see the
  known gap in [§10](#10-known-gaps-and-version-notes)).

### 5.6 Bare and OpenRC specifics

* **OpenRC (Alpine)** — there is no systemd path unit, so the installer drops a
  narrow `/etc/sudoers.d/skygate-update` rule and the service triggers the applier
  with `setsid sudo -n <applier>`; the applier restarts via
  `rc-service skygate restart` (`MODE=openrc`, with a `start` fallback).
  `/etc/conf.d/skygate` exports `SKYGATE_INSTALL_KIND=openrc`,
  `SKYGATE_UPDATE_STATE_PATH` and `SKYGATE_UPDATE_DIR` so the page knows the
  platform without guessing.
* **Bare** — no supervisor: `MODE=bare` stops the recorded PID and starts the
  process again with `SKYGATE_UPDATE_BARE_START`. If that key is empty, restart the
  process yourself and re-check `/healthz`.
* All three modes run `migrate-only` as the service user **before** the swap, so a
  migration that cannot be applied aborts the update with the old binary still in
  place.

---

## 6. Manual update procedures

Use these when the in-app path is unavailable (no admin session, air-gapped,
Windows, or a broken binary). Always back up the DB first.

### 6.1 Docker compose (source checkout)

```bash
cd /path/to/skygate
sudo docker compose stop skygate                      # graceful WAL flush for SQLite
git remote set-url origin https://github.com/BarsSky/skygate.git
git fetch --tags --prune --force
git checkout v1.5.9
sudo chown -R "$(id -un)":"$(id -gn)" data/ts/        # container tailscaled writes as root
sudo docker compose build skygate
sudo docker run --rm --volumes-from skygate skygate-skygate:latest /app/skygate migrate-only
sudo docker compose up -d --force-recreate --no-deps skygate
until curl -fsS http://127.0.0.1:8080/healthz | grep -q '1.5.9'; do sleep 3; done
```

`migrate-only` is a **subcommand**, not a flag — `--migrate-only` prints
`unknown command`.

### 6.2 Docker compose (prebuilt image)

The same as [§4.2](#42-docker-compose-prebuilt-image): edit `SKYGATE_IMAGE`, then
`pull` + `up -d --force-recreate --no-deps skygate`. Rollback = set the tag back.

### 6.3 Native systemd

```bash
TAG=v1.5.9; ARCH=linux-amd64; BASE=https://github.com/BarsSky/skygate/releases/download/$TAG
cd /tmp
curl -fsSLO $BASE/skygate-$TAG-$ARCH.tar.gz
curl -fsSLO $BASE/SHA256SUMS && sha256sum -c --ignore-missing SHA256SUMS
tar -xzf skygate-$TAG-$ARCH.tar.gz
sudo systemctl stop skygate
sudo cp -p /usr/local/bin/skygate /usr/local/bin/skygate.prev        # rollback target
sudo env $(grep -v '^#' /etc/skygate/skygate.env | xargs) ./skygate migrate-only
sudo install -m 0755 skygate /usr/local/bin/skygate                  # stops the service anyway
sudo systemctl start skygate
systemctl is-active skygate && curl -fsS http://127.0.0.1:8080/healthz
```

If the unit refuses to start after a crash loop: `sudo systemctl reset-failed skygate`
(systemd keeps the failed state and will not restart a unit in that state).

### 6.4 Native OpenRC (Alpine)

Identical, with `rc-service skygate stop|start` instead of `systemctl`, and the env
file loaded through `/etc/conf.d/skygate`.

### 6.5 Bare binary

```bash
kill -TERM "$(pidof skygate)"                 # or your supervisor's stop
cp -p skygate skygate.prev
# download + verify exactly as in §6.3, then:
./skygate.<new> migrate-only
mv skygate.<new> skygate && chmod +x skygate   # atomic rename
nohup ./skygate >> skygate.log 2>&1 &         # or supervisorctl start skygate
until curl -fsS http://127.0.0.1:8080/healthz | grep -q '1.5.9'; do sleep 2; done
```

### 6.6 Windows

```powershell
.\deploy\Setup-Skygate-Win.ps1 -Version v1.5.9      # downloads, installs, re-registers the service
Get-Service Skygate | Restart-Service
Invoke-RestMethod http://127.0.0.1:8080/healthz
```

The previous `skygate.exe` is not kept automatically — copy it aside first if you
need a rollback path.

---

## 7. Mirrors and air-gapped hosts

Set `SKYGATE_UPDATE_BASE_URL` in the **root-owned** helper config, layout
`<base>/<TAG>/skygate-<TAG>-linux-amd64.tar.gz` **plus** `<base>/<TAG>/SHA256SUMS`:

```bash
# /etc/skygate/update-helper.conf
SKYGATE_UPDATE_BASE_URL="https://mirror.example.com/skygate"
```

```bash
# mirror layout
/srv/mirror/skygate/v1.5.9/skygate-v1.5.9-linux-amd64.tar.gz
/srv/mirror/skygate/v1.5.9/SHA256SUMS
```

* `https://` for anything remote; plain `http://` is accepted **only** for loopback
  mirrors (`127.0.0.1`, `localhost`, `[::1]`) — on the same host.
* `SHA256SUMS` is mandatory for a mirror (there is no GitHub digest to fall back
  to). Mirror mode is chosen because the base URL is set in root-owned config: a
  compromised skygate cannot redirect the download.
* Air-gapped hosts should also set `SKYGATE_UPDATE_CHECK=false` so the UI stops
  polling GitHub, and can keep using `Check now` — or simply paste the target tag
  into the update form.

---

## 8. After the update

```bash
curl -fsS http://127.0.0.1:8080/healthz   # expect {"status":"ok","version":"…"}
curl -fsS http://127.0.0.1:8080/readyz    # db + headscale + headplane + tailscale
```

1. `/healthz` must report the **target** build string; `readyz` must list every
   dependency as ok.
2. Open `/admin/update` — the job should read `done`, with the helper's
   `apply.log` attached on native installs.
3. Spot-check the pages you depend on (dashboard, `/admin/devices`, exit
   rules/DERP/Tailscale panels) and the login flow.
4. For a release cut in this repo, run the pre-deploy gate on the host that has
   the checks' dependencies: `bash scripts/verify_pre_deploy.sh` — it must end at
   `0 FAIL` (checks that need live state report `SKIP`, not `FAIL`).
5. Roll back if any of the above fails: automatic on native (the helper already
   did it), **Rollback now** in-app, or the manual steps in [§6](#6-manual-update-procedures).

---

## 9. Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| Native **Update** button used to say *"auto-updater for systemd not yet implemented"* | pre-v1.5.9 binary | upgrade to v1.5.9+ (or use the manual steps) |
| Page says *"helper not installed"* / the request file is never picked up | installer predates v1.5.9, or `skygate-update.path` is not enabled | re-run `deploy/install.sh` (idempotent), then `systemctl status skygate-update.path` |
| Job stays "waiting for the privileged helper" > 5 min | applier failed before writing a verdict, or the path unit never fired | `journalctl -u skygate-update -n 200`, `tail <data_dir>/update/apply.log` |
| `could not verify … (no SHA256SUMS asset and no GitHub asset digest)` | release has no checksums, or GitHub API rate-limited | use v1.5.9+ assets, set `SKYGATE_UPDATE_GITHUB_TOKEN`, or point at a mirror with `SHA256SUMS` |
| `SHA256 mismatch` | corrupted download or a mirror that does not match the release | re-download; fix the mirror (never bypass the check) |
| `target "…" is not a release tag` | the form got a git-describe label (`v1.5.8-39-g…`) or a bare SHA | pass a real tag (`v1.5.9`); for a commit build use the manual steps |
| Update aborts, service still on the old version, `result.status=rolled_back` | new binary booted but `/healthz` never reported the target build | read `apply.log`; the old binary is back in place — this is the designed safety net |
| Update aborts before the swap with a migration error | DB cannot be migrated (e.g. SQLite on a pre-v1.5.9 release) | migrate to a compatible release; the old binary was never replaced |
| `systemctl restart` refuses after a crash loop | unit is in `failed` state | `sudo systemctl reset-failed skygate` |
| `docker compose pull` → `denied` / `no matching manifest for linux/amd64` | registry blocked (a local build is needed) or an ARM-only tag | use the source-rebuild path, or the Go tarball on ARM |
| Scheduled update never fires | install kind is native (the scheduler skips it) | use **Update** manually, or keep a Docker install |
| `.env` values ignored after `docker compose -f …` from another CWD | compose interpolation is CWD-relative | `cd` into the project dir or pass `--env-file` |

---

## 10. Known gaps and version notes

* **v1.5.8 and older cannot run on SQLite** (`duplicate column name: user_name`
  during migration v44, fresh or existing DB). Native SQLite hosts need v1.5.9+;
  the applier's pre-swap migration step is what prevents a bricked install.
* **`SHA256SUMS` is only attached to releases from v1.5.9 on.** Older releases are
  verified through the GitHub asset digest automatically; a mirror cannot use that
  fallback.
* **The in-app manual command lists** (`internal/update/manual.go`) still reference
  the historical asset names `skygate-linux-amd64` / `…​.sha256`, which the current
  release pipeline does **not** publish (it publishes
  `skygate-<TAG>-linux-amd64.tar.gz` + `SHA256SUMS`). Copy the commands from
  [§6](#6-manual-update-procedures) of this document instead, and check the release
  page. *(Tracked as a documentation defect.)*
* **The Docker image is linux/amd64 only** by design; the Go tarballs cover
  linux/darwin × amd64/arm64 and windows-amd64.
* **Native installs never auto-apply** — the scheduler is Docker-only, and
  `SKYGATE_AUTO_UPDATE_ENABLED` only controls whether the in-app "Apply"
  affordance is shown.
* **The registry rate-limits unauthenticated update checks**; set
  `SKYGATE_GITHUB_TOKEN` (5000/h) if you check frequently.
