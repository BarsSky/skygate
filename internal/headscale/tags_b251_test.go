// tags_b251_test.go — B251 / B245 tests for EnsureTagOwner
// against HuJSON-formatted headscale policies.
//
// Background: headscale 0.29 returns its policy in HuJSON
// (unquoted keys, comments allowed, trailing commas). The
// pre-B251 code used `encoding/json` directly, which failed
// on the unquoted-key format with:
//   `cannot unmarshal string into Go value of type map[string]interface {}`
//
// B251 wraps the raw policy bytes in hujson.Standardize()
// before json.Unmarshal. These tests pin that contract by
// serving a HuJSON-shaped policy via httptest.

package headscale

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// TestEnsureTagOwner_B251_HuJSONPolicy mirrors TestEnsureTagOwner_AddsWhenMissing
// but serves the policy in HuJSON (unquoted keys) — the format
// headscale 0.29 actually returns. Pre-B251 this failed with
// `cannot unmarshal string into Go value of type map[string]interface {}`.
func TestEnsureTagOwner_B251_HuJSONPolicy(t *testing.T) {
	var policyMu sync.Mutex
	policy := map[string]interface{}{
		"acls":      []interface{}{},
		"tagOwners": map[string]interface{}{},
	}
	var putCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/v1/policy":
			policyMu.Lock()
			defer policyMu.Unlock()
			// The HTTP body itself is valid JSON ({"policy": "..."})
			// so c.do's json.Decode passes. The inner policy string
			// is HuJSON — that's the payload GetACL caches and that
			// EnsureTagOwner standardizes via hujson.Standardize().
			// Pre-B251 EnsureTagOwner json.Unmarshal'd the string
			// directly, which failed on unquoted keys.
			hujsonInner := `{
  /* headscale 0.29 emits HuJSON — comments tolerated,
     trailing commas tolerated. Keys are quoted. */
  "acls": [],
  "tagOwners": {},
  "hosts": {},
  "groups": {},
}`
			// json.Marshal of a string produces a JSON string literal,
			// so the outer {"policy": "..."} is always valid JSON.
			outer, _ := json.Marshal(map[string]string{"policy": hujsonInner})
			w.Header().Set("Content-Type", "application/json")
			w.Write(outer)
		case r.Method == "PUT" && r.URL.Path == "/api/v1/policy":
			policyMu.Lock()
			defer policyMu.Unlock()
			putCount++
			var pb struct {
				Policy json.RawMessage `json:"policy"`
			}
			if err := json.NewDecoder(r.Body).Decode(&pb); err != nil {
				http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
				return
			}
			raw := bytes.TrimSpace(pb.Policy)
			if len(raw) == 0 {
				http.Error(w, "empty policy body", http.StatusBadRequest)
				return
			}
			if raw[0] == '"' {
				var s string
				if err := json.Unmarshal(raw, &s); err != nil {
					http.Error(w, "bad stringified policy: "+err.Error(), http.StatusBadRequest)
					return
				}
				if err := json.Unmarshal([]byte(s), &policy); err != nil {
					http.Error(w, "bad policy hujson: "+err.Error(), http.StatusBadRequest)
					return
				}
			} else {
				if err := json.Unmarshal(raw, &policy); err != nil {
					http.Error(w, "bad policy json: "+err.Error(), http.StatusBadRequest)
					return
				}
			}
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"policy":"ok"}`))
		default:
			http.Error(w, "unexpected: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "fake-token")

	if err := c.EnsureTagOwner(
		"tag:dev-infra-skygate-host",
		[]string{"infra@tsnet.skynas.ru", "tagged-devices@tsnet.skynas.ru"},
	); err != nil {
		t.Fatalf("EnsureTagOwner against HuJSON policy: %v", err)
	}
	if putCount != 1 {
		t.Errorf("PUT count = %d, want 1 (B251 must pre-populate tagOwners for the new tag)", putCount)
	}
	policyMu.Lock()
	to, _ := policy["tagOwners"].(map[string]interface{})
	policyMu.Unlock()
	if _, ok := to["tag:dev-infra-skygate-host"]; !ok {
		t.Errorf("tagOwners missing tag:dev-infra-skygate-host after EnsureTagOwner; got: %v", to)
	}
}

// TestEnsureTagOwner_B251_PreservesExistingTagOwners ensures the
// hujson rewrite doesn't drop pre-existing tagOwners entries
// while adding the new one. Pre-fix regression that bit the
// agent VM on 2026-09-15 (every AddTag failure for skygate-host-1-1
// was caused by tagOwners wiping itself between ticks).
func TestEnsureTagOwner_B251_PreservesExistingTagOwners(t *testing.T) {
	var policyMu sync.Mutex
	policy := map[string]interface{}{
		"acls": []interface{}{},
		"tagOwners": map[string]interface{}{
			"tag:dev-skyadmin-emilia": []interface{}{"skyadmin@tsnet.skynas.ru"},
			"tag:dev-infra-emilia":    []interface{}{"infra@tsnet.skynas.ru"},
		},
	}
	var putCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/v1/policy":
			policyMu.Lock()
			defer policyMu.Unlock()
			hujsonPolicy := `{
  "acls": [],
  "tagOwners": {
    "tag:dev-skyadmin-emilia": ["skyadmin@tsnet.skynas.ru"],
    "tag:dev-infra-emilia":    ["infra@tsnet.skynas.ru"],
  },
  "hosts": {},
  "groups": {},
}`
			outer, _ := json.Marshal(map[string]string{"policy": hujsonPolicy})
			w.Header().Set("Content-Type", "application/json")
			w.Write(outer)
		case r.Method == "PUT" && r.URL.Path == "/api/v1/policy":
			policyMu.Lock()
			defer policyMu.Unlock()
			putCount++
			var pb struct {
				Policy json.RawMessage `json:"policy"`
			}
			if err := json.NewDecoder(r.Body).Decode(&pb); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			raw := bytes.TrimSpace(pb.Policy)
			if raw[0] == '"' {
				var s string
				_ = json.Unmarshal(raw, &s)
				_ = json.Unmarshal([]byte(s), &policy)
			} else {
				_ = json.Unmarshal(raw, &policy)
			}
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"policy":"ok"}`))
		default:
			http.Error(w, "unexpected: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "fake-token")
	if err := c.EnsureTagOwner(
		"tag:dev-infra-skygate-host",
		[]string{"infra@tsnet.skynas.ru"},
	); err != nil {
		t.Fatalf("EnsureTagOwner: %v", err)
	}
	if putCount != 1 {
		t.Errorf("PUT count = %d, want 1", putCount)
	}
	policyMu.Lock()
	defer policyMu.Unlock()
	to, _ := policy["tagOwners"].(map[string]interface{})
	if _, ok := to["tag:dev-skyadmin-emilia"]; !ok {
		t.Errorf("pre-existing tag:dev-skyadmin-emilia dropped after EnsureTagOwner; got: %v", to)
	}
	if _, ok := to["tag:dev-infra-emilia"]; !ok {
		t.Errorf("pre-existing tag:dev-infra-emilia dropped after EnsureTagOwner; got: %v", to)
	}
	if _, ok := to["tag:dev-infra-skygate-host"]; !ok {
		t.Errorf("new tag:dev-infra-skygate-host missing after EnsureTagOwner; got: %v", to)
	}
	// Sanity: EnsureTagOwner must NOT add hostnames that aren't
	// in the requested owners slice (the B251 reserved-name
	// contract — `skygate-host` hostname belongs to `infra`).
	for k := range to {
		if !strings.HasPrefix(k, "tag:") {
			t.Errorf("tagOwners contains non-tag key %q", k)
		}
	}
}
// TestEnsureTagOwner_B251_StringifiedPolicy covers the actual
// failure mode that bit the skygate-host-1-1 autoupdater on
// 2026-09-15: headscale's /api/v1/policy returns the policy
// inside a string field (`"policy": "{...escaped JSON...}"`)
// rather than as a top-level JSON object. Pre-B251 EnsureTagOwner
// passed those bytes straight to `json.Unmarshal(p, &map[string]interface{})`
// which crashed with:
//   `cannot unmarshal string into Go value of type map[string]interface {}`
// (the live skygate stderr: `ensure-tag-owner: parse ACL (got 61854 bytes): json: cannot unmarshal string into Go value of type map[string]interface {}`).
//
// The B251 fix unquotes the stringified form first via
// unquotePolicyIfStringified before passing the unwrapped
// bytes to hujson.Standardize + json.Unmarshal.
func TestEnsureTagOwner_B251_StringifiedPolicy(t *testing.T) {
	var policyMu sync.Mutex
	policy := map[string]interface{}{
		"acls": []interface{}{},
		"tagOwners": map[string]interface{}{
			"tag:dev-skyadmin-emilia": []interface{}{"skyadmin@tsnet.skynas.ru"},
		},
	}
	var putCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/v1/policy":
			policyMu.Lock()
			defer policyMu.Unlock()
			// Return the policy wrapped in `{"policy": "..."}`
			// — JSON object with a STRING `policy` field. This is
			// what GetACL's c.do() yields when the headscale API's
			// `Policy` field carries a stringified blob (the legacy
			// wire format). c.do decodes this fine (Policy is
			// json.RawMessage), but the value it caches is the raw
			// `"...escaped..."` string with quotes — that's what
			// EnsureTagOwner has to unwrap.
			inner, _ := json.Marshal(policy)
			outer, _ := json.Marshal(map[string]string{"policy": string(inner)})
			w.Header().Set("Content-Type", "application/json")
			w.Write(outer)
		case r.Method == "PUT" && r.URL.Path == "/api/v1/policy":
			policyMu.Lock()
			defer policyMu.Unlock()
			putCount++
			var pb struct {
				Policy json.RawMessage `json:"policy"`
			}
			if err := json.NewDecoder(r.Body).Decode(&pb); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			raw := bytes.TrimSpace(pb.Policy)
			if raw[0] == '"' {
				var s string
				_ = json.Unmarshal(raw, &s)
				_ = json.Unmarshal([]byte(s), &policy)
			} else {
				_ = json.Unmarshal(raw, &policy)
			}
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"policy":"ok"}`))
		default:
			http.Error(w, "unexpected: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "fake-token")
	if err := c.EnsureTagOwner(
		"tag:dev-infra-skygate-host",
		[]string{"infra@tsnet.skynas.ru", "tagged-devices@tsnet.skynas.ru"},
	); err != nil {
		t.Fatalf("EnsureTagOwner against stringified policy: %v", err)
	}
	if putCount != 1 {
		t.Errorf("PUT count = %d, want 1", putCount)
	}
	policyMu.Lock()
	to, _ := policy["tagOwners"].(map[string]interface{})
	policyMu.Unlock()
	if _, ok := to["tag:dev-infra-skygate-host"]; !ok {
		t.Errorf("tagOwners missing tag:dev-infra-skygate-host; got: %v", to)
	}
	if _, ok := to["tag:dev-skyadmin-emilia"]; !ok {
		t.Errorf("pre-existing tag:dev-skyadmin-emilia dropped; got: %v", to)
	}
}
