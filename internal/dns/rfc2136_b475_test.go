// rfc2136_b475_test.go — B475: the RFC 2136 + TSIG adapter against a FAKE authoritative server.
//
// The adapter speaks a binary protocol, so "does it look right" is not a question that can be answered by
// reading it. Two things must be proven on the wire:
//
//  1. THE SIGNATURE. A TSIG MAC is an HMAC over the message PLUS a block of variables, and the message
//     included in the digest is the one BEFORE the TSIG record was appended (ARCOUNT without it). A
//     signer that bumps the count first produces a MAC that verifies nowhere, and the symptom a real
//     server gives is NOTAUTH — which reads like "wrong key". The fake therefore RECOMPUTES the digest
//     from the bytes it received, with its own implementation, and answers BADSIG when it disagrees.
//  2. THE UPDATE SEMANTICS. RFC 2136 has no "set" operation: replacing an address means deleting the
//     RRset and adding the new record, and deleting one TXT means class NONE with the exact RDATA (class
//     ANY would delete every TXT at the name). The fake applies the operations to an in-memory zone, so
//     "the record is now X" is asserted rather than "the bytes contained a 1".
//
// Everything runs over 127.0.0.1 with an ephemeral port; nothing here needs a real DNS server.
package dns

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeDNSZone is the in-memory zone the fake server serves and updates.
type fakeDNSZone struct {
	mu      sync.Mutex
	soa     bool
	records map[string][]dnsRR // key: name|type
	updates int                // how many UPDATE messages arrived
	queries int
	lastRaw []byte // the last UPDATE as received (signed)
}

func newFakeDNSZone(soa bool) *fakeDNSZone {
	return &fakeDNSZone{soa: soa, records: map[string][]dnsRR{}}
}

func recKey(name string, typ uint16) string { return fmt.Sprintf("%s|%d", name, typ) }

func (z *fakeDNSZone) put(rr dnsRR) {
	z.mu.Lock()
	defer z.mu.Unlock()
	z.records[recKey(rr.Name, rr.Type)] = append(z.records[recKey(rr.Name, rr.Type)], rr)
}

func (z *fakeDNSZone) deleteRRset(name string, typ uint16) {
	z.mu.Lock()
	defer z.mu.Unlock()
	delete(z.records, recKey(name, typ))
}

func (z *fakeDNSZone) deleteExact(rr dnsRR) {
	z.mu.Lock()
	defer z.mu.Unlock()
	k := recKey(rr.Name, rr.Type)
	kept := []dnsRR{}
	for _, cur := range z.records[k] {
		if !bytes.Equal(cur.Data, rr.Data) {
			kept = append(kept, cur)
		}
	}
	z.records[k] = kept
}

func (z *fakeDNSZone) lookup(name string, typ uint16) []dnsRR {
	z.mu.Lock()
	defer z.mu.Unlock()
	return append([]dnsRR(nil), z.records[recKey(name, typ)]...)
}

// fakeDNS is the server: it verifies TSIG on every message and answers like BIND would.
type fakeDNS struct {
	t       *testing.T
	zone    *fakeDNSZone
	keyName string
	algo    string
	secret  []byte
	// badSecret makes the server verify with a DIFFERENT key than the client signed with.
	badSecret []byte
	// rcode forces an rcode other than NOERROR.
	rcode int
	// timeSkew shifts the server's TSIG time-signed, to model a clock problem.
	timeSkew int64
	srv      net.Listener
}

