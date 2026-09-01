// v1.5.0+ / B200 — unit tests for the cluster/node
// package. The node helpers (AddNode / RemoveNode /
// LookupNode) all hit the DB, so most of the test
// surface is the round-trip-safe TEXT[] formatter
// (pqStringArray) and parser (parsePGTextArray) plus
// the sentinel errors.

package cluster

import "testing"

func TestPQStringArray(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want string
	}{
		{"nil", nil, "{}"},
		{"empty", []string{}, "{}"},
		{"single", []string{"skygate"}, "{skygate}"},
		{"two", []string{"skygate", "patroni-primary"}, "{skygate,patroni-primary}"},
		// NOTE: values with special chars (", , \) are wrapped
		// in double-quotes with backslash escaping. The parse
		// function below does NOT yet understand these escapes
		// (it just strips the outer quotes). That's fine for
		// our use case (roles = "skygate", "patroni-primary",
		// etc. — no special chars), but a future improvement
		// would be a real escape-aware parser.
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := pqStringArray(c.in)
			if got != c.want {
				t.Errorf("pqStringArray(%v) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestParsePGTextArray(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"empty braces", "{}", nil},
		{"null", "NULL", nil},
		{"single", "{skygate}", []string{"skygate"}},
		{"two", "{skygate,patroni-primary}", []string{"skygate", "patroni-primary"}},
		{"spaces", "{a, b, c}", []string{"a", "b", "c"}},
		{"trailing comma", "{a,}", []string{"a"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parsePGTextArray(c.in)
			if !slicesEqual(got, c.want) {
				t.Errorf("parsePGTextArray(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestPQStringArray_RoundTrip(t *testing.T) {
	// Round-trip a few values: pqStringArray → parsePGTextArray
	// should give back the original. The escape rules only
	// cover the rare cases; we test the common path.
	cases := [][]string{
		nil,
		{},
		{"skygate"},
		{"skygate", "patroni-primary"},
		{"skygate", "skygate-standby", "patroni-primary", "patroni-replica"},
	}
	for i, in := range cases {
		got := parsePGTextArray(pqStringArray(in))
		if !slicesEqual(got, in) {
			t.Errorf("case %d: round-trip lost data: %v → %v → %v", i, in, pqStringArray(in), got)
		}
	}
}

func TestNodeStateConstants(t *testing.T) {
	// Pinned values — change detection if a refactor
	// silently renames a state.
	if NodeStatePending != "pending" {
		t.Errorf("NodeStatePending = %q, want %q", NodeStatePending, "pending")
	}
	if NodeStateReady != "ready" {
		t.Errorf("NodeStateReady = %q, want %q", NodeStateReady, "ready")
	}
	if NodeStateDraining != "draining" {
		t.Errorf("NodeStateDraining = %q, want %q", NodeStateDraining, "draining")
	}
	if NodeStateFailed != "failed" {
		t.Errorf("NodeStateFailed = %q, want %q", NodeStateFailed, "failed")
	}
}

func TestNodeRoleConstants(t *testing.T) {
	if NodeRoleSkygate != "skygate" {
		t.Errorf("NodeRoleSkygate = %q, want %q", NodeRoleSkygate, "skygate")
	}
	if NodeRoleStandby != "skygate-standby" {
		t.Errorf("NodeRoleStandby = %q, want %q", NodeRoleStandby, "skygate-standby")
	}
	if NodeRolePatroniPrimary != "patroni-primary" {
		t.Errorf("NodeRolePatroniPrimary = %q, want %q", NodeRolePatroniPrimary, "patroni-primary")
	}
	if NodeRolePatroniReplica != "patroni-replica" {
		t.Errorf("NodeRolePatroniReplica = %q, want %q", NodeRolePatroniReplica, "patroni-replica")
	}
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
