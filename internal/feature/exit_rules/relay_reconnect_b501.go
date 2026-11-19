// relay_reconnect_b501.go — B501 (2026-11-19): «переподключить relay» как действие
// администратора, а не как SSH-сессия.
//
// ПОЧЕМУ ЭТО НЕ ИЗБЫТОЧНО. Живой случай: `karolina` держала `Nearest DERP: New York
// City`, хотя собственный регион 900 (`mow`) отвечал; причина — устаревшая карта DERP в
// её tailscaled, лечится перезапуском демона. Та же операция нужна в «спорных ситуациях
// по доступу» (relay не отвечает по одному транспорту, но жив) и после обновления (relay
// с устаревшим netmap перестаёт быть кандидатом в DERP). Раньше это требовало ssh-сессии:
// действие не повторяемое, не проверяемое и не в аудите.
//
// ГРАНИЦА, КОТОРУЮ НЕЛЬЗЯ РАЗМЫВАТЬ: переподключить можно RELAY (skygate ходит на него
// по проверенной лестнице транспортов). Телефон переподключить с сервера НЕЛЬЗЯ —
// `tailscaled` там недоступен, и любая кнопка, обещающая это, была бы ложью. Для
// устройства остаётся честный ответ: переподключение делает владелец на самом устройстве
// (и `scripts/device_connectivity.sh` показывает, какой слой сломан).
package exit_rules

import (
	"fmt"
	"log"
	"strings"

	"skygate/internal/headscale"
)

// RelayReconnectResult is what the page reports back.
type RelayReconnectResult struct {
	Hostname string
	// OK is true only when the relay ANSWERED afterwards with a parsed status; a
	// successful ssh exit is not evidence that the relay came back (B451's rule).
	OK       bool
	Via      string // transport that carried the command: tailnet / public / name / tailnet-jump
	Endpoint string
	// Region is the DERP region the relay reported after reconnecting ("mow"). It is the
	// fact that made this action worth building: "reconnected" without it is a guess.
	Region string
	// HostName and Online come from the relay's own view of itself.
	HostName string
	Online   bool
	Err      string
	// Attempts lists every transport that was tried, in order, so a failure names the
	// paths instead of saying "unreachable".
	Attempts []string
}

// Summary renders one line for the flash banner.
func (r RelayReconnectResult) Summary() string {
	if r.OK {
		region := r.Region
		if region == "" {
			region = "unknown"
		}
		online := "offline"
		if r.Online {
			online = "online"
		}
		return fmt.Sprintf("%s reconnected via %s (%s), DERP region: %s, %s",
			r.Hostname, r.Via, r.Endpoint, region, online)
	}
	msg := r.Err
	if msg == "" {
		msg = "the relay did not answer after the restart"
	}
	if len(r.Attempts) > 0 {
		msg += " — tried: " + strings.Join(r.Attempts, "; ")
	}
	return r.Hostname + ": " + msg
}

// ReconnectRelay restarts the relay's tailscaled over the proven transport ladder and
// reports the region it came back on.
//
// It refuses a relay that IS this host: skygate runs unprivileged and must not try to
// restart the daemon it shares a host with — that operator action belongs on the host.
func (s *Service) ReconnectRelay(node string) RelayReconnectResult {
	res := RelayReconnectResult{Hostname: node}
	if strings.TrimSpace(node) == "" {
		res.Err = "empty relay hostname"
		return res
	}
	if s.HS == nil {
		res.Err = "headscale client is not configured"
		return res
	}
	// The same locality evidence the route apply uses (B293): a relay on this very host
	// must be reported, not silently attempted.
	if placement := headscale.DetectRelayPlacement(liveExitNodeIPs(s.HS, node)); placement.Local {
		res.Err = "this relay IS the panel host — restart tailscaled on the host directly (skygate runs unprivileged and does not restart its own neighbour)"
		return res
	}

	cfg := lookupRelaySSHConfig(s.dbc(), node)
	keyPath := cfg.KeyPath
	if strings.TrimSpace(keyPath) == "" {
		keyPath = s.Cfg.SSHKeyPath
	}
	if cfg.TailscaleIP == "" {
		if ip := liveExitNodeIP(s.HS, node); ip != "" {
			cfg.TailscaleIP = ip
		}
	}

	cands, notes := relaySSHEndpoints(s.HS, cfg, node)
	cands, jumpNotes := appendJumpCandidates(s.dbc(), s.HS, cfg, node, cands)
	res.Attempts = append(res.Attempts, notes...)
	res.Attempts = append(res.Attempts, jumpNotes...)
	if len(cands) == 0 {
		res.Err = "no usable transport is configured for this relay (no tailnet address, no ssh_target)"
		return res
	}

	for _, ep := range cands {
		probe := ep.ProbeEndpoint()
		if err := ProbeRelayEndpoint(probe, relayProbeTimeout); err != nil {
			res.Attempts = append(res.Attempts, ep.Label()+" is not answering: "+err.Error())
			continue
		}
		out, err := s.HS.ExecOnRelay(ep.Target(), keyPath, ep.Jump, headscale.RelayReconnectCommand)
		if err != nil {
			res.Attempts = append(res.Attempts, ep.Label()+": "+err.Error())
			log.Printf("exit-relay reconnect(%s): %s could not run the restart: %v — trying the next transport", node, ep.Label(), err)
			continue
		}
		st, perr := headscale.ParseRelaySelfState(out)
		if perr != nil {
			res.Attempts = append(res.Attempts, ep.Label()+": "+perr.Error())
			log.Printf("exit-relay reconnect(%s): the restart ran over %s but its result could not be read: %v", node, ep.Label(), perr)
			continue
		}
		res.OK = true
		res.Via = ep.Kind
		res.Endpoint = ep.Label()
		res.Region = st.Relay
		res.HostName = st.HostName
		res.Online = st.Online
		log.Printf("exit-relay reconnect(%s): restarted over the %s transport (%s); back as %q, DERP region %q, online=%v",
			node, ep.Kind, ep.Label(), st.HostName, st.Relay, st.Online)
		return res
	}

	res.Err = "no transport could restart the relay"
	return res
}
