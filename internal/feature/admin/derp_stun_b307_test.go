// B307 (v1.5.72) — the STUN probe must speak the dialect the relay requires.
//
// Root cause, proven live on the agent VM (2026-09-23): derper's STUN server is
// tailscale.com/net/stunserver, which parses requests with
// stun.ParseBindingRequest — and that returns ErrNoFingerprint
// ("STUN request didn't end in fingerprint") for a request that does not END in a
// valid FINGERPRINT attribute. The relay's own counters, read from /debug/vars:
//
//	before:                              {"not_stun":20,"success":0}
//	4 bare RFC 5389 Binding Requests  →  {"not_stun":24,"success":0}
//	1 request with SOFTWARE+FINGERPRINT → {"not_stun":25,"success":1}
//
// and a bare request to 127.0.0.1:3478 was silent while the same request with the
// two attributes came back "REPLY 44 bytes (txid match=True)".
//
// These tests pin the behaviour against a local server that implements the same
// rule, so the fix cannot silently regress.
package admin

import (
	"encoding/binary"
	"hash/crc32"
	"net"
	"strings"
	"testing"
	"time"
)

// tailscaleStyleSTUNServer answers only requests that end in a valid
// FINGERPRINT attribute — the rule derper's stunserver enforces.
func tailscaleStyleSTUNServer(t *testing.T) (addr string, sawSoftware *bool, sawFingerprint *bool) {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	soft, fp := false, false
	go func() {
		buf := make([]byte, 2048)
		for {
			n, peer, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			pkt := buf[:n]
			if len(pkt) < stunHeaderLen || binary.BigEndian.Uint32(pkt[4:8]) != stunMagicCookie {
				continue // not STUN at all
			}
			// Walk the attributes: require SOFTWARE and a VALID trailing
			// FINGERPRINT, exactly like stun.ParseBindingRequest.
			declared := int(binary.BigEndian.Uint16(pkt[2:4]))
			end := stunHeaderLen + declared
			if end > len(pkt) {
				continue
			}
			hasSoftware, fingerprintOK := false, false
			for off := stunHeaderLen; off+stunAttrHeaderLen <= end; {
				at := binary.BigEndian.Uint16(pkt[off : off+2])
				al := int(binary.BigEndian.Uint16(pkt[off+2 : off+4]))
				val := off + stunAttrHeaderLen
				if val+al > len(pkt) {
					break
				}
				if at == stunAttrSoftware {
					hasSoftware = string(pkt[val:val+al]) == stunSoftwareValue
				}
				if at == stunAttrFingerprint && al == 4 {
					want := crc32.ChecksumIEEE(pkt[:off]) ^ stunFingerprintXORMask
					fingerprintOK = binary.BigEndian.Uint32(pkt[val:val+4]) == want
				}
				off = val + al
				if pad := al % 4; pad != 0 {
					off += 4 - pad
				}
			}
			if hasSoftware {
				soft = true
			}
			if !fingerprintOK {
				continue // derper's rule: no valid fingerprint → no answer
			}
			fp = true
			txID := pkt[8:20]
			resp := appendU16(nil, stunBindingSuccess)
			resp = appendU16(resp, 12)
			resp = appendU32(resp, stunMagicCookie)
			resp = append(resp, txID...)
			resp = appendU16(resp, stunAttrXorMappedAddress)
			resp = appendU16(resp, 8)
			resp = append(resp, 0, 0x01)
			resp = appendU16(resp, 0x1234^uint16(stunMagicCookie>>16))
			resp = append(resp, 127^0x21, 0^0x12, 0^0xa4, 1^0x42)
			_, _ = pc.WriteTo(resp, peer)
		}
	}()
	time.Sleep(20 * time.Millisecond)
	return pc.LocalAddr().String(), &soft, &fp
}

func TestProbeSTUNSpeaksFingerprint_B307(t *testing.T) {
	addr, sawSoftware, sawFingerprint := tailscaleStyleSTUNServer(t)
	res, err := probeSTUNUDP(addr, 2*time.Second)
	if err != nil {
		t.Fatalf("probe against a fingerprint-requiring server failed: %v "+
			"(a bare request is rejected as not_stun — that was the live defect)", err)
	}
	if res.ReflexiveAddr == "" {
		t.Errorf("probe succeeded but carried no reflexive address: %+v", res)
	}
	if !*sawSoftware {
		t.Error("the request carried no SOFTWARE attribute")
	}
	if !*sawFingerprint {
		t.Error("the request carried no VALID fingerprint — derper would count it as not_stun")
	}
}

func TestProbeSTUNFallsBackToBareForGenericServers_B307(t *testing.T) {
	// A pedantic generic STUN server that only answers header-only requests:
	// the fingerprint shape is ignored, so the probe must retry with the bare
	// form instead of reporting the relay as closed.
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer pc.Close()
	go func() {
		buf := make([]byte, 2048)
		for {
			n, peer, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			pkt := buf[:n]
			if len(pkt) < stunHeaderLen || binary.BigEndian.Uint16(pkt[2:4]) != 0 {
				continue // only the bare shape is answered here
			}
			resp := appendU16(nil, stunBindingSuccess)
			resp = appendU16(resp, 12)
			resp = appendU32(resp, stunMagicCookie)
			resp = append(resp, pkt[8:20]...)
			resp = appendU16(resp, stunAttrXorMappedAddress)
			resp = appendU16(resp, 8)
			resp = append(resp, 0, 0x01)
			resp = appendU16(resp, 0x4321^uint16(stunMagicCookie>>16))
			resp = append(resp, 10^0x21, 0^0x12, 0^0xa4, 2^0x42)
			_, _ = pc.WriteTo(resp, peer)
		}
	}()
	time.Sleep(20 * time.Millisecond)
	res, err := probeSTUNUDP(pc.LocalAddr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("probe did not fall back to the bare request: %v", err)
	}
	if res.ReflexiveAddr == "" {
		t.Errorf("fallback probe returned no reflexive address: %+v", res)
	}
}

func TestProbeSTUNFailureNamesBothShapes_B307(t *testing.T) {
	// A closed UDP port: the error must name BOTH attempts, so a red tile never
	// hides which packet was sent.
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := pc.LocalAddr().String()
	_ = pc.Close()

	_, err = probeSTUNUDP(addr, 400*time.Millisecond)
	if err == nil {
		t.Fatal("probe against a closed port succeeded")
	}
	msg := err.Error()
	if !strings.Contains(msg, "fingerprint form") || !strings.Contains(msg, "bare form") {
		t.Errorf("error = %q, want it to name both request shapes", msg)
	}
}
