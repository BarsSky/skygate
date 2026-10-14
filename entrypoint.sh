#!/bin/sh
# 2026-07-14: Этап 14 v2 — Tailscale in-image.
#
# This entrypoint does three things in order:
#
#   1. (optional) Start tailscaled and bring up the Tailscale client
#      with `--accept-routes`. Skipped entirely when TS_AUTHKEY_FILE
#      is not set, so a non-RF deployment can run the same image
#      without joining a tailnet at all.
#
#   2. Build skygate from the bind-mounted source (go mod download, git for
#      build labels, go build with LDFLAGS). B383 (v1.5.122): the two Go caches
#      live on a PERSISTED path, the module download is bounded, and a start
#      whose sources are unchanged skips the build entirely — measured before
#      the fix: a recreate with cold caches took 18 MINUTES (proxy.golang.org
#      timed out) with no listener on :8080 at all; after it: ~3s warm, and a
#      provisional /healthz answers while the build runs.
#
#   3. exec /app/skygate so it becomes PID 1 of the container and
#      receives signals directly from docker.
#
# We intentionally do NOT call `tailscale set --exit-node=...` here.
# The relay (a separate node in the tailnet) advertises the Telegram
# IP ranges as subnet routes; skygate's `tailscale up --accept-routes`
# picks them up automatically, so api.telegram.org traffic is routed
# via the relay while everything else (headscale, etc.) stays direct.
# This keeps skygate's "exit-node=nodename" off the Tailscale client
# entirely (see the discussion in the commit message).
set -e

# 1. Tailscale setup.
#
# We guard on the authkey file path (a docker secret path, or
# /data/ts/authkey written by the /admin/tailscale web UI) being
# present AND readable. A non-RF deployment that doesn't need
# Tailscale at all can simply not set the env var + not mount the
# secret; the entrypoint then skips tailscaled and skygate starts
# with direct internet access.
#
# v1.5.8+ (B258): the /admin/tailscale web UI mirrors this skip
# check (tailscaleAuthKeyDisabled in internal/feature/admin/tailscale.go).
# When this var is /dev/null, the entrypoint skips tailscaled AND
# the UI shows a "Tailscale is disabled by config" banner with
# the Start button hard-disabled. Operator can't trigger the raw
# os.ReadFile error by clicking.
#
# v0.33.1.9: accept BOTH the legacy TS_AUTHKEY_FILE and the newer
# SKYGATE_TS_AUTHKEY_FILE (set in docker-compose.yml). The two names
# were a long-standing mismatch — docker-compose sets
# SKYGATE_TS_AUTHKEY_FILE=/run/secrets/ts_authkey, but the entrypoint
# only checked TS_AUTHKEY_FILE, so the auth key was being silently
# ignored. Picking the first non-empty of the two in order (legacy
# first, so any operator who manually set the old name still wins)
# keeps the change additive and backwards-compatible.
TS_AUTHKEY_FILE="${TS_AUTHKEY_FILE:-${SKYGATE_TS_AUTHKEY_FILE:-}}"
# v0.33.1.9: also pick up /data/ts/authkey (the file the
# /admin/tailscale web UI writes to). Lowest priority so the
# explicit env vars still win on a fresh boot; the web UI
# key is the per-deploy override that survives container
# restarts (the /data dir is bind-mounted from the host).
if [ -z "$TS_AUTHKEY_FILE" ] && [ -f /data/ts/authkey ]; then
    TS_AUTHKEY_FILE="/data/ts/authkey"
fi
export TS_AUTHKEY_FILE
if [ -n "$TS_AUTHKEY_FILE" ] && [ -f "$TS_AUTHKEY_FILE" ]; then
    echo "[init] starting tailscaled"
    # tailscaled writes tailscaled.state into --statedir; the control
    # socket is at /var/run/tailscale/tailscaled.sock. Both paths are
    # bind-mounted from the host in docker-compose.yml so the state
    # survives container restarts.
    mkdir -p /var/lib/tailscale /var/run/tailscale
    tailscaled --statedir=/var/lib/tailscale \
        >/var/log/tailscaled.log 2>&1 &
    TAILSCALED_PID=$!
    echo "[init] tailscaled PID=$TAILSCALED_PID"

    # Wait for the control socket. tailscaled takes a few seconds to
    # come up; we give it 30s and continue anyway if it's not ready
    # (skygate still works on the host network even without Tailscale).
    READY=""
    for i in $(seq 1 30); do
        if tailscale status >/dev/null 2>&1; then
            READY="yes"
            echo "[init] tailscaled ready after ${i}s"
            break
        fi
        sleep 1
    done
    if [ -z "$READY" ]; then
        echo "[init] WARNING: tailscaled not ready after 30s; continuing"
    fi

    AUTHKEY=$(cat "$TS_AUTHKEY_FILE")
    # 2026-08-10: v0.33.1.34 B86 — accept BOTH the legacy
    # TS_LOGIN_SERVER and the newer SKYGATE_TS_LOGIN_SERVER
    # (set in docker-compose.yml), mirroring the
    # TS_AUTHKEY_FILE → SKYGATE_TS_AUTHKEY_FILE fallback that
    # v0.33.1.9 already added for the authkey. The
    # docker-compose.yml was updated in v0.33.1.16 (B65) to
    # set SKYGATE_TS_LOGIN_SERVER (so .env / env_file wins
    # over a hardcoded value), but the entrypoint was never
    # updated to read the new name. The pre-B86 default
    # `https://head.example.com` is a placeholder that points
    # at the Tailscale example domain — `tailscale up` against
    # it silently failed (the 30s timeout + "WARNING"
    # swallowed the error), the state file ended up with
    # ControlURL=`https://head.example.com`, and the
    # container's tailscaled is in NoState forever after.
    # Live symptom: 100.64.0.3 unreachable from inside the
    # skygate container even though tailscale0 is up
    # (state shows "logged out, fetch control key from
    # head.example.com: no DNS fallback"). The fallback chain
    # preserves the legacy name (so an operator who manually
    # set TS_LOGIN_SERVER still wins) and adds the newer
    # SKYGATE_TS_LOGIN_SERVER (which docker-compose / .env
    # set).
    LOGIN_SERVER="${TS_LOGIN_SERVER:-${SKYGATE_TS_LOGIN_SERVER:-https://head.example.com}}"
    # Same pattern for the hostname: legacy TS_HOSTNAME + newer SKYGATE_TS_HOSTNAME.
    # B384 (v1.5.123): the default was the legacy `skygate-host-1` placeholder — the
    # shape B320/B321 exist to remove, because a suffixed client silently fails every
    # "is this me?" check (infra ownership, the SSH-source ACL, the self-probes). The
    # canonical name is `skygate-host`, and a configured placeholder is REWRITTEN to it
    # (below) exactly as the Go side's NormalizeTailscaleHostname does — the two must
    # agree or the name flips depending on which half runs last.
    HOSTNAME="${TS_HOSTNAME:-${SKYGATE_TS_HOSTNAME:-skygate-host}}"

