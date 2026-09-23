package headscale

// preauth_b311_test.go — B311 (2026-09-23).
//
// The live failure on the native host `aro` when the operator pressed
// «Сгенерировать ключ»:
//
//	api: headscale POST /api/v1/preauthkey: 500 {"code":2,
//	     "message":"auth-key must be either tagged or owned by user"}
//	cli: docker exec: exec: "docker": executable file not found in $PATH
//
// Two defects, one request body and one install-kind assumption:
//
//   - headscale 0.29.x reads the key's owner from `user`, and DISCARDS the
//     unknown `user_id` (measured against the live API: {"user":85} → key
//     created, {"user_id":85} → the 500 above), so the owner looked unset;
//   - the CLI rung hardcoded `docker exec`, which cannot exist on a native
//     install — the same defect B267/B272 fixed for routes and tags.
//
// These tests pin both halves without a daemon: the request body on the wire,
// and the argv the install-kind ladder hands to its runner.

import (
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestB311_NestedResponseIsTheSuccessfulPath covers the fifth defect found in
// this block, and the reason the operator saw BOTH errors at once: headscale
// 0.29.x answers a create with {"preAuthKey": {…}}, `user` as an OBJECT and
// `expiration` as a protobuf Timestamp. The old flat struct matched none of that,
// so a 200 with a real key looked like "no key in the response" and the caller
// fell through to the CLI rung — which on `aro` (no docker) then produced the
// second half of the report.
func TestB311_NestedResponseIsTheSuccessfulPath(t *testing.T) {
	body := `{"preAuthKey":{"id":"411","key":"hskey-nested","reusable":false,` +
		`"ephemeral":false,"used":false,"expiration":{"seconds":1790000000,"nanos":0},` +
		`"aclTags":["tag:exit-node"],"user":{"id":"85","name":"infra",` +
		`"createdAt":"2026-08-10T19:45:06.787440691Z"}}}`
	_, c, _ := fakePreauthHS(t, http.StatusOK, body)
	c.ExecContainer = "headscale"
	cliCalled := false
	c.SetDockerRunner(func(args ...string) ([]byte, error) {
		cliCalled = true
		return nil, os.ErrNotExist
	})
	pk, err := c.CreatePreauthKeyWithTags(85, "1h", false, []string{"tag:exit-node"})
	if err != nil {
		t.Fatalf("a 200 with the real 0.29.x body must be the successful path: %v", err)
	}
	if pk.Key != "hskey-nested" {
		t.Errorf("key = %q, want hskey-nested", pk.Key)
	}
	if pk.ID != "411" {
		t.Errorf("id = %q, want 411", pk.ID)
	}
	if pk.UserID != 85 || pk.UserName != "infra" {
		t.Errorf("user = (%d, %q), want (85, infra) from the nested object", pk.UserID, pk.UserName)
	}
	if len(pk.ACLTags) != 1 || pk.ACLTags[0] != "tag:exit-node" {
		t.Errorf("aclTags = %v, want [tag:exit-node] (so a caller can VERIFY the tag landed)", pk.ACLTags)
	}
	if pk.Expiration == "" {
		t.Error("expiration was not decoded from the protobuf Timestamp object")
	}
	if cliCalled {
		t.Error("the CLI rung was attempted although the API answered 200 with a key")
	}
}

// TestB311_FlatLegacyResponseStillParses keeps the older flat shape working.
func TestB311_FlatLegacyResponseStillParses(t *testing.T) {
	_, c, _ := fakePreauthHS(t, http.StatusOK, `{"id":"42","key":"hskey-flat","user_id":7,"reusable":true,"expiration":"2030-01-01T00:00:00Z"}`)
	pk, err := c.CreatePreauthKey(7, "1h", true)
	if err != nil {
		t.Fatalf("flat legacy response: %v", err)
	}
	if pk.Key != "hskey-flat" || pk.UserID != 7 || !pk.Reusable {
		t.Errorf("parsed = %+v, want the flat response decoded as before", pk)
	}
}

// TestB311_RequestBodyNamesTheOwnerAsUser pins the field headscale reads.
func TestB311_RequestBodyNamesTheOwnerAsUser(t *testing.T) {
	_, c, cap := fakePreauthHS(t, http.StatusOK, `{"id":"42","key":"hskey-test"}`)
	if _, err := c.CreatePreauthKeyWithTags(7, "1h", false, []string{"tag:exit-node"}); err != nil {
		t.Fatalf("CreatePreauthKeyWithTags: %v", err)
	}
	if !strings.Contains(cap.body, `"user":7`) {
		t.Errorf("body = %s, want `\"user\":7` (the field headscale 0.29.x reads)", cap.body)
	}
	if strings.Contains(cap.body, `"user_id"`) {
		t.Errorf("body = %s, must NOT carry the ignored `user_id`", cap.body)
	}
	// The tag field is `acl_tags`. Measured on v0.29.3: `tags` is silently
	// dropped (the reply came back with aclTags: []), so a key minted for
	// tag:exit-node would register an UNTAGGED node.
	if !strings.Contains(cap.body, `"acl_tags":["tag:exit-node"]`) {
		t.Errorf("body = %s, want `\"acl_tags\":[\"tag:exit-node\"]`", cap.body)
	}
	if strings.Contains(cap.body, `"tags":`) {
		t.Errorf("body = %s, must NOT use the silently-ignored `tags` field", cap.body)
	}
}

// TestB311_ExpirePrefersTheRouteThisHeadscaleServes pins the expire rungs:
// 0.29.x answers POST /api/v1/preauthkey/expire with {"id":…} and 404s the
// PUT /{id}/expire spelling the code used to try first.
func TestB311_ExpirePrefersTheRouteThisHeadscaleServes(t *testing.T) {
	var paths, bodies []string
	srv, c, _ := fakePreauthHS(t, http.StatusOK, `{}`)
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 512)
		n, _ := r.Body.Read(buf)
		paths = append(paths, r.Method+" "+r.URL.Path)
		bodies = append(bodies, string(buf[:n]))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	})
	if err := c.ExpirePreauthKey(85, "411"); err != nil {
		t.Fatalf("ExpirePreauthKey: %v", err)
	}
	if len(paths) == 0 || paths[0] != "POST /api/v1/preauthkey/expire" {
		t.Errorf("first expire rung = %v, want POST /api/v1/preauthkey/expire", paths)
	}
	if len(bodies) == 0 || !strings.Contains(bodies[0], `"id":"411"`) {
		t.Errorf("first expire body = %v, want {\"id\":\"411\"}", bodies)
	}
}

