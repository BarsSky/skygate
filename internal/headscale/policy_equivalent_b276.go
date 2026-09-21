// B276 — policy equivalence: is the policy headscale is serving the one skygate
// would generate right now?
//
// Why this exists. skygate owns two halves of the same decision and they are
// written by different code paths at different times:
//
//   - the DATA plane: which relay advertises which prefix (`SyncAdvertisedRoutes`
//     / `StaggeredSync`, recomputed every few minutes) — and headscale serves a
//     subnet prefix from exactly ONE relay, the primary;
//   - the CONTROL plane: the ACL, whose per-CIDR grant pins that prefix to a named
//     relay (`via`), regenerated only when a rule, user or device changes.
//
// B275 made the pin follow the assignment table, which is right — but nothing
// regenerated the ACL when the table changed. Live on the reference host the ACL
// was applied at 18:19, the table moved 28 Cloudflare/Google prefixes to the other
// relay after 19:26, and every pin for those prefixes then named a relay that was
// no longer the primary: clients silently dropped the routes (the operator's
// device lost its whole Cloudflare set with no log line anywhere). Comparing the
// live policy with the generated one is the cheapest way to make that class
// self-reporting, so the sync paths and the admin page can say "the ACL is stale"
// instead of leaving the operator to diff two files by hand.
//
// The comparison is semantic, not textual: headscale returns the policy as an
// object (or, on older versions, as a stringified blob) with its own key order and
// indentation, while the generator emits compact JSON. Both sides go through the
// same normalisation the B251 tag path uses (unquote if stringified → hujson
// .Standardize → json.Unmarshal) and are compared as decoded values.
package headscale

import (
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/tailscale/hujson"
)

// PolicyEquivalent reports whether two policy documents describe the same policy.
//
// A nil error with equivalent=false means both sides parsed and differ. An error
// means at least one side could not be parsed — the caller should treat that as
// "cannot tell" and report it rather than claiming drift (a policy skygate cannot
// parse is a different, louder problem, and guessing here would produce a
// spurious "stale" verdict and a pointless apply).
func PolicyEquivalent(a, b string) (bool, error) {
	am, err := decodePolicyValue(a)
	if err != nil {
		return false, fmt.Errorf("policy A: %w", err)
	}
	bm, err := decodePolicyValue(b)
	if err != nil {
		return false, fmt.Errorf("policy B: %w", err)
	}
	return reflect.DeepEqual(am, bm), nil
}

// decodePolicyValue normalises one policy document (stringified or HuJSON, with
// or without comments/trailing commas) into a generic decoded value.
func decodePolicyValue(policy string) (interface{}, error) {
	raw, err := unquotePolicyIfStringified([]byte(policy))
	if err != nil {
		return nil, fmt.Errorf("unquote stringified policy (got %d bytes): %w", len(policy), err)
	}
	raw, err = hujson.Standardize(raw)
	if err != nil {
		return nil, fmt.Errorf("standardize HuJSON (got %d bytes): %w", len(policy), err)
	}
	var v interface{}
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("decode policy (got %d bytes): %w", len(policy), err)
	}
	return v, nil
}
