package admin

import (
	"encoding/json"
	"testing"

	"skygate/internal/exitnode"
	"skygate/internal/headscale"
)

// TestBaseRouteRepairDecision_B417 is the safety contract of the auto-repair: it may
// approve ONLY a node that (a) has a row in exit_servers — i.e. skygate registered it,
// (b) is online, (c) advertises a base route, (d) has not had that route approved.
func TestBaseRouteRepairDecision_B417(t *testing.T) {
	node := headscale.NodeView{
		ID:              "7",
		Hostname:        "aisha",
		Online:          true,
		IsExitNode:      true,
		AvailableRoutes: []string{"0.0.0.0/0", "::/0"},
		ApprovedRoutes:  []string{},
	}
	if !baseRouteRepairDecision("aisha", node) {
		t.Fatal("an online, advertising, unapproved skygate relay must be repaired")
	}

	cases := []struct {
		name string
		row  string
		n    headscale.NodeView
	}{
		{"already approved", "aisha", func() headscale.NodeView {
			n := node
			n.ApprovedRoutes = []string{"0.0.0.0/0", "::/0"}
			return n
		}()},
		{"half approved still repaired", "aisha", func() headscale.NodeView {
			n := node
			n.ApprovedRoutes = []string{"0.0.0.0/0"}
			return n
		}()},
		{"offline", "aisha", func() headscale.NodeView { n := node; n.Online = false; return n }()},
		{"other node", "emilia", node},
		{"no exit_servers row", "", node},
		{"advertises only a subnet", "aisha", func() headscale.NodeView {
			n := node
			n.AvailableRoutes = []string{"10.9.0.0/24"}
			return n
		}()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := baseRouteRepairDecision(c.row, c.n)
			want := c.name == "half approved still repaired"
			if got != want {
				t.Fatalf("decision = %v, want %v", got, want)
			}
		})
	}

	// A node with no routes at all must never be touched.
	empty := node
	empty.AvailableRoutes = nil
	if baseRouteRepairDecision("aisha", empty) {
		t.Fatal("a node advertising nothing must never be auto-approved")
	}
}

func TestContainsRoute_B417(t *testing.T) {
	if !containsRoute([]string{" 0.0.0.0/0 "}, "0.0.0.0/0") {
		t.Fatal("a padded route must still match")
	}
	if containsRoute([]string{"0.0.0.0/1"}, "0.0.0.0/0") {
		t.Fatal("a different prefix must not match")
	}
	if containsRoute(nil, "0.0.0.0/0") {
		t.Fatal("an empty list contains nothing")
	}
}

// TestProvisionJob_JSONRoundtrip pins the durable shape: the job is stored as JSON in
// global_settings, so every field the page needs must survive a round trip.
func TestProvisionJob_JSONRoundtrip(t *testing.T) {
	in := exitnode.Job{
		ID: "1", Host: "192.0.2.10", NodeName: "aisha", SSHUser: "root", SSHPort: 2222,
		KeyPath: "/ssh-sync/skygate_sync", Desc: "d", Accept: 1,
		InstallTS: true, SetupNAT: true, Actor: "skyadmin", ActorID: 42,
		Status: exitnode.JobFailed, Step: exitnode.JobStepWaitNode,
		Message: "машина настроена, но узел не появился", Attempt: 2,
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out exitnode.Job
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.ActorID != 42 || out.Attempt != 2 || out.Step != exitnode.JobStepWaitNode ||
		out.Status != exitnode.JobFailed || out.KeyPath != in.KeyPath || out.Host != in.Host {
		t.Fatalf("round trip lost a field the page needs: %+v", out)
	}
}
