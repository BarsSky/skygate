// Tests for the v0.11.1 runtime renderer. Three test groups:
//
//   1. Pure renderer: renderHeadscaleConfig + renderHeadscaleCompose
//      with various env + config inputs. Verifies the
//      substitution + YAML list rendering + headplane-strip
//      logic without touching docker.
//
//   2. Docker orchestration: applyHeadscale / applyBundledDERP /
//      applyHeadplane. Uses a fakeDocker to capture the
//      docker invocations and assert on them.
//
//   3. URL probe: probeDerpURL + probeAllDerps. Uses
//      httptest.NewServer to return canned derpmap.json
//      responses.

package handlers

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"skygate/internal/db"
)

// ---------- 1. Pure renderer ----------

// makeTestRenderer copies the two headscale templates into
// a temp dir and returns a renderer rooted there. Tests
// that touch renderHeadscaleConfig / renderHeadscaleCompose
// need this so the renderer's readTemplate finds the
// fixtures (the production renderer reads
// /app/deploy/templates/).
func makeTestRenderer(t *testing.T) *renderer {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{
		"headscale-config.yaml.tmpl",
		"headscale-compose.yml.tmpl",
	} {
		// Try to read from the real deploy dir first
		// (the dev workspace has them). Fall back to
		// writing a minimal template so the test is
		// self-contained.
		src, err := os.ReadFile(filepath.Join("..", "..", "deploy", "templates", name))
		if err == nil {
			if err := os.WriteFile(filepath.Join(dir, name), src, 0o644); err != nil {
				t.Fatalf("write %s: %v", name, err)
			}
			continue
		}
		// Minimal fallback template. The renderer's
		// contract is "expand ${VAR} + replace
		// __MARKER__"; we don't care about the rest
		// of the file content.
		var content string
		switch name {
		case "headscale-config.yaml.tmpl":
			content = "server_url: ${SKYGATE_CONTROL_URL}\nderp:\n  urls:\n__HEADSCALE_DERP_URLS__\npolicy:\n  auto_approve:\n    routes:\n__HEADSCALE_AUTO_APPROVE_ROUTES__\n"
		case "headscale-compose.yml.tmpl":
			content = "services:\n  headscale:\n    image: headscale/headscale:0.29.1\n  headplane:\n    image: ${HEADPLANE_IMAGE}\nvolumes:\n  headscale_data:\n  headplane_data:\n"
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return &renderer{templatesDir: dir}
}

// TestRenderHeadscaleConfig_BasicSubstitution: ${VAR} is
// replaced from os.Getenv; missing env is preserved.
func TestRenderHeadscaleConfig_BasicSubstitution(t *testing.T) {
	t.Setenv("SKYGATE_CONTROL_URL", "https://head.example.com")
	t.Setenv("HEADSCALE_BASE_DOMAIN", "tsnet.example.com")
	rndr := makeTestRenderer(t)
	cfg := &db.IntegrationConfig{
		DERPExternalURLs: []string{"https://derp1.example.com"},
	}
	got, err := rndr.renderHeadscaleConfig(cfg)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(got, "server_url: https://head.example.com") {
		t.Errorf("server_url not substituted: %q", got)
	}
	if !strings.Contains(got, "    - https://derp1.example.com") {
		t.Errorf("external DERP URL not in derp.urls list: %q", got)
	}
}

// TestRenderHeadscaleConfig_PreservesMissingEnv: if an
// env var is unset, the renderer leaves the ${VAR}
// token alone so the operator notices the missing
// variable in the file (matches deploy.sh's
// "leave original on miss" semantics).
func TestRenderHeadscaleConfig_PreservesMissingEnv(t *testing.T) {
	// Use a temp file that has a var that won't be set.
	dir := t.TempDir()
	tmpl := "x: ${DEFINITELY_UNSET_XYZ_123}\n"
	if err := os.WriteFile(filepath.Join(dir, "headscale-config.yaml.tmpl"), []byte(tmpl), 0o644); err != nil {
		t.Fatal(err)
	}
	os.Unsetenv("DEFINITELY_UNSET_XYZ_123")
	rndr := &renderer{templatesDir: dir}
	got, err := rndr.renderHeadscaleConfig(&db.IntegrationConfig{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(got, "${DEFINITELY_UNSET_XYZ_123}") {
		t.Errorf("missing env should be preserved as ${VAR}, got: %q", got)
	}
}

// TestRenderHeadscaleConfig_MultipleDERPURLs: multiple
// external URLs are emitted as one YAML list entry per
// line, in the order they appear in the config.
func TestRenderHeadscaleConfig_MultipleDERPURLs(t *testing.T) {
	rndr := makeTestRenderer(t)
	cfg := &db.IntegrationConfig{
		DERPExternalURLs: []string{
			"https://a.example.com",
			"https://b.example.com",
			"https://c.example.com",
		},
	}
	got, err := rndr.renderHeadscaleConfig(cfg)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{
		"    - https://a.example.com",
		"    - https://b.example.com",
		"    - https://c.example.com",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in rendered config:\n%s", want, got)
		}
	}
}

// TestRenderHeadscaleCompose_KeepsHeadplaneWhenBundled:
// mode=bundled → the headplane service block is present
// in the rendered compose.
func TestRenderHeadscaleCompose_KeepsHeadplaneWhenBundled(t *testing.T) {
	rndr := makeTestRenderer(t)
	cfg := &db.IntegrationConfig{HeadplaneMode: "bundled"}
	got, err := rndr.renderHeadscaleCompose(cfg)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(got, "headplane:") {
		t.Errorf("bundled mode should keep headplane service, got: %q", got)
	}
	if !strings.Contains(got, "headplane_data:") {
		t.Errorf("bundled mode should keep headplane_data volume, got: %q", got)
	}
}

// TestRenderHeadscaleCompose_StripsHeadplaneWhenOff: mode=off
// → the headplane service block AND the headplane_data
// volume are stripped from the rendered compose.
func TestRenderHeadscaleCompose_StripsHeadplaneWhenOff(t *testing.T) {
	rndr := makeTestRenderer(t)
	cfg := &db.IntegrationConfig{HeadplaneMode: "off"}
	got, err := rndr.renderHeadscaleCompose(cfg)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(got, "headplane:") {
		t.Errorf("off mode should strip headplane service, got: %q", got)
	}
	if strings.Contains(got, "headplane_data:") {
		t.Errorf("off mode should strip headplane_data volume, got: %q", got)
	}
}

// TestRenderHeadscaleCompose_StripsHeadplaneWhenExternal:
// mode=external also strips the local sidecar (we point at
// the existing instance — the local one is just clutter).
func TestRenderHeadscaleCompose_StripsHeadplaneWhenExternal(t *testing.T) {
	rndr := makeTestRenderer(t)
	cfg := &db.IntegrationConfig{
		HeadplaneMode:       "external",
		HeadplaneExternalURL: "https://headplane.example.com",
	}
	got, err := rndr.renderHeadscaleCompose(cfg)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(got, "headplane:") {
		t.Errorf("external mode should strip local headplane service, got: %q", got)
	}
}

// TestStripHeadplaneServiceBlock: the pure helper
// (called by renderHeadscaleCompose) returns the
// input unchanged when no headplane block is present.
func TestStripHeadplaneServiceBlock_NoOpWhenAbsent(t *testing.T) {
	in := "services:\n  headscale:\n    image: foo\n"
	if got := stripHeadplaneServiceBlock(in); got != in {
		t.Errorf("no-op expected, got diff:\nbefore: %q\nafter:  %q", in, got)
	}
}

// ---------- 2. Docker orchestration ----------

// fakeDocker is a minimal in-process docker CLI. Each call
// records the (name, args) tuple; the canned return value
// is set per-test by setting the next fields.
type fakeDocker struct {
	calls      []string
	nextStdout string
	nextStderr string
	nextErr    error
	// stdinCalls records the stdin payloads for
	// dockerCmdStdin invocations.
	stdinCalls []string
}

func (f *fakeDocker) cmd(name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	return []byte(f.nextStdout), f.nextErr
}

func (f *fakeDocker) stdin(r io.Reader, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	if r != nil {
		b, _ := io.ReadAll(r)
		f.stdinCalls = append(f.stdinCalls, string(b))
	}
	return []byte(f.nextStdout), f.nextErr
}

// withFakeDocker swaps the package-level dockerCmd /
// dockerCmdStdin for the duration of t.
func withFakeDocker(t *testing.T, f *fakeDocker) {
	t.Helper()
	origCmd := dockerCmd
	origStdin := dockerCmdStdin
	dockerCmd = f.cmd
	dockerCmdStdin = f.stdin
	t.Cleanup(func() {
		dockerCmd = origCmd
		dockerCmdStdin = origStdin
	})
}

// TestApplyHeadscale_PushesAndSIGHUPs: the apply path
// for headscale issues exactly two docker calls in order:
// (1) docker cp <tmpfile> headscale:/etc/headscale/config.yaml
// (the temp file is in the skygate container; the daemon
// pushes it via the docker API), and (2) docker kill -s
// HUP headscale.
func TestApplyHeadscale_PushesAndSIGHUPs(t *testing.T) {
	f := &fakeDocker{nextStdout: ""}
	withFakeDocker(t, f)
	rndr := makeTestRenderer(t)

	// Set the env vars the template references so the
	// render is valid.
	t.Setenv("SKYGATE_CONTROL_URL", "https://head.example.com")

	res := rndr.applyHeadscale(&db.IntegrationConfig{
		DERPExternalURLs: []string{"https://derp1.example.com"},
	})
	if !res.OK {
		t.Fatalf("expected ok, got err=%q steps=%v", res.Err, res.Steps)
	}
	if len(f.calls) != 2 {
		t.Fatalf("expected 2 docker calls, got %d: %v", len(f.calls), f.calls)
	}
	if !strings.HasPrefix(f.calls[0], "docker cp ") {
		t.Errorf("first call should be docker cp, got %q", f.calls[0])
	}
	if !strings.Contains(f.calls[0], "headscale:/etc/headscale/config.yaml") {
		t.Errorf("first call should target headscale config, got %q", f.calls[0])
	}
	if !strings.Contains(f.calls[1], "kill -s HUP headscale") {
		t.Errorf("second call should be docker kill HUP, got %q", f.calls[1])
	}
	// The temp file written by the renderer should
	// contain the rendered body (with the external
	// DERP URL embedded).
	tmpPath := "/tmp/skygate-headscale-config.yaml"
	b, err := os.ReadFile(tmpPath)
	if err != nil {
		// On Windows /tmp/... may not be writable the
		// way it is on Linux. The renderer's contract
		// is "write the file; the daemon reads it";
		// the test contract is "we can read the same
		// path back". If that fails on Windows, fall
		// back to checking the render produced the
		// expected trace (which the assertions above
		// already cover).
		t.Logf("temp file not readable at %s (Windows?): %v — skipping content assertion", tmpPath, err)
		return
	}
	if !strings.Contains(string(b), "https://derp1.example.com") {
		t.Errorf("rendered body missing external URL: %q", b)
	}
	os.Remove(tmpPath)
}

// TestApplyBundledDERP_StartsWhenEnabled: if the derper
// container is not running and BundledDERP=true, the
// apply path calls `docker start derper`.
func TestApplyBundledDERP_StartsWhenEnabled(t *testing.T) {
	f := &fakeDocker{nextStdout: "false"} // inspect says not running
	withFakeDocker(t, f)
	rndr := makeTestRenderer(t)
	res := rndr.applyBundledDERP(&db.IntegrationConfig{BundledDERP: true})
	if !res.OK {
		t.Errorf("expected ok, got err=%q", res.Err)
	}
	// Two calls: inspect + start.
	if len(f.calls) != 2 {
		t.Errorf("expected 2 docker calls, got %d: %v", len(f.calls), f.calls)
	}
	if !strings.HasPrefix(f.calls[0], "docker inspect") {
		t.Errorf("first call should be inspect, got %q", f.calls[0])
	}
	if !strings.HasPrefix(f.calls[1], "docker start derper") {
		t.Errorf("second call should be start derper, got %q", f.calls[1])
	}
}

// TestApplyBundledDERP_StopsWhenDisabled: running derper
// + BundledDERP=false → docker stop derper.
func TestApplyBundledDERP_StopsWhenDisabled(t *testing.T) {
	f := &fakeDocker{nextStdout: "true"} // inspect says running
	withFakeDocker(t, f)
	rndr := makeTestRenderer(t)
	res := rndr.applyBundledDERP(&db.IntegrationConfig{BundledDERP: false})
	if !res.OK {
		t.Errorf("expected ok, got err=%q", res.Err)
	}
	if !strings.HasPrefix(f.calls[1], "docker stop derper") {
		t.Errorf("expected docker stop, got %q", f.calls[1])
	}
}

// TestApplyBundledDERP_NoOpWhenStateMatches: running +
// BundledDERP=true → just inspect, no start (idempotent).
func TestApplyBundledDERP_NoOpWhenStateMatches(t *testing.T) {
	f := &fakeDocker{nextStdout: "true"} // running
	withFakeDocker(t, f)
	rndr := makeTestRenderer(t)
	res := rndr.applyBundledDERP(&db.IntegrationConfig{BundledDERP: true})
	if !res.OK {
		t.Errorf("expected ok, got err=%q", res.Err)
	}
	if len(f.calls) != 1 {
		t.Errorf("expected 1 docker call (inspect), got %d: %v", len(f.calls), f.calls)
	}
}

// TestApplyHeadplane_RemovesWhenExternal: mode=external
// + headplane container running → docker stop + docker rm.
func TestApplyHeadplane_RemovesWhenExternal(t *testing.T) {
	f := &fakeDocker{nextStdout: "true"} // running
	withFakeDocker(t, f)
	rndr := makeTestRenderer(t)
	res := rndr.applyHeadplane(&db.IntegrationConfig{HeadplaneMode: "external"})
	if !res.OK {
		t.Errorf("expected ok, got err=%q", res.Err)
	}
	if len(f.calls) != 3 {
		t.Errorf("expected 3 calls (inspect+stop+rm), got %d: %v", len(f.calls), f.calls)
	}
	if !strings.HasPrefix(f.calls[1], "docker stop headplane") {
		t.Errorf("second call should be stop, got %q", f.calls[1])
	}
	if !strings.HasPrefix(f.calls[2], "docker rm headplane") {
		t.Errorf("third call should be rm, got %q", f.calls[2])
	}
}

// TestApplyAll_PropagatesFailure: a headscale push
// failure short-circuits the derper + headplane stages
// (we don't start/stop containers if the headscale
// config didn't push).
func TestApplyAll_PropagatesFailure(t *testing.T) {
	f := &fakeDocker{
		nextStdout: "",
		nextErr:    errFakeDocker, // pretend docker exec failed
	}
	withFakeDocker(t, f)
	rndr := makeTestRenderer(t)
	res := rndr.applyAll(&db.IntegrationConfig{
		DERPExternalURLs: []string{"https://derp1.example.com"},
		BundledDERP:      true,
		HeadplaneMode:    "bundled",
	})
	if res.OK {
		t.Errorf("expected failure, got ok: %v", res)
	}
	// Only 1 docker call: the failed push. No inspect on
	// derper / headplane because the headscale stage
	// short-circuited.
	if len(f.calls) != 1 {
		t.Errorf("expected 1 call, got %d: %v", len(f.calls), f.calls)
	}
}

// errFakeDocker is a sentinel error used by the fake to
// simulate a docker exec failure.
var errFakeDocker = &fakeErr{msg: "fake docker exec error"}

type fakeErr struct{ msg string }

func (e *fakeErr) Error() string { return e.msg }

// TestApplyAll_SuccessPath: all three sub-applies
// succeed in order.
func TestApplyAll_SuccessPath(t *testing.T) {
	f := &fakeDocker{nextStdout: "true"} // inspect says running (idempotent)
	withFakeDocker(t, f)
	rndr := makeTestRenderer(t)
	t.Setenv("SKYGATE_CONTROL_URL", "https://head.example.com")

	res := rndr.applyAll(&db.IntegrationConfig{
		DERPExternalURLs: []string{"https://derp1.example.com"},
		BundledDERP:      true,  // already running → no start
		HeadplaneMode:    "off", // running → stop + rm
	})
	if !res.OK {
		t.Errorf("expected ok, got err=%q steps=%v", res.Err, res.Steps)
	}
	// Calls: exec(cat), kill HUP, inspect derper (no start),
	// inspect headplane, stop headplane, rm headplane.
	if len(f.calls) < 4 {
		t.Errorf("expected ≥4 calls, got %d: %v", len(f.calls), f.calls)
	}
}

// ---------- 3. URL probe ----------

// TestProbeDerpURL_OK: a server that returns 200 with
// non-empty body is OK.
func TestProbeDerpURL_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{"Regions":{}}`))
	}))
	defer srv.Close()
	res := probeDerpURL(srv.URL)
	if !res.OK {
		t.Errorf("expected ok, got err=%q", res.Err)
	}
	if res.LatencyMS < 0 {
		t.Errorf("latency should be ≥0, got %d", res.LatencyMS)
	}
}

// TestProbeDerpURL_500: a server that returns 500 is
// reported as fail with HTTP 500 in the error.
func TestProbeDerpURL_500(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()
	res := probeDerpURL(srv.URL)
	if res.OK {
		t.Errorf("expected fail, got ok")
	}
	if !strings.Contains(res.Err, "HTTP 500") {
		t.Errorf("err should mention HTTP 500, got %q", res.Err)
	}
}

// TestProbeDerpURL_EmptyBody: a server that returns 200
// with empty body is reported as fail (real derpmap.json
// is never empty).
func TestProbeDerpURL_EmptyBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()
	res := probeDerpURL(srv.URL)
	if res.OK {
		t.Errorf("expected fail on empty body, got ok")
	}
	if !strings.Contains(res.Err, "empty body") {
		t.Errorf("err should mention empty body, got %q", res.Err)
	}
}

// TestProbeAllDerps: probeAllDerps returns one
// TestResult per input URL, in order.
func TestProbeAllDerps(t *testing.T) {
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{"Regions":{}}`))
	}))
	defer good.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer bad.Close()
	results := probeAllDerps([]string{good.URL, bad.URL})
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if !results[0].OK {
		t.Errorf("results[0] should be ok, got err=%q", results[0].Err)
	}
	if results[1].OK {
		t.Errorf("results[1] should be fail, got ok")
	}
}

