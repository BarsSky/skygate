package exit_rules

// reconciler_rename.go — v1.5.2 (B231) — preferred-exit
// hostname-rename migrator.
//
// The problem
// -----------
// `device_exit_node_prefs` is keyed on (user_id,
// device_hostname). When the operator renames a
// device in headscale (e.g. cyborg → cyborg-v2),
// `node_owner_map.hostname` gets updated to the new
// name, but the prefs row stays on the OLD hostname.
// The old pref is now invisible to /my/devices (which
// looks up by current hostname) and to the B229
// reconciler (which iterates pairs that exist in
// `device_rules` — but device_rules is keyed by
// `device_hostname` too, so rename also orphans the
// rules). The result: orphan pref + orphan rules +
// invisible device.
//
// B231 detects three classes of "orphan" prefs and
// reacts:
//
//   - ClassificationNormal:     the (user, hostname)
//                                pair has a matching
//                                node_owner_map row → no-op
//   - ClassificationRename:     the (user, hostname)
//                                pair is missing from
//                                node_owner_map, but
//                                there's exactly ONE
//                                other row with the same
//                                user_id and tag → it's
//                                a rename. AUTO-MIGRATE
//                                the pref to the new
//                                hostname (UPSERT new
//                                row, DELETE old row).
//                                Audit + Telegram
//                                alert (rate-limited).
//   - ClassificationAmbiguous:  the (user, hostname)
//                                pair is missing, and
//                                MULTIPLE rows match
//                                the same tag → operator
//                                has more than one device
//                                with the same preferred
//                                tag (rare). Do nothing
//                                automatically; log a
//                                "needs manual review"
//                                skip-change with the
//                                candidate hostnames.
//   - ClassificationOrphan:     the (user, hostname)
//                                pair is missing, and NO
//                                row matches the tag →
//                                the device was likely
//                                permanently deleted.
//                                Do NOT auto-delete (too
//                                dangerous — the operator
//                                might re-register the
//                                device with a slightly
//                                different tag, in which
//                                case the pref is the
//                                operator's only memory of
//                                "this user used to want
//                                emilia"). Write a
//                                "preferred_reconcile_orphan_candidate"
//                                audit row + send a
//                                rate-limited Telegram
//                                alert with the manual
//                                DELETE SQL.
//
// 2026-09-03: v1.5.2 (B231).

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"

	"skygate/internal/db"
)

// OrphanClassification — v1.5.2 (B231) — the verdict of
// classifyOrphanPref. The constant values are also used
// as keys in RenameMigration.Classification (string-
// comparable for the audit log + Telegram alert).
type OrphanClassification string

const (
	ClassificationNormal    OrphanClassification = "normal"
	ClassificationRename    OrphanClassification = "rename"
	ClassificationAmbiguous OrphanClassification = "ambiguous"
	ClassificationOrphan    OrphanClassification = "orphan"
)

// RenameMigration — v1.5.2 (B231) — single change the
// rename migrator applied (or would apply in dry-run
// mode). Returned from MigrateRenamedDevicePrefs so
// the caller (RunPreferredExitReconciler in
// handlers.go) can render a one-line summary per
// tick + pass the list to the optional Telegram
// alerter.
type RenameMigration struct {
	Classification   OrphanClassification
	UserID           int64
	Username         string
	OldHostname      string
	NewHostname      string // "" for Ambiguous + Orphan
	ExitNodeTag      string
	AmbiguousMatches []string // hostnames that share the tag (Ambiguous only)
	Applied          bool     // true if the live-mode write happened; false for dry-run
}

// RenameOrphanCandidate is the in-memory shape of one
// (user, device) pref row's input to the decision
// function. The DB-touching MigrateRenamedDevicePrefs
// populates a list of these and calls
// ClassifyRenameMigration per item; the pure
// classification is the unit-testable surface
// (mirrors B229's PlanDevicePrefChange).
type RenameOrphanCandidate struct {
	UserID           int64
	Username         string
	Hostname         string
	ExitNodeTag      string
	// HasNodeOwnerMapRow is true if (user, hostname) is
	// in node_owner_map. Computed by the caller.
	HasNodeOwnerMapRow bool
	// CandidatesForRename is the list of (user, tag)
	// matches in node_owner_map that don't match the
	// current hostname. Computed by the caller via
	// SELECT hostname FROM node_owner_map WHERE
	// tagged_by_user_id = $1 AND tag = $2 AND hostname
	// <> $3. Empty → ClassificationOrphan. Length 1
	// → ClassificationRename. Length 2+ →
	// ClassificationAmbiguous.
	CandidatesForRename []string
}

