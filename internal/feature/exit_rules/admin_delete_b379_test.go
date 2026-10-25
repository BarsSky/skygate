// admin_delete_b379_test.go — B379 (closing TD-29), the handler-level half.
//
// THE DEFECT: both delete controls on /admin/exit-rules posted to
// `/admin/exit-rules/delete` and no mux.Handle registered that path, so an admin
// delete answered 404 for EVERY rule, of every user. A contract that only greps the
// route table would prove the path is registered and nothing about what it does — and
// what it does is the whole risk here, because the admin path is the only one that
// deletes ANOTHER user's rows.
//
// What is pinned below, against a real migrated SQLite database:
//   - a plain rule of ANOTHER user is deleted (the point of the handler);
//   - a domain rule takes its derived /32 children with it, even though those helpers
//     are scoped by user_id — i.e. the handler resolved the owner correctly;
//   - an `all_devices` rule takes the whole fan-out group (B277.4), not just the row;
//   - a rule that does not exist is reported instead of silently ignored;
//   - a NON-admin is refused with 403 and nothing is deleted;
//   - every successful delete leaves an audit row naming the OWNER.
package exit_rules

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"skygate/internal/auth"
	skygatedb "skygate/internal/db"
)

// b379Backend is the test Backend: a fixed Claims, rendering methods that are never
// reached (the handler redirects), and an Audit that writes the row into the DATABASE —
// so the assertion below reads the same table /admin/audit reads, instead of trusting a
// recorded call.
type b379Backend struct {
	claims *auth.Claims
	db     *sql.DB
}

func (b *b379Backend) Render(w http.ResponseWriter, r *http.Request, name string, data any) {}
func (b *b379Backend) RenderWithLayout(w http.ResponseWriter, r *http.Request, name string, c *auth.Claims, data map[string]any) {
}
func (b *b379Backend) CurrentUser(r *http.Request) *auth.Claims { return b.claims }
func (b *b379Backend) Audit(userID int64, username, action, detail string) {
	_ = skygatedb.AppendAuditLog(b.db, userID, username, action, detail)
}

func newB379DB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := skygatedb.OpenFastSQLiteForTest(t.TempDir() + "/b379.db")
	if err != nil {
		t.Skipf("sqlite dialect unavailable in this build: %v", err)
	}
	if err := skygatedb.MigrateSQLite(d); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// b379SeedRule inserts one device_rules row for `owner` and returns its id. The column
// list is the one the live schema requires (B125's natural key, V074's all_devices).
func b379SeedRule(t *testing.T, d *sql.DB, owner int64, ownerName string, deviceID int,
	exitNode, targetType, targetValue, parentDomain string, allDevices bool) int {
	t.Helper()
	// device_rules.user_id carries a real FK, so the owner must exist first.
	b379SeedUser(t, d, owner, ownerName)
	ad := 0
	if allDevices {
		ad = 1
	}
	res, err := d.Exec(`INSERT INTO device_rules
		(user_id, user_name, device_id, device_ip, exit_node_id, target_type, target_value,
		 action, enabled, parent_domain, all_devices, created_at)
		VALUES ($1,$2,$3,'100.64.0.20',$4,$5,$6,'accept',1,$7,$8,0)`,
		owner, ownerName, deviceID, exitNode, targetType, targetValue, parentDomain, ad)
	if err != nil {
		t.Fatalf("seed rule (%s %s): %v", targetType, targetValue, err)
	}
	id, _ := res.LastInsertId()
	return int(id)
}

func b379Count(t *testing.T, d *sql.DB, q string, args ...interface{}) int {
	t.Helper()
	var n int
	if err := d.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", q, err)
	}
	return n
}

// b379SeedUser is idempotent: b379SeedRule calls it for every rule, and the fixture is
// about the RULES, not about the user rows.
func b379SeedUser(t *testing.T, d *sql.DB, id int64, username string) {
	t.Helper()
	if _, err := d.Exec(`INSERT INTO portal_users (id, username, password_hash, is_admin)
	                     VALUES ($1, $2, 'x', 0) ON CONFLICT (id) DO NOTHING`, id, username); err != nil {
		t.Fatalf("seed portal user %d: %v", id, err)
	}
}

// b379SeedRuleWithLabel seeds one rule that carries a group label (v0.79 / B380). It
// lives beside b379SeedRule rather than inside it so the B379 fixtures keep their exact
// shape (no label) and a B380 test says in one line what it needs.
func b379SeedRuleWithLabel(t *testing.T, d *sql.DB, owner int64, ownerName string, deviceID int, label string) {
	t.Helper()
	b379SeedUser(t, d, owner, ownerName)
	if _, err := d.Exec(`INSERT INTO device_rules
		(user_id, user_name, device_id, device_ip, exit_node_id, target_type, target_value,
		 action, enabled, parent_domain, all_devices, group_label, created_at)
		VALUES ($1,$2,$3,'100.64.0.20','emilia','domain',$4,'accept',1,'',0,$5,0)`,
		owner, ownerName, deviceID, "b380-"+label+".invalid", label); err != nil {
		t.Fatalf("seed labelled rule for user %d: %v", owner, err)
	}
}