func newFakeDNS(t *testing.T, z *fakeDNSZone) *fakeDNS {
	t.Helper()
	secret := []byte("0123456789abcdef0123456789abcdef")
	f := &fakeDNS{
		t: t, zone: z, keyName: "skygate-key.", algo: "hmac-sha256",
		secret: secret, badSecret: secret,
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	f.srv = ln
	t.Cleanup(func() { ln.Close() })
	go f.serve()
	return f
}

func (f *fakeDNS) addr() string { return f.srv.Addr().String() }

func (f *fakeDNS) serve() {
	for {
		conn, err := f.srv.Accept()
		if err != nil {
			return
		}
		go f.handle(conn)
	}
}

// handle reads one framed message, verifies it, applies it and writes one signed response.
func (f *fakeDNS) handle(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	var lenBuf [2]byte
	if _, err := io.ReadFull(conn, lenBuf[:]); err != nil {
		return
	}
	n := int(binary.BigEndian.Uint16(lenBuf[:]))
	raw := make([]byte, n)
	if _, err := io.ReadFull(conn, raw); err != nil {
		return
	}
	msg, err := parseResponse(raw)
	if err != nil {
		f.writeRcode(conn, raw, 1, nil) // FORMERR
		return
	}
	reqMAC, ok := f.verify(raw, msg)
	if !ok {
		f.writeRcode(conn, raw, 9, nil) // NOTAUTH
		return
	}
	var answer []dnsRR
	if msg.Opcode == dnsOpcodeUpdate {
		f.zone.mu.Lock()
		f.zone.updates++
		f.zone.lastRaw = append([]byte(nil), raw...)
		f.zone.mu.Unlock()
		if f.rcode != 0 {
			f.writeRcode(conn, raw, f.rcode, reqMAC)
			return
		}
		for _, rr := range msg.Update {
			switch {
			case rr.Class == dnsClassANY:
				f.zone.deleteRRset(rr.Name, rr.Type)
			case rr.Class == dnsClassNONE:
				f.zone.deleteExact(dnsRR{Name: rr.Name, Type: rr.Type, Class: dnsClassIN, Data: rr.Data})
			case rr.Class == dnsClassIN:
				f.zone.deleteRRset(rr.Name, rr.Type) // an add replaces the RRset in these tests
				f.zone.put(rr)
			}
		}
		f.writeRcode(conn, raw, 0, reqMAC)
		return
	}
	// A QUERY: name in the zone section, type in ZoneType.
	f.zone.mu.Lock()
	f.zone.queries++
	f.zone.mu.Unlock()
	switch msg.ZoneType {
	case dnsTypeSOA:
		if f.zone.soa {
			answer = append(answer, dnsRR{Name: msg.Zone, Type: dnsTypeSOA, Class: dnsClassIN, TTL: 3600, Data: []byte{0}})
		}
	case dnsTypeA, dnsTypeTXT:
		answer = append(answer, f.zone.lookup(msg.Zone, msg.ZoneType)...)
	}
	f.respond(conn, raw, msg, 0, answer, reqMAC)
}

// verify recomputes the request digest from the RECEIVED bytes, independently of the signer: strip the
// TSIG RR, decrement ARCOUNT, HMAC the remainder with the TSIG variables read out of that RR.
func (f *fakeDNS) verify(raw []byte, msg dnsMessage) ([]byte, bool) {
	if len(msg.Extra) == 0 {
		f.t.Errorf("the request carried no additional section — an unsigned UPDATE would be REFUSED by a real server")
		return nil, false
	}
	tsigRR := msg.Extra[len(msg.Extra)-1]
	if tsigRR.Type != dnsTypeTSIG {
		f.t.Errorf("the last additional record is type %d, not TSIG (250)", tsigRR.Type)
		return nil, false
	}
	data := tsigRR.Data
	algo, off, err := decodeName(data, 0)
	if err != nil || dnsAbsName(algo) != dnsAbsName(f.algo) {
		f.t.Errorf("TSIG algorithm %q, want %q (%v)", algo, f.algo, err)
		return nil, false
	}
	if off+6+2+2 > len(data) {
		f.t.Errorf("TSIG rdata truncated")
		return nil, false
	}
	timeSigned := uint64(data[off])<<40 | uint64(data[off+1])<<32 | uint64(data[off+2])<<24 |
		uint64(data[off+3])<<16 | uint64(data[off+4])<<8 | uint64(data[off+5])
	off += 6
	fudge := binary.BigEndian.Uint16(data[off:])
	off += 2
	macSize := int(binary.BigEndian.Uint16(data[off:]))
	off += 2
	if off+macSize+2+2+2 > len(data) {
		f.t.Errorf("TSIG rdata truncated")
		return nil, false
	}
	gotMAC := data[off : off+macSize]
	off += macSize
	origID := binary.BigEndian.Uint16(data[off:])
	off += 2
	tsigErr := binary.BigEndian.Uint16(data[off:])
	off += 2
	otherLen := int(binary.BigEndian.Uint16(data[off:]))
	off += 2
	var other []byte
	if off+otherLen <= len(data) {
		other = data[off : off+otherLen]
	}
	if gotMAC == nil || len(gotMAC) == 0 {
		f.t.Errorf("the request's TSIG MAC is empty")
		return nil, false
	}
	// Clock window.
	if skew := time.Now().Unix() - int64(timeSigned); skew > int64(fudge) || skew < -int64(fudge) {
		f.t.Errorf("request time signed differs by %ds (fudge %ds)", skew, fudge)
		return nil, false
	}
	unsigned := stripLastRR(raw)
	ar := binary.BigEndian.Uint16(unsigned[10:12])
	binary.BigEndian.PutUint16(unsigned[10:12], ar-1)

	keyName := f.keyName
	if keyName == "" {
		keyName = dnsAbsName(tsigRR.Name)
	}
	vars := encodeName(keyName)
	vars = binary.BigEndian.AppendUint16(vars, dnsClassANY)
	vars = binary.BigEndian.AppendUint32(vars, 0)
	vars = append(vars, encodeName(f.algo)...)
	vars = append(vars, byte(timeSigned>>40), byte(timeSigned>>32), byte(timeSigned>>24),
		byte(timeSigned>>16), byte(timeSigned>>8), byte(timeSigned))
	vars = binary.BigEndian.AppendUint16(vars, fudge)
	vars = binary.BigEndian.AppendUint16(vars, tsigErr)
	vars = binary.BigEndian.AppendUint16(vars, uint16(len(other)))
	vars = append(vars, other...)

	m := hmac.New(sha256.New, f.badSecret)
	m.Write(unsigned)
	m.Write(vars)
	if !hmac.Equal(m.Sum(nil), gotMAC) {
		f.t.Logf("BADSIG: the request's MAC did not verify with key %q", f.keyName)
		return nil, false
	}
	if origID != msg.ID {
		f.t.Errorf("TSIG original id %d != message id %d", origID, msg.ID)
	}
	return gotMAC, true
}

// writeRcode answers with the given rcode and no records, signed.
func (f *fakeDNS) writeRcode(conn net.Conn, raw []byte, rcode int, reqMAC []byte) {
	msg, _ := parseResponse(raw)
	f.respond(conn, raw, msg, rcode, nil, reqMAC)
}

// respond writes ONE signed answer: the question is echoed, the records go to the answer section.
func (f *fakeDNS) respond(conn net.Conn, raw []byte, req dnsMessage, rcode int, answers []dnsRR, reqMAC []byte) {
	id := binary.BigEndian.Uint16(raw[0:2])
	resp := dnsMessage{ID: id, Opcode: req.Opcode, Zone: req.Zone, ZoneType: req.ZoneType}
	resp.Flags = dnsFlagQR | uint16(rcode)
	// Reuse the builder: the RRs go into the "Prereq" slot because wire() writes the sections in order
	// and a response's ANCOUNT is that field.
	resp.Prereq = answers
	wire := resp.wire()
	signed, _ := f.signResponse(wire, reqMAC)
	framed := binary.BigEndian.AppendUint16(make([]byte, 0, len(signed)+2), uint16(len(signed)))
	framed = append(framed, signed...)
	_, _ = conn.Write(framed)
}

// signResponse signs like the client does: the digest covers the pre-append message, then the TSIG RR is
// appended with ARCOUNT incremented, and the REQUEST's MAC is mixed in first.
//
// When the server could not authenticate the REQUEST (reqMAC empty) the answer carries a TSIG with an
// EMPTY MAC — RFC 8945 §5.3.2 — which is exactly what a real BIND does for BADSIG/BADKEY/NOTAUTH, and
// what the client's verification branch for macSize==0 exists for.
func (f *fakeDNS) signResponse(msg []byte, reqMAC []byte) ([]byte, []byte) {
	now := uint64(time.Now().Unix() + f.timeSkew)
	var mac []byte
	if len(reqMAC) > 0 {
		m := hmac.New(sha256.New, f.secret)
		m.Write(msg)
		m.Write(binary.BigEndian.AppendUint16(nil, uint16(len(reqMAC))))
		m.Write(reqMAC)
		vars := encodeName(f.keyName)
		vars = binary.BigEndian.AppendUint16(vars, dnsClassANY)
		vars = binary.BigEndian.AppendUint32(vars, 0)
		vars = append(vars, encodeName(f.algo)...)
		vars = append(vars, byte(now>>40), byte(now>>32), byte(now>>24), byte(now>>16), byte(now>>8), byte(now))
		vars = binary.BigEndian.AppendUint16(vars, f.fudgeOrDefault())
		vars = binary.BigEndian.AppendUint16(vars, 0)
		vars = binary.BigEndian.AppendUint16(vars, 0)
		m.Write(vars)
		mac = m.Sum(nil)
	}

	rdata := append(encodeName(f.algo), byte(now>>40), byte(now>>32), byte(now>>24),
		byte(now>>16), byte(now>>8), byte(now))
	rdata = binary.BigEndian.AppendUint16(rdata, f.fudgeOrDefault())
	rdata = binary.BigEndian.AppendUint16(rdata, uint16(len(mac)))
	rdata = append(rdata, mac...)
	rdata = binary.BigEndian.AppendUint16(rdata, binary.BigEndian.Uint16(msg[0:2]))
	rdata = binary.BigEndian.AppendUint16(rdata, 0)
	rdata = binary.BigEndian.AppendUint16(rdata, 0)

	signed := append([]byte(nil), msg...)
	ar := binary.BigEndian.Uint16(signed[10:12])
	binary.BigEndian.PutUint16(signed[10:12], ar+1)
	out := append(signed, encodeName(f.keyName)...)
	out = binary.BigEndian.AppendUint16(out, dnsTypeTSIG)
	out = binary.BigEndian.AppendUint16(out, dnsClassANY)
	out = binary.BigEndian.AppendUint32(out, 0)
	out = binary.BigEndian.AppendUint16(out, uint16(len(rdata)))
	out = append(out, rdata...)
	return out, mac
}

func (f *fakeDNS) fudgeOrDefault() uint16 { return 300 }

// rfc2136TestProvider builds the adapter against the fake, with the matching base64 secret.
func rfc2136TestProvider(t *testing.T, f *fakeDNS) *rfc2136Provider {
	t.Helper()
	row := extRow("rfc2136", "example.com", map[string]string{
		"server":     f.addr(),
		"key_name":   "skygate-key",
		"key_secret": base64.StdEncoding.EncodeToString(f.secret),
		"key_algo":   "hmac-sha256",
	})
	p, err := BuildProvider("rfc2136", BuildDeps{Row: &row})
	if err != nil {
		t.Fatalf("BuildProvider(rfc2136) = %v", err)
	}
	cp, ok := p.(*rfc2136Provider)
	if !ok {
		t.Fatalf("BuildProvider returned %T", p)
	}
	cp.timeout = 3 * time.Second
	return cp
}

// TestB475_UpdateIsSignedAndApplied — the happy path, asserted on the SERVER's side: the signature
// verified (the fake refuses otherwise), the RRset was replaced and the new address is what a later read
// returns.
func TestB475_UpdateIsSignedAndApplied(t *testing.T) {
	zone := newFakeDNSZone(true)
	f := newFakeDNS(t, zone)
	p := rfc2136TestProvider(t, f)
	ctx := context.Background()

	if err := p.UpdateRecord(ctx, "example.com", "skygate", "192.0.2.10"); err != nil {
		t.Fatalf("UpdateRecord: %v", err)
	}
	if zone.updates != 1 {
		t.Fatalf("the server saw %d UPDATE messages, want 1", zone.updates)
	}
	got := zone.lookup("skygate.example.com.", dnsTypeA)
	if len(got) != 1 {
		t.Fatalf("the zone holds %d A records, want exactly 1 (the RRset must be replaced, not appended)", len(got))
	}
	if ip := net.IP(got[0].Data).String(); ip != "192.0.2.10" {
		t.Errorf("the zone holds %s, want 192.0.2.10", ip)
	}
	// A second update replaces the first.
	if err := p.UpdateRecord(ctx, "example.com", "skygate", "192.0.2.11"); err != nil {
		t.Fatalf("UpdateRecord (second): %v", err)
	}
	if got := zone.lookup("skygate.example.com.", dnsTypeA); len(got) != 1 || net.IP(got[0].Data).String() != "192.0.2.11" {
		t.Errorf("after the second update the zone holds %+v", got)
	}
	// And reading it back through the provider works.
	ip, err := p.GetRecord(ctx, "example.com", "skygate")
	if err != nil {
		t.Fatalf("GetRecord: %v", err)
	}
	if ip != "192.0.2.11" {
		t.Errorf("GetRecord = %s, want 192.0.2.11", ip)
	}
	// A non-IP target is refused before anything is sent.
	before := zone.updates
	if err := p.UpdateRecord(ctx, "example.com", "skygate", "not-an-ip"); err == nil {
		t.Error("UpdateRecord accepted a non-IP value")
	}
	if zone.updates != before {
		t.Error("UpdateRecord sent an UPDATE before validating the address")
	}
}

// TestB475_DigestCoversThePreAppendArcount — the trap this adapter is written around, proven: the same
// bytes verify with the ARCOUNT decremented and do NOT verify with the count left as sent. If a future
// refactor bumps the count before hashing, the first half of this test fails on the server side and the
// second half shows exactly why.
func TestB475_DigestCoversThePreAppendArcount(t *testing.T) {
	zone := newFakeDNSZone(true)
	f := newFakeDNS(t, zone)
	p := rfc2136TestProvider(t, f)
	if err := p.UpdateRecord(context.Background(), "example.com", "skygate", "192.0.2.10"); err != nil {
		t.Fatalf("UpdateRecord: %v", err)
	}
	raw := append([]byte(nil), zone.lastRaw...)
	msg, err := parseResponse(raw)
	if err != nil {
		t.Fatalf("parse the sent message: %v", err)
	}
	tsig := msg.Extra[len(msg.Extra)-1]
	if tsig.Type != dnsTypeTSIG {
		t.Fatal("the sent message does not end with a TSIG record")
	}
	// Recompute both ways.
	pre := stripLastRR(raw)
	ar := binary.BigEndian.Uint16(pre[10:12])
	binary.BigEndian.PutUint16(pre[10:12], ar-1)
	post := stripLastRR(raw) // ARCOUNT left as sent

	// Pull the variables out of the TSIG rdata.
	data := tsig.Data
	algo, off, _ := decodeName(data, 0)
	timeSigned := uint64(data[off])<<40 | uint64(data[off+1])<<32 | uint64(data[off+2])<<24 |
		uint64(data[off+3])<<16 | uint64(data[off+4])<<8 | uint64(data[off+5])
	off += 6
	fudge := binary.BigEndian.Uint16(data[off:])
	off += 2
	macSize := int(binary.BigEndian.Uint16(data[off:]))
	off += 2
	wantMAC := data[off : off+macSize]

	digest := func(msgBytes []byte) []byte {
		m := hmac.New(sha256.New, f.secret)
		m.Write(msgBytes)
		vars := encodeName("skygate-key.")
		vars = binary.BigEndian.AppendUint16(vars, dnsClassANY)
		vars = binary.BigEndian.AppendUint32(vars, 0)
		vars = append(vars, encodeName(algo)...)
		vars = append(vars, byte(timeSigned>>40), byte(timeSigned>>32), byte(timeSigned>>24),
			byte(timeSigned>>16), byte(timeSigned>>8), byte(timeSigned))
		vars = binary.BigEndian.AppendUint16(vars, fudge)
		vars = binary.BigEndian.AppendUint16(vars, 0)
		vars = binary.BigEndian.AppendUint16(vars, 0)
		m.Write(vars)
		return m.Sum(nil)
	}
	if !hmac.Equal(digest(pre), wantMAC) {
		t.Error("the MAC does NOT verify over the message with ARCOUNT decremented — the digest rule changed")
	}
	if hmac.Equal(digest(post), wantMAC) {
		t.Error("the MAC ALSO verifies over the message with ARCOUNT as sent — the assertion cannot discriminate")
	}
}

// TestB475_WrongSecretIsRejectedAndReported — a server that cannot verify answers NOTAUTH, and the error
// must say so instead of looking like a network failure.
func TestB475_WrongSecretIsRejectedAndReported(t *testing.T) {
	zone := newFakeDNSZone(true)
	f := newFakeDNS(t, zone)
	f.badSecret = []byte("a-different-secret-of-the-same-length!")
	p := rfc2136TestProvider(t, f)
	err := p.UpdateRecord(context.Background(), "example.com", "skygate", "192.0.2.10")
	if err == nil {
		t.Fatal("an update signed with the wrong key was accepted")
	}
	if !strings.Contains(err.Error(), "NOTAUTH") {
		t.Errorf("the error does not name the server's answer: %v", err)
	}
	if len(zone.lookup("skygate.example.com.", dnsTypeA)) != 0 {
		t.Error("the zone was modified although the signature failed")
	}
}

// TestB475_ForcedRcodesAreNamed — NOTZONE / REFUSED / NXDOMAIN all read as words, not numbers.
func TestB475_ForcedRcodesAreNamed(t *testing.T) {
	for rcode, want := range map[int]string{5: "REFUSED", 6: "YXDOMAIN", 9: "NOTAUTH", 10: "NOTZONE"} {
		zone := newFakeDNSZone(true)
		f := newFakeDNS(t, zone)
		f.rcode = rcode
		p := rfc2136TestProvider(t, f)
		err := p.UpdateRecord(context.Background(), "example.com", "skygate", "192.0.2.10")
		if err == nil {
			t.Errorf("rcode %d was reported as success", rcode)
			continue
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("rcode %d error = %v, want it to name %s", rcode, err, want)
		}
	}
}

// TestB475_RecordNotFound — an empty answer is ErrRecordNotFound, the failover's "fresh deploy" signal.
func TestB475_RecordNotFound(t *testing.T) {
	zone := newFakeDNSZone(true)
	f := newFakeDNS(t, zone)
	p := rfc2136TestProvider(t, f)
	if _, err := p.GetRecord(context.Background(), "example.com", "absent"); !errors.Is(err, ErrRecordNotFound) {
		t.Errorf("GetRecord on an absent name = %v, want ErrRecordNotFound", err)
	}
}

// TestB475_TXTOperationsUseTheExactRdata — delete must be class NONE with the exact value (class ANY would
// delete every TXT at the name, including another challenge).
func TestB475_TXTOperationsUseTheExactRdata(t *testing.T) {
	zone := newFakeDNSZone(true)
	f := newFakeDNS(t, zone)
	p := rfc2136TestProvider(t, f)
	ctx := context.Background()

	if err := p.CreateTXT(ctx, "example.com", "_acme-challenge", "challenge-1", 60); err != nil {
		t.Fatalf("CreateTXT: %v", err)
	}
	if err := p.CreateTXT(ctx, "example.com", "_acme-challenge", "challenge-2", 60); err != nil {
		t.Fatalf("CreateTXT (second): %v", err)
	}
	// Two challenges on ONE name: an RRset with two values, which is what a real server holds while two
	// orders are in flight. The fake's add replaces the RRset, so the two values are seeded explicitly.
	zone.deleteRRset("_acme-challenge.example.com.", dnsTypeTXT)
	zone.put(dnsRR{Name: "_acme-challenge.example.com.", Type: dnsTypeTXT, Class: dnsClassIN, TTL: 60, Data: txtData("challenge-1")})
	zone.put(dnsRR{Name: "_acme-challenge.example.com.", Type: dnsTypeTXT, Class: dnsClassIN, TTL: 60, Data: txtData("challenge-2")})
	if got := len(zone.lookup("_acme-challenge.example.com.", dnsTypeTXT)); got != 2 {
		t.Fatalf("seed produced %d TXT records, want 2", got)
	}
	if err := p.DeleteTXT(ctx, "example.com", "_acme-challenge", "challenge-1"); err != nil {
		t.Fatalf("DeleteTXT: %v", err)
	}
	left := zone.lookup("_acme-challenge.example.com.", dnsTypeTXT)
	if len(left) != 1 {
		t.Fatalf("after deleting one value %d TXT records remain, want 1 — the delete removed the wrong set", len(left))
	}
	if !bytes.Equal(left[0].Data, txtData("challenge-2")) {
		t.Error("the WRONG challenge value survived")
	}
	// The sent message must have carried class NONE with the exact RDATA.
	msg, err := parseResponse(zone.lastRaw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	found := false
	for _, rr := range msg.Update {
		if rr.Type == dnsTypeTXT && rr.Class == dnsClassNONE && bytes.Equal(rr.Data, txtData("challenge-1")) {
			found = true
		}
	}
	if !found {
		t.Errorf("the delete did not use class NONE with the exact TXT rdata: %+v", msg.Update)
	}
}

// TestB475_SetTTLKeepsTheAddress — the TTL rewrite replaces the RRset with the same data.
func TestB475_SetTTLKeepsTheAddress(t *testing.T) {
	zone := newFakeDNSZone(true)
	f := newFakeDNS(t, zone)
	p := rfc2136TestProvider(t, f)
	ctx := context.Background()
	if err := p.UpdateRecord(ctx, "example.com", "skygate", "192.0.2.10"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := p.SetTTL(ctx, "example.com", "skygate", 300); err != nil {
		t.Fatalf("SetTTL: %v", err)
	}
	recs := zone.lookup("skygate.example.com.", dnsTypeA)
	if len(recs) != 1 {
		t.Fatalf("%d A records after SetTTL, want 1", len(recs))
	}
	if recs[0].TTL != 300 {
		t.Errorf("TTL = %d, want 300", recs[0].TTL)
	}
	if ip := net.IP(recs[0].Data).String(); ip != "192.0.2.10" {
		t.Errorf("SetTTL changed the address to %s", ip)
	}
	if err := p.SetTTL(ctx, "example.com", "absent", 300); !errors.Is(err, ErrRecordNotFound) {
		t.Errorf("SetTTL on an absent record = %v, want ErrRecordNotFound", err)
	}
}

// TestB475_TestConnectionQueriesTheSOA — the one question that proves server + zone + key + clocks.
func TestB475_TestConnectionQueriesTheSOA(t *testing.T) {
	zone := newFakeDNSZone(true)
	f := newFakeDNS(t, zone)
	p := rfc2136TestProvider(t, f)
	if err := p.TestConnection(context.Background()); err != nil {
		t.Fatalf("TestConnection: %v", err)
	}
	if zone.queries != 1 {
		t.Errorf("the server saw %d QUERY messages, want 1", zone.queries)
	}
	if zone.updates != 0 {
		t.Error("TestConnection sent an UPDATE — a credential test must not write")
	}
	// A zone the server does not serve answers NXDOMAIN through the same path.
	empty := newFakeDNSZone(false)
	f2 := newFakeDNS(t, empty)
	f2.rcode = 3
	p2 := rfc2136TestProvider(t, f2)
	if err := p2.TestConnection(context.Background()); err == nil {
		t.Error("TestConnection reported success against a server that does not serve the zone")
	}
}

// TestB475_ClockSkewIsNamed — a signature outside the fudge window is reported as a CLOCK problem, because
// "wrong key" is the wrong diagnosis and costs an hour.
func TestB475_ClockSkewIsNamed(t *testing.T) {
	zone := newFakeDNSZone(true)
	f := newFakeDNS(t, zone)
	f.timeSkew = 3600 // the server signs two hours in the future
	p := rfc2136TestProvider(t, f)
	err := p.UpdateRecord(context.Background(), "example.com", "skygate", "192.0.2.10")
	if err == nil {
		t.Fatal("a response signed an hour outside the window was accepted")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "clock") {
		t.Errorf("the error does not name the clocks: %v", err)
	}
}

// TestB475_ListRecordsIsANamedRefusal — RFC 2136 cannot enumerate a zone, and the message must say what to
// do instead.
func TestB475_ListRecordsIsANamedRefusal(t *testing.T) {
	zone := newFakeDNSZone(true)
	f := newFakeDNS(t, zone)
	p := rfc2136TestProvider(t, f)
	recs, err := p.ListRecords(context.Background(), "example.com")
	if err == nil {
		t.Fatal("ListRecords answered success for a dynamic-update server")
	}
	if recs != nil {
		t.Error("ListRecords returned records together with an error")
	}
	for _, want := range []string{"AXFR", "Test connection"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
}

// TestB475_CredentialValidation — the four fields are validated where they are READ, with messages that
// name the field.
func TestB475_CredentialValidation(t *testing.T) {
	cases := []struct {
		name string
		row  map[string]string
		must string
	}{
		{"no server", map[string]string{"key_name": "k", "key_secret": base64.StdEncoding.EncodeToString([]byte("s"))}, "server address is required"},
		{"no key name", map[string]string{"server": "127.0.0.1:53", "key_secret": base64.StdEncoding.EncodeToString([]byte("s"))}, "TSIG key name is required"},
		{"bad base64", map[string]string{"server": "127.0.0.1:53", "key_name": "k", "key_secret": "not base64!!"}, "not valid base64"},
		{"bad algo", map[string]string{"server": "127.0.0.1:53", "key_name": "k", "key_secret": base64.StdEncoding.EncodeToString([]byte("s")), "key_algo": "hmac-md5"}, "unsupported TSIG algorithm"},
	}
	for _, tc := range cases {
		row := extRow("rfc2136", "example.com", tc.row)
		_, err := BuildProvider("rfc2136", BuildDeps{Row: &row})
		if err == nil {
			t.Errorf("%s: BuildProvider accepted the row", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.must) {
			t.Errorf("%s: error = %v, want it to contain %q", tc.name, err, tc.must)
		}
	}
	// A server without a port gets the DNS default, and the provider says so in its address.
	row := extRow("rfc2136", "example.com", map[string]string{
		"server": "ns1.example.com", "key_name": "skygate-key",
		"key_secret": base64.StdEncoding.EncodeToString([]byte("secret")),
	})
	p, err := BuildProvider("rfc2136", BuildDeps{Row: &row})
	if err != nil {
		t.Fatalf("BuildProvider: %v", err)
	}
	if got := p.(*rfc2136Provider).server; got != "ns1.example.com:53" {
		t.Errorf("server = %q, want the default port appended", got)
	}
	// And without a zone the credential test says which field is missing.
	row2 := extRow("rfc2136", "", map[string]string{
		"server": "ns1.example.com", "key_name": "skygate-key",
		"key_secret": base64.StdEncoding.EncodeToString([]byte("secret")),
	})
	p2, err := BuildProvider("rfc2136", BuildDeps{Row: &row2})
	if err != nil {
		t.Fatalf("BuildProvider: %v", err)
	}
	if err := p2.TestConnection(context.Background()); err == nil || !strings.Contains(err.Error(), "zone") {
		t.Errorf("TestConnection without a zone = %v, want it to name the zone", err)
	}
}

// TestB475_CapabilitiesAreTheAdaptersTruth — the registry entry and the adapter agree, and the two
// capabilities this adapter genuinely lacks nothing of (A/AAAA/TXT/TTL) are all true.
func TestB475_CapabilitiesAreTheAdaptersTruth(t *testing.T) {
	zone := newFakeDNSZone(true)
	f := newFakeDNS(t, zone)
	p := rfc2136TestProvider(t, f)
	spec, ok := LookupSpec("rfc2136")
	if !ok {
		t.Fatal("rfc2136 is not in the provider registry")
	}
	if p.Capabilities() != spec.Caps {
		t.Errorf("adapter %+v vs registry %+v", p.Capabilities(), spec.Caps)
	}
	if want := (Capabilities{A: true, AAAA: true, TXT: true, TTL: true}); p.Capabilities() != want {
		t.Errorf("capabilities = %+v, want %+v", p.Capabilities(), want)
	}
	if len(spec.Fields) != 4 {
		t.Errorf("the rfc2136 spec has %d fields; a TSIG credential has four parts", len(spec.Fields))
	}
}
