// telegram_container.go — the container's own Tailscale, and how it is rendered.
//
// Split out of telegram.go in refactor Phase D (2026-10-01): reading the state
// file, running `tailscale` inside the container, the B258.1 re-apply route and
// the two HTML fragments the page fetches asynchronously.

package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"skygate/internal/auth"
	"skygate/internal/i18n"
)

// readContainerTailscaleState (B185) shells out to
// `docker exec <container> tailscale status --json` and
// returns a structured snapshot. Returns Available=false
// when the container isn't reachable (no docker socket
// mounted, wrong hostname, tailscaled not running) so
// the template can render a clean "diagnostic not
// available" message instead of failing the page.
//
// The `tailscale set --accept-routes=true` recovery
// action (handleTelegramReapplyAcceptRoutes) is the
// same path the B185 entrypoint fix takes on every
// container restart, so the manual "Re-apply" button
// is only needed when the operator restarted the
// container after the entrypoint fix was deployed but
// the persisted state still has RouteAll=false (e.g.
// tailscale set --accept-routes=false was run by hand
// at some point).
func readContainerTailscaleState(containerName string) ContainerTailscaleState {
	var out ContainerTailscaleState
	out.Hostname = containerName
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "exec", containerName,
		"tailscale", "status", "--json")
	stdout, err := cmd.Output()
	if err != nil {
		out.RawStderr = err.Error()
		return out
	}
	var raw struct {
		BackendState string   `json:"BackendState"`
		TailscaleIPs []string `json:"TailscaleIPs"`
		Self         struct {
			HostName string   `json:"HostName"`
			Tags     []string `json:"Tags"`
		} `json:"Self"`
		Prefs struct {
			RouteAll      bool     `json:"RouteAll"`
			AdvertiseTags []string `json:"AdvertiseTags"`
			ExitNodeID    string   `json:"ExitNodeID"`
		} `json:"Prefs"`
	}
	if err := json.Unmarshal(stdout, &raw); err != nil {
		out.RawStderr = "parse: " + err.Error()
		return out
	}
	out.Available = true
	out.BackendState = raw.BackendState
	if raw.Self.HostName != "" {
		out.Hostname = raw.Self.HostName
	}
	for _, ip := range raw.TailscaleIPs {
		if strings.Contains(ip, ":") {
			out.IP6 = ip
		} else {
			out.IP4 = ip
		}
	}
	out.RouteAll = raw.Prefs.RouteAll
	if len(raw.Prefs.AdvertiseTags) > 0 {
		out.AdvertiseTags = raw.Prefs.AdvertiseTags
	} else if len(raw.Self.Tags) > 0 {
		// Fallback to Self.Tags — `tailscale status --json`
		// reports the SAME tag list under both Prefs and Self
		// but the tailnet ACLs can sometimes strip one or
		// the other depending on the headscale version. Using
		// either is fine for the operator-facing display.
		out.AdvertiseTags = raw.Self.Tags
	}
	out.ExitNodeID = raw.Prefs.ExitNodeID
	out.HasAcceptIssue = !out.RouteAll || len(out.AdvertiseTags) == 0
	return out
}

