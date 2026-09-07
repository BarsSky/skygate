# install-common.sh — V4 (B237.13) shared helpers for the
# per-OS install scripts (install-debian.sh, install-rhel.sh,
# install-alpine.sh). Sourced (not executed) by each per-OS
# script. The contract is:
#
#   - Per-OS scripts source this file at the top:
#         . "$(dirname "$0")/install-common.sh"
#   - All shared env vars (SKYGATE_VERSION, SKYGATE_DATA_DIR, etc.)
#     are read from the environment (set by install.sh's `export`,
#     or directly by the operator running the per-OS script
#     standalone with SKYGATE_VERSION=v1.5.0 ./install-debian.sh).
#   - The single public entry point is `do_install`. The per-OS
#     script's `main` function is a wrapper that installs the
#     OS-specific deps + service manager, then calls do_install.
#
# This split exists because the install sequence has 3 phases:
#   (1) OS-specific deps + service-manager init (apt/dnf/apk +
#       systemd/OpenRC enable). DIFFERENT per OS.
#   (2) Download + verify + extract the tarball. IDENTICAL per OS.
#   (3) Install binary + config + user + enable service. Most of
#       this is identical; the service-manager bits (systemd unit
#       vs OpenRC script) are OS-specific.
#
# We split (1) and (3)'s service-manager bits into the per-OS
# script; (2) and the rest of (3) live here in install-common.

# -------- shared helpers (sourced) --------

