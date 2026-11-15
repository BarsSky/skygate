// cloudflare_b474_test.go — B474: the Cloudflare adapter, exercised against a FAKE API.
//
// A provider adapter cannot be verified by grepping its source: the questions that matter are "does it
// send the token in the header", "does it read `success:false` as failure", "does it create a record
// when there is none and patch it when there is", "does a proxied record get refused instead of being
// silently rewritten", "does pagination stop". All of those are behaviour of the WIRE, so the tests
// stand up an httptest server that answers like Cloudflare v4 and assert on the requests it received.
//
// The fake is deliberately strict about the two things a lenient fake would hide: the Authorization
// header (a token in a query parameter would leak into logs) and the envelope's `success` flag (a 200
// with success:false is how a panel ends up claiming "ok" while nothing changed).
package dns

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// cfFake is one recorded request, so a test can assert on the METHOD, the PATH and the BODY.
type cfFakeReq struct {
	Method string
	Path   string
	Query  string
	Body   map[string]any
	Auth   string
}

// cfFakeAPI is a minimal Cloudflare v4 server: zones, records, verify, and a call log.
type cfFakeAPI struct {
	t        *testing.T
	mu       sync.Mutex
	requests []cfFakeReq
	// records is the zone's DNS records; the fake assigns ids on create.
	records []cfRecord
	// zoneName is the one zone this token can see.
	zoneName string
	// omitZone makes /zones answer an empty result (a token that cannot see the zone).
	omitZone bool
	// failSuccess answers HTTP 200 with success:false (a permission error) — the shape that must NOT
	// be read as success.
	failSuccess bool
	// proxyRecord makes the A record PROXIED.
	proxyRecord bool
	// seq gives every created record a UNIQUE id. A constant id made two TXT records share one, and a
	// delete by that id then removed both — a defect of the FAKE that looked exactly like an adapter
	// deleting the wrong record, which is why it is fixed here and never worked around in the adapter.
	seq int
}

func newCFFake(t *testing.T) (*cfFakeAPI, *httptest.Server) {
	t.Helper()
	f := &cfFakeAPI{t: t, zoneName: "example.com"}
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(srv.Close)
	return f, srv
}

// itoa formats a small counter without pulling strconv into the assertions.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

func (f *cfFakeAPI) calls() []cfFakeReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]cfFakeReq(nil), f.requests...)
}

// recordIDs is a helper: the fake's current records, by name+type.
func (f *cfFakeAPI) record(name, typ string) (cfRecord, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.records {
		if strings.EqualFold(r.Name, name) && strings.EqualFold(r.Type, typ) {
			return r, true
		}
	}
	return cfRecord{}, false
}

