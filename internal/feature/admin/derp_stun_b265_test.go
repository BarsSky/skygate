// File: internal/feature/admin/derp_stun_b265_test.go
//
// B265 (2026-09-19) — regression tests for the real STUN/UDP probe
// that replaced the /debug/vars counter read on /admin/derp.
//
// The pre-B265 STUN tile could only turn green when
// `https://<derper>/debug/vars` returned JSON with
// `stun.counter_requests.success > 0`. Upstream derper gates
// /debug/* behind tsweb.AllowDebugAccess (loopback / Tailscale IP /
// TS_ALLOW_DEBUG_IP only), so the skygate container — whose source
// address is a docker/LAN address — always received
//
//	HTTP 403 "debug access denied"
//
// and the tile rendered "STUN UDP :3478 closed" (red) on a
// healthy relay. These tests pin the replacement: a real RFC 5389
// Binding Request round trip.
//
// The wire-format tests are pure (no network): build → parse
// round-trip, XOR-MAPPED-ADDRESS decoding, and the foreign/stale
// response rejections that would otherwise produce false positives.

package admin

import (
	"encoding/binary"
	"hash/crc32"
	"net"
	"testing"
	"time"
)

// encodeMappedAddr builds a MAPPED-ADDRESS / XOR-MAPPED-ADDRESS
// attribute body (family, port, address) the same way a STUN
// server does, so the decoder can be tested without a server.
func encodeMappedAddr(ip net.IP, port int, xor bool, txID []byte) []byte {
	var fam byte
	var raw []byte
	if v4 := ip.To4(); v4 != nil {
		fam, raw = 0x01, v4
	} else {
		fam, raw = 0x02, ip.To16()
	}
	body := make([]byte, 4+len(raw))
	body[0] = 0
	body[1] = fam
	p := port
	if xor {
		p ^= int(stunMagicCookie >> 16)
	}
	binary.BigEndian.PutUint16(body[2:4], uint16(p))
	copy(body[4:], raw)
	if xor {
		cookie := make([]byte, 4)
		binary.BigEndian.PutUint32(cookie, stunMagicCookie)
		for i := 0; i < 4; i++ {
			body[4+i] ^= cookie[i]
		}
		if len(raw) == 16 && len(txID) == 12 {
			for i := 0; i < 12; i++ {
				body[8+i] ^= txID[i]
			}
		}
	}
	return body
}

// buildSTUNResponse assembles a Binding Success Response carrying
// one address attribute.
func buildSTUNResponse(txID, attrBody []byte, attrType uint16) []byte {
	pkt := make([]byte, stunHeaderLen)
	binary.BigEndian.PutUint16(pkt[0:2], stunBindingSuccess)
	binary.BigEndian.PutUint16(pkt[2:4], uint16(stunAttrHeaderLen+len(attrBody)))
	binary.BigEndian.PutUint32(pkt[4:8], stunMagicCookie)
	copy(pkt[8:20], txID)
	pkt = append(pkt, byte(attrType>>8), byte(attrType))
	pkt = append(pkt, byte(len(attrBody)>>8), byte(len(attrBody)))
	pkt = append(pkt, attrBody...)
	for len(pkt)%4 != 0 {
		pkt = append(pkt, 0)
	}
	return pkt
}

