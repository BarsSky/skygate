// Package certsync — crypto helper functions for cert
// validation. Split into a separate file (certsync_crypto.go)
// so the main certsync.go stays focused on the S3 +
// scheduler surface; the crypto bits are only used by
// validateCertKeyPair and can be unit-tested in isolation.

package certsync

import (
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/x509"
)

// parsePKCS1PrivateKey parses a PKCS#1 RSA private key
// (the "BEGIN RSA PRIVATE KEY" PEM block). Returns the
// parsed key on success, an error otherwise. Used by
// validateCertKeyPair to check that the uploaded key
// matches the uploaded cert.
func parsePKCS1PrivateKey(der []byte) (interface{}, error) {
	return x509.ParsePKCS1PrivateKey(der)
}

// parsePKCS8PrivateKey parses a PKCS#8 private key (the
// "BEGIN PRIVATE KEY" PEM block, which is the modern
// format used by `openssl genpkey` and most modern
// tooling). Handles RSA, ECDSA, and Ed25519 keys
// uniformly.
func parsePKCS8PrivateKey(der []byte) (interface{}, error) {
	key, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, err
	}
	return key, nil
}

// parseECPrivateKey parses an SEC1 EC private key (the
// "BEGIN EC PRIVATE KEY" PEM block). Returns the parsed
// key on success, an error otherwise.
func parseECPrivateKey(der []byte) (interface{}, error) {
	return x509.ParseECPrivateKey(der)
}

// publicKeyFromPrivate returns the public key
// corresponding to a private key. Returns an error for
// unsupported key types (the parser accepts PKCS#8 for
// RSA/ECDSA/Ed25519 but the public-key extract only
// supports RSA + ECDSA — the cert sync surface cares
// about LE-style certs which are RSA or ECDSA, not
// Ed25519).
func publicKeyFromPrivate(priv interface{}) (interface{}, error) {
	switch k := priv.(type) {
	case *rsa.PrivateKey:
		return &k.PublicKey, nil
	case *ecdsa.PrivateKey:
		return &k.PublicKey, nil
	default:
		return nil, x509.ErrUnsupportedAlgorithm
	}
}
