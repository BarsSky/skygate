package telegram

import (
	"net/url"
	"skygate/internal/i18n"
	"skygate/internal/subnet"
)

func mySubnetReply(env BotEnv) string {
	// 2026-07-16: v0.16.3 — "more HTML" pass. Mark HTML
	// so the <b>/<code>/<i> in the body render. Same
	// pattern as the other /my_* replies.
	markHTMLReply()
	lang := env.Lang
	if !env.IsIdentified() {
		return i18n.T(lang, "bot.mysubnet.not_bound")
	}
	// Single-row read against the denormalized columns.
	// subnets are opt-in (not allocated by default), so
	// most users have empty strings here — we return
	// the "empty" hint instead of showing zero values.
	var cidr, status, routerNodeID, controlPlaneURL string
	if err := env.DB.QueryRow(`
		SELECT subnet_cidr, subnet_status, subnet_router_node_id, headscale_url
		  FROM portal_users
		 WHERE id = $1
	`, env.PortalUserID).Scan(&cidr, &status, &routerNodeID, &controlPlaneURL); err != nil {
		return i18n.Tf(lang, "bot.mysubnet.db_error", err)
	}
	if cidr == "" {
		// No subnet allocated. The "empty" hint points
		// the user at /admin/users/{id}/subnet so the
		// operator knows where to provision one.
		return i18n.Tf(lang, "bot.mysubnet.empty", env.Username)
	}
	// Router hostname: the v0.16.0 release stores the
	// headscale node_id but not the friendly hostname
	// (the v0.16.1 sidecar work fills the hostname
	// column in user_subnets). For v0.16.0 we show
	// either the node_id (if registered) or a "not yet
	// provisioned" hint.
	var routerLabel string
	switch {
	case routerNodeID != "":
		// node_id is a numeric headscale id; the v0.16.1
		// work will resolve it to a hostname via
		// headscale.ListNodes().
		routerLabel = "node " + routerNodeID
	default:
		routerLabel = i18n.T(lang, "bot.mysubnet.label_router") + ": " +
			// No good "not yet" key in the catalog yet;
			// reusing a related string.
			"—"
	}
	// Plane label: empty = global plane, otherwise show
	// the URL (truncated to host for readability).
	planeLabel := "(global)"
	if controlPlaneURL != "" {
		// Truncate to host portion for readability; the
		// full URL is in /admin/users/{id}/subnet.
		if u, err := url.Parse(controlPlaneURL); err == nil && u.Host != "" {
			planeLabel = u.Host
		} else {
			planeLabel = controlPlaneURL
		}
	}
	// Build the body. The "sharing" sections are empty
	// in v0.16.0; the v0.17.1 release will fill them
	// with /share_subnet and /revoke_subnet data.
	// 2026-07-17: v0.18.0 — append a MagicDNS section
	// with the sidecar's auto-resolving FQDN. The
	// user can `tailscale ssh <fqdn>` or just use
	// the FQDN as a destination from any tailnet
	// client. Per-device records (the wildcard
	// pattern) are documented in the note.
	names := subnet.ComputeMagicDNSNames(env.Username)
	magicBody := Section(i18n.T(lang, "bot.mysubnet.section_magicdns")) + "\n" +
		Field(i18n.T(lang, "bot.mysubnet.magicdns_sidecar_label"),
			"<code>"+names.Sidecar+"</code>") + "\n" +
		Field(i18n.T(lang, "bot.mysubnet.magicdns_short_label"),
			"<code>"+names.SidecarShort+"</code>") + "\n" +
		"<i>" + i18n.T(lang, "bot.mysubnet.magicdns_note") + "</i>"
	body := i18n.Tf(lang, "bot.mysubnet.header", env.Username) + "\n" +
		Section(i18n.T(lang, "bot.mysubnet.section_subnet")) + "\n" +
		Field(i18n.T(lang, "bot.mysubnet.label_cidr"), cidr) + "\n" +
		Field(i18n.T(lang, "bot.mysubnet.label_status"), status) + "\n" +
		Field(i18n.T(lang, "bot.mysubnet.label_router"), routerLabel) + "\n" +
		Field(i18n.T(lang, "bot.mysubnet.label_planes"), planeLabel) + "\n\n" +
		magicBody + "\n\n" +
		Section(i18n.T(lang, "bot.mysubnet.section_sharing")) + "\n" +
		"<i>" + i18n.T(lang, "bot.mysubnet.sharing_v0_17_1") + "</i>"
	return body
}

// mySubnetProvisionReply — v0.16.7. /mysubnet provision.
// Issues a per-user preauth key (tag:subnet-router, 1h
// TTL, single-use) and replies with the key + the
// suggested `tailscale up` command. The user runs that
// command on their sidecar host; the auto-approver in
// internal/sidecar approves the route within ~30s and
// flips /mysubnet status to active.
//
// `skipWrap: true` in the dispatch means HandleCommand
// doesn't add a second gate envelope on top of our
// butler gate — the key + command are already in a
// <pre> block that the user needs to copy verbatim.
