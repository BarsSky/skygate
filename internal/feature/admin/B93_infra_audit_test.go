package admin

// B93_infra_audit_test.go — v0.33.1.41 — pins the contract
// for the 'infra' user identity in audit_log rows produced
// by the /admin/telegram SetEgress handler.
//
// Background: before B93, clicking "Set" on /admin/telegram
// wrote an audit_log row with the admin's own user_id +
// username (e.g. user=skyadmin, action=telegram_egress_set,
// detail=relay=emilia routes=12 ssh=ok). The operator's
// point was: the bot is a SYSTEM function (infra), not the
// admin's personal action. The new flow looks up the
// 'infra' portal user via InfraAuditIdentity and uses its
// (id, "infra") for the audit row.
//
// Tests:
//   - InfraAuditIdentity_FallsBackToCaller_WhenNoInfra
//   - InfraAuditIdentity_ReturnsInfra_WhenLinked
//   - InfraAuditIdentity_FallsBackWhenUnlinked

import (
	"testing"
)

// InfraAuditIdentity_FallsBackToCaller_WhenNoInfra — when
// the test DB has no 'infra' portal user, the shim
// returns the caller's own (id, username). This is the
// pre-B93 behaviour, preserved as the fallback path.
func TestInfraAuditIdentity_FallsBackToCaller_WhenNoInfra(t *testing.T) {
	s := newTestService(t)
	uid, name := s.Backend.InfraAuditIdentity(42, "skyadmin")
	if uid != 42 || name != "skyadmin" {
		t.Errorf("got (%d, %q), want (42, skyadmin)", uid, name)
	}
}

// InfraAuditIdentity_ReturnsInfra_WhenLinked — when the
// 'infra' portal user is seeded with a non-NULL
// headscale_user_id, the shim returns (99, "infra")
// regardless of the caller's fallback values.
func TestInfraAuditIdentity_ReturnsInfra_WhenLinked(t *testing.T) {
	s := newTestService(t)
	// Seed the 'infra' row. The V054 path uses id=99; we
	// don't pin it here (the test doesn't care about the
	// specific id) — just verify the shim picks it up.
	if _, err := s.DB.Exec(
		`INSERT INTO portal_users (id, username, headscale_user_id) VALUES (99, 'infra', 11)`,
	); err != nil {
		t.Fatalf("seed infra: %v", err)
	}
	uid, name := s.Backend.InfraAuditIdentity(42, "skyadmin")
	if uid != 99 || name != "infra" {
		t.Errorf("got (%d, %q), want (99, infra)", uid, name)
	}
}

// InfraAuditIdentity_FallsBackWhenUnlinked — when the
// 'infra' row exists but headscale_user_id is NULL
// (ensureInfraUser hasn't linked yet), the shim returns
// the caller's values. This matches the production
// semantics: a row without headscale_user_id is the
// "not yet wired" state, and we shouldn't pretend it's
// fully provisioned. The pre-B93 behaviour (admin
// audit) is preserved as the fallback.
func TestInfraAuditIdentity_FallsBackWhenUnlinked(t *testing.T) {
	s := newTestService(t)
	// Seed at id=99 with NULL headscale_user_id (the
	// newMemoryDB schema allows it: headscale_user_id
	// INTEGER, not NOT NULL).
	if _, err := s.DB.Exec(
		`INSERT INTO portal_users (id, username, headscale_user_id) VALUES (99, 'infra', NULL)`,
	); err != nil {
		t.Fatalf("seed infra: %v", err)
	}
	uid, name := s.Backend.InfraAuditIdentity(42, "skyadmin")
	if uid != 42 || name != "skyadmin" {
		t.Errorf("got (%d, %q), want fallback (42, skyadmin)", uid, name)
	}
}
