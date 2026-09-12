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

# -------- 2026-09-12: defaults for env vars (B-install-debian-fix) --------
# The caller (install-debian.sh / install-rh.sh / install-alpine.sh)
# inherits `set -euo pipefail`, which propagates to this file via
# sourcing. Without defaults, an unset env var on a standalone
# invocation crashes with "unbound variable" before the function
# body even runs. The defaults here let this file work both:
#   - Called via install.sh (env exported upstream — defaults are no-ops)
#   - Called standalone (env vars unset — defaults apply)
: "${GITHUB_OWNER:=BarsSky}"
: "${GITHUB_REPO:=skygate}"
: "${SKYGATE_VERSION:=latest}"
: "${SKYGATE_CHANNEL:=stable}"
: "${SKYGATE_PORT:=8080}"
: "${SKYGATE_USER:=skygate}"
: "${SKYGATE_DATA_DIR:=/var/lib/skygate}"
: "${SKYGATE_ETC_DIR:=/etc/skygate}"
: "${SKYGATE_BIN:=/usr/local/bin/skygate}"
: "${SKIP_VERIFY:=${SKYGATE_SKIP_VERIFY:-0}}"
: "${SKYGATE_DB_TYPE:=}"
: "${SKYGATE_IMPORT_EXISTING_ON_FIRST_RUN:=}"
# Note: SKIP_VERIFY is intentionally NOT exported — it's a
# per-invocation flag, not a runtime value. The download function
# reads it from the env via "$SKIP_VERIFY" in the calling script's
# scope, not via export.
export GITHUB_OWNER GITHUB_REPO SKYGATE_VERSION SKYGATE_CHANNEL SKYGATE_PORT SKYGATE_USER SKYGATE_DATA_DIR SKYGATE_ETC_DIR SKYGATE_BIN SKYGATE_DB_TYPE SKYGATE_IMPORT_EXISTING_ON_FIRST_RUN

# -------- B-mod-sqlite-pg-bidi v1.5.4: --db-type flag handling --------
# The operator can choose the DB backend at install time via:
#   --db-type=sqlite    (default for self-host, no PG needed)
#   --db-type=postgres  (prod / HA, requires external PG instance)
#
# The flag is parsed by the per-OS install script and exported as
# SKYGATE_DB_TYPE before this file is sourced. If unset, the
# installer prompts the operator interactively (TTY-only).
#
# The SKYGATE_DB_TYPE env var is consumed by write_env_file below
# to populate SKYGATE_DB (the new env var that B-mod-sqlite-pg-bidi
# v1.5.4 added to support both dialects). Legacy SKYGATE_DB_DSN
# is still honored for backward compat with existing v1.3.0-v1.5.3
# PG deployments.

