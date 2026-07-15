// 2026-07-15: Этап 14 v15 (v0.11.1) — runtime renderer for the
// integration config.
//
// v0.11.0 added /admin/integrations (web UI for DERP + Headplane)
// but the form only persisted — applying the change still
// required running ./deploy/deploy.sh on the host. v0.11.1
// ports the deploy-time template rendering to Go and wires
// the rendered output directly into the headscale / derper /
// headplane containers via `docker exec` (the skygate
// container has /var/run/docker.sock mounted).
//
// Three actions supported:
//
//  1. Apply — re-render `deploy/templates/headscale-config.yaml.tmpl`
//     and `deploy/templates/headscale-compose.yml.tmpl` with
//     the current IntegrationConfig, push the new config.yaml
//     into the running headscale container, and SIGHUP it
//     to reload (headscale supports SIGHUP for derp.urls +
//     log-level changes; for anything else the container
//     restart is the source of truth). Start/stop the
//     bundled derper and headplane containers as the
//     toggles require.
//
//  2. Test URL — probe each external DERP URL (5s timeout
//     via HEAD or GET; same probe as the v0.11.0
//     probeDerpmapURL helper but inlined here so the
//     handler can return per-URL results inline).
//
//  3. Test single URL — same as #2 but for a specific URL
//     the admin typed in the form (used to validate the
//     "Test" button next to each input).
//
// Why a separate file? admin_integrations.go owns the form
// CRUD (Save action); this file owns the runtime side
// (Apply + Test). The two actions have very different
// concerns: Save writes to SQLite, Apply shells out to
// docker. Splitting the file keeps the test surface small
// — Save tests don't need to mock docker, Apply tests
// don't need to mock global_settings.
//
// dockerCmd is a package-level variable so tests can swap
// the executor (the production code calls docker on the
// skygate container's mounted /var/run/docker.sock; tests
// use a fakeRecorder to capture what would have been
// invoked).

package handlers

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

// newRenderer returns a renderer rooted at the deploy
// templates dir. On the live system this is
// `/app/deploy/templates` (the docker-compose.yml
// bind-mounts `./:/app` so the host's repo is visible
// at /app inside the skygate container). Tests pass a
// temp dir with the templates copied in.
func newRenderer() *renderer {
	return &renderer{templatesDir: "/app/deploy/templates"}
}

// renderHeadscaleConfig produces the body of
// ${DEPLOY_HEADSCALE_DIR}/config/config.yaml from the
// stored IntegrationConfig. Pure function — no I/O. The
// env-var lookup is bounded to the variables the template
// references (HEADSCALE_* + SKYGATE_*); anything else is
// left as `${VAR}` so a missing variable is visible rather
// than silently empty.
//
// This mirrors the Python one-liner in deploy/deploy.sh's
// render_template() function, minus the chmod and the
// destination write. The output is byte-for-byte
// compatible with what deploy.sh would have produced for
// the same env + config (the deploy script uses python's
// `os.environ.get(var, original)` for the ${VAR}
// substitution; we use the same "leave it alone if unset"
// semantics to keep the two paths drop-in interchangeable).
func (r *renderer) renderHeadscaleConfig(cfg *intConfig) (string, error) {
	tmpl, err := r.readTemplate("headscale-config.yaml.tmpl")
	if err != nil {
		return "", err
	}
	// ${VAR} → os.Getenv(VAR) (keep original on miss)
	out := expandEnv(tmpl)
	// __HEADSCALE_AUTO_APPROVE_ROUTES__ → YAML list
	routesBlock := renderYAMLList("HEADSCALE_AUTO_APPROVE_ROUTES",
		csvFromEnv("HEADSCALE_AUTO_APPROVE_ROUTES"))
	out = strings.ReplaceAll(out, "__HEADSCALE_AUTO_APPROVE_ROUTES__", routesBlock)
	// __HEADSCALE_DERP_URLS__ → YAML list (HEADSCALE_DERP_URLS
	// + cfg.DERPExternalURLs, in that order — matches
	// deploy.sh). The DERP map list is the merge of the
	// deploy-time Tailscale public derp + the operator's
	// external URLs.
	merged := append(csvFromEnv("HEADSCALE_DERP_URLS"), cfg.DERPExternalURLs...)
	derpBlock := renderYAMLList("HEADSCALE_DERP_URLS", merged)
	out = strings.ReplaceAll(out, "__HEADSCALE_DERP_URLS__", derpBlock)
	return out, nil
}

// renderHeadscaleCompose produces docker-compose.yml body
// with the headplane service block kept or stripped
// according to cfg.HeadplaneMode:
//   - "bundled" → keep the block (the operator wants the
//     sidecar running)
//   - "external" or "off" → strip the block (the operator
//     points at an existing instance or doesn't want
//     Headplane wired in at all; deploy.sh does the same)
//
// renderHeadscaleCompose is the runtime counterpart of
// deploy.sh's `if HEADPLANE_ENABLED=false || [ -n
// HEADPLANE_EXTERNAL_URL ]` branch. By keeping the
// stripping logic in Go we get the same behaviour without
// a shell-out to python.
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

