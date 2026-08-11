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

// dockerCmdStdin is like dockerCmd but feeds stdin (used to
// pipe rendered files into a `docker exec` cat command).
var dockerCmdStdin = func(stdin io.Reader, name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	cmd.Stdin = stdin
	return cmd.CombinedOutput()
}

// renderer is the runtime renderer for the integration
// config. It is stateless (the App.DB has the persisted
// state; the env has the deploy-time values) so the
// methods are free functions rather than a struct. The
// struct is used purely for testability: a renderer
// instance can be built with a custom templatesDir.
type renderer struct {
	templatesDir string
}

func newRenderer() *renderer {
	return &renderer{templatesDir: "/app/deploy/templates"}
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
	derpBlock := renderYAMLList("HEADSCALE_DERP_URLS", merged)
	out = strings.ReplaceAll(out, "__HEADSCALE_DERP_URLS__", derpBlock)
	return out, nil
}

func (r *renderer) renderHeadscaleCompose(cfg *intConfig) (string, error) {
	tmpl, err := r.readTemplate("headscale-compose.yml.tmpl")
	if err != nil {
		return "", err
	}
	out := expandEnv(tmpl)
	if cfg.HeadplaneMode != "bundled" {
		out = stripHeadplaneServiceBlock(out)
	}
	return out, nil
}

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

func stripHeadplaneServiceBlock(s string) string {
	startMarker := "\n  headplane:"
	start := strings.Index(s, startMarker)
	if start < 0 {
		return s
	}
	body := s[start+1:]
	lines := strings.Split(body, "\n")
	end := len(lines)
	for i := 1; i < len(lines); i++ {
		line := lines[i]
		if line == "" {
			continue
		}
		if !startsWithWhitespace(line) {
			end = i
			break
		}
	}
	kept := append([]string{}, lines[:0]...)
	kept = append(kept, lines[end:]...)
	newBody := strings.Join(kept, "\n")
	volMarker := "\n  headplane_data:"
	volStart := strings.Index(newBody, volMarker)
	if volStart >= 0 {
		volBody := newBody[volStart+1:]
		volLines := strings.Split(volBody, "\n")
		volEnd := len(volLines)
		for i := 1; i < len(volLines); i++ {
			line := volLines[i]
			if line == "" {
				continue
			}
			if !startsWithWhitespace(line) {
				volEnd = i
				break
			}
		}
		volKept := append([]string{}, volLines[:0]...)
		volKept = append(volKept, volLines[volEnd:]...)
		newBody = strings.Join(volKept, "\n")
	}
	return s[:start+1] + newBody
}

func startsWithWhitespace(s string) bool {
	if s == "" {
		return false
	}
	c := s[0]
	return c == ' ' || c == '\t'
}

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
	want := cfg.BundledDERP

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
