// sync_routes_converge_b374_test.go — B374 (2026-10-09).
//
// The live incident these pin, in order (measured on the reference deployment):
//
//	14:08      the container is recreated (entrypoint rebuilds the binary)
//	14:0x      the first route-apply pass cannot reach karolina (SSH timed out), so
//	           B352 excludes her and `prefix_owner` moves 197 prefixes to emilia,
//	           and `device_exit_node_prefs` is re-pinned to tag:dev-infra-emilia
//	14:0x-15:2 emilia's `--advertise-routes` is NEVER pushed: `headscale nodes list`
//	           shows emilia available=2 approved=2 and karolina 199/199, and
//	           `global_settings[relay_apply_state:emilia]` keeps its 13:01:07
//	           timestamp. The container log holds ZERO lines containing `advertis`
//	           or `staggeredSync`. Every device whose grant pins
//	           via=[tag:dev-infra-emilia] loses YouTube/Telegram and every other
//	           pinned prefix.
//	15:2x      a manual "Re-sync all" fixes it instantly (emilia 205/205).
//
// Root cause: the ONLY periodic caller of StaggeredSync() was the domain
// auto-updater, gated on `added > 0 || removed > 0`; that tick logged
// "18 domain(s) skipped (re-resolve interval 6h0m0s)" for the whole outage.
//
// The tests below drive `convergeAdvertisedRoutes` (the pass body) against a real
// migrated SQLite database and a real headscale client pointed at a node stub, with
// the ONE per-relay apply step replaced by a recorder — so "no transport is opened
// for a converged relay" is an assertion, not a hope.
package exit_rules

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	skygatedb "skygate/internal/db"
	"skygate/internal/headscale"
)

// b374NodeStub serves `/api/v1/node` with whatever Nodes is set to. `HSNode.Name`
// is set deliberately: NodeView.Hostname falls back to Name only when GivenName is
// empty, and the pass matches the relay on either.
type b374NodeStub struct {
	mu    sync.Mutex
	nodes []headscale.HSNode
	reads int
}

func (s *b374NodeStub) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/node" {
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
			return
		}
		s.mu.Lock()
		s.reads++
		nodes := append([]headscale.HSNode{}, s.nodes...)
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"nodes": nodes})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (s *b374NodeStub) readCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads
}

// b374RelayNode is one exit-node row: the base routes are what a relay that has
// never advertised anything reports.
func b374RelayNode(name string, routes []string) headscale.HSNode {
	return headscale.HSNode{
		ID:              name,
		Name:            name,
		GivenName:       name,
		IPAddresses:     []string{"100.64.0.9"},
		Online:          true,
		Tags:            []string{"tag:exit-node"},
		AvailableRoutes: routes,
		ApprovedRoutes:  routes,
	}
}

// b374ApplyRecorder replaces the ONE per-relay apply step and records what the pass
// decided to apply. It is the seam that makes "the converged relay was not touched"
// observable without a transport.
type b374ApplyRecorder struct {
	mu    sync.Mutex
	calls []string
	// keys records the route list handed in for each call, so a test can assert
	// that the owned set (and not the stale advertised one) was applied.
	keys map[string][]string
	// err, when set, makes every application report a label the pass reads as a
	// failure (the `=err=` shape the result map uses).
	err string
}

func newB374ApplyRecorder() *b374ApplyRecorder {
	return &b374ApplyRecorder{keys: map[string][]string{}}
}

func (r *b374ApplyRecorder) install(t *testing.T) {
	t.Helper()
	prev := syncOneExitNodeFn
	syncOneExitNodeFn = func(_ *headscale.Client, _ *sql.DB, _ func(string) int, _ string, node string, routes []string, result map[string]string) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.calls = append(r.calls, node)
		r.keys[node] = append([]string{}, routes...)
		if r.err != "" {
			result[node] = "ssh=err=" + r.err
			return
		}
		result[node] = "ssh=ok via tailnet approved=2"
	}
	t.Cleanup(func() { syncOneExitNodeFn = prev })
}

func (r *b374ApplyRecorder) applied() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := append([]string{}, r.calls...)
	sort.Strings(out)
	return out
}

func (r *b374ApplyRecorder) routesFor(node string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string{}, r.keys[node]...)
}

func (r *b374ApplyRecorder) touched(node string) bool {
	for _, c := range r.applied() {
		if c == node {
			return true
		}
	}
	return false
}