# resolve_release_url: turn "latest" or "vX.Y.Z" into the actual
# GitHub release tarball URL for the current arch. Uses the GitHub
# API for "latest" (resolves the tag + redirects) and the public
# releases/download endpoint for explicit tags. The response is
# the URL to curl.
#
# Args:
#   $1 = "latest" or "vX.Y.Z" (a tag)
#   $2 = "linux-amd64" / "linux-arm64" / "darwin-amd64" (target triple)
# Echoes:
#   Two lines: the tarball URL, the SHA256SUMS URL
#   (separated by \n; caller reads with `read -r TARBALL SHA256SUMS`)
#
# Why this is in install-common.sh and not in the per-OS scripts:
# the GitHub API URL + the asset name format are the SAME across
# all distros. Only the package manager (apt/dnf/apk) and the
# service manager (systemd/OpenRC) differ per OS.
resolve_release_url() {
    local version="$1"
    local triple="$2"
    local owner="$GITHUB_OWNER"
    local repo="$GITHUB_REPO"
    local api_url
    local tag
    if [ "$version" = "latest" ]; then
        api_url="https://api.github.com/repos/${owner}/${repo}/releases/latest"
        tag=$(curl -fsSL "$api_url" | sed -n 's/^[[:space:]]*"tag_name":[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)
        if [ -z "$tag" ]; then
            echo "ERROR: could not resolve 'latest' release tag from $api_url" >&2
            return 1
        fi
    else
        tag="$version"
    fi
    # Asset naming convention matches .github/workflows/release.yml:
    #   skygate-v1.5.0-linux-amd64.tar.gz
    #   skygate-v1.5.0-linux-arm64.tar.gz
    #   skygate-v1.5.0-darwin-amd64.tar.gz
    local asset_base="skygate-${tag}-${triple}.tar.gz"
    echo "https://github.com/${owner}/${repo}/releases/download/${tag}/${asset_base}"
    echo "https://github.com/${owner}/${repo}/releases/download/${tag}/SHA256SUMS"
}

# download_and_verify: curl the tarball + SHA256SUMS, verify the
# tarball's hash against SHA256SUMS, extract to a temp dir, then
# copy the binary to the install path. Sets the global BINARY_PATH
# to the extracted binary (so the caller can chmod + install it).
#
# Args:
#   $1 = tarball URL
#   $2 = SHA256SUMS URL
#   $3 = install dir for the binary (e.g. /usr/local/bin)
#   $4 = "1" to skip verification (air-gapped), "0" otherwise
download_and_verify() {
    local tarball_url="$1"
    local sums_url="$2"
    local install_dir="$3"
    local skip_verify="$4"

    local work
    work="$(mktemp -d -t skygate-install.XXXXXX)"
    trap "rm -rf '$work'" EXIT

    echo "[install] downloading $tarball_url"
    if ! curl -fsSL --retry 3 --retry-delay 2 -o "$work/skygate.tar.gz" "$tarball_url"; then
        echo "ERROR: failed to download tarball from $tarball_url" >&2
        echo "Check SKYGATE_VERSION (set to 'latest' or 'vX.Y.Z') and" >&2
        echo "your internet connectivity. For air-gapped installs, set" >&2
        echo "SKYGATE_SKIP_VERIFY=1 and pre-stage the tarball." >&2
        return 1
    fi
    echo "[install] downloading $sums_url"
    if ! curl -fsSL --retry 3 --retry-delay 2 -o "$work/SHA256SUMS" "$sums_url"; then
        echo "ERROR: failed to download SHA256SUMS from $sums_url" >&2
        return 1
    fi

    if [ "$skip_verify" != "1" ]; then
        echo "[install] verifying SHA256"
        # SHA256SUMS contains lines like:
        #   <hex>  skygate-v1.5.0-linux-amd64.tar.gz
        # The tarball was saved as "$work/skygate.tar.gz" (a
        # different name), so we need to:
        #   (a) compute the sha256 of the downloaded file
        #   (b) look up the EXPECTED sha256 in SHA256SUMS by the
        #       asset basename (derived from the URL)
        local asset_name
        asset_name="$(basename "$tarball_url")"
        local expected_sha
        expected_sha="$(awk -v n="$asset_name" '$2 == n { print $1; exit }' "$work/SHA256SUMS")"
        if [ -z "$expected_sha" ]; then
            echo "ERROR: asset '$asset_name' not found in SHA256SUMS" >&2
            return 1
        fi
        local actual_sha
        actual_sha="$(sha256sum "$work/skygate.tar.gz" | awk '{ print $1 }')"
        if [ "$expected_sha" != "$actual_sha" ]; then
            echo "ERROR: SHA256 mismatch" >&2
            echo "  expected: $expected_sha" >&2
            echo "  actual:   $actual_sha" >&2
            echo "Refusing to install — the tarball may be corrupted or" >&2
            echo "tampered with. Re-download, or set SKYGATE_SKIP_VERIFY=1" >&2
            echo "ONLY if you've verified the file out-of-band." >&2
            return 1
        fi
        echo "[install] SHA256 OK"
    else
        echo "[install] WARNING: SKYGATE_SKIP_VERIFY=1, skipping hash check"
    fi

    echo "[install] extracting"
    # The tarball layout is FLAT: skygate (binary) + skygate.sha256
    # at the root (not inside a subdir). This is so the install
    # scripts can `tar -xzf ... && install ./skygate ...`.
    if ! tar -xzf "$work/skygate.tar.gz" -C "$work"; then
        echo "ERROR: failed to extract tarball" >&2
        return 1
    fi
    if [ ! -f "$work/skygate" ]; then
        echo "ERROR: tarball did not contain 'skygate' binary at root" >&2
        return 1
    fi

    # Install: copy + chmod. Use install(1) for atomic replace
    # (it does cp + chmod + chown in one syscall).
    install -m 0755 "$work/skygate" "${install_dir}/skygate"
    echo "[install] installed: ${install_dir}/skygate ($(stat -c '%s' "${install_dir}/skygate") bytes)"
}

# detect_arch: turn `uname -m` into the GitHub release triple
# suffix. Returns "amd64" for x86_64, "arm64" for aarch64, etc.
# Falls back to amd64 (most common server arch) if uname returns
# something we don't recognize — the download will fail loudly
# in that case, which is the right behavior.
detect_arch() {
    local m
    m="$(uname -m)"
    case "$m" in
        x86_64|amd64)   echo "amd64" ;;
        aarch64|arm64)  echo "arm64" ;;
        *)
            echo "WARN: unrecognized arch '$m', defaulting to amd64" >&2
            echo "amd64"
            ;;
    esac
}

