// File: internal/feature/admin/derp_status_resolve.go
//
// B-fix (2026-09-15): resolveDERPPort / resolveSTUNPort helpers used by
// collectDerpStatus() in derp.go. The pre-fix code HARDCODED DERPPort="443"
// and STUNPort="3478" as seed values, which masked a real bug: when the
// operator runs derper on a non-standard port (e.g. --a=:8443 with NPM
// terminating TLS on :443 externally), /admin/derp showed ":443" but the
// actual derper process listened on :8443 plain HTTP (no LE cert, fell
// back to HTTP because the Let's Encrypt HTTP-01 challenge can't reach
// derper through NPM).
//
// The fix reads the port from three sources in priority order:
//
//  1. DERP_HTTP_PORT / DERP_STUN_PORT env vars (set in docker-compose,
//     match the systemd unit's --a= / --stun flags).
//  2. The bundled row in derp_relays DB (canonical — this is what
//     /admin/derp/relays/derpmap.json publishes to headscale clients).
//     Extracted from the URL via a small url.Parse helper.
//  3. The historical defaults "443" / "3478" (last resort, with a
//     prominent comment so the next reader knows these are placeholders).
//
// The auto-register helper (EnsureBundledDerpRelay) lives in
// derp_relays_auto.go (sibling file). It runs at boot from
// cmd/skygate/main.go to insert the bundled derp_relays row on a fresh
// install where the operator forgot to click /admin/derp/relays/add —
// this is what fixed the live agent VM 2026-09-15.
//
// 2026-09-15: v1.5.3 — B-bug-fix (hardcoded DERP port masked broken derper).

package admin

import (
	"database/sql"
	"net"
	"net/url"
	"os"
	"strings"
)

// resolveDERPPort returns the public DERP HTTPS port the operator has
// configured for the bundled derper. Pre-fix this returned the
// hardcoded "443" regardless of the actual setup, which silently
// masked derpers running on :8443 (with NPM TLS termination) or
// other non-standard ports.
//
// Resolution order (first non-empty wins):
//   1. DERP_HTTP_PORT env var (matches systemd ExecStart --a=:PORT)
//   2. Bundled derp_relays row's URL port (what /admin/derp/relays/derpmap.json
//      actually publishes to headscale)
//   3. "443" — historical default; caller treats empty as "443"
//
// Returns "" if both env and DB-row lookups failed. Caller should
// fall back to "443" rather than treating "" as a real port.
func resolveDERPPort(d *sql.DB) string {
	if v := strings.TrimSpace(os.Getenv("DERP_HTTP_PORT")); v != "" {
		return v
	}
	if d != nil {
		if p := bundledDERPPortFromDB(d); p != "" {
			return p
		}
	}
	return ""
}

// resolveSTUNPort is the STUN analogue of resolveDERPPort. Same
// priority order: DERP_STUN_PORT env, then bundled derp_relays row.
// Currently no STUN port column in derp_relays, so the second source
// always returns "" — the Tailscale/derper default is 3478 and we
// haven't seen any operator run a non-standard STUN port. Kept as a
// TODO if STUN port ever diverges.
func resolveSTUNPort(d *sql.DB) string {
	if v := strings.TrimSpace(os.Getenv("DERP_STUN_PORT")); v != "" {
		return v
	}
	// TODO(B-bug-fix-future): persist STUN port in derp_relays if it
	// ever diverges from 3478.
	return ""
}

// bundledDERPPortFromDB reads the is_bundled=1 row from derp_relays
// and extracts the port from its URL. Returns "" if no bundled row
// or the row's URL is malformed.
//
// Pure read — no DB writes. Safe to call on every /admin/derp page
// load. The query is O(1) (the unique partial index
// derp_relays_is_bundled_idx guarantees at most one row matches).
func bundledDERPPortFromDB(d *sql.DB) string {
	var urlStr string
	err := d.QueryRow(`
		SELECT url FROM derp_relays
		 WHERE is_bundled = 1 AND enabled = 1
		 LIMIT 1
	`).Scan(&urlStr)
	if err != nil || urlStr == "" {
		return ""
	}
	u, err := url.Parse(urlStr)
	if err != nil || u.Host == "" {
		return ""
	}
	// Extract explicit port (e.g. "derp.skynas.ru:8443"); fall back
	// to the scheme's default port (443 for https, 80 for http).
	host := u.Host
	if _, port, splitErr := net.SplitHostPort(host); splitErr == nil && port != "" {
		return port
	}
	switch u.Scheme {
	case "https":
		return "443"
	case "http":
		return "80"
	}
	return ""
}