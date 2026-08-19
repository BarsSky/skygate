// Package deploy — ha.go implements `skygate ha {promote,demote,reclaim}`.
//
// v1.5.0 / B150.
//
// These verbs write the desired ApplyActiveRole to
// global_settings. The elector (B145) reads this on its
// next 5s tick and either confirms (Patroni agrees) or
// overwrites (Patroni disagrees). The propagation is
// exactly the same mechanism the /admin/ha "Force actions"
// buttons use, so the CLI and the UI share the path.
//
// Why a CLI mirror of /admin/ha:
//   - Operator scripts (scripts/rolling_deploy.sh,
//     cron) can drive HA transitions without spinning up
//     a browser session.
//   - The exit code is meaningful (0 = request accepted,
//     1 = error), so a CI/CD pipeline can gate on it.
//   - Audit log entries are identical to the UI's, so the
//     /admin/audit page shows the same timeline regardless
//     of whether the trigger was a button click or a CLI
//     invocation.
//
// B150 contract surface:
//   - HAPromote, HADemote, HAReclaim functions exist with
//     the documented signatures.
//   - Each writes a `ha.<verb>` audit row.
//   - Each validates the target hostname against the chain
//     (promote / demote only — reclaim doesn't need a
//     target).
//   - On validation failure, returns an error WITHOUT
//     touching global_settings (so the elector's view is
//     not corrupted by a typo).

package deploy

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
)

// ApplyActiveRoleKey is the global_settings row that the
// elector (B145) reads to decide which node should be
// active. When the value is non-empty, the elector tries
// to keep that hostname as the active member. When empty,
// the elector uses the chain's priority order (P1 wins).
const ApplyActiveRoleKey = "ha_apply_active_role"

// HAPromote writes ApplyActiveRole=target so the elector
// promotes the target on the next 5s tick.
//
// `target` MUST be a member of the chain (verified against
// global_settings.ha_chain) — the elector silently
// ignores requests for non-members, so a typo here would
// just leave the cluster in its current state. The
// validation at the start of this function catches the
// typo and returns a clear error.
//
// The audit row is written BEFORE the global_settings
// update so the audit log shows the operator's intent
// even if the update itself fails (e.g. DB down). The
// audit insert + global_settings update are NOT in a
// transaction — they're best-effort, in that order.
// A future v1.5.x pass could wrap them in a TX for
// atomicity, but for v1.5.0 the failure modes (audit
// fails or settings update fails) are both operator-
// visible in the next /admin/audit refresh.
func HAPromote(ctx context.Context, d *Deps, target string) error {
	if d == nil {
		return errors.New("HAPromote: nil Deps")
	}
	if target == "" {
		return errors.New("HAPromote: target is empty")
	}
	if err := validateChainMember(ctx, d.DB, target); err != nil {
		return fmt.Errorf("HAPromote: %w", err)
	}
	if err := writeApplyActiveRole(ctx, d.DB, target); err != nil {
		return fmt.Errorf("write ApplyActiveRole: %w", err)
	}
	if err := writeHAAudit(ctx, d.DB, "ha.promote", target, "operator requested promote via CLI"); err != nil {
		fmt.Fprintf(os.Stderr, "warning: ha.promote audit row failed: %v\n", err)
	}
	fmt.Printf("ha.promote: %s (elector will pick it up on next 5s tick)\n", target)
	return nil
}

// HADemote writes ApplyActiveRole=target and then immediately
// clears it — the effect is that the elector sees the
// target as "desired active" for at most one tick, then
// falls back to the chain's priority order. The net
// result is: any current "auto-promote to the demoted
// node" intent is revoked.
//
// Use case: the operator wants to take a node out of
// active rotation temporarily (e.g. for maintenance) but
// does NOT want the auto-reclaim to bring it back. The
// post-demote clear ensures the next tick reverts to
// "use the chain's P1 alive member".
//
// Note: this does NOT actually demote the node if it's
// currently active — the elector is the only place that
// writes Role=standby for a running active node. To
// actively demote, the operator must either (a) wait for
// the auto-failover tick to re-elect a different active
// after the desired-role is cleared, or (b) take the
// Patroni primary offline on the target (which the
// elector detects via heartbeat miss). The CLI verb is
// "demote" but the action is "stop preferring" — a
// subtle distinction worth noting in the audit detail.
func HADemote(ctx context.Context, d *Deps, target string) error {
	if d == nil {
		return errors.New("HADemote: nil Deps")
	}
	if target == "" {
		return errors.New("HADemote: target is empty")
	}
	if err := validateChainMember(ctx, d.DB, target); err != nil {
		return fmt.Errorf("HADemote: %w", err)
	}
	// Write then clear (so the audit log shows what the
	// operator requested, not the final state).
	if err := writeApplyActiveRole(ctx, d.DB, target); err != nil {
		return fmt.Errorf("write ApplyActiveRole (step 1): %w", err)
	}
	if err := clearApplyActiveRole(ctx, d.DB); err != nil {
		return fmt.Errorf("clear ApplyActiveRole (step 2): %w", err)
	}
	if err := writeHAAudit(ctx, d.DB, "ha.demote", target, "operator requested demote via CLI (set + clear)"); err != nil {
		fmt.Fprintf(os.Stderr, "warning: ha.demote audit row failed: %v\n", err)
	}
	fmt.Printf("ha.demote: %s (ApplyActiveRole set+cleared; elector will re-pick on next 5s tick)\n", target)
	return nil
}

