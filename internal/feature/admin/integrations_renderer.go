// Package admin — integrations_renderer.go is the runtime
// renderer for the integration config: re-renders the
// headscale config and compose templates, pushes the new
// config via docker cp, SIGHUPs headscale, and starts/stops
// the bundled derper and headplane containers.
//
// refactor-v0.30 Phase B step 3b.2 (2026-07-29): moved
// from internal/handlers/admin_integrations_renderer.go.
// The functions here are pure-Go (no *App dependency) so
// the move is a straight copy with only the package
// declaration changing.

package admin

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"skygate/internal/db"
)

// dockerCmd is the function used to execute docker commands
// against the host's docker daemon (the skygate container has
// /var/run/docker.sock bind-mounted). Tests override it to
// capture invocations without touching a real docker socket.
var dockerCmd = func(name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	return cmd.CombinedOutput()
}

// 2026-08-12 v1.3.9 (P4 catalog cleanup): dockerCmdStdin
// was added in a prior refactor for the integrations
// page but is no longer called — the renderer was
// simplified to render the compose file in-memory
// rather than piping through `docker exec`. Staticcheck
// U1000 (unused). Removed.

// renderer is the runtime renderer for the integration
// config. It is stateless (the App.DB has the persisted
// state; the env has the deploy-time values) so the
// methods are free functions rather than a struct. The
// struct is used purely for testability: a renderer
// instance can be built with a custom templatesDir.
type renderer struct {
	templatesDir string
	// v1.3.17: DB handle for the new derp_relays table.
	// applyBundledDERP reads IsBundledDerpRelayEnabled
	// (the CRUD-managed source of truth) instead of
	// cfg.BundledDERP (the legacy global_settings flag).
	db *sql.DB
}

// newRendererWithDB returns a renderer that can query
// the derp_relays table. Used by applyAndRenderDerp in
// integrations.go — the new (v1.3.17) call site.
func newRendererWithDB(d *sql.DB) *renderer {
	return &renderer{templatesDir: "/app/deploy/templates", db: d}
}

func (r *renderer) renderHeadscaleConfig(cfg *intConfig) (string, error) {
	tmpl, err := r.readTemplate("headscale-config.yaml.tmpl")
	if err != nil {
		return "", err
	}
	out := expandEnv(tmpl)
	routesBlock := renderYAMLList("HEADSCALE_AUTO_APPROVE_ROUTES",
		csvFromEnv("HEADSCALE_AUTO_APPROVE_ROUTES"))
	out = strings.ReplaceAll(out, "__HEADSCALE_AUTO_APPROVE_ROUTES__", routesBlock)
	merged := append(csvFromEnv("HEADSCALE_DERP_URLS"), cfg.DERPExternalURLs...)
	// v1.3.17: also include the v1.3.17 derp_relays
	// table's enabled rows. The legacy cfg.DERPExternalURLs
	// (textarea) is kept for backward compat — operators
	// who haven't migrated to /admin/derp/relays still
	// work. db.ListEnabledDerpRelayURLs may add NEW URLs
	// (operator added via the CRUD UI) or omit disabled
	// ones (operator toggled off via the CRUD UI).
	if r.db != nil {
		if extra, err := db.ListEnabledDerpRelayURLs(r.db); err == nil {
			merged = appendUnique(merged, extra)
		}
	}
	derpBlock := renderYAMLList("HEADSCALE_DERP_URLS", merged)
	out = strings.ReplaceAll(out, "__HEADSCALE_DERP_URLS__", derpBlock)
	return out, nil
}

// appendUnique appends to from b's elements to a, skipping
// any already present in a. Used by renderHeadscaleConfig
// to merge the legacy cfg.DERPExternalURLs with the new
// derp_relays table's enabled URLs.
func appendUnique(a, b []string) []string {
	seen := make(map[string]bool, len(a))
	for _, s := range a {
		seen[s] = true
	}
	for _, s := range b {
		if !seen[s] {
			a = append(a, s)
			seen[s] = true
		}
	}
	return a
}

// 2026-08-12 v1.3.9 (P4 catalog cleanup): renderHeadscaleCompose
// + stripHeadplaneServiceBlock + startsWithWhitespace
// were used by the old "render compose file from /admin
// integrations" page which was removed in a recent
// refactor (the page now shows pre-rendered content
// from a static file). All three are staticcheck
// U1000 (unused). Removed.

func (r *renderer) readTemplate(name string) (string, error) {
	p := filepath.Join(r.templatesDir, name)
	b, err := os.ReadFile(p)
	if err != nil {
		return "", fmt.Errorf("read template %s: %w", name, err)
	}
	return string(b), nil
}

// intConfig is an alias for *db.IntegrationConfig so the
// renderer signature is self-documenting at the call site.
type intConfig = db.IntegrationConfig

