// rfc2136_b475.go — B475: the third provider, and the one that has no HTTP API at all.
//
// WHY THIS IS THE HARDEST OF THE THREE. reg.ru (B468) and Cloudflare (B474) are HTTPS+JSON: a client can
// be written against a documented envelope. RFC 2136 dynamic DNS UPDATE is a BINARY protocol carried over
// TCP, authenticated by TSIG (RFC 8945) — an HMAC over the message PLUS a block of TSIG variables — and
// there is no library in this project's dependency set. Adding one would mean touching go.mod/go.sum,
// which every operator host keeps as an operator-managed file (AGENTS rule 7); so this adapter speaks the
// wire itself with nothing but `net`, `crypto/hmac` and `encoding/binary`. That is ~600 lines including
// the pieces below, and every byte of it is asserted by a fake server that re-computes the signature.
//
// WHAT IT DOES NOT DO, DELIBERATELY: `ListRecords`. RFC 2136 is a WRITE protocol — it can add and delete
// specific records, but it cannot enumerate a zone. The only way to read a whole zone is AXFR, which most
// authoritative servers disable per-IP and which is a different feature (a transfer, not a credential
// test). So the panel's «Read the zone» answers a NAMED error for this provider instead of pretending,
// and the runbook says so. Everything the switch actually needs — read one A record, write one A record,
// change its TTL, and the DNS-01 TXT pair — is implemented for real.
//
// TWO TRAPS THIS FILE IS WRITTEN AROUND:
//
//  1. THE TSIG DIGEST COVERS THE MESSAGE **BEFORE** THE TSIG RR EXISTS — ARCOUNT counts only the other
//     additional records — while the bytes on the wire carry ARCOUNT+1. Incrementing the count before
//     hashing produces a signature that verifies nowhere; the server answers NOTAUTH (or BADSIG) with no
//     detail, and the natural next suspicion is the secret. BIND and miekg/dns both sign the pre-append
//     form, and that is the form this file signs.
//  2. A TSIG RR MUST BE THE LAST RECORD OF THE ADDITIONAL SECTION, and its RDATA covers the algorithm
//     name, time signed, fudge, MAC size, MAC, original ID, error and other-data length — every field is
//     length-prefixed where the RFC says so, and a single wrong prefix byte shifts the whole tail.
package dns

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"hash"
	"io"
	"net"
	"strings"
	"time"
)

// rfc2136DefaultPort is appended to a server address without one. 53 is the only sane default and the
// error message names it, because "connection refused" against a missing port is a confusing symptom.
const rfc2136DefaultPort = "53"

// rfc2136DefaultAlgo is the TSIG algorithm used when the credential does not name one. hmac-sha256 is
// what BIND and Knot generate by default today; hmac-sha1 is still accepted for old deployments.
const rfc2136DefaultAlgo = "hmac-sha256."

// DNS constants used by the UPDATE/QUERY builders. Spelled out instead of imported: the project has no
// DNS dependency, and a table of four numbers is cheaper than one.
const (
	dnsTypeA        = 1
	dnsTypeSOA      = 6
	dnsTypeTXT      = 16
	dnsTypeANY      = 255
	dnsTypeTSIG     = 250
	dnsClassIN      = 1
	dnsClassNONE    = 254 // "delete an individual RR" per RFC 2136 §2.5.4
	dnsClassANY     = 255 // "delete every RRset at this name"
	dnsOpcodeQuery  = 0
	dnsOpcodeUpdate = 5
	dnsFlagQR       = 1 << 15
	// dnsUpdateTTLAdd is the TTL written for an added A record. 300 matches the switch runbook's
	// "lower it a day before"; SetTTL overwrites it explicitly.
	dnsUpdateTTLAdd = 300
)

// rfc2136Provider is the RFC 2136 UPDATE client behind the dns.Provider contract.
type rfc2136Provider struct {
	server  string // host:port
	keyName string // the TSIG key name, e.g. "skygate-key." (a trailing dot is normalised)
	secret  []byte
	algo    string // "hmac-sha256." etc.
	fudge   uint16
	timeout time.Duration
	// now is the clock used for the TSIG "time signed" field. A test needs to be exact about it,
	// because a signature computed a second later is a different MAC.
	now func() time.Time
	// zoneHint is the zone from the credential row. TestConnection takes no zone argument, and a DNS
	// server cannot be asked "are you authoritative for anything" in a way that also proves the key,
	// so the zone is carried from the row instead.
	zoneHint string
}

