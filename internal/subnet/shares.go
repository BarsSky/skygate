// Cross-user IP-level subnet sharing (v0.17.1).
//
// When user G (grantor) shares their personal subnet
// with user B (grantee), the ACL builder appends
// G's CIDR to B's per-user dst list. The reverse is
// NOT automatic — sharing is one-directional (G → B).
// Bob can be granted access to alice's subnet, but
// alice doesn't automatically get access to bob's.
//
// Future v0.17.1 follow-up: symmetric sharing
// (granting one direction is the same as granting the
// reverse). For v0.17.1 we keep it explicit so the
// operator can audit each direction independently.
//
// Concurrency: Grant is idempotent (PRIMARY KEY on
// (grantor, grantee)). The caller can call Grant
// multiple times for the same pair without error.
// Revoke is also idempotent (no rows affected = no
// error).
package subnet

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Share is one row of user_subnet_shares: the
// grantor (whose subnet is being shared) and the
// grantee (who gets access to grantor's subnet). The
// asymmetric semantics mean a share row only grants
// the grantee → grantor direction, not the reverse.
type Share struct {
	GrantorUserID int64
	GranteeUserID int64
	CreatedAt     time.Time
}

// ErrSelfShare is returned by Grant when the caller
// tries to share a subnet with themselves. Sharing
// with yourself is a no-op (the per-user rule already
// includes your own CIDR), but the explicit error
// surfaces the operation as a contract violation
// rather than silently succeeding.
var ErrSelfShare = errors.New("subnet: cannot share subnet with self")

// ErrShareNotFound is returned by Revoke when the
// (grantor, grantee) pair has no row in
// user_subnet_shares. Revoke is otherwise idempotent
// (a no-op revocation is a no-op), but the explicit
// error is useful for the bot reply ("you haven't
// shared with this user yet") rather than silently
// succeeding.
var ErrShareNotFound = errors.New("subnet: share not found")

// Grant adds a (grantor, grantee) row to
// user_subnet_shares. Idempotent: a duplicate row
// (same grantor, same grantee) is a no-op, not an
// error. Returns ErrSelfShare if grantor == grantee
// (sharing with yourself is a contract violation).
//
// Pre-condition: the grantor MUST have a row in
// user_subnets. The function checks this and returns
// ErrNotFound if the grantor has no subnet allocated
// — there's nothing to share.
//
// v0.17.1: caller is responsible for re-applying the
// ACL after a successful Grant (the ACL builder
// reads the shares table). The auto-reapply on
// Allocate handler does this transparently; the
// bot /share_subnet path explicitly calls
// acl.ApplyACLPipelineForPlane after a successful
// Grant.
func Grant(d *sql.DB, grantorUserID, granteeUserID int64) error {
	if grantorUserID == granteeUserID {
		return ErrSelfShare
	}
	// Pre-check: grantor must have a subnet row.
	sub, err := Get(d, grantorUserID)
	if err != nil {
		return err
	}
	if sub == nil {
		return ErrNotFound
	}
	now := time.Now().Unix()
	// INSERT OR IGNORE handles the duplicate-Grant
	// case (same grantor+grantee inserted twice is a
	// no-op, not an error). The PRIMARY KEY constraint
	// on (grantor_user_id, grantee_user_id) prevents
	// the duplicate; the IGNORE clause skips the
	// constraint violation silently.
	_, err = d.Exec(`
		INSERT OR IGNORE INTO user_subnet_shares
			(grantor_user_id, grantee_user_id, created_at)
		VALUES (?, ?, ?)
	`, grantorUserID, granteeUserID, now)
	if err != nil {
		return fmt.Errorf("subnet: grant share: %w", err)
	}
	return nil
}

// Revoke removes a (grantor, grantee) row from
// user_subnet_shares. Idempotent in spirit: a no-op
// revocation (the row isn't present) is a no-op, not
// an error. Returns ErrShareNotFound for the missing
// case so the bot can show a useful reply ("you
// haven't shared with this user yet") instead of
// silently succeeding.
//
// v0.17.1: caller is responsible for re-applying the
// ACL after a successful Revoke (same reason as
// Grant — the ACL builder reads the shares table).
func Revoke(d *sql.DB, grantorUserID, granteeUserID int64) error {
	if grantorUserID == granteeUserID {
		return ErrSelfShare
	}
	res, err := d.Exec(`
		DELETE FROM user_subnet_shares
		 WHERE grantor_user_id = ? AND grantee_user_id = ?
	`, grantorUserID, granteeUserID)
	if err != nil {
		return fmt.Errorf("subnet: revoke share: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrShareNotFound
	}
	return nil
}

// ListSharedBy returns every share that userID has
// GRANTED — i.e. the list of users who can access
// userID's personal subnet. Used by the admin UI
// "Sharing" section to show "I've shared my subnet
// with: bob, daniil".
//
// v0.17.1.
func ListSharedBy(d *sql.DB, userID int64) ([]Share, error) {
	rows, err := d.Query(`
		SELECT grantor_user_id, grantee_user_id, created_at
		  FROM user_subnet_shares
		 WHERE grantor_user_id = ?
		 ORDER BY created_at
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanShares(rows)
}

// ListSharedWith returns every share that OTHERS
// have granted TO userID — i.e. the list of users
// whose subnets userID can access. Used by the
// admin UI to show "I have access to: alice,
// michail's subnets".
//
// v0.17.1.
func ListSharedWith(d *sql.DB, userID int64) ([]Share, error) {
	rows, err := d.Query(`
		SELECT grantor_user_id, grantee_user_id, created_at
		  FROM user_subnet_shares
		 WHERE grantee_user_id = ?
		 ORDER BY created_at
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanShares(rows)
}

// scanShares is a small helper that wraps the
// rows.Next() loop with the right Scan signature.
func scanShares(rows *sql.Rows) ([]Share, error) {
	var out []Share
	for rows.Next() {
		var s Share
		var createdI int64
		if err := rows.Scan(&s.GrantorUserID, &s.GranteeUserID, &createdI); err != nil {
			return nil, err
		}
		s.CreatedAt = time.Unix(createdI, 0)
		out = append(out, s)
	}
	return out, rows.Err()
}
