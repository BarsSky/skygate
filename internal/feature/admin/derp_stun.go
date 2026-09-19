// File: internal/feature/admin/derp_stun.go
//
// B265 (2026-09-19) — real STUN/UDP probe for the /admin/derp
// "STUN UDP" metric tile.
//
// WHY THIS EXISTS
//
// Pre-B265 the tile's `STUNListening` boolean had exactly two
// setters, both in derp.go, and both read the SAME URL —
// `https://<bundled-host>:<bundled-port>/debug/vars`:
//
//	derp.go  step 2 (parseDerperVars)  → STUNListening = success > 0
//	derp.go  step 4 (inline struct)    → STUNListening = success > 0
//
// Upstream derper gates every /debug/* handler behind
// tsweb.Protected → tsweb.AllowDebugAccess, which only admits
// loopback, a Tailscale source IP, TS_ALLOW_DEBUG_IP, or
// TS_DEBUG_KEY_PATH + ?debugkey=. The skygate container's source
// address is a docker/LAN address in every deployment shape
// (hostname → extra_hosts LAN IP, or hostname → public IP with
// docker MASQUERADE), so the answer is always
//
//	HTTP 403 "debug access denied"
//
// Verified live on the reference VM 2026-09-19:
//
//	from the skygate container: /debug/vars → 403 debug access denied
//	from 127.0.0.1 on the host: /debug/vars → 200,
//	    "stun":{"counter_requests":{"success":33709,...}}
//
// so the page rendered "STUN UDP :3478 closed" (red) while STUN
// was demonstrably healthy (UDP 3478 bound, 33709 successful
// requests, and a remote VPS measuring the relay via netcheck).
// The B260.1 WebSocket fallback restored `Running` but explicitly
// left STUN at zero ("those stay at zero, which is honest" —
// it was not honest, it was a false negative).
//
// B265 replaces the counter-read with an actual UDP round trip:
// a STUN Binding Request (RFC 5389 §6) sent to the bundled
// derper's STUN port, watching for a Binding Success Response
// whose transaction ID and magic cookie match. That is the same
// packet Tailscale clients use to score DERP regions for home
// relay selection (netcheck), so a green tile now means "clients
// on this path can score this relay", not "we could read a
// counter".
//
// The HTTP /debug/* probes are still attempted first (they carry
// the richer metrics when the operator HAS exposed debug access);
// the STUN round trip is the authoritative fallback that works
// regardless of derper's --debug configuration.

package admin

import (
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"strings"
	"time"
)

// STUN protocol constants (RFC 5389).
const (
	// stunMagicCookie is the fixed value present in every STUN
	// message since RFC 5389; responses must echo it.
	stunMagicCookie uint32 = 0x2112A442

	// stunBindingRequest / stunBindingSuccess are the message
	// types (the top 2 bits of the first 16-bit word are zero).
	stunBindingRequest uint16 = 0x0001
	stunBindingSuccess uint16 = 0x0101

	// stunHeaderLen is the fixed header: type(2) + length(2) +
	// magic cookie(4) + transaction ID(12).
	stunHeaderLen = 20

	// stunAttrHeaderLen is type(2) + length(2) for each attribute.
	stunAttrHeaderLen = 4

	// stunAttrXorMappedAddress (0x0020) carries the reflexive
	// transport address; its presence is what proves we reached a
	// real STUN server rather than an echo.
	stunAttrXorMappedAddress uint16 = 0x0020
	stunAttrMappedAddress    uint16 = 0x0001
)

// STUNProbeResult is the outcome of one UDP STUN round trip.
//
// RTT is the measured round-trip time; it is the number a
// Tailscale client would use to score this relay. ReflexiveAddr is
// the address the STUN server saw us as (empty when the response
// carried no address attribute) — the same value a client uses for
// NAT discovery.
type STUNProbeResult struct {
	RTT           time.Duration
	ReflexiveAddr string
}

// buildSTUNBindingRequest returns a 20-byte RFC 5389 Binding
// Request plus its random 12-byte transaction ID. Pure function so
// it can be unit-tested byte-for-byte.
func buildSTUNBindingRequest() (pkt, txID []byte, err error) {
	txID = make([]byte, 12)
	if _, err := rand.Read(txID); err != nil {
		return nil, nil, fmt.Errorf("stun: transaction id: %w", err)
	}
	pkt = make([]byte, stunHeaderLen)
	binary.BigEndian.PutUint16(pkt[0:2], stunBindingRequest)
	binary.BigEndian.PutUint16(pkt[2:4], 0) // no attributes
	binary.BigEndian.PutUint32(pkt[4:8], stunMagicCookie)
	copy(pkt[8:20], txID)
	return pkt, txID, nil
}

