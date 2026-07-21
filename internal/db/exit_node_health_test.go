// Tests for the exit_node_health + exit_node_state_changes helpers
// in internal/db/exit_node_health.go.
//
// 2026-07-15: v0.13.0. The pattern follows exit_servers_test.go
// (same author, same shape): openTestDB → seed → exercise the
// helper → assert the typed view matches what we wrote.
//
// The most interesting behaviours to pin down are:
//   * Round-trip: UpsertExitNodeHealth then GetExitNodeHealth
//     returns the same struct.
//   * State-change dedup: two consecutive RecordExitNodeStateChange
//     calls produce two rows; ListPendingExitNodeStateChanges
//     returns both, MarkExitNodeStateChangeAlerted drops one off
//     the pending list.
//   * Delete: removing a snapshot doesn't touch the transition
//     log (operator audit trail survives node deletion).
//   * Count: CountHealthyExitNodes only counts healthy=1 rows.

package db

import (
	"database/sql"
	"testing"
	"time"
)

// seedExitNodeHealth inserts one row via UpsertExitNodeHealth so
// the test exercises the real write path (not raw SQL). Returns
// the ExitNodeHealth struct so callers can tweak fields before
// upserting again — the loop pattern ("insert, then update a
// field, then upsert, then check") is the natural way to test
// "did the second upsert win?" in the same test.
func seedExitNodeHealth(t *testing.T, d *sql.DB, h ExitNodeHealth) {
	t.Helper()
	if h.LastCheckAt.IsZero() {
		h.LastCheckAt = time.Now().UTC()
	}
	if err := UpsertExitNodeHealth(d, h); err != nil {
		t.Fatalf("seed ExitNodeHealth(%q): %v", h.NodeID, err)
	}
}

// --- UpsertExitNodeHealth + GetExitNodeHealth round-trip ---

func TestUpsertAndGetExitNodeHealth_RoundTrip(t *testing.T) {
	d := openTestDB(t)
	// Truncate to seconds: the INTEGER unix columns lose
	// sub-second precision, so comparing with
	// time.Time.Equal would fail by microseconds. Truncating
	// here keeps the assertion meaningful ("the round trip
	// preserved the timestamp") without flakiness.
	now := time.Now().UTC().Truncate(time.Second)
	want := ExitNodeHealth{
		NodeID:             "3",
		Hostname:           "emilia",
		Online:             true,
		LastSeen:           "2026-07-15T12:34:56Z",
		AdvertisedRoutesOK: true,
		HasExitTag:         true,
		State:              "online",
		Healthy:            true,
		LastCheckAt:        now,
		LastStateChangeAt:  now,
		ConsecutiveFailures: 0,
	}
	seedExitNodeHealth(t, d, want)

	got, err := GetExitNodeHealth(d, "3")
	if err != nil {
		t.Fatalf("GetExitNodeHealth: %v", err)
	}
	if got.NodeID != want.NodeID || got.Hostname != want.Hostname {
		t.Errorf("identity mismatch: got %+v, want %+v", got, want)
	}
	if got.Online != want.Online {
		t.Errorf("Online = %v, want %v", got.Online, want.Online)
	}
	if got.AdvertisedRoutesOK != want.AdvertisedRoutesOK {
		t.Errorf("AdvertisedRoutesOK = %v, want %v", got.AdvertisedRoutesOK, want.AdvertisedRoutesOK)
	}
	if got.HasExitTag != want.HasExitTag {
		t.Errorf("HasExitTag = %v, want %v", got.HasExitTag, want.HasExitTag)
	}
	if got.State != want.State {
		t.Errorf("State = %q, want %q", got.State, want.State)
	}
	if got.Healthy != want.Healthy {
		t.Errorf("Healthy = %v, want %v", got.Healthy, want.Healthy)
	}
	if got.LastSeen != want.LastSeen {
		t.Errorf("LastSeen = %q, want %q", got.LastSeen, want.LastSeen)
	}
	// last_seen parse is best-effort; the RFC3339 value above
	// must parse cleanly. If it doesn't, the helper is broken
	// in a way the round-trip test should catch.
	if got.LastSeenParsed.IsZero() {
		t.Errorf("LastSeenParsed is zero; expected non-zero for RFC3339 %q", got.LastSeen)
	}
	if !got.LastCheckAt.Equal(want.LastCheckAt) {
		t.Errorf("LastCheckAt = %v, want %v", got.LastCheckAt, want.LastCheckAt)
	}
	if !got.LastStateChangeAt.Equal(want.LastStateChangeAt) {
		t.Errorf("LastStateChangeAt = %v, want %v", got.LastStateChangeAt, want.LastStateChangeAt)
	}
}

