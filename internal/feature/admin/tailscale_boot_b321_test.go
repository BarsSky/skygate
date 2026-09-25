package admin

// tailscale_boot_b321_test.go — B321: the intent and the canonical name must be what
// this process acts on, not what a frozen container environment happens to say.
//
// The live report these tests pin down:
//
//	«при каждом обновлении слетает запуск tailscale приходится прожимать каждый раз
//	 старт и активным стал skygate-host-1 как skygate tailscale хотя должен быть
//	 строго skygate-host»
//
// Two independent defects: the entrypoint's /dev/null sentinel is frozen at container
// creation (so nothing re-applied the operator's earlier "start" decision), and
// tailscaleHostname() returned the same frozen `skygate-host-1` pin, which renamed the
// node straight back to the legacy v0.33.1.9 placeholder.

import (
	"os"
	"path/filepath"
	"testing"

	skygatedb "skygate/internal/db"
	"skygate/internal/headscale"
)

func newB321DB(t *testing.T) *skygatedb.FixedDBSource {
	t.Helper()
	_, d, err := skygatedb.OpenWithDialect("file::memory:?cache=shared")
	if err != nil {
		t.Skipf("sqlite dialect unavailable: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := skygatedb.ApplyMigrations(d, skygatedb.DialectSQLite); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	return &skygatedb.FixedDBSource{DB: d}
}

func writeB321KeyFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "authkey")
	if err := os.WriteFile(path, []byte("tskey-test-1234567890\n"), 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}
	return path
}

// The placeholder shape is the whole reason a "correct" configuration still produced
// `skygate-host-1`: B251 made the canonical name match by STRICT equality, so a
// suffixed client fails every "is this me?" check in the product.
func TestNormalizeTailscaleHostname_B321(t *testing.T) {
	cases := []struct {
		in        string
		want      string
		rewritten bool
	}{
		{"skygate-host-1", "skygate-host", true},
		{"skygate-host-2", "skygate-host", true},
		{"skygate-host-1-1", "skygate-host", true},
		{"SKYGATE-HOST-3", "skygate-host", true},
		{"  skygate-host-4  ", "skygate-host", true},
		{"skygate-host", "skygate-host", false},
		{"my-custom-node", "my-custom-node", false},
		{"skygate-host-", "skygate-host-", false},
		{"skygate-hostx", "skygate-hostx", false},
		{"", "", false},
	}
	for _, c := range cases {
		got, rewritten := NormalizeTailscaleHostname(c.in)
		if got != c.want || rewritten != c.rewritten {
			t.Errorf("NormalizeTailscaleHostname(%q) = (%q, %v), want (%q, %v)",
				c.in, got, rewritten, c.want, c.rewritten)
		}
	}
}

// DB > env > default, and the env pin can no longer win by being frozen into the
// container: once the operator saves a name on the page, that is the name.
func TestTailscaleHostnameResolved_B321(t *testing.T) {
	src := newB321DB(t)
	svc := &Service{DB: src, TailscaleHostname: "skygate-host-1"}

	name, source, rewritten := svc.tailscaleHostnameResolved()
	if name != ReservedSelfHostname || source != "env" || !rewritten {
		t.Fatalf("env pin: got (%q, %q, %v), want (%q, env, true)",
			name, source, rewritten, ReservedSelfHostname)
	}
	if got := svc.tailscaleHostname(); got != ReservedSelfHostname {
		t.Fatalf("tailscaleHostname() = %q, want %q", got, ReservedSelfHostname)
	}

	if err := svc.setTailscaleDesiredState(tailscaleDesiredOn); err != nil {
		t.Fatalf("setTailscaleDesiredState: %v", err)
	}
	if got := svc.tailscaleDesiredState(); got != tailscaleDesiredOn {
		t.Fatalf("tailscaleDesiredState() = %q, want %q", got, tailscaleDesiredOn)
	}

	// The DB wins over the frozen environment value.
	if _, err := src.DB.Exec(`INSERT INTO global_settings (key, value) VALUES (?, ?)`,
		tailscaleHostnameDBKey, "operator-node"); err != nil {
		t.Fatalf("seed tailscale.hostname: %v", err)
	}
	name, source, rewritten = svc.tailscaleHostnameResolved()
	if name != "operator-node" || source != "db" || rewritten {
		t.Fatalf("db override: got (%q, %q, %v), want (operator-node, db, false)", name, source, rewritten)
	}
}