// readTemplate loads a template file from the configured
// templatesDir. The templates are baked into the skygate
// image via the bind-mount in docker-compose.yml, so the
// file is always present in production. In tests the
// caller sets renderer.templatesDir to a temp dir.
func (r *renderer) readTemplate(name string) (string, error) {
	p := filepath.Join(r.templatesDir, name)
	b, err := os.ReadFile(p)
	if err != nil {
		return "", fmt.Errorf("read template %s: %w", name, err)
	}
	return string(b), nil
}

// intConfig is an alias for *db.IntegrationConfig so the
// renderer signature is self-documenting at the call site
// (the handler passes a *db.IntegrationConfig it just loaded
// from SQLite; passing it as intConfig makes the renderer's
// "I work on the saved config" intent clear).
type intConfig = db.IntegrationConfig

// expandEnv is a small, deterministic replacement for
// os.Expand that preserves the original ${VAR} on a
// missing variable (so the operator sees a broken
// template in the file, not a silent empty value).
// Also: ${VAR} on its own (no default) → os.Getenv
// result, empty if unset; $${VAR} is a literal escape
// (matches bash semantics; the deploy script doesn't
// need this but the operator may want to embed a
// literal ${SOMETHING} in their config later).
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

// renderYAMLList produces the multi-line indented YAML
// list for a special marker. The list is one entry per
// line, indented with the marker prefix; each entry is
// "- <value>". The variable name is purely for log
// messages (the renderer logs which variable it's
// expanding so a misconfigured env is easy to spot).
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

// csvFromEnv reads an env var and splits it as CSV. Used
// for HEADSCALE_DERP_URLS and HEADSCALE_AUTO_APPROVE_ROUTES
// which are comma-separated in .env. The split matches
// deploy.sh's IFS=',' read loop (trims whitespace, drops
// empty entries).
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