// ClassifyRenameMigration is the pure decision function
// for the B231 rename migrator — given a candidate
// (pre-loaded by the caller with the two lookups
// against node_owner_map), return the verdict.
//
// 2026-09-03: v1.5.2 (B231).
func ClassifyRenameMigration(c RenameOrphanCandidate) OrphanClassification {
	if c.HasNodeOwnerMapRow {
		return ClassificationNormal
	}
	if len(c.CandidatesForRename) == 0 {
		return ClassificationOrphan
	}
	if len(c.CandidatesForRename) == 1 {
		return ClassificationRename
	}
	return ClassificationAmbiguous
}

// MigrateRenamedDevicePrefs — v1.5.2 (B231) — the
// DB-touching orchestrator. Walks every row in
// `device_exit_node_prefs` and classifies it via
// ClassifyRenameMigration. For ClassificationRename
// rows, applies the rename (UPSERT new row + DELETE
// old row) in live mode, or just logs in dry-run
// mode. For Ambiguous + Orphan, writes audit_log +
// (rate-limited) Telegram alert.
//
// Inherits the live/dry-run gate from
// SKYGATE_PREFERRED_RECONCILER_LIVE (same as the main
// B229 reconciler). Disabled by env. The DB-backed
// on/off toggle (B231 UI) gates the whole
// RunPreferredExitReconciler goroutine — if it's off,
// the rename migrator doesn't run either.
//
// 2026-09-03: v1.5.2 (B231).
func (s *Service) MigrateRenamedDevicePrefs(ctx context.Context, n ReconcilerNotifier) ([]RenameMigration, error) {
	if n == nil {
		n = noopNotifier{}
	}
	live := PreferredExitReconcilerLive()
	usernameCache := make(map[int64]string)

	rows, err := s.dbc().QueryContext(ctx, `
		SELECT user_id, device_hostname, exit_node_tag
		  FROM device_exit_node_prefs
		 WHERE device_hostname <> '' AND device_hostname IS NOT NULL
	`)
	if err != nil {
		return nil, fmt.Errorf("rename-migrator: list prefs: %w", err)
	}
	defer rows.Close()

	type prefRow struct {
		userID      int64
		hostname    string
		exitNodeTag string
	}
	var prefs []prefRow
	for rows.Next() {
		var p prefRow
		if err := rows.Scan(&p.userID, &p.hostname, &p.exitNodeTag); err != nil {
			continue
		}
		if p.userID == 0 || p.hostname == "" {
			continue
		}
		prefs = append(prefs, p)
	}
	rows.Close()

	var changes []RenameMigration
	now := time.Now()

	for _, p := range prefs {
		username := usernameCache[p.userID]
		if username == "" {
			_ = s.dbc().QueryRowContext(ctx,
				`SELECT username FROM portal_users WHERE id = $1`, p.userID,
			).Scan(&username)
			usernameCache[p.userID] = username
		}

		cand := s.classifyRenamePref(ctx, p.userID, username, p.hostname, p.exitNodeTag)
		verdict := ClassifyRenameMigration(cand)

		switch verdict {
		case ClassificationNormal:
			// No-op. The (user, hostname) pair is
			// in node_owner_map as expected.
			continue
		case ClassificationRename:
			newHost := cand.CandidatesForRename[0]
			m := RenameMigration{
				Classification: ClassificationRename,
				UserID:         p.userID,
				Username:       username,
				OldHostname:    p.hostname,
				NewHostname:    newHost,
				ExitNodeTag:    p.exitNodeTag,
			}
			if !live {
				log.Printf("preferred-reconciler: DRY-RUN would MIGRATE pref user=%s old=%s new=%s tag=%s",
					username, p.hostname, newHost, p.exitNodeTag)
				m.Applied = false
			} else {
				if err := s.applyRenameMigration(ctx, p.userID, p.hostname, newHost, p.exitNodeTag); err != nil {
					log.Printf("preferred-reconciler: MIGRATE pref user=%s old=%s new=%s FAILED: %v",
						username, p.hostname, newHost, err)
					continue
				}
				m.Applied = true
				_ = db.AppendAuditLogWithTarget(s.dbc(), 0, "system",
					"preferred_reconcile_migrated",
					fmt.Sprintf("MIGRATE pref user=%s old_hostname=%s new_hostname=%s tag=%s reason=hostname-rename",
						username, p.hostname, newHost, p.exitNodeTag),
					"headscale_node", p.hostname)
				log.Printf("preferred-reconciler: MIGRATE pref user=%s old=%s new=%s tag=%s",
					username, p.hostname, newHost, p.exitNodeTag)
				if shouldAlert(p.hostname, "rename", now) {
					n.SendAlert(fmt.Sprintf("♻️ preferred-exit reconciled (B231)\nMIGRATE user=%s old_hostname=%s new_hostname=%s tag=%s\nreason: hostname-rename detected (the operator renamed the device in headscale; B231 moved the pref to the new hostname)\nrollback via SQL: re-INSERT the old (user, hostname) row manually if you need to undo",
						username, p.hostname, newHost, p.exitNodeTag))
				}
			}
			changes = append(changes, m)
		case ClassificationAmbiguous:
			m := RenameMigration{
				Classification:   ClassificationAmbiguous,
				UserID:           p.userID,
				Username:         username,
				OldHostname:      p.hostname,
				ExitNodeTag:      p.exitNodeTag,
				AmbiguousMatches: cand.CandidatesForRename,
			}
			// No auto-apply for Ambiguous. Just
			// log + alert + audit. The operator
			// picks the right one manually on
			// /my/devices (or via SQL).
			_ = db.AppendAuditLogWithTarget(s.dbc(), 0, "system",
				"preferred_reconcile_ambiguous",
				fmt.Sprintf("AMBIGUOUS pref user=%s hostname=%s tag=%s candidates=%v (operator has multiple devices with the same tag — needs manual review)",
					username, p.hostname, p.exitNodeTag, cand.CandidatesForRename),
				"headscale_node", p.hostname)
			log.Printf("preferred-reconciler: AMBIGUOUS pref user=%s hostname=%s tag=%s candidates=%v — needs manual review",
				username, p.hostname, p.exitNodeTag, cand.CandidatesForRename)
			if shouldAlert(p.hostname, "ambiguous", now) {
				n.SendAlert(fmt.Sprintf("♻️ preferred-exit reconciled (B231)\nAMBIGUOUS pref user=%s hostname=%s tag=%s\ncandidates: %v\nreason: multiple devices have the same preferred tag. B231 cannot auto-pick. Resolve on /my/devices or via SQL UPDATE.\nnoop (audit row written)",
					username, p.hostname, p.exitNodeTag, cand.CandidatesForRename))
			}
			changes = append(changes, m)
		case ClassificationOrphan:
			m := RenameMigration{
				Classification: ClassificationOrphan,
				UserID:         p.userID,
				Username:       username,
				OldHostname:    p.hostname,
				ExitNodeTag:    p.exitNodeTag,
			}
			// No auto-DELETE. Just log + alert +
			// audit. The operator runs the manual
			// DELETE via SQL.
			_ = db.AppendAuditLogWithTarget(s.dbc(), 0, "system",
				"preferred_reconcile_orphan_candidate",
				fmt.Sprintf("ORPHAN pref user=%s hostname=%s tag=%s (no device with this hostname in node_owner_map, no tag match either — likely deleted device)\nManual cleanup: DELETE FROM device_exit_node_prefs WHERE user_id=%d AND device_hostname='%s'",
					username, p.hostname, p.exitNodeTag, p.userID, p.hostname),
				"headscale_node", p.hostname)
			log.Printf("preferred-reconciler: ORPHAN pref user=%s hostname=%s tag=%s — manual cleanup required",
				username, p.hostname, p.exitNodeTag)
			if shouldAlert(p.hostname, "orphan", now) {
				n.SendAlert(fmt.Sprintf("♻️ preferred-exit reconciled (B231)\nORPHAN pref user=%s hostname=%s tag=%s\nreason: device with this hostname is not in headscale and no tag match found. The device was likely permanently deleted.\nManual cleanup: DELETE FROM device_exit_node_prefs WHERE user_id=%d AND device_hostname='%s'\nIf the device will be re-registered with the same tag, leave the row in place; B229 will pick it up on the next tick.",
					username, p.hostname, p.exitNodeTag, p.userID, p.hostname))
			}
			changes = append(changes, m)
		}
	}

	return changes, nil
}

