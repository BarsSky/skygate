// B304 (v1.5.69) — accessors for Service.Keys.
//
// The key store stopped being immutable-at-boot: /admin/oidc can now move it to
// another directory (KeyStore.Reload) and can repair a store that failed to
// initialise at boot (B270's degrade path leaves Keys == nil, so the provider
// answers 503 until someone fixes the directory — which previously meant editing
// the env and restarting).
//
// Both operations swap the *KeyStore pointer, so every reader goes through
// KeysRef() under the same lock instead of touching the field directly. The
// store itself is already internally synchronised for in-place key changes;
// this guard covers the pointer swap.
package oidc

import "sync"

// keysMu guards Service.Keys. Package-level on purpose: exactly one OIDC service
// exists per process, and the alternative (a mutex field) would need every
// Service literal — including the ones in tests — to be initialised with it.
var keysMu sync.RWMutex

// KeysRef returns the current key store (nil when it never initialised).
// Nil-safe: callers keep using KeyStore's own nil-safe methods.
func (s *Service) KeysRef() *KeyStore {
	if s == nil {
		return nil
	}
	keysMu.RLock()
	defer keysMu.RUnlock()
	return s.Keys
}

// SetKeys installs a key store, clearing KeyStoreErr — the runtime repair path
// used by /admin/oidc when the boot-time directory could not be created or when
// the operator points key_dir somewhere new.
func (s *Service) SetKeys(ks *KeyStore) {
	if s == nil {
		return
	}
	keysMu.Lock()
	s.Keys = ks
	if ks != nil && ks.Ready() {
		s.KeyStoreErr = ""
	}
	keysMu.Unlock()
}

// KeyStoreErrRef reports why the key store is unavailable ("" when it is fine).
func (s *Service) KeyStoreErrRef() string {
	if s == nil {
		return ""
	}
	keysMu.RLock()
	defer keysMu.RUnlock()
	return s.KeyStoreErr
}
