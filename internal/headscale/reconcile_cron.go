// Package headscale — reconcile_cron.go (B237.18).
//
// Periodic reconciliation cron. Same pattern as
// derphealth/cron.go (B189):
//
//   - StartReconcileCron(ctx, db, hs, interval) launches
//     a background goroutine that runs ReconcileUsers
//     every `interval`.
//   - sync.Once guards against double-spawn (main.go + the
//     /admin/headscale page might both try to start it).
//   - runOnce wraps one cycle in a recover() so a panic
//     in one cycle doesn't kill the cron.
//   - RunOnceNow is the manual trigger (`skygate
//     headscale-users-reconcile` + /admin/headscale
//     POST /admin/headscale/reconcile).
//
// The cron is opt-in via main.go's config check on
// cfg.ReconcileHeadscaleUsersEnabled (default true so the
// operator gets reconciliation by default; false in
// air-gapped installs where headscale is unreachable).
//
// Default interval: 1 hour. The 24h-N case (user
// deleted yesterday, reconciled next day) is
// acceptable; the per-row impact (a rule that points
// at a non-existent headscale user) is small
// (Tailscale silently treats the user as missing,
// the rule doesn't fire, the operator notices when
// the device "doesn't get the right exit node").
// Lowering the interval to 15 min would just spam
// the headscale API without changing the operator's
// detect window materially.

package headscale

import (
	"context"
	"database/sql"
	"log"
	"sync"
	"time"
)

// DefaultReconcileInterval is the default cadence
// when StartReconcileCron is called with interval=0.
const DefaultReconcileInterval = 1 * time.Hour

// startReconcileCronOnce guards StartReconcileCron so
// a second concurrent call (e.g. main.go + the
// /admin/headscale page) doesn't spawn a second
// background goroutine. Same pattern as
// derphealth.startCronOnce.
var startReconcileCronOnce sync.Once

// StartReconcileCron launches the periodic
// reconciliation loop. It's a no-op after the first
// successful call (so reloading the service / re-running
// main.go's wiring doesn't double the cron). The loop
// runs until ctx is cancelled.
//
// Intended usage from main.go:
//
//	if err := headscale.StartReconcileCron(ctx, db, hs, cfg.HeadscaleUserReconcileInterval); err != nil {
//	    log.Fatalf("reconcile cron: %v", err)
//	}
//
// Parameters:
//   - ctx: parent context (cancels the goroutine on
//     graceful shutdown)
//   - db: *sql.DB for portal_users + audit_log
//   - hs: *Client (the headscale HTTP client)
//   - interval: 0 = DefaultReconcileInterval (1h);
//     otherwise the operator's override
func StartReconcileCron(ctx context.Context, db *sql.DB, hs *Client, interval time.Duration) error {
	if db == nil {
		return errReconcileNilDB
	}
	if hs == nil {
		return errReconcileNilHS
	}
	if interval <= 0 {
		interval = DefaultReconcileInterval
	}
	startReconcileCronOnce.Do(func() {
		go func() {
			// Run once immediately so the
			// first reconciliation happens
			// at startup (catches the
			// pre-cron-install drift
			// immediately on the first
			// deploy). Then steady-state
			// ticks.
			runReconcileCycle(ctx, db, hs)
			t := time.NewTicker(interval)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					runReconcileCycle(ctx, db, hs)
				}
			}
		}()
	})
	log.Printf("reconcile: cron enabled (interval=%s, headscale_users_reconcile will run on startup + every %s)",
		interval, interval)
	return nil
}

// runReconcileCycle does one reconciliation cycle. Wrapped
// in a recover so a panic in one cycle doesn't kill the
// cron (the next tick will try again; the panic message
// goes to the skygate log for the operator to read).
func runReconcileCycle(ctx context.Context, db *sql.DB, hs *Client) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("reconcile: cycle panic: %v", r)
		}
	}()
	if _, err := ReconcileUsers(ctx, db, hs); err != nil {
		// The function already wrote a "head is
		// unreachable" audit row + logged the
		// error. Nothing more to do here.
		log.Printf("reconcile: cycle error (see audit row + prior log): %v", err)
	}
}

// RunOnceNow is the manual trigger used by
// `skygate headscale-users-reconcile` and by the
// /admin/headscale page's "Reconcile now" button.
// Unlike StartReconcileCron, this is synchronous and
// returns the result so the caller can render it.
func RunOnceNow(ctx context.Context, db *sql.DB, hs *Client) (ReconcileResult, error) {
	if db == nil {
		return ReconcileResult{}, errReconcileNilDB
	}
	if hs == nil {
		return ReconcileResult{}, errReconcileNilHS
	}
	return ReconcileUsers(ctx, db, hs)
}

// Sentinels so the caller can distinguish "reconcile
// skipped because of nil dep" from "reconcile ran
// and found nothing to do" from "reconcile ran and
// found real orphans". The /admin/headscale page
// renders the distinction in its flash banner.
var (
	errReconcileNilDB = reconcileErr("reconcile: db is nil")
	errReconcileNilHS = reconcileErr("reconcile: headscale client is nil")
)

// reconcileErr is a typed error so the /admin/headscale
// page can use errors.Is to distinguish "skipped"
// from "ran and found something".
type reconcileErr string

func (e reconcileErr) Error() string { return string(e) }