func TestBuildSTUNBindingRequest_WireFormat(t *testing.T) {
	// B307 (v1.5.72) — RENEGOTIATED. This test used to pin a 20-byte header-only
	// request ("RFC 5389: no attributes"). That shape is answered by a generic
	// STUN server but is REJECTED by derper's stunserver with ErrNoFingerprint
	// ("STUN request didn't end in fingerprint") and counted as not_stun — live
	// on the agent VM the relay's own counters went {not_stun:20,success:0} →
	// {not_stun:24,success:0} for four bare probes, and {success:1} for one
	// request carrying SOFTWARE+FINGERPRINT. The default shape therefore carries
	// them now; the bare form is pinned separately as the fallback.
	pkt, txID, err := buildSTUNBindingRequest()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if want := stunHeaderLen + stunAttrHeaderLen + len(stunSoftwareValue) + 8; len(pkt) != want {
		t.Fatalf("packet len = %d, want %d (header + SOFTWARE + FINGERPRINT)", len(pkt), want)
	}
	if mt := binary.BigEndian.Uint16(pkt[0:2]); mt != stunBindingRequest {
		t.Errorf("message type = 0x%04x, want 0x%04x", mt, stunBindingRequest)
	}
	if l := int(binary.BigEndian.Uint16(pkt[2:4])); l != len(pkt)-stunHeaderLen {
		t.Errorf("declared attribute length = %d, want %d (must include the fingerprint)", l, len(pkt)-stunHeaderLen)
	}
	if mc := binary.BigEndian.Uint32(pkt[4:8]); mc != stunMagicCookie {
		t.Errorf("magic cookie = 0x%08x, want 0x%08x", mc, stunMagicCookie)
	}
	if len(txID) != 12 {
		t.Fatalf("transaction id len = %d, want 12", len(txID))
	}
	if got := pkt[8:20]; string(got) != string(txID) {
		t.Errorf("packet transaction id %x != returned txID %x", got, txID)
	}

	// SOFTWARE (type 0x8022, 8 bytes, unpadded).
	if at := binary.BigEndian.Uint16(pkt[20:22]); at != stunAttrSoftware {
		t.Errorf("first attribute type = 0x%04x, want SOFTWARE 0x%04x", at, stunAttrSoftware)
	}
	if al := binary.BigEndian.Uint16(pkt[22:24]); int(al) != len(stunSoftwareValue) {
		t.Errorf("SOFTWARE length = %d, want %d", al, len(stunSoftwareValue))
	}
	if got := string(pkt[24:32]); got != stunSoftwareValue {
		t.Errorf("SOFTWARE value = %q, want %q", got, stunSoftwareValue)
	}

	// FINGERPRINT (type 0x8028, 4 bytes) whose CRC32 XOR 0x5354554e covers
	// everything BEFORE the attribute — the check ParseBindingRequest performs.
	if at := binary.BigEndian.Uint16(pkt[32:34]); at != stunAttrFingerprint {
		t.Fatalf("second attribute type = 0x%04x, want FINGERPRINT 0x%04x", at, stunAttrFingerprint)
	}
	if al := binary.BigEndian.Uint16(pkt[34:36]); al != 4 {
		t.Errorf("FINGERPRINT length = %d, want 4", al)
	}
	wantFP := crc32.ChecksumIEEE(pkt[:32]) ^ stunFingerprintXORMask
	if got := binary.BigEndian.Uint32(pkt[36:40]); got != wantFP {
		t.Errorf("fingerprint = 0x%08x, want 0x%08x (derper answers only a valid one)", got, wantFP)
	}

	// A second request must use a fresh transaction ID (otherwise a
	// late response to a previous probe would be accepted).
	pkt2, txID2, err := buildSTUNBindingRequest()
	if err != nil {
		t.Fatalf("build 2: %v", err)
	}
	if string(txID2) == string(txID) {
		t.Errorf("second request reused transaction id %x", txID)
	}
	if string(pkt2[8:20]) != string(txID2) {
		t.Errorf("second packet txID mismatch")
	}

	// The bare fallback shape stays available for generic STUN servers.
	bare, bareTx, err := buildSTUNBareBindingRequest()
	if err != nil {
		t.Fatalf("bare build: %v", err)
	}
	if len(bare) != stunHeaderLen {
		t.Errorf("bare packet len = %d, want %d", len(bare), stunHeaderLen)
	}
	if l := binary.BigEndian.Uint16(bare[2:4]); l != 0 {
		t.Errorf("bare declared attribute length = %d, want 0", l)
	}
	if len(bareTx) != 12 || string(bare[8:20]) != string(bareTx) {
		t.Errorf("bare transaction id mismatch")
	}
}

func TestParseSTUNBindingResponse_XorMappedIPv4(t *testing.T) {
	_, txID, err := buildSTUNBindingRequest()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	body := encodeMappedAddr(net.ParseIP("95.165.170.190"), 41641, true, txID)
	resp := buildSTUNResponse(txID, body, stunAttrXorMappedAddress)
	got, err := parseSTUNBindingResponse(resp, txID)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got != "95.165.170.190:41641" {
		t.Errorf("XOR-MAPPED-ADDRESS decoded to %q, want 95.165.170.190:41641", got)
	}
}

func TestParseSTUNBindingResponse_XorMappedIPv6(t *testing.T) {
	_, txID, err := buildSTUNBindingRequest()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	ip := net.ParseIP("fd7a:115c:a1e0::9")
	body := encodeMappedAddr(ip, 3478, true, txID)
	resp := buildSTUNResponse(txID, body, stunAttrXorMappedAddress)
	got, err := parseSTUNBindingResponse(resp, txID)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if want := "[fd7a:115c:a1e0::9]:3478"; got != want {
		t.Errorf("XOR-MAPPED-ADDRESS (v6) decoded to %q, want %q", got, want)
	}
}

