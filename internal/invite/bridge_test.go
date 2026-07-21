// 2026-07-20: v0.21.0 — tests for the bridge logic.

package invite

import (
	"errors"
	"testing"
	"time"

	"skygate/internal/db"
)

// stubACLApplier is a no-op that records the
// plane URLs it was called with. Used to
// verify the re-apply scope (all distinct
// headscale URLs, exactly once each).
type stubACLApplier struct {
	applied []string
	failOn  map[string]error // planeURL -> err
}

func (s *stubACLApplier) ApplyACLForPlane(planeURL string) error {
	if err, ok := s.failOn[planeURL]; ok {
		return err
	}
	s.applied = append(s.applied, planeURL)
	return nil
}

// stubAuditor records audit calls.
type stubAuditor struct {
	calls []string
}

func (s *stubAuditor) Audit(actorID int64, actorName, action, detail string) error {
	s.calls = append(s.calls, action+":"+detail)
	return nil
}

// stubNotifier records alert calls.
type stubNotifier struct {
	alerts []string
}

func (s *stubNotifier) SendAlert(text string) int64 {
	s.alerts = append(s.alerts, text)
	return 0
}

func TestApplyBridgeWritesShareRow(t *testing.T) {
	d := db.OpenForTest(t)
	insertUser(t, d, "alice")
	insertUser(t, d, "bob")
	aliceID := userIDByName(t, d, "alice")
	bobID := userIDByName(t, d, "bob")

	applier := &stubACLApplier{}
	auditor := &stubAuditor{}
	notifier := &stubNotifier{}

	err := ApplyBridge(d, aliceID, bobID, "TESTCODE", "bob", nil, applier, auditor, notifier)
	if err != nil {
		t.Fatalf("ApplyBridge: %v", err)
	}

	// Verify the share row was written
	var grantor, grantee int64
	if err := d.QueryRow(`SELECT grantor_user_id, grantee_user_id FROM user_subnet_shares WHERE grantor_user_id = ?`, aliceID).Scan(&grantor, &grantee); err != nil {
		t.Fatalf("read share: %v", err)
	}
	if grantor != aliceID || grantee != bobID {
		t.Errorf("share = (%d, %d), want (%d, %d)", grantor, grantee, aliceID, bobID)
	}
}

func TestApplyBridgeIdempotent(t *testing.T) {
	d := db.OpenForTest(t)
	insertUser(t, d, "alice")
	insertUser(t, d, "bob")
	aliceID := userIDByName(t, d, "alice")
	bobID := userIDByName(t, d, "bob")
	applier := &stubACLApplier{}
	auditor := &stubAuditor{}
	notifier := &stubNotifier{}

	// First call — writes the share
	if err := ApplyBridge(d, aliceID, bobID, "CODE1", "bob", nil, applier, auditor, notifier); err != nil {
		t.Fatalf("first ApplyBridge: %v", err)
	}
	// Second call — same ids, different code
	if err := ApplyBridge(d, aliceID, bobID, "CODE2", "bob", nil, applier, auditor, notifier); err != nil {
		t.Fatalf("second ApplyBridge: %v", err)
	}
	// Should still be exactly 1 share row
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM user_subnet_shares WHERE grantor_user_id = ? AND grantee_user_id = ?`, aliceID, bobID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("share rows = %d, want 1 (idempotent)", n)
	}
}

func TestApplyBridgeRejectsSelfBridge(t *testing.T) {
	d := db.OpenForTest(t)
	insertUser(t, d, "alice")
	aliceID := userIDByName(t, d, "alice")
	applier := &stubACLApplier{}
	auditor := &stubAuditor{}
	notifier := &stubNotifier{}

	err := ApplyBridge(d, aliceID, aliceID, "CODE", "alice", nil, applier, auditor, notifier)
	if !errors.Is(err, ErrSelfInvite) {
		t.Errorf("ApplyBridge(self) = %v, want ErrSelfInvite", err)
	}
}

func TestApplyBridgeWritesAudit(t *testing.T) {
	d := db.OpenForTest(t)
	insertUser(t, d, "alice")
	insertUser(t, d, "bob")
	aliceID := userIDByName(t, d, "alice")
	bobID := userIDByName(t, d, "bob")
	applier := &stubACLApplier{}
	auditor := &stubAuditor{}
	notifier := &stubNotifier{}

	if err := ApplyBridge(d, aliceID, bobID, "AUDITEST", "bob", nil, applier, auditor, notifier); err != nil {
		t.Fatalf("ApplyBridge: %v", err)
	}

	// Should have written exactly 1 audit row
	// (the bridge call)
	if len(auditor.calls) != 1 {
		t.Errorf("audit calls = %d, want 1", len(auditor.calls))
	}
	if !contains(auditor.calls[0], "AUDITEST") {
		t.Errorf("audit detail missing code: %q", auditor.calls[0])
	}
}

func TestApplyBridgeNotifiesGrantor(t *testing.T) {
	d := db.OpenForTest(t)
	insertUser(t, d, "alice")
	insertUser(t, d, "bob")
	aliceID := userIDByName(t, d, "alice")
	bobID := userIDByName(t, d, "bob")
	applier := &stubACLApplier{}
	auditor := &stubAuditor{}
	notifier := &stubNotifier{}

	if err := ApplyBridge(d, aliceID, bobID, "NOTIFYTEST", "bob", nil, applier, auditor, notifier); err != nil {
		t.Fatalf("ApplyBridge: %v", err)
	}
	if len(notifier.alerts) != 1 {
		t.Errorf("alerts = %d, want 1", len(notifier.alerts))
	}
	if !contains(notifier.alerts[0], "NOTIFYTEST") {
		t.Errorf("alert missing code: %q", notifier.alerts[0])
	}
}

func TestDistinctHeadscaleURLs(t *testing.T) {
	d := db.OpenForTest(t)
	// Insert users with various plane URLs
	_, err := d.Exec(`INSERT INTO portal_users(username, password_hash, headscale_url) VALUES
		('u1', 'x', 'http://plane1:50444'),
		('u2', 'x', 'http://plane1:50444'),
		('u3', 'x', 'http://plane2:50444'),
		('u4', 'x', '')`)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	urls, err := DistinctHeadscaleURLs(d)
	if err != nil {
		t.Fatalf("DistinctHeadscaleURLs: %v", err)
	}
	if len(urls) != 2 {
		t.Errorf("urls = %v, want 2 distinct", urls)
	}
}

func contains(s, needle string) bool {
	return len(s) >= len(needle) && (s == needle || indexOf(s, needle) >= 0)
}

func indexOf(s, needle string) int {
	for i := 0; i+len(needle) <= len(s); i++ {
		if s[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// silence unused warning
var _ = time.Now
