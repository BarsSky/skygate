# Skygate — Headscale Management Portal

Tailscale/headscale management portal with exit node rules, split-tunnel routing,
device management, and ACL automation.

## Supported Platforms

| Platform | Docker | Deploy system | Notes |
|---|---|---|---|
| **Linux** | `docker` | Full | Primary target |
| **Windows** | `docker.exe` (Docker Desktop) | Full | Git Bash required |
| **macOS** | `docker` | Full | Untested, should work |

## Architecture

```
NPM Proxy -> Skygate (Go 1.23, SQLite) -> Headscale v0.29 API
                  |
           Headplane UI
           DERP relay (optional)
```

## Quick Start

### 1. Clone & configure

```bash
git clone <repo> /home/skyadmin/skygate
cd /home/skyadmin/skygate
cp .env.example .env
nano .env   # fill in all values
```

### 2. Deploy (fresh install)

```bash
./deploy/deploy.sh
```

### 3. Deploy (from backup)

```bash
./deploy/deploy.sh --from-path /path/to/backup-dir
```

### 4. Backup

```bash
./deploy/backup.sh
```

### 5. Validate

```bash
./deploy/validate.sh
```

## Windows Deployment

### Prerequisites

- Docker Desktop installed and running
- Git Bash (included with Git for Windows)
- Python 3 (for template rendering)

### Path conventions

Git Bash maps Windows paths to Unix-style:
- `C:\Users\knaga` -> `/mnt/c/Users/knaga`
- `C:\Projects` -> `/mnt/c/Projects`

The deploy system auto-detects Windows via `uname -s` (MINGW*/MSYS*),
sets `DOCKER_CMD=docker.exe`, and uses `$USERPROFILE` for paths.

### Docker on Windows

The deploy system uses `${DOCKER_CMD}` which is:
- `docker` on Linux/macOS
- `docker.exe` on Windows (Git Bash)

Docker Desktop must be running before deploy. Verify:
```powershell
docker.exe ps
```

### Example .env for Windows

```bash
# Paths using forward slashes (Git Bash compatible)
DEPLOY_HEADSCALE_DIR=/mnt/c/Projects/skygate-test/headscale
DEPLOY_SKYGATE_DIR=/mnt/c/Projects/skygate-test/skygate
SSH_DIR=/c/Users/knaga/.ssh
```

## Platform Detection

The `deploy/lib/env.sh` script detects the OS at runtime:

```bash
_KERNEL="$(uname -s)"
case "${_KERNEL}" in
    MINGW*|MSYS*|CYGWIN*)   SKYGATE_OS=windows; DOCKER_CMD=docker.exe ;;
    Linux)                   SKYGATE_OS=linux;   DOCKER_CMD=docker ;;
    Darwin)                  SKYGATE_OS=macos;   DOCKER_CMD=docker ;;
esac
```

All deploy scripts use `${DOCKER_CMD}` for Docker calls and guard `chmod`/`chown`
with `${SKYGATE_OS}` checks (no-op on Windows).

## Configuration (.env)

All secrets and deployment paths live in a single `.env` file.
See `.env.example` for the full template.

| Component | Variables |
|---|---|
| Skygate | SKYGATE_* |
| Headscale | HEADSCALE_URL, HEADSCALE_API_KEY, HEADSCALE_* |
| Headplane | HEADPLANE_* (nested with __) |
| Exit nodes | SKYGATE_EXIT_SSH* |
| DERP | DERP_* |
| Deployment | DEPLOY_*, DOCKER_* |

## Backup Contents

| Artifact | Source |
|---|---|
| skygate.db | Docker volume (after WAL checkpoint) |
| headscale-db.sqlite | Docker volume (after WAL checkpoint) |
| headscale-config/ | YAML config + noise_private.key |
| headplane-config.yaml | Static config (no secrets) |
| headplane-data/ | Volume dump |
| ssh/ | Exit node sync keys |
| skygate-repo.bundle | Git bundle |
| docker-images/ | docker save of all images |
| .env | All secrets & configuration |

## Post-deploy

- Configure NPM reverse proxy for external access
- Add DNS records for skygate.<domain>, head.<domain>
- Set up Tailscale clients with --login-server
- Verify exit node route sync

## Pitfalls

- **NPM caches HTML** — add `?t=` to URL after template deploy
- **Windows Docker** — must be Docker Desktop (not WSL-only docker)
- **headscale policy.mode** — must be `database` for API ACL
- **Windows file permissions** — chmod/chown are skipped on Windows
- **Git Bash paths** — use forward slashes, `/mnt/c/...` prefix
