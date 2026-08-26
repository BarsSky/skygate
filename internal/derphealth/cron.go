package derphealth

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
)

// CronInterval is how often StartCron runs ProbeAll. 5 min
// is a balance: latency changes (especially on mobile
// networks) are visible within ~5 min, but the dashboard
// table doesn't grow unbounded (one row per DERP, not one
// per probe). The cron always upserts, never inserts.
const CronInterval = 5 * time.Minute

// startCronOnce guards StartCron so a second concurrent
// invocation (e.g. main.go + the manual /admin/derp/probe
// handler) doesn't spawn a second background goroutine.
var startCronOnce sync.Once

// StartCron launches the periodic probe loop. It's a no-op
// after the first successful call (so reloading the
// service / re-running main.go's wiring doesn't double
// the cron). The loop runs until ctx is cancelled.
//
// Intended usage from main.go:
//
//	if err := derphealth.StartCron(ctx, db, &http.Client{Timeout: 10*time.Second}); err != nil {
//	    log.Fatalf("derp cron: %v", err)
//	}
func StartCron(ctx context.Context, db *sql.DB, httpClient *http.Client) error {
	if db == nil {
		return fmt.Errorf("derphealth.StartCron: db is nil")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	var firstErr error
	startCronOnce.Do(func() {
		go func() {
			// Run once immediately so the dashboard has
			// data the first time it's loaded.
			runOnce(ctx, db, httpClient)
			t := time.NewTicker(CronInterval)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					runOnce(ctx, db, httpClient)
				}
			}
		}()
	})
	return firstErr
}

// runOnce does one probe cycle. Wrapped in a recover so a
// panic in one goroutine doesn't kill the cron.
func runOnce(ctx context.Context, db *sql.DB, httpClient *http.Client) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("derphealth: probe cycle panic: %v", r)
		}
	}()
	derps, err := FetchAllDERPs(ctx, db, httpClient)
	if err != nil && len(derps) == 0 {
		log.Printf("derphealth: fetch all: %v", err)
		return
	}
	persist := PersistToDB(db)
	results := ProbeAll(ctx, derps, httpClient, persist)
	ok, bad := 0, 0
	for _, r := range results {
		if r.Healthy {
			ok++
		} else {
			bad++
		}
	}
	log.Printf("derphealth: probed %d DERPs (ok=%d, bad=%d)", len(results), ok, bad)
}

// RunOnceNow is the manual probe entry point used by
// `skygate derp-probe` and by the /admin/derp/probe POST
// handler. Unlike StartCron, this is synchronous and
// returns the results so the caller can render them.
func RunOnceNow(ctx context.Context, db *sql.DB, httpClient *http.Client) ([]ProbeResult, error) {
	if db == nil {
		return nil, fmt.Errorf("derphealth.RunOnceNow: db is nil")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	derps, err := FetchAllDERPs(ctx, db, httpClient)
	if err != nil && len(derps) == 0 {
		return nil, err
	}
	return ProbeAll(ctx, derps, httpClient, PersistToDB(db)), nil
}
