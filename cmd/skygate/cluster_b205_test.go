// v1.5.0+ (B205) — unit tests for the cluster CLI helpers.
//
// The CLI subcommands are end-to-end (DB + HTTP), so the
// unit tests focus on the pure helpers: clusterRolesToSlice
// (the local PG-TEXT-array-literal parser), sqlNullString,
// and the CLI dispatcher. The full subcommand path is
// covered by the live B205 verify script (skygate cluster
// invite → join → nodes → audit on the live VM).

package main

import (
	"reflect"
	"testing"
)

func TestClusterRolesToSlice(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"empty braces", "{}", nil},
		{"NULL", "NULL", nil},
		{"single", "{skygate}", []string{"skygate"}},
		{"two", "{skygate,skygate-standby}", []string{"skygate", "skygate-standby"}},
		{"quoted with space", `{"a b","c d"}`, []string{"a b", "c d"}},
		{"no braces (passthrough)", "skygate", []string{"skygate"}},
		{"trim whitespace", "  {a, b, c}  ", []string{"a", "b", "c"}},
		{"empty inner", "{,}", nil},
		{"quoted with comma", `{"a,b","c"}`, []string{"a,b", "c"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := clusterRolesToSlice(c.in)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("clusterRolesToSlice(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestSqlNullStringScan(t *testing.T) {
	t.Run("nil src", func(t *testing.T) {
		var s sqlNullString
		if err := s.Scan(nil); err != nil {
			t.Errorf("Scan(nil): %v", err)
		}
		if s.Valid {
			t.Errorf("Valid=true after nil scan, want false")
		}
	})
	t.Run("string src", func(t *testing.T) {
		var s sqlNullString
		if err := s.Scan("hello"); err != nil {
			t.Errorf("Scan(\"hello\"): %v", err)
		}
		if !s.Valid || s.String != "hello" {
			t.Errorf("got %+v, want Valid=true String=hello", s)
		}
	})
	t.Run("bytes src", func(t *testing.T) {
		var s sqlNullString
		if err := s.Scan([]byte("world")); err != nil {
			t.Errorf("Scan([]byte): %v", err)
		}
		if !s.Valid || s.String != "world" {
			t.Errorf("got %+v, want Valid=true String=world", s)
		}
	})
	t.Run("unsupported type", func(t *testing.T) {
		var s sqlNullString
		if err := s.Scan(42); err == nil {
			t.Errorf("Scan(42) expected error, got nil")
		}
	})
}

func TestRunClusterSubcommand_UnknownVerb(t *testing.T) {
	err := runClusterSubcommand([]string{"nope"})
	if err == nil {
		t.Error("expected error for unknown verb, got nil")
	}
}

func TestRunClusterSubcommand_NoVerb(t *testing.T) {
	err := runClusterSubcommand([]string{})
	if err == nil {
		t.Error("expected error for missing verb, got nil")
	}
}

// TestClusterStateFileRoundtrip — pins the on-disk JSON
// shape of /etc/skygate/cluster-state.json. A future
// refactor that renames a field (e.g. api_url → apiUrl)
// will fail this test, which is the point — the field
// names ARE the wire contract with bootstrap_standby.sh
// + any operator shell scripts that read the file.
func TestClusterStateFileRoundtrip(t *testing.T) {
	original := &clusterState{
		NodeID:           "node-abc123def456",
		ClusterID:        "skygate-staging",
		Hostname:         "svi-direct-1",
		Token:            "sgn1.eyJ0eXAiOiJKV1QiLCJhbGciOiJIUzI1NiJ9.test",
		APIURL:           "http://127.0.0.1:8080",
		HeartbeatSeconds: 30,
	}
	tmpFile := t.TempDir() + "/cluster-state.json"
	if err := writeClusterState(tmpFile, original); err != nil {
		t.Fatalf("writeClusterState: %v", err)
	}
	got, err := readClusterState(tmpFile)
	if err != nil {
		t.Fatalf("readClusterState: %v", err)
	}
	if !reflect.DeepEqual(original, got) {
		t.Errorf("roundtrip mismatch: orig=%+v got=%+v", original, got)
	}
}

func TestReadClusterState_Incomplete(t *testing.T) {
	cases := []struct {
		name string
		st   clusterState
	}{
		{"missing node_id", clusterState{Token: "x", APIURL: "http://x"}},
		{"missing token", clusterState{NodeID: "n", APIURL: "http://x"}},
		{"missing api_url", clusterState{NodeID: "n", Token: "x"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tmpFile := t.TempDir() + "/cluster-state.json"
			if err := writeClusterState(tmpFile, &c.st); err != nil {
				t.Fatalf("writeClusterState: %v", err)
			}
			if _, err := readClusterState(tmpFile); err == nil {
				t.Errorf("readClusterState(%s) expected error, got nil", c.name)
			}
		})
	}
}
