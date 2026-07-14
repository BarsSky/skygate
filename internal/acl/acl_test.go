package acl

// 2026-07-13: Этап 11 part 2b — tests for the shared ACL pipeline.
// GenerateACL is exercised end-to-end via the handlers tests
// (which still use it indirectly through the App wrapper) and
// SaveACLSnapshot + ApplyACLPipeline are tested directly here.

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"skygate/internal/headscale"
)

// minimalSchema covers the tables ApplyACLPipeline touches. The
// production migrations are not run here because the test stays
// in-memory and the schema is small.
const minimalSchema = `
CREATE TABLE portal_users (
	id INTEGER PRIMARY KEY,
	username TEXT NOT NULL,
	password_hash TEXT DEFAULT '',
	is_admin INTEGER DEFAULT 0,
	headscale_user_id INTEGER DEFAULT 0,
	theme TEXT DEFAULT 'linear',
	created_at INTEGER DEFAULT 0,
	default_device_node_id TEXT NOT NULL DEFAULT '',
	default_exit_node_id TEXT NOT NULL DEFAULT ''
);
CREATE TABLE device_rules (
	id INTEGER PRIMARY KEY,
	user_id INTEGER NOT NULL,
	device_id INTEGER NOT NULL,
	exit_node_id TEXT NOT NULL DEFAULT '',
	target_type TEXT NOT NULL DEFAULT 'domain',
	target_value TEXT NOT NULL,
	action TEXT DEFAULT 'accept',
	device_ip TEXT DEFAULT '',
	parent_domain TEXT DEFAULT '',
	enabled INTEGER DEFAULT 1
);
CREATE TABLE acl_snapshots (
	id INTEGER PRIMARY KEY,
	version INTEGER NOT NULL,
	config TEXT NOT NULL,
	created_by TEXT NOT NULL,
	applied_success INTEGER DEFAULT NULL,
	error_msg TEXT DEFAULT '',
	created_at INTEGER DEFAULT 0
);
CREATE TABLE exit_rule_logs (
	id INTEGER PRIMARY KEY,
	version INTEGER NOT NULL,
	action TEXT NOT NULL,
	detail TEXT DEFAULT '',
	created_at INTEGER DEFAULT 0
);
`

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	for _, q := range strings.Split(minimalSchema, ";") {
		q = strings.TrimSpace(q)
		if q == "" {
			continue
		}
		if _, err := d.Exec(q); err != nil {
			t.Fatalf("schema %q: %v", q, err)
		}
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func seedPortalUser(t *testing.T, d *sql.DB, username string) int64 {
	t.Helper()
	res, err := d.Exec(`INSERT INTO portal_users (username) VALUES (?)`, username)
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

// recordingAlerter captures SendAlert calls. The count is
// atomic so the async SendAlert goroutine in SaveACLSnapshot
// doesn't race with the test goroutine.
type recordingAlerter struct {
	count atomic.Int64
	last  atomic.Value // string
}

func (r *recordingAlerter) SendAlert(text string) int64 {
	r.count.Add(1)
	r.last.Store(text)
	return 0
}

func TestGenerateACLValidJSONShape(t *testing.T) {
	d := openTestDB(t)
	seedPortalUser(t, d, "alice")
	seedPortalUser(t, d, "bob")

	aclStr, err := GenerateACL(d)
	if err != nil {
		t.Fatalf("GenerateACL: %v", err)
	}
	if aclStr == "" || aclStr[0] != '{' {
		t.Fatalf("ACL JSON should start with '{', got %q...", aclStr[:min(10, len(aclStr))])
	}
	for _, want := range []string{
		`"dst": ["alice@tsnet.skynas.ru:*"]`,
		`"dst": ["bob@tsnet.skynas.ru:*"]`,
		`"dst": ["tag:public:*"]`,
		`"dst": ["tag:exit-node:*"]`,
		`"dst": ["*:*"]`,
		// tagOwners must declare every tag referenced
		// elsewhere in the policy, including tag:exit-node
		// (used by the SSH rule). Without this entry
		// headscale refuses the policy with "tag not found".
		`"tag:exit-node": ["skyadmin@tsnet.skynas.ru"]`,
		// Этап 14 v7: SSH rules for admin to manage
		// tag:exit-node (existing) and tag:public relay
		// nodes (new) as root. Match the multi-line JSON
		// formatting exactly so we catch accidental
		// whitespace regressions.
		`"src": ["tag:private", "skyadmin@tsnet.skynas.ru"],` + "\n" + `      "dst": ["tag:exit-node"]`,
		`"src": ["skyadmin@tsnet.skynas.ru"],` + "\n" + `      "dst": ["tag:public"]`,
	} {
		if !strings.Contains(aclStr, want) {
			t.Errorf("ACL missing %q", want)
		}
	}
}

func TestGenerateACLIncludesDeviceRules(t *testing.T) {
	d := openTestDB(t)
	uid := seedPortalUser(t, d, "alice")
	_, _ = d.Exec(`INSERT INTO device_rules (user_id, device_id, exit_node_id, target_type, target_value, action, device_ip) VALUES (?, 42, 'emilia', 'ip', '1.2.3.4', 'accept', '100.64.0.5')`, uid)

	aclStr, err := GenerateACL(d)
	if err != nil {
		t.Fatalf("GenerateACL: %v", err)
	}
	if !strings.Contains(aclStr, "1.2.3.4:*") {
		t.Error("expected 1.2.3.4 entry in ACL")
	}
	if !strings.Contains(aclStr, "100.64.0.5") {
		t.Error("expected device_ip 100.64.0.5 as src in ACL")
	}
}

func TestSaveACLSnapshotWritesRow(t *testing.T) {
	d := openTestDB(t)
	rec := &recordingAlerter{}
	ver := SaveACLSnapshot(d, `{"acls":[]}`, "alice", rec)
	if ver < 1 {
		t.Errorf("SaveACLSnapshot returned %d, want >= 1", ver)
	}
	var gotVersion int
	var gotConfig, gotBy string
	_ = d.QueryRow(`SELECT version, config, created_by FROM acl_snapshots WHERE id = 1`).Scan(&gotVersion, &gotConfig, &gotBy)
	if gotVersion != ver {
		t.Errorf("version = %d, want %d", gotVersion, ver)
	}
	if gotConfig != `{"acls":[]}` {
		t.Errorf("config = %q", gotConfig)
	}
	if gotBy != "alice" {
		t.Errorf("created_by = %q, want 'alice'", gotBy)
	}
}

func TestSaveACLSnapshotAlerterNotified(t *testing.T) {
	d := openTestDB(t)
	rec := &recordingAlerter{}
	SaveACLSnapshot(d, `{"acls":[]}`, "alice", rec)
	// SaveACLSnapshot fires SendAlert async via goroutine; the
	// count is atomic so polling is race-free. Give the scheduler
	// a moment to run the goroutine.
	for i := 0; i < 100; i++ {
		if rec.count.Load() > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Error("expected SendAlert to be called, got 0 calls")
}

func TestSaveACLSnapshotNilAlerter(t *testing.T) {
	d := openTestDB(t)
	// nil alerter must not panic — the bot path relies on this.
	ver := SaveACLSnapshot(d, `{"acls":[]}`, "alice", nil)
	if ver < 1 {
		t.Errorf("ver = %d, want >= 1", ver)
	}
	var n int
	_ = d.QueryRow(`SELECT COUNT(*) FROM acl_snapshots`).Scan(&n)
	if n != 1 {
		t.Errorf("expected 1 snapshot row, got %d", n)
	}
}

// fakeHeadscale is a minimal httptest-backed headscale server that
// handles PUT /api/v1/policy (the only endpoint ApplyACLPipeline
// touches). The returned *headscale.Client points at the test
// server so the test runs without a real headscale instance.
func fakeHeadscale(t *testing.T, policyStatus int, policyErr error) (*headscale.Client, *atomic.Int64) {
	t.Helper()
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/policy" || r.Method != http.MethodPut {
			http.Error(w, "unexpected: "+r.Method+" "+r.URL.Path, 404)
			return
		}
		calls.Add(1)
		if policyErr != nil {
			http.Error(w, policyErr.Error(), policyStatus)
			return
		}
		w.WriteHeader(policyStatus)
		_, _ = w.Write([]byte(`{"policy":"...","updated_at":"x"}`))
	}))
	t.Cleanup(srv.Close)
	hs := headscale.New(srv.URL, "fake-key")
	return hs, &calls
}

