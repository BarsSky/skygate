# Release v1.5.9 — checklist and notes (draft)

**Status:** prepared, NOT tagged. The tag/release is an operator decision
(public artifact + CI + ghcr push), so nothing here runs until you say go.
**Base:** `v1.5.8` → `main` @ `e2802c51` (33 commits).
**Author of this checklist:** agent session 2026-09-18 (B261/B262/B252.1/B253 +
gate cleanup). Facts below are from that session; each claim names its evidence.

---

## 1. What is in v1.5.9

### The §12 hardening plan (docs/plans/2026-09-17-sqlite-pg-and-headscale-hardening.md)

| Area | Commit | One-line |
|---|---|---|
| SQLite migration chain | `1f55e44f` | V071 was missing, `IF NOT EXISTS` ADD COLUMN silently no-op'd on SQLite, and the chain broke on every second start. Repaired + idempotency tests (§12.13) |
| Dialect separation | `9f39c6f1` | runtime-branching dialect shims; removes one-backend-only SQL from the shared layer |
| R6 (raw error pages) | `9a44d822`, `4e0193e5`, `13766e42` | DB errors no longer replace the whole page with text/plain; a guard test freezes the remaining 101 sites so the class cannot grow |
| R7 (preauth keys) | `3cbbffab` | never hand out a key skygate cannot account for |
| R6 rollout procedure | `8f82d7ba` + plan §12.14 | per-file inventory for paying the rest down |

### Self-update (the main feature)

* **`1ee1366b`** — install-kind detection checks container markers before
  systemd (a container on a systemd host used to be told to run `systemctl`).
* **`5af182db` B261** — native (systemd/bare) self-update via a privileged
  helper: the unprivileged service writes a data-only `request.props`, a
  root-owned path unit fires a root-owned applier that does
  backup → verified download → pre-swap `migrate-only` → atomic rename swap →
  restart → `/healthz` **build-string** check → rollback. Live-verified:
  happy path `done`, real rollback `rolled_back` (plan §12.16).
* **`f3aaa3fb` B261.1** — **`SKYGATE_DB=sqlite:/path` never opened**, i.e.
  every native SQLite install was dead on arrival (the form is what the
  installer writes; modernc's driver does not know the `sqlite:` scheme).
* **`a0893b1f` B261.2** — the published releases carry no `SHA256SUMS` asset,
  so verification now falls back to GitHub's per-asset digest.
* **`11978945` B261.3** — work dir was 0700 root-owned while migrations run as
  the service user.
* **`e61e5433` B261.4** — `--migrate-only` does not exist (it is the
  subcommand `migrate-only`); the manual steps had the same typo. Also fixed
  the hardcoded `DB backend: postgres` log lines.
* **`c3dce90f` B261.5** — `SKYGATE_UPDATE_BASE_URL` mirror support
  (root-owned, https or loopback, SHA256SUMS mandatory).
* **`ac048d1a` B262** — OpenRC/Alpine as a first-class install kind
  (`rc-service` restart, `install-alpine.sh` installs the helper) **and** the
  release-asset fix: `release.yml` downloaded `SHA256SUMS` into a directory of
  the same name, so the flatten step left a DIRECTORY and v1.5.6–v1.5.8 shipped
  no checksum file at all.

### UI halves that were never merged (checks were red because the feature was missing)

* **`ebc0ce14` B252.1** — `/admin/derp` certificate auto-renewal card +
  `PostAdminDerpCertSyncRun` (the route answered 501 before).
* **`ebc0ce14` B253** — `/admin/telegram` stale-while-revalidate probe
  (30s success / 5min failure TTL) + the "Probe now" button (route was 501).

### Gate hygiene

`9dccc6f2`, `e2802c51`, `c1d14644`, `4742f062`, `0b51cf1e` — see §3.

---

## 2. Verification already done (before tagging)

* `scripts/verify_pre_deploy.sh` — run locally at `e2802c51` (the full gate,
  ~10 min). B261’s 26 contracts pass; the previously-red checks are fixed or
  SKIP cleanly (see the commit messages for the per-check rationale).
* `go build ./...`, `go vet ./...`, `staticcheck ./...` — clean.
* `go test ./internal/update/... ./internal/db/ ./internal/feature/admin/...
  ./internal/i18n/...` — green.
* **Live on <VM_HOST>**:
  * **clean-host acceptance (install → update)**, run before the tag at the
    operator's request in a throwaway `jrei/systemd-debian:12` container
    (`--privileged --cgroupns=host`, systemd 252 as PID 1), nothing on the VM
    touched:

    ```
    install-debian.sh --db-type=sqlite --install-kind=systemd   # v1.5.8 + SKIP_VERIFY=1
      → unit has Environment=SKYGATE_INSTALL_KIND / _UPDATE_STATE_PATH / _UPDATE_DIR
      → skygate-update.{path,service} installed, path unit ACTIVE
      → helper script + /etc/skygate/update-helper.conf, env SKYGATE_DB=sqlite:/…
    # v1.5.8 itself cannot open that DB (pre-§12.13 chain) — expected
    printf 'TARGET=v1.5.9…' > /var/lib/skygate/update/request.props   # as skygate
      → the root-owned path unit fired the applier by itself, 6s total:
          SHA256 OK (verified against SHA256SUMS asset)
          migrate-only: opening sqlite (DSN=sqlite:/…) → OK
          restarting (systemd) → healthz reports build 'v1.5.9+good1234' after 2s
          verdict: done            (unit active, /healthz serves v1.5.9)
    ```

    **It paid for itself: it found a real installer bug.** On minimal Debian 12
    `write_env_file` died at `xxd: command not found` — `xxd` ships in the
    xxd/vim-common package, which is in NONE of the installer dependency
    lists — and under `set -euo pipefail` the install **aborted between
    "installed: /usr/local/bin/skygate" and the env file / unit / helper**.
    Fixed in `739a314f` (openssl → od (coreutils) → xxd, clear error if none);
    contracts N/N2 pin the fallbacks and their order.
  * happy-path native update: `verdict: done`, `/healthz` matched
    `v1.5.9+good1234` in 2s;
  * real rollback: an artifact that boots but reports another build →
    `verdict: rolled_back`, previous binary restored byte-for-byte, service
    healthy;
  * pre-swap aborts: unknown tag / a release that cannot migrate the DB →
    `failed` **before** the swap, service untouched;
  * OpenRC branch in an Alpine container: `restarting (openrc)` →
    `rc-service skygate restart` → `verdict: done`;
  * installer path for real: `write_update_helper` produced
    `/etc/skygate/update-helper.conf` + `skygate-update.{path,service}`, path
    unit `active`;
  * prod container unaffected throughout: `/readyz` db/headscale/headplane/
    tailscale all ok; the operator's three local files (`docker-compose.yml`,
    `go.mod`, `go.sum`) survived every `git pull`.