# ---------------------------------------------------------------------------
# B384 (v1.5.123) — the client's settings must actually APPLY, and the node must be
# able to say whether they did.
#
# The measured defect (reference host, 2026-10-14): `tailscale up` failed on EVERY boot
# and EVERY 5-minute tick with
#     Error: changing settings via 'tailscale up' requires mentioning all
#     non-default flags. To proceed, either re-run your command with --reset or …
# because the persisted state carried prefs nobody mentioned (NoStatefulFiltering=true,
# NetfilterMode=2). `up` therefore applied NOTHING — an operator editing the hostname or
# the tags in the panel waited forever — and the journal carried a permanent failure
# line. Two traps are encoded below because both were paid for:
#   * the message is WRAPPED across lines, so every match collapses whitespace first
#     (the Go twin, tailscalePrefDriftKind, is unit-tested on the real wrapped text);
#   * `tailscale set` is the tool that does NOT require the full flag set (upstream's
#     own words), so it is tried FIRST whenever the daemon already answers.
#
# The helpers between the markers are pure, and
# scripts/check_b384_tailscale_settings_apply.sh extracts and exercises them.
# ---------------------------------------------------------------------------
# >>> B384 helpers (begin)
#
# skygate_tc_normalize_hostname <name> — the canonical name for a configured one: the
# legacy `skygate-host-<n>…` placeholder maps to `skygate-host`. Prints the name.
skygate_tc_normalize_hostname() {
    _b384_n="$(printf '%s' "${1:-}" | tr -d '[:space:]')"
    case "$(printf '%s' "$_b384_n" | tr 'A-Z' 'a-z')" in
        skygate-host-[0-9]*) printf '%s\n' "skygate-host" ;;
        *)                   printf '%s\n' "$_b384_n" ;;
    esac
}
# skygate_tc_advertise_tag <hostname> — the ONE derivation of the tag that describes a
# node: tag:dev-infra-<canonical-name>. Never emits the retired `-1` shape.
skygate_tc_advertise_tag() {
    _b384_h="$(skygate_tc_normalize_hostname "${1:-}")"
    [ -n "$_b384_h" ] || return 0
    printf 'tag:dev-infra-%s\n' "$(printf '%s' "$_b384_h" | tr 'A-Z' 'a-z')"
}
# skygate_tc_error_kind <output> — classify a failed set/up. Whitespace is collapsed
# FIRST: the live message is wrapped, so a plain substring search never matches it.
skygate_tc_error_kind() {
    _b384_o="$(printf '%s' "${1:-}" | tr '\n\t' '  ' | tr -s ' ' | tr 'A-Z' 'a-z')"
    case "$_b384_o" in
        *"requires mentioning all non-default flags"*) printf '%s\n' "drift" ;;
        *"flag provided but not defined"*)             printf '%s\n' "bad-flag" ;;
        *"invalid or not permitted"*|*"not permitted"*) printf '%s\n' "tags-not-permitted" ;;
        *"logged out"*|*"not logged in"*)              printf '%s\n' "logged-out" ;;
        *"context deadline exceeded"*|*"timed out"*)   printf '%s\n' "timeout" ;;
        *)                                             printf '%s\n' "" ;;
    esac
}
# skygate_tc_set_supports_advertise_tags — does THIS client's `set` accept the flag?
# Measured on the reference host (Tailscale 1.102.3, the version the image ships):
# `tailscale set --help` lists --hostname/--accept-routes/--accept-dns/--exit-node and NO
# --advertise-tags, while `tailscale up` has it. Passing it anyway made the FIRST rung fail
# on every boot ("flag provided but not defined: -advertise-tags"), so the tags only ever
# landed through `up` — and the log carried a permanent failure line. Exit 0 = supported.
skygate_tc_set_supports_advertise_tags() {
    tailscale set --help 2>&1 | grep -q -- '--advertise-tags'
}
# skygate_tc_set_tag_arg <tags> <support 0|1> — the --advertise-tags argument for `set`,
# or nothing when this client's `set` does not know the flag (then `up` carries the tags).
skygate_tc_set_tag_arg() {
    [ "${2:-0}" = "1" ] || return 0
    [ -n "${1:-}" ] || return 0
    printf '%s\n' "--advertise-tags=$1"
}
# skygate_tc_try_set <hostname> <tags> <common-flags> <tag-arg> — run the `set` rung and
# report whether it may be ACCEPTED as final:
#   0  `set` succeeded AND the daemon's own view now matches the intent (accepted)
#   2  `set` succeeded, but the daemon does not match (tags this client's `set` cannot
#      carry) — the caller must continue with `up`, which does have the flag
#   1  `set` failed (the caller continues with `up` anyway)
# The command's own output is printed on stdout for the caller to classify.
skygate_tc_try_set() {
    _b384_sh="$1"; _b384_st="${2:-}"; _b384_scommon="$3"; _b384_stag="${4:-}"
    # shellcheck disable=SC2086
    if _b384_sout="$(timeout 30 tailscale set --hostname="$_b384_sh" $_b384_scommon $_b384_stag 2>&1)"; then
        if skygate_tc_verify "$_b384_sh" "$_b384_st" >/dev/null 2>&1; then
            printf '%s\n' "$_b384_sout"
            return 0
        fi
        printf '%s\n' "$_b384_sout"
        return 2
    fi
    printf '%s\n' "$_b384_sout"
    return 1
}
# skygate_tc_verify <want-host> <want-tags> — compare the intent with the daemon's OWN
# view (.Self.HostName + .Self.Tags) and report ONE named line. Empty tags means "no
# expectation". Prints "OK …" or "WARN …"; exit 0 when it matches.
skygate_tc_verify() {
    _b384_wh="$1"; _b384_wt="${2:-}"
    _b384_v="$(tailscale status --json 2>/dev/null | python3 -c '
import json,sys
try:
    d = json.load(sys.stdin)
except Exception:
    print("|"); raise SystemExit
s = d.get("Self") or {}
host = (s.get("HostName") or "").strip()
if not host:
    dns = (s.get("DNSName") or "").rstrip(".")
    host = dns.split(".")[0] if dns else ""
print(host + "|" + ",".join(sorted(t.strip() for t in (s.get("Tags") or []) if t.strip())))
' 2>/dev/null)"
    _b384_gh="${_b384_v%%|*}"
    _b384_gt="${_b384_v#*|}"
    if [ -z "$_b384_gh" ]; then
        printf 'WARN cannot verify — the daemon did not answer tailscale status --json (wanted hostname=%s tags=%s)\n' \
            "$_b384_wh" "${_b384_wt:-none}"
        return 1
    fi
    _b384_gt_sorted="$(printf '%s' "$_b384_gt" | tr ',' '\n' | tr -d '[:space:]' | tr 'A-Z' 'a-z' | grep -v '^$' | sort | paste -sd, -)"
    _b384_wt_sorted="$(printf '%s' "$_b384_wt" | tr ',' '\n' | tr -d '[:space:]' | tr 'A-Z' 'a-z' | grep -v '^$' | sort | paste -sd, -)"
    if [ "$(printf '%s' "$_b384_gh" | tr 'A-Z' 'a-z')" = "$(printf '%s' "$_b384_wh" | tr 'A-Z' 'a-z')" ] \
        && [ "$_b384_gt_sorted" = "$_b384_wt_sorted" ]; then
        printf 'OK hostname=%s tags=%s\n' "$_b384_gh" "${_b384_gt:-none}"
        return 0
    fi
    printf 'SETTINGS NOT APPLIED — the daemon reports hostname=%s tags=%s, the configuration asks for hostname=%s tags=%s\n' \
        "$_b384_gh" "${_b384_gt:-none}" "$_b384_wh" "${_b384_wt:-none}"
    return 1
}
# <<< B384 helpers (end)
# ---------------------------------------------------------------------------
    # B384: a configured placeholder is normalised to the canonical name, loudly.
    _canon_hostname="$(skygate_tc_normalize_hostname "$HOSTNAME")"
    if [ "$_canon_hostname" != "$HOSTNAME" ]; then
        echo "[init] hostname=$HOSTNAME is the legacy v0.33.1.9 placeholder — using the canonical name $_canon_hostname (update SKYGATE_TS_HOSTNAME in .env/docker-compose.yml to silence this)"
        HOSTNAME="$_canon_hostname"
    fi
    # 2026-08-25 (B185): Tailscale's `tailscale up` is an
    # "all-or-nothing" command — if the persisted state has
    # any non-default flag set (e.g. --advertise-tags), the
    # next `tailscale up` MUST mention that flag, or it
    # errors with "requires mentioning all non-default
    # flags" and the entire call is a no-op. Live symptom
    # (2026-08-25): the operator's earlier `tailscale set
    # --advertise-tags=tag:dev-infra-skygate-host-1,tag:private`
    # persisted into tailscaled.state, and every subsequent
    # `tailscale up --accept-routes` silently failed
    # (WARNING logged, exit code non-zero), leaving
    # `RouteAll=false` in the state. Result: skygate
    # container never accepted the relay's advertised
    # subnet routes, and the Telegram probe stayed
    # "unreachable" even though everything else was correct.
    #
    # The fix: read the existing state's advertise-tags
    # (if any) and pass them back. If the state has no
    # advertise-tags, DERIVE the tag from the canonical hostname (B384: the literal
    # `tag:dev-infra-skygate-host-1,tag:private` this used to fall back to was the
    # retired placeholder shape and named a node that does not exist).
    # Operators can override with SKYGATE_TS_ADVERTISE_TAGS.
    ADVERTISE_TAGS="${SKYGATE_TS_ADVERTISE_TAGS:-}"
    if [ -z "$ADVERTISE_TAGS" ] && [ -f /var/lib/tailscale/tailscaled.state ]; then
        # tailscaled.state is a JSON blob. The current
        # profile's AdvertiseTags field is base64-encoded
        # inside _profiles. Python is the most reliable way
        # to extract it (jq is also fine if present).
        ADVERTISE_TAGS=$(python3 -c "
import json, base64, sys
try:
    with open('/var/lib/tailscale/tailscaled.state') as f:
        d = json.load(f)
    profiles = json.loads(d.get('_profiles', '{}'))
    cur = d.get('_current-profile', '')
    p = profiles.get(cur, {})
    tags = p.get('AdvertiseTags') or []
    if tags:
        print(','.join(tags))
except Exception:
    pass
" 2>/dev/null)
    fi
    if [ -z "$ADVERTISE_TAGS" ]; then
        ADVERTISE_TAGS="$(skygate_tc_advertise_tag "$HOSTNAME")"
        echo "[init] advertise-tags derived from the hostname: $ADVERTISE_TAGS"
    fi
    echo "[init] advertise-tags=$ADVERTISE_TAGS"

    echo "[init] tailscale up --accept-routes (login-server=$LOGIN_SERVER, hostname=$HOSTNAME)"
    # 2026-07-14: --accept-dns=false is critical. By default
    # tailscaled overwrites /etc/resolv.conf with Tailscale's
    # MagicDNS resolver (100.100.100.100), which only knows
    # about tailnet names. Docker's own DNS (127.0.0.11) is no
    # longer consulted, so the container can't resolve its
    # Docker-network peers by name — most importantly
    # "headscale" (the headscale API endpoint configured via
    # HEADSCALE_URL=http://headscale:50444).
    #
    # Disabling accept-dns means the container keeps using
    # Docker's DNS. The downside is that tailnet names (e.g.
    # `relay-1.tailnet`) won't resolve from inside the
    # container — but skygate doesn't currently need to
    # resolve tailnet names; it only talks to the Docker
    # service named "headscale" and to api.telegram.org (an
    # IP literal, after the resolver at probe time).
    #
    # The previous "sidecar + network_mode: service:tailscale"
    # setup hit the same DNS problem differently: the shared
    # netns broke Docker's embedded DNS responder. In-image
    # tailscaled doesn't share netns, so the responder works
    # — we just need to stop tailscaled from replacing
    # /etc/resolv.conf.
    #
    # 2026-07-25: v0.28.5c — explicitly add --exit-node= (empty)
    # to the `tailscale up` invocation. Reason: Tailscale
    # persists ALL prefs in tailscaled.state (bind-mounted from
    # the host). If the operator ever ran `tailscale set
    # --exit-node=relay-1` on the container (even once, for
    # testing), the state remembers it. The next `tailscale up`
    # then errors with "requires mentioning all non-default
    # flags" because the new invocation doesn't list the
    # previously-set --exit-node. The entrypoint prints a
    # warning and continues — but the OLD exit-node is still
    # active. Symptom: skygate-host-1 routes ALL traffic (including
    # 172.18.0.0/16 Docker bridge) through the exit-node, which
    # breaks connectivity to the openresty/Caddy upstream
    # (504 Gateway Time-out on https://skygate.example.com).
    # Adding --exit-node= to the entrypoint ensures the exit-node
    # is cleared on every skygate restart. If the operator wants
    # the skygate container to be an exit-node itself, they can
    # explicitly set it AFTER the entrypoint runs (`docker exec
    # skygate tailscale set --exit-node=<tag>`).
    # B384 (v1.5.123) — the apply ladder. `tailscale up` alone FAILED on every boot and
    # every tick on the reference host because the state carried prefs this invocation
    # never mentions (NoStatefulFiltering, NetfilterMode), and upstream refuses to change
    # ANY setting in that case. So:
    #   1. the daemon already answers  → `tailscale set` (upstream: "does not require the
    #      complete set of desired settings") — the one property `up` cannot offer here;
    #   2. otherwise                   → `up` with every flag this container owns (the
    #      first join, where no state exists to drift from);
    #   3. `up` refused for drift      → retry ONCE with `up --reset`, the remedy upstream
    #      prints, safe here because this call names every pref the container owns;
    #   4. then VERIFY against the daemon's own view and report one named line, because
    #      "the command exited 0" is not the same statement as "the settings are applied".
    TC_COMMON_SET="--accept-routes --accept-dns=false --exit-node="
    TC_APPLIED=0
    # B384.1 (v1.5.124) — probe ONE flag capability instead of discovering it by failing:
    # 1.102.3's `set` has no --advertise-tags, so the tag belongs to the `up` rung there.
    if skygate_tc_set_supports_advertise_tags; then
        TC_SET_TAG_SUPPORT=1
    else
        TC_SET_TAG_SUPPORT=0
        echo "[init] this client's 'tailscale set' has no --advertise-tags — the tag is left to 'tailscale up' (it has it)"
    fi
    TC_SET_TAG_ARG="$(skygate_tc_set_tag_arg "$ADVERTISE_TAGS" "$TC_SET_TAG_SUPPORT")"
    if tailscale status >/dev/null 2>&1; then
        if TC_OUT="$(skygate_tc_try_set "$HOSTNAME" "$ADVERTISE_TAGS" "$TC_COMMON_SET" "$TC_SET_TAG_ARG")"; then
            echo "[init] tailscale set applied and VERIFIED (hostname=$HOSTNAME tags=$ADVERTISE_TAGS)"
            TC_APPLIED=1
        else
            # $? must be read FIRST: it is the rung's verdict (2 = set worked, the daemon
            # does not match yet → `up` carries the tags; 1 = set failed).
            TC_SET_RC=$?
            if [ "$TC_SET_RC" = "2" ]; then
                echo "[init] tailscale set applied the flags it can, but the daemon does not match the intent yet — continuing with 'tailscale up' (it carries the flags 'set' cannot)"
            else
                echo "[init] tailscale set failed ($(skygate_tc_error_kind "$TC_OUT")): $(printf '%s' "$TC_OUT" | head -2 | tr '\n' ' ')"
            fi
        fi
    fi
    if [ "$TC_APPLIED" != "1" ]; then
        if TC_OUT="$(timeout 30 tailscale up \
                --login-server="$LOGIN_SERVER" \
                --authkey="$AUTHKEY" \
                --hostname="$HOSTNAME" \
                $TC_COMMON_SET \
                --advertise-tags="$ADVERTISE_TAGS" 2>&1)"; then
            echo "[init] tailscale up applied (hostname=$HOSTNAME tags=$ADVERTISE_TAGS)"
            TC_APPLIED=1
        else
            TC_KIND="$(skygate_tc_error_kind "$TC_OUT")"
            if [ "$TC_KIND" = "drift" ]; then
                echo "[init] tailscale up refused: the persisted prefs drifted from this invocation — retrying ONCE with --reset (upstream's own remedy)"
                if TC_OUT="$(timeout 30 tailscale up --reset \
                        --login-server="$LOGIN_SERVER" \
                        --authkey="$AUTHKEY" \
                        --hostname="$HOSTNAME" \
                        $TC_COMMON_SET \
                        --advertise-tags="$ADVERTISE_TAGS" 2>&1)"; then
                    echo "[init] tailscale up --reset applied (hostname=$HOSTNAME tags=$ADVERTISE_TAGS) — the drift is cleared for good"
                    TC_APPLIED=1
                fi
            fi
        fi
    fi
    if [ "$TC_APPLIED" != "1" ]; then
        # timeout exits 124, tailscale itself exits non-zero on auth failure. Either way:
        # log WHICH kind of failure it was and move on. v0.33.1.9 entry-point fix — without
        # the timeout, `tailscale up` blocks forever when tailscaled didn't come up (e.g.
        # because the headscale login-server was unreachable), which hung the entrypoint
        # and prevented skygate from ever reaching /healthz. The 30s ceiling matches the
        # tailscaled-readiness wait above.
        echo "[init] WARNING: could not apply the Tailscale settings (kind=$(skygate_tc_error_kind "$TC_OUT")); continuing — the app re-tries every 5 minutes"
    fi
    echo "[init] tailscale-apply: $(skygate_tc_verify "$HOSTNAME" "$ADVERTISE_TAGS" || true)"

    echo "[init] tailscale status:"
    tailscale status 2>&1 | head -10 || true
else
    echo "[init] TS_AUTHKEY_FILE not set — Tailscale skipped (non-RF mode)"
fi

# 2026-08-10: v0.33.1.39 B91 — pre-flight headscale wait.
#
# skygate is designed to start INDEPENDENTLY of headscale. The
# admin explicitly configures HEADSCALE_URL via .env (or the
# /admin/headscale web UI), and headscale being down should
# never block the skygate container from booting. This is the
# whole point of B90-style loose coupling between services.
#
# That said, when the VM reboots, ALL containers restart in
# parallel. headscale (gRPC, DB migrations, policy reload)
# takes ~30s to come up; skygate is up in ~5s. For the first
# 25s after a reboot, every skygate → headscale API call fails
# (auth.go New() builds the client eagerly, main.go's
# ensureHeadscaleUser runs at startup, B77 autoupdater polls
# immediately, /readyz returns 503 because headscale isn't
# reachable yet). The errors are non-fatal — skygate keeps
# running and recovers when headscale comes up — but the
# operator sees a wall of "headscale unreachable" errors and
# may incorrectly diagnose it as a broken startup.
#
# This pre-flight wait polls HEADSCALE_URL /health for up to
# 60s. It does NOT block skygate startup if the URL is empty
# (no .env value, no per-admin override), unreachable, or
# still booting — it just logs a warning and continues. The
# 60s ceiling matches the tailscaled-wait above (both are
# best-effort startup probes that don't gate the build).
#
# HEADSCALE_URL is read directly (set by docker-compose's
# env_file from .env). No SKYGATE_ prefix — headscale runs
# outside the skygate container, so it doesn't know about
# the SKYGATE_ namespace. The /health endpoint is a
# headscale-native endpoint (gRPC health-checked) that
# returns 200 once the daemon is ready to accept API calls.
HEADSCALE_URL="${HEADSCALE_URL:-}"
# 2026-08-11: v0.33.1.42 D4 — pre-flight wait timeout is now
# configurable via SKYGATE_HEADSCALE_WAIT_TIMEOUT (in seconds).
# The 60s default is fine for most VMs (headscale boots in
# <30s on a single-VM deploy). Operators with slow disks, large
# headscale DBs, or NAS-backed headscale data dirs can set this
# higher (e.g. SKYGATE_HEADSCALE_WAIT_TIMEOUT=180 for a 3-min
# ceiling). Set to 0 to disable the wait entirely (skygate
# starts immediately; the autoupdater + B91 pattern still
# tolerates headscale being down — the pre-flight wait is just
# cosmetic noise-reduction on a cold boot).
HS_WAIT_TIMEOUT="${SKYGATE_HEADSCALE_WAIT_TIMEOUT:-60}"
if [ -n "$HEADSCALE_URL" ] && [ "$HS_WAIT_TIMEOUT" -gt 0 ]; then
    echo "[init] pre-flight: waiting for headscale at $HEADSCALE_URL (max ${HS_WAIT_TIMEOUT}s)"
    HS_READY=""
    for i in $(seq 1 "$HS_WAIT_TIMEOUT"); do
        if wget -qO- --timeout=2 "$HEADSCALE_URL/health" >/dev/null 2>&1; then
            HS_READY="yes"
            echo "[init] headscale ready after ${i}s"
            break
        fi
        sleep 1
    done
    if [ -z "$HS_READY" ]; then
        echo "[init] WARNING: headscale not ready after ${HS_WAIT_TIMEOUT}s; skygate starting anyway (admin can fix HEADSCALE_URL via /admin/headscale if it points to the wrong place)"
    fi
elif [ -n "$HEADSCALE_URL" ] && [ "$HS_WAIT_TIMEOUT" = "0" ]; then
    echo "[init] SKYGATE_HEADSCALE_WAIT_TIMEOUT=0; pre-flight wait disabled (skygate starts immediately; B91 pattern tolerates headscale being down)"
else
    echo "[init] HEADSCALE_URL not set; headscale calls will return errors until configured (set in .env or /admin/headscale)"
fi

# 2026-09-09 (B-mod-db-retry) — pre-flight DB reachability check.
#
# Background: skygate v1.3.0+ removed the SQLite fallback, so
# SKYGATE_DB_DSN MUST point at a reachable PostgreSQL. A stale
# DSN (e.g. pointing at a now-shut-down staging DB) would
# previously fail silently inside skygate (after the
# healthcheck marked the container "healthy") and result in an
# infinite entrypoint.sh restart loop — every restart re-runs
# `go mod download` + `go build` (~5-7s each), and the operator
# sees only "Up X hours (healthy)" in docker ps while every
# actual request 500s.
#
# This pre-flight check extracts the host:port from the DSN
# and probes it with a 3-second TCP connect. If the host is
# unreachable, the entrypoint exits 1 with a clear, actionable
# error message (NOT a silent restart). The 3-second timeout
# matches skygate's internal Ping context (5s in OpenDSNWithRetry)
# and is short enough to fail fast on a permanent error but
# long enough to tolerate a 1-2s network blip.
#
# If SKYGATE_DB_DSN is empty, we log a warning and continue
# (skygate v1.3.0+ will fail to start, but the failure mode is
# the same as before this check existed — so we don't
# change behavior for empty DSN operators).
#
# The check uses bash's built-in /dev/tcp/host/port (no
# external binary required, works in any POSIX shell).
if [ -n "${SKYGATE_DB_DSN:-}" ]; then
    # Parse host:port from a postgres://user:pass@host:port/db
    # DSN. We use shell parameter expansion (no sed/awk needed)
    # to keep this lightweight and avoid pulling in coreutils.
    db_part="${SKYGATE_DB_DSN#*@}"           # user:pass@host:port/db → host:port/db
    db_part="${db_part%%/*}"                 # host:port/db → host:port
    db_host="${db_part%%:*}"                 # host:port → host
    db_port="${db_part##*:}"                 # host:port → port
    if [ -n "$db_host" ] && [ -n "$db_port" ] && [ "$db_port" != "$db_part" ]; then
        if timeout 3 bash -c "exec 3<>/dev/tcp/$db_host/$db_port" 2>/dev/null; then
            echo "[init] DB pre-flight: $db_host:$db_port reachable"
        else
            echo "[init] DB pre-flight: $db_host:$db_port UNREACHABLE — skygate would enter restart loop" >&2
            echo "[init] Fix: update SKYGATE_DB_DSN in .env to point at a live PostgreSQL" >&2
            echo "[init] Fix: or remove SKYGATE_DB_DSN (skygate v1.3.0+ has no SQLite fallback, so it MUST point at PG)" >&2
            echo "[init] Current SKYGATE_DB_DSN: ${SKYGATE_DB_DSN}" >&2
            exit 1
        fi
    else
        echo "[init] DB pre-flight: SKYGATE_DB_DSN present but unparseable: $SKYGATE_DB_DSN" >&2
        echo "[init] Expected format: postgres://user:pass@host:port/db?sslmode=disable" >&2
        exit 1
    fi
else
    echo "[init] DB pre-flight: SKYGATE_DB_DSN not set — skygate v1.3.0+ will fail to start (no SQLite fallback)"
fi

# ---------------------------------------------------------------------------
# B383 (v1.5.122) — the helpers that decide what a container start costs.
#
# Everything between the BEGIN/END markers is pure shell with no side effects,
# so scripts/check_b383_entrypoint_build_budget.sh extracts exactly this block
# and exercises it against synthetic trees and stamps — the contract tests the
# SAME code the container runs, not a copy of it.
#
# Why this exists (measured on the reference host 2026-10-14):
#   * caches inside the container layer  -> lost on every recreate;
#   * `go mod download` with a COLD module cache hit proxy.golang.org, the
#     connection timed out, and the whole start took 18 minutes with nothing
#     listening on :8080 — the operator saw "the web panel does not display";
#   * measured costs after a warm-up: `go mod download` 0.03s, full rebuild
#     3.1s (warm build cache) / 48s (cold build cache), no-op rebuild 0.4s.
# ---------------------------------------------------------------------------
# >>> B383 helpers (begin)
#
# skygate_go_cache_dirs <preferred_root> <fallback_mod> <fallback_build>
#   Prints "<mod>\t<build>". Prefers <preferred_root>/{mod,build} — a path the
#   operator's install already persists (the /data bind mount) — and falls back
#   to the container-local defaults when that root cannot be created or written,
#   so an install with a read-only /data behaves exactly as it did before.
#   Exit 0 = persisted root in use, 1 = fell back (the caller logs a warning).
skygate_go_cache_dirs() {
    _b383_root="${1:-}"
    _b383_fb_mod="${2:-/go/pkg/mod}"
    _b383_fb_build="${3:-/root/.cache/go-build}"
    if [ -n "$_b383_root" ] \
        && mkdir -p "$_b383_root/mod" "$_b383_root/build" 2>/dev/null \
        && [ -w "$_b383_root/mod" ] && [ -w "$_b383_root/build" ]; then
        printf '%s\t%s\n' "$_b383_root/mod" "$_b383_root/build"
        return 0
    fi
    printf '%s\t%s\n' "$_b383_fb_mod" "$_b383_fb_build"
    return 1
}

# skygate_go_source_fingerprint <appdir>
#   Prints a stable hash of exactly what a rebuild depends on. In a git checkout
#   (the docker install always binds one) it hashes HEAD plus the porcelain
#   status, so the panel's updater — which does `git checkout <tag>` in that very
#   tree — ALWAYS changes the fingerprint and the rebuild still happens. Outside
#   git it falls back to a listing of sizes and mtimes of the Go sources.
skygate_go_source_fingerprint() {
    _b383_app="${1:-/app}"
    if command -v git >/dev/null 2>&1 && git -C "$_b383_app" rev-parse --git-dir >/dev/null 2>&1; then
        {
            git -C "$_b383_app" rev-parse HEAD 2>/dev/null || true
            git -C "$_b383_app" status --porcelain 2>/dev/null || true
        } | sha256sum 2>/dev/null | awk '{print $1}'
        return 0
    fi
    find "$_b383_app/cmd" "$_b383_app/internal" "$_b383_app/go.mod" "$_b383_app/go.sum" \
        -type f 2>/dev/null \
        | sort \
        | while read -r _b383_f; do
            printf '%s %s %s\n' "$_b383_f" "$(wc -c < "$_b383_f" 2>/dev/null || echo 0)" \
                "$(stat -c %Y "$_b383_f" 2>/dev/null || echo 0)"
        done \
        | sha256sum 2>/dev/null | awk '{print $1}'
    return 0
}

# skygate_go_stamp_binary / skygate_go_binary_unchanged
#   The stamp records the fingerprint AND the binary's own size+mtime, so a
#   binary that was replaced or rebuilt by anything else can never be mistaken
#   for "the one this stamp describes".
skygate_go_stamp_binary() {
    _b383_bin="${1:-/app/skygate}"
    stat -c '%s %Y' "$_b383_bin" 2>/dev/null || echo "missing"
}
skygate_go_binary_unchanged() {
    _b383_bin="${1:-/app/skygate}"
    _b383_recorded="${2:-}"
    [ -n "$_b383_recorded" ] || return 1
    [ "$(skygate_go_stamp_binary "$_b383_bin")" = "$_b383_recorded" ]
}

# skygate_go_build_needed <appdir> <binary> <stampfile> <force>
#   Prints one line explaining the decision; exit 0 = build, 1 = skip.
#   A skip requires ALL of: an executable binary, a stamp, an identical
#   fingerprint, and the same binary the stamp describes. Anything else —
#   including an unreadable tree — builds, which is the pre-B383 behaviour.
skygate_go_build_needed() {
    _b383_app="${1:-/app}"
    _b383_bin="${2:-/app/skygate}"
    _b383_stamp="${3:-}"
    _b383_force="${4:-0}"
    if [ "$_b383_force" = "1" ]; then
        echo "build forced (SKYGATE_FORCE_BUILD=1)"
        return 0
    fi
    if [ ! -x "$_b383_bin" ]; then
        echo "build no executable at $_b383_bin"
        return 0
    fi
    if [ -z "$_b383_stamp" ] || [ ! -f "$_b383_stamp" ]; then
        echo "build no build stamp yet"
        return 0
    fi
    _b383_now="$(skygate_go_source_fingerprint "$_b383_app")"
    if [ -z "$_b383_now" ]; then
        echo "build fingerprint unavailable"
        return 0
    fi
    _b383_prev="$(head -n 1 "$_b383_stamp" 2>/dev/null || true)"
    _b383_prevbin="$(sed -n '2p' "$_b383_stamp" 2>/dev/null || true)"
    if [ "$_b383_now" != "$_b383_prev" ]; then
        echo "build sources changed since the last build (${_b383_prev:-none} -> $_b383_now)"
        return 0
    fi
    if ! skygate_go_binary_unchanged "$_b383_bin" "$_b383_prevbin"; then
        echo "build the recorded binary is not the one on disk any more"
        return 0
    fi
    echo "skip sources unchanged and the binary matches the stamp ($_b383_now)"
    return 1
}

# skygate_boot_responder_start <port>
#   While the build runs there is no process to answer, so the operator's browser
#   and the panel's health poll see a dead port (that is exactly what the
#   18-minute incident looked like). The provisional body has ready:false and NO
#   build label, so a poller can never mistake "still compiling" for "up".
#
#   Measured on the reference image: `busybox httpd` is NOT there (Alpine ships
#   it in busybox-extras: "httpd: applet not found"), and neither is curl — but
#   python3 and wget ARE. So the responder prefers python3's stdlib server, falls
#   back to a netcat loop, and says so when neither exists instead of pretending.
skygate_boot_responder_start() {
    _b383_port="${1:-8080}"
    B383_BOOT_DIR="$(mktemp -d 2>/dev/null)"
    [ -n "$B383_BOOT_DIR" ] || return 0
    cat > "$B383_BOOT_DIR/healthz" <<EOF
{"status":"booting","phase":"building","stage":"go-build","ready":false,"started":"${B383_BOOT_TS:-}","note":"skygate is compiling from source; this provisional endpoint disappears when the process takes over :${_b383_port}"}
EOF
    cat > "$B383_BOOT_DIR/index.html" <<EOF
<!doctype html><title>Skygate — building</title>
<h1>Skygate is building</h1>
<p>The container is compiling skygate from the bind-mounted source. This page
disappears as soon as the process takes over port ${_b383_port}. Reload in a
few seconds. Measured warm-cache cost: about 3 seconds.</p>
EOF

    if command -v python3 >/dev/null 2>&1; then
        ( cd "$B383_BOOT_DIR" && exec python3 -m http.server "$_b383_port" --bind 0.0.0.0 ) >/dev/null 2>&1 &
        B383_BOOT_PID=$!
    elif command -v nc >/dev/null 2>&1; then
        # netcat loop: one request at a time, good enough for a boot page.
        # `-l -p PORT` is GNU netcat, `-l PORT` is OpenBSD nc; try the first and
        # fall back to the second on the same port.
        (
            while :; do
                { printf 'HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nConnection: close\r\n\r\n'
                  cat "$B383_BOOT_DIR/healthz"; } | nc -l -p "$_b383_port" >/dev/null 2>&1 \
                || { printf 'HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nConnection: close\r\n\r\n'
                     cat "$B383_BOOT_DIR/healthz"; } | nc -l "$_b383_port" >/dev/null 2>&1
            done
        ) >/dev/null 2>&1 &
        B383_BOOT_PID=$!
    else
        echo "[init] WARN: neither python3 nor nc is available — no provisional /healthz during the build (the panel stays dark until the process starts)"
        B383_BOOT_PID=""
        return 0
    fi

    sleep 0.5
    if ! kill -0 "$B383_BOOT_PID" 2>/dev/null; then
        echo "[init] WARN: the provisional /healthz on :$_b383_port could not be started (port busy?) — the panel stays dark until the process starts"
        B383_BOOT_PID=""
        return 0
    fi
    # Prove it actually SERVES, not merely that a process exists.
    _b383_wget=""
    command -v wget >/dev/null 2>&1 && _b383_wget="wget"
    [ -z "$_b383_wget" ] && command -v busybox >/dev/null 2>&1 && _b383_wget="busybox wget"
    if [ -n "$_b383_wget" ]; then
        if $_b383_wget -qO- "http://127.0.0.1:$_b383_port/healthz" 2>/dev/null | grep -q '"phase":"building"'; then
            echo "[init] provisional /healthz answering on :$_b383_port while the build runs (pid $B383_BOOT_PID)"
        else
            echo "[init] WARN: the provisional responder is running but did not answer on :$_b383_port"
        fi
    else
        echo "[init] provisional /healthz on :$_b383_port (pid $B383_BOOT_PID; no wget to verify it)"
    fi
    return 0
}
skygate_boot_responder_stop() {
    [ -n "${B383_BOOT_PID:-}" ] || return 0
    kill "$B383_BOOT_PID" 2>/dev/null || true
    wait "$B383_BOOT_PID" 2>/dev/null || true
    B383_BOOT_PID=""
    [ -n "${B383_BOOT_DIR:-}" ] && rm -rf "$B383_BOOT_DIR" 2>/dev/null
    B383_BOOT_DIR=""
    return 0
}
# <<< B383 helpers (end)
# ---------------------------------------------------------------------------

# 2. Build skygate (existing flow, preserved verbatim from the
# pre-Tailscale entrypoint).
#
# 2026-09-07: V8 (B237.15) — prebuilt-image path. When
# `SKYGATE_PREBUILT=1` is set (which the Dockerfile.prebuilt
# does automatically), the binary is already in /app/skygate
# (copied in by the CI release workflow). Skip the
# go-mod-download + go-build step entirely:
#   - saves ~60s on first start
#   - removes the dependency on `git` + `go` in the runtime image
#     (Dockerfile.prebuilt doesn't include them, keeping the
#     image at ~30 MB instead of ~600 MB)
#   - removes the .git bind-mount requirement (the prebuilt
#     image doesn't need the source tree at all)
#
# Why detect SKYGATE_PREBUILT instead of testing for /app/skygate
# presence: the dev path (Dockerfile, single-stage, ./:/app bind-
# mount) DOES produce a /app/skygate after the first build. If we
# detected "binary exists → skip build", a stale /app/skygate
# from a prior run would prevent the new code from being built
# when the operator edits the source and restarts. The explicit
# env var is unambiguous: SKYGATE_PREBUILT=1 means "the CI built
# this binary, don't try to rebuild it from a bind-mount that
# may or may not exist".
#
# If the env var is NOT set, we fall through to the original
# in-container-build path (unchanged, for the operator's local
# ./:/app dev workflow).
if [ "${SKYGATE_PREBUILT:-0}" = "1" ]; then
    if [ ! -x /app/skygate ]; then
        echo "[init] SKYGATE_PREBUILT=1 but /app/skygate missing or not executable — image is broken" >&2
        exit 1
    fi
    echo "[init] SKYGATE_PREBUILT=1: using prebuilt binary ($(stat -c '%s' /app/skygate) bytes)"
else
    cd /app
    # B383: resolve the caches onto a path that SURVIVES a container recreate.
    # /data is a bind mount on the docker install, so the 570 MB module cache and
    # the 261 MB build cache stop being re-downloaded and re-compiled from
    # scratch every time the panel's updater recreates the container.
    GO_CACHE_ROOT="${SKYGATE_GO_CACHE_ROOT:-/data/go-caches}"
    if B383_DIRS="$(skygate_go_cache_dirs "$GO_CACHE_ROOT" "/go/pkg/mod" "/root/.cache/go-build")"; then
        echo "[init] Go caches on the persisted path ($GO_CACHE_ROOT)"
    else
        echo "[init] WARN: $GO_CACHE_ROOT is not usable — falling back to the container-local Go caches (a recreate will have to re-fetch modules)"
    fi
    GOMODCACHE="$(printf '%s' "$B383_DIRS" | cut -f1)"
    GOCACHE="$(printf '%s' "$B383_DIRS" | cut -f2)"
    export GOMODCACHE GOCACHE
    B383_STAMP="$GO_CACHE_ROOT/build.stamp"
    [ -d "$GO_CACHE_ROOT" ] || B383_STAMP=""
    echo "[init] GOMODCACHE=$GOMODCACHE GOCACHE=$GOCACHE"

    # One decision, captured once (the function prints WHY and returns 0=build).
    if B383_REASON="$(skygate_go_build_needed /app /app/skygate "$B383_STAMP" "${SKYGATE_FORCE_BUILD:-0}")"; then
        B383_BUILD=1
    else
        B383_BUILD=0
    fi
    # `SKYGATE_BUILD_PLAN=1` reports the decision and exits without touching
    # anything — the operator's answer to "why is it rebuilding?", and the
    # surface the contract can exercise on a real host.
    if [ "${SKYGATE_BUILD_PLAN:-0}" = "1" ]; then
        echo "[plan] decision   : $B383_REASON"
        echo "[plan] fingerprint: $(skygate_go_source_fingerprint /app)"
        echo "[plan] stamp      : ${B383_STAMP:-<none>}"
        echo "[plan] binary     : $(skygate_go_stamp_binary /app/skygate)"
        echo "[plan] forced     : ${SKYGATE_FORCE_BUILD:-0}"
        exit 0
    fi

    if [ "$B383_BUILD" = "1" ]; then
        B383_BOOT_TS="$(date -u +"%Y-%m-%dT%H:%M:%SZ")"
        echo "[init] build decision: $B383_REASON"
        skygate_boot_responder_start "${SKYGATE_PORT:-8080}"
        # git/openssh are already in the image (Dockerfile). Only reach for apk
        # when they are genuinely missing: the unconditional `apk add` used to
        # put the alpine CDN in the critical path of every container start.
        if ! command -v git >/dev/null 2>&1 || ! command -v ssh >/dev/null 2>&1; then
            apk add --no-cache openssh-client git 2>/dev/null || true
        fi
        echo "Downloading Go modules..."
        # A cold cache + a slow proxy used to hang here for 18 minutes with no
        # message. Bound it, and say what happened instead of failing silently:
        # `go mod download` is 0.03s when the cache is warm, so a timeout means
        # the cache is incomplete AND the proxy is unreachable.
        if ! timeout "${SKYGATE_GO_MOD_TIMEOUT:-900}" go mod download 2>&1; then
            echo "[init] ERROR: 'go mod download' failed or exceeded SKYGATE_GO_MOD_TIMEOUT=${SKYGATE_GO_MOD_TIMEOUT:-900}s." >&2
            echo "[init]        The module cache at $GOMODCACHE is incomplete and the Go proxy is unreachable from this container." >&2
            echo "[init]        Either restore egress (proxy.golang.org), point GOPROXY at a mirror, or pre-populate $GO_CACHE_ROOT on the host." >&2
            echo "[init]        Continuing: the build below will fail if a module is really missing." >&2
        fi
        # `go mod tidy` used to run on EVERY start against the operator's
        # bind-mounted checkout: it needs the network and it REWRITES go.mod /
        # go.sum (the three files rule 7 says must survive). Opt-in only now.
        if [ "${SKYGATE_GO_MOD_TIDY:-0}" = "1" ]; then
            echo "[init] go mod tidy (SKYGATE_GO_MOD_TIDY=1)"
            go mod tidy || true
        fi
        echo "Building Skygate..."
# 2026-07-11: inject build label from git so the web footer + telegram
# /version reflect the real tag/commit. .git is bind-mounted via
# docker-compose (`./:/app`); if it's missing (e.g. CI build from a
# tarball), fall back to "dev". The alpine image does NOT include
# git, so we install it via apk above.
# git 2.35+ refuses to operate on a repo whose owner doesn't match
# the current uid ("dubious ownership"). The host bind-mounts .git
# as uid 1000 while we run as root, so mark /app as safe explicitly.
#
# 2026-08-12: v1.3.1 (Phase 2 of SQLite removal) — REMOVED `-tags postgres`
# build tag. v1.3.0 (commit b1baa4a) deleted the `//go:build postgres`
# conditional; the only DB driver left is github.com/jackc/pgx/v5
# (pure Go). The runtime build is now CGO_ENABLED=0 (the default
# when no `import "C"` exists; verified by `grep -r "import \"C\"" cmd/
# internal/ 2>/dev/null` returning zero matches on 2026-08-12). Result:
# 24 MB static binary that doesn't link against musl/libc.
git config --global --add safe.directory /app
GIT_VER=$(git describe --tags --always 2>/dev/null || echo "dev")
GIT_COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")
if [ "${GIT_VER}" = "dev" ] || [ "${GIT_COMMIT}" = "unknown" ]; then
    echo "  WARN: build label not resolved (GIT_VER=${GIT_VER} GIT_COMMIT=${GIT_COMMIT})" >&2
fi
BUILD_TIME=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS="-X main.version=${GIT_VER} -X main.commit=${GIT_COMMIT} -X main.buildTime=${BUILD_TIME}"
echo "  version=${GIT_VER} commit=${GIT_COMMIT} built=${BUILD_TIME}"
B383_T0=$(date +%s)
go build -buildvcs=false -ldflags "${LDFLAGS}" -o /app/skygate ./cmd/skygate || { echo "BUILD FAILED"; exit 1; }
chmod +x /app/skygate
echo "  build finished in $(( $(date +%s) - B383_T0 ))s"
# Record the stamp only after a SUCCESSFUL build: fingerprint on line 1, the
# binary it produced on line 2.
if [ -n "$B383_STAMP" ]; then
    {
        printf '%s\n' "$(skygate_go_source_fingerprint /app)"
        printf '%s\n' "$(skygate_go_stamp_binary /app/skygate)"
    } > "$B383_STAMP" 2>/dev/null || echo "[init] WARN: could not write the build stamp $B383_STAMP"
fi
skygate_boot_responder_stop
    else
        echo "[init] build decision: $B383_REASON"
        echo "[init] reusing the existing binary — force a rebuild with SKYGATE_FORCE_BUILD=1"
    fi
    echo "Skygate ready, starting..."
fi  # end of the SKYGATE_PREBUILT gate (the build-block above only runs in dev mode)

# 3. SSH key setup (B202.5). The agent's ~/.ssh/ is bind-mounted
# at /etc/skygate/ssh_key. We copy id_ed25519 to /tmp/ssh_key
# and chmod 600 (ssh requires private keys to be 0600 — without
# this the transport's `ssh svi "pg_dump ..."` would fail with
# "Permissions 0755 are too open" regardless of which user runs
# the ssh inside the container).
#
# Workaround: this host's overlayfs turns single-file bind-mounts
# into empty directories (a kernel issue on the agent's docker
# version). When the bind mount is empty inside the container,
# we fall back to reading the key from the host via a different
# mechanism: the `docker.sock` mount + the docker CLI, then
# `docker cp` from the HOST filesystem (passed via a host
# bind-mount at /host-keys, which IS a directory mount that
# works correctly on this host's overlayfs). The /host-keys
# path is set up by docker-compose.yml to point at the host's
# .ssh/ directory; we cat the id_ed25519 from there.
if [ -f /etc/skygate/ssh_key/id_ed25519 ]; then
    cp /etc/skygate/ssh_key/id_ed25519 /tmp/ssh_key
    chmod 600 /tmp/ssh_key
elif [ -f /host-keys/id_ed25519 ]; then
    # overlayfs workaround: read from the host's .ssh/ via a
    # directory mount at /host-keys (which works on this host's
    # overlayfs where single-file mounts become empty dirs).
    echo "[init] B202.5: cat /host-keys/id_ed25519 > /tmp/ssh_key (overlayfs workaround)"
    cp /host-keys/id_ed25519 /tmp/ssh_key
    chmod 600 /tmp/ssh_key
fi

# 3. Exec skygate as PID 1. tailscaled (if running) is orphaned to
# PID 1 (= skygate now) and continues serving; when the container
# exits docker sends SIGTERM to PID 1 and SIGKILL to the rest after
# the grace period, so tailscaled doesn't leak.
exec /app/skygate