func (f *cfFakeAPI) handle(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	var body map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &body)
	}
	f.mu.Lock()
	f.requests = append(f.requests, cfFakeReq{
		Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Body: body,
		Auth: r.Header.Get("Authorization"),
	})
	f.mu.Unlock()

	reply := func(status int, result any, success bool) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		env := map[string]any{"success": success, "result": result}
		if !success {
			env["errors"] = []map[string]any{{"code": 9109, "message": "Unauthorized to access requested resource"}}
		}
		_ = json.NewEncoder(w).Encode(env)
	}

	if f.failSuccess {
		reply(http.StatusOK, []any{}, false)
		return
	}

	path := strings.TrimSuffix(r.URL.Path, "/")
	switch {
	case path == "/user/tokens/verify":
		reply(http.StatusOK, map[string]any{"id": "tok", "status": "active"}, true)
	case path == "/zones":
		if r.URL.Query().Get("name") != f.zoneName || f.omitZone {
			reply(http.StatusOK, []any{}, true)
			return
		}
		reply(http.StatusOK, []map[string]any{{"id": "zone-1", "name": f.zoneName}}, true)
	case path == "/zones/zone-1/dns_records" && r.Method == http.MethodGet:
		f.mu.Lock()
		out := make([]map[string]any, 0, len(f.records))
		wantName := r.URL.Query().Get("name")
		wantType := r.URL.Query().Get("type")
		for _, rec := range f.records {
			if wantName != "" && !strings.EqualFold(rec.Name, wantName) {
				continue
			}
			if wantType != "" && !strings.EqualFold(rec.Type, wantType) {
				continue
			}
			out = append(out, map[string]any{
				"id": rec.ID, "name": rec.Name, "type": rec.Type,
				"content": rec.Content, "ttl": rec.TTL, "proxied": rec.Proxied,
			})
		}
		f.mu.Unlock()
		reply(http.StatusOK, out, true)
	case path == "/zones/zone-1/dns_records" && r.Method == http.MethodPost:
		name, _ := body["name"].(string)
		typ, _ := body["type"].(string)
		content, _ := body["content"].(string)
		ttl := 1
		if v, ok := body["ttl"].(float64); ok {
			ttl = int(v)
		}
		f.mu.Lock()
		f.seq++
		newID := "rec-new-" + typ + "-" + itoa(f.seq)
		f.records = append(f.records, cfRecord{
			ID: newID, Name: name, Type: typ, Content: content, TTL: ttl,
			Proxied: f.proxyRecord && typ == "A",
		})
		f.mu.Unlock()
		reply(http.StatusOK, map[string]any{"id": newID}, true)
	case strings.HasPrefix(path, "/zones/zone-1/dns_records/") && (r.Method == http.MethodPatch || r.Method == http.MethodPut):
		id := strings.TrimPrefix(path, "/zones/zone-1/dns_records/")
		f.mu.Lock()
		for i := range f.records {
			if f.records[i].ID == id {
				if v, ok := body["content"].(string); ok {
					f.records[i].Content = v
				}
				if v, ok := body["ttl"].(float64); ok {
					f.records[i].TTL = int(v)
				}
			}
		}
		f.mu.Unlock()
		reply(http.StatusOK, map[string]any{"id": id}, true)
	case strings.HasPrefix(path, "/zones/zone-1/dns_records/") && r.Method == http.MethodDelete:
		id := strings.TrimPrefix(path, "/zones/zone-1/dns_records/")
		f.mu.Lock()
		kept := f.records[:0]
		for _, rec := range f.records {
			if rec.ID != id {
				kept = append(kept, rec)
			}
		}
		f.records = kept
		f.mu.Unlock()
		reply(http.StatusOK, map[string]any{"id": id}, true)
	default:
		reply(http.StatusNotFound, nil, false)
	}
}

// cfTestProvider builds the adapter against the fake.
func cfTestProvider(t *testing.T, baseURL string) *cloudflareProvider {
	t.Helper()
	row := extRow("cloudflare", "example.com", map[string]string{"token": "test-token"})
	p, err := BuildProvider("cloudflare", BuildDeps{Row: &row, BaseURL: baseURL})
	if err != nil {
		t.Fatalf("BuildProvider(cloudflare) = %v", err)
	}
	cp, ok := p.(*cloudflareProvider)
	if !ok {
		t.Fatalf("BuildProvider returned %T, want *cloudflareProvider", p)
	}
	return cp
}

// TestB474_TokenTravelsInTheHeader — a token in a query string ends up in proxy logs; the adapter must
// send it as a bearer header, and every request it made must carry it.
func TestB474_TokenTravelsInTheHeader(t *testing.T) {
	f, srv := newCFFake(t)
	p := cfTestProvider(t, srv.URL)
	if err := p.TestConnection(context.Background()); err != nil {
		t.Fatalf("TestConnection: %v", err)
	}
	if _, err := p.GetRecord(context.Background(), "example.com", "skygate"); err != nil && !errors.Is(err, ErrRecordNotFound) {
		t.Fatalf("GetRecord: %v", err)
	}
	calls := f.calls()
	if len(calls) == 0 {
		t.Fatal("the adapter made no request at all")
	}
	for _, c := range calls {
		if c.Auth != "Bearer test-token" {
			t.Errorf("%s %s sent Authorization %q, want the bearer token", c.Method, c.Path, c.Auth)
		}
		if strings.Contains(c.Query, "token") {
			t.Errorf("%s %s put the token in the query string: %q", c.Method, c.Path, c.Query)
		}
	}
}

