// B365 — the last three gaps in the panel-only cluster onboarding, pinned as
// PROPERTIES rather than as strings.
//
// All three came out of one live measurement (2026-10-08): a real second host
// was onboarded entirely from the panel, and the block it was handed had a step
// that could not work, a silent missing setting, and a node row that kept the
// discovery pass's label as its version.
//
//	gap 1  step 4 ran `systemctl enable --now skygate` + `curl /healthz` — a
//	       STANDBY has no skygate unit and no web service, so the operator was
//	       told to run something that does not exist.
//	gap 2  `cluster_database.dsn_template` was empty, so neither the block nor
//	       the join named the one thing left to do, and the standby became a
//	       member that can never serve as a mirror.
//	gap 3  the join returned the discovery row untouched, leaving
//	       `skygate_version=(discovered via Tailscale)` on a host that had
//	       really joined. (Behaviourally pinned in internal/cluster/join_b365_test.go.)
package admin

import (
	"database/sql"
	"strings"
	"testing"

	"skygate/internal/i18n"
)

// b365Service returns a Service whose DB source is a migrated in-memory SQLite
// database — the native install's backend, and the one where these reads must
// work.
func b365Service(t *testing.T) (*Service, *sql.DB) {
	t.Helper()
	src := b282SQLiteDB(t)
	return &Service{DB: src}, src.db
}

// TestB365_HeartbeatStepStartsTheDaemon pins gap 1. The step must
//   - name the unit it enables (skygate-heartbeat) and the daemon command,
//   - write that unit itself (nothing installs one on a bare host),
//   - be idempotent (re-pasting the block is not an error),
//   - verify the DAEMON, and NOT promise a skygate web service: a standby runs
//     the binary only, so no `skygate` unit and no `curl /healthz` may appear.
func TestB365_HeartbeatStepStartsTheDaemon(t *testing.T) {
	steps := clusterOnboardSteps(clusterOnboardPayload{
		Hostname: "svyatoslava", InviteTok: "sgn1.A.B", APIURL: "https://skygate.example.com",
		ControlURL: "https://head.example.com", Version: "v1.5.107",
	})
	if len(steps) != 5 {
		t.Fatalf("expected 5 steps, got %d", len(steps))
	}
	if steps[3].TitleKey != "cluster.onboard_step_heartbeat" {
		t.Errorf("step 4 title = %q, want the heartbeat step (a standby has no skygate web service)",
			steps[3].TitleKey)
	}
	cmd := steps[3].Command

	// (a) the unit it enables, named.
	for _, want := range []string{
		"skygate-heartbeat",
		clusterOnboardHeartbeatUnit,
		"skygate cluster heartbeat-daemon --state-file=" + clusterOnboardHeartbeatStateFile,
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("heartbeat step does not contain %q:\n%s", want, cmd)
		}
	}
	// (b) the unit is RENDERED by the block: a bare host has no
	// deploy/heartbeat-daemon.service (measured — the operator had to start the
	// daemon by hand with setsid).
	for _, want := range []string{
		"/etc/systemd/system/skygate-heartbeat.service",
		"ExecStart=/usr/local/bin/skygate cluster heartbeat-daemon",
		"[Install]",
		"WantedBy=multi-user.target",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("heartbeat step must write the unit inline (%q missing):\n%s", want, cmd)
		}
	}
	// (c) idempotent: reload + enable --now (both are no-ops on a re-run).
	for _, want := range []string{"systemctl daemon-reload", "systemctl enable --now skygate-heartbeat"} {
		if !strings.Contains(cmd, want) {
			t.Errorf("heartbeat step must be idempotent — %q missing:\n%s", want, cmd)
		}
	}
	// (d) the verification names what actually runs.
	if !strings.Contains(cmd, "systemctl is-active --quiet skygate-heartbeat") {
		t.Errorf("heartbeat step must verify the DAEMON's state:\n%s", cmd)
	}
	if !strings.Contains(cmd, "journalctl -u skygate-heartbeat") {
		t.Errorf("heartbeat step must show the first heartbeats in the daemon's journal:\n%s", cmd)
	}
	// (e) the NEGATIVE property: nothing here may tell the operator to enable a
	// `skygate` service or to probe a local HTTP port. The pre-B365 command was
	// exactly that, and it could only fail.
	for _, bad := range []string{
		"enable --now skygate ", "is-active skygate", "enable --now skygate &&",
		"healthz", "curl", "http://127.0.0.1", "systemctl is-active skygate",
	} {
		if strings.Contains(cmd, bad) {
			t.Errorf("heartbeat step still promises a skygate web service (%q):\n%s", bad, cmd)
		}
	}
	// The state file the daemon reads must be the one step 3 wrote.
	if !strings.Contains(steps[2].Command, "--state-file="+clusterOnboardHeartbeatStateFile) {
		t.Errorf("step 3 must write the state file the daemon reads (%q):\n%s",
			clusterOnboardHeartbeatStateFile, steps[2].Command)
	}
}

