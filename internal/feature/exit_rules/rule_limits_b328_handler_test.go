// rule_limits_b328_handler_test.go — B328, the handler-level half.
//
// The pure test in rule_limits_b328_test.go pins the LADDER. This one pins the CALL
// SITE, because the operator's bug was a call site: `PostMyExitRule` asked the ladder
// for "this device" and handed it the system-wide number. A test that only exercises
// the new helper would keep passing if someone wired the system count back in.
//
// The fixture is the operator's own shape — 500 enabled rows system-wide, 1 counted row
// on device 56 — and the handler must INSERT the rule instead of flashing
// «device limit exceeded: 500/500 user-facing rules on this device».
package exit_rules

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"skygate/internal/auth"
	"skygate/internal/config"
	skygatedb "skygate/internal/db"
	"skygate/internal/headscale"
)

// b328Backend satisfies the exit_rules Backend interface for tests: it returns a
// fixed Claims and no-ops the rendering methods (the handler under test redirects).
type b328Backend struct{ claims *auth.Claims }

func (b *b328Backend) Render(w http.ResponseWriter, r *http.Request, name string, data any) {}
func (b *b328Backend) RenderWithLayout(w http.ResponseWriter, r *http.Request, name string, c *auth.Claims, data map[string]any) {
}
func (b *b328Backend) CurrentUser(r *http.Request) *auth.Claims { return b.claims }
func (b *b328Backend) Audit(userID int64, username, action, detail string) {
}

// b328NodeStub serves the minimum headscale surface PostMyExitRule touches: the node
// list its ownership check walks.
func b328NodeStub(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/node" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"nodes": []map[string]any{{
					"id":          "56",
					"name":        "cyborg",
					"givenName":   "cyborg",
					"ipAddresses": []string{"100.64.0.20"},
					"online":      true,
					"user":        map[string]any{"id": "1", "name": "skyadmin"},
				}},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestB328_HandlerInsertsWhenTheSystemTotalReachesTheDeviceCap is THE regression on the
// call site: with 500 enabled rows system-wide and one counted row on device 56, the
// per-device cap of 500 must not stop the insert.
func TestB328_HandlerInsertsWhenTheSystemTotalReachesTheDeviceCap(t *testing.T) {
	d := newB328DB(t)
	const targetDevice = 56
	seedB328LiveShape(t, d, targetDevice, 500)
	b328Exec(t, d, `INSERT INTO node_owner_map (node_id, headscale_user_id, username, tag, tagged_by_user_id, tagged_at, hostname)
	                VALUES ('56', 1, 'skyadmin', 'tag:dev-skyadmin-cyborg', 1, 0, 'cyborg')`)

	s := &Service{
		Backend: &b328Backend{claims: &auth.Claims{UserID: 1, Username: "skyadmin", IsAdmin: true}},
		DB:      skygatedb.FixedDBSource{DB: d},
		Cfg:     &config.Config{MaxRulesPerDevice: 500},
		HS:      headscale.New(b328NodeStub(t).URL, "test-token"),
	}

	form := url.Values{
		"device_id":    {strconv.Itoa(targetDevice)},
		"exit_node":    {"karolina"},
		"target_type":  {"ip"},
		"target_value": {"192.0.2.0/24"},
		"action":       {"accept"},
	}
	req := httptest.NewRequest(http.MethodPost, "/my/exit-rules", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	s.PostMyExitRule(rec, req)

	// The refusal the operator saw, in either of the two shapes this handler uses
	// (redirect-with-flash, or a raw error body).
	refusal := rec.Header().Get("Location") + " " + rec.Body.String()
	for _, needle := range []string{"device limit exceeded", "device+limit+exceeded"} {
		if strings.Contains(refusal, needle) {
			t.Fatalf("the per-device cap still refused the insert (%q)\n  response: %d %s", needle, rec.Code, refusal)
		}
	}

	if rec.Code != http.StatusFound {
		t.Fatalf("handler answered %d, want a 302 redirect; body: %s", rec.Code, rec.Body.String())
	}

	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM device_rules
	                      WHERE user_id = 1 AND device_id = ? AND target_value = '192.0.2.0/24'`,
		targetDevice).Scan(&n); err != nil {
		t.Fatalf("count inserted rule: %v", err)
	}
	if n != 1 {
		t.Fatalf("the rule was not inserted (count=%d); redirect: %s", n, rec.Header().Get("Location"))
	}
}
