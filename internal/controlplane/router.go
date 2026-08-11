// Package controlplane owns the per-user headscale control
// plane routing. Before v0.12.0 every handler in the codebase
// called `a.HS.Do(...)` against a single headscale client
// built at startup from HEADSCALE_URL + HEADSCALE_API_KEY.
// v0.12.0 added per-user overrides via
// portal_users.headscale_url + portal_users.headscale_api_key_enc,
// so different users can live on different headscale control
// planes.
//
// The router is centralised here so the lookup + cache logic
// is in one place. Handlers call:
//
//   router.ForUser(c.UserID).ListAllNodes()  // user's own plane
//   router.Global().ListAllNodes()          // explicit "global"
//
// ForUser reads portal_users.headscale_url for the given id;
// if it's empty, it returns the default Global() client
// (the same instance every time, no extra alloc). If non-empty,
// it builds (or fetches from the cache) a *headscale.Client
// for the (url, key) pair. The cache is a simple map keyed by
// url; we don't expect to see hundreds of unique planes in
// practice, so a sync.Mutex-protected map is plenty.
//
// Fail-open on per-user client build: if the stored ciphertext
// is corrupt (ErrSecretCiphertextCorrupt), we log and fall
// through to the global client. The user sees the global
// plane's data with a degraded experience (their own rules
// might not show), but the page doesn't http.StatusInternalServerError. The admin can
// fix the row in /admin/users.
//
// refactor-v0.30 Phase D3 (2026-07-29): the Router type
// (and its 5 methods — ForUser, Global, PlaneURLForUser,
// InvalidateCache, Init) was previously a set of methods
// on *App in internal/handlers/app_controlplane.go. The
// move is purely organisational — the router doesn't
// actually need a *App, it just needs db + secretKeyHex +
// globalClient + cache. After Phase D3, *App has a
// `*Router` field and the old methods are 1-line
// delegates to it.
package controlplane

import (
	"database/sql"
	"log"
	"sync"

	"skygate/internal/db"
	"skygate/internal/headscale"
)

// Router is the per-user headscale client cache +
// resolver. One Router per process. Construction:
//
//   r := controlplane.New(db, secretKeyHex, globalClient)
//   r.Init()  // allocates the cache map
//
// Methods:
//   - Global() — returns the default client (the same instance
//                every time; no alloc).
//   - ForUser(uid) — returns the per-user client (or falls
//                    through to Global() on miss / error).
//   - PlaneURLForUser(uid) — the URL of the user's plane
//                            ("" = the global default plane).
//   - InvalidateCache(url) — drop the cache entry for url
//                            (or all entries if url == "").
//
// Thread-safe: the cache is guarded by sync.Mutex.
type Router struct {
	db           *sql.DB
	secretKeyHex string
	globalClient *headscale.Client

	mu    sync.Mutex
	cache map[string]*headscale.Client
}

// New builds a Router. Call Init() before use (the init
// step is split out so the App constructor can wire the
// Router + the App without an order-of-init footgun).
func New(d *sql.DB, secretKeyHex string, global *headscale.Client) *Router {
	return &Router{
		db:           d,
		secretKeyHex: secretKeyHex,
		globalClient: global,
	}
}

// Init allocates the cache map. Idempotent (safe to call
// multiple times). Pre-D3 this was a.HSForUserState.Init();
// kept as a separate method so tests can build a Router
// without booting the full App.
func (r *Router) Init() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cache == nil {
		r.cache = map[string]*headscale.Client{}
	}
}

// Global returns the headscale client built at startup
// from HEADSCALE_URL / HEADSCALE_API_KEY. Use this in
// cross-user handlers (admin/devices, admin/exit-rules/*)
// where the request is not on behalf of a single user,
// and in any code that needs to talk to the operator's
// primary control plane regardless of per-user overrides.
func (r *Router) Global() *headscale.Client {
	return r.globalClient
}