// TestB311_ExpireCLIRungHasNoUserFlag pins the flag spelling: 0.29.x's
// `preauthkeys expire` has exactly one flag (`-i/--id`), so the old
// `-u <user>` argv could only ever fail with "unknown flag: -u".
func TestB311_ExpireCLIRungHasNoUserFlag(t *testing.T) {
	dir := t.TempDir()
	shim := filepath.Join(dir, "docker")
	if runtime.GOOS == "windows" {
		shim = filepath.Join(dir, "docker.bat")
	}
	if err := os.WriteFile(shim, []byte("#!/bin/sh\nexit 0\n@echo off\r\n"), 0o755); err != nil {
		t.Fatalf("write docker shim: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// Both API rungs must fail so the CLI rung is reached.
	srv, c, _ := fakePreauthHS(t, http.StatusNotFound, `{"code":5,"message":"Not Found"}`)
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":5,"message":"Not Found"}`))
	})
	c.ExecContainer = "headscale"
	var got []string
	c.SetDockerRunner(func(args ...string) ([]byte, error) {
		got = args
		return []byte("expired"), nil
	})
	if err := c.ExpirePreauthKey(85, "411"); err != nil {
		t.Fatalf("CLI rung must succeed: %v", err)
	}
	joined := strings.Join(got, " ")
	if strings.Contains(joined, "-u") || strings.Contains(joined, "--user") {
		t.Errorf("expire argv = %q, must not pass a user flag (it does not exist)", joined)
	}
	for _, want := range []string{"preauthkeys expire", "--id 411", "--force"} {
		if !strings.Contains(joined, want) {
			t.Errorf("expire argv = %q, want it to contain %q", joined, want)
		}
	}
}