// ---------- 4. Handler integration (smoke) ----------

// TestPostAdminDerpConfig_ActionApply: when the form is
// posted with action=apply, the handler runs the apply
// (which uses the fake docker) and re-renders the page
// (200) with the apply result visible in the body.
func TestPostAdminDerpConfig_ActionApply(t *testing.T) {
	f := &fakeDocker{nextStdout: "true"}
	withFakeDocker(t, f)

	// The renderer needs the templates dir; set the env
	// var the production code uses to find them. In a
	// test the path is wrong, so we set the renderer's
	// templatesDir via a side channel: the production
	// newRenderer() always uses /app/deploy/templates,
	// which doesn't exist in tests. Monkey-patch the
	// templates dir by creating that exact path with
	// the templates in it.
	setupProductionTemplatesDir(t)

	app, d := newTestApp(t, &testNotifier{})
	defer d.Close()
	app.withTemplates()

	form := url.Values{}
	form.Set("external_urls", "https://derp1.example.com")
	form.Set("bundled_enabled", "0")
	form.Set("action", "apply")
	req := authedReqFor(t, app, "POST", "/admin/derp/config", form, "skyadmin")
	w := httptest.NewRecorder()
	app.PostAdminDerpConfig(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 (re-render), got %d; body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "Результат применения") && !strings.Contains(body, "Apply result") {
		t.Errorf("expected apply result heading, got body: %s", body)
	}
	if !strings.Contains(body, "Изменения применены") && !strings.Contains(body, "Changes applied") {
		t.Errorf("expected applied message, got body: %s", body)
	}
	// DB was persisted (apply = save + apply).
	var urls string
	_ = d.QueryRow(`SELECT value FROM global_settings WHERE key = 'derp.external_urls'`).Scan(&urls)
	if !strings.Contains(urls, "derp1.example.com") {
		t.Errorf("apply should persist config, got derp.external_urls=%q", urls)
	}
}

