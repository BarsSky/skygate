// Package admin — tailscale.go owns the /admin/tailscale page
// (status, auth key paste, start/stop tailscaled).
//
// v0.33.1.9: closes the "skygate can't reach api.telegram.org
// because Tailscale isn't running in the container and I
// can't SSH in to fix it" gap. The web UI lets the admin
// paste a headscale preauth key, writes it to /data/ts/authkey,
// and starts tailscaled + tailscale up --accept-routes inside
// the running container — all from the browser, no SSH.
//
// File map (this 1963-line file was split in refactor Phase D,
// 2026-10-01 — the preauth-key resolution, the runtime probes, the
// HTTP handlers and the B236 advertise-routes rules had nothing to do
// with each other). Contracts pin the package and the named symbols,
// not one file's line layout:
//   - tailscale.go                  — this doc, hostname → headscale user
//     resolution (the canonical helper, B259.1) and the UI state shape
//     + its 5s cache
//   - tailscale_config.go           — DB/env-resolved settings and their
//     sources
//   - tailscale_runtime.go          — status probes, the auth-key file,
//     start/up/stop
//   - tailscale_handlers.go         — GET + POST dispatch, save/start/stop
//   - tailscale_enable.go           — generate key, enable/disable in the
//     container (B259)
//   - tailscale_restart_env.go      — restart skygate + the .env rewrite
//     (B323)
//   - tailscale_advertise_routes.go — B236 advertise-routes management

package admin

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"sync"
	"time"

	"skygate/internal/headscale"
	"skygate/internal/tsstate"
)

// 2026-08-05 v0.33.1.11 — tailscale auth key auto-generation.
//
// findUserForHostname resolves the headscale user that should
// own a preauth key for the configured Tailscale hostname
// (default "skygate-host").
//
// ────────────────────────────────────────────────────────────────────
// ⚠️  THIS IS THE CANONICAL HELPER for hostname → headscale user
// mapping. DO NOT inline a "find user by hostname" lookup anywhere
// else. B259 (2026-09-16) shipped with `generateAndWriteTailscaleKeyForEnable`
// duplicating weaker logic (u.Name == hostname || u.Name ==
// strings.TrimSuffix(hostname, "-1")), which forced the operator
// to manually create a phantom `skygate-host` headscale user.
// That violated B251's invariant. B259.1 (2026-09-17) collapsed
// the duplicate logic to a single call here.
//
// If you need hostname → headscale user anywhere in skygate —
// /admin/headscale, /admin/devices, exit-rule per-host lookups,
// anything — CALL THIS FUNCTION. The B-check
// `scripts/check_b259_tailscale_toggle.sh` (section J)
// source-greps for inline `u.Name == hostname` patterns
// outside this function and fails the deploy if it finds any.
// ────────────────────────────────────────────────────────────────────
//
// Two paths, depending on hostname:
//
//  1. Reserved hostname "skygate-host" (B251):
//     Always returns the headscale user that backs the
//     `infra` portal user (default `id=85`). This is the
//     single VM that runs the skygate container itself,
//     and it's always infra-owned per the operator's 2026-08-13
//     directive ("infra user будет владеть skygate + exit nodes").
//     We do NOT search the live node list, because the
//     pre-B251 behaviour fell back to whatever headscale
//     user happened to have a node with that hostname — so
//     if the operator ever temporarily moved `skygate-host`
//     under `skyadmin` (a deprecated path), the next "Generate
//     Auth Key" click would mint a key for `skyadmin`, which
//     would re-bind the production VM to a different portal
//     user. Skipping the search makes the binding canonical.
//
//  2. Any other hostname: walks the live headscale node list
//     and returns the UserID of the first node that matches.
//     The previous behaviour (kept for non-reserved
//     hostnames) is the most reliable signal: a fresh
//     container doesn't necessarily have a User row in
//     headscale yet — the user is created on the first node
//     registration. Looking for a node with the matching
//     hostname (via the admin's ListAllNodes path) tells us
//     who this skygate instance previously registered as.
//
// Returns the headscale user ID (int64, suitable for
// CreatePreauthKey's userID param) and the user.Name for
// audit / flash messages. The 4-second timeout matches
// ListAllNodes' internal cap so a stuck headscale doesn't
// hang the page request.
func (s *Service) findUserForHostname(ctx context.Context, hs *headscale.Client, hostname string) (int64, string, error) {
	if hs == nil {
		return 0, "", fmt.Errorf("headscale client not configured")
	}
	// B251 reserved-name shortcut: pin to infra unconditionally.
	if hostname == "skygate-host" {
		uid, err := s.infraHeadscaleUserID(ctx)
		if err != nil {
			return 0, "", err
		}
		return uid, "infra", nil
	}
	listCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	_ = listCtx
	nodes, err := hs.ListAllNodes()
	if err != nil {
		return 0, "", fmt.Errorf("list nodes: %w", err)
	}
	for _, n := range nodes {
		if n.Hostname != hostname {
			continue
		}
		if n.UserID == "" {
			continue
		}
		uid, perr := strconv.ParseInt(n.UserID, 10, 64)
		if perr != nil {
			return 0, "", fmt.Errorf("user id %q for hostname %q is not numeric: %w", n.UserID, hostname, perr)
		}
		return uid, n.UserName, nil
	}
	return 0, "", fmt.Errorf("no node with hostname %q in headscale (register once first, or use /admin/headscale to create a preauth key manually)", hostname)
}

