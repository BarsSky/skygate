// 2026-07-15: Этап 14 v18 (v0.12.0) — per-user headscale
// client routing.
//
// Before v0.12.0, every handler in the codebase called
// `a.HS.Do(...)` (or one of a.HS's typed methods) against the
// single headscale client built at startup from
// HEADSCALE_URL + HEADSCALE_API_KEY. v0.12.0 adds per-user
// overrides via portal_users.headscale_url +
// portal_users.headscale_api_key_enc, so different users
// can live on different headscale control planes.
//
// The router is centralised in this file so the lookup +
// cache logic is in one place. Handlers call:
//
//   a.HSForUser(c.UserID).ListAllNodes()  // user's own plane
//   a.HSGlobal().ListAllNodes()          // explicit "global"
//
// HSForUser reads portal_users.headscale_url for the given
// id; if it's empty, it returns the default a.HS (same
// instance every time, no extra alloc). If non-empty, it
// builds (or fetches from the cache) a *headscale.Client
// for the (url, key) pair. The cache is a simple map keyed
// by url; we don't expect to see hundreds of unique planes
// in practice, so a sync.Mutex-protected map is plenty.
//
// Fail-open on per-user client build: if the stored
// ciphertext is corrupt (ErrSecretCiphertextCorrupt), we
// log and fall through to the global client. The user sees
// the global plane's data with a degraded experience
// (their own rules might not show), but the page doesn't
// 500. The admin can fix the row in /admin/users.

package handlers

import (
	"log"

	"skygate/internal/db"
	"skygate/internal/headscale"
)

// HSForUser returns the headscale client to use for a given
// portal user id. If the user has a per-user override, the
// returned client is built from the (url, key) pair in
// portal_users. Otherwise the global default is returned
// (the same instance that a.HS points at; no extra alloc).
//
// Errors in reading or decrypting the per-user row fall
// through to the global client. The error is logged so the
// operator sees the degraded state in `docker logs skygate`
// (a corrupt key is operator-fixable, not user-fixable).
func (a *App) HSForUser(userID int64) *headscale.Client {
	if a.SecretKeyHex == "" {
		// v0.12.0 wasn't fully wired (SKYGATE_SECRET_KEY
		// not set) — fall through to the global client.
		// We don't want to 500 the whole portal just
		// because encryption isn't configured.
		return a.HSGlobal()
	}
	cfg, err := db.GetUserHeadscaleConfig(a.DB, userID, a.SecretKeyHex)
	if err != nil {
		// ErrNoUserControlPlane is the common case
		// (most users don't have an override). Don't
		// log — the helper is on the request path.
		if err != db.ErrNoUserControlPlane {
			log.Printf("hs-for-user: userID=%d err=%v (falling back to global)", userID, err)
		}
		return a.HSGlobal()
	}
	return a.clientFor(cfg.URL, cfg.APIKey)
}

// HSGlobal returns the headscale client built at startup
// from HEADSCALE_URL / HEADSCALE_API_KEY. Use this in
// cross-user handlers (admin/devices, admin/exit-rules/*)
// where the request is not on behalf of a single user, and
// in any code that needs to talk to the operator's primary
// control plane regardless of per-user overrides.
//
// The function is a one-liner now, but having it on App
// means handlers that today use a.HS directly can be
// trivially audited ("every HS access goes through
// HSGlobal or HSForUser — direct a.HS access is a bug").
func (a *App) HSGlobal() *headscale.Client {
	return a.hs
}

// PlaneURLForUser returns the headscale_url the given
// portal user is on ("" = the global default plane).
// The bot path uses this to scope acl.GenerateACLForPlane
// to the right identities — headscale rejects unknown
// identities in tagOwners, so the per-plane ACL
// generation must know which plane to build for.
//
// 2026-07-16: v0.13.0 — paired with HSForUser so the
// bot can do "issue preauth on the right plane AND push
// ACL to the right plane" in the same command.
func (a *App) PlaneURLForUser(userID int64) string {
	cfg, err := db.GetUserHeadscaleConfig(a.DB, userID, a.SecretKeyHex)
	if err != nil {
		// ErrNoUserControlPlane is the common case (most
		// users don't have an override). Treat as the
		// global default plane.
		return ""
	}
	return cfg.URL
}

// clientFor returns a cached or freshly-built headscale
// client for the given (url, key). The cache is a
// sync.Mutex-protected map keyed by url. We deliberately
// don't bother with LRU — the typical install has 1-5
// planes and they live for the lifetime of the process.
func (a *App) clientFor(url, key string) *headscale.Client {
	a.hsCacheMu.Lock()
	defer a.hsCacheMu.Unlock()
	if c, ok := a.hsCache[url]; ok {
		// Cache hit: verify the key still matches
		// (an admin could rotate the per-user key
		// via the UI; the cached client would be
		// using the old key).
		if c.ApiKeyForCache() == key {
			return c
		}
		// Key rotated: drop the cache entry and
		// fall through to rebuild.
		delete(a.hsCache, url)
	}
	c := headscale.New(url, key)
	a.hsCache[url] = c
	return c
}

// InvalidateHSCache clears the per-url client cache. Used
// by /admin/users when an admin updates a per-user control
// plane — the cached client for that url is now stale.
//
// /admin/control-planes also calls this when the admin
// rotates a per-user api_key via the Test / Save flow.
func (a *App) InvalidateHSCache(url string) {
	a.hsCacheMu.Lock()
	defer a.hsCacheMu.Unlock()
	if url == "" {
		a.hsCache = map[string]*headscale.Client{}
		return
	}
	delete(a.hsCache, url)
}

// InitHSForUserState wires the per-user routing state on
// App. Call from main.go after App is constructed; tests
// call it via New(). The init is small but lives in this
// file (rather than handlers.go) so the per-user router
// is self-contained.
func (a *App) InitHSForUserState() {
	a.hsCacheMu.Lock()
	if a.hsCache == nil {
		a.hsCache = map[string]*headscale.Client{}
	}
	a.hsCacheMu.Unlock()
}