// parseSTUNBindingResponse validates a STUN response against the
// request we sent and extracts the XOR-MAPPED-ADDRESS (or the
// legacy MAPPED-ADDRESS) when present.
//
// Returns an error for: short packets, wrong message type, wrong
// magic cookie, or a transaction-ID mismatch (a late response to a
// PREVIOUS probe — the classic false positive when several probes
// share a socket). Pure function.
func parseSTUNBindingResponse(resp, wantTxID []byte) (string, error) {
	if len(resp) < stunHeaderLen {
		return "", fmt.Errorf("stun: response too short (%d bytes)", len(resp))
	}
	if mt := binary.BigEndian.Uint16(resp[0:2]); mt != stunBindingSuccess {
		return "", fmt.Errorf("stun: unexpected message type 0x%04x (want 0x%04x)", mt, stunBindingSuccess)
	}
	if mc := binary.BigEndian.Uint32(resp[4:8]); mc != stunMagicCookie {
		return "", fmt.Errorf("stun: bad magic cookie 0x%08x", mc)
	}
	if len(wantTxID) == 12 && string(resp[8:20]) != string(wantTxID) {
		return "", fmt.Errorf("stun: transaction id mismatch (stale response)")
	}
	// Walk the attribute list. `declared` bounds the loop so a
	// malformed length field can't walk past the packet.
	declared := int(binary.BigEndian.Uint16(resp[2:4]))
	end := stunHeaderLen + declared
	if end > len(resp) {
		end = len(resp)
	}
	for off := stunHeaderLen; off+stunAttrHeaderLen <= end; {
		at := binary.BigEndian.Uint16(resp[off : off+2])
		al := int(binary.BigEndian.Uint16(resp[off+2 : off+4]))
		val := off + stunAttrHeaderLen
		if val+al > len(resp) {
			break
		}
		switch at {
		case stunAttrXorMappedAddress:
			if addr := decodeSTUNAddr(resp[val:val+al], true, wantTxID); addr != "" {
				return addr, nil
			}
		case stunAttrMappedAddress:
			if addr := decodeSTUNAddr(resp[val:val+al], false, wantTxID); addr != "" {
				return addr, nil
			}
		}
		// Attributes are padded to a 4-byte boundary.
		off = val + al
		if pad := al % 4; pad != 0 {
			off += 4 - pad
		}
	}
	return "", nil
}

// decodeSTUNAddr decodes a MAPPED-ADDRESS / XOR-MAPPED-ADDRESS
// attribute body into "ip:port". For the XOR form the port is
// XORed with the top 16 bits of the magic cookie and the address
// with the cookie followed by the 12-byte transaction ID
// (RFC 5389 §15.2). Pure function. Returns "" when the family is
// unknown or the body is truncated.
func decodeSTUNAddr(b []byte, xor bool, txID []byte) string {
	if len(b) < 4 {
		return ""
	}
	family := b[1]
	port := int(binary.BigEndian.Uint16(b[2:4]))
	if xor {
		port ^= int(stunMagicCookie >> 16)
	}
	switch family {
	case 0x01: // IPv4
		if len(b) < 8 {
			return ""
		}
		ip := make(net.IP, 4)
		copy(ip, b[4:8])
		if xor {
			cookie := make([]byte, 4)
			binary.BigEndian.PutUint32(cookie, stunMagicCookie)
			for i := 0; i < 4; i++ {
				ip[i] ^= cookie[i]
			}
		}
		return net.JoinHostPort(ip.String(), fmt.Sprint(port))
	case 0x02: // IPv6
		if len(b) < 20 {
			return ""
		}
		ip := make(net.IP, 16)
		copy(ip, b[4:20])
		if xor {
			mask := make([]byte, 16)
			binary.BigEndian.PutUint32(mask[0:4], stunMagicCookie)
			if len(txID) == 12 {
				copy(mask[4:16], txID)
			}
			for i := 0; i < 16; i++ {
				ip[i] ^= mask[i]
			}
		}
		return net.JoinHostPort(ip.String(), fmt.Sprint(port))
	}
	return ""
}

// isDebugAccessDenied reports whether a /debug/* response body is
// derper's (really tsweb's) access-denied page rather than JSON.
//
// Upstream `tsweb.Protected` answers every rejected /debug/*
// request with a plain-text "debug access denied" body and status
// 403, so the body is the literal string. httpGet discards the
// status code (it returns the body regardless), which is why the
// status has to be recognised from the body here.
func isDebugAccessDenied(body []byte) bool {
	return strings.Contains(string(body), "debug access denied")
}