// The decision table. The point of B321 is case 3: the operator's recorded "on"
// outranks the container's frozen `/dev/null` sentinel, while an install that never
// touched the panel keeps the exact pre-B321 env-driven behaviour.
func TestTailscaleAutostartDecision_B321(t *testing.T) {
	old := tailscaleAvailableFn
	tailscaleAvailableFn = func() bool { return true }
	t.Cleanup(func() { tailscaleAvailableFn = old })

	src := newB321DB(t)
	keyFile := writeB321KeyFile(t)
	svc := &Service{DB: src, TailscaleAuthKeyPath: keyFile}
	svcNull := &Service{DB: src, TailscaleAuthKeyPath: "/dev/null"}

	// 1. No intent, /dev/null sentinel → do not start.
	if up, why := svcNull.tailscaleAutostartDecision(); up {
		t.Fatalf("sentinel with no intent must not autostart, got up=true (%s)", why)
	}
	// 2. No intent, a usable key in the environment → start (legacy path).
	if up, why := svc.tailscaleAutostartDecision(); !up {
		t.Fatalf("env-configured key must autostart, got up=false (%s)", why)
	}
	// 3. The operator said ON from the panel → start even though the DB path is
	//    /dev/null, i.e. even though this container's frozen environment disables it.
	if err := svcNull.setTailscaleDesiredState(tailscaleDesiredOn); err != nil {
		t.Fatalf("set intent on: %v", err)
	}
	if err := skygatedb.SetGlobalSetting(src.DB, tailscaleAuthKeyPathDBKey, keyFile); err != nil {
		t.Fatalf("seed auth key path: %v", err)
	}
	if up, why := svcNull.tailscaleAutostartDecision(); !up {
		t.Fatalf("panel intent ON must outrank the frozen /dev/null sentinel, got up=false (%s)", why)
	}
	// 4. The operator said OFF → never start, even with a usable key.
	if err := svc.setTailscaleDesiredState(tailscaleDesiredOff); err != nil {
		t.Fatalf("set intent off: %v", err)
	}
	if up, why := svc.tailscaleAutostartDecision(); up {
		t.Fatalf("panel intent OFF must suppress the autostart, got up=true (%s)", why)
	}
	// 5. Missing binaries → no autostart, and the reason names it.
	tailscaleAvailableFn = func() bool { return false }
	if up, why := svc.tailscaleAutostartDecision(); up || why == "" {
		t.Fatalf("without the binaries the decision must be false with a reason, got (%v, %q)", up, why)
	}
}

// The intent is what makes the start survive a recreate, so it must read back exactly
// as written, and an unknown value must degrade to "unset" — never to a wrong
// decision (a corrupt row must not look like "off").
func TestTailscaleDesiredState_RoundTrip_B321(t *testing.T) {
	src := newB321DB(t)
	svc := &Service{DB: src}
	if got := svc.tailscaleDesiredState(); got != "" {
		t.Fatalf("fresh DB must be unset, got %q", got)
	}
	if err := svc.setTailscaleDesiredState("ON"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if got := svc.tailscaleDesiredState(); got != tailscaleDesiredOn {
		t.Fatalf("after set(ON) got %q, want %q", got, tailscaleDesiredOn)
	}
	if _, err := src.DB.Exec(`UPDATE global_settings SET value = ? WHERE key = ?`,
		"yes-please", tailscaleDesiredStateDBKey); err != nil {
		t.Fatalf("seed bogus value: %v", err)
	}
	if got := svc.tailscaleDesiredState(); got != "" {
		t.Fatalf("a bogus value must read as unset, got %q", got)
	}
}

// cleanupStaleSelfTags is the B321 half that removes the tag the PREVIOUS name left
// behind. Its guards matter more than its happy path: it runs automatically after a
// reclaim, so it must refuse to do anything at all without a headscale client, and it
// must treat "same name" as nothing-to-do.
func TestCleanupStaleSelfTags_Guards_B321(t *testing.T) {
	svc := &Service{}
	if got := svc.cleanupStaleSelfTags("skygate-host-1", ReservedSelfHostname); got != nil {
		t.Fatalf("no headscale client must yield nil, got %v", got)
	}
	svc.HSGlobalFn = func() *headscale.Client { return nil }
	if got := svc.cleanupStaleSelfTags("skygate-host-1", ReservedSelfHostname); got != nil {
		t.Fatalf("nil headscale client must yield nil, got %v", got)
	}
	if got := svc.cleanupStaleSelfTags(ReservedSelfHostname, ReservedSelfHostname); got != nil {
		t.Fatalf("identical names must yield nil, got %v", got)
	}
	if got := svc.cleanupStaleSelfTags("", ReservedSelfHostname); got != nil {
		t.Fatalf("empty previous name must yield nil, got %v", got)
	}
}

// B321.1 — the leftover tag the operator actually has. headscale tags are additive, so a
// rename leaves the old `tag:dev-infra-<old-name>` on the node: live, node 87 wore BOTH
// `tag:dev-infra-skygate-host` and `tag:dev-infra-skygate-host-1` after a successful
// reclaim, and the reclaim button answers "no stale node holds ..." once the ghost is gone
// — so this selection is what lets the autostart tick clear it without a manual untag.
func TestStaleInfraTags_B321(t *testing.T) {
	cases := []struct {
		name string
		tags []string
		want []string
	}{
		{
			"the live leftover",
			[]string{"tag:dev-infra-skygate-host", "tag:dev-infra-skygate-host-1", "tag:private"},
			[]string{"tag:dev-infra-skygate-host-1"},
		},
		{"only the canonical tag", []string{"tag:dev-infra-skygate-host"}, nil},
		{
			"other families are never touched",
			[]string{"tag:exit-node", "tag:private", "tag:subnet-router", "tag:dev-michail-basic"},
			nil,
		},
		{
			"the legacy tag:infra-<host> form is covered too",
			[]string{"tag:infra-old-name", "tag:dev-infra-skygate-host"},
			[]string{"tag:infra-old-name"},
		},
		{"the suffix comparison is case-insensitive", []string{"tag:dev-infra-SKYGATE-HOST"}, nil},
	}
	for _, c := range cases {
		got := staleInfraTags(c.tags, "skygate-host")
		if len(got) != len(c.want) {
			t.Errorf("%s: staleInfraTags(%v) = %v, want %v", c.name, c.tags, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: staleInfraTags(%v) = %v, want %v", c.name, c.tags, got, c.want)
				break
			}
		}
	}
}