# resolve_db_type: read SKYGATE_DB_TYPE from env, prompt if unset.
# Sets SKYGATE_DB to the appropriate default DSN for the chosen
# type. Exports SKYGATE_DB so write_env_file picks it up.
#
# Args:
#   $1 = SKYGATE_DATA_DIR (used for the SQLite default path)
#
# Behavior:
#   SKYGATE_DB_TYPE=sqlite    → SKYGATE_DB=sqlite:/.../skygate.db
#   SKYGATE_DB_TYPE=postgres  → SKYGATE_DB= (operator must fill in)
#   SKYGATE_DB_TYPE unset + TTY → interactive prompt
#   SKYGATE_DB_TYPE unset + non-TTY → defaults to sqlite
#   SKYGATE_DB already set → no-op (operator explicitly set it)
resolve_db_type() {
    local data_dir="$1"

    # If SKYGATE_DB is already set (operator override), respect it.
    if [ -n "${SKYGATE_DB:-}" ]; then
        echo "[install] SKYGATE_DB already set: $SKYGATE_DB"
        export SKYGATE_DB
        return 0
    fi

    # If SKYGATE_DB_DSN is set (legacy v1.3.0-v1.5.3), keep it.
    # SKYGATE_DB takes precedence, so SKYGATE_DB_DSN only wins if
    # SKYGATE_DB is unset. The write_env_file below documents the
    # precedence.
    local db_type="${SKYGATE_DB_TYPE:-}"

    # If still unset, prompt (TTY) or default to sqlite (non-TTY).
    if [ -z "$db_type" ]; then
        if [ -t 0 ]; then
            echo "[install] Choose DB backend:"
            echo "  1) sqlite  (default, single-host, no PG needed)"
            echo "  2) postgres (HA / prod, requires external PG instance)"
            read -r -p "Enter choice [1]: " choice
            case "${choice:-1}" in
                2|postgres|pg) db_type="postgres" ;;
                *)             db_type="sqlite" ;;
            esac
        else
            db_type="sqlite"
        fi
    fi

    case "$db_type" in
        sqlite)
            export SKYGATE_DB="sqlite:${data_dir}/skygate.db"
            echo "[install] DB type: sqlite -> SKYGATE_DB=$SKYGATE_DB"
            ;;
        postgres|pg)
            export SKYGATE_DB=""
            echo "[install] DB type: postgres — fill SKYGATE_DB in /etc/skygate/skygate.env"
            echo "         Format: postgres://skygate:<password>@<host>:5432/skygate?sslmode=disable"
            ;;
        *)
            echo "ERROR: unknown --db-type='$db_type' (use sqlite or postgres)" >&2
            return 1
            ;;
    esac
}

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
    # 2026-09-12: SKIP_VERIFY=1 also skips the SHA256SUMS download.
    # Some releases (notably v1.5.3) publish a tarball but not the
    # SHA256SUMS file (a release pipeline hiccup). Without this
    # fallback, an operator who sets SKIP_VERIFY=1 still fails the
    # install because the SHA256SUMS URL 404s. With this fallback,
    # SKIP_VERIFY=1 means "trust whatever you can download".
    #
    # Important: SKYGATE_SKIP_VERIFY=1 must reach this script
    # even under sudo. The default Ubuntu sudoers ships with
    # `Defaults env_reset`, which strips the env. Operators must
    # either run the script as root directly or use `sudo -E`
    # to preserve the env var. install.sh handles this for the
    # default dispatch path; standalone calls (sudo bash
    # install-debian.sh) need `sudo -E`.
    if [ "$skip_verify" = "1" ]; then
        echo "[install] SKYGATE_SKIP_VERIFY=1, skipping SHA256SUMS download"
        SKIP_SUMS_DOWNLOAD=1
    else
        SKIP_SUMS_DOWNLOAD=0
    fi
    if [ "$SKIP_SUMS_DOWNLOAD" != "1" ]; then
        if ! curl -fsSL --retry 3 --retry-delay 2 -o "$work/SHA256SUMS" "$sums_url"; then
            echo "ERROR: failed to download SHA256SUMS from $sums_url" >&2
            echo "If the release doesn't ship a SHA256SUMS file (some" >&2
            echo "releases do — e.g. v1.5.3), re-run with" >&2
            echo "  SKYGATE_SKIP_VERIFY=1 sudo -E bash install-debian.sh" >&2
            echo "(the 'sudo -E' preserves the env across sudo's env_reset)." >&2
            return 1
        fi
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

    # Resolve the DB type (sqlite default / postgres / explicit
    # override) — sets SKYGATE_DB for the heredoc below.
    resolve_db_type "$data_dir"

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

# === Database (B-mod-sqlite-pg-bidi v1.5.4) ===
# Set SKYGATE_DB to one of:
#   - sqlite:/var/lib/skygate/skygate.db    (default — single-host, no PG needed)
#   - /var/lib/skygate/skygate.db            (bare path = SQLite)
#   - postgres://skygate:<password>@<host>:5432/skygate?sslmode=disable
# SKYGATE_DB takes precedence over the legacy SKYGATE_DB_DSN
# (which is still honored for v1.3.0-v1.5.3 backward compat).
# See docs/deploy.md §10 + the B-mod-sqlite-pg-bidi plan at
# docs/superpowers/plans/2026-09-11-b-mod-sqlite-pg-bidi.md.
SKYGATE_DB=sqlite:${SKYGATE_DATA_DIR}/skygate.db
# Legacy env var (v1.3.0-v1.5.3, PG-only). Leave empty unless
# you specifically need to fall back to the old naming.
SKYGATE_DB_DSN=

# === First-run auto-sync (B-mod-first-run-adoption T7) ===
# If 'true' AND node_owner_map is empty AND at least one
# portal_user exists, run SyncNodesFromHeadscale + exit-server
# auto-detect on first boot. The "deploy skygate as a sidecar
# to an existing headscale" flow goes from manual "click Sync
# from headscale" to fully automatic on first boot. Default:
# empty (auto-sync OFF). Set to 'true' for the sidecar flow.
# See docs/sidecar-mode.md "First-run adoption" + docs/install-dry-run-report.md.
# Note: the \${SKYGATE_IMPORT_EXISTING_ON_FIRST_RUN:-} expansion
# means the calling environment's value (set by install.sh's
# export + install-debian.sh's --import-existing= flag) flows
# into this file. Default empty if unset.
SKYGATE_IMPORT_EXISTING_ON_FIRST_RUN=${SKYGATE_IMPORT_EXISTING_ON_FIRST_RUN:-}

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

     Optional (B-mod-first-run-adoption T7 — sidecar adoption):
       SKYGATE_IMPORT_EXISTING_ON_FIRST_RUN=true
       # If you're deploying skygate NEXT TO an existing headscale
       # that already has nodes, set this to "true" so skygate
       # auto-imports them on first boot. See docs/sidecar-mode.md
       # "First-run adoption" + docs/install-dry-run-report.md.
       # Default empty = auto-sync OFF (you'll have to click
       # 'Sync from headscale' on /admin/devices manually).

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