// b374SeedAssignment seeds the assignment table (the owner named by the caller) and
// makes sure both relays have a recorded apply and a health row — the B352 relay list
// then contains both, which is what the pass iterates. Existing rows are left alone.
func b374SeedAssignment(t *testing.T, d *sql.DB, owner string) {
	t.Helper()
	if owner != "" {
		if _, err := d.Exec(`INSERT INTO prefix_owner (prefix, exit_node_id, source, claims, devices, updated_at)
		                     VALUES ('104.16.0.0/12', ?, 'explicit', 1, 1, 0)`, owner); err != nil {
			t.Fatalf("seed prefix_owner: %v", err)
		}
	}
	now := time.Now().Unix()
	for _, relay := range []string{"karolina", "emilia"} {
		if _, err := d.Exec(`INSERT INTO global_settings (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value`,
			RelayApplyStateKey(relay), strconv.FormatInt(now, 10)+"|ok"); err != nil {
			t.Fatalf("seed relay_apply_state for %s: %v", relay, err)
		}
		if _, err := d.Exec(`INSERT INTO exit_node_health (node_id, hostname, online, last_seen, advertised_routes_ok,
		                              has_exit_tag, state, healthy, last_check_at, last_state_change_at, consecutive_failures)
		                     SELECT ?, ?, 1, ?, 1, 1, 'online', 1, ?, ?, 0
		                     WHERE NOT EXISTS (SELECT 1 FROM exit_node_health WHERE hostname = ?)`,
			relay, relay, now, now, now, relay); err != nil {
			t.Fatalf("seed exit_node_health for %s: %v", relay, err)
		}
	}
}

// --- A: the pure predicate ------------------------------------------------------

// TestB374_RoutesNeedConvergence treats order and duplicates as noise, always
// requires the base routes, and catches the same-size swap that a cardinality check
// would miss.
func TestB374_RoutesNeedConvergence(t *testing.T) {
	cases := []struct {
		name       string
		owned      []string
		advertised []string
		want       bool
	}{
		{"identical", []string{"104.16.0.0/12"}, []string{"104.16.0.0/12"}, false},
		{"both empty", nil, nil, false},
		{"order and duplicates are noise", []string{"a/32", "b/32", "a/32"}, []string{"b/32", "a/32", "b/32"}, false},
		{"bases explicit on the advertised side only", []string{"0.0.0.0/0", "::/0", "c/32"}, []string{"c/32"}, false},
		{"the empty advertisement is the base pair on the wire", []string{"0.0.0.0/0", "::/0"}, nil, false},
		// Both sides carry the base pair after normalisation (the apply path injects
		// them unconditionally), so an advertisement that merely omits them is NOT a
		// difference — a relay is not rewritten because headscale reported one route.
		{"a base omitted by the node is not a difference", []string{"11.0.0.0/24"}, []string{"11.0.0.0/24"}, false},
		{"the live case: 2 advertised, 199 owned", []string{"a/32", "b/32", "c/32"}, nil, true},
		{"same size, different content", []string{"a/32", "b/32"}, []string{"a/32", "c/32"}, true},
		{"stale extra", nil, []string{"old/32"}, true},
	}
	for _, tc := range cases {
		got, detail := RoutesNeedConvergence(tc.owned, tc.advertised)
		if got != tc.want {
			t.Errorf("%s: need=%v, want %v (detail %q)", tc.name, got, tc.want, detail)
		}
		if got && strings.TrimSpace(detail) == "" {
			t.Errorf("%s: a difference must be named, got an empty detail", tc.name)
		}
		if !got && detail != "" {
			t.Errorf("%s: a converged pair must have no detail, got %q", tc.name, detail)
		}
	}
}

// TestB374_NormalizeRouteSetAlwaysRequiresTheBases: the base pair is the contract
// every caller shares, so a relay that "advertises nothing" is already converged with
// an owned set of nothing — that is the property that keeps the pass from opening a
// transport for every idle relay.
func TestB374_NormalizeRouteSetAlwaysRequiresTheBases(t *testing.T) {
	set := NormalizeRouteSet(nil)
	if !set["0.0.0.0/0"] || !set["::/0"] || len(set) != 2 {
		t.Fatalf("NormalizeRouteSet(nil) = %v, want exactly the two base routes", set)
	}
	if need, _ := RoutesNeedConvergence(nil, nil); need {
		t.Fatal("a relay that owns nothing and advertises nothing must NOT need an apply — that would be one ssh per tick per idle relay")
	}
}

// --- B: the pass ------------------------------------------------------------------

