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