// b379Post drives the handler with the given id and claims.
func b379Post(s *Service, id int, cookie string) *httptest.ResponseRecorder {
	form := url.Values{"id": {strconv.Itoa(id)}}
	req := httptest.NewRequest(http.MethodPost, "/admin/exit-rules/delete", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	rec := httptest.NewRecorder()
	s.PostAdminExitRuleDelete(rec, req)
	return rec
}

// TestB379_AdminDeletesAnotherUsersRule — the core of TD-29: the admin page could not
// delete ANY rule. This one belongs to uid=7, the admin is uid=1.
func TestB379_AdminDeletesAnotherUsersRule(t *testing.T) {
	d := newB379DB(t)
	id := b379SeedRule(t, d, 7, "michail", 42, "emilia", "subnet", "192.0.2.0/24", "", false)
	s := &Service{
		Backend: &b379Backend{claims: &auth.Claims{UserID: 1, Username: "skyadmin", IsAdmin: true}, db: d},
		DB:      skygatedb.FixedDBSource{DB: d},
	}

	rec := b379Post(s, id, "")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("admin delete answered %d, want 303 (the 404 this closes): %s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "deleted=1") {
		t.Errorf("redirect does not report the delete: %q", loc)
	}
	if n := b379Count(t, d, `SELECT COUNT(*) FROM device_rules WHERE id = $1`, id); n != 0 {
		t.Errorf("rule %d is still there — the admin delete did nothing", id)
	}
	if n := b379Count(t, d, `SELECT COUNT(*) FROM audit_log WHERE action = 'admin_delete_exit_rule_for_user'`); n != 1 {
		t.Errorf("audit rows for the admin delete = %d, want 1", n)
	}
	// The audit detail must name the OWNER, not the admin — that is the difference
	// between "someone deleted a rule" and "the admin deleted michail's rule".
	var detail string
	if err := d.QueryRow(`SELECT detail FROM audit_log WHERE action = 'admin_delete_exit_rule_for_user'`).Scan(&detail); err != nil {
		t.Fatalf("read audit detail: %v", err)
	}
	if !strings.Contains(detail, "owner=michail") || !strings.Contains(detail, "uid=7") {
		t.Errorf("audit detail does not name the owner: %q", detail)
	}
}

// TestB379_DomainDeleteTakesTheDerivedSubnets — the cascade that made a one-line
// DeleteRule(id) wrong: the derived /32 rows are what the ACL pins, and every one of
// them is scoped by user_id, so an admin delete that did not resolve the owner would
// leave them behind and the auto-updater would re-add the domain on its next tick.
func TestB379_DomainDeleteTakesTheDerivedSubnets(t *testing.T) {
	d := newB379DB(t)
	// A DOMAIN row carries parent_domain = the domain itself (B123's convention, which
	// `GetRuleTargetTypeAndParent` + `qDeleteRulesByIDOrParentDomain` rely on), and its
	// derived /32 children carry the same value. The live insert path writes exactly
	// this shape — see bulk_add.go's "target_type=domain, parent_domain=the domain".
	domainID := b379SeedRule(t, d, 7, "michail", 42, "emilia", "domain", "example.com", "example.com", false)
	b379SeedRule(t, d, 7, "michail", 42, "emilia", "subnet", "192.0.2.10/32", "example.com", false)
	b379SeedRule(t, d, 7, "michail", 42, "emilia", "subnet", "192.0.2.11/32", "example.com", false)
	// A DECOY: a derived row for the same domain but ANOTHER owner. The cascade is
	// owner-scoped and must not touch it (the cross-user risk the user_id filters exist
	// for). seeder writes it for uid=8.
	b379SeedRule(t, d, 8, "daniil", 43, "emilia", "subnet", "192.0.2.12/32", "example.com", false)

	s := &Service{
		Backend: &b379Backend{claims: &auth.Claims{UserID: 1, Username: "skyadmin", IsAdmin: true}, db: d},
		DB:      skygatedb.FixedDBSource{DB: d},
	}
	rec := b379Post(s, domainID, "")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("admin domain delete answered %d, want 303", rec.Code)
	}
	if n := b379Count(t, d, `SELECT COUNT(*) FROM device_rules WHERE user_id = 7`); n != 0 {
		t.Errorf("owner 7 still has %d rule(s) after deleting the domain — the derived /32 cascade did not run", n)
	}
	if n := b379Count(t, d, `SELECT COUNT(*) FROM device_rules WHERE user_id = 8`); n != 1 {
		t.Errorf("the cascade crossed users: owner 8 lost rows (left %d, want 1)", n)
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "deleted=3") {
		t.Errorf("redirect should report 3 deleted rows, got %q", loc)
	}
}

