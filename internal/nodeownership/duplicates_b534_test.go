package nodeownership

// duplicates_b534_test.go — B534.
//
// The first test is the LIVE fixture: the operator's 2026-11-27 report
// ("при OIDC выскочил skybars-1 хотя это устройство skybars"), with the values
// read out of headscale at the time — id=55 `skybars` (daniil, offline since
// 12:16) and id=198 `skybars-1` (skyadmin, online, created 14:41). A detector
// that does not fire on that pair detects nothing worth having.

import (
	"strings"
	"testing"

	"skygate/internal/headscale"
)

func TestB534_TheLiveSkybarsPairIsReported(t *testing.T) {
	nodes := []headscale.NodeView{
		{ID: "55", GivenName: "skybars", Hostname: "SkyBars", UserName: "daniil",
			Tags: []string{"tag:dev-daniil-skybars", "tag:private"}, Online: false,
			LastSeen: "2026-11-27T12:16:23Z", CreatedAt: "2026-09-19T05:46:50Z"},
		{ID: "198", GivenName: "skybars-1", Hostname: "SkyBars", UserName: "skyadmin",
			Tags: []string{"tag:dev-skyadmin-skybars-1", "tag:private"}, Online: true,
			LastSeen: "2026-11-27T14:31:22Z", CreatedAt: "2026-11-27T14:41:59Z"},
		{ID: "35", GivenName: "skybars-secure", Hostname: "SkyBars", UserName: "skyadmin",
			Tags: []string{"tag:dev-skyadmin-skybars-secure"}, Online: true},
	}
	dups := DetectDuplicates(nodes)
	if len(dups) != 1 {
		t.Fatalf("DetectDuplicates = %d pair(s), want exactly 1 (skybars-1 vs skybars): %+v", len(dups), dups)
	}
	d := dups[0]
	if d.NewID != "198" || d.OldID != "55" {
		t.Fatalf("pair = new %s / old %s, want 198 / 55 (the suffixed node is the NEW one)", d.NewID, d.OldID)
	}
	if d.Kind != DuplicateSameHostStale {
		t.Errorf("Kind = %q, want %q (same hostname, old node offline)", d.Kind, DuplicateSameHostStale)
	}
	reason := d.Reason()
	for _, want := range []string{"skybars-1", "skybars", "daniil", "skyadmin", "RE-REGISTRATION"} {
		if !strings.Contains(reason, want) {
			t.Errorf("Reason() is missing %q:\n%s", want, reason)
		}
	}
}

func TestB534_BothOnlineIsItsOwnKind(t *testing.T) {
	nodes := []headscale.NodeView{
		{ID: "1", GivenName: "phone", Hostname: "Phone", Online: true},
		{ID: "2", GivenName: "phone-1", Hostname: "Phone", Online: true},
	}
	dups := DetectDuplicates(nodes)
	if len(dups) != 1 || dups[0].Kind != DuplicateSameHostLive {
		t.Fatalf("dups = %+v, want one pair of kind %q (two live nodes, one device)", dups, DuplicateSameHostLive)
	}
	if !strings.Contains(dups[0].Reason(), "BOTH online") {
		t.Errorf("Reason() must say both are online:\n%s", dups[0].Reason())
	}
}

func TestB534_DifferentHostIsWeakerButReported(t *testing.T) {
	nodes := []headscale.NodeView{
		{ID: "1", GivenName: "laptop", Hostname: "LAPTOP-A", Online: true},
		{ID: "2", GivenName: "laptop-1", Hostname: "LAPTOP-B", Online: true},
	}
	dups := DetectDuplicates(nodes)
	if len(dups) != 1 || dups[0].Kind != DuplicateDifferentHost {
		t.Fatalf("dups = %+v, want one pair of kind %q", dups, DuplicateDifferentHost)
	}
}

func TestB534_WhatIsNotADuplicate(t *testing.T) {
	cases := map[string][]headscale.NodeView{
		"a suffixed name with no base node is somebody's naming": {
			{ID: "1", GivenName: "work-2", Hostname: "WORK"},
		},
		"no suffix at all": {
			{ID: "1", GivenName: "emilia", Hostname: "emilia"},
			{ID: "2", GivenName: "karolina", Hostname: "karolina"},
		},
		"counter 0 is not headscale's suffix": {
			{ID: "1", GivenName: "box", Hostname: "BOX"},
			{ID: "2", GivenName: "box-0", Hostname: "BOX"},
		},
		"empty given names are not compared": {
			{ID: "1", GivenName: "", Hostname: "X"},
			{ID: "2", GivenName: "-1", Hostname: "X"},
		},
	}
	for name, nodes := range cases {
		if dups := DetectDuplicates(nodes); len(dups) != 0 {
			t.Errorf("%s: DetectDuplicates = %+v, want none", name, dups)
		}
	}
}

func TestB534_DeterministicOrderAndDevTag(t *testing.T) {
	nodes := []headscale.NodeView{
		{ID: "9", GivenName: "zeta-1", Hostname: "Z"},
		{ID: "10", GivenName: "zeta", Hostname: "Z"},
		{ID: "3", GivenName: "alpha-2", Hostname: "A", Tags: []string{"tag:private", "tag:dev-x-alpha"}},
		{ID: "4", GivenName: "alpha", Hostname: "A"},
	}
	dups := DetectDuplicates(nodes)
	if len(dups) != 2 {
		t.Fatalf("dups = %d, want 2", len(dups))
	}
	if dups[0].NewGiven != "alpha-2" || dups[1].NewGiven != "zeta-1" {
		t.Errorf("order = %q, %q; want sorted by given name", dups[0].NewGiven, dups[1].NewGiven)
	}
	if dups[0].NewTag != "tag:dev-x-alpha" {
		t.Errorf("NewTag = %q, want the tag:dev-* tag (the one the operator can search for)", dups[0].NewTag)
	}
	if dups[1].NewTag != "(none)" {
		t.Errorf("NewTag = %q, want (none) when the node carries no tags", dups[1].NewTag)
	}
	if _, _, ok := duplicateBase("no-suffix"); ok {
		t.Error("duplicateBase accepted a name with no counter")
	}
	if base, n, ok := duplicateBase("nothing-phone-2"); !ok || base != "nothing-phone" || n != 2 {
		t.Errorf("duplicateBase(nothing-phone-2) = %q,%d,%v; want nothing-phone,2,true", base, n, ok)
	}
}
