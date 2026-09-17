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
// Resolution order (first non-empty wins) — B260.2.3 fix:
//   1. Bundled derp_relays row's URL port (the DB is the source of
//      truth for what /admin/derp/relays/derpmap.json publishes to
//      headscale — and it's editable via the web UI, so it stays in
//      sync with reality)
//   2. DERP_HTTP_PORT env var (legacy override; pre-B260.2.3 the env
//      took priority and the stale value `:8443` from the pre-B-derper-cert
//      systemd era masked the correct `:443` even after B260's
//      ORDER BY id ASC LIMIT 1 fix)
//   3. "443" — historical default; caller treats empty as "443"
//
// Returns "" if both DB and env lookups failed. Caller should fall
// back to "443" rather than treating "" as a real port.
func resolveDERPPort(d *sql.DB) string {
	if d != nil {
		if p := bundledDERPPortFromDB(d); p != "" {
			return p
		}
	}
	if v := strings.TrimSpace(os.Getenv("DERP_HTTP_PORT")); v != "" {
		return v
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
//
// B260 — added `ORDER BY id ASC LIMIT 1`. Pre-B260 the query was
// `LIMIT 1` without `ORDER BY` which is non-deterministic when
// multiple `is_bundled=1` rows exist. The live agent VM
// 192.168.13.69 had a data-hygiene issue (id=2 and id=3 both
// `is_bundled=1` from a direct-SQL insert during the B-derper-cert
// migration) and the page flipped between "443" and "8443"
// depending on which row PG picked. The `ORDER BY id ASC LIMIT 1`
// makes the resolution deterministic — always picks the oldest
// bundled row (id=2, port 443 on the live VM). The dual-bundled
// data is operator-cleanup separate (TODO B260.1 migration to
// dedupe + the AddDerpRelay guard already prevents new duplicates).
func bundledDERPPortFromDB(d *sql.DB) string {
	var urlStr string
	err := d.QueryRow(`
		SELECT url FROM derp_relays
		 WHERE is_bundled = 1 AND enabled = 1
		 ORDER BY id ASC
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

// B260 — bundledDERPHostnameFromDB returns the hostname of the
// bundled derper (e.g. "derp.skynas.ru"). Used by the
// /admin/derp status collection to build a TLS-aware probe URL.
// Same deterministic ordering as bundledDERPPortFromDB —
// `ORDER BY id ASC LIMIT 1` to handle the dual-bundled-row
// edge case consistently.
func bundledDERPHostnameFromDB(d *sql.DB) string {
	var hostname string
	err := d.QueryRow(`
		SELECT hostname FROM derp_relays
		 WHERE is_bundled = 1 AND enabled = 1
		 ORDER BY id ASC
		 LIMIT 1
	`).Scan(&hostname)
	if err != nil || hostname == "" {
		return ""
	}
	return hostname
}

// B260 — resolveDERPHostname returns the derper's public hostname
// (the one Tailscale clients dial and the cert CN matches). Same
// resolution shape as resolveDERPPort: DB override first (the
// bundled row's hostname is the canonical answer), then the
// SKYGATE_DERP_HOSTNAME env var as bootstrap, then "" (caller
// falls back to localhost probe which fails cleanly).
//
// The result is what we pass as the SNI hostname in the TLS
// handshake when probing derper from inside the skygate container.
// Without a real hostname here, the TLS cert validation fails
// (cert is for "derp.skynas.ru", not for the IP we'd otherwise
// dial) and the status probe errors with "x509: certificate is
// valid for derp.skynas.ru, not 192.168.13.69" — the symptom
// that pre-B260 caused "DERPER-SERVICE: stopped" on /admin/derp
// despite derper being up and answering.
func resolveDERPHostname(d *sql.DB) string {
	if d != nil {
		if h := bundledDERPHostnameFromDB(d); h != "" {
			return h
		}
	}
	if v := strings.TrimSpace(os.Getenv("SKYGATE_DERP_HOSTNAME")); v != "" {
		return v
	}
	return ""
}