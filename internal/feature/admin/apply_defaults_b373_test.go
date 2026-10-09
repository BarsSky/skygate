// B373 — the panel's "apply the defaults" buttons, pinned as PROPERTIES.
//
// The work order was «добавь подсказки с активными кнопками чтобы пользователь мог
// как прочитать что надо сделать и для чего так и применить параметры по умолчанию»
// — a hint that explains, next to a button that acts. Two defects had to be closed
// for the button to be honest:
//
//  1. cluster_database.dsn_template had TWO placeholder conventions. The panel (and
//     buildDSNTemplate, and the CLI/i18n help texts) put %s in the PASSWORD position
//     and claimed a "Phase 3.1 watchdog" substitutes it at read time; the only
//     substitution in the tree — internal/cluster.substituteDSNTemplate(tpl, host) —
//     puts the primary's HOSTNAME there. Filling the template the way the panel
//     described therefore made `skygate join` hand the standby
//     `postgres://user:<primary-hostname>@host/db`, i.e. the hostname in the password
//     field, and it could never authenticate.
//  2. the Tailscale "apply the defaults" action did not exist: the operator could
//     Start/Stop/Enable, but nothing persisted "this is the intended state" in one
//     click, and the reason Tailscale comes back down is not in the panel at all
//     (the container environment is frozen at CREATION and pins the /dev/null
//     sentinel).
//
// These tests pin the two properties that make the buttons safe:
//   - the composed template carries the %s where the HOST goes and NEVER a password
//     (it is rendered on the page, written to audit_log and copied by every backup);
//   - the "defaults" action writes a real path + intent + canonical name, and it
//     refuses to invent a key when no usable file exists.
package admin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"skygate/internal/db"
)

// TestB373_DSNTemplatePutsThePlaceholderWhereTheHostGoes is the convention pin.
func TestB373_DSNTemplatePutsThePlaceholderWhereTheHostGoes(t *testing.T) {
	tpl, host, port, dbname, user, sslmode, ok := dsnTemplateFromDSN(
		"postgres://admin:sup3r-s3cret@10.11.12.13:5433/skygate_staging?sslmode=disable")
	if !ok {
		t.Fatal("a well-formed PostgreSQL DSN must produce a template")
	}
	if tpl != "postgres://admin@%s:5433/skygate_staging?sslmode=disable" {
		t.Errorf("template = %q — the ONE %%s must sit where the HOST goes (the join substitutes the primary's hostname)", tpl)
	}
	if host != "10.11.12.13" || port != "5433" || dbname != "skygate_staging" || user != "admin" || sslmode != "disable" {
		t.Errorf("parsed fields = %q/%q/%q/%q/%q", host, port, dbname, user, sslmode)
	}
	// The property that must never regress: no credential in the value that is
	// stored in cluster_database, rendered on /admin/database and audited.
	if strings.Contains(tpl, "sup3r-s3cret") || strings.Contains(strings.ToLower(tpl), "password") {
		t.Errorf("the template carries a credential: %q", tpl)
	}
	if strings.Count(tpl, "%s") != 1 {
		t.Errorf("template must contain exactly one %%s, got %q", tpl)
	}
	if strings.Contains(tpl, host) {
		t.Errorf("the template pins the primary's own host (%q) instead of %%s — the same row must work after a failover renames the primary", host)
	}
	// And the shape the join understands: substituting the host yields a DSN whose
	// host field is the primary (this is what substituteDSNTemplate does).
	joined := strings.Replace(tpl, "%s", "skygate-host", 1)
	if joined != "postgres://admin@skygate-host:5433/skygate_staging?sslmode=disable" {
		t.Errorf("substituted DSN = %q, want the host in the host position", joined)
	}
}

