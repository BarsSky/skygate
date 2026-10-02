// telegram_state.go — what /admin/telegram needs to know before it draws.
//
// Split out of telegram.go in refactor Phase D (2026-10-01): the UI state shape,
// the container-tailscale shape, the egress shape and the loader that fills them
// from the DB, the container and the probe cache. Keeping the reader apart from
// the writers is what makes "the page shows X" auditable.

package admin

import (
	"os"
	"strings"
	"time"

	"skygate/internal/db"
	"skygate/internal/headscale"
)

// telegramUIState is the shape the template consumes.
type telegramUIState struct {
	Configured    bool
	TokenFP       string
	ChatID        string
	UpdatedAt     string
	StrictMode    bool
	LoginTokenTTL int
	Probe         TelegramProbeResult
	// Egress carries the v0.33.1.8 "which relay runs
	// Telegram-CIDR" selector. SelectedNodeID is the headscale
	// node_id of the currently chosen exit-node ("" when none).
	// SelectedHostname is the friendly name rendered in the
	// "currently selected" line. Available is the list of every
	// enabled exit-node the admin can pick from (sourced from
	// exit_servers via db.ListExitServers).
	Egress EgressState
	// Container (B185) carries the live tailscaled state
	// inside the skygate container — RouteAll / AdvertiseTags
	// / ExitNodeID / TailscaleIPs. Filled by
	// readContainerTailscaleState which `docker exec`s into
	// the container and parses `tailscale status --json`.
	// When the container is unreachable (no docker socket
	// mount, wrong hostname, etc.) the helper returns a
	// state with Available=false and the page shows a
	// "container diagnostic not available" note instead
	// of failing the page render.
	Container ContainerTailscaleState
}

// ContainerTailscaleState is the diagnostic snapshot of the
// skygate container's tailscaled — the field that decides
// whether the container will accept subnet routes from the
// chosen egress relay. If RouteAll is false the Telegram
// probe will be "unreachable" no matter what the relay
// advertises; the "Re-apply accept-routes" button is the
// one-click fix (calls `tailscale set --accept-routes=true`
// inside the container, which updates the persisted state
// without the "requires mentioning all non-default flags"
// gotcha that breaks `tailscale up` when the state already
// has --advertise-tags set).
//
// 2026-08-25 (B185): replaces the "ssh to relay + run
// update-routes.sh + check 4 things by hand" runbook
// with a single page that shows the live container state
// + a button to fix the most common break (RouteAll=false).
type ContainerTailscaleState struct {
	Available      bool
	Hostname       string
	BackendState   string
	IP4            string
	IP6            string
	RouteAll       bool
	AdvertiseTags  []string
	ExitNodeID     string
	HasAcceptIssue bool   // RouteAll=false OR no AdvertiseTags
	RawStderr      string // last `tailscale status` / `docker exec` error
}

// EgressState is the per-page state for the egress-relay card.
// Lives in telegram.go (kept private) so the template can read
// it via {{.State.Egress.SelectedHostname}} etc.
type EgressState struct {
	SelectedNodeID   string
	SelectedHostname string
	Available        []db.ExitServer
	// B265 (2026-09-19): skygate runs ON one (or more) of these relay
	// hosts. Two consequences the operator must be told about:
	//   * selecting that relay as `telegram.egress_node_id` routes
	//     skygate's OWN management traffic through a node it manages —
	//     a broken relay then takes the panel down with it;
	//   * the api.telegram.org probe on this page originates from THIS
	//     host (the container uses the host's network stack), so an
	//     egress/split-routing policy this host cannot traverse makes
	//     the probe fail while every other tailnet device works. That
	//     path must be configured from a client, not from here.
	ColocationHostnames []string
	// ColocationHostnamesText is the ", "-joined form for the template
	// (text/template has no join builtin in this project's funcmap).
	ColocationHostnamesText string
	ColocationWarning       bool
	// B293 (2026-09-23): the co-located relay IS this machine's own tailscaled
	// node (verified from the live daemon, not from a hostname guess — the
	// operator's `aro` runs headscale + skygate + the exit node in one place,
	// `tailscale status` reports Self.HostName=exit-node-vps with 100.64.0.1).
	//
	// This is a DIFFERENT warning from B265's. Two facts flip:
	//   * selecting it as `telegram.egress_node_id` cannot route anything: the
	//     relay's egress IS this host's egress, so the selection is a no-op at
	//     best and a loop at worst;
	//   * the api.telegram.org probe on this page becomes REPRESENTATIVE (it
	//     measures exactly the path the bot will use), not untrustworthy.
	// If Telegram is unreachable from here, the answer is a DIFFERENT relay.
	LocalSelfRelay     bool
	LocalSelfHostnames []string
	LocalSelfText      string
}