// TestB379_AllDevicesDeleteTakesTheWholeFanOut — B277.4's rule: one logical
// «все устройства» rule is one row PER DEVICE, so deleting the clicked row alone leaves
// the ghosts that keep the destination routed.
func TestB379_AllDevicesDeleteTakesTheWholeFanOut(t *testing.T) {
	d := newB379DB(t)
	first := b379SeedRule(t, d, 7, "michail", 42, "emilia", "subnet", "198.51.100.0/24", "", true)
	b379SeedRule(t, d, 7, "michail", 43, "emilia", "subnet", "198.51.100.0/24", "", true)
	b379SeedRule(t, d, 7, "michail", 44, "emilia", "subnet", "198.51.100.0/24", "", true)

	s := &Service{
		Backend: &b379Backend{claims: &auth.Claims{UserID: 1, Username: "skyadmin", IsAdmin: true}, db: d},
		DB:      skygatedb.FixedDBSource{DB: d},
	}
	rec := b379Post(s, first, "")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("admin all-devices delete answered %d, want 303", rec.Code)
	}
	if n := b379Count(t, d, `SELECT COUNT(*) FROM device_rules`); n != 0 {
		t.Errorf("%d row(s) of the fan-out group survived (want 0)", n)
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "deleted=3") || !strings.Contains(loc, "deleted_fanout=2") {
		t.Errorf("redirect should name the fan-out cascade, got %q", loc)
	}
}

// TestB379_LegacyDomainRowWithoutParentDeletesOnlyItself pins the BOUNDARY of the parity
// with the owner's own page, so nobody has to guess it later: a domain row whose
// `parent_domain` is empty (a shape the live insert path does not produce, but a legacy
// or hand-edited row can) deletes as ONE row in the admin path, exactly as it does on
// /my/exit-rules — both paths share the same predicate
// (`target_type='domain' && parent_domain != ”`). Widening the cascade here alone would
// make the two pages disagree about what the same click removes.
func TestB379_LegacyDomainRowWithoutParentDeletesOnlyItself(t *testing.T) {
	d := newB379DB(t)
	legacy := b379SeedRule(t, d, 7, "michail", 42, "emilia", "domain", "example.com", "", false)
	b379SeedRule(t, d, 7, "michail", 42, "emilia", "subnet", "192.0.2.10/32", "example.com", false)

	s := &Service{
		Backend: &b379Backend{claims: &auth.Claims{UserID: 1, Username: "skyadmin", IsAdmin: true}, db: d},
		DB:      skygatedb.FixedDBSource{DB: d},
	}
	rec := b379Post(s, legacy, "")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("admin delete of a legacy domain row answered %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "deleted=1") {
		t.Errorf("a legacy domain row should report exactly 1 deleted row, got %q", loc)
	}
	if n := b379Count(t, d, `SELECT COUNT(*) FROM device_rules WHERE id = $1`, legacy); n != 0 {
		t.Errorf("the legacy domain row itself was not deleted")
	}
	if n := b379Count(t, d, `SELECT COUNT(*) FROM device_rules WHERE target_type = 'subnet'`); n != 1 {
		t.Errorf("the cascade fired for a row that carries no parent_domain (left %d derived rows, want 1)", n)
	}
}

// TestB379_MissingRuleIsReportedNotIgnored — the /my path skips an unknown id (right for
// a checkbox list); a single explicit admin click must say so.
func TestB379_MissingRuleIsReportedNotIgnored(t *testing.T) {
	d := newB379DB(t)
	s := &Service{
		Backend: &b379Backend{claims: &auth.Claims{UserID: 1, Username: "skyadmin", IsAdmin: true}, db: d},
		DB:      skygatedb.FixedDBSource{DB: d},
	}
	rec := b379Post(s, 987654, "")
	if rec.Code != http.StatusFound {
		t.Fatalf("a missing rule answered %d, want 302 with a message", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "err=") {
		t.Errorf("a missing rule produced no message: %q", loc)
	}
	if n := b379Count(t, d, `SELECT COUNT(*) FROM audit_log`); n != 0 {
		t.Errorf("a missing rule wrote %d audit row(s), want 0", n)
	}
}

// TestB379_NonAdminIsRefused — the route is behind the admin middleware AND the handler
// re-checks; a non-admin must not be able to delete another user's rule.
func TestB379_NonAdminIsRefused(t *testing.T) {
	d := newB379DB(t)
	id := b379SeedRule(t, d, 7, "michail", 42, "emilia", "subnet", "203.0.113.0/24", "", false)
	s := &Service{
		Backend: &b379Backend{claims: &auth.Claims{UserID: 9, Username: "michail", IsAdmin: false}, db: d},
		DB:      skygatedb.FixedDBSource{DB: d},
	}
	rec := b379Post(s, id, "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("a non-admin got %d, want 403", rec.Code)
	}
	if n := b379Count(t, d, `SELECT COUNT(*) FROM device_rules WHERE id = $1`, id); n != 1 {
		t.Errorf("the non-admin's request deleted the rule anyway")
	}
}