func TestApplyACLPipelineSuccess(t *testing.T) {
	d := openTestDB(t)
	seedPortalUser(t, d, "alice")
	hs, hsCalls := fakeHeadscale(t, http.StatusOK, nil)
	rec := &recordingAlerter{}

	res := ApplyACLPipeline(d, hs, rec, "alice", "user alice added rule test")
	if !res.Applied {
		t.Errorf("Applied = false, want true; err = %v", res.Err)
	}
	if res.Err != nil {
		t.Errorf("Err = %v, want nil", res.Err)
	}
	if res.Version < 1 {
		t.Errorf("Version = %d, want >= 1", res.Version)
	}
	if hsCalls.Load() != 1 {
		t.Errorf("HS SetPolicy called %d times, want 1", hsCalls.Load())
	}

	// acl_snapshots row marked applied.
	var applied sql.NullInt64
	_ = d.QueryRow(`SELECT applied_success FROM acl_snapshots WHERE version = ?`, res.Version).Scan(&applied)
	if !applied.Valid || applied.Int64 != 1 {
		t.Errorf("applied_success = %v, want 1", applied)
	}

	// exit_rule_logs has one row for the apply.
	var logAction, logDetail string
	_ = d.QueryRow(`SELECT action, detail FROM exit_rule_logs WHERE version = ? ORDER BY id DESC LIMIT 1`, res.Version).Scan(&logAction, &logDetail)
	if logAction != "apply" {
		t.Errorf("log action = %q, want %q", logAction, "apply")
	}
	if !strings.Contains(logDetail, "user alice added rule test") {
		t.Errorf("log detail = %q, want to contain the detailForLog", logDetail)
	}
}