// TestB474_GetRecordAndNotFound — an absent A record is ErrRecordNotFound (a fresh deploy), not "".
func TestB474_GetRecordAndNotFound(t *testing.T) {
	f, srv := newCFFake(t)
	p := cfTestProvider(t, srv.URL)
	if _, err := p.GetRecord(context.Background(), "example.com", "skygate"); err == nil {
		t.Fatal("GetRecord answered success for a record that does not exist")
	} else if !errors.Is(err, ErrRecordNotFound) {
		t.Fatalf("GetRecord error = %v, want ErrRecordNotFound", err)
	}
	f.mu.Lock()
	f.records = append(f.records, cfRecord{ID: "rec-a", Name: "skygate.example.com", Type: "A", Content: "192.0.2.10", TTL: 300})
	f.mu.Unlock()
	ip, err := p.GetRecord(context.Background(), "example.com", "skygate")
	if err != nil {
		t.Fatalf("GetRecord: %v", err)
	}
	if ip != "192.0.2.10" {
		t.Errorf("GetRecord = %q, want 192.0.2.10", ip)
	}
}

// TestB474_UpdateCreatesThenPatches — the two shapes the HA failover needs: a missing record is
// CREATED, an existing one is PATCHED (not deleted and recreated, which would drop its id and allow a
// reader to see "no record at all").
func TestB474_UpdateCreatesThenPatches(t *testing.T) {
	f, srv := newCFFake(t)
	p := cfTestProvider(t, srv.URL)

	if err := p.UpdateRecord(context.Background(), "example.com", "skygate", "192.0.2.10"); err != nil {
		t.Fatalf("UpdateRecord (create): %v", err)
	}
	created, ok := f.record("skygate.example.com", "A")
	if !ok {
		t.Fatal("the A record was not created")
	}
	if created.Content != "192.0.2.10" {
		t.Errorf("created content = %q", created.Content)
	}

	if err := p.UpdateRecord(context.Background(), "example.com", "skygate", "192.0.2.11"); err != nil {
		t.Fatalf("UpdateRecord (patch): %v", err)
	}
	patched, _ := f.record("skygate.example.com", "A")
	if patched.Content != "192.0.2.11" {
		t.Errorf("content after the second update = %q, want 192.0.2.11", patched.Content)
	}
	if patched.ID != created.ID {
		t.Errorf("the record id changed from %q to %q — the update recreated the record", created.ID, patched.ID)
	}
	// A non-IP target is refused before any request is made.
	before := len(f.calls())
	if err := p.UpdateRecord(context.Background(), "example.com", "skygate", "not-an-ip"); err == nil {
		t.Error("UpdateRecord accepted a non-IP value")
	}
	if len(f.calls()) != before {
		t.Error("UpdateRecord called the API before validating the address")
	}
}

// TestB474_ProxiedRecordIsRefused — an orange-clouded A record answers with Cloudflare's edge
// addresses, so rewriting its content would look like a switch that silently does nothing.
func TestB474_ProxiedRecordIsRefused(t *testing.T) {
	f, srv := newCFFake(t)
	f.proxyRecord = true
	p := cfTestProvider(t, srv.URL)
	if err := p.UpdateRecord(context.Background(), "example.com", "skygate", "192.0.2.10"); err != nil {
		t.Fatalf("UpdateRecord (create, proxied fixture): %v", err)
	}
	err := p.UpdateRecord(context.Background(), "example.com", "skygate", "192.0.2.11")
	if err == nil {
		t.Fatal("UpdateRecord rewrote a PROXIED record — the switch would have no visible effect")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "prox") {
		t.Errorf("the refusal does not say the record is proxied: %v", err)
	}
}

