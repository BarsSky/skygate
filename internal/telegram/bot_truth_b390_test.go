// B390 (2026-10-25, TD-33) — THE BOT MUST NOT STATE A VALUE IT DOES NOT HAVE.
//
// Every test here runs against a REAL migrated SQLite database (the migration chain,
// not a hand-written table) and asserts what the operator would READ. They are written
// to fail when a value becomes fabricated again — not to pin that some call exists:
//
//	H1  /exit_nodes + /myexitnodes read `devices`, a table with no writer anywhere in
//	    the repository, so every relay was "offline". The fixtures below make the two
//	    sources DISAGREE on purpose: if a reply ever reads `devices` again, the answer
//	    flips and the test fails.
//	H2  /my_status printed "devices: 0" when the read FAILED. The fixture drops the
//	    table the count comes from, so a zero can only be a guess.
//	H3  /version printed a hardcoded "v0.32" against a v0.81 schema. The fixture
//	    records a migration version the constant could never have been.
//	H6  /delrule + /clearrules counted loop iterations, not deletions. The fixture
//	    asks to delete rules that are not there.
package telegram

import (
	"database/sql"
	"strings"
	"testing"

	"skygate/internal/db"
	"skygate/internal/headscale"
	"skygate/internal/i18n"
)

// b390DB opens a throwaway SQLite database migrated through the real chain.
func b390DB(t *testing.T) *sql.DB {
	t.Helper()
	_, d, err := db.OpenWithDialect("sqlite:" + t.TempDir() + "/b390.db")
	if err != nil {
		t.Skipf("sqlite dialect unavailable: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := db.MigrateSQLite(d); err != nil {
		t.Fatalf("MigrateSQLite: %v", err)
	}
	// The catalogue must be live, otherwise i18n.T returns the KEY and every
	// substring assertion below would pass or fail for the wrong reason.
	if i18n.GlobalCatalog == nil {
		i18n.SetGlobal(i18n.New())
	}
	return d
}

// b390SeedUser seeds one portal user and one owned device.
func b390SeedUser(t *testing.T, d *sql.DB, username string) (userID int64) {
	t.Helper()
	res, err := d.Exec(`INSERT INTO portal_users (username, password_hash, is_admin, headscale_user_id)
	                    VALUES ($1, 'x', 1, 1)`, username)
	if err != nil {
		t.Fatalf("seed portal_users: %v", err)
	}
	userID, _ = res.LastInsertId()
	if _, err := d.Exec(`INSERT INTO node_owner_map (node_id, username, hostname, tag)
	                     VALUES ('7', $1, 'workpc', 'tag:dev-1')`, username); err != nil {
		t.Fatalf("seed node_owner_map: %v", err)
	}
	return userID
}

// b390Env builds the BotEnv the replies see.
func b390Env(d *sql.DB, username string, userID int64, lang string) BotEnv {
	chat := int64(42)
	return BotEnv{
		DB:           d,
		Lang:         lang,
		ChatID:       chat,
		PortalUserID: userID,
		Username:     username,
		IsAdmin:      true,
	}
}

// --- H1: the status of a relay comes from the monitor, not from `devices` ---

func TestB390_ExitNodesReadsTheHealthSnapshotNotTheDevicesTable(t *testing.T) {
	d := b390DB(t)
	userID := b390SeedUser(t, d, "skyadmin")

	// A relay the portal knows about (node_owner_map is what /exit_nodes lists).
	if _, err := d.Exec(`INSERT INTO node_owner_map (node_id, username, hostname, tag)
	                     VALUES ('1', 'skyadmin', 'exit-node-vps', 'tag:exit-node')`); err != nil {
		t.Fatalf("seed exit node owner: %v", err)
	}
	// The `devices` table — the source the reply used to read — says ONLINE.
	// It has no writer in production, which is why every relay was "offline"; the
	// fixture writes it explicitly so a regression cannot hide behind an empty table.
	if _, err := d.Exec(`INSERT INTO devices (user_id, node_id, hostname, last_seen, online)
	                     VALUES ($1, '1', 'exit-node-vps', 1700000000, 1)`, userID); err != nil {
		t.Fatalf("seed devices: %v", err)
	}
	// The monitor says OFFLINE. The reply must believe the monitor.
	if err := db.UpsertExitNodeHealth(d, db.ExitNodeHealth{
		NodeID:   "1",
		Hostname: "exit-node-vps",
		Online:   false,
		State:    "offline",
		Healthy:  false,
	}); err != nil {
		t.Fatalf("seed exit_node_health: %v", err)
	}

	env := b390Env(d, "skyadmin", userID, "en")
	admin := exitNodesReply(env)
	user := myExitNodesReply(env)

	offline := i18n.T("en", "bot.exit_nodes.status_offline")
	online := i18n.T("en", "bot.exit_nodes.status_online")
	for name, reply := range map[string]string{"/exit_nodes": admin, "/myexitnodes": user} {
		if strings.Contains(reply, online) {
			t.Errorf("B390 H1: %s says %q while the monitor says %q — it is reading the writerless "+
				"`devices` table again\nreply: %s", name, online, offline, reply)
		}
	}
	// /myexitnodes lists ENABLED exit_servers rows, so give it one and check the word.
	if _, err := d.Exec(`INSERT INTO exit_servers (node_id, hostname, enabled) VALUES ('1', 'exit-node-vps', 1)`); err != nil {
		t.Fatalf("seed exit_servers: %v", err)
	}
	if got := myExitNodesReply(env); !strings.Contains(got, offline) {
		t.Errorf("B390 H1: /myexitnodes does not report the monitor's %q state\nreply: %s", offline, got)
	}
}

func TestB390_UnmeasuredRelaySaysNoDataInsteadOfOffline(t *testing.T) {
	d := b390DB(t)
	userID := b390SeedUser(t, d, "skyadmin")
	if _, err := d.Exec(`INSERT INTO exit_servers (node_id, hostname, enabled) VALUES ('1', 'exit-node-vps', 1)`); err != nil {
		t.Fatalf("seed exit_servers: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO node_owner_map (node_id, username, hostname, tag)
	                     VALUES ('1', 'skyadmin', 'exit-node-vps', 'tag:exit-node')`); err != nil {
		t.Fatalf("seed exit node owner: %v", err)
	}
	// No exit_node_health row at all: the monitor has not sampled this node yet.
	env := b390Env(d, "skyadmin", userID, "en")
	noData := i18n.T("en", "bot.exit_nodes.status_no_data")
	offline := i18n.T("en", "bot.exit_nodes.status_offline")

	for name, reply := range map[string]string{
		"/exit_nodes":   exitNodesReply(env),
		"/myexitnodes":  myExitNodesReply(env),
		"/exit_nodes#2": exitNodesReply(env), // idempotence of the wording
	} {
		if strings.Contains(reply, offline) {
			t.Errorf("B390 H1: %s calls an unmeasured relay %q — an absent value is not a measured zero\nreply: %s",
				name, offline, reply)
		}
		if !strings.Contains(reply, noData) {
			t.Errorf("B390 H1: %s does not say %q for a relay the monitor has not sampled\nreply: %s",
				name, noData, reply)
		}
	}
}

// --- H2: a failed device count is not zero ---

func TestB390_MyStatusDoesNotInventZeroDevices(t *testing.T) {
	d := b390DB(t)
	userID := b390SeedUser(t, d, "skyadmin")
	// Drop the table the device count reads. Any number in the reply now has to be
	// a guess; the old code printed a confident "0".
	if _, err := d.Exec(`DROP TABLE node_owner_map`); err != nil {
		t.Fatalf("drop node_owner_map: %v", err)
	}
	env := b390Env(d, "skyadmin", userID, "en")
	unavailable := i18n.T("en", "bot.my_status.value_unavailable")
	reply := myStatusReply(env)

	// The label is followed by the value in a <code> block; a zero there is the bug.
	if strings.Contains(reply, "<code>0</code>") {
		t.Errorf("B390 H2: /my_status reports 0 devices after a FAILED read — that number was never measured\nreply: %s", reply)
	}
	if !strings.Contains(reply, unavailable) {
		t.Errorf("B390 H2: /my_status does not mark the device count as %q\nreply: %s", unavailable, reply)
	}
	if !strings.Contains(reply, i18n.T("en", "bot.my_status.devices_unavailable")[:10]) {
		t.Errorf("B390 H2: /my_status hides WHY the device count is missing\nreply: %s", reply)
	}
}

// --- H3: the schema level comes from the database ---

func TestB390_VersionReadsTheSchemaLevelFromTheDatabase(t *testing.T) {
	d := b390DB(t)
	userID := b390SeedUser(t, d, "skyadmin")
	want, err := db.LatestAppliedMigrationVersion(d)
	if err != nil {
		t.Fatalf("LatestAppliedMigrationVersion: %v", err)
	}
	if want == 0 {
		t.Fatal("B390 H3: the migrated test database recorded no applied migration — the test premise is wrong")
	}
	env := b390Env(d, "skyadmin", userID, "en")
	reply := versionReply(env)
	if !strings.Contains(reply, schemaLabelFor(want)) {
		t.Errorf("B390 H3: /version does not report the database's own level %q\nreply: %s", schemaLabelFor(want), reply)
	}
	// The constant that used to be printed must be gone for good: a v0.32 answer
	// on a v0.NN schema is exactly the lie this test exists to prevent.
	if strings.Contains(reply, "v0.32") {
		t.Errorf("B390 H3: /version still prints the retired hardcoded constant v0.32\nreply: %s", reply)
	}
}

// TestB390_VersionNeverInventsALevelWhenTheReadFails — a database whose tracking
// table is missing must produce the "unavailable" wording, not a number.
func TestB390_VersionNeverInventsALevelWhenTheReadFails(t *testing.T) {
	d := b390DB(t)
	userID := b390SeedUser(t, d, "skyadmin")
	if _, err := d.Exec(`DROP TABLE applied_migrations`); err != nil {
		t.Fatalf("drop applied_migrations: %v", err)
	}
	env := b390Env(d, "skyadmin", userID, "en")
	reply := versionReply(env)
	if !strings.Contains(reply, "n/a") {
		t.Errorf("B390 H3: /version does not say the level is unavailable\nreply: %s", reply)
	}
	if strings.Contains(reply, "v0.32") {
		t.Errorf("B390 H3: /version fell back to the retired constant\nreply: %s", reply)
	}
}

// --- H6: the delete replies count deletions ---

func TestB390_DeleteRuleCountsWhatItDeleted(t *testing.T) {
	d := b390DB(t)
	userID := b390SeedUser(t, d, "skyadmin")
	env := b390Env(d, "skyadmin", userID, "en")
	// No rules exist: asking for id 4242 must not be reported as a deletion. Two
	// honest wordings are acceptable (it is rejected while parsing, before any
	// DELETE runs), so the assertion is on the CLAIM, not on the phrasing: the
	// retired behaviour was «✓ Удалено 1 правил…».
	reply := deleteRuleReply(env, "4242")
	if strings.Contains(reply, i18n.T("en", "bot.delrule.applied_ok")[:9]) {
		t.Errorf("B390 H6: /delrule claims a deletion for a rule that does not exist\nreply: %s", reply)
	}
	if !strings.Contains(reply, "4242") {
		t.Errorf("B390 H6: /delrule does not name the id it did nothing with\nreply: %s", reply)
	}
	var auditCount int
	if err := d.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'rule_deleted'`).Scan(&auditCount); err != nil {
		t.Fatalf("count audit: %v", err)
	}
	// The audit row is allowed (it records the attempt) but must not claim a delete.
	var detail string
	if err := d.QueryRow(`SELECT COALESCE(detail, '') FROM audit_log WHERE action = 'rule_deleted' LIMIT 1`).Scan(&detail); err != nil && err != sql.ErrNoRows {
		t.Fatalf("read audit detail: %v", err)
	}
	if strings.Contains(detail, "deleted 1") {
		t.Errorf("B390 H6: the audit row claims a deletion that never happened: %q", detail)
	}
}

// TestB390_ClearRulesCountsWhatItDeleted — the mint phase counts what EXISTS, the
// confirm phase must count what it REMOVED.
func TestB390_ClearRulesCountsWhatItDeleted(t *testing.T) {
	d := b390DB(t)
	userID := b390SeedUser(t, d, "skyadmin")
	// Two real rules for this user.
	for _, v := range []string{"1.2.3.4", "5.6.7.8"} {
		if _, err := d.Exec(`INSERT INTO device_rules (user_id, device_id, exit_node_id,
		    target_type, target_value) VALUES ($1, '7', '1', 'ip', $2)`,
			userID, v); err != nil {
			t.Fatalf("seed device_rules: %v", err)
		}
	}
	env := b390Env(d, "skyadmin", userID, "en")
	_ = env // the clear path is exercised through deleteRuleJobs, which it shares with /delrule
	out := deleteRuleJobs(d, userID, []ruleDeleteJob{
		{id: 1, targetType: "ip"},
		{id: 2, targetType: "ip"},
		{id: 999, targetType: "ip"}, // does not exist
	})
	if len(out.deleted) != 2 {
		t.Errorf("B390 H6: deleteRuleJobs counted %d deletions, want 2 (the third id matches nothing): %+v",
			len(out.deleted), out)
	}
	if len(out.failed) != 1 || !strings.Contains(out.failed[0], "#999") {
		t.Errorf("B390 H6: the id that matched nothing is not reported as failed: %+v", out.failed)
	}
	// Run it again: everything is gone now, so nothing may be counted.
	again := deleteRuleJobs(d, userID, []ruleDeleteJob{{id: 1, targetType: "ip"}})
	if len(again.deleted) != 0 {
		t.Errorf("B390 H6: a second delete of the same rule counted %d deletions", len(again.deleted))
	}
}

// schemaLabelFor mirrors schemaLevelLabel's formatting without reaching into the
// package's private formatting choice: it is the documented "v0.NN" label.
func schemaLabelFor(version int) string {
	return "v0." + itoa(version)
}

// itoa is a tiny strconv.Itoa so this file does not import strconv for one call.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// --- H4: a failed ACL re-apply must not be reported as a success ---

func TestB390_SubnetShareReportsAFailedACL(t *testing.T) {
	d := b390DB(t)
	callerID := b390SeedUser(t, d, "skyadmin")
	if _, err := d.Exec(`INSERT INTO portal_users (username, password_hash, is_admin, headscale_user_id)
	                     VALUES ('alice', 'x', 0, 2)`); err != nil {
		t.Fatalf("seed grantee: %v", err)
	}
	// The grantor needs a personal subnet for subnet.Grant to succeed.
	if _, err := d.Exec(`INSERT INTO user_subnets (user_id, cidr, subnet_bits, status, created_at, updated_at)
	                     VALUES ($1, '100.100.7.0/24', 24, 'active', 0, 0)`, callerID); err != nil {
		t.Fatalf("seed user_subnets: %v", err)
	}
	// HSForPortalUser returns nil on purpose: that is the read-only / corrupt
	// per-plane key state, in which acl.ApplyACLPipelineForPlane skips the push and
	// reports Applied=false. The share ROW is written; the POLICY is not.
	env := b390Env(d, "skyadmin", callerID, "en")
	env.HSForPortalUser = func(int64) *headscale.Client { return nil }
	env.PortalPlaneURL = func(int64) string { return "" }
	reply := mySubnetShareReply(env, []string{"alice"})

	failKey := "bot.mysubnet.share_ok_acl_failed"
	okKey := "bot.mysubnet.share_ok"
	if strings.Contains(reply, i18n.Tf("en", okKey, "skyadmin", "alice")) {
		t.Errorf("B390 H4: /mysubnet share reports the plain success text while the ACL was NOT applied\nreply: %s", reply)
	}
	if !strings.Contains(reply, strings.SplitN(i18n.T("en", failKey), "%", 2)[0]) {
		t.Errorf("B390 H4: /mysubnet share does not report the ACL failure (want the %q wording)\nreply: %s", failKey, reply)
	}
	// And the row really is in the database: the reply's claim must stay true.
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM user_subnet_shares`).Scan(&n); err != nil {
		t.Fatalf("count subnet_shares: %v", err)
	}
	if n != 1 {
		t.Errorf("B390 H4: the share row was not written (count=%d) — the reply's wording would be wrong", n)
	}
}

// --- /help <cmd>: the reference comes from the catalogue, in the caller's language ---

func TestB390_HelpDetailUsesTheCatalogueInTheCallersLanguage(t *testing.T) {
	if i18n.GlobalCatalog == nil {
		i18n.SetGlobal(i18n.New())
	}
	ru := helpDetailReply("/delrule", BotEnv{Lang: "ru"})
	en := helpDetailReply("/delrule", BotEnv{Lang: "en"})

	if ru == en {
		t.Errorf("B390 (help): RU and EN /help delrule are identical — the reply is not coming from the "+
			"catalogue\nru: %s", ru)
	}
	wantRU := i18n.T("ru", "bot.help_detail.delrule")
	if ru != wantRU {
		t.Errorf("B390 (help): /help delrule in a Russian chat is not the catalogue's Russian text\n"+
			"got:  %s\nwant: %s", ru, wantRU)
	}
	if !strings.HasPrefix(ru, "/delrule") {
		t.Errorf("B390 (help): the Russian reference does not describe /delrule: %s", ru)
	}
	// A command nobody wrote a reference for must say so, in the caller's language —
	// never fall through to an English default.
	missing := helpDetailReply("/nosuchcmd", BotEnv{Lang: "ru"})
	if strings.Contains(missing, "No detailed help") {
		t.Errorf("B390 (help): an unknown command still answers in English: %s", missing)
	}
	if !strings.Contains(missing, "nosuchcmd") {
		t.Errorf("B390 (help): the unknown-command reply does not name the command: %s", missing)
	}
}

// TestB390_EveryHelpDetailKeyExistsInBothLanguages — the reason the switch could rot:
// a hardcoded table is invisible to TestCatalogsParity. With the lookup, a command
// whose key exists in one language only would silently answer in RU for EN callers,
// so pin the pairs the bot can actually be asked for.
func TestB390_EveryHelpDetailKeyExistsInBothLanguages(t *testing.T) {
	if i18n.GlobalCatalog == nil {
		i18n.SetGlobal(i18n.New())
	}
	for _, cmd := range []string{
		"ack", "add_device", "add_rule", "audit", "bind", "clearrules", "delete_rule",
		"delrule", "exit_nodes", "exit_nodes_health", "help", "lang", "my_nodes",
		"my_quota", "my_rules", "my_status", "myexitnodes", "nodes", "quota",
		"restart", "rules", "status", "sync_nodes", "unbind", "version",
	} {
		key := "bot.help_detail." + cmd
		for _, lang := range []string{"ru", "en"} {
			if got := i18n.T(lang, key); got == key {
				t.Errorf("B390 (help): %s is missing from the %s catalogue — a caller of that "+
					"language gets the fallback instead of a reference", key, lang)
			}
		}
	}
}