// probeSTUNForStatus fills the B265 STUN fields on `st` by sending
// a real RFC 5389 Binding Request over UDP.
//
// Candidate order (first address that answers wins):
//
//  1. The hostname the TLS probes already use (the bundled
//     derp_relays `hostname`, which in a docker deployment resolves
//     through `extra_hosts` to the host that runs derper). This is
//     the same target clients dial, so it is the most meaningful
//     path to test.
//  2. Every distinct host of the bundled derp_relays URLs — a
//     second bundled row (e.g. a stale :8443 row) may carry the one
//     hostname that actually resolves.
//  3. SKYGATE_DERP_PROBE_HOST (the operator's explicit "where the
//     container can reach derper" hint; documented in
//     docker-compose.yml and check_b260 section K).
//  4. SKYGATE_DERP_HOSTNAME.
//
// Every failure is recorded: STUNErr holds the last candidate's
// error (trimmed for the UI) and STUNBlocked stays true, so the
// page shows "probed, unreachable (+reason)" instead of a bare
// red tile.
func probeSTUNForStatus(st *DerpStatus, db *sql.DB, derpHost, stunPort string) {
	if st == nil {
		return
	}
	candidates := STUNCandidateHosts(db, derpHost)
	timeout := 2 * time.Second
	var lastErr error
	for _, host := range candidates {
		res, err := probeSTUNUDP(net.JoinHostPort(host, stunPort), timeout)
		if err != nil {
			lastErr = err
			continue
		}
		st.STUNListening = true
		st.STUNBlocked = false
		st.STUNRTT = fmt.Sprintf("%d ms", res.RTT.Milliseconds())
		st.STUNReflex = res.ReflexiveAddr
		st.STUNErr = ""
		return
	}
	st.STUNListening = false
	st.STUNBlocked = true
	if lastErr != nil {
		st.STUNErr = trimSTUNErr(lastErr.Error())
	}
}

// trimSTUNErr shortens a Go network error for the metric tile: the
// useful part is the leading cause ("read (no UDP response …):
// i/o timeout"), not the whole dial chain.
func trimSTUNErr(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, ": "); i > 0 && len(s) > 60 {
		// Keep the first clause, then append the last one so both
		// "what failed" and "why" survive.
		parts := strings.Split(s, ": ")
		if len(parts) >= 2 {
			s = parts[0] + ": " + parts[len(parts)-1]
		}
	}
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}

// STUNCandidateHosts returns the probe order used by
// probeSTUNForStatus (and exposed for tests + the page's "measured
// from" annotation):
//
//  1. `derpHost` — the hostname the TLS probes already use (the
//     bundled derp_relays hostname, which a docker deployment
//     resolves through `extra_hosts` to the host running derper).
//  2. Every distinct host of the bundled derp_relays URLs (a second
//     bundled row, e.g. a stale :8443 entry, may carry the one
//     hostname that resolves).
//  3. SKYGATE_DERP_PROBE_HOST — the operator's explicit "where the
//     container can reach derper" hint (docker-compose.yml,
//     check_b260 section K).
//  4. SKYGATE_DERP_HOSTNAME.
//
// Falls back to 127.0.0.1 only when nothing at all is configured,
// so the probe fails cleanly and visibly instead of silently
// probing nothing.
func STUNCandidateHosts(db *sql.DB, derpHost string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(h string) {
		h = strings.TrimSpace(h)
		if h == "" || seen[h] {
			return
		}
		seen[h] = true
		out = append(out, h)
	}
	add(derpHost)
	if db != nil {
		for _, h := range bundledDERPHostnamesFromDB(db) {
			add(h)
		}
	}
	add(os.Getenv("SKYGATE_DERP_PROBE_HOST"))
	add(os.Getenv("SKYGATE_DERP_HOSTNAME"))
	if len(out) == 0 {
		add("127.0.0.1")
	}
	return out
}

// "host:port"; port defaults to 3478) over UDP and waits up to
// `timeout` for a valid Binding Success Response.
//
// Returns the round trip + the reflexive address on success. Any
// network error, timeout, or malformed/foreign response is
// returned as an error so the caller can label the tile with the
// reason instead of a bare boolean.
func probeSTUNUDP(addr string, timeout time.Duration) (STUNProbeResult, error) {
	var res STUNProbeResult
	if addr == "" {
		return res, fmt.Errorf("stun: empty address")
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(addr, "3478")
	}
	pkt, txID, err := buildSTUNBindingRequest()
	if err != nil {
		return res, err
	}
	conn, err := net.DialTimeout("udp", addr, timeout)
	if err != nil {
		return res, fmt.Errorf("stun: dial %s: %w", addr, err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return res, fmt.Errorf("stun: deadline: %w", err)
	}
	start := time.Now()
	if _, err := conn.Write(pkt); err != nil {
		return res, fmt.Errorf("stun: write: %w", err)
	}
	// A STUN server answers with a single datagram; anything
	// bigger than the response we care about is either a DERP
	// frame or garbage, and both fail the parse below.
	buf := make([]byte, 1500)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return res, fmt.Errorf("stun: read (no UDP response from %s): %w", addr, err)
		}
		rtt := time.Since(start)
		refl, perr := parseSTUNBindingResponse(buf[:n], txID)
		if perr != nil {
			// Wrong transaction id = stale datagram from a
			// previous probe; keep waiting until the deadline.
			if time.Since(start) >= timeout {
				return res, perr
			}
			continue
		}
		res.RTT = rtt
		res.ReflexiveAddr = refl
		return res, nil
	}
}