func (s *Service) loadTelegramUIState() telegramUIState {
	token, chatID, ok, err := db.LoadTelegramToken(s.dbc())
	state := telegramUIState{
		LoginTokenTTL: db.LoadTelegramLoginTokenTTL(s.dbc()),
		StrictMode:    db.LoadTelegramStrictMode(s.dbc()),
	}
	// v0.33.1.8: load the egress selector BEFORE the early
	// return. The operator may want to pre-configure which
	// relay terminates api.telegram.org traffic BEFORE the
	// bot token is saved (e.g. the order of operations is
	// "fix the network path first, then enable the bot").
	// The previous layout (after the early return) made the
	// Egress card disappear until a token was saved, which
	// was a chicken-and-egg UX trap on the
	// "Telegram-egress unreachable" path.
	if v, gerr := db.GetGlobalSetting(s.dbc(), "telegram.egress_node_id", ""); gerr == nil {
		state.Egress.SelectedNodeID = v
	}
	if relays, lerr := db.ListExitServers(s.dbc()); lerr == nil {
		for _, e := range relays {
			if e.Enabled {
				state.Egress.Available = append(state.Egress.Available, e)
			}
		}
	}
	if state.Egress.SelectedNodeID != "" {
		for _, e := range state.Egress.Available {
			if e.NodeID == state.Egress.SelectedNodeID {
				state.Egress.SelectedHostname = e.Hostname
				break
			}
		}
		if state.Egress.SelectedHostname == "" {
			if h, herr := db.LookupExitServerHostname(s.dbc(), state.Egress.SelectedNodeID); herr == nil {
				state.Egress.SelectedHostname = h
			}
		}
	}

	// B265: flag egress relays that live on THIS host (see the
	// EgressState.Colocation* fields). We derive this host's identities
	// from skygate's own exit_servers rows + the env/config hostname and
	// compare them with the available relays' hostnames. Best-effort —
	// an empty result simply means "no overlap detected".
	if selfIPs := SelfExitNodeIdentities(s.dbc(), os.Getenv("SKYGATE_TS_HOSTNAME")); len(selfIPs) > 0 {
		selfSet := map[string]bool{}
		for _, ip := range selfIPs {
			selfSet[strings.ToLower(strings.TrimSpace(ip))] = true
		}
		// B293: split the co-located relays into "this machine's own tailscale
		// node" (verified against the live daemon OR the local interface
		// addresses — B293.1, so it also works for an unprivileged service user)
		// and the rest (B265's warning).
		selfAddrs := headscale.LocalSelfIPs()
		for _, e := range state.Egress.Available {
			if !selfHostMatches(e, selfSet) {
				continue
			}
			if len(selfAddrs) > 0 {
				if _, isSelf := headscale.IsLocalRelay(headscale.LocalSelf{IPs: selfAddrs}, splitCommaList(e.TailscaleIP)); isSelf {
					state.Egress.LocalSelfRelay = true
					state.Egress.LocalSelfHostnames = append(state.Egress.LocalSelfHostnames, e.Hostname)
					continue
				}
			}
			state.Egress.ColocationHostnames = append(state.Egress.ColocationHostnames, e.Hostname)
		}
		state.Egress.LocalSelfText = strings.Join(state.Egress.LocalSelfHostnames, ", ")
		state.Egress.ColocationWarning = len(state.Egress.ColocationHostnames) > 0
		state.Egress.ColocationHostnamesText = strings.Join(state.Egress.ColocationHostnames, ", ")
	}

	if err != nil || !ok {
		return state
	}
	state.Configured = true
	state.TokenFP = db.TelegramFingerprint(token)
	state.ChatID = chatID
	var ts int64
	row := s.dbc().QueryRow(`SELECT MAX(updated_at) FROM global_settings WHERE key IN ($1, $2)`,
		"telegram.bot_token", "telegram.chat_id")
	if err := row.Scan(&ts); err == nil && ts > 0 {
		state.UpdatedAt = time.Unix(ts, 0).UTC().Format("2006-01-02 15:04:05 UTC")
	}
	// 2026-09-16 (B255): the container's tailscaled
	// state used to be read here via
	// readContainerTailscaleState(), which shells out to
	// `docker exec skygate-skygate-1 tailscale status
	// --json` (~1-3s on a healthy host, ~8s if
	// tailscaled isn't running). That blocked page
	// render. The state is now filled in by the bg
	// handler AdminTelegramContainerBg + the JS in
	// admin/telegram.html, NOT here.
	//
	// state.Container intentionally stays at its zero
	// value (Available=false) until the bg handler fires
	// — the template's id="telegram-container-slot" is
	// rendered empty + the bg response replaces it.
	return state
}