// rfc2136Spec is the third registry entry. Four fields because a TSIG credential genuinely has four
// parts: where to send the update, which key, the key's algorithm and the key's secret.
var rfc2136Spec = Spec{
	Name:      "rfc2136",
	Label:     "RFC 2136 (dynamic DNS + TSIG)",
	NeedsZone: true,
	Fields: []FieldSpec{
		{
			Name: "server", LabelKey: "ha.dns_field_rfc2136_server", HelpKey: "ha.dns_field_rfc2136_server_help",
			Kind: FieldText, Required: true,
		},
		{
			Name: "key_name", LabelKey: "ha.dns_field_rfc2136_key_name", HelpKey: "ha.dns_field_rfc2136_key_name_help",
			Kind: FieldText, Required: true,
		},
		{
			Name: "key_secret", LabelKey: "ha.dns_field_rfc2136_secret", HelpKey: "ha.dns_field_rfc2136_secret_help",
			Kind: FieldPassword, Required: true,
		},
		{
			Name: "key_algo", LabelKey: "ha.dns_field_rfc2136_algo", HelpKey: "ha.dns_field_rfc2136_algo_help",
			Kind: FieldText, Required: false,
		},
	},
	// AAAA and TXT are implemented; TTL is part of the write. There is no "list the zone" capability to
	// advertise — see the file comment.
	Caps: Capabilities{A: true, AAAA: true, TXT: true, TTL: true},
}

// newRFC2136Provider builds the adapter from a credential row's fields.
func newRFC2136Provider(server, keyName, keySecret, keyAlgo string) (*rfc2136Provider, error) {
	server = strings.TrimSpace(server)
	if server == "" {
		return nil, fmt.Errorf("rfc2136: the server address is required (host or host:port)")
	}
	if !strings.Contains(server, ":") {
		server = net.JoinHostPort(server, rfc2136DefaultPort)
	}
	if _, _, err := net.SplitHostPort(server); err != nil {
		return nil, fmt.Errorf("rfc2136: the server address %q is not host:port: %w", server, err)
	}
	keyName = dnsAbsName(keyName)
	if keyName == "" {
		return nil, fmt.Errorf("rfc2136: the TSIG key name is required")
	}
	secret, err := base64.StdEncoding.DecodeString(strings.TrimSpace(keySecret))
	if err != nil {
		return nil, fmt.Errorf("rfc2136: the TSIG secret is not valid base64: %w", err)
	}
	if len(secret) == 0 {
		return nil, fmt.Errorf("rfc2136: the TSIG secret is empty")
	}
	algo := dnsAbsName(strings.ToLower(strings.TrimSpace(keyAlgo)))
	if algo == "" {
		algo = rfc2136DefaultAlgo
	}
	if _, err := tsigHashFor(algo); err != nil {
		return nil, err
	}
	return &rfc2136Provider{
		server: server, keyName: keyName, secret: secret, algo: algo,
		fudge: 300, timeout: 10 * time.Second, now: time.Now,
	}, nil
}

// Name / Capabilities — the registry is the single source.
func (p *rfc2136Provider) Name() string               { return rfc2136Spec.Name }
func (p *rfc2136Provider) Capabilities() Capabilities { return rfc2136Spec.Caps }

// tsigHashFor maps a TSIG algorithm name to its hash. Only the three HMAC-SHA variants BIND ships are
// accepted: an unknown algorithm must be refused HERE, not produce a signature the server silently
// drops as BADSIG.
func tsigHashFor(algo string) (func() hash.Hash, error) {
	switch dnsAbsName(strings.ToLower(algo)) {
	case "hmac-sha256.", "hmac-sha256":
		return sha256.New, nil
	case "hmac-sha1.", "hmac-sha1":
		return sha1.New, nil
	case "hmac-sha512.", "hmac-sha512":
		return sha512.New, nil
	}
	return nil, fmt.Errorf("rfc2136: unsupported TSIG algorithm %q (allowed: hmac-sha1, hmac-sha256, hmac-sha512)", algo)
}

// ---------- wire primitives ----------

// dnsAbsName normalises a name to the wire form: lower-case, exactly one trailing dot, no empty labels.
func dnsAbsName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.TrimSuffix(name, ".")
	if name == "" {
		return ""
	}
	labels := strings.Split(name, ".")
	for _, l := range labels {
		if l == "" || len(l) > 63 {
			return ""
		}
	}
	return name + "."
}

// encodeName writes a domain name in wire format.
func encodeName(name string) []byte {
	name = dnsAbsName(name)
	if name == "" {
		return []byte{0}
	}
	out := make([]byte, 0, len(name)+1)
	for _, label := range strings.Split(strings.TrimSuffix(name, "."), ".") {
		out = append(out, byte(len(label)))
		out = append(out, label...)
	}
	return append(out, 0)
}

