// B276 — the control plane must follow the data plane.
//
// The assignment table and the advertised routes are recomputed on every sync pass
// (minutes), while the ACL — whose per-CIDR grants carry `via=[owner]` since B275 —
// was regenerated only when a rule, user or device changed. A prefix that changed
// relay therefore kept a pin naming the OLD relay: headscale's `via` is a
// permission filter, so the client silently never received that route and fell back
// to the direct path.
//
// Live on the reference host: the ACL was applied at 18:19, the table moved 28
// Cloudflare/Google prefixes to the other relay after 19:26, and the operator's
// device lost its whole Cloudflare set — with no log line anywhere saying so. The
// tests below are that sequence, on a real (migrated, in-memory) database and a real
// headscale client pointed at a policy stub.
package exit_rules

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	skygatedb "skygate/internal/db"
	"skygate/internal/headscale"
)

// b276Stub is a headscale policy endpoint: GET returns the policy it is "serving"
// and PUT records what skygate pushed.
type b276Stub struct {
	mu     sync.Mutex
	served string
	puts   []string
}

func (s *b276Stub) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/policy":
			s.mu.Lock()
			served := s.served
			s.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"policy": served})
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/policy":
			var body struct {
				Policy json.RawMessage `json:"policy"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "bad body", http.StatusBadRequest)
				return
			}
			raw := strings.TrimSpace(string(body.Policy))
			if strings.HasPrefix(raw, `"`) {
				var unq string
				if err := json.Unmarshal(body.Policy, &unq); err == nil {
					raw = unq
				}
			}
			s.mu.Lock()
			s.puts = append(s.puts, raw)
			s.served = raw
			s.mu.Unlock()
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"policy":"ok"}`))
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (s *b276Stub) putCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.puts)
}

func (s *b276Stub) lastPut() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.puts) == 0 {
		return ""
	}
	return s.puts[len(s.puts)-1]
}

// newB276DB builds a fully migrated database (the real schema, so the generator
// runs against the real column set).
//
// The database is a FILE, not `:memory:`. The sqlite pool hands each new
// connection its own empty in-memory database, so the seeded rows were invisible
// to the next query — and every reader here wraps a failed query into an empty
// result, so the symptom was an empty owner map rather than an error.
func newB276DB(t *testing.T) *sql.DB {
	t.Helper()
	_, d, err := skygatedb.OpenWithDialect("sqlite:" + t.TempDir() + "/b276.db")
	if err != nil {
		t.Skipf("sqlite dialect unavailable in this build: %v", err)
	}
	if err := skygatedb.MigrateSQLite(d); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// seedB276 wires the minimum the generator + the ownership engine read: one user,
// its device, the two relays, their health, and the rule that claims the prefix.
func seedB276(t *testing.T, d *sql.DB) {
	t.Helper()
	exec := func(q string, args ...interface{}) {
		t.Helper()
		if _, err := d.Exec(q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	exec(`INSERT INTO portal_users (id, username, password_hash, is_admin, headscale_user_id)
	      VALUES (1, 'skyadmin', 'x', 1, 1)`)
	exec(`INSERT INTO node_owner_map (node_id, headscale_user_id, username, tag, tagged_by_user_id, tagged_at, hostname)
	      VALUES ('9', 1, 'skyadmin', 'tag:dev-skyadmin-skyworker', 1, 0, 'skyworker')`)
	exec(`INSERT INTO node_owner_map (node_id, headscale_user_id, username, tag, tagged_by_user_id, tagged_at, hostname)
	      VALUES ('3', 85, 'infra', 'tag:dev-infra-emilia', 1, 0, 'emilia')`)
	exec(`INSERT INTO node_owner_map (node_id, headscale_user_id, username, tag, tagged_by_user_id, tagged_at, hostname)
	      VALUES ('11', 85, 'infra', 'tag:dev-infra-karolina', 1, 0, 'karolina')`)
	now := time.Now().Unix()
	for _, h := range []struct {
		id, host string
	}{{"3", "emilia"}, {"11", "karolina"}} {
		exec(`INSERT INTO exit_node_health (node_id, hostname, online, last_seen, advertised_routes_ok,
		                                    has_exit_tag, state, healthy, last_check_at, last_state_change_at, consecutive_failures)
		      VALUES (?, ?, 1, ?, 1, 1, 'online', 1, ?, ?, 0)`, h.id, h.host, now, now, now)
	}
	// The device's rule: 104.16.0.0/12 through karolina.
	exec(`INSERT INTO device_rules (user_id, device_id, user_name, exit_node_id, target_type, target_value, action, enabled, parent_domain)
	      VALUES (1, 9, 'skyadmin', 'karolina', 'ip', '104.16.0.0/12', 'accept', 1, 'cdn:cloudflare:registry.npmjs.org')`)
}

// stalePolicyForB276 is the live policy from BEFORE the ownership change: the pin
// names emilia (the old owner) while the current rules/table say karolina.
func stalePolicyForB276(t *testing.T) string {
	t.Helper()
	return `{
  "grants": [
    { "src": ["tag:dev-skyadmin-skyworker"], "dst": ["h-rule-104-16-0-0-12"], "ip": ["*"], "via": ["tag:dev-infra-emilia"] }
  ],
  "hosts": { "h-rule-104-16-0-0-12": "104.16.0.0/12" },
  "tagOwners": {}
}`
}

// TestB276_OwnershipChangeReappliesACL is the regression test for the live
// failure: the assignment moved to another relay and the ACL was never
// regenerated, so every device kept a pin to a relay that no longer served the
// prefix.
func TestB276_OwnershipChangeReappliesACL(t *testing.T) {
	t.Setenv("SKYGATE_ACL_VIA_ENABLED", "true")
	ownershipACLMu.Lock()
	ownershipACLLastRun = time.Time{}
	ownershipACLMu.Unlock()
	t.Cleanup(func() {
		ownershipACLMu.Lock()
		ownershipACLLastRun = time.Time{}
		ownershipACLMu.Unlock()
	})

	d := newB276DB(t)
	seedB276(t, d)
	stub := &b276Stub{served: stalePolicyForB276(t)}
	srv := stub.server(t)
	hs := headscale.New(srv.URL, "test-token")

	// The table currently claims emilia; the rules say karolina, so the pass must
	// move the prefix — and therefore re-apply the ACL.
	if _, err := d.Exec(`INSERT INTO prefix_owner (prefix, exit_node_id, source, claims, devices, updated_at)
	                     VALUES ('104.16.0.0/12', 'emilia', 'explicit', 1, 1, 0)`); err != nil {
		t.Fatalf("seed prefix_owner: %v", err)
	}

	s := &Service{DB: skygatedb.FixedDBSource{DB: d}, HS: hs}
	if _, chg, err := s.reconcilePrefixOwnership(); err != nil {
		t.Fatalf("reconcilePrefixOwnership: %v", err)
	} else if chg == 0 {
		t.Fatalf("expected the ownership pass to move 104.16.0.0/12 from emilia to karolina, got chg=0")
	}

	if got := stub.putCount(); got != 1 {
		t.Fatalf("policy PUTs = %d, want 1 (the change MUST be pushed — this is the whole block)", got)
	}
	// The table must have moved, otherwise the assertion below would be about a
	// stale owner for the wrong reason.
	var owner string
	if err := d.QueryRow(`SELECT exit_node_id FROM prefix_owner WHERE prefix='104.16.0.0/12'`).Scan(&owner); err != nil {
		t.Fatalf("read back the assignment: %v", err)
	}
	if owner != "karolina" {
		t.Fatalf("assignment owner = %q, want karolina (the current rule's relay)", owner)
	}
	pushed := stub.lastPut()
	if !strings.Contains(pushed, `"h-rule-104-16-0-0-12"`) {
		t.Fatalf("pushed policy does not mention the rule alias:\n%s", pushed)
	}
	if !strings.Contains(pushed, `"via":["tag:dev-infra-karolina"]`) && !strings.Contains(pushed, `"via": ["tag:dev-infra-karolina"]`) {
		t.Errorf("pushed policy still does not pin 104.16.0.0/12 to the new owner (karolina):\n%s", pushed)
	}
	if strings.Contains(pushed, `"via":["tag:dev-infra-emilia"]`) {
		t.Errorf("pushed policy still pins the prefix to the OLD owner (emilia):\n%s", pushed)
	}
}

// TestB276_NoChangeMeansNoWrite: once the table matches the rules and headscale
// serves the generated policy, a pass must not touch the policy API — the sync runs
// every few minutes and on a file-mode host every write restarts headscale.
func TestB276_NoChangeMeansNoWrite(t *testing.T) {
	t.Setenv("SKYGATE_ACL_VIA_ENABLED", "true")
	ownershipACLMu.Lock()
	ownershipACLLastRun = time.Time{}
	ownershipACLMu.Unlock()
	t.Cleanup(func() {
		ownershipACLMu.Lock()
		ownershipACLLastRun = time.Time{}
		ownershipACLMu.Unlock()
	})

	d := newB276DB(t)
	seedB276(t, d)
	stub := &b276Stub{}
	srv := stub.server(t)
	hs := headscale.New(srv.URL, "test-token")
	s := &Service{DB: skygatedb.FixedDBSource{DB: d}, HS: hs}

	// First pass: the table is empty → the engine seeds it (chg>0 with inserts) and
	// pushes the generated policy.
	if _, _, err := s.reconcilePrefixOwnership(); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	puts := stub.putCount()

	// The stub now serves exactly what skygate pushed, and the table matches the
	// rules → the second pass must be quiet.
	ownershipACLMu.Lock()
	ownershipACLLastRun = time.Time{} // ignore the throttle for this assertion
	ownershipACLMu.Unlock()
	if _, chg, err := s.reconcilePrefixOwnership(); err != nil {
		t.Fatalf("second reconcile: %v", err)
	} else if chg != 0 {
		t.Fatalf("second pass changed %d assignment(s), want 0 (the table is already correct)", chg)
	}
	if got := stub.putCount(); got != puts {
		t.Errorf("policy PUTs = %d after a no-op pass, want %d (a pass with nothing to change must not write)", got, puts)
	}
}

// TestB276_ThrottleAbsorbsAChurningTable: the domain auto-updater rewrites derived
// rows every few minutes, so a churning table must not turn into an apply storm.
func TestB276_ThrottleAbsorbsAChurningTable(t *testing.T) {
	t.Setenv("SKYGATE_ACL_VIA_ENABLED", "true")
	ownershipACLMu.Lock()
	ownershipACLLastRun = time.Now() // pretend an apply just ran
	ownershipACLMu.Unlock()
	t.Cleanup(func() {
		ownershipACLMu.Lock()
		ownershipACLLastRun = time.Time{}
		ownershipACLMu.Unlock()
	})

	d := newB276DB(t)
	seedB276(t, d)
	stub := &b276Stub{served: stalePolicyForB276(t)}
	srv := stub.server(t)
	s := &Service{DB: skygatedb.FixedDBSource{DB: d}, HS: headscale.New(srv.URL, "test-token")}

	s.applyACLAfterOwnershipChange(0, 3)

	if got := stub.putCount(); got != 0 {
		t.Errorf("policy PUTs = %d, want 0 — the throttle must absorb the churn and defer to the next pass", got)
	}
}