func TestApplyACLPipelineSetPolicyError(t *testing.T) {
	d := openTestDB(t)
	seedPortalUser(t, d, "alice")
	hs, hsCalls := fakeHeadscale(t, http.StatusInternalServerError, fmt.Errorf("policy boom"))
	rec := &recordingAlerter{}

	res := ApplyACLPipeline(d, hs, rec, "alice", "user alice added rule test")
	if res.Applied {
		t.Error("Applied = true, want false on SetPolicy failure")
	}
	if res.Err == nil {
		t.Error("Err = nil, want non-nil")
	}
	if res.Version < 1 {
		t.Errorf("Version = %d, want >= 1 (snapshot is always saved)", res.Version)
	}
	if hsCalls.Load() != 1 {
		t.Errorf("HS SetPolicy called %d times, want 1", hsCalls.Load())
	}

	// acl_snapshots row exists but is NOT marked applied.
	var nApplied, nFailed int
	_ = d.QueryRow(`SELECT COUNT(*) FROM acl_snapshots WHERE version = ? AND applied_success = 1`, res.Version).Scan(&nApplied)
	_ = d.QueryRow(`SELECT COUNT(*) FROM acl_snapshots WHERE version = ? AND applied_success = 0`, res.Version).Scan(&nFailed)
	if nApplied != 0 {
		t.Errorf("expected 0 applied rows on failure, got %d", nApplied)
	}
	if nFailed != 1 {
		t.Errorf("expected 1 failed row on failure, got %d", nFailed)
	}

	// error_msg captures the headscale error.
	var errMsg string
	_ = d.QueryRow(`SELECT error_msg FROM acl_snapshots WHERE version = ?`, res.Version).Scan(&errMsg)
	if !strings.Contains(errMsg, "policy boom") {
		t.Errorf("error_msg = %q, want to contain 'policy boom'", errMsg)
	}

	// exit_rule_logs has the failure row.
	var logAction, logDetail string
	_ = d.QueryRow(`SELECT action, detail FROM exit_rule_logs WHERE version = ? ORDER BY id DESC LIMIT 1`, res.Version).Scan(&logAction, &logDetail)
	if logAction != "apply_fail" {
		t.Errorf("log action = %q, want %q", logAction, "apply_fail")
	}
	if !strings.Contains(logDetail, "policy boom") {
		t.Errorf("log detail = %q, want to contain 'policy boom'", logDetail)
	}
}

func TestApplyACLPipelineGenerateACLError(t *testing.T) {
	// Closed DB → GenerateACL fails → no snapshot, no SetPolicy
	// call, returned Version=0, Err!=nil.
	d, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_ = d.Close()
	hs, hsCalls := fakeHeadscale(t, http.StatusOK, nil)

	res := ApplyACLPipeline(d, hs, nil, "alice", "test")
	if res.Err == nil {
		t.Error("expected Err on closed DB")
	}
	if res.Applied {
		t.Error("Applied = true, want false on GenerateACL error")
	}
	if res.Version != 0 {
		t.Errorf("Version = %d, want 0 on GenerateACL error", res.Version)
	}
	if hsCalls.Load() != 0 {
		t.Errorf("HS SetPolicy called %d times on GenerateACL error, want 0", hsCalls.Load())
	}
}

func TestApplyACLPipelineNilAlerter(t *testing.T) {
	// Bot-style: no notifier, just the DB + HS writes. The
	// pipeline must not panic on nil alerter.
	d := openTestDB(t)
	seedPortalUser(t, d, "alice")
	hs, _ := fakeHeadscale(t, http.StatusOK, nil)

	res := ApplyACLPipeline(d, hs, nil, "alice", "test")
	if !res.Applied {
		t.Errorf("Applied = false; err = %v", res.Err)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
