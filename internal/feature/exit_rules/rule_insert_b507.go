package exit_rules

// rule_insert_b507.go — what ONE insert attempt actually DID.
//
// THE DEFECT (measured on the live installation, 2026-11-20): the API answered
//
//	{"added": N, "duplicates": 0, "errors": [], "ids": [...]}
//
// for a batch that was a RETRY of an earlier batch. `insertRuleUnique` returned
// `(true, existingID)` for a row that already existed — and, worse, also for the
// B480 case where the target was already pinned to a DIFFERENT relay — so the
// caller counted every idempotent re-send as a fresh insert and `dupCount` was
// declared `:= 0` and never incremented. `agent_brief.go` promises the assistant
// the opposite ("re-sending the same batch is safe"), so the API contradicted its
// own package documentation, and in the B480 case it also LIED about which relay
// would carry the traffic: the assistant believed it had pinned `emilia` while the
// prefix stayed on the old relay.
//
// The fix is not a counter: it is an OUTCOME. A bool cannot distinguish "written",
// "already there" and "refused because somebody else owns the target", and every
// caller needs a different one of those three answers.

// ruleInsertOutcome is the result of ONE insert attempt.
type ruleInsertOutcome int

const (
	// ruleInsertCreated — a genuinely new row was written.
	ruleInsertCreated ruleInsertOutcome = iota
	// ruleInsertExisting — the identical rule was already stored (the target,
	// device and relay all matched). Re-sending a batch lands here: idempotent,
	// nothing written, and the caller must NOT count it as new.
	ruleInsertExisting
	// ruleInsertOtherRelay — the target is already reserved on this device by a
	// DIFFERENT relay (B480). Nothing was written: moving a reservation is the
	// operator's request flow (B440), never a side effect of adding a duplicate.
	// A caller that reports this as success is telling the operator that a relay
	// they named will carry traffic it will not carry.
	ruleInsertOtherRelay
	// ruleInsertFailed — a database error; the row is not there.
	ruleInsertFailed
)

// String is used in API responses and log lines, so it reads as the operator's
// language rather than an integer.
func (o ruleInsertOutcome) String() string {
	switch o {
	case ruleInsertCreated:
		return "created"
	case ruleInsertExisting:
		return "existing"
	case ruleInsertOtherRelay:
		return "other-relay"
	case ruleInsertFailed:
		return "failed"
	}
	return "unknown"
}

// ruleInsertResult is the outcome plus what it refers to.
type ruleInsertResult struct {
	Outcome ruleInsertOutcome
	// ID is the stored row's id: the new one for ruleInsertCreated, the existing
	// one for ruleInsertExisting and for ruleInsertOtherRelay (the row that HOLDS
	// the reservation — which is exactly the row the caller was not told about).
	ID int
	// OtherRelay names the relay that currently reserves the target, for
	// ruleInsertOtherRelay only.
	OtherRelay string
}
