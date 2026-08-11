#!/bin/bash
# scripts/check_b91.sh — invoked by verify_pre_deploy.sh B91 check.
#
# Why a separate file: the B91 check has too many nested quotes
# (single + double + backticks in the comments) to safely inline
# in verify_pre_deploy.sh's run_check helper, which builds the
# command via printf "%s" "...". The backticks in inline comments
# (e.g. `docker compose build`) get interpreted as command
# substitution by bash -c, even inside double-quoted printf args,
# which silently truncates the cmd. A dedicated shell script
# avoids all of that.
#
# Pinned contracts:
#   - entrypoint.sh has the v0.33.1.39 pre-flight wait
#   - docker-compose.yml has restart: unless-stopped + healthcheck
#   - docker-compose.yml has entrypoint: /app/entrypoint.sh
#     override (so the bind-mount is what runs, not the image's
#     baked-in /entrypoint.sh)
#   - docker-compose.yml does NOT have skygate → headscale
#     depends_on (loose coupling by design)
#   - bash -n entrypoint.sh passes (syntax check)

set -e

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

# 1. entrypoint.sh has the pre-flight wait
grep -qF "v0.33.1.39" entrypoint.sh || { echo "SKY-FAIL: entrypoint.sh missing v0.33.1.39 marker" >&2; exit 1; }
grep -qF "HEADSCALE_URL" entrypoint.sh || { echo "SKY-FAIL: entrypoint.sh missing HEADSCALE_URL" >&2; exit 1; }
grep -qF "pre-flight" entrypoint.sh || { echo "SKY-FAIL: entrypoint.sh missing pre-flight marker" >&2; exit 1; }
grep -qF "/health" entrypoint.sh || { echo "SKY-FAIL: entrypoint.sh missing /health endpoint" >&2; exit 1; }
grep -qF "B91" entrypoint.sh || { echo "SKY-FAIL: entrypoint.sh missing B91 marker" >&2; exit 1; }

# 2. docker-compose.yml has the right shape
grep -qF "v0.33.1.39" docker-compose.yml || { echo "SKY-FAIL: docker-compose.yml missing v0.33.1.39 marker" >&2; exit 1; }
grep -qF "B91" docker-compose.yml || { echo "SKY-FAIL: docker-compose.yml missing B91 marker" >&2; exit 1; }
grep -qF "restart: unless-stopped" docker-compose.yml || { echo "SKY-FAIL: docker-compose.yml missing restart: unless-stopped" >&2; exit 1; }
grep -qF "healthcheck:" docker-compose.yml || { echo "SKY-FAIL: docker-compose.yml missing healthcheck:" >&2; exit 1; }
grep -qF "entrypoint: /app/entrypoint.sh" docker-compose.yml || { echo "SKY-FAIL: docker-compose.yml missing 'entrypoint: /app/entrypoint.sh' override" >&2; exit 1; }

# 3. Loose coupling: skygate MUST NOT depend on headscale/headplane.
#    caddy has depends_on: - skygate (caddy waits for skygate, not
#    the other way). The check uses PyYAML to parse the actual
#    structured config (not grep matching comment text).
test -f scripts/check_skygate_depends_on.py || { echo "SKY-FAIL: scripts/check_skygate_depends_on.py missing" >&2; exit 1; }
# Resolve Python robustly: `python3` may resolve to the WindowsApps
# MS-Store stub on Windows hosts. The same candidates the B91 + the
# rest of verify_pre_deploy.sh use for `go` apply here for `python3`.
PYTHON=""
# On Windows hosts, `python3` in PATH often resolves to the
# WindowsApps MS-Store stub at
# C:\Users\<user>\AppData\Local\Microsoft\WindowsApps\python3.exe
# which prints "Python was not found; run without arguments to
# install from the Microsoft Store" and exits. We MUST skip
# that stub and use a real Python install.
if command -v python3 >/dev/null 2>&1; then
    candidate=$(command -v python3)
    case "$candidate" in
        *WindowsApps*) ;;  # Skip the WindowsApps stub
        *) PYTHON="$candidate" ;;
    esac
fi
if [ -z "$PYTHON" ]; then
    for cand in /c/Python314/python.exe /c/Python313/python.exe /c/Python312/python.exe /c/Python311/python.exe /usr/bin/python3 /usr/local/bin/python3; do
        if [ -x "$cand" ]; then PYTHON="$cand"; break; fi
    done
fi
if [ -z "$PYTHON" ]; then
    echo "SKY-FAIL: python3 not found (WindowsApps stub doesn't count)" >&2
    exit 1
fi
"$PYTHON" scripts/check_skygate_depends_on.py

# 4. entrypoint.sh is syntactically valid
bash -n entrypoint.sh || { echo "SKY-FAIL: entrypoint.sh syntax check failed" >&2; exit 1; }

echo "B91 check passed: skygate starts independently of headscale/headplane after VM reboot"