# write_env_file: write the starter /etc/skygate/skygate.env.
# The operator fills in HEADSCALE_URL, HEADSCALE_API_KEY, and
# SKYGATE_JWT_SECRET before starting the service. Other vars
# (SKYGATE_PORT, SKYGATE_USER) come from the unit file's
# EnvironmentFile= directive.
write_env_file() {
    local etc_dir="$1"
    local data_dir="$2"
    local port="$3"
    local user="$4"
    local env_file="${etc_dir}/skygate.env"

    if [ -f "$env_file" ]; then
        echo "[install] preserving existing $env_file (not overwriting)"
        return 0
    fi

    # Generate a starter JWT secret. 32 bytes hex = 64 chars
    # (matches SKYGATE_JWT_SECRET length expected by the Go
    # code: any length is fine, but 32+ bytes is recommended).
    local jwt_secret
    jwt_secret="$(head -c 32 /dev/urandom | xxd -p -c 64)"

    cat > "$env_file" <<EOF
# /etc/skygate/skygate.env — systemd EnvironmentFile for skygate.
#
# 2026-09-07 (V4 / B237.13): this file is written by install-{debian,rh,alpine}.sh
# on first install. Re-running the installer PRESERVES an existing
# file (the operator's HEADSCALE_URL + HEADSCALE_API_KEY are not
# clobbered). To reset, delete this file and re-run.
#
# After editing, run: sudo systemctl restart skygate

# === Required: fill in before starting skygate ===
# Where headscale is reachable from this host. Examples:
#   http://localhost:50444                  (headscale on the same host)
#   http://192.0.2.10:50444                 (headscale on a LAN peer)
#   http://100.64.0.1:50444                 (headscale on the Tailscale net)
#   https://headscale.example.com           (headscale behind a reverse proxy)
HEADSCALE_URL=

# API key for headscale (admin scope). Get it on the headscale host:
#   headscale apikeys create --expiration 365d
#   or:  docker exec headscale headscale apikeys create --expiration 365d
HEADSCALE_API_KEY=

# JWT signing secret for skygate's own session cookies. Already
# generated for you below — keep it secret, rotate periodically.
SKYGATE_JWT_SECRET=${jwt_secret}

# === Database ===
# Default: SQLite at \${SKYGATE_DATA_DIR}/skygate.db (single file,
# zero setup, suitable for single-host deploys).
# For HA: switch to PostgreSQL. The DSN format is
#   postgres://skygate:<password>@<host>:5432/skygate?sslmode=disable
# See docs/deploy.md §10 for the full HA setup.
SKYGATE_DB_DSN=

# === Optional: Tailscale in-container ===
# Set TS_AUTHKEY_FILE to /etc/skygate/ts_authkey (after writing
# the authkey file) to enable in-container tailscaled. Default:
# Tailscale is OFF (skygate uses direct internet).
TS_AUTHKEY_FILE=
EOF

    chmod 0640 "$env_file"
    chown "${user}:${user}" "$env_file" 2>/dev/null || true
    echo "[install] wrote $env_file (set HEADSCALE_URL + HEADSCALE_API_KEY before starting)"
}

# write_systemd_unit: write /etc/systemd/system/skygate.service.
# systemd-only — the alpine (OpenRC) script has its own.
#
# Design notes:
#   - Type=simple (skygate is a foreground HTTP server, not a
#     fork-and-daemonize process). The unit's PID 1 is skygate.
#   - Restart=on-failure with a 5s delay. The 5s delay gives
#     headscale a chance to finish booting on a host reboot
#     (skygate restarts before headscale, fails the headscale
#     healthz check, restarts again 5s later — usually up by
#     then). The in-container docker-compose path has the
#     B91 pre-flight wait for this; the systemd path doesn't
#     (a systemd unit shouldn't be in the business of waiting
#     for arbitrary other services).
#   - ProtectSystem=strict makes /usr + /etc + /boot read-only.
#     /var/lib/skygate + /etc/skygate are still writable
#     (via ReadWritePaths=).
#   - NoNewPrivileges + PrivateTmp + ProtectHome reduce the
#     blast radius if skygate is compromised.
#   - User=skygate (NOT root). The skygate binary refuses to
#     run as root (it's a security check in cmd/skygate/main.go).
#   - EnvironmentFile=/etc/skygate/skygate.env is the single
#     source of truth for runtime config. The unit file itself
#     is environment-free except for the data dirs.
write_systemd_unit() {
    local user="$1"
    local data_dir="$2"
    local etc_dir="$3"
    local unit_file="/etc/systemd/system/skygate.service"

    cat > "$unit_file" <<EOF
# /etc/systemd/system/skygate.service
# 2026-09-07 (V4 / B237.13): written by install-{debian,rh}.sh.
# Re-running the installer overwrites this file (intentional —
# the unit is project-owned, not operator-owned). The companion
# /etc/skygate/skygate.env is the operator's file and IS preserved.
[Unit]
Description=Skygate — self-service Tailscale/headscale portal
Documentation=https://github.com/${GITHUB_OWNER:-BarsSky}/${GITHUB_REPO:-skygate}
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=${user}
Group=${user}
EnvironmentFile=-${etc_dir}/skygate.env
ExecStart=${SKYGATE_BIN:-/usr/local/bin/skygate}
WorkingDirectory=${data_dir}
Restart=on-failure
RestartSec=5
# Cap the restart loop. If skygate dies 5 times in 60s, give
# up (the operator needs to look at /var/log/skygate).
StartLimitIntervalSec=60
StartLimitBurst=5
# Sandboxing
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=${data_dir} ${etc_dir}
# The skygate binary needs to bind 0.0.0.0:8080; it does NOT
# need any Linux capabilities (no NET_ADMIN, no SYS_ADMIN — the
# prebuilt image's tailscaled runs in a separate container, and
# the systemd install path doesn't use Tailscale at all).
# Resource limits (soft — operator can override in a drop-in)
LimitNOFILE=65536
# Logging
StandardOutput=journal
StandardError=journal
SyslogIdentifier=skygate

[Install]
WantedBy=multi-user.target
EOF

    systemctl daemon-reload
    echo "[install] wrote $unit_file"
}

