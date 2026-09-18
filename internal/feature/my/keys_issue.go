// internal/feature/my/keys_issue.go — shared helpers for issuing pre-auth
// keys.
//
// R7 (2026-09-18 audit)
// ---------------------
// Three issuance paths created the key in headscale and then wrote the
// local mirror row with the error only LOGGED:
//
//	internal/feature/my/preauth.go:130    (POST /my/preauth)
//	internal/feature/my/keys.go:445       (POST /my/keys/{id}/reissue)
//	internal/feature/my/devices.go:1487   (POST /my/devices/{id}/reregister)
//
// The user therefore saw a success page with a working key while
// skygate had no record of it. Everything downstream keys off that row:
//
//   - the node-ownership backfill matches a registering node to its
//     issuer by preauth_keys.headscale_preauth_id (strategy A), so the
//     device was never attributed to its owner and sat on "pending"
//     forever (the B175 class);
//   - /my/keys never listed it, and it could never be expired or
//     cleaned up from the UI.
//
// The fix makes the persistence failure a HARD failure and compensates:
// the just-created headscale key is expired, so we never hand out a key
// that skygate cannot account for. Callers render an inline error
// instead of the key page.
package my

import (
	"errors"
	"log"

	"skygate/internal/db"
	"skygate/internal/headscale"
)

// errNilPreauthKey guards against a caller passing a nil key (which would
// otherwise panic on key.Key). Returning an error keeps the failure
// inside the normal flow.
var errNilPreauthKey = errors.New("preauth key is nil")

// osLabel maps the OS code submitted by the "Add device" form to the
// label user/preauth_result.html prints.
//
// The template has always referenced {{.OSLabel}} in three places, but no
// handler ever set it — the page rendered "instructions for <no value>".
// Unknown/empty codes fall back to a neutral label rather than echoing an
// empty value back into the page.
func osLabel(os string) string {
	switch os {
	case "android":
		return "Android"
	case "ios":
		return "iOS"
	case "linux":
		return "Linux"
	case "macos":
		return "macOS"
	case "windows":
		return "Windows"
	case "":
		return "your device"
	default:
		return os
	}
}

// persistIssuedKey writes the local preauth_keys mirror row for a key that
// headscale has just created, and expires the headscale key if the write
// fails.
//
// Returns the new local row id on success. On failure the returned error
// is the DB error; the caller must NOT show the key to the user.
//
// op is a short prefix for the log lines ("web.my.preauth" etc.) so the
// three call sites stay distinguishable in the container logs.
//
// Note on the audit row: callers still log an AppendAuditLog failure and
// continue. That is deliberate — once preauth_keys holds the key the
// issuance is sound, and failing the whole operation because an audit row
// could not be written would be worse than a missing audit entry. The
// KEY row is the one that must not be lost silently.
func (s *Service) persistIssuedKey(
	userID, hsUserID int64,
	key *headscale.PreauthKey,
	expiresAt int64,
	op string,
) (int64, error) {
	if key == nil {
		return 0, errNilPreauthKey
	}

	newID, err := db.InsertPreauthKey(s.dbc(), userID, key.Key, expiresAt, key.ID)
	if err == nil {
		return newID, nil
	}

	log.Printf("%s: InsertPreauthKey userID=%d hsKeyID=%s err=%v — "+
		"expiring the headscale key as compensation", op, userID, key.ID, err)

	// Compensating action: the key exists in headscale but skygate cannot
	// account for it, so revoke it. Best-effort — if this ALSO fails the
	// key stays live and untracked, which is worth a louder log line
	// because only the operator can clean it up.
	if key.ID != "" {
		if eerr := s.Backend.HSForUserFn(userID).ExpirePreauthKey(hsUserID, key.ID); eerr != nil {
			log.Printf("%s: COMPENSATION FAILED for hsKeyID=%s err=%v — "+
				"the key is live in headscale but has no local row; "+
				"revoke it manually via headscale preauthkeys expire",
				op, key.ID, eerr)
		}
	}
	return 0, err
}
