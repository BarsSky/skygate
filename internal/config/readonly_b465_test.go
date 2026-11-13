package config

import (
	"os"
	"strings"
	"testing"
)

// B465 follow-up, measured on the live restored copy (2026-11-12 21:09 UTC): the guard that
// lived in the scheduler-wiring functions at the END of main() was printed and then the same
// process started
//
//	reconcile: cron enabled           (users reconcile — writes portal_users↔headscale links)
//	autoupdater: starting             (writes the operator's DNS)
//	preferred-reconciler: starting    (writes device_exit_node_prefs + the ACL policy)
//	update-scheduler: ARMED           (may self-update the process)
//	backup-scheduler: started         (dumps the DB and uploads it to S3)
//
// because those start sites are scattered across main(). The fix is one decision on the
// CONFIGURATION, which every start site already consults. These tests pin that decision: a
// writer that is on before the call must be off after it, and nothing may re-enable it.
func withReadOnlyEnv(t *testing.T, value string) {
	t.Helper()
	prev, had := os.LookupEnv("SKYGATE_READONLY_COPY")
	t.Cleanup(func() {
		if had {
			_ = os.Setenv("SKYGATE_READONLY_COPY", prev)
			return
		}
		_ = os.Unsetenv("SKYGATE_READONLY_COPY")
	})
	if value == "" {
		_ = os.Unsetenv("SKYGATE_READONLY_COPY")
		return
	}
	_ = os.Setenv("SKYGATE_READONLY_COPY", value)
}

// allWritersOn returns a config with every writer switch explicitly ON, so the test proves
// ApplyReadOnlyCopy turns them OFF rather than merely observing defaults that are off already.
func allWritersOn() *Config {
	return &Config{
		DNSAutoUpdateEnabled:         true,
		PrefReconcileInterval:        60 * 60 * 1e9,
		NodeDiscoveryInterval:        60 * 1e9,
		StaggerInterval:              30 * 1e9,
		ExitNodeCheckInterval:        5 * 60 * 1e9,
		HeadscalePollInterval:        24 * 60 * 60 * 1e9,
		ReconcileHeadscaleUsers:      true,
		ExpireWatchEnabled:           true,
		ExpireWatchInterval:          5 * 60 * 1e9,
		UpdateCheckEnabled:           true,
		UpdateScheduleEnabled:        true,
		BackupVerifyInAppEnabled:     true,
		CleanupSmokeMeshInAppEnabled: true,
		CertSyncEnabled:              true,
		CertSyncInterval:             30 * 1e9,
		TokenAutoRotateEnabled:       true,
		KeyNotifyEnabled:             true,
		ACMERenewEnabled:             true,
		HAEnabled:                    true,
	}
}

// TestB465_ApplyReadOnlyCopyDisablesEveryWriter is the whole point of the call: a copy must
// not have a single writer left on, and the list it returns is what the boot log prints (so
// an operator can see WHICH passes the flag took away instead of guessing).
func TestB465_ApplyReadOnlyCopyDisablesEveryWriter(t *testing.T) {
	withReadOnlyEnv(t, "1")
	c := allWritersOn()
	off := c.ApplyReadOnlyCopy()
	if len(off) != 17 {
		t.Fatalf("expected 17 disabled passes, got %d: %v", len(off), off)
	}
	for _, name := range []string{
		"dns-autoupdate", "preferred-reconciler", "node-discovery", "exit-node-monitor",
		"headscale-update-monitor", "reconcile-headscale-users", "update-check",
		"update-scheduler", "backup-verify", "smoke-mesh-cleanup", "certsync",
		"token-rotate", "key-expiry-notify", "ha-chain", "acme-renew",
	} {
		found := false
		for _, got := range off {
			if got == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("the disabled list does not name %q: %v", name, off)
		}
	}
	if c.DNSAutoUpdateEnabled || c.ReconcileHeadscaleUsers || c.UpdateScheduleEnabled ||
		c.BackupVerifyInAppEnabled || c.CertSyncEnabled || c.TokenAutoRotateEnabled ||
		c.KeyNotifyEnabled || c.HAEnabled || c.ExpireWatchEnabled || c.UpdateCheckEnabled ||
		c.CleanupSmokeMeshInAppEnabled {
		t.Fatalf("a boolean writer survived: %+v", c)
	}
	if c.PrefReconcileInterval != 0 || c.NodeDiscoveryInterval != 0 || c.ExitNodeCheckInterval != 0 ||
		c.HeadscalePollInterval != 0 || c.StaggerInterval != 0 || c.ExpireWatchInterval != 0 ||
		c.CertSyncInterval != 0 {
		t.Fatalf("an interval writer survived: %+v", c)
	}
}

// TestB465_ApplyReadOnlyCopyIsANoOpOnAnActingHost — the flag must be invisible on the normal
// host, otherwise every install would go quiet after an upgrade.
func TestB465_ApplyReadOnlyCopyIsANoOpOnAnActingHost(t *testing.T) {
	withReadOnlyEnv(t, "")
	c := allWritersOn()
	if off := c.ApplyReadOnlyCopy(); off != nil {
		t.Fatalf("an acting host must disable nothing, got %v", off)
	}
	if !c.DNSAutoUpdateEnabled || !c.HAEnabled || c.PrefReconcileInterval == 0 {
		t.Fatalf("an acting host lost a writer: %+v", c)
	}
}

// TestB465_ReadOnlyCopyAcceptsTheDocumentedSpellings — the env reader is the single source of
// truth for "am I a copy", and a copy that does not recognise its own value is silent about
// being a copy.
func TestB465_ReadOnlyCopyAcceptsTheDocumentedSpellings(t *testing.T) {
	for _, v := range []string{"1", "true", "TRUE", " yes ", "on"} {
		withReadOnlyEnv(t, v)
		if !ReadOnlyCopy() {
			t.Errorf("%q must mean read-only", v)
		}
	}
	for _, v := range []string{"", "0", "false", "no", "off", "2"} {
		withReadOnlyEnv(t, v)
		if ReadOnlyCopy() {
			t.Errorf("%q must NOT mean read-only", v)
		}
	}
}

// TestB465_ApplyReadOnlyCopyIsIdempotent — main() may be re-entered by a test harness and a
// restart-within-process path; a second call must not report work it did not do.
func TestB465_ApplyReadOnlyCopyIsIdempotent(t *testing.T) {
	withReadOnlyEnv(t, "1")
	c := allWritersOn()
	first := c.ApplyReadOnlyCopy()
	second := c.ApplyReadOnlyCopy()
	if len(first) == 0 {
		t.Fatal("first call disabled nothing")
	}
	if len(second) != 0 {
		t.Fatalf("second call reported work it did not do: %v", second)
	}
	if !strings.Contains(strings.Join(first, ","), "certsync") {
		t.Fatalf("the first call must name certsync: %v", first)
	}
}