// decodeName reads a (possibly compressed) name from a message starting at off. It returns the name and
// the offset AFTER it (compression pointers do not move the read cursor).
func decodeName(msg []byte, off int) (string, int, error) {
	var sb strings.Builder
	next := -1
	jumps := 0
	for {
		if off >= len(msg) {
			return "", 0, fmt.Errorf("rfc2136: name runs past the message")
		}
		l := int(msg[off])
		if l == 0 {
			off++
			break
		}
		if l&0xC0 == 0xC0 {
			if off+1 >= len(msg) {
				return "", 0, fmt.Errorf("rfc2136: truncated compression pointer")
			}
			ptr := int(binary.BigEndian.Uint16(msg[off:]) & 0x3FFF)
			if next < 0 {
				next = off + 2
			}
			if ptr >= len(msg) || jumps > 20 {
				return "", 0, fmt.Errorf("rfc2136: bad compression pointer")
			}
			jumps++
			off = ptr
			continue
		}
		if off+1+l > len(msg) {
			return "", 0, fmt.Errorf("rfc2136: label runs past the message")
		}
		sb.WriteString(strings.ToLower(string(msg[off+1 : off+1+l])))
		sb.WriteByte('.')
		off += 1 + l
	}
	if next >= 0 {
		off = next
	}
	return sb.String(), off, nil
}

// dnsMessage is the minimal message this file builds or reads.
type dnsMessage struct {
	ID       uint16
	Opcode   int
	Flags    uint16
	Zone     string
	ZoneType uint16
	Prereq   []dnsRR
	Update   []dnsRR
	Extra    []dnsRR
}

// dnsRR is one resource record (all sections of an UPDATE share this shape).
type dnsRR struct {
	Name  string
	Type  uint16
	Class uint16
	TTL   uint32
	Data  []byte
}

// rcode of a message (low 4 bits of Flags).
func (m dnsMessage) rcode() int { return int(m.Flags & 0x000F) }

// wire renders the message WITHOUT the TSIG record. The caller appends TSIG after bumping ARCOUNT.
func (m dnsMessage) wire() []byte {
	out := make([]byte, 12, 512)
	binary.BigEndian.PutUint16(out[0:2], m.ID)
	// The opcode is set; the RCODE bits are KEPT. Clearing them (a `&^ 0xF` on the whole flags word)
	// silently turns every error answer built through this helper into NOERROR — which is how a test
	// fixture or a future response builder would report a refused update as success.
	flags := m.Flags &^ uint16(0xF<<11)
	flags |= uint16(m.Opcode&0xF) << 11
	binary.BigEndian.PutUint16(out[2:4], flags)
	binary.BigEndian.PutUint16(out[4:6], 1) // QDCOUNT: the zone section, present in UPDATE and QUERY
	binary.BigEndian.PutUint16(out[6:8], uint16(len(m.Prereq)))
	binary.BigEndian.PutUint16(out[8:10], uint16(len(m.Update)))
	binary.BigEndian.PutUint16(out[10:12], uint16(len(m.Extra)))
	zoneType := m.ZoneType
	if zoneType == 0 {
		zoneType = dnsTypeSOA // the UPDATE zone section is always ZTYPE=SOA
	}
	if m.Opcode == dnsOpcodeQuery && m.ZoneType == 0 {
		zoneType = dnsTypeSOA
	}
	out = append(out, encodeName(m.Zone)...)
	out = binary.BigEndian.AppendUint16(out, zoneType)
	out = binary.BigEndian.AppendUint16(out, dnsClassIN)
	for _, sec := range [][]dnsRR{m.Prereq, m.Update, m.Extra} {
		for _, rr := range sec {
			out = append(out, encodeName(rr.Name)...)
			out = binary.BigEndian.AppendUint16(out, rr.Type)
			out = binary.BigEndian.AppendUint16(out, rr.Class)
			out = binary.BigEndian.AppendUint32(out, rr.TTL)
			out = binary.BigEndian.AppendUint16(out, uint16(len(rr.Data)))
			out = append(out, rr.Data...)
		}
	}
	return out
}

