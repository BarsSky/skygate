// B272 (2026-09-19) — headscale policy file mode + tag writes on a native install.
//
// Live case (host `aro`, native systemd headscale 0.29.2):
//
//	headscale/config.yaml:  policy: { mode: file, path: /etc/headscale/policy.hujson }
//	PUT /api/v1/policy → 500 {"code":2,"message":"update is disabled for modes other than database"}
//	AddTag             → tag: exec: "docker": executable file not found in $PATH
//
// Result: `node_owner_map` claimed `tag:dev-daniil-workpc` while the node had
// NO tags, every per-device ACL rule matched nothing, and the operator saw no
// error anywhere (tag.autoupdate_failed empty, metric never incremented).
package headscale

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestB272_IsFileModePolicyError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"445/404 endpoint missing", &APIError{StatusCode: 404, Body: `{"code":5,"message":"Not Found"}`}, true},
		{"405 method not allowed", &APIError{StatusCode: 405, Body: "method not allowed"}, true},
		{
			"headscale 0.29.2 file-mode 500 (live)",
			&APIError{StatusCode: 500, Body: `{"code":2, "message":"update is disabled for modes other than database"}`},
			true,
		},
		{"500 from a broken policy stays real", &APIError{StatusCode: 500, Body: `{"code":2,"message":"failed to parse policy"}`}, false},
		{"400 bad request stays real", &APIError{StatusCode: 400, Body: `{"code":3,"message":"update is disabled"}`}, false},
		{"plain error", os.ErrPermission, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isFileModePolicyError(tc.err); got != tc.want {
				t.Fatalf("isFileModePolicyError = %v, want %v (err=%v)", got, tc.want, tc.err)
			}
		})
	}
}

func TestB272_ParseHeadscalePolicyConfig(t *testing.T) {
	live := `# headscale config
server_url: https://hs.example.com
database:
  type: sqlite
  sqlite:
    path: /var/lib/headscale/db.sqlite
policy:
  mode: file
  path: /etc/headscale/policy.hujson
derp:
  urls:
    - https://controlplane.tailscale.com/derpmap/default
`
	path, mode := parseHeadscalePolicyConfig(live)
	if path != "/etc/headscale/policy.hujson" {
		t.Errorf("path = %q, want /etc/headscale/policy.hujson", path)
	}
	if mode != "file" {
		t.Errorf("mode = %q, want file", mode)
	}

	// A `path:` in another section must never be mistaken for the policy file.
	other := "database:\n  path: /var/lib/headscale/db.sqlite\n"
	if p, _ := parseHeadscalePolicyConfig(other); p != "" {
		t.Errorf("path = %q, want empty (the database path is not the policy file)", p)
	}

	// database mode with a seed path: the path is present but the mode says
	// the API should work, so DiscoverPolicyPath must not treat it as file mode.
	seeded := "policy:\n  mode: database\n  path: /etc/headscale/policy.hujson\n"
	if p, m := parseHeadscalePolicyConfig(seeded); p == "" || m != "database" {
		t.Errorf("got (%q,%q), want the path and mode=database", p, m)
	}

	// Quoted values and inline comments.
	quoted := "policy:\n  mode: \"file\"\n  path: \"/etc/headscale/policy.hujson\" # operator file\n"
	if p, m := parseHeadscalePolicyConfig(quoted); p != "/etc/headscale/policy.hujson" || m != "file" {
		t.Errorf("quoted parse = (%q,%q), want unquoted values", p, m)
	}
}

