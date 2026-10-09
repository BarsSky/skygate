// B371 — an identical cert-fetch failure must not fill the journal.
//
// The live error this pins (reference deployment, 2026-10-09):
//
//	certsync: get .version: Get "http://s3..amazonaws.com/skygate-backups/?location=":
//	no such host
//
// printed every 30 seconds — 2880 identical lines a day — because the S3 endpoint
// and region were both empty (see cmd/skygate/main_helpers.go, certSyncS3Config).
// The noise is what made a configuration error read as "certsync is hanging".
package certsync

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

// TestB371_AnIdenticalFailureIsReportedOnceAnHour is the suppression window: the
// first occurrence is always reported, a different failure is never suppressed, and
// the same one comes back after the window so a long outage stays visible.
func TestB371_AnIdenticalFailureIsReportedOnceAnHour(t *testing.T) {
	// This test owns the package-level state; reset it so a previous test cannot
	// make this one pass or fail for the wrong reason.
	certFetchErrMu.Lock()
	certFetchErrLast = ""
	certFetchErrAt = time.Time{}
	certFetchErrMu.Unlock()

	nonsense := errors.New(`Get "http://s3..amazonaws.com/skygate-backups/?location=": no such host`)

	if !fetchFailureIsNew(nonsense) {
		t.Fatal("the FIRST occurrence of a failure must always be reported")
	}
	if fetchFailureIsNew(nonsense) {
		t.Error("the same failure was reported again inside the window — this is the 2880-lines-a-day defect")
	}
	different := fmt.Errorf("dial tcp 127.0.0.1:9000: connect: connection refused")
	if !fetchFailureIsNew(different) {
		t.Error("a DIFFERENT failure must never be suppressed")
	}
	// After the window the same message is reported again.
	certFetchErrMu.Lock()
	certFetchErrAt = time.Now().Add(-2 * certFetchErrRepeatAfter)
	certFetchErrMu.Unlock()
	if !fetchFailureIsNew(different) {
		t.Error("the same failure must reappear after the window, so a long outage stays visible")
	}
	// nil is never a failure.
	if fetchFailureIsNew(nil) {
		t.Error("nil must not count as a new failure")
	}
	// Leave the package state clean for whatever runs next.
	certFetchErrMu.Lock()
	certFetchErrLast = ""
	certFetchErrAt = time.Time{}
	certFetchErrMu.Unlock()
}

// TestB371_TheWindowIsAnHour guards the constant: a shorter one would still be
// noise and a longer one would hide a second, unrelated outage.
func TestB371_TheWindowIsAnHour(t *testing.T) {
	if certFetchErrRepeatAfter != time.Hour {
		t.Errorf("certFetchErrRepeatAfter = %v, want 1h (the B318 window)", certFetchErrRepeatAfter)
	}
}
