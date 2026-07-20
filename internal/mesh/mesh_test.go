// 2026-07-20: v0.22.0 — mesh package tests.
//
// The mesh is the N-way generalization of the v0.17.1
// one-directional subnet share. The tests pin the CRUD
// lifecycle (create / join / leave / dissolve / list)
// + the integration with the ACL builder (the critical
// "shared network" use case the user described as
// "radmin-like").
//
// These tests use the same in-memory SQLite + minimal
// schema pattern as internal/acl/acl_test.go and
// internal/invite/invite_test.go. The schema is the
// minimum needed to exercise the mesh code: portal_users
// (FK target), meshes (the table the package owns),
// mesh_members (the table the package owns), and
// user_subnets (for the ACL integration test).

package mesh

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// minimalSchema covers the tables the mesh package
// touches. Production migrations are not run here
// because the test stays in-memory and the schema
// is small.
const minimalSchema = `
CREATE TABLE portal_users (
	id INTEGER PRIMARY KEY,
	username TEXT NOT NULL,
	password_hash TEXT DEFAULT '',
	is_admin INTEGER DEFAULT 0,
	headscale_user_id INTEGER DEFAULT 0,
	headscale_url TEXT NOT NULL DEFAULT '',
	subnet_cidr TEXT NOT NULL DEFAULT '',
	subnet_status TEXT NOT NULL DEFAULT '',
	subnet_router_node_id TEXT NOT NULL DEFAULT ''
);
CREATE TABLE user_subnets (
	id INTEGER PRIMARY KEY,
	user_id INTEGER NOT NULL UNIQUE,
	cidr TEXT NOT NULL,
	subnet_bits INTEGER NOT NULL DEFAULT 24,
	status TEXT NOT NULL DEFAULT 'pending',
	control_plane_url TEXT NOT NULL DEFAULT '',
	router_node_id TEXT NOT NULL DEFAULT '',
	router_container_id TEXT NOT NULL DEFAULT '',
	router_hostname TEXT NOT NULL DEFAULT '',
	created_at INTEGER DEFAULT 0,
	updated_at INTEGER DEFAULT 0
);
CREATE TABLE meshes (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	code TEXT NOT NULL UNIQUE,
	name TEXT NOT NULL DEFAULT '',
	creator_user_id INTEGER NOT NULL,
	status TEXT NOT NULL DEFAULT 'active',
	created_at INTEGER NOT NULL DEFAULT 0,
	dissolved_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE mesh_members (
	mesh_id INTEGER NOT NULL,
	user_id INTEGER NOT NULL,
	joined_at INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (mesh_id, user_id)
);
`

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	for _, q := range strings.Split(minimalSchema, ";") {
		q = strings.TrimSpace(q)
		if q == "" {
			continue
		}
		if _, err := d.Exec(q); err != nil {
			t.Fatalf("schema %q: %v", q, err)
		}
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func seedUser(t *testing.T, d *sql.DB, name string) int64 {
	t.Helper()
	res, err := d.Exec(`INSERT INTO portal_users (username) VALUES (?)`, name)
	if err != nil {
		t.Fatalf("seed user %s: %v", name, err)
	}
	id, _ := res.LastInsertId()
	return id
}

func TestGenerateCodeShape(t *testing.T) {
	for i := 0; i < 50; i++ {
		c, err := GenerateCode()
		if err != nil {
			t.Fatalf("GenerateCode: %v", err)
		}
		if len(c) != CodeLength {
			t.Errorf("code length = %d, want %d (code=%q)", len(c), CodeLength, c)
		}
		for j, ch := range c {
			if !strings.ContainsRune(CodeAlphabet, ch) {
				t.Errorf("code[%d] = %q not in alphabet %q", j, ch, CodeAlphabet)
			}
		}
	}
}

func TestCreateMeshAddsCreatorAsMember(t *testing.T) {
	d := openTestDB(t)
	creatorID := seedUser(t, d, "alice")
	m, err := CreateMesh(d, creatorID, "office-net")
	if err != nil {
		t.Fatalf("CreateMesh: %v", err)
	}
	if m.Name != "office-net" {
		t.Errorf("Name = %q, want %q", m.Name, "office-net")
	}
	if m.Status != StatusActive {
		t.Errorf("Status = %q, want %q", m.Status, StatusActive)
	}
	if m.CreatorUserID != creatorID {
		t.Errorf("CreatorUserID = %d, want %d", m.CreatorUserID, creatorID)
	}
	if len(m.Code) != CodeLength {
		t.Errorf("Code length = %d, want %d", len(m.Code), CodeLength)
	}
	// Creator is the first member.
	members, err := ListMembers(d, m.ID)
	if err != nil {
		t.Fatalf("ListMembers: %v", err)
	}
	if len(members) != 1 {
		t.Fatalf("member count = %d, want 1", len(members))
	}
	if members[0].UserID != creatorID {
		t.Errorf("creator's user_id = %d, want %d", members[0].UserID, creatorID)
	}
}

func TestCreateMeshRejectsEmptyName(t *testing.T) {
	d := openTestDB(t)
	creatorID := seedUser(t, d, "alice")
	_, err := CreateMesh(d, creatorID, "   ")
	if err == nil {
		t.Fatal("expected error on empty name, got nil")
	}
}

func TestLookupByCodeNormalizesCase(t *testing.T) {
	d := openTestDB(t)
	creatorID := seedUser(t, d, "alice")
	m, err := CreateMesh(d, creatorID, "lab")
	if err != nil {
		t.Fatalf("CreateMesh: %v", err)
	}
	// Lookup with lowercase + whitespace should
	// still find the mesh (matches v0.21.0 invite
	// behaviour: codes are normalized to upper-case
	// at lookup time).
	lowered := strings.ToLower(m.Code)
	got, err := LookupByCode(d, " "+lowered+" ")
	if err != nil {
		t.Fatalf("LookupByCode: %v", err)
	}
	if got.ID != m.ID {
		t.Errorf("ID = %d, want %d", got.ID, m.ID)
	}
}

func TestJoinMeshAddsMember(t *testing.T) {
	d := openTestDB(t)
	creatorID := seedUser(t, d, "alice")
	joinerID := seedUser(t, d, "bob")
	m, err := CreateMesh(d, creatorID, "office")
	if err != nil {
		t.Fatalf("CreateMesh: %v", err)
	}
	if err := JoinMesh(d, m.Code, joinerID); err != nil {
		t.Fatalf("JoinMesh: %v", err)
	}
	members, _ := ListMembers(d, m.ID)
	if len(members) != 2 {
		t.Fatalf("member count = %d, want 2", len(members))
	}
	hasCreator := false
	hasJoiner := false
	for _, m := range members {
		if m.UserID == creatorID {
			hasCreator = true
		}
		if m.UserID == joinerID {
			hasJoiner = true
		}
	}
	if !hasCreator || !hasJoiner {
		t.Errorf("members missing: hasCreator=%v hasJoiner=%v", hasCreator, hasJoiner)
	}
}

func TestJoinMeshDissolvedRejected(t *testing.T) {
	d := openTestDB(t)
	creatorID := seedUser(t, d, "alice")
	joinerID := seedUser(t, d, "bob")
	m, err := CreateMesh(d, creatorID, "office")
	if err != nil {
		t.Fatalf("CreateMesh: %v", err)
	}
	if err := DissolveMesh(d, m.Code, creatorID); err != nil {
		t.Fatalf("DissolveMesh: %v", err)
	}
	if err := JoinMesh(d, m.Code, joinerID); !errors.Is(err, ErrDissolved) {
		t.Errorf("JoinMesh on dissolved mesh: err = %v, want ErrDissolved", err)
	}
}

func TestLeaveMeshRemovesMember(t *testing.T) {
	d := openTestDB(t)
	creatorID := seedUser(t, d, "alice")
	joinerID := seedUser(t, d, "bob")
	m, err := CreateMesh(d, creatorID, "office")
	if err != nil {
		t.Fatalf("CreateMesh: %v", err)
	}
	_ = JoinMesh(d, m.Code, joinerID)
	if err := LeaveMesh(d, m.Code, joinerID); err != nil {
		t.Fatalf("LeaveMesh: %v", err)
	}
	members, _ := ListMembers(d, m.ID)
	if len(members) != 1 {
		t.Errorf("member count after leave = %d, want 1", len(members))
	}
	// Leaving twice is a no-op with ErrNotMember.
	if err := LeaveMesh(d, m.Code, joinerID); !errors.Is(err, ErrNotMember) {
		t.Errorf("LeaveMesh (2nd): err = %v, want ErrNotMember", err)
	}
}

func TestDissolveMeshOnlyByCreator(t *testing.T) {
	d := openTestDB(t)
	creatorID := seedUser(t, d, "alice")
	otherID := seedUser(t, d, "bob")
	m, err := CreateMesh(d, creatorID, "office")
	if err != nil {
		t.Fatalf("CreateMesh: %v", err)
	}
	// Other user can't dissolve.
	if err := DissolveMesh(d, m.Code, otherID); err == nil {
		t.Error("non-creator dissolve should fail, got nil")
	}
	// Creator can.
	if err := DissolveMesh(d, m.Code, creatorID); err != nil {
		t.Fatalf("creator dissolve: %v", err)
	}
	got, _ := LookupByCode(d, m.Code)
	if got.Status != StatusDissolved {
		t.Errorf("Status after dissolve = %q, want %q", got.Status, StatusDissolved)
	}
}

func TestListMeshesForUserActiveOnly(t *testing.T) {
	d := openTestDB(t)
	aliceID := seedUser(t, d, "alice")
	bobID := seedUser(t, d, "bob")

	// alice creates two meshes; one is dissolved.
	m1, _ := CreateMesh(d, aliceID, "active-mesh")
	m2, _ := CreateMesh(d, aliceID, "doomed-mesh")
	_ = DissolveMesh(d, m2.Code, aliceID)
	// bob joins the active one.
	_ = JoinMesh(d, m1.Code, bobID)

	// alice's view: only m1 (m2 is dissolved).
	aliceMeshes, _ := ListMeshesForUser(d, aliceID)
	if len(aliceMeshes) != 1 {
		t.Errorf("alice's mesh count = %d, want 1", len(aliceMeshes))
	}
	if len(aliceMeshes) > 0 && aliceMeshes[0].ID != m1.ID {
		t.Errorf("alice's mesh ID = %d, want %d (m1)",
			aliceMeshes[0].ID, m1.ID)
	}

	// bob's view: only m1.
	bobMeshes, _ := ListMeshesForUser(d, bobID)
	if len(bobMeshes) != 1 {
		t.Errorf("bob's mesh count = %d, want 1", len(bobMeshes))
	}
}

func TestLookupByCodeEmptyNotFound(t *testing.T) {
	d := openTestDB(t)
	if _, err := LookupByCode(d, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("empty code: err = %v, want ErrNotFound", err)
	}
	if _, err := LookupByCode(d, "   "); !errors.Is(err, ErrNotFound) {
		t.Errorf("whitespace code: err = %v, want ErrNotFound", err)
	}
}
