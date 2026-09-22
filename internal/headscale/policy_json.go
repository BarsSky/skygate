// internal/headscale/policy_json.go — one normaliser for the policy the
// headscale API hands back.
//
// WHY THIS EXISTS (B282, 2026-09-22)
// ----------------------------------
// GET /api/v1/policy is served in three different shapes depending on
// the headscale version and on how the operator's policy file is
// written:
//
//	(a) a JSON OBJECT — `{"grants":[…]}` — what `headscale policy get`
//	    prints and what 0.29.x returns;
//	(b) a STRINGIFIED policy — `{"policy":"{\"grants\":[…]}"}` — the
//	    legacy wire shape, and still what the live native `aro` host
//	    answers: GetACL returned the quoted string, so
//	    /admin/headscale/acl answered
//	    `500 list acl: unmarshal policy: json: cannot unmarshal string
//	    into Go value of type admin.ACLView`;
//	(c) HuJSON — the operator's actual /etc/headscale/policy.hujson
//	    with `//` comments and trailing commas, which plain
//	    encoding/json rejects.
//
// Consumers that call json.Unmarshal on the raw reply therefore work on
// one shape and break on the others. PolicyJSON is the single entry
// point that unquotes (b), standardises (c) and passes (a) through, so
// every reader gets strict JSON bytes. It is the exported twin of the
// unexported unquotePolicyIfStringified helper the tag path (B251) and
// PolicyEquivalent (B276) already use.
package headscale

import (
	"fmt"

	"github.com/tailscale/hujson"
)

// PolicyJSON normalises a policy document (object, stringified object or
// HuJSON) into strict JSON bytes ready for encoding/json.
//
// An error means the payload was neither a JSON object/array, nor a JSON
// string literal wrapping one, nor valid HuJSON — the caller should
// surface it verbatim rather than guessing (a policy skygate cannot
// parse is a louder problem than a missing page).
func PolicyJSON(policy string) ([]byte, error) {
	raw, err := unquotePolicyIfStringified([]byte(policy))
	if err != nil {
		return nil, fmt.Errorf("unquote stringified policy (got %d bytes): %w", len(policy), err)
	}
	std, err := hujson.Standardize(raw)
	if err != nil {
		return nil, fmt.Errorf("standardize HuJSON (got %d bytes): %w", len(policy), err)
	}
	return std, nil
}
