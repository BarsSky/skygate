// v1.5.0+ / B212 — unit tests for the DSN bootstrap
// helpers + JoinResponse field pinning. B212 added:
//   - JoinResponse.DSN (substituted DSN)
//   - JoinResponse.PrimaryHost (the hostname we substituted)
//   - substituteDSNTemplate() helper
//   - readPrimaryHost() helper
//
// The DB-hitting helpers (readPrimaryHost) are
// covered by the live-verify on the agent. substituteDSNTemplate
// is pure-Go and pinned here.

package cluster

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSubstituteDSNTemplate(t *testing.T) {
	cases := []struct {
		name string
		tpl  string
		host string
		want string
	}{
		{
			name: "empty template",
			tpl:  "",
			host: "skygate-host-1",
			want: "",
		},
		{
			name: "no %s placeholder (hardcoded host)",
			tpl:  "postgres://admin:pass@localhost:5432/mydb?sslmode=disable",
			host: "skygate-host-1",
			want: "postgres://admin:pass@localhost:5432/mydb?sslmode=disable",
		},
		{
			name: "single %s substitution",
			tpl:  "postgres://admin:pass@%s:5433/skygate_staging?sslmode=disable",
			host: "skygate-host-1",
			want: "postgres://admin:pass@skygate-host-1:5433/skygate_staging?sslmode=disable",
		},
		{
			name: "only the first %s is substituted (user position left alone)",
			tpl:  "postgres://%s:pass@%s:5432/db",
			host: "skygate-host-1",
			want: "postgres://skygate-host-1:pass@%s:5432/db",
		},
		{
			name: "host empty + template has %s",
			tpl:  "postgres://admin:pass@%s:5433/db",
			host: "",
			want: "", // no host = no DSN bootstrap available
		},
		{
			name: "host empty + template has no %s (no substitution needed)",
			tpl:  "postgres://admin:pass@localhost:5432/db",
			host: "",
			want: "postgres://admin:pass@localhost:5432/db",
		},
		{
			name: "Tailscale IP as host (100.64.0.x range)",
			tpl:  "postgres://admin:pass@%s:5433/db",
			host: "100.64.0.24",
			want: "postgres://admin:pass@100.64.0.24:5433/db",
		},
		{
			name: "IPv6 host (no brackets — go pgx accepts either)",
			tpl:  "postgres://admin:pass@%s:5433/db",
			host: "::1",
			want: "postgres://admin:pass@::1:5433/db",
		},
		{
			name: "host with Tailscale MagicDNS name (skygate-host-1.tsnet.skynas.ru)",
			tpl:  "postgres://admin:pass@%s:5433/db",
			host: "skygate-host-1.tsnet.skynas.ru",
			want: "postgres://admin:pass@skygate-host-1.tsnet.skynas.ru:5433/db",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := substituteDSNTemplate(c.tpl, c.host)
			if got != c.want {
				t.Errorf("substituteDSNTemplate(%q, %q) = %q, want %q", c.tpl, c.host, got, c.want)
			}
		})
	}
}

func TestSubstituteDSNTemplate_IdempotentForEmptyHost(t *testing.T) {
	// When the template has no %s and host is empty,
	// the template is returned as-is. This is the
	// "hardcoded localhost" fallback path — the
	// standby can use the DSN directly without
	// knowing the primary's hostname.
	tpl := "postgres://admin:pass@localhost:5432/db"
	got := substituteDSNTemplate(tpl, "")
	if got != tpl {
		t.Errorf("expected template-as-is for empty host + no %%s; got %q", got)
	}
}

func TestJoinResponse_B212Fields(t *testing.T) {
	// B212 added DSN + PrimaryHost to JoinResponse.
	// Pin the JSON tag names so a future refactor
	// doesn't silently break the standby's parse path.
	resp := JoinResponse{
		ClusterID:     "skygate-staging",
		NodeID:        "node-abc",
		Hostname:      "skygate-standby-1",
		DSNTemplate:   "postgres://...@%s:5433/db",
		DSN:           "postgres://...@skygate-host-1:5433/db",
		PrimaryHost:   "skygate-host-1",
		DBName:        "skygate_staging",
		DBUsername:    "admin",
		HeartbeatHint: 30,
	}
	// Marshal and assert the JSON tag names — the
	// public contract for /api/cluster/join's
	// response. The standby's `skygate join` parses
	// these.
	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	js := string(b)
	for _, want := range []string{
		`"dsn":"postgres://...@skygate-host-1:5433/db"`,
		`"primary_host":"skygate-host-1"`,
		`"dsn_template":"postgres://...@%s:5433/db"`,
		`"node_id":"node-abc"`,
		`"heartbeat_seconds":30`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("JoinResponse JSON missing %q; got: %s", want, js)
		}
	}
}

func TestJoinResponse_B212DSNBackwardCompat(t *testing.T) {
	// Backward compat: B200 / B201 clients only know
	// about dsn_template. B212 added dsn alongside
	// it (not replacing it). Assert that a response
	// with both fields has both keys present.
	resp := JoinResponse{
		DSNTemplate: "postgres://...@%s:5433/db",
		DSN:         "postgres://...@primary:5433/db",
	}
	b, _ := json.Marshal(resp)
	js := string(b)
	if !strings.Contains(js, `"dsn_template"`) {
		t.Errorf("B212 dropped dsn_template; got: %s", js)
	}
	if !strings.Contains(js, `"dsn"`) {
		t.Errorf("B212 missing dsn field; got: %s", js)
	}
}
