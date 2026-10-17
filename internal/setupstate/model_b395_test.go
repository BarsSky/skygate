package setupstate

// model_b395_test.go — the four states, the honest-unknown rule, the ambiguity
// detector and the ACL warning.
//
// These are behavioural: each test drives Detect() with a fake client, because the
// defect this package exists to prevent is a STATE misdiagnosis ("headscale is down"
// rendered as "everything is empty, start here"), which a source grep cannot see.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"skygate/internal/headscale"
)

// fakeHS is a scripted headscale. Every field is an explicit answer, so a test says
// which question failed, not merely that Detect returned something.
type fakeHS struct {
	pingErr  error
	users    []headscale.HSUser
	usersErr error
	nodes    []headscale.NodeView
	nodesErr error
	acl      string
	aclErr   error
}

func (f *fakeHS) PingContext(context.Context) error           { return f.pingErr }
func (f *fakeHS) ListUsers() ([]headscale.HSUser, error)      { return f.users, f.usersErr }
func (f *fakeHS) ListAllNodes() ([]headscale.NodeView, error) { return f.nodes, f.nodesErr }
func (f *fakeHS) GetACL() (string, error)                     { return f.acl, f.aclErr }

// TestDetect_AbsentIsNotEmpty is the regression for the whole package: "no client"
// must be Absent with UNKNOWN counts, never "0 users, 0 nodes".
func TestDetect_AbsentIsNotEmpty(t *testing.T) {
	rep := Detect(context.Background(), nil, Config{})
	if rep.State != StateAbsent {
		t.Fatalf("state = %s, want absent", rep.State)
	}
	if rep.InfraNodes.Known || rep.PendingReview.Known {
		t.Error("the counts claim to be known about a headscale we never asked")
	}
	if rep.InfraNodes.String() != "—" {
		t.Errorf("an unknown count rendered as %q — the page would print a zero", rep.InfraNodes.String())
	}
	if rep.Reason == "" {
		t.Error("no reason — the operator cannot act on «absent»")
	}
	if len(rep.Steps) == 0 {
		t.Error("no steps — the wizard has nothing to show")
	}
}

// TestDetect_UnreachableKeepsTheError pins that an unreachable headscale is its own
// state and that the raw error survives into the report.
func TestDetect_UnreachableKeepsTheError(t *testing.T) {
	rep := Detect(context.Background(),
		&fakeHS{pingErr: errors.New("dial tcp 100.64.0.1:8080: connection refused")}, Config{})
	if rep.State != StateUnreachable {
		t.Fatalf("state = %s, want unreachable", rep.State)
	}
	if !strings.Contains(rep.Reason, "connection refused") {
		t.Errorf("reason = %q, want the raw dial error", rep.Reason)
	}
	if rep.PendingReview.Known {
		t.Error("the review count claims to be known while headscale was unreachable")
	}
}

// TestDetect_EmptyVsPopulated pins the boundary: zero users AND zero nodes is
// Empty; anything else is Populated (and the counts are then facts).
func TestDetect_EmptyVsPopulated(t *testing.T) {
	empty := Detect(context.Background(), &fakeHS{}, Config{})
	if empty.State != StateEmpty {
		t.Fatalf("empty state = %s, want empty", empty.State)
	}
	if !empty.PendingReview.Known {
		t.Error("a reachable, answered headscale must give KNOWN counts (even when zero)")
	}

	pop := Detect(context.Background(), &fakeHS{
		users: []headscale.HSUser{{ID: "1", Name: "alice"}},
		nodes: []headscale.NodeView{{ID: "10", GivenName: "laptop", UserName: "alice"}},
		acl:   `{"grants":[{"src":["tag:x"],"dst":["autogroup:internet"]}]}`,
	}, Config{InfraUsername: "infra", PortalByUsername: map[string]PortalUser{"alice": {ID: 7, Name: "alice"}}})
	if pop.State != StatePopulated {
		t.Fatalf("populated state = %s, want populated", pop.State)
	}
	if len(pop.Users) != 1 || pop.Users[0].PortalUserID != 7 {
		t.Errorf("users = %+v, want alice mapped to portal id 7", pop.Users)
	}
	if pop.InfraNodes.Value != 0 || !pop.InfraNodes.Known {
		t.Errorf("infra count = %+v, want a known 0", pop.InfraNodes)
	}
	if !pop.ACLLocked {
		t.Error("an ACL with grants must be flagged as about to be rewritten")
	}
}

// TestDetect_UsersWithoutNodesIsStillEmpty is the live finding from docker scenario 2:
// skygate creates the bootstrap admin user at boot (ensureHeadscaleUser), so a
// brand-new headscale ALWAYS has one user. A user the portal created itself is not
// "data to import", and with no devices there is nothing to import.
func TestDetect_UsersWithoutNodesIsStillEmpty(t *testing.T) {
	rep := Detect(context.Background(), &fakeHS{
		users: []headscale.HSUser{{ID: "1", Name: "admin"}, {ID: "2", Name: "infra"}},
	}, Config{InfraUsername: "infra", PortalOwn: []string{"admin"}})
	if rep.State != StateEmpty {
		t.Fatalf("state = %s, want empty for a control plane whose only users are the portal's own", rep.State)
	}
	if rep.Reason == "" {
		t.Error("the empty state should explain that the users are the portal's own")
	}
}