// infraHeadscaleUserID returns the headscale user ID backing
// the `infra` portal user. B251 added this helper so the
// reserved-name path in findUserForHostname has one place to
// look up the canonical mapping instead of inlining the
// DB query. Looks up `portal_users.username='infra'` and
// returns its `headscale_user_id` column.
//
// Returns (0, error) when:
//   - no `infra` row exists (operator hasn't run ensureInfraUser
//     yet — skygate boot pre-flight must create the row first)
//   - `headscale_user_id` is NULL or 0 (ensureInfraUser hasn't
//     linked yet, OR the test schema defaulted it to 0)
//   - the portal_users table itself isn't reachable
func (s *Service) infraHeadscaleUserID(ctx context.Context) (int64, error) {
	conn := s.dbc()
	if conn == nil {
		return 0, fmt.Errorf("infra lookup: nil db source")
	}
	row := conn.QueryRowContext(ctx,
		`SELECT headscale_user_id FROM portal_users WHERE username = 'infra'`)
	var uid sql.NullInt64
	if err := row.Scan(&uid); err != nil {
		if err == sql.ErrNoRows {
			return 0, fmt.Errorf("infra user missing in portal_users (run ensureInfraUser before /admin/tailscale)")
		}
		return 0, fmt.Errorf("infra lookup: %w", err)
	}
	if !uid.Valid || uid.Int64 == 0 {
		return 0, fmt.Errorf("infra headscale_user_id not linked (ensureInfraUser hasn't completed)")
	}
	return uid.Int64, nil
}

