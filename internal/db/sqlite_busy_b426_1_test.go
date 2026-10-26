// internal/db/sqlite_busy_b426_1_test.go — B426.1 (2026-10-26).
//
// The alert transport writes from TWO goroutines by design: `insertAlert` on the send path
// and `go pruneAlerts` on every send. openSQLiteWith gives a production file DSN
// foreign_keys(1) + journal_mode(WAL) + busy_timeout(2000), but it TRUSTS any DSN that
// already carries `_pragma=` — and B419's fast per-test handle supplied its own, so the
// whole production set was replaced and the timeout was gone.
//
// Measured on this workstation before the fix, 4 goroutines x 100 insert+prune rounds on
// a fast per-test database:
//
//	without busy_timeout      inserts_ok=163  busy=237   ← 59% of the writes LOST
//	with busy_timeout(2000)   inserts_ok=400  busy=0
//
// Why an insert failure matters even though the message is still delivered: insertAlert
// returns no id, so SendAlertEvent returns 0 for an alert the operator DID receive, and 0
// is exactly what callers read as "not delivered" (the B391 disk monitor retries on it).
// That is how a green tree failed CI under load only: TestB391_DedupSuppressesRepeats
// InsideTheWindow died on "the first alert of a key must be sent" and passed 5/5 alone.
package db

import (
	"sync"
	"sync/atomic"
	"testing"
)

func TestB4261ConcurrentWritesOnAFastTestDBAreNotLost(t *testing.T) {
	d, err := OpenFastSQLiteForTest(t.TempDir() + "/conc.db")
	if err != nil {
		t.Skipf("sqlite dialect unavailable: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if _, err := d.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY AUTOINCREMENT, body TEXT)`); err != nil {
		t.Fatalf("create: %v", err)
	}

	const writers, rounds = 4, 50
	var lost, landed int64
	var firstErr atomic.Value
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				// The two writers the alert path really has, in the same interleaving.
				if _, err := d.Exec(`INSERT INTO t(body) VALUES ('x')`); err != nil {
					firstErr.CompareAndSwap(nil, err.Error())
					atomic.AddInt64(&lost, 1)
					continue
				}
				atomic.AddInt64(&landed, 1)
				_, _ = d.Exec(`DELETE FROM t WHERE id NOT IN (SELECT id FROM t ORDER BY id DESC LIMIT 5)`)
			}
		}()
	}
	wg.Wait()

	want := int64(writers * rounds)
	if lost != 0 {
		msg, _ := firstErr.Load().(string)
		t.Fatalf("B426.1: %d of %d concurrent writes were LOST (first error: %s) — a delivered alert would return id=0", lost, want, msg)
	}
	if landed != want {
		t.Fatalf("B426.1: only %d of %d writes landed", landed, want)
	}
}