// runContainerTailscale (B185) shells out to
// `docker exec <container> tailscale set <args>`. The
// "Re-apply accept-routes" button uses
// `tailscale set --accept-routes=true` (NOT
// `tailscale up --accept-routes`) precisely because the
// `tailscale up` form is "all-or-nothing" and breaks
// when the persisted state has --advertise-tags set
// (the B185 entrypoint bug). `tailscale set` only
// patches the specified field, which is what we want
// here.
func runContainerTailscale(containerName string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	full := append([]string{"exec", containerName, "tailscale", "set"}, args...)
	cmd := exec.CommandContext(ctx, "docker", full...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// handleTelegramReapplyAcceptRoutes (B185) is the
// one-click fix for the B185 entrypoint bug: if the
// container's tailscaled state has RouteAll=false
// (the B185 root cause), run `tailscale set
// --accept-routes=true` to flip the bit without
// breaking the "requires mentioning all non-default
// flags" check. The persisted state is updated, the
// routes propagate to the kernel within a few seconds,
// and the next probe will see "ok_relay" instead of
// "unreachable".
func (s *Service) handleTelegramReapplyAcceptRoutes(w http.ResponseWriter, r *http.Request, c *auth.Claims) {
	const containerName = "skygate-skygate-1"
	out, err := runContainerTailscale(containerName, "--accept-routes=true")
	if err != nil {
		s.Backend.Audit(c.UserID, c.Username, "telegram_reapply_accept_routes",
			fmt.Sprintf("container=%s err=%q out=%q", containerName, err.Error(), out))
		s.redirectWithFlash(w, r, "", fmt.Sprintf("docker exec tailscale set --accept-routes=true: %v (%s)", err, out))
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "telegram_reapply_accept_routes",
		fmt.Sprintf("container=%s out=%q", containerName, out))
	s.invalidateTelegramProbe()
	s.redirectWithFlash(w, r, "Container tailscaled: --accept-routes=true применён. Probe обновится в течение 30с.", "")
}

// renderProbeHTML returns the `<div class="alert alert-probe
// probe-{state}">…</div>` block used by both AdminTelegram
// (synchronous path) and AdminTelegramProbeBg (async path).
// The output is wrapped in `id="telegram-probe-slot"` so the JS
// in /admin/telegram.html can swap it via
// `document.getElementById('telegram-probe-slot').outerHTML = …`
// after fetch.
//
// `container` is the live state from readContainerTailscaleState —
// it's used to choose the FIRST troubleshooting tip when the probe
// is unreachable (the operator's B255 follow-up: the most common
// cause on the live VM was a RouteAll=false container, not a
// missing relay, so the pre-B255 banner that always listed the
// 4 generic tips pointed at the wrong knob).
//
// Mirrors admin/telegram.html lines 55-103. If you change the
// template, change this function in lockstep — the unit tests in
// telegram_b255_test.go assert the structural parity.
func renderProbeHTML(probe TelegramProbeResult, container ContainerTailscaleState, lang string) string {
	stateStr := probe.State.String()
	var sb strings.Builder
	sb.WriteString(`<div id="telegram-probe-slot" class="alert alert-probe probe-`)
	sb.WriteString(stateStr)
	sb.WriteString(`">`)
	switch stateStr {
	case "ok_direct":
		sb.WriteString(`<i class="fa-solid fa-globe"></i>`)
	case "ok_relay":
		sb.WriteString(`<i class="fa-solid fa-route"></i>`)
	default:
		sb.WriteString(`<i class="fa-solid fa-triangle-exclamation"></i>`)
	}
	sb.WriteString(`<div><strong>`)
	switch stateStr {
	case "ok_direct":
		sb.WriteString(html.EscapeString(i18n.T(lang, "telegram.probe_ok_direct_label")))
	case "ok_relay":
		sb.WriteString(html.EscapeString(i18n.T(lang, "telegram.probe_ok_relay_label")))
	default:
		sb.WriteString(html.EscapeString(i18n.T(lang, "telegram.probe_unreachable_label")))
	}
	sb.WriteString(`</strong><div class="sub">`)
	if probe.Message != "" {
		sb.WriteString(html.EscapeString(probe.Message))
	}
	if probe.LatencyMS != "" {
		sb.WriteString(" ")
		sb.WriteString(html.EscapeString(i18n.Tf(lang, "telegram.probe_latency", probe.LatencyMS)))
	}
	sb.WriteString(`</div>`)
	if len(probe.ResolvedIPs) > 0 {
		sb.WriteString(`<div class="sub" style="font-family:monospace;font-size:.85em">`)
		sb.WriteString(html.EscapeString(i18n.T(lang, "telegram.probe_resolved_prefix")))
		for _, ip := range probe.ResolvedIPs {
			sb.WriteString("<code>")
			sb.WriteString(html.EscapeString(ip))
			sb.WriteString("</code> ")
		}
		sb.WriteString(`</div>`)
	}
	if stateStr == "unreachable" {
		sb.WriteString(`<div class="sub" style="margin-top:.5rem"><strong>`)
		sb.WriteString(html.EscapeString(i18n.T(lang, "telegram.probe_troubleshooting")))
		sb.WriteString(`</strong><ul style="margin:.4rem 0 0 1.2rem;line-height:1.5">`)
		// B-bug-fix (2026-09-15): surface the actual cause first
		// instead of the generic 4 tips. Mirrors the template
		// patch on lines 90-94 of admin/telegram.html.
		if !container.Available {
			sb.WriteString(`<li><strong style="color:varc#a00)">`)
			sb.WriteString(html.EscapeString(i18n.T(lang, "telegram.probe_tip_container_off")))
			sb.WriteString(`</strong></li>`)
		} else if !container.RouteAll {
			sb.WriteString(`<li><strong style="color:varc#a00)">`)
			sb.WriteString(html.EscapeString(i18n.T(lang, "telegram.probe_tip_container_no_accept")))
			sb.WriteString(`</strong></li>`)
		}
		for _, tipKey := range []string{
			"telegram.probe_tip_advertise",
			"telegram.probe_tip_approve",
			"telegram.probe_tip_update",
			"telegram.probe_tip_docs",
		} {
			sb.WriteString(`<li>`)
			sb.WriteString(html.EscapeString(i18n.T(lang, tipKey)))
			sb.WriteString(`</li>`)
		}
		sb.WriteString(`</ul></div>`)
	}
	sb.WriteString(`</div></div>`)
	return sb.String()
}

// renderContainerHTML returns the inner block of the container
// tailscale diagnostic card (admin/telegram.html lines 218-260).
// Wrapped in `id="telegram-container-slot"` so the JS can swap
// it. The `csrf` argument is written into the "Re-apply
// accept-routes" form (only rendered when HasAcceptIssue=true).
func renderContainerHTML(ct ContainerTailscaleState, csrf string, lang string) string {
	var sb strings.Builder
	sb.WriteString(`<div id="telegram-container-slot">`)
	if !ct.Available {
		sb.WriteString(`<div class="alert alert-warn"><i class="fa-solid fa-triangle-exclamation"></i> `)
		sb.WriteString(html.EscapeString(i18n.T(lang, "telegram.container_unavailable")))
		if ct.RawStderr != "" {
			sb.WriteString(`<pre style="margin:.4rem 0 0;font-size:.8em;color:#666">`)
			sb.WriteString(html.EscapeString(ct.RawStderr))
			sb.WriteString(`</pre>`)
		}
		sb.WriteString(`</div>`)
	} else {
		sb.WriteString(`<table class="kv" style="font-size:.92em;line-height:1.5">`)
		row := func(label, value string) {
			sb.WriteString(`<tr><th style="text-align:left;width:14em">`)
			sb.WriteString(html.EscapeString(label))
			sb.WriteString(`</th><td>`)
			sb.WriteString(value) // value is pre-built (already HTML-safe code)
			sb.WriteString(`</td></tr>`)
		}
		row(i18n.T(lang, "telegram.container_hostname"), "<code>"+html.EscapeString(ct.Hostname)+"</code>")
		backend := "<code>" + html.EscapeString(ct.BackendState) + "</code>"
		if ct.BackendState == "Running" {
			backend += `<span class="muted">✓</span>`
		} else {
			backend += `<span class="muted">⚠</span>`
		}
		row(i18n.T(lang, "telegram.container_backend"), backend)
		row(i18n.T(lang, "telegram.container_ip4"), "<code>"+html.EscapeString(ct.IP4)+"</code>")
		row(i18n.T(lang, "telegram.container_ip6"), "<code>"+html.EscapeString(ct.IP6)+"</code>")
		var routeAllCell string
		if ct.RouteAll {
			routeAllCell = `<span class="badge badge-success">` +
				html.EscapeString(i18n.T(lang, "telegram.container_route_all_on")) + `</span>`
		} else {
			routeAllCell = `<span class="badge badge-danger">` +
				html.EscapeString(i18n.T(lang, "telegram.container_route_all_off")) +
				`</span><span class="muted" style="margin-left:.4rem">— ` +
				html.EscapeString(i18n.T(lang, "telegram.container_route_all_off_help")) + `</span>`
		}
		row(i18n.T(lang, "telegram.container_route_all"), routeAllCell)
		var tagsCell string
		if len(ct.AdvertiseTags) > 0 {
			for _, tag := range ct.AdvertiseTags {
				tagsCell += "<code>" + html.EscapeString(tag) + "</code> "
			}
		} else {
			tagsCell = `<span class="badge badge-danger">` +
				html.EscapeString(i18n.T(lang, "telegram.container_no_tags")) + `</span>`
		}
		row(i18n.T(lang, "telegram.container_advertise_tags"), tagsCell)
		var exitNodeCell string
		if ct.ExitNodeID != "" {
			exitNodeCell = "<code>" + html.EscapeString(ct.ExitNodeID) + "</code>"
		} else {
			exitNodeCell = `<span class="muted">` +
				html.EscapeString(i18n.T(lang, "telegram.container_no_exit_node")) + `</span>`
		}
		row(i18n.T(lang, "telegram.container_exit_node"), exitNodeCell)
		sb.WriteString(`</table>`)
		if ct.HasAcceptIssue {
			sb.WriteString(`<div class="alert alert-warn" style="margin-top:.5rem"><i class="fa-solid fa-triangle-exclamation"></i> `)
			sb.WriteString(html.EscapeString(i18n.T(lang, "telegram.container_issue_help")))
			sb.WriteString(`</div>`)
			sb.WriteString(`<form action="/admin/telegram" method="POST" style="margin-top:.5rem" onsubmit="return confirm('`)
			sb.WriteString(html.EscapeString(i18n.T(lang, "telegram.container_reapply_confirm")))
			sb.WriteString(`')">`)
			sb.WriteString(`<input type="hidden" name="csrf" value="`)
			sb.WriteString(html.EscapeString(csrf))
			sb.WriteString(`">`)
			sb.WriteString(`<input type="hidden" name="action" value="reapply_accept_routes">`)
			sb.WriteString(`<button type="submit" class="btn btn-primary"><i class="fa-solid fa-rotate"></i> `)
			sb.WriteString(html.EscapeString(i18n.T(lang, "telegram.container_reapply_button")))
			sb.WriteString(`</button></form>`)
		}
	}
	sb.WriteString(`</div>`)
	return sb.String()
}
