// v1.5.0+ / B211 — unit tests for the `skygate init`
// helpers. Most of the logic in init.go is DB-bound
// (the live-verify on Windows Docker covers the DB
// path); these tests pin the pure-Go helpers that
// don't need a DB connection.

package main

import (
	"reflect"
	"testing"
)

func TestParseRolesCSV(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", []string{}},
		{"single", "skygate", []string{"skygate"}},
		{"two", "skygate,db-primary", []string{"skygate", "db-primary"}},
		{"three", "skygate,db-primary,control", []string{"skygate", "db-primary", "control"}},
		{"whitespace trimmed", "skygate , db-primary ,  control", []string{"skygate", "db-primary", "control"}},
		{"empty entries dropped", "skygate,,db-primary,", []string{"skygate", "db-primary"}},
		{"all whitespace dropped", " , , ", []string{}},
		{"hyphenated role preserved", "skygate-standby", []string{"skygate-standby"}},
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

func TestParseRolesCSV_NotNilForValidInput(t *testing.T) {
	// The bootstrap path checks `len(roles) == 0`
	// to bail on empty input. A nil []string has
	// len == 0, so the check is correct, but the
	// JSON output of `init status` should be
	// "roles": [], not "roles": null — pinning
	// parseRolesCSV to always return a non-nil
	// empty slice for empty input.
	got := parseRolesCSV("")
	if got == nil {
		t.Errorf("parseRolesCSV(\"\") returned nil; want empty []string{} so JSON encodes to []")
	}
	if len(got) != 0 {
		t.Errorf("parseRolesCSV(\"\") = %v, want empty", got)
	}
}

func TestInitState_VersionPinned(t *testing.T) {
	// The on-disk init state has a version field
	// so a future breaking change can detect +
	// migrate. Pin the current version.
	if initStateVersion != 1 {
		t.Errorf("initStateVersion = %d, want 1", initStateVersion)
	}
}