// TestB474_SetTTLValidatesAndWrites — the accepted set is enforced locally with a message that names
// it, and an accepted value reaches the record.
func TestB474_SetTTLValidatesAndWrites(t *testing.T) {
	f, srv := newCFFake(t)
	p := cfTestProvider(t, srv.URL)
	if err := p.UpdateRecord(context.Background(), "example.com", "skygate", "192.0.2.10"); err != nil {
		t.Fatalf("seed the record: %v", err)
	}
	if err := p.SetTTL(context.Background(), "example.com", "skygate", 45); err == nil {
		t.Error("SetTTL accepted 45, which the API rejects")
	} else if !strings.Contains(err.Error(), "300") {
		t.Errorf("the refusal does not name the allowed values: %v", err)
	}
	if err := p.SetTTL(context.Background(), "example.com", "skygate", 300); err != nil {
		t.Fatalf("SetTTL(300): %v", err)
	}
	rec, _ := f.record("skygate.example.com", "A")
	if rec.TTL != 300 {
		t.Errorf("record TTL = %d, want 300", rec.TTL)
	}
	if err := p.SetTTL(context.Background(), "example.com", "absent", 300); !errors.Is(err, ErrRecordNotFound) {
		t.Errorf("SetTTL on a missing record = %v, want ErrRecordNotFound", err)
	}
}

// TestB474_TXTRoundTrip — create then delete, and a SECOND delete is success (DNS-01 cleanup is
// retried; failing it would fail a finished order). A different value at the same name is left alone.
func TestB474_TXTRoundTrip(t *testing.T) {
	f, srv := newCFFake(t)
	p := cfTestProvider(t, srv.URL)
	ctx := context.Background()

	if err := p.CreateTXT(ctx, "example.com", "_acme-challenge", "challenge-1", 0); err != nil {
		t.Fatalf("CreateTXT: %v", err)
	}
	rec, ok := f.record("_acme-challenge.example.com", "TXT")
	if !ok {
		t.Fatal("the TXT record was not created")
	}
	if rec.TTL != 60 {
		t.Errorf("the challenge TTL = %d, want the 60s default", rec.TTL)
	}
	// A second challenge for the same name must not delete the first.
	if err := p.CreateTXT(ctx, "example.com", "_acme-challenge", "challenge-2", 60); err != nil {
		t.Fatalf("CreateTXT (second): %v", err)
	}
	if err := p.DeleteTXT(ctx, "example.com", "_acme-challenge", "challenge-1"); err != nil {
		t.Fatalf("DeleteTXT: %v", err)
	}
	if _, ok := f.record("_acme-challenge.example.com", "TXT"); !ok {
		t.Fatal("DeleteTXT removed the OTHER challenge record as well")
	}
	if err := p.DeleteTXT(ctx, "example.com", "_acme-challenge", "challenge-1"); err != nil {
		t.Errorf("a repeated DeleteTXT failed (%v) — DNS-01 cleanup retries must be idempotent", err)
	}
	if err := p.DeleteTXT(ctx, "example.com", "_acme-challenge", "challenge-2"); err != nil {
		t.Fatalf("DeleteTXT (second value): %v", err)
	}
	if _, ok := f.record("_acme-challenge.example.com", "TXT"); ok {
		t.Error("the TXT record survived its deletion")
	}
}

// TestB474_ListRecordsStripsTheZone — the panel matches names the operator typed (bare subnames), so
// the FQDN the API returns must be shortened, and the apex must become "" rather than the zone name.
func TestB474_ListRecordsStripsTheZone(t *testing.T) {
	f, srv := newCFFake(t)
	f.mu.Lock()
	f.records = []cfRecord{
		{ID: "1", Name: "example.com", Type: "A", Content: "192.0.2.1", TTL: 300},
		{ID: "2", Name: "skygate.example.com", Type: "A", Content: "192.0.2.10", TTL: 600},
		{ID: "3", Name: "skygate.example.com", Type: "TXT", Content: "v=spf1 -all", TTL: 300},
	}
	f.mu.Unlock()
	p := cfTestProvider(t, srv.URL)
	recs, err := p.ListRecords(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("ListRecords: %v", err)
	}
	if len(recs) != 3 {
		t.Fatalf("ListRecords returned %d records, want 3: %+v", len(recs), recs)
	}
	byName := map[string]Record{}
	for _, r := range recs {
		byName[r.Name+"/"+r.Type] = r
	}
	if r, ok := byName["skygate/A"]; !ok || r.Content != "192.0.2.10" || r.TTL != 600 {
		t.Errorf("the skygate/A record is wrong: %+v", recs)
	}
	if _, ok := byName["/A"]; !ok {
		t.Errorf("the apex record was not shortened to \"\": %+v", byName)
	}
	if _, ok := byName["skygate/TXT"]; !ok {
		t.Errorf("the TXT record is missing: %+v", byName)
	}
}