# create_user_and_dirs: create the skygate system user (no
# password, no shell, no home — this is a service account, not
# a login account) and the data + etc dirs.
create_user_and_dirs() {
    local user="$1"
    local data_dir="$2"
    local etc_dir="$3"

    if ! id "$user" >/dev/null 2>&1; then
        # -r = system user (uid < 1000 on most distros)
        # -s /usr/sbin/nologin = no shell login
        # -d /nonexistent = no home dir (we use /var/lib/skygate
        #   via WorkingDirectory=, not via $HOME)
        useradd --system --no-create-home --shell /usr/sbin/nologin \
            --comment "Skygate service account" "$user"
        echo "[install] created system user: $user"
    else
        echo "[install] system user $user already exists"
    fi

    install -d -m 0750 -o "$user" -g "$user" "$data_dir"
    install -d -m 0750 -o "$user" -g "$user" "$etc_dir"
    install -d -m 0750 -o "$user" -g "$user" "$data_dir/ts"  # tailscale state
    echo "[install] created dirs: $data_dir $etc_dir"
}

# enable_and_start_service: enable the service at boot + start
# it now. Does NOT block on /healthz (the operator is expected
# to verify the install in their browser; the install script
# just needs the service to be RUNNING, not necessarily healthy).
#
# Note: the service will likely FAIL on first start because
# HEADSCALE_URL + HEADSCALE_API_KEY in /etc/skygate/skygate.env
# are empty placeholders. That's expected — the installer prints
# the next-steps block to tell the operator to fill them in.
enable_and_start_service() {
    local service_name="$1"
    systemctl enable "$service_name"
    # `systemctl restart` instead of `start` because the unit
    # may be already-running from a previous install attempt.
    # We don't `is-active` first because systemctl restart is
    # a no-op if the unit isn't running (it just starts it).
    if systemctl restart "$service_name"; then
        echo "[install] service $service_name started (may be unhealthy until /etc/skygate/skygate.env is filled in)"
    else
        echo "WARN: service $service_name failed to start. Check: journalctl -u $service_name" >&2
    fi
}

# print_next_steps: tell the operator what to do after the
# install script returns. The 3 things they MUST do:
#   1. Fill in HEADSCALE_URL + HEADSCALE_API_KEY in /etc/skygate/skygate.env
#   2. Restart the service (systemctl restart skygate)
#   3. Open the URL in a browser
print_next_steps() {
    local port="$1"
    local service_name="$2"
    local version
    version="$(SKYGATE_BIN=/usr/local/bin/skygate "${SKYGATE_BIN:-/usr/local/bin/skygate}" version 2>/dev/null || echo "unknown")"

    cat <<EOF

================================================================
  Skygate installed: $version
================================================================

  Next steps (3 things, ~2 minutes):

  1. Edit the env file and fill in your headscale coordinates:

       sudo \$EDITOR /etc/skygate/skygate.env

     Required keys (already in the file with placeholders):
       HEADSCALE_URL=        # e.g. http://localhost:50444
       HEADSCALE_API_KEY=    # from 'headscale apikeys create'

  2. Restart the service to pick up the new env:

       sudo systemctl restart $service_name

  3. Open the portal in a browser:

       http://localhost:$port/login

     Default admin:  admin
     Default password:  (set on first login)

  Useful commands:
       sudo systemctl status $service_name
       sudo journalctl -u $service_name -f
       sudo $SKYGATE_BIN version
       sudo $SKYGATE_BIN --help

  To uninstall:
       sudo systemctl disable --now $service_name
       sudo rm -f /etc/systemd/system/$service_name.service
       sudo rm -f $SKYGATE_BIN
       sudo userdel $SKYGATE_USER
       # /var/lib/skygate is PRESERVED (your data lives there)
================================================================
EOF
}