// ForUser returns the headscale client to use for a given
// portal user id. If the user has a per-user override,
// the returned client is built from the (url, key) pair
// in portal_users. Otherwise the global default is
// returned (the same instance that Global() points at;
// no extra alloc).
//
// Errors in reading or decrypting the per-user row fall
// through to the global client. The error is logged so
// the operator sees the degraded state in `docker logs
// skygate` (a corrupt key is operator-fixable, not
// user-fixable).
//
// 2026-07-17: v0.16.7 — userID == 0 is a sentinel for
// "no specific user, use the global default". The
// sidecar.Manager passes 0 when it wants the global
// plane (no per-user override applies). Without this
// short-circuit the DB lookup logs "portal_user not
// found" every 30s.
func (r *Router) ForUser(userID int64) *headscale.Client {
	if userID == 0 {
		return r.Global()
	}
	if r.secretKeyHex == "" {
		// v0.12.0 wasn't fully wired (SKYGATE_SECRET_KEY
		// not set) — fall through to the global client.
		// We don't want to http.StatusInternalServerError the whole portal just
		// because encryption isn't configured.
		return r.Global()
	}
	cfg, err := db.GetUserHeadscaleConfig(r.db, userID, r.secretKeyHex)
	if err != nil {
		// ErrNoUserControlPlane is the common case
		// (most users don't have an override). Don't
		// log — the helper is on the request path.
		if err != db.ErrNoUserControlPlane {
			log.Printf("hs-for-user: userID=%d err=%v (falling back to global)", userID, err)
		}
		return r.Global()
	}
	return r.clientFor(cfg.URL, cfg.APIKey)
}

// PlaneURLForUser returns the headscale_url the given
// portal user is on ("" = the global default plane).
// The bot path uses this to scope acl.GenerateACLForPlane
// to the right identities — headscale rejects unknown
// identities in tagOwners, so the per-plane ACL generation
// must know which plane to build for.
//
// 2026-07-16: v0.13.0 — paired with ForUser so the bot
// can do "issue preauth on the right plane AND push ACL
// to the right plane" in the same command.
func (r *Router) PlaneURLForUser(userID int64) string {
	cfg, err := db.GetUserHeadscaleConfig(r.db, userID, r.secretKeyHex)
	if err != nil {
		// ErrNoUserControlPlane is the common case
		// (most users don't have an override). Treat
		// as the global default plane.
		return ""
	}
	return cfg.URL
}

// clientFor returns a cached or freshly-built headscale
// client for the given (url, key). The cache is a
// sync.Mutex-protected map keyed by url. We deliberately
// don't bother with LRU — the typical install has 1-5
// planes and they live for the lifetime of the process.
func (r *Router) clientFor(url, key string) *headscale.Client {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.cache[url]; ok {
		// Cache hit: verify the key still matches
		// (an admin could rotate the per-user key
		// via the UI; the cached client would be
		// using the old key).
		if c.ApiKeyForCache() == key {
			return c
		}
		// Key rotated: drop the cache entry and
		// fall through to rebuild.
		delete(r.cache, url)
	}
	c := headscale.New(url, key)
	r.cache[url] = c
	return c
}

// InvalidateCache clears the per-url client cache. Used
// by /admin/users when an admin updates a per-user
// control plane — the cached client for that url is now
// stale. /admin/control-planes also calls this when the
// admin rotates a per-user api_key via the Test / Save
// flow.
func (r *Router) InvalidateCache(url string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if url == "" {
		r.cache = map[string]*headscale.Client{}
		return
	}
	delete(r.cache, url)
}

// CacheSize returns the number of cached per-URL clients.
// Exposed for tests (the pre-Phase-D3 test file asserted
// the cache went empty after InvalidateCache(""); the
// public method lets the moved test keep that contract).
// Cheap: holds the mutex for the duration of a single
// map len read.
func (r *Router) CacheSize() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.cache)
}
