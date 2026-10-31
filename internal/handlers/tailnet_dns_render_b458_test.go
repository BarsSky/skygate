// tailnet_dns_render_b458_test.go — B458 (2026-10-31). The page must SAY when DNS leaves
// the tailnet.
//
// The defect this block removes was invisible: headscale handed `[1.1.1.1, 8.8.8.8]` to every
// client while the tailnet carried only the second address, and no page or log line said so.
// These render tests pin the three states the card can be in — carried, configured but not
// advertised, and unreadable — because "the resolver is configured" and "the resolver is
// reachable through the tailnet" are different facts.
package handlers

import (
	"bytes"
	"strings"
	"testing"
)

func b458DNSPrefix(prefix, owner, source string, carried, advertised bool) map[string]any {
	return map[string]any{"Prefix": prefix, "Owner": owner, "Source": source, "Carried": carried, "Advertised": advertised}
}

// TestExitNodesRendersB458_TailnetDNSCarried — the healthy case: both resolvers advertised,
// no leak text.
func TestExitNodesRendersB458_TailnetDNSCarried(t *testing.T) {
	tpl := loadExitNodesBody(t)
	data := map[string]any{
		"Nodes": []stubExitNodeInfo{}, "TotalCount": 0, "HealthyCount": 0,
		"ControlURL": "https://head.example.com", "SSHKeyPath": "/ssh-sync/id_ed25519",
		"Page": "admin/exit_nodes", "Title": "Exit nodes",
		"TailnetDNS": map[string]any{
			"Known": true, "Source": "/etc/headscale/config.yaml",
			"Resolvers": []string{"1.1.1.1", "8.8.8.8"},
			"Rows": []map[string]any{
				b458DNSPrefix("1.1.1.1/32", "emilia", "system", true, true),
				b458DNSPrefix("8.8.8.8/32", "emilia", "explicit", true, true),
			},
		},
	}
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "body-admin-exit_nodes", data); err != nil {
		t.Fatalf("render body: %v", err)
	}
	got := buf.String()
	for _, want := range []string{
		`id="tailnet-dns"`,
		"exit_nodes.tailnet_dns.title",
		"exit_nodes.tailnet_dns.state_carried",
		"1.1.1.1/32",
		"8.8.8.8/32",
		"/etc/headscale/config.yaml",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the DNS card must render %q", want)
		}
	}
	if strings.Contains(got, "exit_nodes.tailnet_dns.leak") {
		t.Errorf("a fully carried resolver set must not claim a leak:\n%s", got)
	}
}

// TestExitNodesRendersB458_LeakIsNamed — the live shape: 1.1.1.1 configured but advertised
// by nobody. The card must name it as a danger, not as "configured".
func TestExitNodesRendersB458_LeakIsNamed(t *testing.T) {
	tpl := loadExitNodesBody(t)
	data := map[string]any{
		"Nodes": []stubExitNodeInfo{}, "TotalCount": 0, "HealthyCount": 0,
		"ControlURL": "https://head.example.com", "SSHKeyPath": "/ssh-sync/id_ed25519",
		"Page": "admin/exit_nodes", "Title": "Exit nodes",
		"TailnetDNS": map[string]any{
			"Known": true, "Source": "/etc/headscale/config.yaml",
			"Resolvers": []string{"1.1.1.1", "8.8.8.8"},
			"Rows": []map[string]any{
				b458DNSPrefix("1.1.1.1/32", "", "", false, false),
				b458DNSPrefix("8.8.8.8/32", "emilia", "explicit", true, true),
			},
			"NotCarried": []string{"1.1.1.1/32"},
		},
	}
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "body-admin-exit_nodes", data); err != nil {
		t.Fatalf("render body: %v", err)
	}
	got := buf.String()
	for _, want := range []string{
		"exit_nodes.tailnet_dns.state_missing",
		"exit_nodes.tailnet_dns.leak",
		"exit_nodes.tailnet_dns.no_owner",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("an uncarried resolver must be named (%q missing)", want)
		}
	}
}

// TestExitNodesRendersB458_UnknownSetIsNotHealthy — an unreadable resolver set must not look
// like "no resolvers configured": it says it could not be read.
func TestExitNodesRendersB458_UnknownSetIsNotHealthy(t *testing.T) {
	tpl := loadExitNodesBody(t)
	data := map[string]any{
		"Nodes": []stubExitNodeInfo{}, "TotalCount": 0, "HealthyCount": 0,
		"ControlURL": "https://head.example.com", "SSHKeyPath": "/ssh-sync/id_ed25519",
		"Page": "admin/exit_nodes", "Title": "Exit nodes",
		"TailnetDNS": map[string]any{
			"Known": false,
			"Note":  "no headscale config.yaml was readable",
		},
	}
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "body-admin-exit_nodes", data); err != nil {
		t.Fatalf("render body: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, "exit_nodes.tailnet_dns.unknown") {
		t.Errorf("an unreadable resolver set must be stated:\n%s", got)
	}
}
