// relay_weights_render_b456_test.go — B456 (2026-10-31). The template half of the weights editor.
//
// WHY a render test and not only grep contracts: the card is one `{{with .Weights}}` block that
// reads five fields off a view type. A rename on either side compiles, passes every source grep and
// renders a page with an empty weights card — the B448 class of failure (a template field that only
// exists at RENDER time). Executing the real template is the only assertion that catches it.
package handlers

import (
	"bytes"
	"strings"
	"testing"
)

// b456WeightsView is the shape admin.RelayWeightsView has, as plain maps: the test asserts the
// TEMPLATE's contract, so it must not depend on the admin package's structs.
func b456WeightsView(rows []map[string]any, rejected []string, raw string) map[string]any {
	return map[string]any{
		"Rows": rows, "Target": 10, "Configured": len(raw) > 0, "Raw": raw, "Rejected": rejected,
	}
}

// TestExitNodesRendersB456_WeightsCard — the form must be reachable and must carry the numbers the
// handler computed: one number input per relay named w_<relay>, bounded to 1..99, the current load,
// and the share of the next N prefixes.
func TestExitNodesRendersB456_WeightsCard(t *testing.T) {
	tpl := loadExitNodesBody(t)
	data := map[string]any{
		"Nodes":        []stubExitNodeInfo{},
		"TotalCount":   0,
		"HealthyCount": 0,
		"ControlURL":   "https://head.example.com",
		"SSHKeyPath":   "/ssh-sync/id_ed25519",
		"Page":         "admin/exit_nodes",
		"Title":        "Exit nodes",
		"PrefixAdmin": map[string]any{
			"GroupBy": "owner",
			"Weights": b456WeightsView([]map[string]any{
				{"Relay": "emilia", "Weight": 2, "Stored": true, "Load": 17, "Share": 7},
				{"Relay": "karolina", "Weight": 1, "Stored": false, "Load": 140, "Share": 3},
			}, nil, "emilia=2"),
		},
	}
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "body-admin-exit_nodes", data); err != nil {
		t.Fatalf("render body: %v", err)
	}
	got := buf.String()
	for _, want := range []string{
		`id="relay-weights"`,
		`action="/admin/exit-nodes/relay-weights"`,
		`name="w_emilia" value="2"`,
		`name="w_karolina" value="1"`,
		`min="1" max="99"`,
		`name="reset" value="1"`,
		// the two honest columns: what is carried NOW and what the next 10 would look like
		">17<",
		">140<",
		">7<",
		">3<",
		// the setting as the engine parses it
		"emilia=2",
		// "by default" vs "set" are different facts and must not collapse into one badge
		"exit_nodes.weights.stored",
		"exit_nodes.weights.default",
		"exit_nodes.weights.save",
		"exit_nodes.weights.reset",
		"exit_nodes.weights.help",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the weights card must render %q, got:\n%s", want, got)
		}
	}
}

// TestExitNodesRendersB456_NoWeightsNoCard — an install with nothing to show must not get an empty
// table with headings: a card that says nothing trains the operator to ignore the card.
func TestExitNodesRendersB456_NoWeightsNoCard(t *testing.T) {
	tpl := loadExitNodesBody(t)
	data := map[string]any{
		"Nodes":        []stubExitNodeInfo{},
		"TotalCount":   0,
		"HealthyCount": 0,
		"ControlURL":   "https://head.example.com",
		"SSHKeyPath":   "/ssh-sync/id_ed25519",
		"Page":         "admin/exit_nodes",
		"Title":        "Exit nodes",
		"PrefixAdmin":  map[string]any{"GroupBy": "owner"},
	}
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "body-admin-exit_nodes", data); err != nil {
		t.Fatalf("render body: %v", err)
	}
	if strings.Contains(buf.String(), `id="relay-weights"`) {
		t.Errorf("the weights card rendered without a weights view:\n%s", buf.String())
	}
}

// TestExitNodesRendersB456_RejectedWeightsAreVisible — an entry the engine could not parse (written
// by hand in SQL, or by an older build) means a relay is running at weight 1 while somebody believes
// otherwise. The card must show it instead of quietly re-rendering defaults.
func TestExitNodesRendersB456_RejectedWeightsAreVisible(t *testing.T) {
	tpl := loadExitNodesBody(t)
	data := map[string]any{
		"Nodes":        []stubExitNodeInfo{},
		"TotalCount":   0,
		"HealthyCount": 0,
		"ControlURL":   "https://head.example.com",
		"SSHKeyPath":   "/ssh-sync/id_ed25519",
		"Page":         "admin/exit_nodes",
		"Title":        "Exit nodes",
		"PrefixAdmin": map[string]any{
			"GroupBy": "owner",
			"Weights": b456WeightsView([]map[string]any{
				{"Relay": "emilia", "Weight": 1, "Stored": false, "Load": 3, "Share": 5},
			}, []string{"karolina=abc"}, "karolina=abc"),
		},
	}
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "body-admin-exit_nodes", data); err != nil {
		t.Fatalf("render body: %v", err)
	}
	got := buf.String()
	for _, want := range []string{"exit_nodes.weights.rejected_badge", "karolina=abc"} {
		if !strings.Contains(got, want) {
			t.Errorf("an unparsed weight must be visible (%q missing):\n%s", want, got)
		}
	}
}