func TestParseSTUNBindingResponse_LegacyMappedAddress(t *testing.T) {
	_, txID, err := buildSTUNBindingRequest()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	body := encodeMappedAddr(net.ParseIP("100.64.0.9"), 1234, false, txID)
	resp := buildSTUNResponse(txID, body, stunAttrMappedAddress)
	got, err := parseSTUNBindingResponse(resp, txID)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got != "100.64.0.9:1234" {
		t.Errorf("MAPPED-ADDRESS decoded to %q, want 100.64.0.9:1234", got)
	}
}

func TestParseSTUNBindingResponse_RejectsForeignAndStale(t *testing.T) {
	_, txID, err := buildSTUNBindingRequest()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	body := encodeMappedAddr(net.ParseIP("192.0.2.1"), 1, true, txID)
	good := buildSTUNResponse(txID, body, stunAttrXorMappedAddress)

	// (a) too short
	if _, err := parseSTUNBindingResponse(good[:10], txID); err == nil {
		t.Error("short packet accepted; want error")
	}
	// (b) wrong message type (a Binding Request echoed back)
	bad := append([]byte(nil), good...)
	binary.BigEndian.PutUint16(bad[0:2], stunBindingRequest)
	if _, err := parseSTUNBindingResponse(bad, txID); err == nil {
		t.Error("Binding Request accepted as a response; want error")
	}
	// (c) wrong magic cookie
	bad = append([]byte(nil), good...)
	binary.BigEndian.PutUint32(bad[4:8], 0xdeadbeef)
	if _, err := parseSTUNBindingResponse(bad, txID); err == nil {
		t.Error("bad magic cookie accepted; want error")
	}
	// (d) stale transaction id — the classic false positive when a
	// probe times out and a late answer to the PREVIOUS probe
	// arrives on the same socket.
	other, _, err := buildSTUNBindingRequest()
	if err != nil {
		t.Fatalf("build other: %v", err)
	}
	if _, err := parseSTUNBindingResponse(good, other[8:20]); err == nil {
		t.Error("stale transaction-id response accepted; want error")
	}
}

func TestProbeSTUNUDP_EmptyAddress(t *testing.T) {
	if _, err := probeSTUNUDP("", time.Second); err == nil {
		t.Error("empty address accepted; want error")
	}
}

// TestProbeSTUNUDP_NoListenerFails is the negative-path guard: a
// closed UDP port must return a timeout error rather than a fake
// success. This is what makes a RED tile trustworthy (the old
// implementation was red on healthy relays and would have been red
// on dead ones for the wrong reason).
func TestProbeSTUNUDP_NoListenerFails(t *testing.T) {
	// 127.0.0.1:2 is the "compressnet" port; nothing binds it in a
	// test container, and UDP gives ICMP port-unreachable → the
	// read fails fast with connection-refused or times out.
	if _, err := probeSTUNUDP("127.0.0.1:2", 300*time.Millisecond); err == nil {
		t.Error("probe of a closed UDP port succeeded; want error")
	}
}

// TestParseDebugAccessDeniedBody pins the B265 acknowledgement of
// tsweb's 403 body, which is how the page knows the rich metrics
// are unavailable rather than zero.
func TestParseDebugAccessDeniedBody(t *testing.T) {
	if !isDebugAccessDenied([]byte("debug access denied\n")) {
		t.Error("tsweb 403 body not recognised")
	}
	if isDebugAccessDenied([]byte(`{"derp":{"accepts":1}}`)) {
		t.Error("valid JSON metrics misdetected as access-denied")
	}
}

// TestSTUNCandidateHosts_OrderAndFallback pins the probe order:
// the TLS-probe hostname first (the path clients dial), then the
// operator's explicit hints, and 127.0.0.1 only when nothing else
// is configured.
func TestSTUNCandidateHosts_OrderAndFallback(t *testing.T) {
	t.Setenv("SKYGATE_DERP_PROBE_HOST", "192.0.2.69")
	t.Setenv("SKYGATE_DERP_HOSTNAME", "derp.example.com")
	got := STUNCandidateHosts(nil, "derp.skynas.ru")
	want := []string{"derp.skynas.ru", "192.0.2.69", "derp.example.com"}
	if len(got) != len(want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("candidate[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}

	// Nothing configured → loopback so the probe fails visibly
	// instead of probing nothing.
	t.Setenv("SKYGATE_DERP_PROBE_HOST", "")
	t.Setenv("SKYGATE_DERP_HOSTNAME", "")
	got = STUNCandidateHosts(nil, "")
	if len(got) != 1 || got[0] != "127.0.0.1" {
		t.Errorf("empty config candidates = %v, want [127.0.0.1]", got)
	}
}
