# Skygate operations handbook

This is the consolidated day-2 operations handbook for skygate. It replaces the
runbooks that were removed in the **2026-09-18 documentation restructure**
(`docs/runbooks/clean-install.md`, `pg-cutover.md`, `infra-retag.md`,
`issues-closeout.md`, `svyatoslava-bootstrap.md`, `tailnet-split-fix.md` and the
internal `auto-deploy-baseline.md`, `auto-deploy-test-plan.md`,
`release-v1.5.9.md`, `telegram-relay.md`, `architecture/wal-g-notes.md`); every
procedure below is carried over from those files, and the full original text
stays in git history. Install paths live in [INSTALL.md](INSTALL.md), the
update paths in [UPDATE.md](UPDATE.md), and the environment-variable reference
in [deploy.md](deploy.md) — this file does not repeat them.

**Reading order for a specific job:** §1 release, §2 deploy pipeline, §3 DB
cutover, §4 new host, §5 retag, §6 tailnet split, §7 issue close-out, §8
Telegram relay, §9 backup/WAL-G, §10 the recurring checklist, §11 admin role
delegation.

## Table of contents

1. [Release process](#1-release-process) — tag → `release.yml` jobs (docker amd64-only, Go tarballs, `SHA256SUMS`, GitHub release), pre/post-tag checklists, asset verification, clean-host acceptance, rollback.
2. [Deployment pipeline](#2-deployment-pipeline) — baseline, script inventory, ordering guarantees, test phases, failure triage.
3. [PostgreSQL cutover / migration between hosts](#3-postgresql-cutover--migration-between-hosts) — pre-flight, quiesce, dump/restore, DSN switch, verification, rollback.
4. [New-host bootstrap](#4-new-host-bootstrap) — prerequisites, DNS/ports, secrets, first admin, verification, rollback.
5. [Infrastructure retag / rename procedure](#5-infrastructure-retag--rename-procedure) — safe ordering, defensive rename, failure modes.
6. [Tailnet split fix](#6-tailnet-split-fix) — symptoms, cause, fix.
7. [Issues close-out workflow](#7-issues-close-out-workflow) — status matrix, evidence, close comments.
8. [Telegram relay operations](#8-telegram-relay-operations) — deploy, verify, maintain, fail over, roll back.
9. [Backup, WAL-G and the restore drill](#9-backup-wal-g-and-the-restore-drill) — install, archiving, base backups, drill, v3.0.8 quirks.
10. [Day-2 checklist](#10-day-2-checklist) — weekly / before every release / after every incident.
11. [Admin role delegation and the primary admin](#11-admin-role-delegation-and-the-primary-admin) — promote/demote from `/admin/users`, who is immutable, and how to recover the primary marker.
12. [Caveats — stale or contradictory sources](#12-caveats--stale-or-contradictory-sources)

---

## 0. Conventions and placeholders

Every host, IP and secret below is a placeholder: `<VM_HOST>` (primary skygate
host), `<DB_HOST>`/`<DB_PORT>` (PostgreSQL endpoint), `<GATEWAY>` (LAN
gateway/router), `<OPERATOR_USER>` (the headscale/portal user that owns the
cluster), `<HA_HOST>` (standby skygate host), `<RELAY_HOST>` (Tailscale relay
with unrestricted egress), `<NPM_HOST>` (TLS-terminating proxy),
`<MINIO_HOST>` (S3-compatible backup endpoint). IPs are RFC 5737
(`192.0.2.x`, `198.51.100.x`, `203.0.113.x`) plus `100.64.x.y` for
Tailscale/CGNAT; domains are `example.com` variants.

Two rules keep this file committable: **never paste a real LAN/private IP,
internal hostname or secret** into a doc or script (the `.githooks/pre-commit`
hook blocks known private addresses but is not exhaustive), and **keep secrets
outside the repo** (`chmod 600` `.env`, `/etc/skygate/` for native installs,
`/etc/skygate-secrets/` for OIDC/DERP material) — reference them by path, never
by value.

> **Caveat:** one superseded runbook carried a live-looking preauth key inline,
> and two carried real public IPs and internal hostnames. None of those values
> are reproduced here; treat any preauth key found in git history as compromised
> and rotate it (see §6).

---

## 1. Release process

### 1.1 What a tag produces

A push of a tag matching `v[0-9]+.[0-9]+.[0-9]+*` (e.g. `v1.5.9`,
`v1.5.10-rc.1`) triggers `.github/workflows/release.yml`. It has four jobs:

| Job | Produces |
|---|---|
| `docker` | `ghcr.io/<owner-lowercase>/skygate:<tag>` — **linux/amd64 only** — plus, for non-prerelease tags only, `:latest`, `:vX.Y` and `:vX`. Provenance and SBOM attestations are disabled so the GitHub install widget shows one image, not three. |
| `binaries` | Go tarballs/zips, `CGO_ENABLED=0`, matrix = linux/darwin × amd64/arm64 + windows-amd64. Each archive is flat (`./skygate` + `./skygate.sha256`) so install scripts can `tar -xzf … && ./skygate`. |
| `sums` | `SHA256SUMS` — one line per archive, in `sha256sum -c` format. |
| `release` | The GitHub Release. Its body is the `## vX.Y.Z` section extracted from **`RELEASE-NOTES.md`** (checked out sparsely by that job); if the section is missing the workflow generates a commit list instead, so the body is never empty. v1.5.8's empty body came from this job having **no checkout at all**. |

Why amd64-only for Docker: the pre-v1.5.4 matrix pushed the same tag from an
amd64 and an arm64 job, and the second push overwrote the first — `:v1.5.2`
ended up with *no* amd64 manifest (Issue #4). The Go tarballs still cover ARM;
an operator who needs an ARM image builds `docker buildx build --platform
linux/arm64` from the released source. Re-enabling multi-arch is a future
B-block, not a quick edit.

### 1.2 Cutting a release

**B281 (v1.5.46): "green" now means 0 FAIL.** The gate is only as good as the
status it reads, and that status used to be empty of information: the
`verify-pre` job ran the *whole* catalog — which is deliberately fail-tolerant
(it always exits 0; see §1.3) — and never inspected its output, so a SUCCESS job
could contain a dozen FAIL rows. It also had no per-check budget, which is why
it came back as `cancelled` on every push (it exceeded its own
`timeout-minutes`, and the log just stopped mid-catalog). Now each check runs
under `timeout "${SKYGATE_CHECK_TIMEOUT:-900}"` (a blown budget is a **named**
`TIMEOUT` row), the job budget is 60 minutes, and the run step fails the job on
any `FAIL`/`TIMEOUT` row in the ANSI-stripped log. So:

* a green `verify-pre` means the catalog really reported `0 FAIL`;
* if `verify-pre` is red, read the FAIL rows in the log — they name the check,
  and a standalone `bash scripts/check_bNNN*.sh` reproduces it;
* the `test` job and the catalog can now disagree only about real code, not about
  which `go` binary either of them found.

**B280 (v1.5.46): a tag and a release are CI-gated.** `scripts/ci_gate.sh` is the
single implementation of "is `ci.yml` green for this exact commit?" (exit `0`
green / `1` not green / `2` cannot verify; `--wait` for a run in progress). It is
called from three places, so they cannot disagree:

| Caller | What it refuses |
|---|---|
| `.githooks/pre-tag` (local) | `git tag -a vX.Y.Z` while CI is red / running / absent, or the commit is not on `main` |
| `.github/workflows/tag-release.yml` | creating the tag at all — this is the sanctioned path, it gates **before** `git tag` and then dispatches `release.yml` |
| `.github/workflows/release.yml` → `preflight` job | the whole release (images, binaries, Release) — **every** publishing job `needs: preflight`, so a tag pushed with `--no-verify` still publishes nothing |

Preferred procedure:

```bash
# 1. Write the "## vX.Y.Z" section at the TOP of RELEASE-NOTES.md and land it
#    in the commit you are about to tag (the workflow extracts that section).
git status --porcelain            # must be empty (locally AND on the VM)
git log --oneline -5

# 2. Push main and WAIT for ci.yml to finish (the gate insists on a
#    completed, successful run for that exact sha).
git push origin main
gh run watch

# 3. Create the tag on the server, CI-gated:
gh workflow run tag-release.yml -f version=vX.Y.Z
#    or: Actions → tag-release → Run workflow → version: vX.Y.Z
#    It verifies the gate, creates the annotated tag, pushes it and starts
#    release.yml at the tag. (The dispatch is required: a tag pushed with the
#    repository GITHUB_TOKEN does not fire `push` events.)

# 4. Watch the release (all five jobs, preflight first).
gh run watch
```

The direct path still works when the hooks are installed — `git tag vX.Y.Z &&
git push origin vX.Y.Z` — and ends in the same place: the local hook checks
first, and `release.yml`'s `preflight` checks again on the server. What the
direct path cannot do is bypass the rule: if `ci.yml` is not green for the
tagged commit, `preflight` fails and `docker`, `binaries`, `sums` and `release`
are all skipped, so nothing is published and no image tag moves.

Escape hatches (both are deliberate, narrow, and visible where they are taken):

* `SKIP_PRE_TAG_CHECK=1 git tag -a vX.Y.Z …` — skips the **local** hook only;
  `preflight` still refuses to publish. Use only in a true emergency and say so
  in the release notes.
* Repository variable `SKYGATE_ALLOW_TAG_OFF_MAIN=1` — allows a tag whose commit
  is not an ancestor of `main` (a genuine hotfix branch). The CI check itself is
  never skippable.
* Repository variable `SKYGATE_PREFLIGHT_WAIT_SECONDS` (default `900`) — how
  long `preflight` waits for a run that is still in progress before refusing.

Pre-release tags (anything with a `-` before the first digit sequence, e.g.
`-rc.1`, `-alpha1`) skip `:latest`, `:vX.Y` and `:vX`, and are marked
pre-release on the GitHub Release. That is deliberate: production pins
`:vX.Y.Z` and can never be surprised by a pre-release.

The workflow has `concurrency: release-${{ github.ref }}` with
`cancel-in-progress: true`, so re-pushing a corrected tag cancels the previous
run.

**Why this exists.** The v1.5.41 → v1.5.45 cycle pushed tags after
`git push --no-verify`. CI caught two real regressions in that window — v1.5.44
introduced an RU i18n parity break and a raw-`http.Error` leak — and both
releases shipped them anyway, because the tag already existed and `release.yml`
builds whatever the tag points at. A tag is a promise that the commit was
tested; `preflight` is what makes the promise checkable. Note the flip side of
`concurrency`: a ci.yml run that was **cancelled** because a newer commit landed
counts as "not green" for the cancelled commit — re-run it (`gh run rerun <id>`)
or tag a commit whose run completed.

### 1.3 Pre-tag checklist

1. `git status --porcelain` clean — locally **and** on the VM.
2. `bash scripts/verify_pre_deploy.sh` on the VM **and** locally: every line
   `PASS` or an explicit `SKIP`; no `FAIL`. The script is fail-tolerant (a FAIL
   *count* does not set a non-zero exit) — read the output, not `$?`.
   **Put the Go bin directory that holds `staticcheck` on `PATH`** (usually
   `$HOME/go/bin`). Two spurious-FAIL traps, both seen on 2026-09-19: without it,
   `B95` reports "staticcheck not found"; and running the catalog as the
   unprivileged operator user against a root-owned checkout fails ~90 checks with
   `permission denied` (starting with `go test ./...` on `data/oidc-keys-test`).
   The invocation that works on the reference VM is root + the operator's Go bin:
   `sudo env PATH="$HOME/go/bin:$PATH" GOFLAGS=-p=2 bash scripts/verify_pre_deploy.sh`.
   A live contract that cannot reach its dependency (network, docker, DB) must
   print `SKIP`; if one prints `FAIL` for an unreachable dependency, that is a
   contract bug — fix the check, do not re-run until it passes.
   **Beware the `cmd | grep -q` false FAIL.** A check that sets `pipefail` and
   ends a pipeline with `grep -q` can FAIL spuriously: `grep -q` exits at the
   first match, the still-writing producer (`go test`, `staticcheck`, …) dies
   with SIGPIPE (141), and `pipefail` reports the *pipeline* as failed even though
   the command succeeded. On 2026-09-19 this produced exactly one rotating FAIL
   per gate run (a different check each time, each passing standalone 20/20).
   Capture first, then match: `OUT="$(cmd 2>&1)"; if grep -q '^ok' <<< "$OUT"`.
   Fixed so far: `B235` (E.2/E.3), `B237.20` (D.2), `B211`–`B214` (the binary link
   now retries once via `scripts/lib/go_build.sh`); the remaining sites are
   tracked as **TD-19** in `docs/ROADMAP.md`. For any single FAIL, re-run that
   check standalone before believing it.
3. `staticcheck ./...` → 0 issues; `go build ./...` and `go vet ./...` clean; the
   focused test set (`./internal/update/... ./internal/db/
   ./internal/feature/admin/... ./internal/i18n/...`) green.
4. The tagged commit contains the fixed `path: dist/checksums` line (§1.5).
5. **Clean-host acceptance passed** (§1.4) — not optional; it is the step that
   catches installer bugs.
6. Release notes text decided — the `## vX.Y.Z` section exists in
   `RELEASE-NOTES.md` (one canonical file; no per-version files).

### 1.4 Clean-host acceptance (required before tagging)

Prove that a *fresh* host can install the release and then self-update to it, on
a throwaway container/host so the production VM is untouched.

```bash
# 1. Throwaway systemd host (systemd must be PID 1 for the native path).
docker run -d --name acc --privileged --cgroupns=host \
  -v /sys/fs/cgroup:/sys/fs/cgroup:rw jrei/systemd-debian:12
docker exec -it acc bash

# 2. Install a native SQLite install (inside the container).
SKYGATE_SKIP_VERIFY=1 bash deploy/install-debian.sh --db-type=sqlite --install-kind=systemd
```

Expected after step 2: `skygate.service` carries `Environment=SKYGATE_INSTALL_KIND`,
`SKYGATE_UPDATE_STATE_PATH`, `SKYGATE_UPDATE_DIR`; `skygate-update.path` +
`skygate-update.service` exist and the **path unit is `active`**;
`/etc/skygate/skygate.env` has `SKYGATE_DB=sqlite:/…`; and
`/usr/local/lib/skygate/skygate-apply-update.sh` +
`/etc/skygate/update-helper.conf` exist.

```bash
# 3. Stage an update request AS THE SERVICE USER — data only; the root-owned
#    path unit notices it and fires the root-owned applier.
sudo -u skygate sh -c 'printf "TARGET=vX.Y.Z\n" > /var/lib/skygate/update/request.props'

# 4. The applier then runs unattended:
#    SHA256 OK (verified against the release SHA256SUMS asset)
#    migrate-only: opening sqlite (DSN=sqlite:/…) → OK
#    restarting (systemd) → healthz reports build 'vX.Y.Z+<sha>' within seconds
#    verdict: done
sudo journalctl -u skygate-update.service -n 50
cat /var/lib/skygate/result.status
systemctl is-active skygate-update.path
curl -fsS http://127.0.0.1:8080/healthz     # build string must match the target tag
```

It is the acceptance test and not a smoke test because the applier polls
`/healthz` for the **build string of the target tag**, not merely HTTP 200 (a
stale process answering 200 used to look like success), and the pre-swap
`migrate-only` refuses a release whose chain cannot handle the current database
*before* the swap — so a bad target cannot brick the host.

> **Caveat:** the first run of this procedure against v1.5.8 failed for a reason
> unrelated to the applier — on minimal Debian `xxd` is absent, `write_env_file`
> died and `set -euo pipefail` aborted the install between "installed:
> /usr/local/bin/skygate" and the env file/unit/helper. The installer now falls
> back `openssl` → `od` → `xxd`; on an older installer, install `xxd`/`openssl`
> and re-run. Also expected: **v1.5.8 and earlier cannot open a `sqlite:` DSN at
> all** (fixed in v1.5.9), so an older binary refuses its own database.

#### Acceptance record — 2026-09-19 (v1.5.9, commit `419e9a99` + docs)

Run on the VM in a throwaway `jrei/systemd-debian:12` container (`--privileged
--cgroupns=host`, systemd as PID 1) with the VM checkout mounted read-only at
`/src`, so the *installer under test is the commit above*:

| Step | Result |
|---|---|
| `SKYGATE_SKIP_VERIFY=1 bash /src/deploy/install-debian.sh --db-type=sqlite --install-kind=systemd` | exit 0; `skygate.service` + `skygate-update.path` **active** + `skygate-update.service` + `/usr/local/lib/skygate/skygate-apply-update.sh` + `/etc/skygate/update-helper.conf` + `SKYGATE_DB=sqlite:/var/lib/skygate/skygate.db`; the JWT secret was generated through the **`od` fallback** (that image ships neither `xxd` nor `openssl`) |
| installed version | `skygate v1.5.8 (commit 32e781a)` — the published `latest`; it mis-detects `sqlite:` as PostgreSQL (`DB backend: postgres (DSN=sqlite:/…`) and never serves `/healthz` |
| request staged as the service user (`runuser -u skygate`) | the path unit fired; applier logged `backed up … → skygate.prev`, `SHA256 OK (…, verified against SHA256SUMS asset)`, `migrate-only: opening sqlite … migrations applied OK`, `installed the new binary over /usr/local/bin/skygate`, `healthz reports build 'v1.5.9+acc0001' after 2s`, **`verdict: done`** |
| verdict files | `result.status=done`, `result.build=v1.5.9+acc0001`, no `result.error` |
| `/healthz` | `{"build":"v1.5.9+acc0001","status":"ok",…}` — the applier matches the **target tag**, not merely HTTP 200 |
| leftovers | SQLite DB created by the migration step (516 KB); `skygate.prev` = the previous (v1.5.8) binary |

The update therefore **rescued a host whose installed binary could not run at
all**. Because the not-yet-published v1.5.9 cannot come from GitHub, the run used
a **loopback mirror** — `SKYGATE_UPDATE_BASE_URL="http://127.0.0.1:8099"` in the
root-owned helper config, served by a throwaway static file server (a ~25-line Go
binary built from a temp module: the container has no `python3`, `busybox`, `nc`
or `python`) — with `<mirror>/v1.5.9/skygate-v1.5.9-linux-amd64.tar.gz` +
`SHA256SUMS` built exactly the way `release.yml` builds them. That also exercises
B261.5 end-to-end. **After the tag is published, re-run the same flow with the
mirror line removed** to cover the GitHub path (and the `SHA256SUMS` asset, §1.5).

### 1.5 Post-tag checklist — verify the published assets

The release is not done when CI is green; it is done when the *assets* are
right. Three verifications, all cheap:

```bash
# 1. SHA256SUMS MUST be a FILE and MUST list every archive.
curl -sS https://api.github.com/repos/BarsSky/skygate/releases/tags/vX.Y.Z \
  | grep -A3 '"name": "SHA256SUMS"'
#    → "content_type": "text/plain" / a real byte size — NOT a directory entry.
#    Then eyeball the release page: SHA256SUMS is a downloadable file.

# 2. The ghcr tag MUST be lowercase.
docker pull ghcr.io/barssky/skygate:vX.Y.Z      # succeeds
docker pull ghcr.io/BarsSky/skygate:vX.Y.Z      # fails: repository name must be lowercase

# 3. Tarball sanity.
curl -fsSLO https://github.com/BarsSky/skygate/releases/download/vX.Y.Z/skygate-vX.Y.Z-linux-amd64.tar.gz
tar -xzf skygate-vX.Y.Z-linux-amd64.tar.gz && ./skygate version
#    → vX.Y.Z+<short-sha>
```

**Why `SHA256SUMS` gets its own check.** From v1.5.6 through v1.5.8 the release
attached **no `SHA256SUMS` at all**: `release.yml` downloaded the artifact into
`dist/SHA256SUMS` while the artifact's only file is *also* named `SHA256SUMS`,
so the file landed at `dist/SHA256SUMS/SHA256SUMS`; the flatten step then ran
`mv <file> .` with a directory of that name already present, which moves the
file *inside* the directory. The release therefore published a directory and
`gh` uploaded nothing. The fix was one line — download into `dist/checksums` —
and the native self-updater had been masking it by falling back to GitHub's
per-asset `digest: sha256:<hex>` from the Releases API.

Consequences: an **internal mirror cannot use the GitHub-digest fallback** (it
must publish its own `SHA256SUMS`; `SKYGATE_UPDATE_BASE_URL` demands it), and
releases ≤ v1.5.8 are installable only with `SKYGATE_SKIP_VERIFY=1` or the
digest fallback. Deploy the release, then run the **live-state checks that SKIP anywhere else**
(they need the real VM):

```bash
for c in check_b191 check_b_admin_user_sync check_b_duplicate_users \
         check_b_reconcile_audit_writes check_b_node_owner_map_orphans \
         check_b_tag_owners check_b_node_attribution; do bash scripts/$c.sh; done
```

### 1.6 Rollback / abort

Operator rule: **if CI failed, delete the tag and re-tag** — do not try to
repair a release in place.

```bash
# Before anyone consumed it:
gh release delete vX.Y.Z --yes
git push origin :refs/tags/vX.Y.Z
git tag -d vX.Y.Z
# fix, then re-tag

# After an operator already updated a host:
#   a) the applier always keeps the previous binary:
sudo install -m 0755 /var/lib/skygate/update/skygate.prev /usr/local/bin/skygate
sudo systemctl restart skygate
#   b) or use the explicit rollback offered on /admin/update to the previous tag
```

The native applier already self-rolls-back when the post-swap healthz build
string does not match: it restores `skygate.prev`, restarts, verifies, and
reports `verdict: rolled_back`. A `verdict: failed` means the applier stopped
*before* the swap (unknown tag, or the target cannot migrate your DB) — the
running service was never touched in that case.

---

## 2. Deployment pipeline

### 2.1 Baseline (what "normal" looks like)

The baseline was captured read-only on the reference VM (probe script
`scripts/__phase0_collect.sh`, output saved under `/tmp/` on the VM). Numbers
drift, but the *shape* is the useful part:

| Metric | Baseline value |
|---|---|
| `verify_pre_deploy.sh` | 356 PASS / 37 FAIL lines, **exit code 0** (fail-tolerant — only the FAIL count is meaningful) |
| `/healthz` | 200, `status=ok` |
| `/readyz` | healthy, all 4 deps ok (`db`, `headscale`, `headplane`, `tailscale`) |
| Reconcile cron | stable: `ok=N linked=0 relinked=0 orphans=0 errors=0` per hourly cycle |
| DB | PostgreSQL reachable from inside the container |
| Disk | watch it — the same session recovered 87% → 63% by trimming journald and docker logs |

Read any future run the way the baseline's 37 FAILs were bucketed:
**(A) operator-side** (missing toolchain, `staticcheck` absent, a script that does
not exist yet, a peer offline); **(B) check-script hygiene** (stale regexes,
`s.DB` vs `s.dbc()`, addresses that moved); **(C) cosmetic** (test ordering,
TD-series minors). Only bucket A blocks a deploy.

### 2.2 The scripts

The auto-deploy/auto-config surface, by name:

| Script | Role |
|---|---|
| `deploy/install*.sh` (`install`, `-debian`, `-rh`, `-alpine`, `-bare`, `-common`) | OS install: Docker, binary, unit, update helper — see [INSTALL.md](INSTALL.md) |
| `deploy/deploy.sh`, `deploy/validate.sh`, `deploy/backup.sh` | Orchestrator; stack health check; S3/local backup of the skygate PG + headscale/headplane state |
| `deploy/oidc-sync.sh`, `deploy/scripts/setup-skygate-public.sh` | OIDC config auto-sync (5 modes + download); public-hostname OIDC wiring |
| `deploy/scripts/create-standby-preauth.sh`, `bootstrap_standby.sh`, `install-tailscale.sh` | Mint a standby preauth key (`--user` name→id, tagOwners, audit row); bootstrap a standby; install Tailscale |
| `scripts/rotate_ts_authkey.sh` | Weekly preauth-key rotation, 14-day expiry buffer |
| `scripts/ha-state/state.sh`, `ha-phase0.sh`, `ha-phase7.sh`, `ha-phase9.sh`, `ha-status.sh` | HA state machine (lock/fsync/stale-lock); mesh prerequisite; wrappers over bootstrap/drill; status + crash marker |
| `scripts/init-headplane.sh` | Auto-apply the headplane API key |
| `scripts/tailnet_probe.sh`, `scripts/fix_tailnet_split.sh` | Tailscale reachability diagnostic; split-brain re-auth helper (§6) |
| `scripts/recover_db_corruption.sh` | DB recovery runbook script |
| `scripts/rebuild_deploy.sh`, `scripts/deploy_to_vm.sh` | Rebuild + recreate the VM container; push → pull → `--force-recreate` → healthz wait |
| `scripts/verify_pre_deploy.sh` | The pre-deploy gate: inline B-checks + delegation to `scripts/check_b*.sh` |

### 2.3 Ordering guarantees

1. **`docker compose` runs from the project directory.** Invoked as
   `docker compose -f /path/to/compose.yml …` from elsewhere, `.env` is *not*
   auto-loaded for interpolation and every `${VAR:-default}` silently resolves to
   its default. Canonical: `cd <project-dir> && sudo docker compose …` (or pass
   `--env-file`). `scripts/rebuild_deploy.sh` and `scripts/deploy_to_vm.sh` bake
   this in.
2. **`--force-recreate` after any compose-level change** (`extra_hosts`,
   volumes, network) — a plain `restart` re-loads the binary but keeps the old
   container network config.
3. **Source-only changes need no image build.** The entrypoint runs `go build` on
   start against the bind-mounted repo, so `git pull` + `docker compose restart
   skygate` is the update; `build` is only for Dockerfile changes.
4. **Host first, then dependents:** headscale reachable before skygate starts;
   skygate healthy before the ACL/policy reapply is meaningful.
5. **Standby provisioning order:** preauth key → `tailscale up` with the correct
   `--user` and tags → `node_owner_map` row → policy reapply. A node that joins
   the synthetic headscale user is invisible to the grants generator, and
   headscale 0.29.x has no `nodes move`, so the only fix is re-provisioning.
6. **Migrate the DB before swapping the binary.** The applier runs `migrate-only`
   *before* the swap for exactly this reason.
7. **`--netfilter-mode=nodir`, never `off`** — `off` leaves a stale `ts-input`
   chain that can black-hole traffic later.

### 2.4 How the pipeline is tested

The test plan ran in eight phases against a non-critical standby host, then a
fresh VM. The phases are the regression suite; run the ones covering what you
changed. **Phase 6 really kills the active container — maintenance window only.**

| Phase | Proves | Gate |
|---|---|---|
| 0 — audit | Baseline documented; policy dumped for rollback | `headscale policy get -o json > /tmp/policy.baseline.json` |
| 1 — Tailscale grants | Peers reach each other both ways | `tailscale ping <peer>` → `pong`, not `no matching peer` |
| 2 — re-deploy standby | `bootstrap_standby.sh` step 0 authenticates Tailscale | node lands in the `infra` user, not the synthetic sentinel |
| 3 — preauth script | name / numeric id / default / nonexistent user / `--dry-run` / `--help` | 9 cases; `ha.preauth.create` audit row; test key unconsumed |
| 4 — policy reapply | Per-device grants include the new node | `headscale policy check -f /tmp/policy.json` clean; E2E SSH both ways |
| 5 — fresh deploy | installers work from zero on a clean VM | `/healthz` 200, node in mesh, grants present |
| 6 — DR drill | Failover completes within the timeout | maintenance window only |
| 7 — regression | The B-check catalogue covers the known narrow places | new checks for grants, `node_owner_map`↔`portal_users`, tagOwners, private-IP leaks |

`scripts/verify_migration.sh` chains the three runtime gates
(`verify_post_deploy.sh --quick` → `/admin/system_tests/run` → the
migration-specific assertions) and is the post-deploy acceptance runner.

### 2.5 When the pipeline fails

| Symptom | First thing to check |
|---|---|
| Deploy "succeeds" but env-dependent behaviour is wrong | Was `docker compose` run from the project directory? Compare the rendered `docker compose config` with `.env`. |
| A container reaches a host that resolves to `127.0.0.1` inside it | The host resolver (systemd-resolved / `/etc/hosts`) is inherited. Pin the target with `extra_hosts` + an env var, then `up -d --force-recreate`. |
| `bootstrap_standby.sh` dies at step 0 | Tailscale/Docker are *not* installed by the base installers, and Tailscale is not in the default Debian repos — add its apt repo first. |
| HA state phases fail immediately | `jq` missing, or `/var/lib/skygate/ha-state/` missing with the wrong owner. |
| Phase marked `failed` after an interrupted `tailscale up` | Crash detection keeps `running`; re-run with `--reset`. |
| New node invisible to grants | It joined the synthetic headscale user (preauth key without `--user`) — re-provision. |
| `.env` points at a decommissioned DB | Confirm `SKYGATE_DB` and the legacy `SKYGATE_DB_DSN` agree (see §12). |

Recurring narrow places: `--user` on the preauth key expects a **numeric** id;
tags must be attached **at auth time** (`--advertise-tags=`); the per-device
grant generator skips users with fewer than two devices; the exit-node catch-all
excludes `tag:dev-infra-skygate-*` hosts; and the standby bootstrap uses
`$(hostname)` in its S3 path, so a hostname mismatch silently pulls nothing.

---

## 3. PostgreSQL cutover / migration between hosts

### 3.1 When to use which procedure

* **Same-database restore onto a new host** (the common "move to a new VM"):
  [backup-restore-and-migration.md](backup-restore-and-migration.md) §3 — backup,
  `scp`, replay `skygate-pg.sql`, update the 5 connection settings, verify.
* **Changing which database skygate *uses*** (SQLite ↔ PostgreSQL): the
  bidirectional converter, `skygate db-migrate --from=… --to=…`.
* **Switching a live deployment from SQLite to PostgreSQL in one window:** the
  procedure below.
* **HA / multi-instance topology:** [ha.md](ha.md) and
  [disaster-recovery.md](disaster-recovery.md).

> **Caveat:** the source runbook for this section is titled "v0.33.0 —
> PostgreSQL cutover" and is dated 2026-08-03. That cutover **has since
> happened** (the repository is PostgreSQL-native as of v1.3.0), so read it as
> the *pattern* for a database cutover, not as an outstanding task. Its
> checkpoint lists also predate the current release naming (`make verify-pre`
> counts, a specific `v0.32.19` floor) — take the *steps*, ignore the specific
> numbers.

The plan splits cleanly so the risky part is small: **the query rewrite can land
days earlier**, because `?` and `$N` placeholders are equivalent for
argument-free statements on SQLite, so the rewritten code can be deployed and
observed on SQLite first. Only the data move needs a window — budget ~20 minutes
of work plus ~15 minutes of downtime.

### 3.2 Pre-flight

```bash
# 1. Gate the current build.
bash scripts/verify_pre_deploy.sh      # all PASS or explicit SKIP
bash scripts/verify_post_deploy.sh     # all PASS

# 2. The target cluster is reachable and empty.
psql "$TARGET_DSN" -c '\l'
psql "$TARGET_DSN" -c '\dt'            # empty is expected

# 3. The dialect test suite passes against the target.
SKYGATE_TEST_PG_DSN="$TARGET_DSN" go test -count=1 -timeout 60s ./internal/db/...

# 4. A backup from within the last hour exists.
ls -lh /home/<OPERATOR_USER>/backups/ | tail -3
```

If any fail, stop. Separately (days earlier is fine), do the rewrite:

```bash
git checkout -b feat/pg-placeholder-rewrite
python scripts/rewrite_placeholders.py internal/db/*.go

# Manual review — the script leaves TODOs it cannot resolve:
grep -rn 'INSERT OR ' internal/db/ | grep -v _test.go    # expect zero
grep -rn 'strftime'   internal/db/ | grep -v _test.go    # expect zero

go build ./... && go test -count=1 -short ./...
```

`INSERT OR REPLACE` / `INSERT OR IGNORE` must become `ON CONFLICT … DO UPDATE`
/ `ON CONFLICT DO NOTHING`, and the conflict target is **table-specific**
(e.g. `node_owner_map` keys on `(node_id)`; `device_rules` keys on
`(user_id, device_id, exit_node_id, target_value)`) — this is the part you must
review by hand. `RETURNING id` is not needed: `LastInsertId()` maps to PG's
`lastval()`.

### 3.3 The cutover window

```bash
# 1. Quiesce: set SKYGATE_READ_ONLY=true in the compose env / .env, then
cd <project-dir>            # CWD matters (see §2.3)
docker compose up -d --force-recreate skygate
until curl -fsS http://localhost:8080/healthz; do sleep 3; done
# /admin/exit-rules now shows the read-only banner

# 2. Snapshot the source database.
sqlite3 <data-dir>/skygate.db "PRAGMA wal_checkpoint(FULL);"
cp -a <data-dir>/skygate.db /home/<OPERATOR_USER>/backups/skygate-pre-pg-$(date +%Y%m%d-%H%M%S).db

# 3. Dump to SQL (through a throwaway sqlite container if sqlite3 is not on the host).
docker run --rm -v <data-dir>:/from:ro -v $HOME/skygate-pg-dump:/to alpine:3.20 sh -c \
  "apk add --no-cache sqlite && sqlite3 /from/skygate.db .dump > /to/dump.sql && wc -l /to/dump.sql"

# 4. Translate + load into the fresh PG database. SQLite-isms to translate:
#    AUTOINCREMENT → SERIAL/BIGSERIAL; "quoted" idents → standard SQL;
#    't'/'f' → true/false; x'..' hex blobs → '\x..';
#    strftime('%s','now') → EXTRACT(EPOCH FROM now())::bigint
python3 $HOME/skygate-pg-dump/translate_dump.py --in dump.sql --out dump.pg.sql
PGPASSWORD=… psql "$TARGET_DSN" -f dump.pg.sql
psql "$TARGET_DSN" -c '\dt'          # ~20 tables expected

# 5. Flip the DSN (SKYGATE_DB=postgres://…; SKYGATE_DB_DSN is the legacy alias).
docker compose up -d --force-recreate skygate
until curl -fsS http://localhost:8080/healthz; do sleep 3; done
# /admin/exit-rules now shows the PostgreSQL-backend banner

# 6. Leave read-only mode (SKYGATE_READ_ONLY=false) and recreate again.
docker compose up -d --force-recreate skygate
until curl -fsS http://localhost:8080/healthz; do sleep 3; done
```

The preferred alternative for step 4 is the built-in converter, which is
dialect-aware and copies row-by-row in FK-dependency order (`BIGSERIAL ↔ INTEGER
PK AUTOINCREMENT`, `BOOLEAN ↔ INTEGER`, `JSONB ↔ TEXT`, `TIMESTAMPTZ ↔ INTEGER`,
`bytea ↔ BLOB`):

```bash
skygate db-migrate --from=sqlite:/var/lib/skygate/skygate.db \
  --to=postgres://skygate:<password>@<DB_HOST>:5432/skygate
```

### 3.4 Post-cutover verification and rollback

```bash
# Row-count parity for the tables that matter.
psql "$TARGET_DSN" -c 'SELECT COUNT(*) FROM portal_users;'
psql "$TARGET_DSN" -c "SELECT id, username, is_admin FROM portal_users ORDER BY id;"
psql "$TARGET_DSN" -c 'SELECT COUNT(*) FROM audit_log;'
psql "$TARGET_DSN" -c 'SELECT COUNT(*) FROM device_rules;'
sqlite3 /home/<OPERATOR_USER>/backups/skygate-pre-pg-*.db 'SELECT COUNT(*) FROM device_rules;'
# expect equal, modulo rows created between the backup and the cutover

bash scripts/verify_post_deploy.sh      # all PASS
bash scripts/verify_migration.sh
curl -fsS http://localhost:8080/readyz  # db:ok, headscale:ok, headplane:ok, tailscale:ok
```

Clean up with `rm -rf $HOME/skygate-pg-dump`; keep the pre-cutover SQLite file
for 7 days before moving it to long-term storage.

**Rollback** (reversible until skygate runs on the PG DSN for the *second* time —
after that `audit_log` and `acl_snapshots` have diverged and rollback means
restoring SQLite from the snapshot):

```bash
# 1. Flip back: SKYGATE_DB=sqlite:<data-dir>/skygate.db (drop the postgres:// line)
docker compose up -d --force-recreate skygate
until curl -fsS http://localhost:8080/healthz; do sleep 3; done

# 2. If the live SQLite file was touched, restore the snapshot.
docker run --rm -v skygate-data:/data -v /home/<OPERATOR_USER>/backups:/from:ro alpine:3.20 sh -c \
  "apk add --no-cache sqlite && cp -f /from/skygate-pre-pg-*.db /data/skygate.db && \
   sqlite3 /data/skygate.db 'PRAGMA integrity_check;'"

# 3. Re-apply the ACL — the PG side may have written acl_snapshots rows that do
#    not exist on the SQLite side.
curl -fsS -X POST -b /tmp/admin.cookie https://skygate.example.com/admin/exit-rules/reapply
```

---

## 4. New-host bootstrap

A generalised HA-mirror bootstrap: provision an additional skygate host that can
be promoted later. It is a **technical HA host**, not a traffic-routing exit
node.

### 4.1 Prerequisites

| What | Where from | Example |
|---|---|---|
| VM | your host panel | 4 vCPU, 8 GB RAM, 50 GB SSD, Ubuntu 22.04 LTS |
| Public IP / hostname | host panel; hostname chosen up front to match the tag namespace | `203.0.113.10` / `<HA_HOST>` |
| headscale URL | the primary's `HEADSCALE_URL` | `https://head.example.com` |
| Preauth key | `headscale preauthkeys create --user <user> --reusable --expiration 24h` | 24 h validity |
| Deploy bucket | same as the primary | `s3://skygate-backups/deploy/` |
| Operator user | the headscale user that owns the cluster | `<OPERATOR_USER>` |

Back up the skygate database and headscale state **before** a new node joins, so
a broken bootstrap is reversible.

### 4.2 DNS, ports, network

1. Create the VM; hostname `<HA_HOST>` (not `<HA_HOST>-1` — that suffix is for a
   second mirror, and the tag namespace assumes it). Bind the public IP and
   verify from inside with `curl ifconfig.me`.
2. `apt update && apt upgrade -y`, then `apt install -y curl wget git nano htop ufw`.
3. Open inbound **22 only** — 80/443 come later with skygate/TLS.
4. Attach the host to the primary's private network if the panel offers one; the
   tailnet is the primary path and the private net is a diagnostics fallback.
5. Point a DNS name at the public IP. The public HTTPS name for the portal is a
   separate record and belongs to [https.md](https.md).

### 4.3 Join the tailnet

```bash
curl -fsSL https://tailscale.com/install.sh | sh
sudo tailscale up \
  --login-server=https://<headscale-fqdn> \
  --authkey=<preauth-key> \
  --hostname=<HA_HOST> \
  --accept-routes=false \
  --advertise-exit-node=false
ip -4 addr show tailscale0 | grep inet     # note the <TAILSCALE_IP_HA>
```

`--accept-routes=false` and `--advertise-exit-node=false` are **critical**: the
mirror must never become a source of outbound user traffic. Tailscale is not in
the default Debian repositories — add the vendor apt repo first if the CLI is
absent (see [networking.md](networking.md), `deploy/scripts/install-tailscale.sh`).

### 4.4 Tag + ownership row

On the primary:

```bash
headscale nodes list --user <operator-username> | grep <HA_HOST>
headscale nodes tag -i <numeric-id> --tags tag:dev-infra-<HA_HOST>
headscale nodes list --user <operator-username> | grep <HA_HOST>   # verify
```

Then insert the ownership row (this is what the grants generator reads):

```sql
-- PG:
INSERT INTO node_owner_map (node_id, owner_username, tag, created_at)
VALUES ('<numeric-id>', 'infra', 'tag:dev-infra-<HA_HOST>', EXTRACT(EPOCH FROM now())::bigint)
ON CONFLICT (node_id) DO UPDATE
  SET tag = EXCLUDED.tag,
      owner_username = EXCLUDED.owner_username,
      updated_at = EXTRACT(EPOCH FROM now())::bigint;
```

`owner_username = 'infra'` is the convention for technical infrastructure;
mixing infra into a personal user bucket makes the per-device ACL check flag the
policy as malformed. Use the `tag:dev-infra-*` namespace, **not** `tag:public` —
`tag:public` is reserved for user-facing services and would leak the node into
the public ACL.

### 4.5 Secrets and config

Either option requires the **binary version to match the primary** — mismatched
versions in an HA pair are unsupported (the elector's JSON contract drifts).

```bash
# Option A — push from the operator's workstation:
go build -o ./bin/skygate ./cmd/skygate
git describe --tags --dirty > ./bin/meta.json
skygate deploy-push --target=<HA_HOST> --binary=./bin/skygate --meta=./bin/meta.json
ssh <user>@<HA_HOST> 'skygate deploy-pull && sudo systemctl restart skygate'

# Option B — install from the release on the host itself:
sudo apt install ./skygate_<version>_<arch>.deb     # or the tarball, see INSTALL.md
sudo cp <from-primary> /etc/skygate/skygate.env     # then edit it:
sudo sed -i 's/^SKYGATE_SELF_HOSTNAME=.*/SKYGATE_SELF_HOSTNAME=<HA_HOST>/' /etc/skygate/skygate.env
sudo chmod 600 /etc/skygate/skygate.env
```

Fill in `HEADSCALE_URL`, `HEADSCALE_API_KEY`, the DB DSN, the JWT secret and the
admin credentials; do not copy the primary's host-specific values blindly
(`SKYGATE_SELF_HOSTNAME` and certificate paths are per-host). Full reference:
[deploy.md §1](deploy.md#1-environment).

### 4.6 Start the service, register as an HA member, first admin

```bash
sudo systemctl enable --now skygate
systemctl status skygate            # active (running)
sudo journalctl -u skygate -n 50
```

1. `/admin/ha` → "Cluster topology" → **Add HA member**: hostname `<HA_HOST>`,
   priority `2` (1 is the primary), tailnet IP `<TAILSCALE_IP_HA>`, public IP
   `203.0.113.10`, role **`standby`**. Save — the elector picks it up on its next
   5-second tick. Always start as standby; promotion happens only when the
   primary dies.
2. Open `http://<HA_HOST>:8080/login` and sign in with `SKYGATE_ADMIN_USER` /
   `SKYGATE_ADMIN_PASS`; **change the password immediately** (defaults are
   `admin` / `admin`).
3. Verify `/admin/update` shows the expected install kind and that the
   privileged update helper is installed.
4. If the DB was restored rather than fresh, confirm the portal admin row
   matches `SKYGATE_ADMIN_USER` and run
   `bash scripts/check_b_admin_user_sync.sh`; `/admin/users` offers the
   remediation ("Adopt as Admin", per-row "Rename").

### 4.7 Verification checklist

| # | Check | Command | Expected |
|---|---|---|---|
| 1 | Tailscale up | `ssh <HA_HOST> tailscale status` | the host and its `100.64.x.y` IP |
| 2 | Liveness | `curl -s http://<TAILSCALE_IP_HA>:8080/healthz` | `ok` (200) + build string |
| 3 | Readiness | `curl -s http://<TAILSCALE_IP_HA>:8080/readyz` | healthy, 4 deps ok |
| 4 | Primary sees it | `/admin/ha` in the browser | 2 members in the chain |
| 5 | Both agree on active | `/admin/ha` on both hosts | both report the primary |
| 6 | DR drill (dry-run) | `/admin/deploy` → "Test-failover" | predicts `<HA_HOST>` as next active |
| 7 | ACL tag owners | `bash scripts/check_b118.sh` | PASS — `tag:dev-infra-*` owned by `infra` |
| 8 | Ownership row | `psql … -c "… WHERE tag='tag:dev-infra-<HA_HOST>';"` | one row, `owner_username='infra'` |

A **real** DR drill (kill the active, watch the standby take over) is a separate
exercise with a maintenance window. Never run it without operator approval.

### 4.8 Do not

Add the host to `exit_servers`, apply `tag:exit-node`, or run
`tailscale up --advertise-exit-node=true` — it is a technical HA host, not a
traffic-routing node; the exit-rule machinery would treat it as user-routable,
users would see it as a selectable exit, and the tag-owner check would fail.
Do not promote it to priority 1 (reserved for the preferred primary), do not run
`skygate ha-promote` on it (a one-shot override for confirmed-primary-down, not a
role change), do not add `tag:public` (leaks the node to every tailnet user), and
do not skip the tag-owner regression check (the new tag is only valid once the
policy's `tagOwners` includes it).

### 4.9 Rollback

Do these **in order** — removing the HA chain member before the headscale node
avoids the elector heartbeating a node that no longer exists (harmless, but it
fills the audit log every 5 s).

```sql
DELETE FROM node_owner_map WHERE node_id = '<numeric-id>';   -- 1
```

```bash
# 2. Remove it from the HA chain via /admin/ha → "Remove member".
# 3. Remove the node from headscale.
headscale nodes delete --force -i <numeric-id>
# 4. On the host: leave the tailnet and uninstall.
sudo tailscale logout
sudo systemctl disable --now skygate
sudo bash deploy/scripts/cleanup-skygate.sh --yes
# 5. Destroy the VM (or keep it powered off while you debug).
```

---

## 5. Infrastructure retag / rename procedure

### 5.1 Why the order matters

Renaming/retagging touches three systems that must end up consistent:
**headscale** (the tag attached to the node), **skygate's `node_owner_map`**
(the ownership/tag row), and the **ACL policy** (the `tagOwners` entry plus the
generated grants). A tag that headscale does not know cannot be attached, and a
tag attached without an ownership row is invisible to the grants generator.

Two hard-won rules follow:

1. **A new tag must exist in the policy's `tagOwners` before you can attach
   it.** Attach first and headscale rejects the tag outright (`requested tags
   are invalid or not permitted`), because the tag was never whitelisted.
2. **Add the new tag before removing the old one.** The pre-fix backfill did
   `UntagNode(old)` then `AddTag(new)`; when the new tag was rejected the old
   tag was already gone, leaving the node with *no* dev-tag and no recovery
   except manual intervention. The current order is `AddTag(new)` first, with
   `UntagNode(old)` and the database row update inside the success branch — so a
   rejected tag cannot desynchronise the row from headscale and the node keeps
   working under its old tag.

### 5.2 Retag a node into the infra bucket

Use this when a node owned by a personal user must become infrastructure (e.g. a
future HA partner). Ownership can change two ways: explicitly via
`headscale nodes tag`, or derived by `BackfillInfra` on skygate's next restart.

```bash
# 1. Mint / reuse a preauth key (24h, reusable) on the primary.
docker exec headscale headscale preauthkeys create --user <user-id> \
  --reusable --expiration 24h

# 2. On the node, generate the exact `tailscale up` command with all
#    non-default flags preserved:
curl -sL https://raw.githubusercontent.com/BarsSky/skygate/main/scripts/fix_tailnet_split.sh -o /tmp/fix.sh
PREAUTH_KEY=<key> bash /tmp/fix.sh

# 3. Edit the printed command: replace the old dev tag with the new infra tag,
#    KEEPING every other tag (tag:exit-node, tag:private, …), then run it.
sudo bash -c "tailscale up --login-server=https://head.<your-domain> \
  --hostname=<node> --advertise-tags=tag:dev-infra-<node>,tag:exit-node,tag:private \
  --auth-key=<key>"
tailscale status        # 4. verify on the node
```

Three notes that separate success from a silent failure:

* **Add `tag:exit-node` when the node must be usable as an egress target.**
  Without it the `* → tag:exit-node` catch-all does not include the node, and
  `isInfraNode`'s exit-node rule does not match either — so `BackfillInfra` never
  moves it and it stays stranded in its old portal-user bucket, invisible to the
  primary.
* **A hostname that does not match the `skygate-host-*` prefix is a trap** — the
  hostname-prefix rule of `isInfraNode` will not match it; the exit-node tag is
  what makes it eligible. Only infra nodes get re-tagged; personal and offline
  devices keep their `tag:dev-<user>-*` tags.
* **The preauth key is a secret**, visible to anyone who can read the node's
  shell history or the file you pasted it into.

### 5.3 Rename a device (defensive order)

Renaming a node with `headscale nodes rename` moves its dev-tag with the
hostname. The backfill computes the new tag, adds it, and only then untags the
old one; on a failed add it logs "keeping existing tags as fallback" and leaves
everything as it was. Practical consequences:

* A tag headscale has never seen must exist in `tagOwners` first — apply the
  policy before you rename, or the rename is rejected.
* Tags are **lowercase-only** (headscale rejects uppercase), which is why the
  generated dev-tag is lowercased at every site that writes it.
* Nodes already on the synthetic "tagged-devices" user with no live dev-tag are
  skipped by every backfill strategy. Apply the tag by hand:
  `headscale nodes tag --force -i <id> --tags tag:dev-<user>-<name>`.

### 5.4 Verification and failure modes

Reachability from the primary's container (`docker exec skygate bash
/app/scripts/tailnet_probe.sh` — every online peer reachable, **no**
`DIAGNOSIS: TAILNET SPLIT LIKELY`), then `/admin/exit-rules` → "Reapply policy"
(generates the infra tag owners and per-device grants for the first time), then
`/admin/system_tests` → `tailnet.split_suspected` → PASS. After a successful
retag: the headscale tag is `tag:dev-infra-<node>`, the `node_owner_map` row
becomes `owner_username='infra'` (via `BackfillInfra` on the next skygate
restart), and the policy gains a mesh between the infra nodes plus one catch-all
per infra exit node.

| Symptom | Cause | Fix |
|---|---|---|
| `tailscale up` → "tag not found" / "tags are invalid or not permitted" | The new tag is not in the policy's `tagOwners` yet | Restart skygate to run `BackfillInfra`, re-apply the policy, then re-run `tailscale up` |
| Map still shows the old peer set after a successful retag | Stale `tailscaled` cache | `sudo systemctl restart tailscaled`, wait 10 s, `tailscale status` |
| Some peers still invisible | `BackfillInfra` has not run for that node | `cd <project-dir> && docker compose restart skygate` |
| Node ends up with **no** dev-tag | Old order (`untag` then `add`) plus a rejected tag | Add the tag by hand with `headscale nodes tag --force`, then re-apply the policy |
| Node stuck on the synthetic "tagged-devices" user | Registered without a valid user mapping; no backfill strategy matches | Manual `headscale nodes tag --force`; going forward attach tags at auth time |

**Time:** ~3–5 minutes per node sequentially, ~5 minutes if the VPS-side nodes
run in parallel across separate SSH sessions, plus ~5 minutes of verification.

---

## 6. Tailnet split fix

### 6.1 Symptoms

* Peers that should see each other report `no matching peer` on `tailscale ping`,
  or simply never appear in `tailscale status`.
* The container-side probe prints a diagnosis line like
  `DIAGNOSIS: TAILNET SPLIT LIKELY` and a partial reachability summary.
* `/admin/system_tests` → `tailnet.split_suspected` fails or reports a partial
  percentage.
* Devices on one side show as offline for no obvious reason (check §2.5 — a
  firewall rule produces the same symptom).

Operator gist of the report this runbook answers (translated): *"the devices
stopped seeing each other — some peers are simply not in the map anymore."*

### 6.2 Cause

The nodes ended up in **two divergent maps** because peers were authenticated
against different states of the headscale control plane (the grants/ACL policy
filters cross-group traffic, so a node whose group membership is stale drops out
of the other group's map). Re-authenticating each node against the *current*
control plane rebuilds a single map.

### 6.3 Fix procedure

```bash
# 1. Mint a fresh preauth key (one-time, 24h, reusable).
docker exec headscale headscale preauthkeys create --user <user-id> \
  --reusable --expiration 24h                      # → save the key

# 2. On EACH affected node, generate the exact `tailscale up` command (the
#    script preserves every non-default flag, avoiding the "changing settings
#    via 'tailscale up' requires mentioning all non-default flags" trap).
curl -sL https://raw.githubusercontent.com/BarsSky/skygate/main/scripts/fix_tailnet_split.sh -o /tmp/fix.sh
PREAUTH_KEY=<key> bash /tmp/fix.sh

# 3. Copy the printed command, run it on that node, then verify:
sudo bash -c "tailscale up --login-server=https://head.<your-domain> \
  --hostname=<this-node> --advertise-tags=<its-tags> --auth-key=<key>"
tailscale status

# 4. After the last node, wait ~60 s for the map to propagate, then probe from
#    the primary; check /admin/system_tests → tailnet.split_suspected → PASS.
docker exec skygate bash /app/scripts/tailnet_probe.sh
#    expected: all online peers reachable, NO split diagnosis line

# 5. Hygiene: expire the key you just used.
docker exec headscale headscale preauthkeys list -o json
docker exec headscale headscale preauthkeys expire --user <user-id> --key <KEY_ID>
```

**Order of operations** (to minimise downtime): the **VPS-side "anchor" nodes
first** — once they re-auth, the new map starts propagating — then the skygate
host, then the home devices last (re-authenticating them kills the old session
held by the home router). Wait 60 s before probing. Never re-auth an exit node
with `--advertise-exit-node=false` or without its existing tags — that silently
strips routing capability; preserve every `tag:*` the node had.

**Downtime:** effectively none (each re-auth takes seconds; peers stay reachable
through the DERP relay). Budget 30–45 minutes of operator time for a medium
tailnet, ~3–5 minutes per device, parallelisable.

### 6.4 If it does not converge

| Symptom | Fix |
|---|---|
| `Error: node exists` during re-auth | Normal — the old NodeKey is still registered under the same NodeID. Wait 5–10 s; headscale re-links it automatically. |
| Map still split after every node re-authed | The headscale database is itself divergent: `docker restart headscale`. The control plane reloads from disk, all `/machine/map` sessions reset, and every node re-polls within 60 s. |
| The preauth key expired mid-rollout | Mint another 24 h reusable key and continue where you stopped. |
| The printed command is rejected for "non-default flags" | Copy the exact command from the error message — nodes carry different non-default flags (`--ssh`, `--shields-up`, advertised routes). |

> **Caveat:** the source runbook is internally inconsistent about fleet size
> (14 vs 17 nodes, one duplicated row) and carried a live preauth key inline —
> see §12.

---

## 7. Issues close-out workflow

### 7.1 The status matrix

Keep one table per batch of issues; it is the artifact that survives, while the
individual close comments are disposable. Use exactly three states — **CLOSED**,
**OPEN**, **PARTIAL** — where PARTIAL is what stops an issue being closed on a
technicality when only half shipped.

| # | Title | Status | Closed by |
|---|---|---|---|
| 1 | … | ✅ CLOSED | commit shas |
| 2 | … | ❌ OPEN | — (what is missing) |
| 3 | … | ⚠️ PARTIAL | what is done / what remains |
| 4 | … | ❌ OPEN | — (non-code work) |
| 5 | … | ✅ CLOSED | commit sha |

### 7.2 Evidence required, and how to close

A close-out is valid only with **all** of: a commit that changes behaviour (not
just docs — the commit is the *mechanism*: a new handler, an env flag, a
dialect-aware code path, an added compose file); a one-line root cause per issue;
a verification path (the command or UI page that confirms the fix live); and an
honest residual list for any part needing non-code work (a CI matrix change, a
schema-docs regeneration, an operator action).

```bash
gh issue close <n> --comment "<commits, mechanism, verification path, and the doc that explains it>"
```

The comment must stand alone: name the commits, the mechanism in one sentence,
the operator-facing document, and — for a partial close — explicitly which half
is still open. Never close an issue whose remaining half is unowned.

### 7.3 Current state of the 2026-09-11 batch

Filed against a first live deploy next to an existing headscale:

| # | Title | Status | Notes |
|---|---|---|---|
| 1 | Nodes joined via the headscale CLI never appear in `/my/devices` or `/my/exit-rules` | ✅ CLOSED | Five commits: a first-run banner helper, the banner wired into `/admin/devices`, the bulk-claim handler ("Sync all nodes for this user"), exit-server auto-detect during the headscale sync, and `SKYGATE_IMPORT_EXISTING_ON_FIRST_RUN` making the whole flow automatic on first boot. |
| 2 | Admin cannot create exit-rules for another user's devices | ❌ OPEN | Needs a separate `POST /admin/exit-rules` handler writing a `device_rules` row with `src = device owner`, not the admin session. |
| 3 | `node_owner_map` / `device_rules` schema docs stale; lite compose has no Postgres | ⚠️ PARTIAL | Compose half closed (a SQLite compose variant was added); schema-docs half open — regenerate from the migration source. |
| 4 | The image had no linux/amd64 manifest | ✅ CLOSED (since) | Closed by dropping arm64 from the Docker matrix (§1.1) — CI/build infrastructure, not code. The source document still says OPEN because it predates the fix. |
| 5 | The PG-only runtime still ran SQLite SQL (`strftime`, wrong `applied_migrations` shape) | ✅ CLOSED | Migration tracking is now dialect-aware: both table creation and "record applied" dispatch on the detected backend and emit PG-native SQL (`EXTRACT(EPOCH FROM now())::bigint`, `ON CONFLICT … DO NOTHING`, `$N`) or SQLite-native SQL (`strftime('%s','now')`, `INSERT OR IGNORE`, `?`). |

---

## 8. Telegram relay operations

The relay exists so a skygate host in a jurisdiction where the Telegram Bot API
is blocked can still reach it. For the feature itself (bot commands, chat
binding, probe semantics) see [TELEGRAM.md](TELEGRAM.md); this section is the
deploy/verify/rollback half.

### 8.1 How the relay works

The skygate host reaches headscale directly over eth0, is **blocked** for
everything else, and reaches the Telegram CIDRs through `tailscale0` via a relay
that advertises them as **subnet routes**. The key design choice is subnet
routes, **not an exit node**: skygate never runs `tailscale set --exit-node=…`,
so only the Telegram ranges are re-routed and the headscale API plus the local
Docker network stay direct. An exit node would replace the default route for
every non-tailnet packet. The relay's advertised routes do nothing until a
headscale admin **approves** them.

Read the canonical CIDR list from source — `TelegramCIDRs` in
`internal/feature/admin/telegram.go`, written by
`deploy/tailscale-relay/{setup.sh,update-routes.sh}` — rather than copying a
range list out of a document, because Telegram adds blocks over time.

### 8.2 Deploy a relay

Pick a host with tailscaled installed and logged in to *your* headscale, with
**unrestricted internet egress** (that is the whole point), on the same tailnet
as skygate.

```bash
# On the relay host:
sudo /opt/skygate/deploy/tailscale-relay/setup.sh
# applies: tailscale set --advertise-routes="<canonical Telegram CIDRs>"

# Then approve the routes — advertising is not enough:
# A. headplane UI: https://head.example.com → Machines → relay node →
#    "advertised routes" → tick each route → Approve
# B. or the CLI:
docker exec headscale headscale nodes list            # find the relay's numeric id
docker exec headscale headscale nodes approve-routes \
  --identifier <id> --routes "<comma-separated canonical CIDRs>"
```

Verify — inside the skygate container the routes are present and used, and the
`/admin/telegram` probe banner flips from "unreachable" to "reachable (Tailscale
relay)" within ~60 s of approval:

```bash
docker exec skygate tailscale status      # a "Subnets" section, each "via 100.64.x.y"
docker exec skygate tailscale netcheck
```

### 8.3 Routine maintenance

Telegram occasionally adds IP blocks; the advertisement is static, so an
unrefreshed relay misses them.

```bash
# Refresh: resolves api/web/core.telegram.org from three public resolvers,
# aggregates the A records into the canonical CIDRs, re-applies with
# `tailscale up --advertise-routes=…` (a RESET, not additive, so stale ranges are
# pruned), and prints the approve-routes command the admin must run. It refuses
# an empty list — that would wipe the advertisement and break the bot.
sudo /usr/local/bin/skygate-update-telegram-routes
# Weekly cron on the relay:
# 0 4 * * 1  /usr/local/bin/skygate-update-telegram-routes >> /var/log/skygate-telegram-routes.log 2>&1
```

The **admin UI egress selector** (`/admin/telegram` → "Egress relay" card) does
the same job interactively: it lists every relay from `exit_servers WHERE
enabled=1`, and on Apply SSHes to the chosen relay and runs the canonical
`tailscale set --advertise-routes=…`, persisting the choice in
`global_settings.telegram.egress_node_id`. "Clear" returns to Tailscale's
metric-based auto-pick. The selection is **not** updated when the SSH call fails;
every attempt is audited (`action LIKE 'telegram_egress%'`). Both relays may
advertise the same routes at once — Tailscale picks by metric.

### 8.4 Failover, rollback, failure modes

```bash
# Failover to another relay:
# 1. On the backup relay: run setup.sh (it starts advertising the same routes).
# 2. Optionally drop the failed relay's advertisement:
#    tailscale set --advertise-routes=   (no routes) on the old relay, or
#    disable its routes in headscale.
# 3. Wait ~60 s — skygate picks the better metric automatically.
```

Only drop the old advertisement to *force* the other relay; two relays
advertising the same routes is a supported state.

**Rollback / disable:** remove `TS_AUTHKEY_FILE` and the `secrets/ts_authkey`
mount from the compose file. The entrypoint skips tailscaled and bot polling goes
over eth0 directly — the "one image, two modes" property (same image, compose
file and binary; only the auth-key env var changes the behaviour).

| Symptom | Cause | Fix |
|---|---|---|
| `/admin/telegram` says "unreachable" | No relay routes approved in headscale | Run the approve-routes command the setup script printed |
| "unreachable", but resolved IPs are shown | The resolved IPs are not inside the advertised ranges | Run the route-refresh script on the relay, then approve the new ranges |
| Banner says "reachable (direct internet)" on an RF VPS | skygate is going out eth0 — Tailscale is not using the accepted routes for those IPs | Confirm `tailscale status` in the container lists the routes and that the resolved IPs are covered |
| `tailscale status` inside skygate says "Tailscale is stopped" | tailscaled failed to start in the container | `docker logs skygate 2>&1 \| grep -iE 'tailscale\|tailscaled'`; check `/var/log/tailscaled.log` in the container |
| Relay shows routes advertised, headscale shows them not enabled | Admin never approved them | Approve |
| Egress card: "No enabled exit-nodes" | No `exit_servers` row has `enabled=1` | Add the relay via `/admin/exit-nodes` (the selector only lists enabled rows) |
| Egress card: SSH flash "Could not resolve hostname" | `ssh_target` uses a hostname the skygate container cannot resolve | Set `ssh_target` to `user@<ip>` on the exit-node row (the `host:port` shorthand only works in `~/.ssh/config`, not on the `ssh` command line) |
| Egress card: "no ssh_key_path provided" | The exit-node row has no `ssh_key_path` and the global env is empty | Set the per-row `ssh_key_path` (or the env); the hardened path deliberately refuses a legacy operator-specific fallback |
| `/admin/exit-nodes` → Re-sync: `ssh=err=… Identity file /ssh-sync/id_ed25519 not accessible` and **`Could not resolve hostname <relay>`**, while the row shows a Tailscale IP | Two B292 causes: (1) `/ssh-sync/id_ed25519` is the CONTAINER default and this host is not a container (the path cannot exist); (2) the `exit_servers` row has neither `ssh_target` nor `tailscale_ip`, so the sync fell back to the bare node name — the IP in the table came from headscale, not from the row | (1) `install -d -m 700 -o <service-user> -g <service-user> /var/lib/skygate/ssh && ssh-keygen -t ed25519 -N '' -f /var/lib/skygate/ssh/id_ed25519`, then append the public key to the relay's `~/.ssh/authorized_keys`; (2) on v1.5.56+ the sync fills `tailscale_ip` from the live headscale view by itself (and resolves `root@<ip>`), and the page flags the row when the key is unusable. Set `ssh_target`/`ssh_key_path` explicitly on the row for a non-default port or a per-relay key |
| Bot polling works but slowly | The relay is far away | Accept it, or pick a closer relay |

---

## 9. Backup, WAL-G and the restore drill

[backup-restore-and-migration.md](backup-restore-and-migration.md) owns the
backup/restore/migration flows (what is in an archive, how to replay it, how to
move hosts, and the archive-integrity checks). This section carries only the
WAL-G operating notes that are not in it, plus the restore drill.

### 9.1 WAL-G layout and install (each PG node)

Object storage is an S3-compatible bucket dedicated to WAL, exposed publicly
(`https://minio.<your-domain>`, for VPS nodes that cannot reach the home LAN) and
over the LAN for co-located nodes. The **primary** runs Patroni and both writes
WAL via `archive_command` and takes base backups; the **replica** has WAL-G and
its env so `wal-g backup-list` / `wal-show` work without public DNS. Both write
to the **same bucket**, so the catalogue is unified. Retention is wal-g's default
(5 base backups plus all WAL — a few GB for ~1 week of PITR); offsite replication
is the storage layer's job.

Install parameters (names, not values): `SKYGATE_MINIO_ACCESS_KEY`,
`SKYGATE_MINIO_SECRET_KEY`, `SKYGATE_MINIO_BUCKET`, `SKYGATE_MINIO_ENDPOINT`
(use the LAN endpoint on co-located nodes — materially faster).

```bash
SKYGATE_MINIO_ACCESS_KEY=<access-key> \
SKYGATE_MINIO_SECRET_KEY=<secret> \
SKYGATE_MINIO_BUCKET=skygate-pg-wal \
SKYGATE_MINIO_ENDPOINT=https://minio.<your-domain> \
sudo bash deploy/pg-ha/wal-g/install_wal_g.sh
# installs lz4 (+ optional daemontools on the primary), downloads the WAL-G
# binary, installs /usr/local/bin/wal-g, writes /etc/wal-g/env (root:postgres,
# mode 640) with every WALG_*/AWS_* variable, then verifies with
# `wal-g backup-list` + `wal-g wal-show`.
```

### 9.2 Primary-only: continuous WAL archiving

```yaml
# /etc/patroni/patroni.yml → postgresql.parameters
    unix_socket_directories: '/var/run/postgresql'
    archive_mode: on
    archive_command: '. /etc/wal-g/env && wal-g wal-push %p'
    archive_timeout: 60
```

```bash
# Reload (SIGHUP) — Patroni schedules a pending restart.
curl -X POST http://127.0.0.1:8008/reload

# Apply. Causes a brief failover (the replica is promoted for a few seconds,
# then the original primary returns and is re-promoted).
yes | sudo -u postgres patronictl -c /etc/patroni/patroni.yml \
  restart skygate-pg <primary-name> --force

sudo -u postgres psql -c "SHOW archive_mode; SHOW archive_command; SHOW archive_timeout;"
sudo -u postgres psql -c "SELECT * FROM pg_stat_archiver;"   # archived_count > 0, growing
```

New WAL segments appear in the bucket within ~60 s.

### 9.3 Base backups, verification and the restore drill

```bash
# Local mode (fast — uses data_dir); the first backup is full, later ones DELTA.
sudo -u postgres bash -c '. /etc/wal-g/env && wal-g backup-push /var/lib/postgresql/data'

# The standing verification set:
sudo -u postgres bash -c '. /etc/wal-g/env && wal-g backup-list'   # bucket visible
sudo -u postgres bash -c '. /etc/wal-g/env && wal-g wal-show'      # segments present, growing
```

**Restore drill.** Do **not** run `wal-g backup-fetch` while Patroni is running —
it conflicts with Patroni's management of the data directory.

```bash
# 1. Stop Patroni + PG on the target node.
sudo systemctl stop patroni 2>/dev/null || sudo pkill -9 -f "patroni /etc/patroni"

# 2. Wipe / recreate the data dir.
sudo rm -rf /var/lib/postgresql/data
sudo -u postgres mkdir /var/lib/postgresql/data
sudo -u postgres chmod 700 /var/lib/postgresql/data

# 3. Fetch the latest base backup.
sudo -u postgres bash -c '. /etc/wal-g/env && \
  wal-g backup-fetch /var/lib/postgresql/data LATEST'

# 4. Configure recovery.
cat > /var/lib/postgresql/data/recovery.signal <<'EOF'
restore_command = '. /etc/wal-g/env && wal-g wal-fetch %f %p'
recovery_target_timeline = 'latest'
EOF
sudo -u postgres chown postgres:postgres /var/lib/postgresql/data/recovery.signal

# 5. Start PG — it replays WAL up to the latest point.
sudo systemctl start postgresql 2>/dev/null || \
  sudo -u postgres /opt/patroni/venv/bin/patroni /etc/patroni.yml
# or, if Patroni should manage it: patronictl -c /etc/patroni/patroni.yml restart skygate-pg
```

Then run the application-level verification from
[backup-restore-and-migration.md](backup-restore-and-migration.md) §2.

### 9.4 WAL-G v3.0.8 quirks

1. **`AWS_ENDPOINT` is the working name for the S3 URL.** The v3.0.0 notes claim
   all S3 env vars moved to a `WALG_` prefix, but in v3.0.8 only
   `WALG_S3_PREFIX` actually maps; endpoint, access key, secret and region still
   use the `AWS_*` family. `WALG_S3_ENDPOINT` / `AWS_S3_ENDPOINT` makes the store
   return `InvalidAccessKeyId: 403` because the SDK signs differently. Working
   set:

   ```bash
   export WALG_S3_PREFIX=s3://skygate-pg-wal
   export AWS_ENDPOINT=https://minio.<your-domain>
   export AWS_ACCESS_KEY_ID=<access-key>
   export AWS_SECRET_ACCESS_KEY=<secret>
   export AWS_REGION=us-east-1
   export AWS_S3_FORCE_PATH_STYLE=true
   ```

2. **`archive_command` must `source` the env file, not use `envdir`.** `envdir`
   expects a directory of single-variable files, not a single file
   ("unable to switch to directory"). `'. /etc/wal-g/env && wal-g wal-push %p'`
   works in the `/bin/sh -c` PostgreSQL uses and needs no daemontools.

---

## 10. Day-2 checklist

### 10.1 Weekly

- [ ] `bash scripts/verify_post_deploy.sh` — runtime checks green on the primary (and any standby).
- [ ] `bash scripts/verify_backup.sh` — the latest archive still replays into a throwaway database (this proves the *dump*, not just the backup job).
- [ ] WAL archiving still advancing: `SELECT * FROM pg_stat_archiver;` → `archived_count` grew since last week; at least one recent `wal-g backup-list` entry exists.
- [ ] Preauth key rotation ran (`scripts/rotate_ts_authkey.sh` cron); no key inside the 14-day expiry buffer.
- [ ] Telegram relay route refresh ran (`0 4 * * 1` cron) and the approve-routes command it printed was approved.
- [ ] Disk headroom on the skygate host and the DB host (journald + docker log rotation is the usual fix).
- [ ] `/admin/system_tests` — no newly failing test (especially `tailnet.split_suspected` and the ACL/exit-rule tests).
- [ ] `/admin/audit` — no unexpected `claim_all_for_user`, `device_deleted`, `tailscale_save_key`, `telegram_egress*` or `ha.preauth.create` rows.

### 10.2 Before every release

- [ ] `git status --porcelain` clean, locally and on the VM.
- [ ] `bash scripts/verify_pre_deploy.sh` — all PASS or explicit SKIP, no FAIL.
- [ ] `staticcheck ./...` clean; `go build ./...`, `go vet ./...` clean.
- [ ] **Clean-host acceptance (§1.4) passed**: install → native self-update → `verdict: done` with a matching `/healthz` build string.
- [ ] Release notes written as the `## vX.Y.Z` section of `RELEASE-NOTES.md` and inside the tagged commit; the workflow contains the fixed `path: dist/checksums` line and the section-extraction step.
- [ ] After the tag: `SHA256SUMS` is a real **file** listing every archive; the ghcr tag is **lowercase**; the linux-amd64 tarball reports the expected build string; the §1.5 live-state checks pass.
- [ ] Rollback path confirmed: `<data_dir>/update/skygate.prev` exists (or the previous tag is still pullable).

### 10.3 After every incident

- [ ] The tag/asset that caused it is deleted or fixed and re-published (§1.6) — never leave a broken release public.
- [ ] The applier verdict (`done` / `rolled_back` / `failed`) and `apply.log` recorded, so the failure is attributable.
- [ ] Root cause in one line, and a guard added so the class cannot return — a B-check contract, a unit test, or a guard test that freezes the remaining count.
- [ ] Any credential, key or address that leaked into git is rotated and removed; the pre-commit hook is a backstop, not a guarantee.
- [ ] Issue close-out done per §7 (with evidence), or explicitly annotated with what remains open.
- [ ] Backups verified, not assumed: the latest archive is restorable and WAL archiving resumed.
- [ ] If a host was replaced or rebuilt, the §4.7 verification table was walked end-to-end.
- [ ] `docs/operations.md` updated **in the same commit** as the fix.

---

## 11. Admin role delegation and the primary admin

Since **v0.72 / B264** the `/admin/users` page has per-row **Promote** and
**Demote** buttons, so an administrator can grant *and* revoke the `admin` role
for other portal users without touching SQL. Both buttons confirm before
submitting and both write an audit row (`admin_promote` / `admin_demote`) named
with the target user and the operator.

**How to delegate.**

1. Sign in as an existing admin and open `/admin/users`.
2. Find the target row and open its action menu (`⋯`).
3. Click **Promote to admin** to grant, **Demote** to revoke.
4. Verify: the row's Role pill flips between `admin` and `user`, and
   `/admin/audit` shows the matching `admin_promote` / `admin_demote` row.

**The primary admin is immutable.** Exactly one portal row carries
`portal_users.is_primary = 1` — the bootstrap/root account named by
`SKYGATE_ADMIN_USER` (default `admin`). That row renders a lock badge instead of
the destructive actions, and the server refuses to demote, delete or rename it.
Two extra refusals protect the install from locking itself out:

* **you cannot demote yourself** — otherwise the admin who clicked would lose
  `/admin/*` on submit (have another admin do it, if that is really the goal);
* **you cannot demote the last remaining admin** — the install would be left
  with no administrator at all.

All three refusals come back as a flash on `/admin/users`, not as an error page.

**Why the marker exists.** Before B264 the only role column was `is_admin`, and
the sync contract asserted *exactly one* admin row — so a second admin could not
exist and "the canonical account" was indistinguishable from "an admin a
colleague granted". V072 adds `is_primary` plus the partial unique index
`portal_users_one_primary_uniq` (`WHERE is_primary = 1`), which makes *at most
one primary* a database-level invariant on both PostgreSQL and SQLite. Multiple
`is_admin = 1` rows are expected and fine.

**If the primary marker is wrong or missing.** The marker self-heals on boot:
`bootstrapAdmin` re-asserts `is_admin = 1` and `is_primary = 1` for the
`SKYGATE_ADMIN_USER` row (and clears a stale marker on another row first) — but
only when `SKYGATE_ADMIN_PASS` is set. The V072 migration backfills the marker
for the configured name and, when that name matches no row, falls back to the
lowest-id admin so the invariant "exactly one primary whenever any admin exists"
still holds. The live check is:

```bash
bash scripts/check_b_admin_user_sync.sh
```

It asserts exactly one `is_primary = 1` row, that the row is `is_admin = 1`, and
that its username equals `SKYGATE_ADMIN_USER` (contracts P/A/A2/B), plus the
headscale-side link (C/D/E). It prints `SKIP` — never `FAIL` — when docker or
PostgreSQL is unreachable. To repair by hand, promote the intended account and
then move the marker in one transaction-free pair of statements (the unique
index forbids two primaries, so clear first):

```sql
UPDATE portal_users SET is_primary = 0 WHERE is_primary = 1 AND username <> 'admin';
UPDATE portal_users SET is_admin = 1, is_primary = 1 WHERE username = 'admin';
```

---

## 12. Caveats — stale or contradictory sources

Consolidating five public runbooks and five internal documents surfaced these
contradictions and staleness issues. None of them changes a procedure above;
they are recorded so a future reader does not trust the wrong copy.

1. **The PG cutover runbook is historical, not pending.** "v0.33.0 — PostgreSQL
   cutover" is status "design-only, blocked on a staging VM", dated 2026-08-03;
   the cutover completed in the v1.3.x line, so PostgreSQL is the production
   backend. Its prerequisites (a `v0.32.19` floor, `make verify-pre` == 35/35,
   `make test` == 118/118 smoke) are from that era — use its *shape* (§3), not
   its counts.
2. **`make verify-pre` / `verify_pre_deploy.sh` is fail-tolerant BY DESIGN locally — CI is the
   enforcer.** The historical baseline printed **37 FAIL lines and still exited 0**, and that is
   still true of the script itself: it prints no final summary and no aggregated exit status, so
   read the FAIL list, never `$?`. Two things changed since: B322 (v1.5.87) made **every
   individual check** propagate its own verdict (a check that counts FAIL must exit non-zero, a
   `-run` filter matching no test fails, and the 24 masked `printf | bash … ; rm -f` chains now
   return their check's status) and the gate prints a failing check's own FAIL lines before its
   20-line window; and `.github/workflows/ci.yml` ("Run verify_pre_deploy.sh (and refuse any
   FAIL)") turns the catalog into the gate by failing the job on any `^  FAIL`/`^  TIMEOUT` line.
   A local run therefore still exits 0 with pre-existing FAILs — those are environment failures
   (`B1` from the container-dependent `internal/headscale` tests, `B176`, `B188.2/B188.3`,
   `B237.16`, `B294`) that SKIP on the CI runner. The missing aggregate exit code is TD-22.
3. **`.env` variable naming is mid-migration.** Older docs and scripts use
   `SKYGATE_DB_DSN` exclusively; the current unified selector is `SKYGATE_DB`,
   with `SKYGATE_DB_DSN` honoured when `SKYGATE_DB` is unset. When both are
   present and disagree, `SKYGATE_DB` wins; see
   [deploy.md §1](deploy.md#1-environment).
4. **"Install Tailscale" is not covered by the base installers.** The deploy
   test plan flags this as an open gap: the Debian installer does not install
   Tailscale (not in the default repositories), yet the standby bootstrap
   requires the CLI and dies at its step 0 without it. Install it first on any
   host using the standby/HA path.
5. **The bootstrap runbook names a stale config path** (`/etc/skygate.env`); the
   installers write `/etc/skygate/skygate.env`. Trust the installers and
   [INSTALL.md](INSTALL.md).
6. **The restore script does not replay a PG dump.** `scripts/restore.sh` was
   written for the SQLite era; for PostgreSQL the operator runs
   `psql -f skygate-pg.sql` manually — documented, not fixed, in
   [backup-restore-and-migration.md](backup-restore-and-migration.md) §2.
7. **The clean-install runbook's "issue #4 still open — use `:latest`" advice is
   obsolete.** arm64 was dropped from the Docker matrix; the image is amd64-only
   by design and ARM users take the Go tarballs or build their own image.
8. **Smaller inconsistencies:** the captured baseline's headscale version label
   disagreed with `docker inspect` (0.29.2 vs v0.29.1) — check the running
   binary; the tailnet-split runbook's fleet count is self-contradictory (14 vs
   17 nodes with a duplicated row) — derive it from `tailscale status`; the HA
   bootstrap's chain-verification table targets `/admin/ha/chain` and
   `/admin/ha/active` "if you exposed a debug endpoint" — treat the `/admin/ha`
   page as the primary verification.
9. **One superseded runbook carried a live-looking preauth key and real public
   IPs/hostnames inline.** They are not reproduced here; rotate any such
   credential found in git history. The pre-commit hook blocks known private
   addresses but not public ones, usernames or keys.
10. **More operational runbooks exist than the ones consolidated here**
    (`derp-cert-sync`, `exit-rules-reconciler`, `ha-active-router`,
    `https-setup`, `oidc-headscale`, `subnet-router`,
    `tailnet-advertised-routes`, and the HA execution tracker). They remain
    where they are; their day-2 topics are covered for operators by
    [https.md](https.md), [oidc.md](oidc.md), [derp.md](derp.md), [ha.md](ha.md)
    and [networking.md](networking.md).

---

## 13. Host disk headroom — the guarantee catalog needs ~1 GB free

Measured 2026-09-25 on the reference VM while running the full catalog: `/` was at **99 % (697 MB
free)** *before* the run and **100 % (388 MB free)** after it. A full `verify_pre_deploy.sh` pass
costs a few hundred MB (Go build-cache growth for `go build ./...`, `go test ./...`, `go vet` and
`staticcheck`, plus a detached worktree). Two consequences worth knowing:

* **Check before you run it.** `df -h /` first; a run that hits ENOSPC midway produces a log with
  no summary and a confusing verdict.
* **The reclaim that costs nothing** is the Go build cache:

  ```bash
  du -sh ~/.cache/go-build      # measured 5.5 GB after a full catalog run
  go clean -cache               # frees it; Go rebuilds on demand, nothing is lost
  ```

  On the reference VM that took the host from 100 % back to **85 % (5.9 GB free)**. It does not
  touch `~/go/pkg` (the module cache — re-downloading it needs network, so leave it), the
  operator's SQLite database or any container image.

The other large consumers measured on that host, in order: `~/.cache` 5.7 GB (mostly the build
cache above), `/var/lib/containerd` 6.3 GB (container images — do **not** prune blindly, the
headscale/DERP stack lives there), `~/.hermes` 2.7 GB, `~/go/pkg` 2.0 GB, `/var/log/journal`
502 MB (`journalctl --vacuum-size=…` is safe but drops diagnostic history — this project's
incident analysis depends on that journal, so prefer the build cache first), `/var/lib/snapd/cache`
357 MB (pure download cache, always safe to delete). The catalog itself leaves nothing behind
beyond the build cache: remove its worktree afterwards with
`git worktree remove --force <path>`.