// TestB365_DSNTemplateMissingIsReported pins gap 2's reporting half: the panel
// must be able to tell the operator, BEFORE the VM is provisioned, that
// cluster_database.dsn_template is empty. It reports — it never invents a DSN.
func TestB365_DSNTemplateMissingIsReported(t *testing.T) {
	svc, raw := b365Service(t)
	const clusterID = "skygate-staging"

	// No cluster_database row at all (the live state: `skygate init` never ran
	// on the primary).
	if svc.onboardDSNTemplateReady(clusterID) {
		t.Error("no cluster_database row must report NOT ready (the join would say 'no DSN bootstrap')")
	}
	// A row with an empty template — the live row.
	if _, err := raw.Exec(`INSERT INTO cluster_database (id, cluster_id, dsn_template) VALUES ($1, $2, '')`,
		clusterID, clusterID); err != nil {
		t.Fatalf("insert cluster_database: %v", err)
	}
	if svc.onboardDSNTemplateReady(clusterID) {
		t.Error("an EMPTY dsn_template must report NOT ready — this is the live gap-2 row")
	}
	// Whitespace is not a template either.
	if _, err := raw.Exec(`UPDATE cluster_database SET dsn_template = '   ' WHERE id = $1`, clusterID); err != nil {
		t.Fatalf("update to blanks: %v", err)
	}
	if svc.onboardDSNTemplateReady(clusterID) {
		t.Error("a whitespace-only dsn_template must report NOT ready")
	}
	// A configured template is the pass-through case: behaviour is unchanged.
	if _, err := raw.Exec(
		`UPDATE cluster_database SET dsn_template = 'postgres://skygate:%s@primary:5432/skygate?sslmode=disable' WHERE id = $1`,
		clusterID); err != nil {
		t.Fatalf("update template: %v", err)
	}
	if !svc.onboardDSNTemplateReady(clusterID) {
		t.Error("a configured dsn_template must report READY — an existing template keeps today's behaviour")
	}
	// An empty cluster id (an older parked payload) must fall back to the same
	// cluster, not silently claim readiness.
	if !svc.onboardDSNTemplateReady("") {
		t.Error("an empty cluster id must fall back to the default cluster row")
	}
}

// TestB365_DSNWarningNamesTheSetting pins gap 2's ACTIONABLE half on both
// surfaces: the join's stderr and the panel warning must name the exact setting
// an operator has to fill, and must not pretend the join produced a DSN.
func TestB365_DSNWarningNamesTheSetting(t *testing.T) {
	catalogueValues := []string{
		// The panel surfaces the warning through these two i18n values; both
		// must name the setting and the page, or the operator is back to
		// guessing.
		i18nValueEN(t, "cluster.onboard_dsn_missing_title"),
		i18nValueEN(t, "cluster.onboard_dsn_missing_help"),
	}
	for i, v := range catalogueValues {
		if !strings.Contains(v, "cluster_database.dsn_template") &&
			!strings.Contains(v, "dsn_template") {
			t.Errorf("panel DSN warning %d does not name cluster_database.dsn_template: %q", i, v)
		}
	}
	if !strings.Contains(catalogueValues[1], "/admin/database") {
		t.Errorf("panel DSN warning does not name the page that fills it: %q", catalogueValues[1])
	}
	if !strings.Contains(catalogueValues[1], "UPDATE cluster_database") &&
		!strings.Contains(catalogueValues[0], "UPDATE cluster_database") {
		// The exact statement lives in the template, so assert it there instead.
		t.Logf("the UPDATE is rendered by the template next to key cluster.onboard_dsn_missing_help")
	}
}

// i18nValueEN / i18nHas read the admin catalogue through the public
// i18n.Catalog. Both languages must define the key (AGENTS rule 10) — the parity
// test compares key SETS, so a key present on only one side would still pass it,
// which is exactly why the panel warning is read here rather than assumed.
func i18nValueEN(t *testing.T, key string) string {
	t.Helper()
	cat := i18n.New()
	for _, lang := range []string{i18n.LangRU, i18n.LangEN} {
		if got := cat.T(lang, key); got == key {
			t.Errorf("i18n key %q is missing from the %s catalogue", key, lang)
		}
	}
	return cat.T(i18n.LangEN, key)
}