---

## 3. Pre-tag checklist

1. `git status --porcelain` — clean (locally and on the VM).
2. `bash scripts/verify_pre_deploy.sh` on the VM **and** locally — every line
   PASS or an explicit SKIP (no FAIL).
3. `staticcheck ./...` → 0 issues (B237.20 contract).
4. Confirm the version bump is not needed: the build label comes from
   `git describe`, so the tag alone defines it.
5. Decide the release notes text (this file is the draft).
6. Make sure the release workflow has the fixed `path: dist/checksums`
   (B262) — i.e. this file’s commit is in the tagged commit.

## 4. Post-tag checklist (the part that proves the fixes)

1. **`SHA256SUMS` is attached as a FILE** (the B262 proof):
   `curl -sS https://api.github.com/repos/BarsSky/skygate/releases/tags/v1.5.9 | grep -A2 '"name": "SHA256SUMS"'`
   — and confirm it is a file, not a directory, on the release page.
2. **ghcr image tag is lowercase** (B237.24): `docker pull ghcr.io/barssky/skygate:v1.5.9`.
3. Tarball sanity: `./skygate version` on the extracted linux-amd64 asset
   reports `v1.5.9…` with a `+<sha>` suffix.
4. **Native SQLite install end-to-end** (the real acceptance test):
   on a scratch host (or the VM with a separate data dir/port):
   `sudo bash deploy/install-debian.sh --db-type=sqlite --install-kind=systemd`,
   fill `HEADSCALE_URL`/`HEADSCALE_API_KEY`, start the unit, open
   `/admin/update`, run an update to `v1.5.9` and confirm `done` + a `/healthz`
   build match. This is the flow that v1.5.8 could not do at all.
5. Re-run the live-state checks ON the VM (they SKIP elsewhere):
   `bash scripts/check_b191.sh`, `check_b_admin_user_sync.sh`,
   `check_b_duplicate_users.sh`, `check_b_reconcile_audit_writes.sh`,
   `check_b_node_owner_map_orphans.sh`, `check_b_tag_owners.sh`,
   `check_b_node_attribution.sh`.

## 5. Known gaps (unchanged by this release)

* **v1.5.8 cannot run on SQLite at all** (its chain predates `1f55e44f`):
  a native SQLite host on v1.5.8 must install v1.5.9 from the tarball, not
  self-update to older tags. The applier refuses such a target **before** the
  swap, so the attempt is safe.
* **bare (no service manager) mode** is covered by unit tests + the contract
  but has not been exercised on a live bare host; OpenRC and systemd are.
* **~25 silent `ADD COLUMN` loops** remain in `migrations_sqlite.go`
  (harmless on a fresh DB, but they still hide real failures). Planned as
  `execSQLiteDDL` + `addColumnIfMissingSQLite` — see §12.13/§12.16.
* **R6** still has 101 raw-error sites (the guard freezes the number; §12.14
  has the per-file inventory).

## 6. Rollback plan (if the release is bad)

Per the operator’s rule (“delete the tag if CI failed”): delete the GitHub
release + the tag, fix, re-tag. For an operator already updated: the applier
keeps `<data_dir>/update/skygate.prev` and the `/admin/update` page offers an
explicit rollback to the previous tag; the manual fallback is
`sudo install -m 0755 <data_dir>/update/skygate.prev /usr/local/bin/skygate &&
sudo systemctl restart skygate`.
