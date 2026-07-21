package headscale

// 2026-07-21: v0.23.0 Phase 1.
//
// Tests for the per-user headscale bootstrap helpers. These
// tests run WITHOUT docker (the bootstrap script itself is
// tested live on the VM; the Go side is responsible for
// shell-out + JSON parse, which is pure I/O we can exercise
// in-process by swapping the script path with a tiny shim).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeScript drops a small shell script at <dir>/<name> that
// prints the given JSON (or error) to stdout / stderr. Used
// by ProvisionUser / DecommissionUser tests to swap the real
// bootstrap script for a deterministic in-test fixture.
func writeScript(t *testing.T, dir, name, stdoutJSON, stderrText, exitCode string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	body := "#!/usr/bin/env bash\n"
	if stdoutJSON != "" {
		body += "cat <<'JSON'\n" + stdoutJSON + "\nJSON\n"
	}
	if stderrText != "" {
		body += "echo " + stderrText + " >&2\n"
	}
	if exitCode != "" {
		body += "exit " + exitCode + "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	return path
}

// withScriptPaths swaps the package-level script paths for the
// duration of the test. Returns a cleanup func that restores
// the originals.
func withScriptPaths(t *testing.T, bootstrap, deprovision string) func() {
	t.Helper()
	origBoot := BootstrapScriptPath
	origDepr := DeprovisionScriptPath
	BootstrapScriptPath = bootstrap
	DeprovisionScriptPath = deprovision
	return func() {
		BootstrapScriptPath = origBoot
		DeprovisionScriptPath = origDepr
	}
}

// TestProvisionUser_ParsesValidJSON — the happy path: script
// prints a well-formed JSON object on stdout, ProvisionUser
// parses it into a ProvisionResult with all the expected
// fields populated. No docker round-trip, no actual headscale
// container — the Go side is the boundary under test.
func TestProvisionUser_ParsesValidJSON(t *testing.T) {
	dir := t.TempDir()
	bootstrap := writeScript(t, dir, "headscale-bootstrap.sh",
		`{"username":"skyadmin","container":"headscale-skyadmin","url":"http://headscale-skyadmin:50450","api_key":"hskey-api-abc123","http_port":50450,"grpc_port":51450,"metrics_port":52450,"base_domain":"skyadmin.tsnet.skynas.ru","headscale_user_id":42}`,
		"", "0")
	defer withScriptPaths(t, bootstrap, "/nonexistent")()

	got, err := ProvisionUser("skyadmin", 1)
	if err != nil {
		t.Fatalf("ProvisionUser: %v", err)
	}
	if got.Username != "skyadmin" {
		t.Errorf("Username = %q, want skyadmin", got.Username)
	}
	if got.Container != "headscale-skyadmin" {
		t.Errorf("Container = %q, want headscale-skyadmin", got.Container)
	}
	if got.URL != "http://headscale-skyadmin:50450" {
		t.Errorf("URL = %q, want http://headscale-skyadmin:50450", got.URL)
	}
	if got.APIKey != "hskey-api-abc123" {
		t.Errorf("APIKey = %q, want hskey-api-abc123", got.APIKey)
	}
	if got.HTTPPort != 50450 {
		t.Errorf("HTTPPort = %d, want 50450", got.HTTPPort)
	}
	if got.HeadscaleUserID != 42 {
		t.Errorf("HeadscaleUserID = %d, want 42", got.HeadscaleUserID)
	}
}

// TestProvisionUser_StripsPreJSONOutput — the real bootstrap
// script emits docker compose progress messages before the
// final JSON. ProvisionUser must find the FIRST '{' in the
// output and parse from there, ignoring the docker noise.
func TestProvisionUser_StripsPreJSONOutput(t *testing.T) {
	dir := t.TempDir()
	bootstrap := writeScript(t, dir, "headscale-bootstrap.sh",
		`Pulling headscale-skyadmin ... done
Container headscale-skyadmin  Started
{"username":"skyadmin","container":"headscale-skyadmin","url":"http://headscale-skyadmin:50451","api_key":"hskey-api-xyz789","http_port":50451,"grpc_port":51451,"metrics_port":52451,"base_domain":"skyadmin.tsnet.skynas.ru","headscale_user_id":7}`,
		"", "0")
	defer withScriptPaths(t, bootstrap, "/nonexistent")()

	got, err := ProvisionUser("skyadmin", 1)
	if err != nil {
		t.Fatalf("ProvisionUser: %v", err)
	}
	if got.HTTPPort != 50451 || got.HeadscaleUserID != 7 {
		t.Errorf("got %+v, want port=50451 hs_user_id=7", got)
	}
}

// TestProvisionUser_ScriptFails — the script exits non-zero
// (e.g. container already exists, docker socket missing).
// ProvisionUser must surface the exit code + stderr in the
// error message so the admin UI can show it verbatim.
func TestProvisionUser_ScriptFails(t *testing.T) {
	dir := t.TempDir()
	bootstrap := writeScript(t, dir, "headscale-bootstrap.sh",
		"", "container headscale-skyadmin already exists", "1")
	defer withScriptPaths(t, bootstrap, "/nonexistent")()

	_, err := ProvisionUser("skyadmin", 1)
	if err == nil {
		t.Fatal("ProvisionUser: want error, got nil")
	}
	// Error message must mention both the exit code and the
	// stderr text (so the admin sees the actual reason).
	msg := err.Error()
	if !strings.Contains(msg, "exit 1") {
		t.Errorf("error = %q, want it to mention exit code 1", msg)
	}
	if !strings.Contains(msg, "already exists") {
		t.Errorf("error = %q, want it to mention stderr text", msg)
	}
}

// TestProvisionUser_MalformedJSON — the script's output
// doesn't contain a parseable JSON object. ProvisionUser
// must fail with a clear "no JSON found" error rather than
// silently returning a zero ProvisionResult.
func TestProvisionUser_MalformedJSON(t *testing.T) {
	dir := t.TempDir()
	bootstrap := writeScript(t, dir, "headscale-bootstrap.sh",
		"no json here, just plain text", "", "0")
	defer withScriptPaths(t, bootstrap, "/nonexistent")()

	_, err := ProvisionUser("skyadmin", 1)
	if err == nil {
		t.Fatal("ProvisionUser: want error, got nil")
	}
	if !strings.Contains(err.Error(), "no JSON found") {
		t.Errorf("error = %q, want 'no JSON found'", err.Error())
	}
}

// TestProvisionUser_MissingRequiredFields — the script
// returns a JSON that's missing url/api_key/container. The
// helper must catch this (we'd otherwise persist an empty
// override and the user's HSForUser call would 404 on the
// empty URL).
func TestProvisionUser_MissingRequiredFields(t *testing.T) {
	dir := t.TempDir()
	bootstrap := writeScript(t, dir, "headscale-bootstrap.sh",
		`{"username":"skyadmin","http_port":50450}`,
		"", "0")
	defer withScriptPaths(t, bootstrap, "/nonexistent")()

	_, err := ProvisionUser("skyadmin", 1)
	if err == nil {
		t.Fatal("ProvisionUser: want error on missing required fields, got nil")
	}
	if !strings.Contains(err.Error(), "missing required fields") {
		t.Errorf("error = %q, want 'missing required fields'", err.Error())
	}
}

// TestProvisionUser_EmptyUsername — defensive: the helper
// must reject empty usernames (which would otherwise produce
// a script call with empty args and a confusing shell error).
func TestProvisionUser_EmptyUsername(t *testing.T) {
	_, err := ProvisionUser("", 1)
	if err == nil {
		t.Fatal("ProvisionUser(empty): want error, got nil")
	}
	if !strings.Contains(err.Error(), "empty username") {
		t.Errorf("error = %q, want 'empty username'", err.Error())
	}
}

// TestProvisionUser_InvalidUID — defensive: uid <= 0 is
// rejected without invoking the script.
func TestProvisionUser_InvalidUID(t *testing.T) {
	_, err := ProvisionUser("skyadmin", 0)
	if err == nil {
		t.Fatal("ProvisionUser(uid=0): want error, got nil")
	}
	if !strings.Contains(err.Error(), "invalid uid") {
		t.Errorf("error = %q, want 'invalid uid'", err.Error())
	}
}

// TestDecommissionUser_HappyPath — script exits 0, no error.
func TestDecommissionUser_HappyPath(t *testing.T) {
	dir := t.TempDir()
	deprovision := writeScript(t, dir, "headscale-deprovision.sh",
		"OK: deprovisioned skyadmin", "", "0")
	defer withScriptPaths(t, "/nonexistent", deprovision)()

	if err := DecommissionUser("skyadmin"); err != nil {
		t.Errorf("DecommissionUser: %v", err)
	}
}

// TestDecommissionUser_ScriptFails — the script refuses
// (e.g. container not found). DecommissionUser returns the
// wrapped error verbatim so the admin can see why.
func TestDecommissionUser_ScriptFails(t *testing.T) {
	dir := t.TempDir()
	deprovision := writeScript(t, dir, "headscale-deprovision.sh",
		"", "no such container", "1")
	defer withScriptPaths(t, "/nonexistent", deprovision)()

	err := DecommissionUser("skyadmin")
	if err == nil {
		t.Fatal("DecommissionUser: want error, got nil")
	}
	if !strings.Contains(err.Error(), "no such container") {
		t.Errorf("error = %q, want it to mention stderr text", err.Error())
	}
}

// TestProvisionResult_JSONRoundTrip — the JSON tags on
// ProvisionResult must be stable (the script writes them,
// the Go side reads them, the encrypted DB write is a
// separate path that doesn't touch these fields). A round-
// trip test catches accidental field renames.
func TestProvisionResult_JSONRoundTrip(t *testing.T) {
	in := ProvisionResult{
		Username:        "skyadmin",
		Container:       "headscale-skyadmin",
		URL:             "http://headscale-skyadmin:50450",
		APIKey:          "hskey-api-roundtrip",
		HTTPPort:        50450,
		GRPCPort:        51450,
		MetricsPort:     52450,
		BaseDomain:      "skyadmin.tsnet.skynas.ru",
		HeadscaleUserID: 42,
	}
	body, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out ProvisionResult
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out != in {
		t.Errorf("round-trip mismatch:\n  in:  %+v\n  out: %+v", in, out)
	}
}
