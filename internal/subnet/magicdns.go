// MagicDNS names for per-user subnets (v0.18.0).
//
// When headscale has `dns.magic_dns: true` and
// `dns.base_domain: tsnet.skynas.ru` (the operator's
// tailnet base), every node registered in the tailnet
// auto-resolves as `<node-name>.<base-domain>`. So:
//
//   * The v0.16.7 sidecar registered with
//     `--hostname=skygate-subnet-<username>` resolves
//     as `skygate-subnet-<username>.tsnet.skynas.ru`.
//     This is the entry point the operator's tailnet
//     clients use to reach the user's 10.0.<uid>.0/24
//     subnet.
//
//   * Devices in the tailnet that are tagged
//     `tag:private` and have names matching the
//     `<username>-*` pattern resolve as
//     `<device>.skygate-subnet-<username>.tsnet.skynas.ru`.
//     headscale's `dns.search_domains` is what makes
//     the short form (`<device>.skygate-subnet-<username>`)
//     resolvable from a tailnet client. Without it,
//     only the FQDN works.
//
// The "exitnode.skygate-subnet-<user>" record (the
// special one from the v0.16.0 roadmap) is NOT a
// MagicDNS auto-record. headscale 0.29 doesn't support
// per-user service records; v0.19.0 is the planned
// home for that (it'll use headscale's policy-level
// `dns.extra_records` or a future service-records
// feature).
//
// The functions in this file are pure-string
// computation — no DB lookup needed. The base
// domain is hardcoded to `tsnet.skynas.ru` for
// now; if/when skygate supports multiple bases
// (per-plane), this becomes a parameter.
package subnet

import "strings"

// BaseDomain is the tailnet's MagicDNS base domain.
// Hardcoded to the operator's deployment. The
// `tsnet` prefix matches what the rest of the
// portal uses for the @-suffix in user identities
// (see internal/acl/acl.go's baseDomain constant).
const BaseDomain = "tsnet.skynas.ru"

// SidecarHostname is the convention the v0.16.7
// Provision handler uses for the tailscaled
// `--hostname=...` argument. Every portal user's
// sidecar registers with this prefix.
const SidecarHostnamePrefix = "skygate-subnet-"

// MagicDNSNames is the bundle of FQDNs the operator
// (or a tailnet client) can use to reach a user's
// per-user subnet. v0.18.0 returns this from
// /admin/users/{id}/subnet and from the bot's
// /mysubnet reply.
type MagicDNSNames struct {
	// Sidecar is the sidecar's own FQDN. Resolves
	// to the sidecar's tailnet IP — the entry point
	// for the user's 10.0.<uid>.0/24 subnet.
	// Format: `skygate-subnet-<username>.tsnet.skynas.ru`
	Sidecar string

	// SidecarShort is the hostname-only form
	// (no `.tsnet.skynas.ru` suffix). Useful when
	// writing inside the tailnet (search domain
	// resolves it). Format: `skygate-subnet-<username>`
	SidecarShort string

	// UserWildcard is the per-user DNS search
	// pattern that the operator can configure
	// via headscale's `dns.search_domains`. Devices
	// inside the user's tailnet with names matching
	// `<device>.skygate-subnet-<username>` then
	// resolve via the search-domain shortcut.
	// Format: `<device>.skygate-subnet-<username>.tsnet.skynas.ru`
	// (the `<device>` is filled in by the operator
	// or the future v0.19.0 per-user device
	// registry).
	UserWildcard string
}

// ComputeMagicDNSNames returns the auto-resolving
// FQDNs for a user with the given username. The
// username is expected to already be lowercase +
// [a-z0-9_-]+ (the per-portal_user_id format);
// ComputeMagicDNSNames doesn't re-validate it.
func ComputeMagicDNSNames(username string) MagicDNSNames {
	sidecar := SidecarHostnamePrefix + username
	return MagicDNSNames{
		Sidecar:      sidecar + "." + BaseDomain,
		SidecarShort: sidecar,
		// The wildcard form documents the search-
		// domain pattern, not a real DNS name. The
		// operator can use it as a hint when
		// configuring headscale's dns.search_domains.
		UserWildcard: "<device>." + sidecar + "." + BaseDomain,
	}
}

// FormatMagicDNSNames renders a multi-line string
// for the admin UI / bot reply. Each line is
// "<label>: <fqdn>". RU/EN labels come from the
// i18n catalog; the caller passes them in so this
// package stays free of i18n deps.
func FormatMagicDNSNames(names MagicDNSNames, labels struct {
	Sidecar      string
	Short        string
	WildcardHint string
}) string {
	var sb strings.Builder
	sb.WriteString(labels.Sidecar)
	sb.WriteString(": ")
	sb.WriteString(names.Sidecar)
	sb.WriteString("\n")
	sb.WriteString(labels.Short)
	sb.WriteString(": ")
	sb.WriteString(names.SidecarShort)
	sb.WriteString("\n")
	sb.WriteString(labels.WildcardHint)
	sb.WriteString(": ")
	sb.WriteString(names.UserWildcard)
	return sb.String()
}
