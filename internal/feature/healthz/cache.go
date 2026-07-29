package healthz

// cache.go — process-wide cache for the readiness probe. We cache
// the last probe result so an external monitor scraping every
// 100ms doesn't hammer the DB or headscale. The cache has a
// 1-second TTL (atomic.Pointer store of the last successful
// probe time + state).

import "sync/atomic"

// readyzCache is updated on every probe. Probes within the
// readyzCacheTTL window get the cached state; probes after that
// trigger a fresh check.
type readyzCache struct {
	lastAt atomic.Int64 // unix seconds
	state  atomic.Pointer[readyzState]
}

// Cache the last probe result for 1 second. Probes
// within that window get the cached state. Probes
// after 1s trigger a fresh check.
const readyzCacheTTL = 1

// readyz is a process-wide cache. Single global is
// fine because the handler is HTTP-serialized (one
// request at a time per goroutine, but they don't
// share a Go pointer to this struct).
var readyz = &readyzCache{}