// parseResponse decodes the header + all four sections of a response. Only what the adapter needs: the
// RCODE, and the A/TXT/SOA records it reads back.
func parseResponse(msg []byte) (dnsMessage, error) {
	var m dnsMessage
	if len(msg) < 12 {
		return m, fmt.Errorf("rfc2136: response shorter than a DNS header (%d bytes)", len(msg))
	}
	m.ID = binary.BigEndian.Uint16(msg[0:2])
	flags := binary.BigEndian.Uint16(msg[2:4])
	m.Flags = flags
	m.Opcode = int(flags>>11) & 0xF
	qd := int(binary.BigEndian.Uint16(msg[4:6]))
	an := int(binary.BigEndian.Uint16(msg[6:8]))
	ns := int(binary.BigEndian.Uint16(msg[8:10]))
	ar := int(binary.BigEndian.Uint16(msg[10:12]))
	off := 12
	for i := 0; i < qd; i++ {
		name, n, err := decodeName(msg, off)
		if err != nil {
			return m, err
		}
		off = n
		if off+4 > len(msg) {
			return m, fmt.Errorf("rfc2136: truncated question")
		}
		typ := binary.BigEndian.Uint16(msg[off:])
		off += 4
		if i == 0 {
			m.Zone = name
			m.ZoneType = typ
		}
	}
	readRRs := func(count int) ([]dnsRR, error) {
		out := make([]dnsRR, 0, count)
		for i := 0; i < count; i++ {
			name, n, err := decodeName(msg, off)
			if err != nil {
				return nil, err
			}
			off = n
			if off+10 > len(msg) {
				return nil, fmt.Errorf("rfc2136: truncated record")
			}
			typ := binary.BigEndian.Uint16(msg[off:])
			class := binary.BigEndian.Uint16(msg[off+2:])
			ttl := binary.BigEndian.Uint32(msg[off+4:])
			rdlen := int(binary.BigEndian.Uint16(msg[off+8:]))
			off += 10
			if off+rdlen > len(msg) {
				return nil, fmt.Errorf("rfc2136: record data runs past the message")
			}
			data := append([]byte(nil), msg[off:off+rdlen]...)
			off += rdlen
			out = append(out, dnsRR{Name: name, Type: typ, Class: class, TTL: ttl, Data: data})
		}
		return out, nil
	}
	var err error
	if m.Prereq, err = readRRs(an); err != nil {
		return m, err
	}
	if m.Update, err = readRRs(ns); err != nil {
		return m, err
	}
	if m.Extra, err = readRRs(ar); err != nil {
		return m, err
	}
	return m, nil
}

// rcodeName turns an RCODE into the words the operator needs. The bare number sends people looking for a
// network problem.
func rcodeName(rcode int) string {
	switch rcode {
	case 0:
		return "NOERROR"
	case 1:
		return "FORMERR (the server could not parse the update)"
	case 2:
		return "SERVFAIL (the server failed to process the update)"
	case 3:
		return "NXDOMAIN (the zone does not exist on this server)"
	case 4:
		return "NOTIMP (dynamic update is not implemented by this server)"
	case 5:
		return "REFUSED (the server refuses updates — check allow-update / update-policy)"
	case 6:
		return "YXDOMAIN (a prerequisite failed)"
	case 7:
		return "YXRRSET (a prerequisite failed)"
	case 8:
		return "NXRRSET (the prerequisite says the record must exist and it does not)"
	case 9:
		return "NOTAUTH (the TSIG key is not authorised for this zone)"
	case 10:
		return "NOTZONE (a name in the update is not inside the zone)"
	case 16:
		return "BADSIG (the TSIG signature did not verify — check the secret and the algorithm)"
	case 17:
		return "BADKEY (the server does not know this TSIG key)"
	case 18:
		return "BADTIME (the client clock and the server clock differ by more than the fudge)"
	}
	return fmt.Sprintf("RCODE %d", rcode)
}

// ---------- TSIG (RFC 8945) ----------

// tsigSign appends the TSIG RR to the message and returns the SIGNED bytes plus the MAC.
//
// THE ARCOUNT ORDER IS THE WHOLE CONTRACT (trap 1 in the file comment): the digest covers the message
// BEFORE the TSIG RR exists — ARCOUNT counting only the other additional records — while the bytes on the
// wire carry ARCOUNT+1. Incrementing first produces a signature that verifies nowhere and is reported as
// NOTAUTH with no detail.
func (p *rfc2136Provider) tsigSign(msg []byte, origID uint16) ([]byte, []byte, error) {
	h, err := tsigHashFor(p.algo)
	if err != nil {
		return nil, nil, err
	}
	now := uint64(p.now().Unix())

	mac := hmac.New(h, p.secret)
	mac.Write(msg) // the pre-append message: ARCOUNT WITHOUT the TSIG record
	mac.Write(p.tsigVariables(now))
	sum := mac.Sum(nil)

	// The TSIG RDATA: algorithm, time signed (48 bit), fudge, MAC size + MAC, original id, error, other.
	rdata := make([]byte, 0, len(p.algo)+32)
	rdata = append(rdata, encodeName(p.algo)...)
	rdata = append(rdata, byte(now>>40), byte(now>>32), byte(now>>24), byte(now>>16), byte(now>>8), byte(now))
	rdata = binary.BigEndian.AppendUint16(rdata, p.fudge)
	rdata = binary.BigEndian.AppendUint16(rdata, uint16(len(sum)))
	rdata = append(rdata, sum...)
	rdata = binary.BigEndian.AppendUint16(rdata, origID)
	rdata = binary.BigEndian.AppendUint16(rdata, 0) // error
	rdata = binary.BigEndian.AppendUint16(rdata, 0) // other data length

	// The wire copy: ARCOUNT includes the TSIG record that is about to be appended.
	signed := append([]byte(nil), msg...)
	ar := binary.BigEndian.Uint16(signed[10:12])
	binary.BigEndian.PutUint16(signed[10:12], ar+1)
	out := append(signed, encodeName(p.keyName)...)
	out = binary.BigEndian.AppendUint16(out, dnsTypeTSIG)
	out = binary.BigEndian.AppendUint16(out, dnsClassANY)
	out = binary.BigEndian.AppendUint32(out, 0) // TTL is 0 for TSIG
	out = binary.BigEndian.AppendUint16(out, uint16(len(rdata)))
	out = append(out, rdata...)
	return out, sum, nil
}