// stripHeadplaneServiceBlock removes the headplane service
// from a rendered docker-compose.yml. The block runs from
// "  headplane:" up to (but not including) the next
// top-level key (a line starting at column 0). It also
// removes the headplane_data volume entry.
//
// The regex strategy is to look for the literal
// "  headplane:" (two-space indent — that's the service
// indent in our compose template) and drop everything
// from there to the next "volumes:" or "networks:"
// top-level section. The deploy script does this with
// python's re.sub; we do it with strings.Index + slicing
// to keep the runtime pure-Go.
func stripHeadplaneServiceBlock(s string) string {
	// Find the headplane service block. Look for
	// "\n  headplane:" (the newline ensures we don't
	// match "headplane_data" or similar).
	startMarker := "\n  headplane:"
	start := strings.Index(s, startMarker)
	if start < 0 {
		return s
	}
	// Find the end: next top-level key at column 0.
	// A "top-level" key in our compose template is a
	// line that starts with a non-space character. The
	// service blocks are all indented; volumes and
	// networks sections are top-level. We scan forward
	// from start.
	body := s[start+1:] // drop the leading \n we matched
	// The first line of the body is "  headplane:". We
	// want to drop everything from the newline after
	// this line up to the next top-level line.
	lines := strings.Split(body, "\n")
	// lines[0] == "  headplane:" — drop it.
	// Scan from i=1; when we hit a line that doesn't
	// start with whitespace, we've reached the next
	// section.
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
	// Reconstruct without the headplane block.
	kept := append([]string{}, lines[:0]...)
	kept = append(kept, lines[end:]...)
	newBody := strings.Join(kept, "\n")
	// Also strip the headplane_data volume entry. The
	// template has a 2-space indented "  headplane_data:"
	// line followed by nothing else. The same
	// "drop until next non-indented line" pattern.
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

// startsWithWhitespace is a one-liner extracted for
// readability in the strip logic. "" returns false
// (handled by the caller).
func startsWithWhitespace(s string) bool {
	if s == "" {
		return false
	}
	c := s[0]
	return c == ' ' || c == '\t'
}

// ---------- Docker orchestration ----------

// ApplyResult is the outcome of an Apply action. The
// handler renders the result back to the user as a
// flash message; the Audit log records the headline.
type ApplyResult struct {
	// OK is true if the whole apply succeeded.
	OK bool
	// Steps is a human-readable trace of what the
	// renderer did (re-render, push, SIGHUP, start
	// containers, etc.). Each step is "ok" or "fail: …".
	Steps []string
	// Err is the final error message if OK is false;
	// empty otherwise.
	Err string
}

// applyHeadscale is the runtime equivalent of
// "./deploy/deploy.sh" for the headscale config +
// restart path. It:
//   1. Renders headscale-config.yaml.tmpl with the
//      current IntegrationConfig.
//   2. Pipes the rendered body into the headscale
//      container's /etc/headscale/config.yaml via
//      `docker exec -i headscale sh -c "cat > ..."`.
//   3. SIGHUPs the headscale container to reload.
//
// Returns the trace of what happened. Errors from step
// 2 or step 3 short-circuit the apply (the operator
// sees the trace in /admin/integrations).
func (r *renderer) applyHeadscale(cfg *intConfig) ApplyResult {
	res := ApplyResult{}
	body, err := r.renderHeadscaleConfig(cfg)
	if err != nil {
		res.Err = err.Error()
		res.Steps = append(res.Steps, "fail: render: "+err.Error())
		return res
	}
	res.Steps = append(res.Steps, "ok: render headscale-config.yaml")

	// Push to headscale container. The container's
	// /etc/headscale is bind-mounted from the host's
	// ${DEPLOY_HEADSCALE_DIR}/config, so writing here
	// updates the on-disk file too.
	out, err := dockerCmdStdin(strings.NewReader(body),
		"docker", "exec", "-i", "headscale",
		"sh", "-c", "cat > /etc/headscale/config.yaml")
	if err != nil {
		res.Err = "push to headscale: " + err.Error() + ": " + string(out)
		res.Steps = append(res.Steps, "fail: docker exec: "+string(out))
		return res
	}
	res.Steps = append(res.Steps, "ok: pushed config.yaml to headscale container")

	// SIGHUP. headscale supports SIGHUP for config
	// reload; derp.urls and log-level changes pick up
	// without a full container restart.
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

// applyBundledDERP starts or stops the bundled derper
// container to match cfg.BundledDERP. Idempotent: if
// the container is already in the desired state, the
// docker call is a no-op and the trace says "ok:
// already ...".
//
// This does NOT create the derper container from
// scratch — that requires the bind-mounted
// derper-compose.yml on the host, which the skygate
// container doesn't see. The first-time install of the
// bundled derper still requires ./deploy/deploy.sh;
// the runtime renderer handles the toggle (start /
// stop / restart) for the common case.
func (r *renderer) applyBundledDERP(cfg *intConfig) ApplyResult {
	res := ApplyResult{}
	want := cfg.BundledDERP

	// Inspect current state.
	out, err := dockerCmd("docker", "inspect", "-f", "{{.State.Running}}", "derper")
	if err != nil {
		// Container doesn't exist. If we want it,
		// tell the operator to run deploy.sh; if we
		// don't, this is fine (already stopped).
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

// applyHeadplane starts or stops the bundled headplane
// container to match cfg.HeadplaneMode. The mode
// determines what "in sync" means:
//   - "bundled" — the local headplane container should
//     be running.
//   - "external" / "off" — the local headplane
//     container should be stopped and removed (we point
//     at the external one, or don't wire Headplane in
//     at all; the local sidecar just consumes resources).
//
// First-time install of the headplane container is
// still deploy.sh's job (the headplane service block
// lives in the bind-mounted docker-compose.yml which
// the skygate container doesn't see).
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
		// stop + remove so the sidecar's host port
		// (50445) is freed for whatever the operator
		// does next. Keeping a stopped container is
		// just clutter.
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

// applyAll is the orchestrator the handler calls. It
// runs the three sub-applies in order, aggregating the
// traces. The first sub-apply's failure short-circuits
// the rest (the headscale config is the source of
// truth; if we can't push it, no point starting/stopping
// containers).
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

// ---------- DERP URL probe ----------

// TestResult is one row in the test-all-URLs table the
// /admin/derp/config page renders below the form. The
// URL is echoed back so the operator can match the
// result to the input.
type TestResult struct {
	URL    string
	OK     bool
	LatencyMS int64
	Err    string
}

// probeDerpURL is the renderer-side counterpart of
// admin_integrations.go's probeDerpmapURL; it also
// measures the round-trip latency and returns it so
// the operator can see which relays respond slowly.
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
	// Read first 16 bytes; an empty body is also "fail"
	// (a real derpmap.json starts with `{` and is
	// non-trivial in size).
	buf := make([]byte, 16)
	n, _ := io.ReadFull(resp.Body, buf)
	if n == 0 {
		return TestResult{URL: u, OK: false, LatencyMS: latency, Err: "empty body"}
	}
	return TestResult{URL: u, OK: true, LatencyMS: latency}
}

// probeAllDerps runs probeDerpURL on every URL in the
// list and returns the per-URL results in the same
// order. The handler renders this as a small table
// below the form.
func probeAllDerps(urls []string) []TestResult {
	out := make([]TestResult, 0, len(urls))
	for _, u := range urls {
		out = append(out, probeDerpURL(u))
	}
	return out
}