// TestB272_SetPolicyFileModeNative drives the real fallback: the API refuses
// (500 file-mode), the policy path is configured, the file is written and a
// reload is attempted. `systemctl` does not exist in the test environment, so
// the reload must FAIL and the previous file content must be restored —
// proving the rollback works.
func TestB272_SetPolicyFileModeNative(t *testing.T) {
	dir := t.TempDir()
	policyPath := filepath.Join(dir, "policy.hujson")
	original := `{"tagOwners":{"tag:exit":["daniil@"]}}`
	if err := os.WriteFile(policyPath, []byte(original), 0o644); err != nil {
		t.Fatalf("seed policy: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && r.URL.Path == "/api/v1/policy" {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"code":2,"message":"update is disabled for modes other than database"}`))
			return
		}
		t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	c := New(srv.URL, "test-key")
	c.PolicyPath = policyPath
	// Force the native branch even on a machine that happens to have docker,
	// so the test is deterministic. (On Windows exec.LookPath consults the
	// real PATH, so an empty PATH is not enough — the native/write-path logic
	// is what we are pinning, and it is Unix-service-manager specific.)
	if runtime.GOOS == "windows" {
		c.headscaleUnit = "definitely-not-a-service"
	} else {
		t.Setenv("PATH", dir)
	}

	newPolicy := `{"tagOwners":{"tag:exit":["daniil@"],"tag:dev-daniil-workpc":["daniil@"]}}`
	err := c.SetPolicy(newPolicy)
	if err == nil {
		t.Fatal("SetPolicy returned nil although the policy could not be reloaded (no usable service manager)")
	}
	// Platform-dependent wording: on a host with a docker CLI the container
	// branch runs first; otherwise the native branch reports the reload.
	if !strings.Contains(err.Error(), "restart") && !strings.Contains(err.Error(), "docker") {
		t.Errorf("error = %v, want it to name the failing step (restart or docker write)", err)
	}
	// Rollback: the operator keeps the previous policy when the apply fails.
	got, _ := os.ReadFile(policyPath)
	if string(got) != original {
		t.Errorf("policy file = %q, want the ORIGINAL content after a failed apply", string(got))
	}
}

// TestB272_SetPolicyFileModeWritesWhenPathConfigured is the happy path with a
// stubbed service manager: the file must contain the new policy and the
// policy must NOT be silently dropped.
func TestB272_SetPolicyFileModeWritesWhenPathConfigured(t *testing.T) {
	dir := t.TempDir()
	policyPath := filepath.Join(dir, "policy.hujson")
	if err := os.WriteFile(policyPath, []byte(`{"tagOwners":{}}`), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Fake systemctl so restartHeadscaleUnit succeeds.
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	stub := filepath.Join(binDir, "systemctl")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	t.Setenv("PATH", binDir)
	// docker must NOT be found, so the native branch is selected.
	t.Setenv("SKYGATE_HEADSCALE_UNIT", "headscale-test")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"code":2,"message":"update is disabled for modes other than database"}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "k")
	c.PolicyPath = policyPath
	c.headscaleUnit = "headscale-test"
	if runtime.GOOS == "windows" {
		// Windows has no `systemctl` and no POSIX service manager: a
		// `systemctl.bat` earlier in PATH makes exec.LookPath succeed, so the
		// restart step is exercised without touching the host.
		shimDir := filepath.Join(dir, "shim")
		if err := os.MkdirAll(shimDir, 0o755); err != nil {
			t.Fatalf("mkdir shim: %v", err)
		}
		if err := os.WriteFile(filepath.Join(shimDir, "systemctl.bat"), []byte("@exit /b 0\r\n"), 0o755); err != nil {
			t.Fatalf("write shim: %v", err)
		}
		t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		// Take the NATIVE branch (the docker CLI may exist on the test host
		// without a running daemon, which would test the container path).
		c.forceNativePolicyWrite = true
	}
	want := `{"tagOwners":{"tag:dev-daniil-workpc":["daniil@"]}}`
	if err := c.SetPolicy(want); err != nil {
		t.Fatalf("SetPolicy: %v", err)
	}
	got, _ := os.ReadFile(policyPath)
	if strings.TrimSpace(string(got)) != want {
		t.Errorf("policy file = %q, want %q", string(got), want)
	}
}

// TestB272_SetPolicyUnknownPathExplainsItself: without a configured path the
// error must tell the operator exactly which variable to set (the pre-B272
// behaviour was to guess a hardcoded docker volume and fail obscurely).
func TestB272_SetPolicyUnknownPathExplainsItself(t *testing.T) {
	t.Setenv("SKYGATE_HEADSCALE_CONFIG", filepath.Join(t.TempDir(), "missing.yaml"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"code":2,"message":"update is disabled for modes other than database"}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "k")
	c.PolicyPath = ""
	err := c.SetPolicy(`{}`)
	if err == nil {
		t.Fatal("want an error when the policy path is unknown")
	}
	if !strings.Contains(err.Error(), "SKYGATE_HEADSCALE_POLICY_PATH") {
		t.Errorf("error = %v, want it to name SKYGATE_HEADSCALE_POLICY_PATH", err)
	}
}

// TestB272_TagNodeUsesRESTFirst pins the native-install fix: the REST API is
// attempted before any CLI, so a host without docker can still tag nodes.
func TestB272_TagNodeUsesRESTFirst(t *testing.T) {
	var gotBody map[string]any
	var gotPath, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/node":
			_, _ = w.Write([]byte(`{"nodes":[]}`))
		case r.URL.Path == "/api/v1/node/2/tags" && r.Method == http.MethodPost:
			gotPath, gotMethod = r.URL.Path, r.Method
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "k")
	// A PATH without docker guarantees that a CLI fallback would fail — so a
	// nil return proves the REST path was used.
	t.Setenv("PATH", t.TempDir())

	if err := c.AddTag(2, "tag:dev-daniil-workpc"); err != nil {
		t.Fatalf("AddTag: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/v1/node/2/tags" {
		t.Fatalf("REST call = %s %s, want POST /api/v1/node/2/tags", gotMethod, gotPath)
	}
	tags, _ := gotBody["tags"].([]any)
	if len(tags) != 1 || tags[0] != "tag:dev-daniil-workpc" {
		t.Errorf("body tags = %v, want [tag:dev-daniil-workpc]", gotBody["tags"])
	}
}

// TestB272_TagNodeFallsBackToCLIWhenAPIMissing: a headscale build without the
// tags endpoint must still work through the CLI (which is install-kind aware).
func TestB272_TagNodeFallsBackToCLIWhenAPIMissing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/node" {
			_, _ = w.Write([]byte(`{"nodes":[{"id":"2","name":"2","givenName":"workpc","tags":[]}]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := New(srv.URL, "k")
	t.Setenv("PATH", t.TempDir()) // no docker, no headscale binary
	err := c.TagNode(2, "tag:dev-daniil-workpc")
	if err == nil {
		t.Fatal("want an error when both the API and the CLI are unavailable")
	}
	// The message must name BOTH attempts, so the operator knows what broke.
	if !strings.Contains(err.Error(), "api:") || !strings.Contains(err.Error(), "cli:") {
		t.Errorf("error = %v, want both the api: and cli: attempts", err)
	}
}
