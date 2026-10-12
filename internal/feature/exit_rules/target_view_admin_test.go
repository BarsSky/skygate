package exit_rules

import "testing"

// B376: the admin by-target view must group like the /my one (a CDN rule by the
// domain the operator typed, accept/deny never merged) AND say whose device each
// tag is — this page is cross-user, where a bare "workpc" is ambiguous.
func TestGroupAdminRulesByTarget_OwnersInDeviceTags(t *testing.T) {
	rules := []AdminRule{
		{ID: 1, UserID: 6, UserName: "skyadmin", DeviceID: 9, DeviceName: "workpc", ExitNode: "emilia", TargetType: "subnet", TargetValue: "104.16.0.0/12", ParentDomain: "cdn:cloudflare:discordapp.com", Action: "accept"},
		{ID: 2, UserID: 7, UserName: "michail", DeviceID: 55, DeviceName: "workpc", ExitNode: "emilia", TargetType: "subnet", TargetValue: "104.16.0.0/12", ParentDomain: "cdn:cloudflare:discordapp.com", Action: "accept"},
	}

	rows := GroupAdminRulesByTarget(rules)
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1 (the CDN expansion must collapse across users too)", len(rows))
	}
	row := rows[0]
	if !row.IsCDNGroup || row.TargetValue != "discordapp.com" || row.CDN != "cloudflare" {
		t.Errorf("row = %+v, want the discordapp.com/cloudflare group", row)
	}
	if row.Count != 2 || len(row.Rules) != 2 {
		t.Fatalf("Count/Rules = %d/%d, want 2/2", row.Count, len(row.Rules))
	}
	if len(row.Devices) != 2 {
		t.Fatalf("got %d device tags, want 2", len(row.Devices))
	}
	// Sorted by label: "michail/workpc" before "skyadmin/workpc".
	if row.Devices[0].DeviceName != "michail/workpc" {
		t.Errorf("tag[0] = %q, want michail/workpc (the owner must be part of the label)", row.Devices[0].DeviceName)
	}
	if row.Devices[1].DeviceName != "skyadmin/workpc" {
		t.Errorf("tag[1] = %q, want skyadmin/workpc", row.Devices[1].DeviceName)
	}
	// The richer AdminRule rows are attached, not the projected copies: the
	// expander needs the B178/B182/B184 annotation fields.
	if row.Rules[0].UserName == "" {
		t.Error("the attached rows lost the AdminRule fields (UserName is empty) — the expander would render blank")
	}
}

// B376: accept and deny stay separate rows here too — an admin looking at a
// destination must see the conflict, not a merged row.
func TestGroupAdminRulesByTarget_ActionIsPartOfTheKey(t *testing.T) {
	rules := []AdminRule{
		{ID: 1, UserID: 6, UserName: "skyadmin", DeviceID: 9, DeviceName: "a", TargetType: "subnet", TargetValue: "10.0.0.0/8", Action: "accept"},
		{ID: 2, UserID: 7, UserName: "michail", DeviceID: 55, DeviceName: "b", TargetType: "subnet", TargetValue: "10.0.0.0/8", Action: "deny"},
	}
	rows := GroupAdminRulesByTarget(rules)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2 (accept and deny must not merge)", len(rows))
	}
	if rows[0].Action != "accept" || rows[1].Action != "deny" {
		t.Errorf("actions = %q/%q, want accept/deny", rows[0].Action, rows[1].Action)
	}
}

func TestGroupAdminRulesByTarget_EmptyAndCount(t *testing.T) {
	if rows := GroupAdminRulesByTarget(nil); rows == nil || len(rows) != 0 {
		t.Fatalf("got %#v, want an empty non-nil slice", rows)
	}
	rows := GroupAdminRulesByTarget([]AdminRule{
		{ID: 1, UserID: 6, UserName: "skyadmin", DeviceID: 9, DeviceName: "a", TargetType: "subnet", TargetValue: "10.0.0.0/8", Action: "accept"},
		{ID: 2, UserID: 6, UserName: "skyadmin", DeviceID: 9, DeviceName: "a", TargetType: "subnet", TargetValue: "192.0.2.0/24", Action: "accept"},
		{ID: 3, UserID: 7, UserName: "michail", DeviceID: 55, DeviceName: "b", TargetType: "subnet", TargetValue: "10.0.0.0/8", Action: "accept"},
	})
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	if got := TargetRowAdminDeviceCount(rows); got != 2 {
		t.Errorf("TargetRowAdminDeviceCount = %d, want 2 distinct devices", got)
	}
}
