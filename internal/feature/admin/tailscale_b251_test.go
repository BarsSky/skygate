// tailscale_b251_test.go — B251 tests for the reserved-name
// shortcut in findUserForHostname and the new
// infraHeadscaleUserID helper. PG-backed: needs SKYGATE_TEST_PG_DSN.

package admin

import (
	"context"
	"database/sql"
	"testing"

	"skygate/internal/db"
)

// dbDBSource wraps a *sql.DB so it satisfies the admin
// package's DBSource interface (Current() *sql.DB). Mirrors
// the pattern in devices_claim_test.go / users_adopt_promote_test.go.
type b251DBSource struct{ db *sql.DB }

func (d *b251DBSource) Current() *sql.DB { return d.db }

// TestInfraHeadscaleUserID_B251_Happy seeds the portal_users
// row the way ensureInfraUser would and verifies the lookup.
func TestInfraHeadscaleUserID_B251_Happy(t *testing.T) {
	conn := db.OpenTestPG(t)
	if conn == nil {
		t.Skip("no SKYGATE_TEST_PG_DSN")
	}
	defer conn.Close()
	ctx := context.Background()

	if _, err := conn.ExecContext(ctx,
		`INSERT INTO portal_users (username, headscale_user_id, is_admin)
		 VALUES ('infra', 85, 0)`,
	); err != nil {
		t.Fatalf("seed portal_users.infra: %v", err)
	}

	svc := &Service{DB: &b251DBSource{db: conn}}
	uid, err := svc.infraHeadscaleUserID(ctx)
	if err != nil {
		t.Fatalf("infraHeadscaleUserID happy path: %v", err)
	}
	if uid != 85 {
		t.Errorf("got uid=%d, want 85", uid)
	}
}

// TestInfraHeadscaleUserID_B251_Missing covers the case where
// ensureInfraUser hasn't run yet (or failed silently). The
// caller (findUserForHostname) wraps this in a user-visible flash
// message; we just verify the error is non-nil.
func TestInfraHeadscaleUserID_B251_Missing(t *testing.T) {
	conn := db.OpenTestPG(t)
	if conn == nil {
		t.Skip("no SKYGATE_TEST_PG_DSN")
	}
	defer conn.Close()
	ctx := context.Background()

	svc := &Service{DB: &b251DBSource{db: conn}}
	_, err := svc.infraHeadscaleUserID(ctx)
	if err == nil {
		t.Errorf("expected error when portal_users.infra missing, got nil")
	}
}

// TestInfraHeadscaleUserID_B251_NullLink covers the case
// where ensureInfraUser created the portal row but didn't yet
// link to the headscale user (headscale_user_id = 0, the
// NOT NULL DEFAULT 0 the schema applies).
func TestInfraHeadscaleUserID_B251_NullLink(t *testing.T) {
	conn := db.OpenTestPG(t)
	if conn == nil {
		t.Skip("no SKYGATE_TEST_PG_DSN")
	}
	defer conn.Close()
	ctx := context.Background()

	if _, err := conn.ExecContext(ctx,
		`INSERT INTO portal_users (username, headscale_user_id, is_admin)
		 VALUES ('infra', 0, 0)`,
	); err != nil {
		t.Fatalf("seed portal_users.infra (null link): %v", err)
	}

	svc := &Service{DB: &b251DBSource{db: conn}}
	_, err := svc.infraHeadscaleUserID(ctx)
	if err == nil {
		t.Errorf("expected error when headscale_user_id is 0, got nil")
	}
}