// TestB474_SuccessFalseIsAFailure — HTTP 200 with success:false is a Cloudflare permission error, and
// reading it as success is how a page reports "ok" while nothing was written.
func TestB474_SuccessFalseIsAFailure(t *testing.T) {
	f, srv := newCFFake(t)
	f.failSuccess = true
	p := cfTestProvider(t, srv.URL)
	err := p.TestConnection(context.Background())
	if err == nil {
		t.Fatal("a success=false answer was reported as a working connection")
	}
	if !strings.Contains(err.Error(), "9109") {
		t.Errorf("the provider's own error code is not surfaced: %v", err)
	}
}

// TestB474_UnreadableZoneNamesThePermission — a token that cannot see the zone answers an empty list;
// the error must name the zone and the permission instead of a bare 403.
func TestB474_UnreadableZoneNamesThePermission(t *testing.T) {
	f, srv := newCFFake(t)
	f.omitZone = true
	p := cfTestProvider(t, srv.URL)
	_, err := p.GetRecord(context.Background(), "example.com", "skygate")
	if err == nil {
		t.Fatal("GetRecord succeeded for a zone the token cannot see")
	}
	for _, want := range []string{"example.com", "Zone:Read"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %q: %v", want, err)
		}
	}
}

// TestB474_CapabilitiesAreTheAdaptersTruth — Cloudflare really does A/AAAA/TXT/TTL, and the registry is
// what the panel's badges read, so the two must agree.
func TestB474_CapabilitiesAreTheAdaptersTruth(t *testing.T) {
	_, srv := newCFFake(t)
	p := cfTestProvider(t, srv.URL)
	spec, ok := LookupSpec("cloudflare")
	if !ok {
		t.Fatal("cloudflare is not in the provider registry — the panel would not offer it")
	}
	if got := p.Capabilities(); got != spec.Caps {
		t.Errorf("adapter capabilities %+v disagree with the registry %+v", got, spec.Caps)
	}
	want := Capabilities{A: true, AAAA: true, TXT: true, TTL: true}
	if p.Capabilities() != want {
		t.Errorf("capabilities = %+v, want %+v", p.Capabilities(), want)
	}
}

// TestB474_NoTokenIsActionable — a missing credential names where to put it, instead of failing later
// with 401 from the API.
func TestB474_NoTokenIsActionable(t *testing.T) {
	row := extRow("cloudflare", "example.com", map[string]string{})
	t.Setenv("SKYGATE_CLOUDFLARE_TOKEN", "")
	_, err := BuildProvider("cloudflare", BuildDeps{Row: &row})
	if err == nil {
		t.Fatal("BuildProvider accepted a cloudflare row without a token")
	}
	for _, want := range []string{"/admin/ha", cloudflareTokenEnv} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %q: %v", want, err)
		}
	}
	// The documented env fallback still builds a provider (a CLI run before anything is stored).
	t.Setenv("SKYGATE_CLOUDFLARE_TOKEN", "env-token")
	p, err := BuildProvider("cloudflare", BuildDeps{})
	if err != nil || p == nil {
		t.Fatalf("BuildProvider with an env token = %v, %v", p, err)
	}
	if p.Name() != "cloudflare" {
		t.Errorf("provider name = %q", p.Name())
	}
}