// TestB374_ConvergencePassAppliesOnlyTheRelayThatDiffers is the whole block in one
// test: emilia owns a prefix it does not advertise → it is applied with exactly its
// OWNED set; karolina advertises nothing and owns nothing → it is converged and the
// apply path is NOT called for it (no ssh, no local tailscale set, no write).
func TestB374_ConvergencePassAppliesOnlyTheRelayThatDiffers(t *testing.T) {
	resetRouteConvergenceSlot()
	t.Cleanup(resetRouteConvergenceSlot)

	d := newB276DB(t)
	seedB276(t, d)
	b374SeedAssignment(t, d, "emilia")

	rec := newB374ApplyRecorder()
	rec.install(t)

	stub := &b374NodeStub{}
	stub.nodes = []headscale.HSNode{
		// The live state: emilia advertises only the bases while owning a prefix.
		b374RelayNode("emilia", []string{"0.0.0.0/0", "::/0"}),
		// karolina owns nothing and advertises the bases: converged.
		b374RelayNode("karolina", []string{"0.0.0.0/0", "::/0"}),
	}
	srv := stub.server(t)
	s := &Service{DB: skygatedb.FixedDBSource{DB: d}, HS: headscale.New(srv.URL, "test-token")}

	h := s.convergeAdvertisedRoutes("test")

	if got := rec.applied(); len(got) != 1 || got[0] != "emilia" {
		t.Fatalf("applied relays = %v, want exactly [emilia] — the relay whose owned set differs", got)
	}
	if rec.touched("karolina") {
		t.Fatalf("the pass touched karolina although its advertisement already matched its (empty) owned set — that is the SSH storm this pass must not become")
	}
	// The applied route list must be the OWNED set (plus the bases), not the stale
	// advertised one.
	gotRoutes := rec.routesFor("emilia")
	want := []string{"0.0.0.0/0", "104.16.0.0/12", "::/0"}
	if strings.Join(gotRoutes, ",") != strings.Join(want, ",") {
		t.Fatalf("emilia was applied with %v, want the owned set %v", gotRoutes, want)
	}
	if len(h.Applied) != 1 || len(h.Converged) != 1 {
		t.Fatalf("health = applied=%v converged=%v, want one of each", h.Applied, h.Converged)
	}
	if stub.readCount() == 0 {
		t.Fatal("the pass never read the headscale node view")
	}
}

// TestB374_ConvergedHostAppliesNothing is the safety half: once every relay's
// advertisement equals its owned set, a pass calls the apply path ZERO times — so on
// a healthy install the new 5-minute pass costs one node read and nothing else.
func TestB374_ConvergedHostAppliesNothing(t *testing.T) {
	resetRouteConvergenceSlot()
	t.Cleanup(resetRouteConvergenceSlot)

	d := newB276DB(t)
	seedB276(t, d)
	b374SeedAssignment(t, d, "emilia")

	rec := newB374ApplyRecorder()
	rec.install(t)

	stub := &b374NodeStub{}
	stub.nodes = []headscale.HSNode{
		b374RelayNode("emilia", []string{"0.0.0.0/0", "104.16.0.0/12", "::/0"}),
		b374RelayNode("karolina", nil),
	}
	srv := stub.server(t)
	s := &Service{DB: skygatedb.FixedDBSource{DB: d}, HS: headscale.New(srv.URL, "test-token")}

	h := s.convergeAdvertisedRoutes("test")

	if got := rec.applied(); len(got) != 0 {
		t.Fatalf("a converged host applied %v — the pass must not open a transport when the sets match", got)
	}
	if len(h.Applied) != 0 || len(h.Converged) != 2 {
		t.Fatalf("health = applied=%v converged=%v, want applied=[] converged=2", h.Applied, h.Converged)
	}
}