// TestB373_ThePanelComposerMatchesTheJoinConvention pins that the EDIT form and
// the new button cannot drift apart again — the defect that made the old template
// unusable.
func TestB373_ThePanelComposerMatchesTheJoinConvention(t *testing.T) {
	fromForm := dsnTemplateFromParts("admin", "10.11.12.13", "5433", "skygate_staging", "disable")
	fromDSN, _, _, _, _, _, ok := dsnTemplateFromDSN(
		"postgres://admin:pw@10.11.12.13:5433/skygate_staging?sslmode=disable")
	if !ok || fromForm != fromDSN {
		t.Errorf("the two composers disagree: form=%q dsn=%q (they must produce one convention)", fromForm, fromDSN)
	}
	// Defaults: no port → 5432, no sslmode → disable.
	if got := dsnTemplateFromParts("admin", "h", "", "skygate", ""); got != "postgres://admin@%s:5432/skygate?sslmode=disable" {
		t.Errorf("defaults = %q", got)
	}
}

// TestB373_UnparseableDSNsAreRefusedNotGuessed: a SQLite file path, a DSN without a
// user, and an empty string must all refuse. A guess here is what produced a broken
// template in the first place.
func TestB373_UnparseableDSNsAreRefusedNotGuessed(t *testing.T) {
	for _, in := range []string{
		"",
		"/var/lib/skygate/skygate.db",
		"file:/data/skygate.db?_pragma=journal_mode(WAL)",
		"postgres://10.11.12.13:5432/skygate_staging?sslmode=disable", // no user
		"postgres://admin:pw@10.11.12.13:5432?sslmode=disable",        // no dbname
	} {
		if tpl, _, _, _, _, _, ok := dsnTemplateFromDSN(in); ok {
			t.Errorf("dsnTemplateFromDSN(%q) accepted the input and produced %q — it must refuse", in, tpl)
		}
	}
}

// TestB373_TheButtonUpsertsTheRow is the behavioural half for /admin/database: the
// row may not exist at all (measured live: 0 rows on a primary whose page was never
// opened), so the write must CREATE it, and the stored value must be the template.
func TestB373_TheButtonUpsertsTheRow(t *testing.T) {
	svc, raw := b365Service(t)

	// Precondition: no row at all — the live gap.
	if _, err := db.GetClusterDatabase(raw, "skygate-staging"); err == nil {
		t.Fatal("setup: the cluster_database row should not exist yet")
	}

	tpl, err := svc.saveDSNTemplateFromDSN(
		"postgres://admin:sup3r-s3cret@10.11.12.13:5433/skygate_staging?sslmode=disable", "skyadmin")
	if err != nil {
		t.Fatalf("saveDSNTemplateFromDSN: %v", err)
	}
	row, err := db.GetClusterDatabase(raw, "skygate-staging")
	if err != nil || row == nil {
		t.Fatalf("the button did not create the row: %v", err)
	}
	if row.DSNTemplate != tpl || !strings.Contains(row.DSNTemplate, "@%s:5433/") {
		t.Errorf("stored template = %q, want the host-placeholder shape", row.DSNTemplate)
	}
	if row.Username != "admin" || row.DBName != "skygate_staging" || row.SSLMode != "disable" {
		t.Errorf("stored fields = %+v", row)
	}
	if strings.Contains(row.DSNTemplate, "sup3r-s3cret") {
		t.Error("the password reached cluster_database — the hint card promises it never does")
	}
	// current_dsn is the human-readable pointer and must say PASSWORD, not the secret.
	if !strings.Contains(row.CurrentDSN, ":PASSWORD@") || strings.Contains(row.CurrentDSN, "sup3r-s3cret") {
		t.Errorf("current_dsn = %q, want the :PASSWORD@ placeholder", row.CurrentDSN)
	}

	// Idempotent: a second click rewrites the same row (upsert).
	if _, err := svc.saveDSNTemplateFromDSN(
		"postgres://admin:other@10.11.12.13:5433/skygate_staging?sslmode=require", "skyadmin"); err != nil {
		t.Fatalf("second save: %v", err)
	}
	again, _ := db.GetClusterDatabase(raw, "skygate-staging")
	if !strings.HasSuffix(again.DSNTemplate, "sslmode=require") {
		t.Errorf("the second click did not update the row: %q", again.DSNTemplate)
	}

	// A non-PostgreSQL "DSN" is refused with the named error, and the row is kept.
	if _, err := svc.saveDSNTemplateFromDSN("/var/lib/skygate/skygate.db", "skyadmin"); err == nil {
		t.Error("a SQLite path must be refused, not turned into a template")
	}
}

