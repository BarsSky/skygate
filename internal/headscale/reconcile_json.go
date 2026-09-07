// Package headscale — reconcile_json.go.
//
// Thin wrapper around encoding/json.Marshal that
// keeps the import surface of reconcile.go tight.
// The caller is marshalJSON in reconcile.go, which
// uses this to serialize ReconcileResult before
// writing the summary audit_log row.
//
// We use MarshalIndent-free Marshal (compact
// JSON) because the audit_log.detail column has
// no size limit but the operator's /admin/audit
// page reads it back into a textarea — compact
// is easier to read for short payloads.

package headscale

import "encoding/json"

func marshalJSONImpl(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
