package admin

// derp_stun_b315_test.go — B315: the STUN probe must survive a NAT that rewrites
// the relay's source address.
//
// # THE LIVE DEFECT THIS PINS
//
// The pre-B315 probe used a CONNECTED UDP socket (`net.DialTimeout("udp", …)`).
// Linux only delivers a datagram to a connected UDP socket when its SOURCE
// matches the peer it was connected to, so when something on the path rewrites
// the relay's address the kernel drops the reply before Go ever sees it and the
// probe reports `i/o timeout` on a relay that answered immediately.
//
// Measured on the agent VM, same network namespace, same instant:
//
//	unconnected (python)  -> 192.168.13.69:3478  REPLY 0x0101, source 172.18.0.1
//	connected   (Go)      -> 192.168.13.69:3478  read udp 172.18.0.3:…: i/o timeout
//	connected   (Go)      -> 172.18.0.1:3478     REPLY, rtt 198µs
//
// The operator's "STUN UDP :3478 closed" tile was therefore partly skygate's own
// instrument lying about a healthy relay.
//
// The test below reproduces that exact shape locally and deterministically: the
// probe dials socket A, and the answer is sent from socket B.

import (
	"net"
	"strconv"
	"testing"
	"time"
)

// stunServerReplyingFromAnotherAddress listens on one address and answers from
// another, like a DNAT'd path does.
//
// Returns the address to dial and a stop function.
func stunServerReplyingFromAnotherAddress(t *testing.T) (dial string, replyFrom string) {
	t.Helper()
	// The socket the probe dials. It never answers from itself.
	front, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("front socket: %v", err)
	}
	t.Cleanup(func() { _ = front.Close() })
	// The socket the answer actually comes from (the NAT's address).
	back, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("back socket: %v", err)
	}
	t.Cleanup(func() { _ = back.Close() })

	go func() {
		buf := make([]byte, 2048)
		for {
			n, peer, rerr := front.ReadFrom(buf)
			if rerr != nil {
				return
			}
			pkt := buf[:n]
			if len(pkt) < stunHeaderLen {
				continue
			}
			resp := appendU16(nil, stunBindingSuccess)
			resp = appendU16(resp, 12)
			resp = appendU32(resp, stunMagicCookie)
			resp = append(resp, pkt[8:20]...) // echo the transaction id
			resp = appendU16(resp, stunAttrXorMappedAddress)
			resp = appendU16(resp, 8)
			resp = append(resp, 0, 0x01)
			resp = appendU16(resp, 0x2222^uint16(stunMagicCookie>>16))
			resp = append(resp, 203^0x21, 0^0x12, 113^0xa4, 7^0x42)
			// Sent from the OTHER socket: this is the rewrite.
			_, _ = back.WriteTo(resp, peer)
		}
	}()
	time.Sleep(20 * time.Millisecond)
	return front.LocalAddr().String(), back.LocalAddr().String()
}

// TestProbeSTUNAcceptsARewrittenReplySource_B315 is the regression test for the
// connected-socket defect: a reply from an address other than the one dialled
// must be accepted (the transaction id, not the source address, is what proves
// the datagram is ours).
func TestProbeSTUNAcceptsARewrittenReplySource_B315(t *testing.T) {
	dial, replyFrom := stunServerReplyingFromAnotherAddress(t)

	res, err := probeSTUNUDP(dial, 2*time.Second)
	if err != nil {
		t.Fatalf("probe against a relay whose reply is rewritten timed out: %v "+
			"(this is the live agent-VM defect: a CONNECTED udp socket drops a datagram whose source is not the peer)", err)
	}
	if res.ReflexiveAddr == "" {
		t.Fatalf("no reflexive address in %+v", res)
	}
	if res.ReplyFrom != replyFrom {
		t.Fatalf("ReplyFrom = %q, want the rewritten source %q (the page reports this so a NAT'd path is visible)",
			res.ReplyFrom, replyFrom)
	}
}

// TestProbeSTUNStillRejectsAForeignTransaction_B315 pins the security half of that
// trade: an unconnected socket accepts datagrams from anyone, so a reply carrying
// someone else's transaction id must NOT be treated as an answer.
func TestProbeSTUNStillRejectsAForeignTransaction_B315(t *testing.T) {
	front, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("socket: %v", err)
	}
	defer front.Close()
	go func() {
		buf := make([]byte, 2048)
		for {
			n, peer, rerr := front.ReadFrom(buf)
			if rerr != nil {
				return
			}
			pkt := buf[:n]
			if len(pkt) < stunHeaderLen {
				continue
			}
			resp := appendU16(nil, stunBindingSuccess)
			resp = appendU16(resp, 12)
			resp = appendU32(resp, stunMagicCookie)
			foreign := make([]byte, 12) // 12 zero bytes: not our transaction id
			resp = append(resp, foreign...)
			resp = appendU16(resp, stunAttrXorMappedAddress)
			resp = appendU16(resp, 8)
			resp = append(resp, 0, 0x01)
			resp = appendU16(resp, 0x1111^uint16(stunMagicCookie>>16))
			resp = append(resp, 127^0x21, 0^0x12, 0^0xa4, 1^0x42)
			_, _ = front.WriteTo(resp, peer)
		}
	}()
	time.Sleep(20 * time.Millisecond)

	if _, err := probeSTUNUDP(front.LocalAddr().String(), 400*time.Millisecond); err == nil {
		t.Fatal("a reply with a FOREIGN transaction id was accepted as an answer")
	}
}

// TestProbeSTUNForStatusReportsTheRewrittenSource_B315 pins the path from the
// socket to the page: the tile only shows the note when the answering address
// differs from the address that was dialled.
func TestProbeSTUNForStatusReportsTheRewrittenSource_B315(t *testing.T) {
	t.Setenv("SKYGATE_DERP_HOSTNAME", "")
	t.Setenv("SKYGATE_DERP_PROBE_HOST", "")
	dial, replyFrom := stunServerReplyingFromAnotherAddress(t)
	host, portStr, err := net.SplitHostPort(dial)
	if err != nil {
		t.Fatalf("split %q: %v", dial, err)
	}
	if _, err := strconv.Atoi(portStr); err != nil {
		t.Fatalf("port %q: %v", portStr, err)
	}

	var st DerpStatus
	probeSTUNForStatus(&st, nil, host, portStr)
	if !st.STUNListening {
		t.Fatalf("probe failed: %s", st.STUNErr)
	}
	if st.STUNReplyFrom != replyFrom {
		t.Fatalf("STUNReplyFrom = %q, want %q", st.STUNReplyFrom, replyFrom)
	}
}

// TestProbeSTUNForStatusStaysQuietOnANormalRelay_B315 is the other half: when the
// answer comes from the address that was dialled (every ordinary deployment)
// there is nothing to report, so the tile must not grow a permanent note.
func TestProbeSTUNForStatusStaysQuietOnANormalRelay_B315(t *testing.T) {
	t.Setenv("SKYGATE_DERP_HOSTNAME", "")
	t.Setenv("SKYGATE_DERP_PROBE_HOST", "")
	addr, _, _ := tailscaleStyleSTUNServer(t)
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split %q: %v", addr, err)
	}
	var st DerpStatus
	probeSTUNForStatus(&st, nil, host, portStr)
	if !st.STUNListening {
		t.Fatalf("probe failed: %s", st.STUNErr)
	}
	if st.STUNReplyFrom != "" {
		t.Fatalf("STUNReplyFrom = %q, want empty when the relay answers from its own address", st.STUNReplyFrom)
	}
}