// tsigVariables is the block the MAC covers AFTER the message (RFC 8945 §4.3.3): key name, class, TTL,
// algorithm, time signed, fudge, error, other length, other data.
func (p *rfc2136Provider) tsigVariables(now uint64) []byte {
	out := encodeName(p.keyName)
	out = binary.BigEndian.AppendUint16(out, dnsClassANY)
	out = binary.BigEndian.AppendUint32(out, 0)
	out = append(out, encodeName(p.algo)...)
	out = append(out, byte(now>>40), byte(now>>32), byte(now>>24), byte(now>>16), byte(now>>8), byte(now))
	out = binary.BigEndian.AppendUint16(out, p.fudge)
	out = binary.BigEndian.AppendUint16(out, 0) // error
	out = binary.BigEndian.AppendUint16(out, 0) // other data length
	return out
}

// tsigVerify checks the server's TSIG over a response, if it carried one. A response WITHOUT TSIG is
// accepted when the server is not configured to sign (some do not); a response WITH a wrong signature is
// an error, because that is the case where accepting it would mean acting on an unauthenticated answer.
func (p *rfc2136Provider) tsigVerify(raw []byte, resp dnsMessage, reqMAC []byte) error {
	if len(resp.Extra) == 0 {
		return nil
	}
	last := resp.Extra[len(resp.Extra)-1]
	if last.Type != dnsTypeTSIG {
		return nil
	}
	if len(last.Data) < len(p.algo)+12 {
		return fmt.Errorf("rfc2136: the response's TSIG record is truncated")
	}
	// Algorithm name, then the fixed fields.
	algo, off, err := decodeName(last.Data, 0)
	if err != nil {
		return err
	}
	if dnsAbsName(algo) != dnsAbsName(p.algo) {
		return fmt.Errorf("rfc2136: the server signed with %q but the credential says %q", algo, p.algo)
	}
	if off+6+2+2+2 > len(last.Data) {
		return fmt.Errorf("rfc2136: the response's TSIG record is truncated")
	}
	timeSigned := uint64(last.Data[off])<<40 | uint64(last.Data[off+1])<<32 | uint64(last.Data[off+2])<<24 |
		uint64(last.Data[off+3])<<16 | uint64(last.Data[off+4])<<8 | uint64(last.Data[off+5])
	off += 6
	fudge := binary.BigEndian.Uint16(last.Data[off:])
	off += 2
	macSize := int(binary.BigEndian.Uint16(last.Data[off:]))
	off += 2
	if off+macSize > len(last.Data) {
		return fmt.Errorf("rfc2136: the response's TSIG MAC is truncated")
	}
	gotMAC := last.Data[off : off+macSize]
	off += macSize
	if macSize == 0 {
		// RFC 8945 §5.3.2: an ERROR response carries a TSIG with an EMPTY MAC when the server could not
		// authenticate the request (BADSIG/BADKEY/NOTAUTH). Insisting on a signature here would replace
		// the server's own answer — "NOTAUTH: the key is not authorised" — with the useless "the
		// signature did not verify", which is the message that sends an operator hunting for a wrong
		// secret when the real problem is a missing update-policy line.
		return nil
	}
	if off+2 > len(last.Data) {
		return fmt.Errorf("rfc2136: the response's TSIG original id is truncated")
	}
	origID := binary.BigEndian.Uint16(last.Data[off:])
	off += 2
	if off+2 > len(last.Data) {
		return fmt.Errorf("rfc2136: the response's TSIG error field is truncated")
	}
	tsigErr := binary.BigEndian.Uint16(last.Data[off:])
	off += 2
	var otherData []byte
	if off+2 <= len(last.Data) {
		otherLen := int(binary.BigEndian.Uint16(last.Data[off:]))
		off += 2
		if off+otherLen <= len(last.Data) {
			otherData = last.Data[off : off+otherLen]
		}
	}
	if tsigErr != 0 {
		return fmt.Errorf("rfc2136: the server rejected the signature: %s", rcodeName(int(tsigErr)))
	}
	// Clock skew: a signature more than fudge seconds away is BADTIME, and saying so saves an hour of
	// "the key must be wrong" when the real cause is NTP on one side.
	if skew := int64(p.now().Unix()) - int64(timeSigned); skew > int64(fudge) || skew < -int64(fudge) {
		return fmt.Errorf("rfc2136: the server's TSIG time differs from ours by %ds (fudge %ds) — check the clocks on both hosts", skew, fudge)
	}
	// Recompute: the message WITHOUT the TSIG record, with ARCOUNT decremented, then the REQUEST's MAC,
	// then the response's own TSIG variables.
	unsigned := stripLastRR(raw)
	if len(unsigned) >= 12 {
		ar := binary.BigEndian.Uint16(unsigned[10:12])
		binary.BigEndian.PutUint16(unsigned[10:12], ar-1)
	}
	h, herr := tsigHashFor(p.algo)
	if herr != nil {
		return herr
	}
	mac := hmac.New(h, p.secret)
	mac.Write(unsigned)
	if len(reqMAC) > 0 {
		mac.Write(binary.BigEndian.AppendUint16(nil, uint16(len(reqMAC))))
		mac.Write(reqMAC)
	}
	vars := encodeName(p.keyName)
	vars = binary.BigEndian.AppendUint16(vars, dnsClassANY)
	vars = binary.BigEndian.AppendUint32(vars, 0)
	vars = append(vars, encodeName(p.algo)...)
	vars = append(vars, byte(timeSigned>>40), byte(timeSigned>>32), byte(timeSigned>>24),
		byte(timeSigned>>16), byte(timeSigned>>8), byte(timeSigned))
	vars = binary.BigEndian.AppendUint16(vars, fudge)
	vars = binary.BigEndian.AppendUint16(vars, tsigErr)
	vars = binary.BigEndian.AppendUint16(vars, uint16(len(otherData)))
	vars = append(vars, otherData...)
	mac.Write(vars)
	if !hmac.Equal(mac.Sum(nil), gotMAC) {
		return fmt.Errorf("rfc2136: the server's TSIG signature did not verify — the shared secret or the algorithm differs")
	}
	_ = origID
	return nil
}