// TestB374_UnreadableAdvertisedPlaneAppliesNothing: an empty node view must never be
// read as "every relay advertises nothing". Without this, a headscale hiccup would
// rewrite every relay on every tick.
func TestB374_UnreadableAdvertisedPlaneAppliesNothing(t *testing.T) {
	resetRouteConvergenceSlot()
	t.Cleanup(resetRouteConvergenceSlot)

	d := newB276DB(t)
	seedB276(t, d)
	b374SeedAssignment(t, d, "emilia")

	rec := newB374ApplyRecorder()
	rec.install(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "headscale is down", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	s := &Service{DB: skygatedb.FixedDBSource{DB: d}, HS: headscale.New(srv.URL, "test-token")}

	h := s.convergeAdvertisedRoutes("test")

	if h.ReadErr == "" {
		t.Fatal("an unreadable advertised plane must be reported in ReadErr")
	}
	if got := rec.applied(); len(got) != 0 {
		t.Fatalf("applied %v although the advertised plane could not be read — absence of evidence is not evidence of a difference", got)
	}
}

// TestB374_ExcludedRelayIsNotRetriedAndIsRecordedStale is the part-3 case: the relay
// is excluded from the healthy set (its last apply failed) and still advertises
// prefixes the assignment table took away. The pass must NOT attempt the apply again
// (B309 already proved the transport fails — retrying every tick is the storm), and
// it must leave a page-visible, named record instead.
func TestB374_ExcludedRelayIsNotRetriedAndIsRecordedStale(t *testing.T) {
	resetRouteConvergenceSlot()
	t.Cleanup(resetRouteConvergenceSlot)

	d := newB276DB(t)
	seedB276(t, d)
	b374SeedAssignment(t, d, "emilia")
	// karolina cannot be configured any more; the assignment table has already moved
	// its prefix to emilia.
	if _, err := d.Exec(`INSERT INTO global_settings (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value`,
		RelayApplyStateKey("karolina"), strconv.FormatInt(time.Now().Unix()-120, 10)+"|err|dial tcp 100.64.0.2:18022: i/o timeout"); err != nil {
		t.Fatalf("seed karolina failure: %v", err)
	}

	rec := newB374ApplyRecorder()
	rec.install(t)

	stub := &b374NodeStub{}
	stub.nodes = []headscale.HSNode{
		b374RelayNode("emilia", []string{"0.0.0.0/0", "104.16.0.0/12", "::/0"}),
		// The stale phantom: karolina still advertises what she no longer owns.
		b374RelayNode("karolina", []string{"0.0.0.0/0", "::/0", "104.16.0.0/12"}),
	}
	srv := stub.server(t)
	s := &Service{DB: skygatedb.FixedDBSource{DB: d}, HS: headscale.New(srv.URL, "test-token")}

	h := s.convergeAdvertisedRoutes("test")

	if rec.touched("karolina") {
		t.Fatalf("the pass tried to apply to karolina although B309 excluded her — that is one useless ssh per tick")
	}
	if len(h.Stale) != 1 || h.Stale[0] != "karolina" {
		t.Fatalf("health.Stale = %v, want [karolina]", h.Stale)
	}
	raw, err := skygatedb.GetGlobalSetting(d, StaleAdvertisementKey("karolina"), "")
	if err != nil || strings.TrimSpace(raw) == "" {
		t.Fatalf("no %s record was written (raw=%q err=%v) — the stale advertisement stays invisible", StaleAdvertisementKey("karolina"), raw, err)
	}
	st := ParseStaleAdvertisement(raw)
	if st.Count != 1 {
		t.Errorf("recorded count = %d, want 1 (one advertised prefix outside the owned set): %q", st.Count, st.Reason)
	}
	if !strings.Contains(st.Reason, "still advertises") || !strings.Contains(st.Reason, "104.16.0.0/12") {
		t.Errorf("the recorded sentence does not name the fact and the prefix: %q", st.Reason)
	}
	// And the page can read it back.
	all := ListStaleAdvertisements(d)
	if got, ok := all["karolina"]; !ok || got.Count != 1 {
		t.Errorf("ListStaleAdvertisements = %+v, want karolina with count 1", all)
	}
}

// TestB374_StaleRecordClearsWhenTheAdvertisementConverges: a warning that outlives
// its reason is the L-60 class of bug — once the relay advertises only its owned set
// again (the operator re-synced, or it came back), the record must be gone.
func TestB374_StaleRecordClearsWhenTheAdvertisementConverges(t *testing.T) {
	resetRouteConvergenceSlot()
	t.Cleanup(resetRouteConvergenceSlot)

	d := newB276DB(t)
	seedB276(t, d)
	b374SeedAssignment(t, d, "emilia")
	if _, err := d.Exec(`INSERT INTO global_settings (key, value) VALUES (?, ?)`,
		StaleAdvertisementKey("karolina"), "1700000000|karolina still advertises 3 prefix(es) that prefix_owner no longer assigns to it: a/32,b/32,c/32"); err != nil {
		t.Fatalf("seed the stale record: %v", err)
	}
	// karolina advertises exactly her owned set (nothing beyond the bases).
	stub := &b374NodeStub{}
	stub.nodes = []headscale.HSNode{
		b374RelayNode("emilia", []string{"0.0.0.0/0", "104.16.0.0/12", "::/0"}),
		b374RelayNode("karolina", []string{"0.0.0.0/0", "::/0"}),
	}
	srv := stub.server(t)
	s := &Service{DB: skygatedb.FixedDBSource{DB: d}, HS: headscale.New(srv.URL, "test-token")}

	s.convergeAdvertisedRoutes("test")

	if _, ok := ListStaleAdvertisements(d)["karolina"]; ok {
		t.Fatal("the stale-advertisement record survived a pass that found karolina converged — the page would warn forever")
	}
}

// TestB374_PassHasItsOwnBudget: the pass must not run on every call of the two
// maintenance ticks that can fire close together, but a converged pass costs one node
// read — so the budget is asserted on the DECISION (a pure function), not on the
// number of API calls.
func TestB374_PassHasItsOwnBudget(t *testing.T) {
	resetRouteConvergenceSlot()
	t.Cleanup(resetRouteConvergenceSlot)

	d := newB276DB(t)
	seedB276(t, d)
	b374SeedAssignment(t, d, "emilia")
	rec := newB374ApplyRecorder()
	rec.install(t)

	stub := &b374NodeStub{}
	stub.nodes = []headscale.HSNode{
		b374RelayNode("emilia", []string{"0.0.0.0/0", "::/0"}),
		b374RelayNode("karolina", nil),
	}
	srv := stub.server(t)
	s := &Service{DB: skygatedb.FixedDBSource{DB: d}, HS: headscale.New(srv.URL, "test-token")}

	first := s.convergeAdvertisedRoutes("tick 1")
	if len(first.Applied) == 0 {
		t.Fatalf("the first pass applied nothing: %+v", first)
	}
	second := s.convergeAdvertisedRoutes("tick 2")
	if !second.Throttled {
		t.Fatalf("the second pass was not throttled: %+v", second)
	}
	if got := len(rec.applied()); got != 1 {
		t.Fatalf("apply calls = %d, want 1 — the budget must absorb two ticks inside the interval", got)
	}
	// The pure decision, driven without a clock.
	now := time.Now()
	resetRouteConvergenceSlot()
	if !takeRouteConvergenceSlot(now, routeConvergenceInterval) {
		t.Fatal("the first claim must be granted")
	}
	if takeRouteConvergenceSlot(now.Add(routeConvergenceInterval-time.Second), routeConvergenceInterval) {
		t.Fatal("a claim inside the interval must be refused")
	}
	if !takeRouteConvergenceSlot(now.Add(routeConvergenceInterval+time.Second), routeConvergenceInterval) {
		t.Fatal("a claim after the interval must be granted")
	}
}

// TestB374_OwnedRoutesForRelayAlwaysCarriesTheBases: the applied list is what
// `tailscale set --advertise-routes=` REPLACES, so it must never be empty.
func TestB374_OwnedRoutesForRelayAlwaysCarriesTheBases(t *testing.T) {
	d := newB276DB(t)
	seedB276(t, d)
	b374SeedAssignment(t, d, "emilia")
	s := &Service{DB: skygatedb.FixedDBSource{DB: d}}

	got := s.OwnedRoutesForRelay("karolina")
	if strings.Join(got, ",") != "0.0.0.0/0,::/0" {
		t.Fatalf("OwnedRoutesForRelay(karolina) = %v, want the two base routes (a relay that owns nothing must still be an exit node)", got)
	}
	got = s.OwnedRoutesForRelay("emilia")
	if strings.Join(got, ",") != "0.0.0.0/0,104.16.0.0/12,::/0" {
		t.Fatalf("OwnedRoutesForRelay(emilia) = %v, want the base routes plus the owned prefix", got)
	}
}

// TestB374_AssignmentsAndAdvertisedPlaneUseOneSource: the pass and the prefix card
// must read the advertised routes from the same headscale call, so the page cannot
// disagree with the decision. A cheap structural assertion — the point is that no
// second API call was invented.
func TestB374_AssignmentsAndAdvertisedPlaneUseOneSource(t *testing.T) {
	d := newB276DB(t)
	seedB276(t, d)
	b374SeedAssignment(t, d, "emilia")
	// The recorder is installed so the pass cannot open a real transport even on a
	// machine where one would work; this test is about the SOURCE of the plane.
	rec := newB374ApplyRecorder()
	rec.install(t)
	stub := &b374NodeStub{}
	stub.nodes = []headscale.HSNode{b374RelayNode("emilia", nil), b374RelayNode("karolina", nil)}
	srv := stub.server(t)
	s := &Service{DB: skygatedb.FixedDBSource{DB: d}, HS: headscale.New(srv.URL, "test-token")}

	resetRouteConvergenceSlot()
	t.Cleanup(resetRouteConvergenceSlot)
	h := s.convergeAdvertisedRoutes("test")
	if h.ReadErr != "" {
		t.Fatalf("ReadErr = %q, want empty (ListAllNodes is the same source the rest of the code reads)", h.ReadErr)
	}
	if got := stub.readCount(); got != 1 {
		t.Fatalf("node reads = %d, want 1 — the pass must resolve the advertised plane from ONE call", got)
	}
}
