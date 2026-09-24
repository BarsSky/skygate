// internal/db/device_owner_b316_test.go — B316 (v1.5.81).
//
// The live fixture is `aro`'s own table, verbatim:
//
//	2 workpc   tagged-devices   tag:dev-daniil-workpc
//	3 laptop  tagged-devices   tag:dev-daniil-laptop
//	6 homepc  daniil           tag:dev-daniil-homepc
//
// headscale had rewritten two of daniil's three devices to its synthetic owner, so the
// device-to-device mesh saw exactly one device for him and emitted no grant at all: the
// two machines could not reach each other while both were online, and nothing said why.
package db

import (
	"database/sql"
	"strings"
	"testing"
)

func seedMeshB316(t *testing.T, d *sql.DB) {
	t.Helper()
	mustExec := func(q string, args ...interface{}) {
		t.Helper()
		if _, err := d.Exec(q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	// ON CONFLICT (id) DO NOTHING is load-bearing on PostgreSQL: the v0.54 migration
	// already seeds `portal_users` id=99 as `infra`, so a bare INSERT dies with
	// "duplicate key value violates unique constraint portal_users_pkey". The live
	// aro roster is exactly that migration's row plus daniil — this fixture must
	// survive being applied on top of the real schema, not just on an empty one.
	mustExec(`INSERT INTO portal_users (id, username, password_hash, is_admin) VALUES (100, 'daniil', 'x', 1) ON CONFLICT (id) DO NOTHING`)
	mustExec(`INSERT INTO portal_users (id, username, password_hash, is_admin) VALUES (99, 'infra', 'x', 0) ON CONFLICT (id) DO NOTHING`)
	mustExec(`INSERT INTO node_owner_map (node_id, headscale_user_id, username, tag, hostname) VALUES
	          ('2', 0, 'tagged-devices', 'tag:dev-daniil-workpc', 'workpc'),
	          ('3', 0, 'tagged-devices', 'tag:dev-daniil-laptop', 'laptop'),
	          ('6', 0, 'daniil',         'tag:dev-daniil-homepc', 'homepc'),
	          ('1', 0, 'tagged-devices', 'tag:dev-infra-exit-node-vps', 'exit-node-vps')`)
	// The rules know the real owner (this is why the panel showed daniil all along).
	mustExec(`INSERT INTO device_rules (user_id, device_id, device_hostname, user_name, exit_node_id, target_type, target_value, action, enabled)
	          VALUES (100, 2, 'workpc', 'daniil', 'exit-node-vps', 'ip', '104.16.0.0/12', 'accept', 1)`)
}

func TestMeshDevices_ResolvesOwnerFromTheDeviceTag_B316(t *testing.T) {
	d := openTestDB(t)
	seedMeshB316(t, d)

	devices, unresolved, err := MeshDevices(d)
	if err != nil {
		t.Fatalf("MeshDevices: %v", err)
	}
	if len(unresolved) != 0 {
		t.Fatalf("unresolved = %+v, want none (every row names a portal user, directly or via its tag)", unresolved)
	}
	byTag := map[string]DeviceMeshTag{}
	for _, dev := range devices {
		byTag[dev.Tag] = dev
	}
	for tag, wantUser := range map[string]string{
		"tag:dev-daniil-workpc":       "daniil", // resolved from the TAG (the row said tagged-devices)
		"tag:dev-daniil-laptop":       "daniil",
		"tag:dev-daniil-homepc":       "daniil", // the row already named daniil
		"tag:dev-infra-exit-node-vps": "infra",
	} {
		dev, ok := byTag[tag]
		if !ok {
			t.Errorf("%s is missing from the mesh — the device would get no device-to-device grant", tag)
			continue
		}
		if dev.Username != wantUser {
			t.Errorf("%s owner = %q, want %q (source %q)", tag, dev.Username, wantUser, dev.Source)
		}
	}
	// The tag-derived rows must be marked as such: the operator has to be able to tell
	// "the row says daniil" from "we derived daniil from the tag".
	if got := byTag["tag:dev-daniil-workpc"].Source; got != "tag" {
		t.Errorf("workpc source = %q, want \"tag\"", got)
	}
	if got := byTag["tag:dev-daniil-homepc"].Source; got != "owner_map" {
		t.Errorf("homepc source = %q, want \"owner_map\"", got)
	}
}

func TestDeviceTagsForMesh_AllThreeDevicesUnderOneUser_B316(t *testing.T) {
	d := openTestDB(t)
	seedMeshB316(t, d)

	byUser, _, err := DeviceTagsForMesh(d)
	if err != nil {
		t.Fatalf("DeviceTagsForMesh: %v", err)
	}
	got := byUser["daniil"]
	want := []string{"tag:dev-daniil-homepc", "tag:dev-daniil-laptop", "tag:dev-daniil-workpc"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("daniil's mesh tags = %v, want %v — with fewer than two devices the ACL emits NO "+
			"device-to-device grant at all (the live aro failure)", got, want)
	}
	if len(byUser["infra"]) != 1 {
		t.Errorf("infra tags = %v, want exactly one", byUser["infra"])
	}
}

func TestMeshDevices_NamesWhatItCannotResolve_B316(t *testing.T) {
	d := openTestDB(t)
	seedMeshB316(t, d)
	// A device whose tag names a user that has no portal account, and whose rules do not
	// mention it either: it must be REPORTED, not silently dropped.
	if _, err := d.Exec(`INSERT INTO node_owner_map (node_id, headscale_user_id, username, tag, hostname)
	                     VALUES ('9', 0, 'tagged-devices', 'tag:dev-ghost-mysterybox', 'mysterybox')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, unresolved, err := MeshDevices(d)
	if err != nil {
		t.Fatalf("MeshDevices: %v", err)
	}
	if len(unresolved) != 1 || unresolved[0].Hostname != "mysterybox" {
		t.Fatalf("unresolved = %+v, want exactly mysterybox", unresolved)
	}
	if !strings.Contains(unresolved[0].Reason, "tagged-devices") {
		t.Errorf("reason = %q, want it to name the synthetic owner", unresolved[0].Reason)
	}
	// And it must not appear as a mesh device of anybody.
	byUser, _, _ := DeviceTagsForMesh(d)
	for user, tags := range byUser {
		for _, tag := range tags {
			if tag == "tag:dev-ghost-mysterybox" {
				t.Errorf("unresolvable device was granted to %q", user)
			}
		}
	}
}

func TestRepairSentinelDeviceOwners_FixesExactlyTheSentinelRows_B316(t *testing.T) {
	d := openTestDB(t)
	seedMeshB316(t, d)

	n, changes, err := RepairSentinelDeviceOwners(d)
	if err != nil {
		t.Fatalf("RepairSentinelDeviceOwners: %v", err)
	}
	if n != 3 {
		t.Fatalf("repaired %d row(s), want 3 (workpc, laptop, exit-node-vps); changes=%v", n, changes)
	}
	for _, want := range []string{"workpc: tagged-devices -> daniil", "laptop: tagged-devices -> daniil",
		"exit-node-vps: tagged-devices -> infra"} {
		found := false
		for _, ch := range changes {
			if strings.HasPrefix(ch, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("no change line for %q (got %v)", want, changes)
		}
	}
	// homepc already named a real user: untouched (and not reported as a change).
	var username string
	if err := d.QueryRow(`SELECT username FROM node_owner_map WHERE hostname = 'homepc'`).Scan(&username); err != nil {
		t.Fatalf("read homepc: %v", err)
	}
	if username != "daniil" {
		t.Errorf("homepc username = %q, want daniil (the repair must not touch real owners)", username)
	}
	// After the repair NOTHING is left unresolved, so the mesh works through the
	// ordinary ownership JOIN as well — the data no longer contradicts itself.
	if _, unresolved, _ := MeshDevices(d); len(unresolved) != 0 {
		t.Errorf("after the repair, unresolved = %+v, want none", unresolved)
	}
	// Idempotent: a second pass changes nothing.
	if n2, _, _ := RepairSentinelDeviceOwners(d); n2 != 0 {
		t.Errorf("second repair changed %d row(s), want 0", n2)
	}
}

func TestRepairSentinelDeviceOwners_RefusesUnprovableOwners_B316(t *testing.T) {
	d := openTestDB(t)
	seedMeshB316(t, d)
	if _, err := d.Exec(`INSERT INTO node_owner_map (node_id, headscale_user_id, username, tag, hostname)
	                     VALUES ('9', 0, 'tagged-devices', 'tag:private', 'private-only')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO node_owner_map (node_id, headscale_user_id, username, tag, hostname)
	                     VALUES ('10', 0, 'tagged-devices', 'tag:dev-ghost-mysterybox', 'mysterybox')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	n, changes, err := RepairSentinelDeviceOwners(d)
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if n != 3 {
		t.Fatalf("repaired %d, want 3 — a class tag and a tag naming no portal user must NOT be repaired (changes=%v)", n, changes)
	}
	for _, host := range []string{"private-only", "mysterybox"} {
		var username string
		if err := d.QueryRow(`SELECT username FROM node_owner_map WHERE hostname = $1`, host).Scan(&username); err != nil {
			t.Fatalf("read %s: %v", host, err)
		}
		if username != SentinelDeviceOwner {
			t.Errorf("%s username = %q, want it left as the sentinel (we cannot prove an owner)", host, username)
		}
	}
}