func TestUpsertExitNodeHealth_ReplacesExistingRow(t *testing.T) {
	d := openTestDB(t)
	now := time.Now().UTC()
	// First write: online.
	seedExitNodeHealth(t, d, ExitNodeHealth{
		NodeID: "3", Hostname: "emilia", Online: true,
		State: "online", Healthy: true, LastCheckAt: now,
	})
	// Second write: same node_id, now offline.
	seedExitNodeHealth(t, d, ExitNodeHealth{
		NodeID: "3", Hostname: "emilia", Online: false,
		State: "offline", Healthy: false, LastCheckAt: now.Add(time.Minute),
	})

	got, err := GetExitNodeHealth(d, "3")
	if err != nil {
		t.Fatalf("GetExitNodeHealth: %v", err)
	}
	if got.Online {
		t.Errorf("Online = true, want false (replacement should have won)")
	}
	if got.State != "offline" {
		t.Errorf("State = %q, want 'offline'", got.State)
	}
	if got.Healthy {
		t.Errorf("Healthy = true, want false")
	}
	// And there should still be exactly one row, not two.
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM exit_node_health`).Scan(&n); err != nil {
		t.Fatalf("COUNT: %v", err)
	}
	if n != 1 {
		t.Errorf("row count = %d, want 1 (INSERT OR REPLACE must not duplicate)", n)
	}
}

// --- ListExitNodeHealth ---

func TestListExitNodeHealth_OrderedByHostname(t *testing.T) {
	d := openTestDB(t)
	now := time.Now().UTC()
	seedExitNodeHealth(t, d, ExitNodeHealth{NodeID: "4", Hostname: "sharlotta", State: "online", LastCheckAt: now})
	seedExitNodeHealth(t, d, ExitNodeHealth{NodeID: "3", Hostname: "emilia", State: "online", LastCheckAt: now})
	seedExitNodeHealth(t, d, ExitNodeHealth{NodeID: "11", Hostname: "karolina", State: "offline", LastCheckAt: now})

	got, err := ListExitNodeHealth(d)
	if err != nil {
		t.Fatalf("ListExitNodeHealth: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d rows, want 3", len(got))
	}
	wantOrder := []string{"emilia", "karolina", "sharlotta"}
	for i, h := range got {
		if h.Hostname != wantOrder[i] {
			t.Errorf("row %d: hostname = %q, want %q", i, h.Hostname, wantOrder[i])
		}
	}
}

// --- CountHealthyExitNodes ---

func TestCountHealthyExitNodes_OnlyCountsHealthy(t *testing.T) {
	d := openTestDB(t)
	now := time.Now().UTC()
	seedExitNodeHealth(t, d, ExitNodeHealth{NodeID: "3", Hostname: "emilia", State: "online", Healthy: true, LastCheckAt: now})
	seedExitNodeHealth(t, d, ExitNodeHealth{NodeID: "4", Hostname: "sharlotta", State: "offline", Healthy: false, LastCheckAt: now})
	seedExitNodeHealth(t, d, ExitNodeHealth{NodeID: "11", Hostname: "karolina", State: "degraded", Healthy: false, LastCheckAt: now})

	got, err := CountHealthyExitNodes(d)
	if err != nil {
		t.Fatalf("CountHealthyExitNodes: %v", err)
	}
	if got != 1 {
		t.Errorf("CountHealthyExitNodes = %d, want 1 (only emilia)", got)
	}
}

// --- RecordExitNodeStateChange + ListPending + MarkAlerted ---

func TestRecordExitNodeStateChange_AppearsInPendingList(t *testing.T) {
	d := openTestDB(t)
	// Truncate to seconds — see the round-trip test for the
	// reason.
	now := time.Now().UTC().Truncate(time.Second)
	id, err := RecordExitNodeStateChange(d, ExitNodeStateChange{
		NodeID: "3", Hostname: "emilia",
		FromState: "online", ToState: "offline",
		DetectedAt: now, Note: "first offline",
	})
	if err != nil {
		t.Fatalf("RecordExitNodeStateChange: %v", err)
	}
	if id == 0 {
		t.Errorf("expected non-zero id, got 0")
	}

	pending, err := ListPendingExitNodeStateChanges(d, 10)
	if err != nil {
		t.Fatalf("ListPendingExitNodeStateChanges: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending = %d, want 1", len(pending))
	}
	if pending[0].NodeID != "3" || pending[0].FromState != "online" || pending[0].ToState != "offline" {
		t.Errorf("pending[0] = %+v, want node=3 online→offline", pending[0])
	}
	if !pending[0].DetectedAt.Equal(now) {
		t.Errorf("DetectedAt = %v, want %v", pending[0].DetectedAt, now)
	}
}

func TestMarkExitNodeStateChangeAlerted_RemovesFromPending(t *testing.T) {
	d := openTestDB(t)
	now := time.Now().UTC()
	id1, _ := RecordExitNodeStateChange(d, ExitNodeStateChange{
		NodeID: "3", Hostname: "emilia",
		FromState: "online", ToState: "offline", DetectedAt: now,
	})
	id2, _ := RecordExitNodeStateChange(d, ExitNodeStateChange{
		NodeID: "4", Hostname: "sharlotta",
		FromState: "online", ToState: "offline",
		DetectedAt: now.Add(time.Minute),
	})

	// Mark only the first as alerted.
	if err := MarkExitNodeStateChangeAlerted(d, id1); err != nil {
		t.Fatalf("MarkExitNodeStateChangeAlerted: %v", err)
	}

	pending, err := ListPendingExitNodeStateChanges(d, 10)
	if err != nil {
		t.Fatalf("ListPendingExitNodeStateChanges: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending = %d, want 1 (one still unalerted)", len(pending))
	}
	if pending[0].ID != id2 {
		t.Errorf("pending[0].ID = %d, want %d (the unalerted one)", pending[0].ID, id2)
	}
}

func TestMarkExitNodeStateChangeAlerted_Idempotent(t *testing.T) {
	d := openTestDB(t)
	now := time.Now().UTC()
	id, _ := RecordExitNodeStateChange(d, ExitNodeStateChange{
		NodeID: "3", Hostname: "emilia",
		FromState: "online", ToState: "offline", DetectedAt: now,
	})
	// Call twice. The second one should be a no-op (the
	// WHERE alerted_at = 0 clause matches nothing). The
	// test asserts no error + the row is still alerted.
	if err := MarkExitNodeStateChangeAlerted(d, id); err != nil {
		t.Fatalf("first MarkExitNodeStateChangeAlerted: %v", err)
	}
	if err := MarkExitNodeStateChangeAlerted(d, id); err != nil {
		t.Fatalf("second MarkExitNodeStateChangeAlerted (should be no-op): %v", err)
	}
	pending, _ := ListPendingExitNodeStateChanges(d, 10)
	if len(pending) != 0 {
		t.Errorf("pending = %d, want 0 after double-mark", len(pending))
	}
}

// --- LatestExitNodeState (monitor dedup helper) ---

func TestLatestExitNodeState_EmptyForUnknownNode(t *testing.T) {
	d := openTestDB(t)
	_, _, err := LatestExitNodeState(d, "999")
	if err != sql.ErrNoRows {
		t.Errorf("err = %v, want sql.ErrNoRows", err)
	}
}

func TestLatestExitNodeState_ReturnsMostRecent(t *testing.T) {
	d := openTestDB(t)
	now := time.Now().UTC()
	RecordExitNodeStateChange(d, ExitNodeStateChange{
		NodeID: "3", FromState: "online", ToState: "offline",
		DetectedAt: now,
	})
	RecordExitNodeStateChange(d, ExitNodeStateChange{
		NodeID: "3", FromState: "offline", ToState: "online",
		DetectedAt: now.Add(time.Minute),
	})
	RecordExitNodeStateChange(d, ExitNodeStateChange{
		NodeID: "3", FromState: "online", ToState: "offline",
		DetectedAt: now.Add(2 * time.Minute),
	})
	from, to, err := LatestExitNodeState(d, "3")
	if err != nil {
		t.Fatalf("LatestExitNodeState: %v", err)
	}
	if from != "online" || to != "offline" {
		t.Errorf("got %s→%s, want online→offline (most recent)", from, to)
	}
}

// --- DeleteExitNodeHealth (snapshot only; transitions survive) ---

func TestDeleteExitNodeHealth_OnlyRemovesSnapshot(t *testing.T) {
	d := openTestDB(t)
	now := time.Now().UTC()
	seedExitNodeHealth(t, d, ExitNodeHealth{
		NodeID: "3", Hostname: "emilia", State: "online",
		Healthy: true, LastCheckAt: now,
	})
	RecordExitNodeStateChange(d, ExitNodeStateChange{
		NodeID: "3", Hostname: "emilia",
		FromState: "unknown", ToState: "online", DetectedAt: now,
	})

	if err := DeleteExitNodeHealth(d, "3"); err != nil {
		t.Fatalf("DeleteExitNodeHealth: %v", err)
	}

	// Snapshot gone.
	_, err := GetExitNodeHealth(d, "3")
	if err != sql.ErrNoRows {
		t.Errorf("after delete: GetExitNodeHealth err = %v, want sql.ErrNoRows", err)
	}
	// Transition log still there.
	pending, _ := ListPendingExitNodeStateChanges(d, 10)
	if len(pending) != 1 {
		t.Errorf("after delete: pending = %d, want 1 (transition log must survive)", len(pending))
	}
}