// TestB311_CLIRungUsesTheInstallKindLadder is the `aro` half: the CLI fallback
// must go through runHeadscaleCLI (which prepends `docker exec <container> <bin>`
// only when docker exists) instead of hardcoding docker itself.
//
// A fake `docker` on PATH makes the ladder choose the docker rung, which lets
// the test observe the exact argv the preauth path contributes.
func TestB311_CLIRungUsesTheInstallKindLadder(t *testing.T) {
	dir := t.TempDir()
	// The shim only has to be FOUND by exec.LookPath — the ladder then calls the
	// injected runner instead of executing it, so a .bat stub is enough on
	// Windows and a shell script is enough everywhere else.
	shim := filepath.Join(dir, "docker")
	if runtime.GOOS == "windows" {
		shim = filepath.Join(dir, "docker.bat")
	}
	if err := os.WriteFile(shim, []byte("#!/bin/sh\nexit 0\n@echo off\r\n"), 0o755); err != nil {
		t.Fatalf("write docker shim: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	_, c, _ := fakePreauthHS(t, http.StatusInternalServerError, "api down (simulating the aro 500)")
	c.ExecContainer = "headscale"
	var got []string
	c.SetDockerRunner(func(args ...string) ([]byte, error) {
		got = args
		return []byte(`{"id":"9","key":"hskey-via-cli"}`), nil
	})
	k, err := c.CreatePreauthKeyWithTags(7, "1h", false, []string{"tag:exit-node"})
	if err != nil {
		t.Fatalf("CLI fallback must succeed through the ladder: %v", err)
	}
	if k.Key != "hskey-via-cli" {
		t.Errorf("key = %q, want hskey-via-cli", k.Key)
	}
	joined := strings.Join(got, " ")
	for _, want := range []string{"exec headscale", "preauthkeys create", "-u 7", "--tags tag:exit-node", "--output json"} {
		if !strings.Contains(joined, want) {
			t.Errorf("CLI argv = %q, want it to contain %q", joined, want)
		}
	}
	// The ladder owns the docker invocation: the preauth layer must not add its
	// own "docker"/"exec" prefix on top of it.
	if strings.Count(joined, "exec") != 1 {
		t.Errorf("CLI argv = %q, want exactly one `exec` (added by the ladder)", joined)
	}
}

// TestB311_NativeInstallRunsTheLocalBinary is the shape that actually failed:
// no docker in PATH at all. The ladder must fall through to the `headscale`
// binary instead of dying with `exec: "docker": executable file not found`.
func TestB311_NativeInstallRunsTheLocalBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH-shim test is POSIX-only; the ladder itself is covered on Linux CI")
	}
	dir := t.TempDir()
	hs := filepath.Join(dir, "headscale")
	script := "#!/bin/sh\n" +
		"echo \"$@\" > " + filepath.Join(dir, "argv.txt") + "\n" +
		"echo '{\"id\":\"11\",\"key\":\"hskey-native\"}'\n"
	if err := os.WriteFile(hs, []byte(script), 0o755); err != nil {
		t.Fatalf("write headscale shim: %v", err)
	}
	// ONLY the shim dir on PATH => exec.LookPath("docker") fails, exactly like
	// the native host, while `headscale` resolves.
	t.Setenv("PATH", dir)

	_, c, _ := fakePreauthHS(t, http.StatusInternalServerError, "api down")
	c.ExecContainer = "headscale" // configured, but docker does not exist here
	c.SetDockerRunner(func(args ...string) ([]byte, error) {
		t.Errorf("the docker rung must not be used without docker on PATH: %v", args)
		return nil, os.ErrNotExist
	})
	k, err := c.CreatePreauthKey(7, "1h", false)
	if err != nil {
		t.Fatalf("native install must use the local headscale binary: %v", err)
	}
	if k.Key != "hskey-native" {
		t.Errorf("key = %q, want hskey-native (from the local binary)", k.Key)
	}
	argv, rerr := os.ReadFile(filepath.Join(dir, "argv.txt"))
	if rerr != nil {
		t.Fatalf("the local binary was not executed: %v", rerr)
	}
	got := string(argv)
	if !strings.Contains(got, "preauthkeys create") || !strings.Contains(got, "-u 7") {
		t.Errorf("local binary argv = %q, want `preauthkeys create -u 7 …`", strings.TrimSpace(got))
	}
	if strings.Contains(got, "exec") || strings.Contains(got, "docker") {
		t.Errorf("local binary argv = %q, must not carry a docker prefix", strings.TrimSpace(got))
	}
}