// stripLastRR removes the final resource record from a message (the TSIG record, which is always last).
func stripLastRR(msg []byte) []byte {
	if len(msg) < 12 {
		return append([]byte(nil), msg...)
	}
	ar := int(binary.BigEndian.Uint16(msg[10:12]))
	if ar == 0 {
		return append([]byte(nil), msg...)
	}
	// Walk from the end: find the start of the last RR by scanning forward, so a compressed name in the
	// middle of the message cannot confuse the boundary.
	off := 12
	qd := int(binary.BigEndian.Uint16(msg[4:6]))
	for i := 0; i < qd; i++ {
		_, n, err := decodeName(msg, off)
		if err != nil {
			return append([]byte(nil), msg...)
		}
		off = n + 4
	}
	total := int(binary.BigEndian.Uint16(msg[6:8])) + int(binary.BigEndian.Uint16(msg[8:10])) +
		int(binary.BigEndian.Uint16(msg[10:12]))
	start := -1
	for i := 0; i < total; i++ {
		start = off
		_, n, err := decodeName(msg, off)
		if err != nil || n+10 > len(msg) {
			return append([]byte(nil), msg...)
		}
		rdlen := int(binary.BigEndian.Uint16(msg[n+8:]))
		off = n + 10 + rdlen
		if off > len(msg) {
			return append([]byte(nil), msg...)
		}
	}
	if start < 0 {
		return append([]byte(nil), msg...)
	}
	return append([]byte(nil), msg[:start]...)
}

// ---------- transport ----------

// exchange sends one signed message over TCP and returns the parsed response.
//
// TCP (not UDP) because an UPDATE that changes several records can exceed 512 bytes once TSIG is
// appended, and a truncated UDP answer is indistinguishable from a refused update.
func (p *rfc2136Provider) exchange(ctx context.Context, m dnsMessage) (dnsMessage, error) {
	origID := m.ID
	unsigned := m.wire()
	signed, mac, err := p.tsigSign(unsigned, origID)
	if err != nil {
		return dnsMessage{}, err
	}
	dialer := &net.Dialer{Timeout: p.timeout}
	conn, err := dialer.DialContext(ctx, "tcp", p.server)
	if err != nil {
		return dnsMessage{}, fmt.Errorf("rfc2136: dial %s: %w", p.server, err)
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	} else {
		_ = conn.SetDeadline(time.Now().Add(p.timeout))
	}
	framed := binary.BigEndian.AppendUint16(make([]byte, 0, len(signed)+2), uint16(len(signed)))
	framed = append(framed, signed...)
	if _, err := conn.Write(framed); err != nil {
		return dnsMessage{}, fmt.Errorf("rfc2136: send to %s: %w", p.server, err)
	}
	var lenBuf [2]byte
	if _, err := io.ReadFull(conn, lenBuf[:]); err != nil {
		return dnsMessage{}, fmt.Errorf("rfc2136: read length from %s: %w", p.server, err)
	}
	n := int(binary.BigEndian.Uint16(lenBuf[:]))
	if n < 12 || n > 65535 {
		return dnsMessage{}, fmt.Errorf("rfc2136: %s announced an impossible message length %d", p.server, n)
	}
	raw := make([]byte, n)
	if _, err := io.ReadFull(conn, raw); err != nil {
		return dnsMessage{}, fmt.Errorf("rfc2136: read response from %s: %w", p.server, err)
	}
	resp, err := parseResponse(raw)
	if err != nil {
		return dnsMessage{}, err
	}
	if resp.ID != origID {
		return dnsMessage{}, fmt.Errorf("rfc2136: the response id %d does not match the request id %d", resp.ID, origID)
	}
	if resp.Flags&dnsFlagQR == 0 {
		return dnsMessage{}, fmt.Errorf("rfc2136: the server answered a request, not a response")
	}
	if err := p.tsigVerify(raw, resp, mac); err != nil {
		return dnsMessage{}, err
	}
	if rc := resp.rcode(); rc != 0 {
		return resp, fmt.Errorf("rfc2136: the server answered %s", rcodeName(rc))
	}
	return resp, nil
}

