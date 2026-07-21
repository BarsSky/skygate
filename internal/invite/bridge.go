// 2026-07-20: v0.21.0 — bridge logic for invite consume.
//
// When an invite is successfully consumed (the
// atomic UPDATE in ConsumeCode returned 1 row),
// the caller invokes ApplyBridge(grantorID,
// granteeID) which:
//
//  1. Inserts a row into user_subnet_shares
//     (grantor_user_id, grantee_user_id) so the
//     ACL builder picks it up on the next
//     pipeline run. The shape matches v0.17.1's
//     admin share — same PRIMARY KEY, so
//     re-applying the bridge is idempotent
//     (INSERT OR IGNORE).
//
//  2. Triggers the v0.17.1 ACL re-apply
//     goroutine for every distinct
//     headscale_url (per-plane ACL). The
//     re-apply is async (goroutine), so the
//     bot /accept reply is fast — the operator
//     doesn't wait for the headscale API to
//     confirm.
//
//  3. Writes an audit_log row so the share is
//     traceable: who shared with whom, when,
//     from which invite code.
//
//  4. Optionally sends a Telegram alert to the
//     grantor (via Notifier.SendAlert, calm
//     mode) so they know their invite was
//     actually used. The grantor usually knows
//     in real time (they sent the code to the
//     grantee), but the alert is useful when
//     the grantee consumed the code days later
//     and the grantor forgot about it.

package invite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"time"
)

// NotifierSink is the subset of the
// telegram.Notifier interface the bridge
// needs. Mirrors the pattern in
// internal/release/monitor.go and
// internal/headscale_version/monitor.go.
type NotifierSink interface {
	SendAlert(text string) int64
}

// ACLApplier is the subset of the ACL pipeline
// the bridge needs. The production wiring is
// the v0.17.1 acl.ApplyACLPipelineForPlane
// closure; tests can pass a no-op.
type ACLApplier interface {
	// ApplyACLForPlane re-runs GenerateACL() and
	// pushes the result to the given headscale
	// URL. The shape mirrors
	// internal/acl.ApplyACLPipelineForPlane.
	ApplyACLForPlane(planeURL string) error
}

// Auditor is the subset of the audit-log API
// the bridge needs. Production wiring is the
// App.audit closure on App.
type Auditor interface {
	Audit(actorID int64, actorName, action, detail string) error
}

// ApplyBridge writes the user_subnet_shares
// row, optionally triggers an ACL re-apply,
// and writes the audit log. The function is
// idempotent: re-applying a bridge that
// already exists is a no-op for the share
// row (INSERT OR IGNORE on PRIMARY KEY) and a
// duplicate trigger for the ACL re-apply
// (which itself is idempotent because the
// pipeline re-reads current state from the
// DB).
//
// grantorID is the user id of the user sharing
// their subnet. granteeID is the user id of
// the user who accepted the invite. They MUST
// both exist in portal_users (the caller —
// the bot /accept handler — has already
// resolved granteeUsername to granteeID via
// the auth middleware).
//
// code is the invite code that was consumed;
// included in the audit row so a future
// "which invite led to this share" lookup is
// one JOIN away. planeURLs is the list of
// distinct headscale URLs the operator has
// configured (drives the ACL re-apply scope);
// if nil, the bridge is recorded but the
// ACL re-apply is skipped (the operator may
// re-apply via /admin/exit-rules/reapply
// later).
//
// Returns nil on success (share row + audit
// written). ACL re-apply failures are logged
// but don't fail the bridge — the share
// itself is the durable state, the ACL
// re-push is best-effort and can be retried
// via the existing /admin/exit-rules/reapply
// endpoint.
func ApplyBridge(
	d *sql.DB,
	grantorID, granteeID int64,
	code, granteeUsername string,
	planeURLs []string,
	applier ACLApplier,
	auditor Auditor,
	notifier NotifierSink,
) error {
	if grantorID <= 0 || granteeID <= 0 {
		return fmt.Errorf("invite: ApplyBridge: invalid ids grantor=%d grantee=%d", grantorID, granteeID)
	}
	if grantorID == granteeID {
		// Should be caught upstream by
		// ErrSelfInvite, but defensive.
		return ErrSelfInvite
	}

	// Step 1: write the share row. INSERT OR
	// IGNORE on the (grantor, grantee) PK means
	// re-applying the bridge is a no-op (the
	// share already exists). The 0-rowsAffected
	// case is normal (idempotent retry), not an
	// error.
	if _, err := d.Exec(`
		INSERT OR IGNORE INTO user_subnet_shares
			(grantor_user_id, grantee_user_id, created_at)
		VALUES (?, ?, ?)
	`, grantorID, granteeID, time.Now().Unix()); err != nil {
		return fmt.Errorf("invite: write share: %w", err)
	}

	// Step 2: audit log. The audit row carries
	// the invite code so a future "this share
	// came from invite ABCD1234" lookup is one
	// grep away.
	if auditor != nil {
		detail := fmt.Sprintf("invite=%s grantor=%d grantee=%d",
			code, grantorID, granteeID)
		if err := auditor.Audit(0, "system", "invite_bridge", detail); err != nil {
			// Audit failure is logged but not
			// fatal — the share is already in
			// the DB.
			log.Printf("invite: audit: %v", err)
		}
	}

	// Step 3: ACL re-apply. One goroutine per
	// distinct plane, fire-and-forget. The
	// bridge reply is fast even on a slow
	// headscale (the actual re-apply is the
	// same v0.17.1 auto-reapply path, which
	// already runs in a goroutine from the
	// handlers).
	if applier != nil && len(planeURLs) > 0 {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			for _, plane := range planeURLs {
				if err := applier.ApplyACLForPlane(plane); err != nil {
					log.Printf("invite: ACL re-apply for plane %q failed (recoverable): %v", plane, err)
				}
			}
			_ = ctx // reserved for future context-aware cancel
		}()
	}

	// Step 4: notify the grantor. Use a
	// best-effort pattern; notifier may be nil
	// (test wiring) or may be a no-op.
	if notifier != nil {
		text := fmt.Sprintf("🌉 Subnet bridge applied\nInvite: %s\nYour subnet is now reachable to: %s",
			code, granteeUsername)
		_ = notifier.SendAlert(text)
	}
	return nil
}

// ResolveGranteeID looks up a portal_users.id
// from a username. Returns 0 if the user
// doesn't exist (the caller decides whether
// to surface that as ErrNotForYou or a custom
// "user hasn't signed up yet" hint).
func ResolveGranteeID(d *sql.DB, username string) (int64, error) {
	var id int64
	err := d.QueryRow(`SELECT id FROM portal_users WHERE username = ?`, username).Scan(&id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, err
	}
	return id, nil
}

// DistinctHeadscaleURLs returns the de-duped
// list of headscale URLs the operator has
// configured across portal_users. Used by
// the bot /accept handler to scope the ACL
// re-apply. Returns an empty slice if every
// user is on the global default plane (in
// which case the ACL re-apply is driven by
// the v0.17.1 hook on the share row insert).
func DistinctHeadscaleURLs(d *sql.DB) ([]string, error) {
	rows, err := d.Query(`
		SELECT DISTINCT headscale_url
		FROM portal_users
		WHERE headscale_url != '' AND headscale_url IS NOT NULL
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
