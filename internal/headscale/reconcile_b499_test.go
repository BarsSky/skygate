package headscale

// reconcile_b499_test.go — B499 (2026-11-19), the D half.
//
// The live history: skygate wrote this audit row hourly for weeks —
//
//	{"outcome":"duplicate_name","old_hs_id":1,"candidate_hs_id":"88",
//	 "action":"operator_must_pick","hint":"... users delete -i <oidc-id> --force ..."}
//
// Three defects in one row. `<oidc-id>` is not an id, so the operator had to
// reconstruct it from `headscale users list`. The row repeated because nothing ever
// decided the case. And the case was usually decidable: the portal table already says
// which user is the real one, and when it says nothing, the OLDEST user is the original
// (the bootstrap admin was id=1; the OIDC duplicates were 86/88/90).
//
// What must NOT come back is the pre-B243 rule ("the last entry in the map wins"),
// which relinked skyadmin from id=1 to the duplicate and orphaned seven devices. So the
// table below pins the boundary: decide when exactly one answer is defensible, refuse
// when two portal users point at different duplicates.

import (
	"strings"
	"testing"
)

func b499Users(ids ...string) []HSUser {
	out := make([]HSUser, 0, len(ids))
	for _, id := range ids {
		out = append(out, HSUser{ID: id, Name: "skyadmin"})
	}
	return out
}

func TestB499_PickCanonicalTable(t *testing.T) {
	cases := []struct {
		name       string
		candidates []HSUser
		referenced map[int64]bool
		wantID     string
		wantExtras []int64
		wantOK     bool
	}{
		{
			name:       "not a duplicate",
			candidates: b499Users("1"),
			referenced: map[int64]bool{1: true},
			wantOK:     false,
		},
		{
			// the portal table already names the canonical user → it wins, the other
			// is the duplicate, and the hint can name its id.
			name:       "one candidate referenced",
			candidates: b499Users("1", "88"),
			referenced: map[int64]bool{1: true},
			wantID:     "1",
			wantExtras: []int64{88},
			wantOK:     true,
		},
		{
			// nobody referenced: the OLDEST is the original (bootstrap admin vs the
			// OIDC duplicate). This is the exact opposite of the pre-B243 bug.
			name:       "no candidate referenced picks the oldest",
			candidates: b499Users("88", "1", "90"), // deliberately unsorted input
			referenced: map[int64]bool{},
			wantID:     "1",
			wantExtras: []int64{88, 90},
			wantOK:     true,
		},
		{
			// two portal users point at two different duplicates: any choice breaks
			// one of them, so there is no safe answer and the operator must pick.
			name:       "two candidates referenced is ambiguous",
			candidates: b499Users("1", "88"),
			referenced: map[int64]bool{1: true, 88: true},
			wantOK:     false,
		},
		{
			// a candidate id that does not parse cannot be canonical.
			name:       "unparseable oldest id",
			candidates: b499Users("", "88"),
			referenced: map[int64]bool{},
			wantOK:     false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			chosen, extras, ok := b499PickCanonical(tc.candidates, tc.referenced)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (chosen=%+v extras=%v)", ok, tc.wantOK, chosen, extras)
			}
			if !ok {
				return
			}
			if chosen.ID != tc.wantID {
				t.Fatalf("chosen = %q, want %q", chosen.ID, tc.wantID)
			}
			if len(extras) != len(tc.wantExtras) {
				t.Fatalf("extras = %v, want %v", extras, tc.wantExtras)
			}
			for i := range extras {
				if extras[i] != tc.wantExtras[i] {
					t.Fatalf("extras = %v, want %v", extras, tc.wantExtras)
				}
			}
		})
	}
}

// TestB499_DeleteHintNamesRealIDs is the smallest but most consequential fix: the
// operator's audit row said `<oidc-id>`, so acting on it required another command. The
// hint must carry the ids the function just computed.
func TestB499_DeleteHintNamesRealIDs(t *testing.T) {
	hint := b499DeleteHint([]int64{88, 90})
	for _, want := range []string{"-i 88", "-i 90", "--force"} {
		if !strings.Contains(hint, want) {
			t.Fatalf("hint %q is missing %q", hint, want)
		}
	}
	if strings.Contains(hint, "<") {
		t.Fatalf("hint still carries a placeholder: %q", hint)
	}
	if b499DeleteHint(nil) != "" {
		t.Fatalf("no extras must mean no hint")
	}
}

// TestB499_BuildUserIndexKeepsEveryCandidate — byName is deduplicated by construction,
// which is exactly why the decision needs the other view.
func TestB499_BuildUserIndexKeepsEveryCandidate(t *testing.T) {
	_, all := buildUserIndex([]HSUser{
		{ID: "88", Name: "SkyAdmin"},
		{ID: "1", Name: "skyadmin"},
		{ID: "5", Name: "michail"},
	})
	if got := len(all["skyadmin"]); got != 2 {
		t.Fatalf("candidates for skyadmin = %d, want 2 (case-insensitive)", got)
	}
	if all["skyadmin"][0].ID != "1" {
		t.Fatalf("candidates are not ordered by numeric id: %+v", all["skyadmin"])
	}
	if got := len(all["michail"]); got != 1 {
		t.Fatalf("michail candidates = %d, want 1", got)
	}
}

// TestB499_StaleLinkWithUnreferencedDuplicateIsDecidedNotRefused is the end-to-end
// shape that produced the hourly row: the portal row points at a headscale id that no
// longer exists, headscale holds the original and a duplicate, and the portal table
// references neither. The reconciler must CHOOSE (the oldest) instead of returning
// duplicate_name — and, with no database handle to write through, it must fail loudly
// rather than panic or silently "resolve".
func TestB499_StaleLinkWithUnreferencedDuplicateIsDecidedNotRefused(t *testing.T) {
	byName, all := buildUserIndex(b499Users("1", "88"))
	outcome, newID, msg := reconcileOne(
		t.Context(),
		nil, // no db: the decision is reached, the write cannot be
		nil,
		byName,
		map[string]bool{"skyadmin": true},
		all,
		map[int64]bool{}, // nobody references either
		1, "skyadmin", 777,
	)
	if outcome == ReconcileDuplicateName {
		t.Fatalf("the unambiguous duplicate was refused: %s", msg)
	}
	if outcome != ReconcileError {
		t.Fatalf("outcome = %v, want an error from the missing db handle (it must not pretend to resolve)", outcome)
	}
	if newID != 777 {
		t.Fatalf("newID = %d, want the old id untouched on failure", newID)
	}
	if !strings.Contains(msg, "database") {
		t.Fatalf("the failure message must name the missing database handle, got %q", msg)
	}
}
