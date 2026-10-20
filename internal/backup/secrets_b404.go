// B404 (v1.5.147) — the backup credentials are secrets: the
// panel must not hand them back, and they should not sit in
// global_settings as plaintext while the rest of the codebase
// already encrypts this class of value.
//
// Two keys are affected (see config.go):
//
//	backup.password       — SMB / WebDAV password
//	backup.s3_secret_key  — S3 Secret Access Key
//
// Pre-B404 both were written verbatim AND rendered back into the
// /admin/backup form as value="…", so the secret travelled to the
// browser on every page load: a screenshot, a shared screen, the
// browser's own form history or a password manager's "save this
// login?" prompt were all enough to leak it, and the operator had
// no way to see the value without exposing it. A password input
// does not help — the DOM contains the value.
//
// The write-only contract is:
//
//	empty field + no clear flag → keep the stored value
//	empty field + clear flag    → delete it
//	non-empty field             → replace it
//
// At rest the value is stored as "enc:v1:<base64>" using
// db.EncryptForColumn (AES-256-GCM keyed by SKYGATE_SECRET_KEY) —
// the same primitive as ha.dns.password_enc and
// portal_users.headscale_api_key_enc. A row written before this
// block keeps working: an unprefixed value is read as plaintext
// (legacy) and is re-written encrypted the next time the form is
// saved with a key configured. Nothing is decrypted silently: a
// ciphertext row without a usable key raises an error naming the
// key instead of feeding a base64 blob to a mount command.
package backup

import (
	"errors"
	"strings"

	"skygate/internal/db"
)

// SecretCipherPrefix marks one encrypted global_settings value.
// The version tag exists so a future key or algorithm rotation can
// introduce "enc:v2:" and still recognise what it is looking at,
// instead of guessing from the shape of the payload.
const SecretCipherPrefix = "enc:v1:"

// Values of Config.SecretsAtRest. The admin page renders a
// different hint for each, because "stored encrypted" and "stored
// in plaintext" are the same green checkmark to a careless reader.
const (
	SecretsAtRestEmpty     = "empty"
	SecretsAtRestPlaintext = "plaintext"
	SecretsAtRestEncrypted = "encrypted"
	SecretsAtRestMixed     = "mixed"
)

// ErrSecretKeyMissing is returned when a stored secret is
// ciphertext but SKYGATE_SECRET_KEY is not configured — the
// process cannot decrypt it, and pretending the value is empty
// would (a) silently disable the backup destination and (b) make
// the next Save overwrite the ciphertext with nothing.
var ErrSecretKeyMissing = errors.New("backup: the stored secret is encrypted but SKYGATE_SECRET_KEY is not set")

// IsCiphertextSecret reports whether a stored global_settings value
// is an encrypted blob rather than a legacy plaintext value.
func IsCiphertextSecret(stored string) bool {
	return strings.HasPrefix(stored, SecretCipherPrefix)
}

// EncryptSecretOptional encrypts one backup secret. An empty key
// means "no master key configured": the value is returned
// unchanged (plaintext), which is exactly the pre-B404 behaviour
// and keeps backups working on an install without
// SKYGATE_SECRET_KEY. The page says so instead of leaving the
// operator to guess (see Config.SecretsAtRest).
func EncryptSecretOptional(plain, keyHex string) (string, error) {
	if plain == "" {
		return "", nil
	}
	if keyHex == "" {
		return plain, nil
	}
	enc, err := db.EncryptForColumn(plain, keyHex)
	if err != nil {
		return "", err
	}
	return SecretCipherPrefix + enc, nil
}

// DecryptSecret turns a stored value into the plaintext secret.
//
//   - empty                     → "", nil (no secret configured)
//   - legacy plaintext          → the value itself, nil
//   - ciphertext + good key     → the plaintext, nil
//   - ciphertext + no key       → ErrSecretKeyMissing
//   - ciphertext + wrong key    → db.ErrSecretCiphertextCorrupt
//
// The last two are errors on purpose. A caller that treats them as
// "empty" would hand an empty password to mount.cifs and report a
// confusing authentication failure.
func DecryptSecret(stored, keyHex string) (string, error) {
	if stored == "" {
		return "", nil
	}
	if !IsCiphertextSecret(stored) {
		return stored, nil
	}
	if keyHex == "" {
		return "", ErrSecretKeyMissing
	}
	plain, err := db.DecryptForColumn(strings.TrimPrefix(stored, SecretCipherPrefix), keyHex)
	if err != nil {
		return "", err
	}
	return plain, nil
}

// SecretFingerprint is the most of a secret the panel is ever
// allowed to show: the last four characters, so an operator can
// tell "the key that ends in…" apart from another one, and nothing
// else. Anything shorter than 8 characters is fully masked rather
// than half-revealed. Empty in → empty out (the template then
// renders no "saved" hint at all).
func SecretFingerprint(secret string) string {
	if secret == "" {
		return ""
	}
	if len(secret) <= 8 {
		return "••••••••"
	}
	return "••••••••" + secret[len(secret)-4:]
}

// secretsAtRest classifies how the two persisted secrets are
// currently stored. It is computed from the RAW stored values
// (before decryption), because the question the page answers —
// "is this value encrypted on disk?" — cannot be asked of a
// decrypted string.
func secretsAtRest(raw ...string) string {
	var anyEmpty, anyPlain, anyEnc bool
	for _, v := range raw {
		switch {
		case v == "":
			anyEmpty = true
		case IsCiphertextSecret(v):
			anyEnc = true
		default:
			anyPlain = true
		}
	}
	switch {
	case anyPlain && anyEnc:
		return SecretsAtRestMixed
	case anyEnc:
		return SecretsAtRestEncrypted
	case anyPlain:
		return SecretsAtRestPlaintext
	case anyEmpty:
		return SecretsAtRestEmpty
	}
	return SecretsAtRestEmpty
}