// TestB373_UsableAuthKeyFileRejectsTheSentinelAndEmptyFiles: the sentinel, an empty
// file, and a directory must all be unusable — otherwise the button would persist
// "the default" as /dev/null and reproduce the very state it exists to fix.
func TestB373_UsableAuthKeyFileRejectsTheSentinelAndEmptyFiles(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, nil, 0600); err != nil {
		t.Fatal(err)
	}
	blank := filepath.Join(dir, "blank")
	if err := os.WriteFile(blank, []byte("   \n"), 0600); err != nil {
		t.Fatal(err)
	}
	good := filepath.Join(dir, "authkey")
	if err := os.WriteFile(good, []byte("tskey-auth-abc123\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		path string
		want bool
	}{
		{"/dev/null", false},
		{"", false},
		{dir, false},
		{filepath.Join(dir, "missing"), false},
		{empty, false},
		{blank, false},
		{good, true},
	}
	for _, c := range cases {
		if got := usableAuthKeyFile(c.path); got != c.want {
			t.Errorf("usableAuthKeyFile(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

// TestB373_DefaultCandidatesAreOrdered: the DB override wins, then the env var, then
// the default path, with no duplicates — the order tailscaleAuthKeyPath() documents.
func TestB373_DefaultCandidatesAreOrdered(t *testing.T) {
	svc, raw := b365Service(t)
	svc.TailscaleAuthKeyPath = "/env/ts/authkey"
	if err := db.SetGlobalSetting(raw, tailscaleAuthKeyPathDBKey, "/db/ts/authkey"); err != nil {
		t.Fatal(err)
	}
	got := svc.tailscaleDefaultKeyCandidates()
	want := []string{"/db/ts/authkey", "/env/ts/authkey", "/data/ts/authkey"}
	if len(got) != len(want) {
		t.Fatalf("candidates = %+v, want %d entries", got, len(want))
	}
	for i := range want {
		if got[i].Path != want[i] {
			t.Errorf("candidate %d = %q, want %q", i, got[i].Path, want[i])
		}
	}
	// Dedup: an env var equal to the default must not appear twice.
	svc.TailscaleAuthKeyPath = "/data/ts/authkey"
	got = svc.tailscaleDefaultKeyCandidates()
	seen := map[string]int{}
	for _, c := range got {
		seen[c.Path]++
	}
	for p, n := range seen {
		if n > 1 {
			t.Errorf("%q appears %d times", p, n)
		}
	}
}

// TestB373_ApplyDefaultsPersistsTheThreeKeys is the behavioural half for
// /admin/tailscale: the button must record a real path, the intent, and the
// canonical name — the three things that make the client survive an update.
func TestB373_ApplyDefaultsPersistsTheThreeKeys(t *testing.T) {
	svc, raw := b365Service(t)
	if err := svc.persistTailscaleDefaults("/data/ts/authkey", "skygate-host"); err != nil {
		t.Fatalf("persistTailscaleDefaults: %v", err)
	}
	for key, want := range map[string]string{
		tailscaleAuthKeyPathDBKey:  "/data/ts/authkey",
		tailscaleDesiredStateDBKey: tailscaleDesiredOn,
		tailscaleHostnameDBKey:     "skygate-host",
	} {
		got, err := db.GetGlobalSetting(raw, key, "")
		if err != nil {
			t.Fatalf("read %s: %v", key, err)
		}
		if got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	// The decision the boot pass makes must now be "yes" — that is the whole point
	// of persisting the intent (the key file itself is checked separately).
	if svc.tailscaleDesiredState() != tailscaleDesiredOn {
		t.Errorf("tailscaleDesiredState() = %q, want %q", svc.tailscaleDesiredState(), tailscaleDesiredOn)
	}
}
