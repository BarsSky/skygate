// internal/feature/exit_rules/sync_b288_test.go — B288 (2026-09-22).
//
// The banner the operator kept seeing. `applyACLIfDrifted` was documented as the
// single "make the control plane match the database" step, but the only trigger
// was `chg > 0` — an ownership FLIP. On a healthy install the assignment table is
// stable, so a live policy written by an older generator (or one whose apply
// failed once) stayed stale FOREVER, and /admin/exit-nodes kept saying
// «политика headscale УСТАРЕЛА» while every rule page said the setup was correct.
// Live on `aro`: the live document carried 16 duplicated grants and two extra tag
// declarations — nothing was wrong with the routing, and nothing was ever going
// to change it either.
//
// The tests below drive `reconcilePrefixOwnership` (the entry point both sync
// paths use) against a real database and a policy stub.
package exit_rules

import (
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

// b288Stub is a headscale policy endpoint that counts reads as well as writes, so
// the throttle can be observed (a converged host must not even read the policy
// more than once per interval).
type b288Stub struct {
	mu     sync.Mutex
	served string
	puts   []string
	gets   int
}

func (s *b288Stub) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/policy" {
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
			return
		}
		switch r.Method {
		case http.MethodGet:
			s.mu.Lock()
			served := s.served
			s.gets++
			s.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"policy": served})
		case http.MethodPut:
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
			http.Error(w, "unexpected "+r.Method, http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (s *b288Stub) putCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.puts)
}

func (s *b288Stub) getCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gets
}

func (s *b288Stub) setServed(policy string) {
	s.mu.Lock()
	s.served = policy
	s.mu.Unlock()
}

// b288ResetThrottles clears both throttles so a test's passes are not swallowed by
// state another test left behind.
func b288ResetThrottles(t *testing.T) {
	t.Helper()
	ownershipACLMu.Lock()
	ownershipACLLastRun = time.Time{}
	ownershipACLMu.Unlock()
	periodicDriftMu.Lock()
	periodicDriftLastRun = time.Time{}
	periodicDriftMu.Unlock()
	t.Cleanup(func() {
		ownershipACLMu.Lock()
		ownershipACLLastRun = time.Time{}
		ownershipACLMu.Unlock()
		periodicDriftMu.Lock()
		periodicDriftLastRun = time.Time{}
		periodicDriftMu.Unlock()
	})
}

// stalePolicyB288 is a parseable policy that is NOT what the database implies —
// the shape the live `aro` document had (an extra duplicated grant).
func stalePolicyB288() string {
	return `{
  "grants": [
    { "src": ["tag:dev-skyadmin-skyworker"], "dst": ["h-rule-104-16-0-0-12"], "ip": ["*"], "via": ["tag:dev-infra-karolina"] },
    { "src": ["tag:dev-skyadmin-skyworker"], "dst": ["h-rule-104-16-0-0-12"], "ip": ["*"], "via": ["tag:dev-infra-karolina"] }
  ],
  "hosts": { "h-rule-104-16-0-0-12": "104.16.0.0/12" },
  "tagOwners": { "tag:dev-skyadmin-skyworker": ["skyadmin@ts.example.com"] }
}`
}