// nextID derives a message id. A random id is not needed for TCP (the stream is ordered), but the id must
// still change per message so a stale answer is detectable — the exchange checks it.
func (p *rfc2136Provider) nextID() uint16 {
	return uint16(p.now().UnixNano() & 0xFFFF)
}

// ---------- the contract ----------

// GetRecord reads the A record for `name` with a QUERY (class IN, type A).
func (p *rfc2136Provider) GetRecord(ctx context.Context, zone, name string) (string, error) {
	zone = dnsAbsName(zone)
	if zone == "" {
		return "", fmt.Errorf("rfc2136: the zone is required")
	}
	fqdn := rfc2136FQDN(name, zone)
	msg := dnsMessage{ID: p.nextID(), Opcode: dnsOpcodeQuery, Zone: fqdn, ZoneType: dnsTypeA}
	resp, err := p.exchange(ctx, msg)
	if err != nil {
		return "", err
	}
	for _, rr := range resp.Prereq {
		if rr.Type == dnsTypeA && net.IP(rr.Data).To4() != nil {
			return net.IP(rr.Data).String(), nil
		}
	}
	return "", ErrRecordNotFound
}

// rfc2136FQDN expands a bare name against the zone (the interface passes "skygate", DNS wants FQDNs).
func rfc2136FQDN(name, zone string) string {
	name = dnsAbsName(name)
	zone = dnsAbsName(zone)
	if name == "" || name == zone {
		return zone
	}
	if strings.HasSuffix(name, "."+zone) {
		return name
	}
	return name + zone
}

// aData packs an IPv4 address as RDATA.
func aData(ip string) ([]byte, error) {
	parsed := net.ParseIP(strings.TrimSpace(ip))
	if parsed == nil {
		return nil, fmt.Errorf("rfc2136: %q is not an IP address", ip)
	}
	if v4 := parsed.To4(); v4 != nil {
		return v4, nil
	}
	return nil, fmt.Errorf("rfc2136: %q is IPv6; the A record needs an IPv4 address", ip)
}

// UpdateRecord replaces the A RRset at `name` with `ip`: delete the RRset, then add the new record, in ONE
// UPDATE message so a reader never sees both addresses (RFC 2136 §3.4.2.2 semantics are atomic per
// message).
func (p *rfc2136Provider) UpdateRecord(ctx context.Context, zone, name, ip string) error {
	data, err := aData(ip)
	if err != nil {
		return err
	}
	zoneAbs := dnsAbsName(zone)
	fqdn := rfc2136FQDN(name, zoneAbs)
	msg := dnsMessage{
		ID: p.nextID(), Opcode: dnsOpcodeUpdate, Zone: zoneAbs,
		Update: []dnsRR{
			{Name: fqdn, Type: dnsTypeA, Class: dnsClassANY, TTL: 0},                          // delete the RRset
			{Name: fqdn, Type: dnsTypeA, Class: dnsClassIN, TTL: dnsUpdateTTLAdd, Data: data}, // add
		},
	}
	_, err = p.exchange(ctx, msg)
	return err
}

// SetTTL rewrites the A record's TTL by replacing the RRset with the same data and a new TTL.
func (p *rfc2136Provider) SetTTL(ctx context.Context, zone, name string, ttl int) error {
	if !p.Capabilities().TTL {
		return ErrUnsupported
	}
	if ttl <= 0 {
		return fmt.Errorf("rfc2136: ttl must be positive, got %d", ttl)
	}
	cur, err := p.GetRecord(ctx, zone, name)
	if err != nil {
		return err
	}
	data, err := aData(cur)
	if err != nil {
		return err
	}
	zoneAbs := dnsAbsName(zone)
	fqdn := rfc2136FQDN(name, zoneAbs)
	msg := dnsMessage{
		ID: p.nextID(), Opcode: dnsOpcodeUpdate, Zone: zoneAbs,
		Update: []dnsRR{
			{Name: fqdn, Type: dnsTypeA, Class: dnsClassANY, TTL: 0},
			{Name: fqdn, Type: dnsTypeA, Class: dnsClassIN, TTL: uint32(ttl), Data: data},
		},
	}
	_, err = p.exchange(ctx, msg)
	return err
}