// classifyRenamePref does the two DB lookups the pure
// ClassifyRenameMigration needs. We factor this out so
// the pure function stays testable without a DB
// (mirrors the B229 collectDevicePrefState +
// PlanDevicePrefChange split).
//
// Lookups:
//   1. node_owner_map.tagged_by_user_id = $1 AND
//      LOWER(hostname) = LOWER($2) → IsRowPresent
//   2. node_owner_map.tagged_by_user_id = $1 AND tag
//      = $3 AND LOWER(hostname) <> LOWER($2) → the
//      rename candidates (hostnames that share the
//      same tag under the same user)
//
// We LOWER both sides so the rename-detection
// tolerates case drift in node_owner_map (the B77
// autoupdater lowercases the tag, but the hostname
// is stored verbatim from headscale's givenName —
// which can be "CYBORG" or "cyborg" depending on
// what the client sent at register time).
//
// 2026-09-03: v1.5.2 (B231).
func (s *Service) classifyRenamePref(ctx context.Context, userID int64, username, hostname, exitNodeTag string) RenameOrphanCandidate {
	cand := RenameOrphanCandidate{
		UserID:      userID,
		Username:    username,
		Hostname:    hostname,
		ExitNodeTag: exitNodeTag,
	}
	// Lookup 1: does (user, hostname) exist in
	// node_owner_map? (case-insensitive on hostname
	// — B175 backfill uses LOWER(hostname) for
	// comparison; we follow suit.)
	var isPresent int
	_ = s.dbc().QueryRowContext(ctx, `
		SELECT COUNT(*) FROM node_owner_map
		 WHERE tagged_by_user_id = $1 AND LOWER(hostname) = LOWER($2)
	`, userID, hostname).Scan(&isPresent)
	cand.HasNodeOwnerMapRow = isPresent > 0
	if cand.HasNodeOwnerMapRow {
		// No need to look for rename candidates.
		return cand
	}
	// Lookup 2: rename candidates — same user +
	// same tag, different hostname. We need this
	// even when exitNodeTag is the legacy form
	// (e.g. "tag:exit-emilia" from a pre-B188
	// row), because the rename detection works on
	// tag equality. The migration's UPSERT will
	// NormalizeExitNodeTag the new hostname's tag
	// via the B188 contract.
	rows, err := s.dbc().QueryContext(ctx, `
		SELECT hostname FROM node_owner_map
		 WHERE tagged_by_user_id = $1
		   AND tag = $2
		   AND LOWER(hostname) <> LOWER($3)
		 ORDER BY hostname
	`, userID, exitNodeTag, hostname)
	if err != nil {
		log.Printf("preferred-reconciler: rename-candidates for %s/%s: %v", username, hostname, err)
		return cand
	}
	defer rows.Close()
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err == nil && h != "" {
			cand.CandidatesForRename = append(cand.CandidatesForRename, h)
		}
	}
	return cand
}

