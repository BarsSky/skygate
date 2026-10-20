package admin

// system_tests_diagnostics_b394.go — B394: a system test must tell the operator
// HOW BAD it is and WHAT TO DO, not only that it failed.
//
// WHY THIS FILE EXISTS
// --------------------
// /admin/system_tests used to answer one question per row: pass/fail/skip. When
// something failed, the page showed the test's raw output and nothing else — no
// severity (a missing journal mode and a black-holed exit node looked equally
// red) and no next step (the operator had to know the product to guess whether
// the answer was "restart the container" or "the database is read-only").
//
// The data lives in a TABLE rather than in the eighteen registry literals for
// one reason: the registry is ordered and every entry is a large closure, so
// editing each one to add two strings would bury the behaviour under diff noise
// and would be re-done for every future test. A lookup table is also what makes
// coverage CHECKABLE — the Go test walks AllTests() and fails when an entry has
// no severity or no remediation, which a "paste two fields next to each closure"
// approach can only enforce by eye.
//
// Category defaults are the floor: every new test inherits a sensible severity
// from its category even before anybody annotates it, so a hastily added test can
// never render as "info" just because the table does not know its name yet.
//
// Remediation holds an i18n KEY, not a sentence: rule 10 (RU+EN pairs) applies to
// everything the operator reads, and B325's ratchet refuses hardcoded strings in
// admin templates.

// TestSeverity ranks a failing test by what it means for the installation.
//
//   - critical: the product cannot do its job (no egress, a broken ACL, a
//     database that is not writable, a missing route approval). Act now.
//   - warning: something is degraded or drifting and will become critical if
//     nobody looks (disk filling, a stale backup, DERP latency, tag drift).
//   - info: an observation with no action implied (a journal mode, a mesh
//     summary, an integrations inventory).
type TestSeverity string

const (
	SeverityCritical TestSeverity = "critical"
	SeverityWarning  TestSeverity = "warning"
	SeverityInfo     TestSeverity = "info"
)

// SeverityRank orders the page: critical first, info last. Unknown severities
// sort with warnings, so a future value cannot hide a failure at the bottom.
func SeverityRank(s TestSeverity) int {
	switch s {
	case SeverityCritical:
		return 0
	case SeverityInfo:
		return 2
	default:
		return 1
	}
}

// testAnnotation is what the page needs to explain a failure.
type testAnnotation struct {
	Severity TestSeverity
	// Remediation is an i18n key whose value says what to do next.
	Remediation string
	// DocLink is the in-panel place with the long version ("" = none).
	DocLink string
}

// categoryDefaults is the floor for any test whose name is not annotated yet.
var categoryDefaults = map[string]testAnnotation{
	"db":           {Severity: SeverityCritical, Remediation: "st.fix.generic_db", DocLink: "/admin/database"},
	"headscale":    {Severity: SeverityCritical, Remediation: "st.fix.generic_headscale", DocLink: "/admin/headscale"},
	"network":      {Severity: SeverityWarning, Remediation: "st.fix.generic_network", DocLink: "/admin/derp"},
	"exit_rules":   {Severity: SeverityCritical, Remediation: "st.fix.generic_exit_rules", DocLink: "/admin/exit-rules"},
	"backup":       {Severity: SeverityWarning, Remediation: "st.fix.generic_backup", DocLink: "/admin/backup"},
	"integrations": {Severity: SeverityInfo, Remediation: "st.fix.generic_integrations", DocLink: "/admin/integrations"},
	"disk":         {Severity: SeverityWarning, Remediation: "st.fix.generic_disk", DocLink: "/admin/database"},
	"derp":         {Severity: SeverityWarning, Remediation: "st.fix.generic_derp", DocLink: "/admin/derp"},
	"build":        {Severity: SeverityWarning, Remediation: "st.fix.generic_build", DocLink: "/admin/update"},
	"mesh":         {Severity: SeverityInfo, Remediation: "st.fix.generic_mesh", DocLink: "/admin/meshes"},
	"prefix_owner": {Severity: SeverityCritical, Remediation: "st.fix.generic_prefix_owner", DocLink: "/admin/exit-nodes"},
}