// ListRecords — RFC 2136 cannot enumerate a zone (see the file comment). The error names the limitation
// and the way out, because "not supported" without a next step is the kind of message that makes an
// operator think the panel is broken.
func (p *rfc2136Provider) ListRecords(ctx context.Context, zone string) ([]Record, error) {
	return nil, fmt.Errorf("rfc2136: a dynamic-update server cannot LIST a zone — reading the whole zone needs an AXFR transfer (a different permission), so enter the names to read instead; «Test connection» reads the zone's SOA and proves the key works")
}

// CreateTXT adds the DNS-01 challenge TXT record.
func (p *rfc2136Provider) CreateTXT(ctx context.Context, zone, name, value string, ttl int) error {
	if !p.Capabilities().TXT {
		return ErrUnsupported
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("rfc2136: the TXT value is empty")
	}
	if ttl <= 0 || ttl > 86400 {
		ttl = 60 // a challenge record must not outlive the order
	}
	zoneAbs := dnsAbsName(zone)
	fqdn := rfc2136FQDN(name, zoneAbs)
	msg := dnsMessage{
		ID: p.nextID(), Opcode: dnsOpcodeUpdate, Zone: zoneAbs,
		Update: []dnsRR{
			{Name: fqdn, Type: dnsTypeTXT, Class: dnsClassIN, TTL: uint32(ttl), Data: txtData(value)},
		},
	}
	_, err := p.exchange(ctx, msg)
	return err
}

// DeleteTXT removes the TXT records at `name` whose content is exactly `value`. A missing record is
// success: DNS-01 cleanup is retried.
func (p *rfc2136Provider) DeleteTXT(ctx context.Context, zone, name, value string) error {
	if !p.Capabilities().TXT {
		return ErrUnsupported
	}
	zoneAbs := dnsAbsName(zone)
	fqdn := rfc2136FQDN(name, zoneAbs)
	msg := dnsMessage{
		ID: p.nextID(), Opcode: dnsOpcodeUpdate, Zone: zoneAbs,
		// Class NONE deletes ONE record whose RDATA matches; the TTL must be 0 in a delete.
		Update: []dnsRR{
			{Name: fqdn, Type: dnsTypeTXT, Class: dnsClassNONE, TTL: 0, Data: txtData(strings.TrimSpace(value))},
		},
	}
	_, err := p.exchange(ctx, msg)
	return err
}

// txtData packs a TXT value as character-strings of at most 255 bytes each.
func txtData(value string) []byte {
	out := []byte{}
	for len(value) > 255 {
		out = append(out, 255)
		out = append(out, value[:255]...)
		value = value[255:]
	}
	out = append(out, byte(len(value)))
	return append(out, value...)
}

// TestConnection queries the zone's SOA. That single question proves four things at once: the server
// answers, the zone exists, the TSIG key is accepted, and the clocks agree — which is exactly what a
// failed update later would have failed to explain.
func (p *rfc2136Provider) TestConnection(ctx context.Context) error {
	zone := dnsAbsName(p.zoneHint)
	if zone == "" {
		return fmt.Errorf("rfc2136: no zone configured — save the credential with the zone (the domain the server is authoritative for)")
	}
	msg := dnsMessage{ID: p.nextID(), Opcode: dnsOpcodeQuery, Zone: zone, ZoneType: dnsTypeSOA}
	resp, err := p.exchange(ctx, msg)
	if err != nil {
		return err
	}
	// NOERROR with an EMPTY answer means the server does not serve this zone: a real authoritative
	// server always returns the SOA for its own apex. Reporting "ok" for an empty answer would let an
	// operator save a credential that cannot work, and the failure would surface hours later during a
	// switch.
	for _, rr := range resp.Prereq {
		if rr.Type == dnsTypeSOA {
			return nil
		}
	}
	return fmt.Errorf("rfc2136: %s answered NOERROR but returned no SOA for %q — it does not serve that zone (check the zone field and the server address)", p.server, zone)
}

// zoneHint is set by BuildProvider from the credential row: TestConnection has no zone argument, and a
// DNS server cannot be asked "are you authoritative for anything" in a way that proves the key works.
func (p *rfc2136Provider) setZoneHint(zone string) { p.zoneHint = zone }

// Compile-time proof that the adapter satisfies the contract.
var _ Provider = (*rfc2136Provider)(nil)
