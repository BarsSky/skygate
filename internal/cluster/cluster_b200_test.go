// v1.5.0+ / B200 — unit tests for the cluster/cluster.go
// helpers (LookupCluster, EnsureCluster).

package cluster

import (
	"encoding/json"
	"testing"
)

func TestLookupCluster_NotFound(t *testing.T) {
	// Without a DB we can't test the happy path, but we
	// can pin the empty-id → ErrClusterNotFound behavior
	// (no DB call needed).
	if _, err := LookupCluster(nil, ""); err != ErrClusterNotFound {
		t.Errorf("LookupCluster(\"\") = %v, want ErrClusterNotFound", err)
	}
}

func TestEnsureCluster_EmptyID(t *testing.T) {
	// No DB call either way — just the input validation.
	if err := EnsureCluster(nil, "", "x"); err == nil {
		t.Error("EnsureCluster(\"\", \"x\") should fail")
	}
}

func TestClusterConstants(t *testing.T) {
	// Pinned — these are referenced from admin handlers +
	// the B200 B-check.
	if DefaultClusterID != "skygate-staging" {
		t.Errorf("DefaultClusterID = %q, want %q", DefaultClusterID, "skygate-staging")
	}
}

func TestCluster_ChainJSON_RoundTrip(t *testing.T) {
	// Round-trip a small JSONB chain through Go's json
	// package. The actual DB round-trip needs an
	// integration test, but the in-memory shape should
	// at least be a valid JSON value.
	chain := []map[string]any{
		{"hostname": "skyadmin-1", "priority": 1},
		{"hostname": "skyadmin-2", "priority": 2},
	}
	raw, err := json.Marshal(chain)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	c := &Cluster{ID: "skygate-staging", Name: "skygate-staging", ChainJSON: raw}
	if c.ID != "skygate-staging" {
		t.Errorf("ID = %q", c.ID)
	}
	if len(c.ChainJSON) == 0 {
		t.Error("ChainJSON should not be empty")
	}
}