// applyRenameMigration is the live-mode write. The
// rename is a 2-step: UPSERT the new (user, hostname)
// row + DELETE the old row. We use a transaction so
// the rename is atomic (no half-state where the
// device is on neither hostname).
//
// We also re-enable via_enabled=1 (the B229 catch-up
// behavior — the operator's pin intent is preserved).
//
// 2026-09-03: v1.5.2 (B231).
func (s *Service) applyRenameMigration(ctx context.Context, userID int64, oldHost, newHost, exitNodeTag string) error {
	conn := s.dbc()
	// Resolve the canonical tag for the NEW
	// hostname (might differ from exitNodeTag if
	// exitNodeTag is the legacy form). If we can't
	// resolve, fall back to the stored tag
	// (defensive — the rename is still semantically
	// correct, the canonical upgrade will happen on
	// the next main-reconcile tick).
	canonical := exitNodeTag
	if tag, err := db.NormalizeExitNodeTag(conn, newHost); err == nil && tag != "" {
		canonical = tag
	}
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return fmt.Errorf("rename-migrator: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	// UPSERT new (user, new_hostname). ON CONFLICT
	// (the (user, new_hostname) PK is already taken
	// by another row — should never happen because
	// we classified Normal otherwise, but
	// defensive): DO NOTHING. The migration is
	// effectively a no-op in that case (and the
	// DELETE below still cleans up the old row).
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO device_exit_node_prefs
			(user_id, device_hostname, exit_node_tag, set_by_user_id, updated_at, via_enabled)
		VALUES ($1, $2, $3, 0, $4, 1)
		ON CONFLICT(user_id, device_hostname) DO UPDATE
			SET exit_node_tag = EXCLUDED.exit_node_tag,
			    via_enabled    = EXCLUDED.via_enabled,
			    updated_at     = EXCLUDED.updated_at
	`, userID, newHost, canonical, time.Now().Unix()); err != nil {
		return fmt.Errorf("rename-migrator: upsert new: %w", err)
	}
	// DELETE old (user, old_hostname). If the old
	// row doesn't exist (race with the operator
	// doing the same manual rename), the DELETE
	// is a no-op.
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM device_exit_node_prefs
		 WHERE user_id = $1 AND device_hostname = $2
	`, userID, oldHost); err != nil {
		return fmt.Errorf("rename-migrator: delete old: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("rename-migrator: commit: %w", err)
	}
	return nil
}