// TailscaleState is the shape the template consumes.
type TailscaleState struct {
	// Available = true when the tailscaled + tailscale binaries
	// are in the container's PATH. False in a "non-RF" build
	// that omits them — the page renders a fallback note.
	Available bool
	// Running = true when the tailscaled process is alive
	// (i.e. there's a PID + the unix socket exists).
	Running bool
	// TailnetIP is the Tailscale IP of this skygate instance
	// (e.g. "100.64.100.10"). "" when not running or not yet
	// authenticated.
	TailnetIP string
	// AcceptedRoutes is the list of CIDRs that skygate has
	// accepted (i.e. the relay's advertised Telegram ranges).
	// Empty when not running.
	AcceptedRoutes []string
	// AdvertisedRoutes is the list of CIDRs that THIS skygate
	// instance advertises to the rest of the tailnet
	// (i.e. the result of `tailscale set --advertise-routes=`).
	// Read from `tailscale status --json` .Prefs.AdvertiseRoutes
	// (which is what was requested) AND/OR
	// .Self.PrimaryRoutes (what headscale has actually
	// approved). B236 surfaces them on /admin/tailscale
	// so the operator can manage them without SSH.
	//
	// 2026-09-04: v0.69.1 (B236) — closes the gap where
	// skygate-host-1 was advertising 192.168.13.0/24 (its
	// own LAN) and the operator had no UI to remove it.
	AdvertisedRoutes []string
	// AdvertisedRoutesApproved is the same list after
	// headscale's approval pass (what's actually in the
	// tailnet, vs what was requested). May differ from
	// AdvertisedRoutes when headscale's policy rejects
	// some routes.
	AdvertisedRoutesApproved []string
	// AdvertisedRoutesSource is "prefs" or "self" —
	// which JSON field the AdvertisedRoutes value came
	// from. Prefs is preferred (shows the operator's
	// intent); falls back to Self.PrimaryRoutes when
	// Prefs is empty.
	AdvertisedRoutesSource string
	// BackendState is the parsed Tailscale state string:
	//   - "Running"      = healthy
	//   - "NeedsLogin"   = tailscaled up but no key auth yet
	//   - "Stopped"      = process is dead
	//   - ""             = not available / not running
	BackendState string
	// AuthKeySet = true when the file at
	// s.TailscaleAuthKeyPath is non-empty.
	AuthKeySet bool
	// AuthKeyPath mirrors s.tailscaleAuthKeyPath() (which
	// itself checks DB first, env var, then default). The
	// template can render "Edit key at <path>" without
	// needing direct Service access.
	AuthKeyPath string
	// AuthKeyPathSource is "db", "env", or "default" — same
	// shape as LoginServerSource. B259.
	AuthKeyPathSource string
	// AuthKeyFP is a short fingerprint of the stored key
	// (e.g. "abcd...wxyz", first 4 + last 4). Used so the
	// admin can see "a key is set" without exposing the
	// full secret in the rendered HTML.
	AuthKeyFP string
	// AuthKeyDisabled = true when the operator explicitly
	// disabled the in-container Tailscale via env config
	// (typically SKYGATE_TS_AUTHKEY_FILE=/dev/null). When
	// true the page shows a banner "Tailscale is
	// intentionally disabled by env config" and the Start
	// button is hard-disabled — the operator cannot start
	// tailscaled from the UI without first editing
	// docker-compose.yml + restarting the container. B258.
	AuthKeyDisabled bool
	// AuthKeyMissing = true when the configured auth-key
	// path is a regular file path (NOT /dev/null or another
	// disabled sentinel) AND the file at that path either
	// doesn't exist or is empty. This is a third visual
	// state that B258 did not model — pre-B258.1 the UI
	// rendered the "Enabled" branch (with Start clickable)
	// which then errored out with "read auth key: no such
	// file" when clicked. B258.1 closes that gap with a
	// dedicated "key missing" banner, a hard-disabled Start
	// button, and a paste-form for the operator to drop a
	// key into the existing path.
	AuthKeyMissing bool
	// LoginServer mirrors s.TailscaleLoginServer.
	LoginServer string
	// LoginServerSource is "db" when the value came from the
	// web-UI's persisted global_settings row (takes
	// precedence over the env var), or "env" when the
	// value still falls back to s.TailscaleLoginServer
	// (the SKYGATE_TS_LOGIN_SERVER env var). Template
	// uses this to render a "source: web-UI/DB" vs
	// "source: env var" hint so the operator knows which
	// value actually wins.
	// v0.33.1.13.
	LoginServerSource string
	// Hostname mirrors s.TailscaleHostname.
	Hostname string
	// StateDir is where tailscaled keeps its state file
	// (/var/lib/tailscale by default). Bind-mounted from
	// the host so state survives container restarts.
	StateDir string
	// LastError is the last error from start/stop (or empty).
	// Cleared on every successful action.
	LastError string
	// ---- B318: why the daemon is NOT running ----
	//
	// The page used to render the "really enabled" branch (a green card with a
	// clickable Start) whenever AuthKeyDisabled/AuthKeyMissing were false, even with
	// the daemon dead — so an operator saw "configured" and read it as "works",
	// while /admin/exit-nodes read the same daemon as absent. Both facts below come
	// from internal/tsstate and are rendered together with the Start button.
	//
	// EnvDisabled: SKYGATE_TS_AUTHKEY_FILE as the CONTAINER ENTRYPOINT sees it is
	// empty or the /dev/null sentinel, so tailscaled was skipped at container start
	// and will be skipped again on the next restart. The DB override
	// (tailscale.auth_key_path) changes only what this page and its Start button do.
	EnvDisabled bool
	// DSentinel is that env value, for the banner ("created with …=/dev/null").
	DSentinel string
	// TunPresent: /dev/net/tun exists in this container. False means tailscaled can
	// never create its interface, whatever the operator presses.
	TunPresent bool
	// DaemonError is the last failure recorded in the tailscaled log (best effort).
	DaemonError string
	// NotRunningReason is the composed explanation shown when Configured && !Running.
	NotRunningReason string
	// ---- B320: the reserved name must belong to the LIVE client ----
	//
	// The agent VM carried a dead registration named `skygate-host` (offline for days,
	// a different machine key) while the RUNNING client was `skygate-host-1`, because
	// `.env`/compose still pin the v0.33.1.9 placeholder. Every "is this me?" check
	// (infra ownership, colocation sanity, the SSH-source ACL) matches the canonical
	// name by strict equality, so they were all keying off the ghost.
	SelfName SelfNameState
}

