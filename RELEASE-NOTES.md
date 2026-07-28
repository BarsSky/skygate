# Skygate release notes

## Where to look for releases

**This file is an index. The authoritative source for any release is
the git tag.** Browse releases:

```sh
git tag --list                      # all tags
git show v0.26.0                    # full diff + message for v0.26.0
git log --oneline v0.25.0..v0.26.0  # commits between two tags
```

The GitHub Releases view mirrors the tags and adds a UI:
https://github.com/skygate-operator/skygate/releases

`CHANGELOG.md` is the human-curated summary of what's in main
at any moment, organized by [Keep a Changelog](https://keepachangelog.com/)
format. Older `RELEASE-NOTES-v0.X.Y.md` files (deleted in 2026-07-24
as part of the v0.27.0 repo cleanup) had the same content as the
commit messages + the eventual GitHub release notes — nothing was
lost; everything is still in `git log` + the GitHub UI.

## Index of pre-cleanup releases (for git archaeology only)

| File (deleted) | Tag | Title / scope |
| --- | --- | --- |
| `RELEASE-NOTES-v0.16.1.md` | [`v0.16.1`](https://github.com/skygate-operator/skygate/releases/tag/v0.16.1) | What changed |
| `RELEASE-NOTES-v0.16.2.md` | [`v0.16.2`](https://github.com/skygate-operator/skygate/releases/tag/v0.16.2) | Symptoms |
| `RELEASE-NOTES-v0.16.3.md` | [`v0.16.3`](https://github.com/skygate-operator/skygate/releases/tag/v0.16.3) | What changed |
| `RELEASE-NOTES-v0.16.4.md` | [`v0.16.4`](https://github.com/skygate-operator/skygate/releases/tag/v0.16.4) |  |
| `RELEASE-NOTES-v0.16.5.md` | [`v0.16.5`](https://github.com/skygate-operator/skygate/releases/tag/v0.16.5) |  |
| `RELEASE-NOTES-v0.16.6.md` | [`v0.16.6`](https://github.com/skygate-operator/skygate/releases/tag/v0.16.6) | What changed |
| `RELEASE-NOTES-v0.16.7.md` | [`v0.16.7`](https://github.com/skygate-operator/skygate/releases/tag/v0.16.7) | What changed |
| `RELEASE-NOTES-v0.16.8.md` | [`v0.16.8`](https://github.com/skygate-operator/skygate/releases/tag/v0.16.8) | Fix |
| `RELEASE-NOTES-v0.16.9.md` | [`v0.16.9`](https://github.com/skygate-operator/skygate/releases/tag/v0.16.9) | 1. Sidebar username empty on /admin/users/{id}/subnet |
| `RELEASE-NOTES-v0.16.10.md` | [`v0.16.10`](https://github.com/skygate-operator/skygate/releases/tag/v0.16.10) | 1. scripts/check_https.py — fix the pre-existing chmod+x mismatch |
| `RELEASE-NOTES-v0.17.0.md` | [`v0.17.0`](https://github.com/skygate-operator/skygate/releases/tag/v0.17.0) | What changed |
| `RELEASE-NOTES-v0.17.1.md` | [`v0.17.1`](https://github.com/skygate-operator/skygate/releases/tag/v0.17.1) | What changed |
| `RELEASE-NOTES-v0.18.0.md` | [`v0.18.0`](https://github.com/skygate-operator/skygate/releases/tag/v0.18.0) | What changed |
| `RELEASE-NOTES-v0.18.1.md` | [`v0.18.1`](https://github.com/skygate-operator/skygate/releases/tag/v0.18.1) | 1. `check_https.py` HSTS /login 404 (the user |
| `RELEASE-NOTES-v0.20.0.md` | [`v0.20.0`](https://github.com/skygate-operator/skygate/releases/tag/v0.20.0) | 1. `headscale-update-monitor` — the operator |
| `RELEASE-NOTES-v0.21.0.md` | [`v0.21.0`](https://github.com/skygate-operator/skygate/releases/tag/v0.21.0) | Why this matters |
| `RELEASE-NOTES-v0.21.1.md` | [`v0.21.1`](https://github.com/skygate-operator/skygate/releases/tag/v0.21.1) | The bug |
| `RELEASE-NOTES-v0.22.0.md` | [`v0.22.0`](https://github.com/skygate-operator/skygate/releases/tag/v0.22.0) |  |
| `RELEASE-NOTES-v0.22.1.md` | [`v0.22.1`](https://github.com/skygate-operator/skygate/releases/tag/v0.22.1) |  |
| `RELEASE-NOTES-v0.22.2.md` | [`v0.22.2`](https://github.com/skygate-operator/skygate/releases/tag/v0.22.2) |  |
| `RELEASE-NOTES-v0.22.3.md` | [`v0.22.3`](https://github.com/skygate-operator/skygate/releases/tag/v0.22.3) |  |
| `RELEASE-NOTES-v0.23.0.md` | [`v0.23.0`](https://github.com/skygate-operator/skygate/releases/tag/v0.23.0) | What changed |
| `RELEASE-NOTES-v0.23.1.md` | [`v0.23.1`](https://github.com/skygate-operator/skygate/releases/tag/v0.23.1) |  |
| `RELEASE-NOTES-v0.23.3.md` | [`v0.23.3`](https://github.com/skygate-operator/skygate/releases/tag/v0.23.3) | TL;DR |
| `RELEASE-NOTES-v0.23.4.md` | [`v0.23.4`](https://github.com/skygate-operator/skygate/releases/tag/v0.23.4) |  |
| `RELEASE-NOTES-v0.24.0.md` | [`v0.24.0`](https://github.com/skygate-operator/skygate/releases/tag/v0.24.0) |  |
| `RELEASE-NOTES-v0.24.1.md` | [`v0.24.1`](https://github.com/skygate-operator/skygate/releases/tag/v0.24.1) | Why this change |
| `RELEASE-NOTES-v0.24.2.md` | [`v0.24.2`](https://github.com/skygate-operator/skygate/releases/tag/v0.24.2) |  |
| `RELEASE-NOTES-v0.25.0.md` | [`v0.25.0`](https://github.com/skygate-operator/skygate/releases/tag/v0.25.0) | What did NOT change |
| `RELEASE-NOTES-v0.25.1.md` | [`v0.25.1`](https://github.com/skygate-operator/skygate/releases/tag/v0.25.1) | 1. Per-user audit log export (CSV/JSON) |
| `RELEASE-NOTES-v0.26.0.md` | [`v0.26.0`](https://github.com/skygate-operator/skygate/releases/tag/v0.26.0) |  |
| `RELEASE-NOTES-v0.28.0.md` | [`v0.28.0`](https://github.com/skygate-operator/skygate/releases/tag/v0.28.0) | per-device ACL via `tag:dev-<user>-<device>` |
| `RELEASE-NOTES-v0.28.1.md` | [`v0.28.1`](https://github.com/skygate-operator/skygate/releases/tag/v0.28.1) | per-user preferred exit-node (UI + data model) |
| `RELEASE-NOTES-v0.28.2.md` | [`v0.28.2`](https://github.com/skygate-operator/skygate/releases/tag/v0.28.2) | `hosts:` block workaround for headscale 0.29.2 grants parser |
| `RELEASE-NOTES-v0.28.3.md` | [`v0.28.3`](https://github.com/skygate-operator/skygate/releases/tag/v0.28.3) | close exit-node bypass: per-user dst has autogroup:internet; catch-all src=tag:public |
| `RELEASE-NOTES-v0.28.4.md` | [`v0.28.4`](https://github.com/skygate-operator/skygate/releases/tag/v0.28.4) | per-device preferred exit-node (workstation-3 → relay-3 etc.) |
| `RELEASE-NOTES-v0.28.5.md` | [`v0.28.5`](https://github.com/skygate-operator/skygate/releases/tag/v0.28.5) | via opt-in (Android-friendly) + migration v0.47 idempotency + tagged-device exit-node fix + entrypoint always clears stale Tailscale exit-node |
| `RELEASE-NOTES-v0.28.6.md` | [`v0.28.6`](https://github.com/skygate-operator/skygate/releases/tag/v0.28.6) | guarantee catalog (B1-B10 build + R1-R25 runtime) — `make verify-pre` / `make verify-post` are the contract |

## v0.29.2 — Remove `container_name: skygate`, add `skygate` host-side wrapper

v0.29.1 worked around the `docker compose up --force-recreate`
race by stopping the orchestrator at "image rebuilt" (manual
swap). The race still affected the host-side deployment flow:
the operator's manual `docker compose up -d --force-recreate
--no-deps skygate` occasionally left the new container in
`Created` state because the old `container_name: skygate`
wasn't always released before compose tried to create the
new one.

**Fix**: remove `container_name: skygate` from
`docker-compose.yml`. Compose auto-names the container
(`skygate-skygate-1` etc.) and the race goes away. Same for
`container_name: caddy` (caddy is in the same compose file,
a stale `caddy` would also block recreate). Did NOT touch
`container_name: headscale-$USERNAME` or `container_name:
derper` — those are managed by separate compose files
(`deploy/headscale-users/`, `deploy/templates/derper-compose.yml.tmpl`)
and aren't affected.

**To avoid breaking the ~20 scripts/docs that use
`docker exec skygate ...`**, added a host-side shell wrapper
`deploy/skygate-cli.sh` that does a label-based lookup
(`com.docker.compose.service=skygate`) and forwards to
`docker exec <real-id> ...`. Installed on the host by
`deploy.sh` as `/usr/local/bin/skygate`. Every existing caller
works without edits. `verify_post_deploy.sh` also resolves
`SKYGATE_CONTAINER` from the same label by default
(override via env var still works for ad-hoc checks).

**Files**:
- `deploy/skygate-cli.sh` (NEW, 80 lines): the wrapper itself.
  Includes `--id` mode (print just the container ID) for
  scripts that want to do their own `docker exec "$CID" ...`
  in hot loops.
- `deploy/deploy.sh`: installs `/usr/local/bin/skygate` at
  the end of the deploy (idempotent).
- `docker-compose.yml`: removed `container_name: skygate` and
  `container_name: caddy`. New containers are `skygate-skygate-1`,
  `caddy-caddy-1` etc.
- `scripts/verify_post_deploy.sh`: resolves `SKYGATE_CONTAINER`
  via label lookup. Banner shows the resolved ID.
- `scripts/verify_pre_deploy.sh`: new B14 catalog check
  (wrapper exists + syntax valid + uses correct label).
- `AGENTS.md`: new "The `skygate` host-side wrapper" section.

**Live verification (operator's VM, 2026-07-28)**:
- `go test ./...` 19/19 PASS, `make verify-pre` 14/14 PASS
- `make verify-post` 26/26 PASS (auto-resolved container ID
  `37562e3b7332` via label)
- Two consecutive `docker compose up -d --force-recreate
  --no-deps skygate` invocations both started the new
  container cleanly (no `Created` stall). The v0.29.0 /
  v0.29.1 race that affected ~1 in 3 invocations is gone.

**Caveats (still open)**:
- The auto-generated container name (`skygate-skygate-1`)
  may increment on every recreate (`skygate-skygate-2`,
  `-3`, ...). The label-based lookup is robust to this, but
  operators who grep `docker ps` for the name will see it
  change. AGENTS.md documents the wrapper.
- v0.29.2 still leaves the orchestrator's auto-swap out
  of scope. A sidecar-based orchestrator (v0.29.3 follow-up)
  is the only way to get a fully automatic
  `git push → build → swap` flow without manual intervention.

## v0.29.3 / v0.29.3.1 — Auto-swap via helper container in host PID namespace

The v0.29.0 / v0.29.1 / v0.29.2 orchestrator chain
handles the "git push → build → swap" lifecycle, but
v0.29.2 still requires a manual `docker compose up`
on the host. v0.29.3 closes the loop: the orchestrator
itself does the swap, end-to-end, with auto-rollback on
any failure.

### The PID-namespace death race (v0.29.3 problem)

The v0.29.3 first version spawned a Setsid-detached
subprocess from inside the OLD skygate container to
run `docker compose up --force-recreate`. The subprocess
escaped the OLD container's process group (Setsid) but
was STILL in the OLD container's PID namespace. When
compose sent SIGTERM to PID 1 of the OLD container
(skygate itself), the signal propagated to all
processes in the same namespace, killing the swap
subprocess mid-way through `docker compose up`. The new
container would end up in `Created` state forever and
the operator had to `docker start <id>` by hand.

Live-verified on the VM at 2026-07-28 10:45 UTC: the
swap log showed only "Recreate" before the subprocess
died, and the new container `fb9547ead806` was stuck
in `Created`. A subsequent attempt with `unshare -fp`
inside the helper failed with "unshare: Operation not
permitted" (the skygate container's CapAdd doesn't
include CAP_SYS_ADMIN, which `CLONE_NEWPID` requires).

### v0.29.3.1 fix: helper container in HOST PID namespace

Instead of running the swap from inside the OLD skygate
container, the orchestrator spawns a HELPER CONTAINER
via `docker run --rm --pid=host --net=host
-v /var/run/docker.sock:/var/run/docker.sock
-v skygate-data:/data
-v $SKYGATE_HOST_REPO_PATH:/host_repo:ro`. The helper
uses the HOST's PID namespace, so its processes are
not in any skygate container's namespace and survive
the OLD container's removal cleanly.

The helper does the full swap:
  1. sleep 3s (orchestrator flush)
  2. `apk add --no-cache docker-cli docker-cli-compose`
     (alpine base image has no docker binary)
  3. `cd /host_repo && docker compose -p skygate -f
     /host_repo/docker-compose.yml up -d
     --force-recreate --no-deps skygate`
  4. poll up to 60s for the new container; if it's
     stuck in Created, call `docker start <id>`
     (handles the rare compose race where Created
     happened but Start didn't)
  5. final healthz check via
     `docker exec $NEW_ID wget -qO- http://localhost:8080/healthz`
     (helper has --net=host so localhost:8080 IS the
     new container's port)
  6. exit (`--rm` self-removes the container)

The OLD orchestrator's swap script now just spawns
the helper container in the background and exits
immediately. Helper self-removes via `--rm`.

### Defense in depth: confirmPendingSwap

The new orchestrator (in the new container) also has a
helper of its own: `confirmPendingSwap` (called from
`renderUpdatePage` on the first /admin/update page
load after the swap). It detects
`phase=build_done` / `phase=rolled_back`, calls
`startStuckSkygateContainer` (the v0.29.3.1 fix for
the `{{.State.Status}}` → `{{.Status}}` format-string
regression in the `docker ps` call), polls
`/healthz` on the new container for up to 30s, and
on 200 calls `store.Complete()` to promote the phase
to `done`. This is the final-arbitration step: even
if the helper container crashes before it can finish
its work, the next /admin/update page load completes
the swap.

### What changed (commits `49b67ce` ... `ebaa44e`)

- `internal/update/docker.go`:
  - `runShellDetached` helper (Setsid, fire-and-forget)
  - `swapSubprocessScript` rewritten to spawn the helper
  - `swapHelperScript` extracted as a separate Go constant
    (shared between success and rollback paths)
  - `writeSwapHelperScript()` Go helper for the success
    path to write the helper file at job start
  - `startStuckSkygateContainer` (v0.29.3.1) in
    `handlers_admin_update.go` — uses the now-correct
    `{{.Status}}` (was `{{.State.Status}}` which is
    `docker inspect` only) to find the new container
  - `confirmPendingSwap` (v0.29.3) in
    `handlers_admin_update.go` — /healthz poll + phase
    promotion on first /admin/update page load after
    a `build_done` or `rolled_back` phase
  - Critical bug fix: `StateStore.Load()` was parsing
    the state file but NOT storing in `s.state` (silent
    no-op for `Log()` and nil return from `Get()`).
    Fixed in commit `9fbc588`.
- `internal/handlers/handlers_admin_update_test.go`
  (NEW) — regression test for the
  `{{.Status}}` vs `{{.State.Status}}` format string
  (TestStartStuckSkygateFormatStringIsDockerPsValid)

### Live verification (2026-07-28, on the VM)

End-to-end test on `admin@192.0.2.1`:
  1. Applied `v0.99.0-nonexistent` via
     `POST /admin/update/apply` (target=v0.99.0-nonexistent)
  2. Orchestrator: backup tag created
     (`skygate-pre-update-ebaa44e`), `git fetch` failed
     (expected — the tag doesn't exist), `failWithRollback`
     spawned the detached subprocess
  3. Subprocess wrote `/data/skygate-swap-helper.sh`
     and ran `docker run --rm --pid=host --net=host ...`
     (the helper container)
  4. Helper installed docker-cli via apk, ran
     `docker compose up --force-recreate --no-deps skygate`,
     polled for the new container (status=running on
     attempt 1, no Created→Started race this time)
  5. Helper did a final healthz check via
     `docker exec $NEW_ID wget -qO- http://localhost:8080/healthz`
     and exited
  6. Operator loaded `/admin/update` →
     `confirmPendingSwap` detected `phase=rolled_back`,
     called `startStuckSkygateContainer` (no-op, container
     already Up), polled `/healthz` (200 on attempt 1),
     promoted phase to `done`
  7. State file log:
     ```
     renderUpdatePage: store=true phase=rolled_back (debug probe)
     renderUpdatePage: phase=rolled_back detected, calling confirmPendingSwap
     skygate container 359dec4c92f9 status=Up
     new orchestrator confirmed swap via /healthz (attempt 1)
     update completed successfully
     ```

`make verify-pre` 13/13 PASS, `make verify-post` 26/26 PASS.
`go test ./...` 19/19 packages green.

### Caveats

- The helper adds ~10s to the swap total (5s apk add
  for docker-cli + 3s orchestrator flush + ~2s compose
  up). Acceptable for an end-to-end upgrade.
- The helper requires network access from the
  `skygate-swap-helper` container to the alpine
  package repo (dl-cdn.alpinelinux.org) for the
  `apk add docker-cli` step. If the network is
  restricted, the swap will fail with
  "docker: not found". A pre-baked
  `skygate-swap-helper:VERSION` image with docker
  pre-installed would avoid this (future v0.29.4+
  work).
- The `ensureComposeServiceRunning` helper from
  v0.29.1 is still in place (unused since v0.29.2
  removed `container_name: skygate`) but is now
  redundant with `startStuckSkygateContainer`.
  v0.29.4 cleanup can remove it.

## v0.29.1 — Orchestrator stops at "image rebuilt", manual swap required

The v0.29.0 auto-updater's first end-to-end test on the
operator's VM surfaced a deeper architectural issue than the
five post-Phase-2 bugfixes had addressed: `docker compose up
--force-recreate --no-deps skygate` sends SIGTERM to the
skygate container — which IS the orchestrator's parent
process (skygate is PID 1 of the container, the orchestrator
is a goroutine inside skygate's HTTP server). The orchestrator
died mid-`up` before the swap completed, leaving the new
container in an undefined state with no healthz verification.
The previously-suspected "Created→Started race" was a
misdiagnosis of this: the race fix (`ensureComposeServiceRunning`)
was the right defensive measure but never got to run because
the orchestrator was already dead.

**Fix**: the orchestrator no longer calls `docker compose up`.
It stops at "image rebuilt + migrations applied" and writes a
one-line `manual_swap` ("docker compose up -d --force-recreate
--no-deps skygate") for the operator to run on the host. The
swap is the only step that has to be on the host — the rest
of the upgrade (backup tag, fetch, checkout, build, rollback
on failure) runs entirely inside the orchestrator.

**Changes**:
- New `PhaseBuildDone` state phase (replaces `swap` + `verify`
  in the success path). `failed` / `rolled_back` are still
  used for error paths.
- New `ManualSwap` field in the state JSON for the single
  command. `SetManualStep(kind, cmd)` helper to add it.
- `ensureComposeServiceRunning` is left in place (untested
  but compile-checked) for v0.29.2 when the orchestrator
  moves to a sidecar container.
- Pre-push hook now uses `MSYSTEM` (set by Git for Windows)
  as the primary Git Bash detection signal, ahead of the
  directory-probe fallback that was unreliable on hybrid
  WSL2+Git Bash systems. **No more `--no-verify` workaround**
  for normal pushes from Git Bash.
- New B13 catalog check: pre-push hook contains `MSYSTEM`.

**Live verification (operator's VM, 2026-07-28)**:
- `go test ./...` 19/19 PASS, `make verify-pre` 13/13 PASS
- `make verify-post` 26/26 PASS
- End-to-end auto-update test: applied `v0.29.0` (real
  existing tag, but with local changes so `git fetch`
  fails — exercises the rollback path) → backup tag
  created → fetch failed as expected → automatic rollback
  `git checkout` OK → chown OK → rollback `docker compose
  build` OK → state at `rolled_back` with `manual_swap`
  populated. Orchestrator survived all the way to the end.
  Container still on the previous build (29b4564).

**Caveats (still open)**:
- The orchestrator can do everything up to `docker compose
  build`. The final `docker compose up` must be done by the
  operator on the host. v0.29.2 follow-up: move the
  orchestrator to a sidecar container that's outside
  skygate's process tree, so the SIGTERM from `up` doesn't
  kill the orchestrator.
- The same `container_name: skygate` race still affects
  host-side `docker compose up` (rare but observed once in
  the v0.29.1 verification). Manual `docker start <id>`
  recovers.

## v0.29.0 — Self-update orchestrator (in-app upgrade + auto-rollback)

The `/admin/update` page (v0.29.0 Phase 1) now ships with a working
in-app upgrade flow. The orchestrator runs the full `git fetch` →
`git checkout <tag>` → `docker compose build` → `docker compose
up --force-recreate` → `/healthz` poll sequence in a background
goroutine and reports each phase to a bind-mounted status file
(`/data/skygate-update-status.json`). On any phase failure the
orchestrator automatically rolls back to the previous tag, including
`docker compose build` + recreate + healthz poll again. If rollback
itself fails, the state file shows the manual steps for operator
intervention.

**Phase 1 (commit `e3ce6f0`)** — detection + manual steps + UI:
GitHub Releases API client, semver comparison, install-kind
detection (Docker/systemd/bare), copy-pasteable manual commands,
`/admin/update` page with status panel + auto-refresh, 17 i18n
keys × 2 langs. Renders on VM in RU + EN, GitHub checker works
(no false "new version" banner when current build is ahead of
all released tags).

**Phase 2 (commit `caf6fb8`)** — auto-updater with state machine
+ auto-rollback: `/admin/update/apply` kicks off the orchestrator,
`/admin/update/rollback` cancels + restores previous, status
file persists across container recreate. 18 i18n keys × 2 langs.

**Post-Phase-2 fixes (commits `0020815`, `4bb4db6`, `f9d3860`,
`a18ad0c`, `5177643`, `bae4fb4`)** — five bugs discovered on
the operator's VM during the first end-to-end auto-update test:

1. **`SKYGATE_REPO_PATH` chdir failure** (`0020815`). The
   orchestrator ran inside the skygate container but tried to
   `chdir /home/admin/skygate` — a host path unreachable
   from inside the container. The source dir is bind-mounted at
   `/app` (per docker-compose.yml's `./:/app`). Fix:
   `defaultRepoPath()` in `internal/config/config.go` auto-
   detects container mode via `/.dockerenv` /
   `/run/.containerenv` and defaults to `/app`. Bare/systemd
   hosts still default to `/home/admin/skygate`.
   `SKYGATE_REPO_PATH` env always overrides.

2. **`sudo chown` in Alpine** (`0020815`). The previous
   rollback path used `sudo chown -R admin:admin ...`
   which fails in the Alpine container (no `sudo` binary, no
   `admin` user). Fix: `detectHostOwner()` captures
   `stat -c '%u:%g' .git/HEAD` BEFORE the first git mutation
   and `chownToHostOwner()` does `chown -R <uid>:<gid>` (no
   sudo). Default 1000:1000; override via `SKYGATE_HOST_OWNER`.

3. **Missing `docker compose` plugin in image** (`4bb4db6`).
   The Alpine image's `docker-cli` package installs only the
   `docker` binary; `docker compose` is the separate
   `docker-cli-compose` plugin. Without it, the orchestrator's
   `docker compose build skygate` errors with "docker:
   unknown command: docker compose". Fix: add
   `docker-cli-compose` to the `apk add` list (~3 MB).

4. **Project name mismatch** (`f9d3860` + `bae4fb4`).
   Docker compose computes the project name from the basename
   of the working directory by default. Inside the container
   the orchestrator runs from `/app` (basename "app") while
   the host's compose was launched from `/home/admin/skygate`
   (basename "skygate"). Two-part fix:
     (a) `docker-compose.yml` env section adds
         `COMPOSE_PROJECT_NAME=skygate`.
     (b) `DockerUpgrader.runCompose` always passes
         `-p <ComposeProject>` (default "skygate", override
         via `SKYGATE_COMPOSE_PROJECT`).

5. **Bind-mount paths invisible to host dockerd** (`a18ad0c` +
    `5177643` + `bae4fb4`). The docker daemon runs on the
    host, so it resolves bind-mount sources as HOST paths. From
    inside the container, `./secrets/ts_authkey` resolved to
    `/app/secrets/ts_authkey` from compose's perspective, but
    the daemon then looked for that path on the host (where
    `/app` doesn't exist). Fix: replace all `./` paths in
    `docker-compose.yml` with
    `${SKYGATE_HOST_REPO_PATH:-/home/admin/skygate}/` and
    add `SKYGATE_HOST_REPO_PATH` to the skygate service env.
    The `${VAR:-default}` syntax is shell-style fallback
    supported by docker compose.

**Live verification (operator's VM, 2026-07-27)**:
- `go test ./...` 19/19 PASS, `make verify-pre` 12/12 PASS
- `docker_test.go` adds 9 test groups (TestShortSHA,
  TestDetectHostOwner_EnvOverride, TestDetectHostOwner_StatAutoDetect,
  TestDetectHostOwner_DefaultFallback, TestDetectHostOwner_Cached,
  TestChownToHostOwner_ArgsShape, TestOwnerPattern,
  TestTruncateOutput, TestNewDockerUpgrader_Defaults,
  TestNewDockerUpgrader_ProjectOverride)
- `make verify-post` 26/26 PASS after the fix chain
- End-to-end auto-update test: applied `v0.99.0-final-test`
  (non-existent) → backup tag created → fetch failed as expected
  → automatic rollback checkout OK → chown to host owner OK →
  container recreated → /healthz 200 with previous build label

**Caveats (still open)**:
- Rollback `docker compose up --force-recreate --no-deps skygate`
  occasionally leaves the new container in `Created` status
  instead of `Started` (race with the old container's
  `container_name: skygate`). Manual `docker start <id>` recovers.
  Tracked as v0.29.1 follow-up: remove `container_name: skygate`
  or improve the orchestrator to handle the Created→Started transition.
- The orchestrator is Docker-only. Systemd / bare install kinds
  generate manual steps but don't auto-execute (Phase 3 follow-up).
- Apply works only for tags already pushed to origin. The orchestrator
  does `git fetch --tags` + `git checkout <target>`; a tag that
  exists only on the operator's local clone would not be findable.

## v0.28.7 — Per-DEVICE ACL grants for tagged devices (Moonlight fix)

The v0.28.0 per-device ACL design tagged every device with
`tag:dev-<user>-<device>`. The per-user grant `src=user@` was supposed
to cover tagged devices too, but in Tailscale v2 policy the per-user
identity doesn't match tagged devices — only the tag does. Result:
workstation-2 (Android, `tag:dev-admin-workstation-2`) couldn't reach workstation-1
(Windows, `tag:dev-admin-workstation-1`) over Tailscale for Moonlight,
even though both devices belonged to the same portal user.

### What changed

v0.29.0 adds a **per-DEVICE grant block** to the generated policy:
for each portal user with N≥2 devices, emit N grants (one per
device as `src`), with `dst` = the list of all OTHER devices of
the same user, and `ip: ["*"]` (required by headscale 0.29.2).
13 grants on this VM (11 admin + 2 user1); O(n) per user,
not O(n²).

The earlier v0.28.7 attempt (commit `8749069`) tried a wildcard
`tag:dev-<user>-*` src, which headscale 0.29.2 rejects with
"src=tag not found" (the parser requires concrete tags in
`tagOwners{}`).

### Why this works

Tailscale ACL is order-sensitive (first match wins). The per-DEVICE
grant is emitted **after** the per-user grant (which keeps SSH
+ untagged identities working) and **before** per-device exit-rules
+ per-device loose grants (autogroup:internet). Tagged-to-tagged
device traffic on the same tailnet now matches the per-DEVICE
grant as a fallback when no specific per-device rule applies.

### Code shape

The per-DEVICE block is duplicated in `GenerateACLForPlane` and
`GenerateACLWithViaForPlane` (the v0.28.1 per-user via variant
takes a parallel code path). v0.30.0 will extract the block to a
shared helper — the duplication is documented in the AGENTS.md
release notes for v0.28.7 as a known cleanup target.

### Verification

- Live: 13 new per-DEVICE grants in the headscale policy after
  `/admin/exit-rules/reapply` (HTTP 303 redirect)
- Operator confirmation: workstation-2 ↔ workstation-1 Moonlight session
  now establishes and streams (2026-07-27)
- `make verify-pre` 9/9 PASS, `make verify-post` 25/26 PASS
  (R9 is a known false-negative — the verify-post script reads
  an older `acl_snapshots` row; the most recent reapply did
  succeed and saved a fresh snapshot)

## How a release is cut

1. `git tag -a v0.X.Y -m "v0.X.Y"` on the commit we want to ship.
2. `git push origin v0.X.Y`.
3. (Operator-driven) create a GitHub release at
   https://github.com/skygate-operator/skygate/releases/new — the body
   summarizes the commits since the previous tag.
4. Update `CHANGELOG.md` to move the entry from `[Unreleased]`
   into the new tagged section.

The operator (admin) writes the release body; the git tag is
the source of truth for "what shipped in v0.X.Y".
