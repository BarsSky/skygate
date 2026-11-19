package startup

// boot_page_b503_test.go — B503 (2026-11-20)
//
// The socket is bound at the top of main (B269) so the updater can verify the build
// of a booting process, which means a browser can reach the real port before the
// real mux exists. The operator's screenshot after an update was that window: the
// raw provisional /healthz JSON rendered in place of /admin/update.
//
// These tests pin the CONNEGOTIATION, not the styling: a browser navigation gets
// the page, a machine endpoint gets the JSON — including when the machine endpoint
// is requested WITH a text/html Accept header, which is the case that would break
// the updater.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func b503Req(method, path, accept string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	if accept != "" {
		r.Header.Set("Accept", accept)
	}
	return r
}

// TestB503_BrowserGetsThePage — the defect itself.
func TestB503_BrowserGetsThePage(t *testing.T) {
	ResetForTest("v1.5.247")
	Enter("headscale-client")

	rec := httptest.NewRecorder()
	StageHandler()(rec, b503Req(http.MethodGet, "/admin/update",
		"text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"))

	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html — a browser navigation must not be shown raw JSON", ct)
	}
	body := rec.Body.String()
	if strings.Contains(body, `"phase":`) {
		t.Error("the HTML page still contains the JSON document")
	}
	for _, want := range []string{"Панель перезапускается", "headscale-client", "/healthz"} {
		if !strings.Contains(body, want) {
			t.Errorf("page is missing %q", want)
		}
	}
	if !strings.Contains(body, `http-equiv="refresh"`) {
		t.Error("page does not refresh itself — the operator would have to guess when to reload")
	}
}

// TestB503_MachineEndpointsStayJSON — the updater's decision must not depend on
// anything a browser could send.
func TestB503_MachineEndpointsStayJSON(t *testing.T) {
	ResetForTest("v1.5.247")
	for _, path := range []string{"/healthz", "/readyz", "/metrics", "/api/v1/nodes", "/oidc/jwks.json"} {
		rec := httptest.NewRecorder()
		StageHandler()(rec, b503Req(http.MethodGet, path, "text/html"))
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Errorf("%s with Accept: text/html answered %q — a machine endpoint must never render HTML", path, ct)
		}
		var m map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
			t.Errorf("%s body is not JSON: %v", path, err)
		}
		if m["build"] != "v1.5.247" {
			t.Errorf("%s build = %v, want the build the updater verifies", path, m["build"])
		}
	}
}

// TestB503_NonBrowserClientsStayJSON: curl sends `*/*`, and the Go updater sends no
// Accept at all. Neither may be switched to HTML.
func TestB503_NonBrowserClientsStayJSON(t *testing.T) {
	ResetForTest("v1.5.247")
	for _, accept := range []string{"*/*", "", "application/json", "text/plain"} {
		rec := httptest.NewRecorder()
		StageHandler()(rec, b503Req(http.MethodGet, "/admin/update", accept))
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Errorf("Accept %q answered %q, want JSON", accept, ct)
		}
	}
}

// TestB503_FatalBootSaysWhyAndDoesNotRefresh: a fatal boot keeps answering 503 and
// the page names the reason — a self-refreshing page over a permanent failure is a
// lie that never stops.
func TestB503_FatalBootSaysWhyAndDoesNotRefresh(t *testing.T) {
	ResetForTest("v1.5.247")
	SetFatal("db: open sqlite:/var/lib/skygate/skygate.db: permission denied")

	rec := httptest.NewRecorder()
	StageHandler()(rec, b503Req(http.MethodGet, "/", "text/html"))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 for a fatal boot", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "permission denied") {
		t.Error("the page does not quote the startup error")
	}
	if strings.Contains(body, `http-equiv="refresh"`) {
		t.Error("a fatal boot still auto-refreshes — it would loop forever")
	}
	if !strings.Contains(body, "journalctl") {
		t.Error("the page does not tell the operator where the log is")
	}
}

// TestB503_HeadAndMethods — HEAD gets the page's headers (and no body), and a
// non-GET is never answered with an HTML body.
func TestB503_HeadAndMethods(t *testing.T) {
	ResetForTest("v1.5.247")
	rec := httptest.NewRecorder()
	StageHandler()(rec, b503Req(http.MethodHead, "/admin/update", "text/html"))
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("HEAD Content-Type = %q, want text/html", ct)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("HEAD returned %d body bytes", rec.Body.Len())
	}
	rec = httptest.NewRecorder()
	StageHandler()(rec, b503Req(http.MethodPost, "/admin/update", "text/html"))
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("POST Content-Type = %q, want JSON", ct)
	}
}

// TestB503_PathRules pins the pure negotiation table.
func TestB503_PathRules(t *testing.T) {
	cases := []struct {
		name, method, path, accept string
		want                       bool
	}{
		{"browser on a panel page", "GET", "/admin/update", "text/html", true},
		{"browser on the root", "GET", "/", "text/html,application/xhtml+xml", true},
		{"browser on healthz", "GET", "/healthz", "text/html", false},
		{"browser on readyz", "GET", "/readyz", "text/html", false},
		{"browser on an api route", "GET", "/api/v1/node", "text/html", false},
		{"curl on a panel page", "GET", "/admin/update", "*/*", false},
		{"no accept header", "GET", "/admin/update", "", false},
		{"form post", "POST", "/admin/login", "text/html", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := WantsBootPage(b503Req(tc.method, tc.path, tc.accept)); got != tc.want {
				t.Errorf("WantsBootPage(%s %s, %q) = %v, want %v", tc.method, tc.path, tc.accept, got, tc.want)
			}
		})
	}
	if WantsBootPage(nil) {
		t.Error("WantsBootPage(nil) = true")
	}
}