// TestB288_StableTableStillHealsDrift is the regression for the operator's
// report: the assignment table does not move, the live policy is stale — the pass
// must still regenerate and push.
func TestB288_StableTableStillHealsDrift(t *testing.T) {
	t.Setenv("SKYGATE_ACL_VIA_ENABLED", "true")
	t.Setenv("SKYGATE_BASE_DOMAIN", "ts.example.com")
	t.Setenv("SKYGATE_ADMIN_USER", "skyadmin")
	b288ResetThrottles(t)

	d := newB276DB(t)
	seedB276(t, d)
	stub := &b288Stub{served: stalePolicyB288()}
	srv := stub.server(t)
	hs := headscale.New(srv.URL, "test-token")
	s := &Service{DB: skygatedb.FixedDBSource{DB: d}, HS: hs}

	// First pass: the assignment table is empty and the rules already name the
	// owner, so the ownership engine reports NO change. Pre-B288 that alone meant
	// "do not touch the ACL" — even though headscale was serving a stale document
	// (the apply was gated on a table CHANGE, which a healthy host never has).
	if _, chg, err := s.reconcilePrefixOwnership(); err != nil {
		t.Fatalf("first reconcile: %v", err)
	} else if chg != 0 {
		t.Fatalf("first pass moved the assignment table (chg=%d) — the test must exercise the STABLE path", chg)
	}
	if got := stub.putCount(); got != 1 {
		t.Fatalf("first pass PUTs = %d, want 1: a stable assignment table plus a stale live policy must still "+
			"re-apply (before B288 the check only ran when the table moved, so this case never applied)", got)
	}

	// Simulate the live `aro` state: the table now matches the rules (so the
	// ownership engine reports NO change) while headscale serves an older
	// document. Before B288 nothing re-applied it — ever.
	b288ResetThrottles(t)
	stub.setServed(stalePolicyB288())
	before := stub.putCount()

	_, chg, err := s.reconcilePrefixOwnership()
	if err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if chg != 0 {
		t.Fatalf("the second pass moved the assignment table (chg=%d) — this test must exercise the STABLE table path", chg)
	}
	if got := stub.putCount(); got != before+1 {
		t.Fatalf("policy PUTs = %d, want %d: a pass with a stable table and a stale live policy must re-apply "+
			"(that is the whole of B288 — otherwise the «политика УСТАРЕЛА» banner is permanent)", got, before+1)
	}
	if strings.Contains(stub.puts[len(stub.puts)-1], `"src":["tag:dev-skyadmin-skyworker"]`+"\n") {
		t.Errorf("the pushed document looks like the stale one:\n%s", stub.puts[len(stub.puts)-1])
	}
}

// TestB288_ConvergedHostWritesNothingAndChecksOncePerInterval: the safety half.
// Once headscale serves the generated policy, a pass must issue no write, and the
// periodic check must not even read the policy more than once per interval (it
// runs unattended on every sync pass).
func TestB288_ConvergedHostWritesNothingAndChecksOncePerInterval(t *testing.T) {
	t.Setenv("SKYGATE_ACL_VIA_ENABLED", "true")
	t.Setenv("SKYGATE_BASE_DOMAIN", "ts.example.com")
	t.Setenv("SKYGATE_ADMIN_USER", "skyadmin")
	b288ResetThrottles(t)

	d := newB276DB(t)
	seedB276(t, d)
	stub := &b288Stub{served: stalePolicyB288()}
	srv := stub.server(t)
	hs := headscale.New(srv.URL, "test-token")
	s := &Service{DB: skygatedb.FixedDBSource{DB: d}, HS: hs}

	// Pass 1 seeds + applies; the stub now serves exactly what skygate pushed.
	if _, _, err := s.reconcilePrefixOwnership(); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	puts := stub.putCount()

	// Pass 2: converged, the throttle is clear → the check runs, compares, and
	// must NOT write.
	b288ResetThrottles(t)
	if _, chg, err := s.reconcilePrefixOwnership(); err != nil {
		t.Fatalf("second reconcile: %v", err)
	} else if chg != 0 {
		t.Fatalf("second pass changed the table (chg=%d)", chg)
	}
	if got := stub.putCount(); got != puts {
		t.Fatalf("a converged host wrote the policy again (%d PUTs, want %d) — on a file-mode install every write restarts headscale", got, puts)
	}
	getsAfterCheck := stub.getCount()

	// Pass 3: inside the interval the periodic check must be skipped entirely.
	if _, _, err := s.reconcilePrefixOwnership(); err != nil {
		t.Fatalf("third reconcile: %v", err)
	}
	if got := stub.putCount(); got != puts {
		t.Fatalf("pass 3 wrote the policy (%d PUTs, want %d)", got, puts)
	}
	if got := stub.getCount(); got != getsAfterCheck {
		t.Errorf("policy reads = %d after an in-interval pass, want %d: the periodic drift check must respect its throttle", got, getsAfterCheck)
	}
}
