// internal/headscale/policy_snapshot_b295.go — B295 (2026-09-23).
//
// What to do when the LIVE policy cannot be read.
//
// Live on `aro` the prefix-assignment card answered «состояние политики неизвестно:
// … connect: connection refused» and the sync logged
//
//	acl-drift: cannot read the live policy to decide whether a re-apply is needed
//	(…) — applying unconditionally
//
// and then WROTE the policy — on a `policy.mode: file` host a write means
// `systemctl restart headscale` (0.29 re-reads the file only at startup), i.e. the
// read failure caused another restart, which caused the next read failure. The
// observation was feeding the outage.
//
// It does not have to: skygate already stores every policy it applied in
// `acl_snapshots`. The last row with applied_success=1 IS the document headscale
// was serving (skygate wrote it), so it answers the same question with no API at
// all:
//
//	generated == last applied snapshot  → nothing to write; a re-apply would only
//	                                      restart headscale for no reason
//	generated != last applied snapshot  → the rules/assignment table really did
//	                                      change; apply (the live read may simply
//	                                      have missed the write)
//	no snapshot at all (fresh install)  → keep the pre-B295 behaviour: apply, and
//	                                      say that the decision was made blind
package headscale

import (
	"strconv"
	"strings"
)

// SnapshotVerdict is the answer to "should we write the policy?" when headscale
// could not be asked.
type SnapshotVerdict struct {
	// Version is the acl_snapshots version that was compared (0 = none).
	Version int
	// Apply is true when the generated policy differs from that snapshot (or no
	// snapshot exists), i.e. a write is justified.
	Apply bool
	// InSync is true when the generated policy is equivalent to the last applied
	// one — the "nothing to do" answer.
	InSync bool
	// Reason is the log/banner line for the operator.
	Reason string
}

// CompareWithSnapshot decides what to do from the last applied snapshot.
//
// Pure: `snapshot` is the stored document (may be empty), `version` its
// acl_snapshots version (0 for none), `generated` the policy skygate would apply
// right now.
func CompareWithSnapshot(generated string, version int, snapshot string) SnapshotVerdict {
	if version <= 0 || strings.TrimSpace(snapshot) == "" {
		return SnapshotVerdict{
			Version: version,
			Apply:   true,
			Reason:  "headscale did not answer and no applied snapshot is stored — applying blind (a fresh install, or the snapshot table was never written)",
		}
	}
	same, err := PolicyEquivalent(generated, snapshot)
	if err != nil {
		return SnapshotVerdict{
			Version: version,
			Apply:   true,
			Reason:  "headscale did not answer and the stored snapshot could not be compared (" + err.Error() + ") — applying",
		}
	}
	if same {
		return SnapshotVerdict{
			Version: version,
			InSync:  true,
			Reason:  "headscale did not answer, but the generated policy equals the last APPLIED snapshot (v" + strconv.Itoa(version) + ") — nothing to write, so headscale is not restarted",
		}
	}
	return SnapshotVerdict{
		Version: version,
		Apply:   true,
		Reason:  "headscale did not answer and the generated policy DIFFERS from the last applied snapshot (v" + strconv.Itoa(version) + ") — applying",
	}
}

// SnapshotInSyncHint is the operator-facing suffix for a page that could not read
// the live policy but CAN prove the last applied document matches.
func SnapshotInSyncHint(version int) string {
	return "compared with the last APPLIED snapshot v" + strconv.Itoa(version) + " (headscale did not answer)"
}