// TestPostAdminDerpConfig_ActionTest: action=test
// probes the URLs and re-renders the page with the
// per-URL results table visible.
func TestPostAdminDerpConfig_ActionTest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{"Regions":{}}`))
	}))
	defer srv.Close()
	app, d := newTestApp(t, &testNotifier{})
	defer d.Close()
	app.withTemplates()

	form := url.Values{}
	form.Set("external_urls", srv.URL)
	form.Set("bundled_enabled", "0")
	form.Set("action", "test")
	req := authedReqFor(t, app, "POST", "/admin/derp/config", form, "skyadmin")
	w := httptest.NewRecorder()
	app.PostAdminDerpConfig(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d; body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "Результаты проверки") && !strings.Contains(body, "Test results") {
		t.Errorf("expected test results heading, got body: %s", body)
	}
	if !strings.Contains(body, srv.URL) {
		t.Errorf("expected test URL echoed, got body: %s", body)
	}
}

// TestPostAdminHeadplane_ActionApply: action=apply on
// the Headplane form runs the same apply pipeline
// (headscale + headplane lifecycle) and re-renders the
// page with the trace.
func TestPostAdminHeadplane_ActionApply(t *testing.T) {
	f := &fakeDocker{nextStdout: "true"}
	withFakeDocker(t, f)
	setupProductionTemplatesDir(t)

	app, d := newTestApp(t, &testNotifier{})
	defer d.Close()
	app.withTemplates()

	form := url.Values{}
	form.Set("mode", "off")
	form.Set("external_url", "")
	form.Set("action", "apply")
	req := authedReqFor(t, app, "POST", "/admin/headplane", form, "skyadmin")
	w := httptest.NewRecorder()
	app.PostAdminHeadplane(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d; body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "Результат применения") && !strings.Contains(body, "Apply result") {
		t.Errorf("expected apply result heading, got body: %s", body)
	}
	// DB has the new mode.
	var mode string
	_ = d.QueryRow(`SELECT value FROM global_settings WHERE key = 'headplane.mode'`).Scan(&mode)
	if mode != "off" {
		t.Errorf("headplane.mode = %q, want off", mode)
	}
}

// setupProductionTemplatesDir creates /app/deploy/templates
// with the two headscale templates so the production
// newRenderer() can find them in tests. It is a no-op
// if the dir already exists.
func setupProductionTemplatesDir(t *testing.T) {
	t.Helper()
	dir := "/app/deploy/templates"
	if _, err := os.Stat(dir); err == nil {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Skipf("cannot create %s: %v (probably running on Windows where /app doesn't exist)", dir, err)
		return
	}
	// Copy from the real repo location.
	for _, name := range []string{
		"headscale-config.yaml.tmpl",
		"headscale-compose.yml.tmpl",
	} {
		src, err := os.ReadFile(filepath.Join("..", "..", "deploy", "templates", name))
		if err != nil {
			t.Skipf("cannot read %s: %v", name, err)
			return
		}
		if err := os.WriteFile(filepath.Join(dir, name), src, 0o644); err != nil {
			t.Skipf("cannot write %s: %v", name, err)
			return
		}
	}
	// /app itself is also a go:embed requirement for the
	// production templates package; not relevant here.
	t.Cleanup(func() { os.RemoveAll("/app/deploy") })
}