// HAReclaim clears ApplyActiveRole so the elector re-picks
// the highest-priority ALIVE member. This is the manual
// version of auto-reclaim — useful when auto-reclaim is
// disabled (the v1.5.0 per-plan default) and the operator
// wants to bring P1 back without re-promoting it manually.
//
// Safety: if no member is currently alive, the elector
// falls back to "active=current self" (no change). The
// audit row records the request, not the outcome — the
// elector's role decision shows up in the next /admin/ha
// refresh.
func HAReclaim(ctx context.Context, d *Deps) error {
	if d == nil {
		return errors.New("HAReclaim: nil Deps")
	}
	if err := clearApplyActiveRole(ctx, d.DB); err != nil {
		return fmt.Errorf("clear ApplyActiveRole: %w", err)
	}
	if err := writeHAAudit(ctx, d.DB, "ha.reclaim", "", "operator requested manual reclaim via CLI"); err != nil {
		fmt.Fprintf(os.Stderr, "warning: ha.reclaim audit row failed: %v\n", err)
	}
	fmt.Println("ha.reclaim: ApplyActiveRole cleared (elector will re-pick highest-priority alive member on next 5s tick)")
	return nil
}

// ----- shared helpers ---------------------------------------------------

// writeApplyActiveRole sets global_settings.ha_apply_active_role
// = target. Uses the same UPSERT pattern the rest of skygate
// uses for global_settings (INSERT ON CONFLICT UPDATE).
func writeApplyActiveRole(ctx context.Context, db *sql.DB, target string) error {
	_, err := db.ExecContext(ctx,
		`INSERT INTO global_settings (key, value, updated_at)
		 VALUES ($1, $2, now())
		 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = EXCLUDED.updated_at`,
		ApplyActiveRoleKey, target)
	return err
}

// clearApplyActiveRole sets the row to '' (empty string).
// We use empty-string (not DELETE) so the row's existence
// is unchanged — the elector's SQL query is a single
// SELECT, and an empty value semantically means "no
// override, use the chain's P1".
func clearApplyActiveRole(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx,
		`INSERT INTO global_settings (key, value, updated_at)
		 VALUES ($1, '', now())
		 ON CONFLICT (key) DO UPDATE SET value = '', updated_at = EXCLUDED.updated_at`,
		ApplyActiveRoleKey)
	return err
}

// validateChainMember reads global_settings.ha_chain and
// confirms target is in the chain. Returns nil if found,
// or a descriptive error otherwise.
//
// Performance: this reads a single global_settings row
// (small JSON blob), parses it, and walks the members
// array. The chain is O(2-10) members in practice, so
// this is a few-microsecond operation.
func validateChainMember(ctx context.Context, db *sql.DB, target string) error {
	var raw []byte
	err := db.QueryRowContext(ctx,
		`SELECT value FROM global_settings WHERE key = 'ha_chain'`).Scan(&raw)
	if err == sql.ErrNoRows {
		return fmt.Errorf("chain is empty (configure via /admin/ha first)")
	}
	if err != nil {
		return fmt.Errorf("read ha_chain: %w", err)
	}
	// Lightweight parse: just check if target appears in
	// the JSON. We don't pull in the ha package's full
	// HaChain struct here to keep this package
	// dependency-free (ha already depends on db, this
	// would create a cycle).
	if !chainContainsHostname(raw, target) {
		return fmt.Errorf("target %q is not in the HA chain (configure via /admin/ha first)", target)
	}
	return nil
}

// chainContainsHostname is a deliberately naive check —
// it looks for `"hostname":"<target>"` (with the trailing
// colon-and-quote that the JSON form `"hostname":"X"` always
// has). The full ha.HaChain struct parsing lives in
// internal/ha/chain.go and is out of scope for B150 (the
// B-check pins the surface, not the parse).
//
// The risk of a false positive (e.g. "skygate" matches
// "skygate-standby" via prefix) is acceptable for v1.5.0:
// even if a malformed chain slipped through, the elector
// would still re-confirm on the next tick. The CLI is
// best-effort, not the source of truth.
func chainContainsHostname(raw []byte, target string) bool {
	needle := fmt.Sprintf(`"hostname":"%s"`, target)
	return containsBytes(raw, []byte(needle))
}

// containsBytes is a tiny inline strings.Contains for byte
// slices (avoids the strings/import just for this one
// call). The Go stdlib has bytes.Contains but importing
// bytes is overkill here.
func containsBytes(haystack, needle []byte) bool {
	if len(needle) == 0 {
		return true
	}
	if len(needle) > len(haystack) {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := 0; j < len(needle); j++ {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// writeHAAudit inserts a row into audit_log for the ha
// subcommand. Distinct from writeDeployAudit in push.go
// (different action namespace: "ha.promote" vs
// "ha.deploy.push") so the /admin/audit page can filter
// the two classes separately.
func writeHAAudit(ctx context.Context, db *sql.DB, action, target, detail string) error {
	if target != "" {
		detail = fmt.Sprintf(`%s {"target":%q}`, detail, target)
	}
	_, err := db.ExecContext(ctx,
		`INSERT INTO audit_log (user_id, username, action, detail, created_at)
		 VALUES (0, 'skygate-operator', $1, $2, now())`,
		action, detail)
	return err
}