// TestDetect_ImportedUsersMakeItPopulated pins the other half: a control-plane user
// skygate did NOT create is exactly the "headscale already has data" case the wizard's
// import flow exists for — even before that user registers a device.
func TestDetect_ImportedUsersMakeItPopulated(t *testing.T) {
	rep := Detect(context.Background(), &fakeHS{
		users: []headscale.HSUser{{ID: "1", Name: "admin"}, {ID: "2", Name: "alice"}},
	}, Config{InfraUsername: "infra", PortalOwn: []string{"admin"}})
	if rep.State != StatePopulated {
		t.Fatalf("state = %s, want populated when a foreign user exists", rep.State)
	}
}

// TestDetect_InfraAndReviewAreSeparated pins the classification the wizard's import
// table depends on: a public node owned by the technical user is NOT an end-user
// device awaiting review.
func TestDetect_InfraAndReviewAreSeparated(t *testing.T) {
	rep := Detect(context.Background(), &fakeHS{
		users: []headscale.HSUser{{ID: "1", Name: "infra"}, {ID: "2", Name: "alice"}},
		nodes: []headscale.NodeView{
			{ID: "1", GivenName: "relay", UserName: "infra", Tags: []string{"tag:exit-node"}},
			{ID: "2", GivenName: "laptop", UserName: "alice"},
			{ID: "3", GivenName: "guest-phone", UserName: "stranger"},
		},
	}, Config{InfraUsername: "infra", PortalByUsername: map[string]PortalUser{"alice": {ID: 7, Name: "alice"}}})
	if rep.InfraNodes.Value != 1 {
		t.Errorf("infra = %d, want 1 (the relay)", rep.InfraNodes.Value)
	}
	if rep.PendingReview.Value != 1 {
		t.Errorf("pending review = %d, want 1 (the stranger's phone)", rep.PendingReview.Value)
	}
	for _, n := range rep.Nodes {
		if n.Hostname == "relay" && !n.Infra {
			t.Error("the relay is not marked as infrastructure")
		}
		if n.Hostname == "guest-phone" && !n.NeedsReview {
			t.Error("an unattributed node is not marked for review")
		}
	}
}

// TestDetect_AmbiguousUsernamesAreNotAutoResolved pins that a duplicate headscale
// username is surfaced, not silently picked.
func TestDetect_AmbiguousUsernamesAreNotAutoResolved(t *testing.T) {
	rep := Detect(context.Background(), &fakeHS{
		users: []headscale.HSUser{{ID: "1", Name: "alice"}, {ID: "2", Name: "Alice"}},
		nodes: []headscale.NodeView{{ID: "1", GivenName: "laptop", UserName: "alice"}},
	}, Config{PortalByUsername: map[string]PortalUser{"alice": {ID: 7, Name: "alice"}}})
	flagged := 0
	for _, u := range rep.Users {
		if u.Ambiguity != "" {
			flagged++
		}
	}
	if flagged != 2 {
		t.Errorf("%d of %d users flagged as ambiguous, want both", flagged, len(rep.Users))
	}
}

// TestStepsMatchTheState pins the ordered wizard per state — the operator asked for
// the branches explicitly, and a step list that does not depend on the state is how
// a wizard becomes a wall of text.
func TestStepsMatchTheState(t *testing.T) {
	cases := []struct {
		name  string
		hs    Client
		state HSState
		first string
	}{
		{"absent", nil, StateAbsent, "install"},
		{"unreachable", &fakeHS{pingErr: errors.New("x")}, StateUnreachable, "diagnose"},
		{"empty", &fakeHS{}, StateEmpty, "admin"},
	}
	for _, tc := range cases {
		rep := Detect(context.Background(), tc.hs, Config{})
		if rep.State != tc.state {
			t.Fatalf("%s: state = %s, want %s", tc.name, rep.State, tc.state)
		}
		if len(rep.Steps) == 0 || rep.Steps[0].ID != tc.first {
			t.Errorf("%s: first step = %+v, want id %q", tc.name, rep.Steps, tc.first)
		}
		for _, s := range rep.Steps {
			if s.State == StepBlocked && s.Why == "" {
				t.Errorf("%s: step %q is blocked with no reason", tc.name, s.ID)
			}
		}
	}
}

// TestStateStringIsStable pins the strings the page and the audit log key on.
func TestStateStringIsStable(t *testing.T) {
	for state, want := range map[HSState]string{
		StateUnknown:     "unknown",
		StateAbsent:      "absent",
		StateUnreachable: "unreachable",
		StateEmpty:       "empty",
		StatePopulated:   "populated",
	} {
		if got := state.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", int(state), got, want)
		}
	}
}