// testAnnotations is the per-test override. A name listed here wins over its
// category default; anything absent keeps the default (never "unannotated").
var testAnnotations = map[string]testAnnotation{
	// The eight that mean "the tailnet is not doing its job".
	"db.integrity_check":             {Severity: SeverityCritical, Remediation: "st.fix.db_integrity", DocLink: "/admin/database"},
	"db.writable_primary":            {Severity: SeverityCritical, Remediation: "st.fix.db_writable", DocLink: "/admin/database"},
	"db.schema_tables":               {Severity: SeverityCritical, Remediation: "st.fix.db_schema", DocLink: "/admin/database"},
	"net.headscale_reachable":        {Severity: SeverityCritical, Remediation: "st.fix.hs_unreachable", DocLink: "/admin/headscale"},
	"headscale.policy_current":       {Severity: SeverityCritical, Remediation: "st.fix.acl_drift", DocLink: "/admin/headscale/acl"},
	"headscale.tags_drift":           {Severity: SeverityCritical, Remediation: "st.fix.tag_drift", DocLink: "/admin/devices"},
	"prefix_owner.consistency":       {Severity: SeverityCritical, Remediation: "st.fix.prefix_owner", DocLink: "/admin/exit-nodes"},
	"headscale.exit_routes_approved": {Severity: SeverityCritical, Remediation: "st.fix.routes_unapproved", DocLink: "/admin/exit-nodes"},

	// Degraded-but-working.
	"db.journal_mode":                 {Severity: SeverityInfo, Remediation: "st.fix.journal_mode", DocLink: "/admin/database"},
	"db.duplicate_devices":            {Severity: SeverityWarning, Remediation: "st.fix.duplicates", DocLink: "/admin/devices"},
	"db.rules_sanity":                 {Severity: SeverityWarning, Remediation: "st.fix.rules_sanity", DocLink: "/admin/exit-rules"},
	"net.tailscale_self":              {Severity: SeverityWarning, Remediation: "st.fix.tailscale_self", DocLink: "/admin/tailscale"},
	"network.dns_resolve":             {Severity: SeverityWarning, Remediation: "st.fix.dns", DocLink: "/admin/system_tests"},
	"headscale.peers_visible":         {Severity: SeverityWarning, Remediation: "st.fix.peers", DocLink: "/admin/headscale"},
	"headscale.exit_nodes_online":     {Severity: SeverityCritical, Remediation: "st.fix.exit_nodes_offline", DocLink: "/admin/exit-nodes"},
	"backup.recent":                   {Severity: SeverityWarning, Remediation: "st.fix.backup_recent", DocLink: "/admin/backup"},
	"backup.verify":                   {Severity: SeverityWarning, Remediation: "st.fix.backup_verify", DocLink: "/admin/backup"},
	"disk.free_space":                 {Severity: SeverityWarning, Remediation: "st.fix.disk", DocLink: "/admin/database"},
	"derp.reachable":                  {Severity: SeverityWarning, Remediation: "st.fix.derp", DocLink: "/admin/derp"},
	"build.ready":                     {Severity: SeverityWarning, Remediation: "st.fix.build", DocLink: "/admin/update"},
	"exit_rules.preferred_mismatch":   {Severity: SeverityWarning, Remediation: "st.fix.pref_mismatch", DocLink: "/admin/exit-rules"},
	"exit_nodes.tcp_connect_speed":    {Severity: SeverityWarning, Remediation: "st.fix.exit_speed", DocLink: "/admin/exit-nodes"},
	"exit_nodes.availability_summary": {Severity: SeverityInfo, Remediation: "st.fix.exit_summary", DocLink: "/admin/exit-nodes"},

	// Destructive test paths: they only run when the operator asks.
	"headscale.device_renew":  {Severity: SeverityInfo, Remediation: "st.fix.device_renew", DocLink: "/admin/devices"},
	"headscale.device_delete": {Severity: SeverityInfo, Remediation: "st.fix.device_delete", DocLink: "/admin/devices"},

	// The rest: informational.
	"headscale.acl_admin_present":     {Severity: SeverityWarning, Remediation: "st.fix.acl_admin", DocLink: "/admin/headscale/acl"},
	"integrations.configured":         {Severity: SeverityInfo, Remediation: "st.fix.integrations", DocLink: "/admin/integrations"},
	"mesh.active_meshes":              {Severity: SeverityInfo, Remediation: "st.fix.mesh", DocLink: "/admin/meshes"},
	"exit_rules.all_in_headscale_acl": {Severity: SeverityWarning, Remediation: "st.fix.rules_not_in_acl", DocLink: "/admin/exit-rules"},
	"exit_rules.domain_prefix":        {Severity: SeverityWarning, Remediation: "st.fix.domain_prefix", DocLink: "/admin/exit-rules"},
	// B398: a pin is not a fault, but it is a CONSTRAINT the operator must know about —
	// the live report ("emilia → ничего не грузится") is what this key exists to prevent.
	"exit_rules.exit_node_pin": {Severity: SeverityWarning, Remediation: "st.fix.exit_node_pin", DocLink: "/admin/exit-nodes"},
	// B400: an unattributed device is not a crash — it is invisible to /my/* pages,
	// «all my devices» rules and the preference reconciler, which is why it is a
	// warning the operator can act on rather than a red critical.
	"ownership.attribution": {Severity: SeverityWarning, Remediation: "st.fix.ownership_attribution", DocLink: "/admin/devices"},
	// B401: a row that is not a relay is not an outage — it is junk in the list that
	// made the operator ask what it was. Warning, with the delete/disable instruction.
	"derp.relay_rows": {Severity: SeverityWarning, Remediation: "st.fix.derp_relay_rows", DocLink: "/admin/derp/relays"},
}

// annotateTest fills Severity/Remediation/DocLink from the tables. Values already
// set on the definition win, so a test that really needs a bespoke sentence can
// carry its own without editing the table.
func annotateTest(def *SystemTestDef) {
	if def == nil {
		return
	}
	a, ok := testAnnotations[def.Name]
	if !ok {
		a, ok = categoryDefaults[def.Category]
	}
	if !ok {
		a = testAnnotation{Severity: SeverityWarning, Remediation: "st.fix.generic"}
	}
	if def.Severity == "" {
		def.Severity = a.Severity
	}
	if def.Remediation == "" {
		def.Remediation = a.Remediation
	}
	if def.DocLink == "" {
		def.DocLink = a.DocLink
	}
}
