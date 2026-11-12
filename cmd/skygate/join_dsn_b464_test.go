// B464 (2026-11-11) — a join that produced a node with NO DATABASE must not exit 0.
//
// THE MEASUREMENT (operator's standby, live): `skygate cluster join` wrote
// /etc/skygate/dbs.env containing the literal line
//
//	# SKYGATE_DB_DSN=<unset — primary returned no DSN; set this manually …>
//
// printed a hint, and exited 0. The hub then held a `skygate-standby` with no
// database, and every later step (heartbeat, /admin/cluster, the "is this host ready"
// question) inherited that belief. The exit code is the one signal nobody can
// overlook.
//
// The ordering pinned below is deliberate and is the reason this test asserts on a
// SIDE EFFECT as well as an error: the invite is already consumed on the primary when
// the CLI finds out (Join refuses a used invite with 409), so the state file must
// exist even on the failing path — otherwise "fix and re-run" would need a fresh
// invite, and the node_id the heartbeat daemon needs would be lost.
package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"skygate/internal/cluster"
)

// b464FakePrimary serves POST /api/cluster/join with the given bootstrap payload.
func b464FakePrimary(t *testing.T, dsn string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/cluster/join" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(cluster.JoinResponse{
			ClusterID:     "skygate-staging",
			NodeID:        "node-disc-svyatoslava",
			Hostname:      "svyatoslava",
			DSN:           dsn,
			HeartbeatHint: 30,
		})
	}))
}

// b464JoinArgs builds the flag set. SKYGATE_SECRET_KEY is cleared so the local token
// sanity check (a soft warning, B212) does not fight the fake token.
func b464JoinArgs(t *testing.T, apiURL string, extra ...string) []string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("SKYGATE_SECRET_KEY", "")
	args := []string{
		"--api-url=" + apiURL,
		"--state-file=" + filepath.Join(dir, "cluster-state.json"),
		"--write-dsn-to=" + filepath.Join(dir, "dbs.env"),
		"--no-heartbeat-hint",
	}
	args = append(args, extra...)
	return append(args, "sgn1.eyJpbnYiOiJ4In0.c2ln")
}

// TestB464_JoinFailsWhenThePrimaryReturnsNoDSN — the contract.
func TestB464_JoinFailsWhenThePrimaryReturnsNoDSN(t *testing.T) {
	srv := b464FakePrimary(t, "")
	defer srv.Close()

	args := b464JoinArgs(t, srv.URL)
	err := runClusterJoin(args)
	if err == nil {
		t.Fatal("join with no DSN exited 0 — that is the silent failure B464 removes")
	}
	msg := err.Error()
	for _, want := range []string{"NO DATABASE", "--allow-no-dsn", "do NOT re-run"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error does not mention %q: %s", want, msg)
		}
	}
	// The state file must still exist: the invite is consumed and the node_id is needed.
	statePath := strings.TrimPrefix(args[1], "--state-file=")
	if _, statErr := os.Stat(statePath); statErr != nil {
		t.Fatalf("state file was not written on the failing path (%v) — a re-run would need a new invite", statErr)
	}
	if _, statErr := os.Stat(strings.TrimPrefix(args[2], "--write-dsn-to=")); statErr != nil {
		t.Fatalf("dbs.env was not written on the failing path (%v)", statErr)
	}
}

// TestB464_JoinSucceedsWithAllowNoDSN — the escape hatch for the OTHER legitimate
// topology: a node that keeps its own local database is a cluster member, but it is
// not a mirror, and the operator has to say so out loud.
func TestB464_JoinSucceedsWithAllowNoDSN(t *testing.T) {
	srv := b464FakePrimary(t, "")
	defer srv.Close()

	if err := runClusterJoin(b464JoinArgs(t, srv.URL, "--allow-no-dsn")); err != nil {
		t.Fatalf("join with --allow-no-dsn = %v, want nil", err)
	}
}

// TestB464_JoinWithADSNStaysQuiet — the happy path must not be affected: a DSN in the
// response means the node is a mirror candidate and the command succeeds.
func TestB464_JoinWithADSNStaysQuiet(t *testing.T) {
	srv := b464FakePrimary(t, "postgres://admin@node-disc-primary:5432/skygate_staging")
	defer srv.Close()

	if err := runClusterJoin(b464JoinArgs(t, srv.URL)); err != nil {
		t.Fatalf("join with a DSN = %v, want nil", err)
	}
}