// tailscaleStateMu guards the status cache so concurrent
// /admin/tailscale GETs don't all shell out to `tailscale status`
// at the same time. The cache is short (5s) and invalidated on
// any state-changing POST.
var (
	tailscaleStateMu  sync.Mutex
	tailscaleStateAt  time.Time
	tailscaleStateVal TailscaleState
)

const tailscaleStateTTL = 5 * time.Second

// loadTailscaleState reads the current state from the
// container. Cached for 5s to avoid hammering `tailscale status`
// on every page render.
//
// Lock discipline: the cache check + cache write both
// happen under tailscaleStateMu. The slow `readTailscaleState`
// call (which may exec `tailscale status` and block for ~1s
// over a slow TUN) runs WITHOUT the lock — otherwise a
// slow probe would block every concurrent page render and
// the deferred Unlock on the cache-write path would Unlock
// an already-unlocked mutex (the v0.33.1.9 first cut did
// exactly that and immediately panicked on the first GET).
func (s *Service) loadTailscaleState() TailscaleState {
	tailscaleStateMu.Lock()
	if !tailscaleStateAt.IsZero() && time.Since(tailscaleStateAt) < tailscaleStateTTL {
		st := tailscaleStateVal
		tailscaleStateMu.Unlock()
		return st
	}
	tailscaleStateMu.Unlock()

	// Cache miss / stale — re-probe without holding the lock.
	st := s.readTailscaleState()

	tailscaleStateMu.Lock()
	tailscaleStateVal = st
	tailscaleStateAt = time.Now()
	tailscaleStateMu.Unlock()
	return st
}

// invalidateTailscaleState forces the next loadTailscaleState
// to actually run a fresh probe. Called by every action that
// can change the running state (start, stop, save_key).
func (s *Service) invalidateTailscaleState() {
	tailscaleStateMu.Lock()
	tailscaleStateAt = time.Time{}
	tailscaleStateMu.Unlock()
}

// readTailscaleState is the uncached worker. Safe to call
// from anywhere; doesn't touch the cache.
func (s *Service) readTailscaleState() TailscaleState {
	st := TailscaleState{
		AuthKeyPath:       s.tailscaleAuthKeyPath(),
		AuthKeyPathSource: s.tailscaleAuthKeyPathSource(),
		LoginServer:       s.tailscaleLoginServer(),
		LoginServerSource: s.tailscaleLoginServerSource(),
		Hostname:          s.tailscaleHostname(),
		StateDir:          s.tailscaleStateDir(),
	}
	st.Available = tailscaleAvailable()
	st.AuthKeyDisabled = s.tailscaleAuthKeyDisabled()
	st.AuthKeySet, st.AuthKeyFP = s.readTailscaleAuthKey()
	// B258.1: "missing" state. Computed AFTER AuthKeyDisabled
	// and AuthKeySet so the three states are mutually
	// exclusive and cover all reachable configurations:
	//   - AuthKeyDisabled=true  → "intentionally off" (B258)
	//   - AuthKeyMissing=true   → "configured but no key" (B258.1)
	//   - else                  → "configured + key set"
	st.AuthKeyMissing = !st.AuthKeyDisabled && !st.AuthKeySet
	// B318: the facts behind "configured but not running".
	boot := tsstate.Detect(st.StateDir)
	st.EnvDisabled = boot.EnvDisabled
	st.DSentinel = boot.AuthKeyFileEnv
	st.TunPresent = boot.TunPresent
	st.DaemonError = boot.LastDaemonError
	if !st.Available {
		return st
	}
	running, ip, routes, backendState, err := tailscaleStatus()
	if err != nil {
		st.LastError = err.Error()
		st.NotRunningReason = boot.Explain()
		return st
	}
	st.Running = running
	if !running {
		st.NotRunningReason = boot.Explain()
	}
	// B320: the name the daemon registered with, and whether a dead registration is
	// squatting the canonical one.
	st.SelfName = s.selfNameState(tailscaleSelfHostname())
	st.TailnetIP = ip
	st.AcceptedRoutes = routes
	st.BackendState = backendState
	// B236: read advertised routes (separate call so the
	// existing tailscaleStatus() signature stays unchanged
	// — the call is cheap, the JSON is already cached by
	// the kernel, and the split keeps the diff small).
	if adv, approved, source := tailscaleAdvertisedRoutes(); adv != nil {
		st.AdvertisedRoutes = adv
		st.AdvertisedRoutesApproved = approved
		st.AdvertisedRoutesSource = source
	}
	return st
}
