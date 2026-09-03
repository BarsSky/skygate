// v1.5.0+ / B218 — unit tests for the Phase 2.5
// `skygate init` refactor: role presets + standby
// mode detection.
//
// The presets + standby detection are pure Go
// (no DB), so the unit tests cover them thoroughly.
// The end-to-end init flow (which requires a real
// DB + .env) is exercised by the B218 live-verify.

package main

import (
	"reflect"
	"testing"
)

func TestParseRolesCSV_Presets(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		// B218 presets
		{"primary preset", "primary", []string{"skygate", "patroni-primary", "control"}},
		{"standby preset", "standby", []string{"skygate-standby", "patroni-replica"}},
		{"db-replica preset", "db-replica", []string{"patroni-replica"}},
		{"control preset", "control", []string{"skygate", "control"}},
		// B218 presets with whitespace (operator typos
		// "  standby  " — we trim and match).
		{"standby with whitespace", "  standby  ", []string{"skygate-standby", "patroni-replica"}},
		// B211 backward compat: explicit role lists
		// (the pre-B218 API) still work.
		{"explicit primary roles", "skygate,patroni-primary,control",
			[]string{"skygate", "patroni-primary", "control"}},
		{"explicit standby roles", "skygate-standby,patroni-replica",
			[]string{"skygate-standby", "patroni-replica"}},
		// Edge: empty input (returns []string{} not nil —
		// the B211 test pins this for JSON encoding).
		{"empty", "", []string{}},
		{"only whitespace", "   ", []string{}},
		// Edge: trailing comma + double comma
		{"trailing comma", "skygate,", []string{"skygate"}},
		{"double comma", "skygate,,patroni-primary", []string{"skygate", "patroni-primary"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseRolesCSV(c.in)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("parseRolesCSV(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestIsStandbyRole(t *testing.T) {
	cases := []struct {
		name  string
		roles []string
		want  bool
	}{
		// Pure primary
		{"primary only", []string{"skygate"}, false},
		{"primary + extras", []string{"skygate", "patroni-primary", "control"}, false},
		// Pure standby
		{"standby only", []string{"skygate-standby"}, true},
		{"standby preset", []string{"skygate-standby", "patroni-replica"}, true},
		// Edge: both skygate and skygate-standby
		// (invalid, but defensible to treat as primary
		// because skygate wins — the operator should
		// not mix these).
		{"both skygate and skygate-standby (invalid)", []string{"skygate", "skygate-standby"}, false},
		// db-replica (no skygate, no skygate-standby)
		// — this is a pure PG replica with no skygate
		// role. isStandbyRole returns false (not a
		// standby by our definition — it's a pure
		// DB-replica without skygate control plane).
		{"db-replica only", []string{"patroni-replica"}, false},
		// Empty
		{"empty roles", []string{}, false},
		{"nil roles", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := isStandbyRole(c.roles)
			if got != c.want {
				t.Errorf("isStandbyRole(%v) = %v, want %v", c.roles, got, c.want)
			}
		})
	}
}