// expandEnv is a small, deterministic replacement for
// os.Expand that preserves the original ${VAR} on a
// missing variable.
func expandEnv(s string) string {
	var out bytes.Buffer
	i := 0
	for i < len(s) {
		// $$ → literal $
		if s[i] == '$' && i+1 < len(s) && s[i+1] == '$' {
			out.WriteByte('$')
			i += 2
			continue
		}
		// ${VAR}
		if s[i] == '$' && i+1 < len(s) && s[i+1] == '{' {
			end := strings.IndexByte(s[i+2:], '}')
			if end < 0 {
				out.WriteByte(s[i])
				i++
				continue
			}
			varName := s[i+2 : i+2+end]
			val, ok := os.LookupEnv(varName)
			if !ok {
				// Preserve original ${VAR} so the
				// operator notices the missing env.
				out.WriteString(s[i : i+2+end+1])
			} else {
				out.WriteString(val)
			}
			i += 2 + end + 1
			continue
		}
		out.WriteByte(s[i])
		i++
	}
	return out.String()
}

func renderYAMLList(varName string, items []string) string {
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		b.WriteString("    - ")
		b.WriteString(item)
		b.WriteByte('\n')
	}
	_ = varName // reserved for future structured logging
	return b.String()
}

func csvFromEnv(name string) []string {
	raw := os.Getenv(name)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// 2026-08-12 v1.3.9 (P4 catalog cleanup): stripHeadplaneServiceBlock
// and startsWithWhitespace were used by the removed
// renderHeadscaleCompose above. All three are now dead.
// Removed.

// ApplyResult is the outcome of an Apply action. The
// handler renders the result back to the user as a
// flash message; the Audit log records the headline.
type ApplyResult struct {
	OK    bool
	Steps []string
	Err   string
}

func (r *renderer) applyHeadscale(cfg *intConfig) ApplyResult {
	res := ApplyResult{}
	body, err := r.renderHeadscaleConfig(cfg)
	if err != nil {
		res.Err = err.Error()
		res.Steps = append(res.Steps, "fail: render: "+err.Error())
		return res
	}
	res.Steps = append(res.Steps, "ok: render headscale-config.yaml")

	tmpPath := "/tmp/skygate-headscale-config.yaml"
	if err := os.WriteFile(tmpPath, []byte(body), 0o644); err != nil {
		res.Err = "write tmp: " + err.Error()
		res.Steps = append(res.Steps, "fail: write "+tmpPath+": "+err.Error())
		return res
	}
	defer os.Remove(tmpPath)
	res.Steps = append(res.Steps, "ok: wrote "+tmpPath)

	out, err := dockerCmd("docker", "cp", tmpPath, "headscale:/etc/headscale/config.yaml")
	if err != nil {
		res.Err = "push to headscale: " + err.Error() + ": " + string(out)
		res.Steps = append(res.Steps, "fail: docker cp: "+string(out))
		return res
	}
	res.Steps = append(res.Steps, "ok: pushed config.yaml to headscale container")

	out, err = dockerCmd("docker", "kill", "-s", "HUP", "headscale")
	if err != nil {
		res.Err = "sighup: " + err.Error() + ": " + string(out)
		res.Steps = append(res.Steps, "fail: docker kill HUP: "+string(out))
		return res
	}
	res.Steps = append(res.Steps, "ok: SIGHUP headscale (config reloaded)")
	res.OK = true
	return res
}

func (r *renderer) applyBundledDERP(cfg *intConfig) ApplyResult {
	res := ApplyResult{}
	// v1.3.17: prefer the derp_relays table's bundled
	// row (the CRUD-managed source of truth) over the
	// legacy global_settings.derp.bundled_enabled flag.
	// Falls back to cfg.BundledDERP if the table is
	// empty (the AutoMigrateDerpRelays helper will have
	// copied the legacy flag into a row on the first
	// /admin/derp/relays GET, so this fallback is for
	// the deploy-time apply path before the UI is
	// loaded).
	if bundledEnabled, err := db.IsBundledDerpRelayEnabled(r.db); err == nil {
		want := bundledEnabled
		_ = want // shadow below
		res.Steps = append(res.Steps,
			fmt.Sprintf("ok: derp_relays bundled row enabled=%t", bundledEnabled))
	} else {
		res.Steps = append(res.Steps,
			"warn: IsBundledDerpRelayEnabled failed: "+err.Error()+
				" (falling back to cfg.BundledDERP)")
	}
	want := cfg.BundledDERP
	if v, err := db.IsBundledDerpRelayEnabled(r.db); err == nil {
		want = v
	}

	out, err := dockerCmd("docker", "inspect", "-f", "{{.State.Running}}", "derper")
	if err != nil {
		if want {
			res.Steps = append(res.Steps, "fail: derper container not found; run ./deploy/deploy.sh to install it")
			res.Err = "derper container does not exist"
			return res
		}
		res.Steps = append(res.Steps, "ok: derper not installed (matches BundledDERP=false)")
		res.OK = true
		return res
	}
	running := strings.TrimSpace(string(out)) == "true"
	if want && !running {
		out, err = dockerCmd("docker", "start", "derper")
		if err != nil {
			res.Err = "start derper: " + err.Error() + ": " + string(out)
			res.Steps = append(res.Steps, "fail: docker start derper: "+string(out))
			return res
		}
		res.Steps = append(res.Steps, "ok: started derper container")
	} else if !want && running {
		out, err = dockerCmd("docker", "stop", "derper")
		if err != nil {
			res.Err = "stop derper: " + err.Error() + ": " + string(out)
			res.Steps = append(res.Steps, "fail: docker stop derper: "+string(out))
			return res
		}
		res.Steps = append(res.Steps, "ok: stopped derper container")
	} else {
		state := "stopped"
		if want {
			state = "running"
		}
		res.Steps = append(res.Steps, "ok: derper already "+state)
	}
	res.OK = true
	return res
}

func (r *renderer) applyHeadplane(cfg *intConfig) ApplyResult {
	res := ApplyResult{}
	want := cfg.HeadplaneMode == "bundled"

	out, err := dockerCmd("docker", "inspect", "-f", "{{.State.Running}}", "headplane")
	if err != nil {
		if want {
			res.Steps = append(res.Steps, "fail: headplane container not found; run ./deploy/deploy.sh to install it")
			res.Err = "headplane container does not exist"
			return res
		}
		res.Steps = append(res.Steps, "ok: headplane not installed (matches mode=off/external)")
		res.OK = true
		return res
	}
	running := strings.TrimSpace(string(out)) == "true"
	if want && !running {
		out, err = dockerCmd("docker", "start", "headplane")
		if err != nil {
			res.Err = "start headplane: " + err.Error() + ": " + string(out)
			res.Steps = append(res.Steps, "fail: docker start headplane: "+string(out))
			return res
		}
		res.Steps = append(res.Steps, "ok: started headplane container")
	} else if !want && running {
		out, err = dockerCmd("docker", "stop", "headplane")
		if err != nil {
			res.Err = "stop headplane: " + err.Error() + ": " + string(out)
			res.Steps = append(res.Steps, "fail: docker stop headplane: "+string(out))
			return res
		}
		out, err = dockerCmd("docker", "rm", "headplane")
		if err != nil {
			res.Err = "rm headplane: " + err.Error() + ": " + string(out)
			res.Steps = append(res.Steps, "fail: docker rm headplane: "+string(out))
			return res
		}
		res.Steps = append(res.Steps, "ok: stopped and removed headplane container")
	} else {
		state := "stopped"
		if want {
			state = "running"
		}
		res.Steps = append(res.Steps, "ok: headplane already "+state)
	}
	res.OK = true
	return res
}

func (r *renderer) applyAll(cfg *intConfig) ApplyResult {
	res := ApplyResult{}
	headscaleRes := r.applyHeadscale(cfg)
	res.Steps = append(res.Steps, headscaleRes.Steps...)
	if !headscaleRes.OK {
		res.Err = headscaleRes.Err
		return res
	}
	derpRes := r.applyBundledDERP(cfg)
	res.Steps = append(res.Steps, derpRes.Steps...)
	if !derpRes.OK {
		res.Err = derpRes.Err
		return res
	}
	hpRes := r.applyHeadplane(cfg)
	res.Steps = append(res.Steps, hpRes.Steps...)
	if !hpRes.OK {
		res.Err = hpRes.Err
		return res
	}
	res.OK = true
	return res
}

// TestResult is one row in the test-all-URLs table the
// /admin/derp/config page renders below the form.
type TestResult struct {
	URL       string
	OK        bool
	LatencyMS int64
	Err       string
}

func probeDerpURL(u string) TestResult {
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return TestResult{URL: u, OK: false, Err: "bad URL: " + err.Error()}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return TestResult{
			URL:       u,
			OK:        false,
			LatencyMS: time.Since(start).Milliseconds(),
			Err:       "fetch: " + err.Error(),
		}
	}
	defer resp.Body.Close()
	latency := time.Since(start).Milliseconds()
	if resp.StatusCode != http.StatusOK {
		return TestResult{
			URL:       u,
			OK:        false,
			LatencyMS: latency,
			Err:       fmt.Sprintf("HTTP %d", resp.StatusCode),
		}
	}
	buf := make([]byte, 16)
	n, _ := io.ReadFull(resp.Body, buf)
	if n == 0 {
		return TestResult{URL: u, OK: false, LatencyMS: latency, Err: "empty body"}
	}
	return TestResult{URL: u, OK: true, LatencyMS: latency}
}

func probeAllDerps(urls []string) []TestResult {
	out := make([]TestResult, 0, len(urls))
	for _, u := range urls {
		out = append(out, probeDerpURL(u))
	}
	return out
}
