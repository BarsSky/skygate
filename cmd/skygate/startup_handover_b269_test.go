// B269 — the boot-time handler swap must survive the transition from the
// provisional startup handler to the real router.
//
// Live regression caught by the B270 probe (2026-09-19): the first version of
// the swap stored an `http.HandlerFunc` and then an `*http.ServeMux` in the
// same `atomic.Value`, which panics with
//
//	sync/atomic: store of inconsistently typed value into Value
//
// INSIDE the handover goroutine — i.e. the process died exactly when it had
// finally finished booting, after /healthz had already answered. No source
// grep could see it; only running the binary could.
package main

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// TestB269_HandlerSwapAcceptsDifferentHandlerImpls pins the concrete-type rule:
// both stores must be the same wrapper type, whatever handler they carry.
func TestB269_HandlerSwapAcceptsDifferentHandlerImpls(t *testing.T) {
	var handler atomic.Value

	// Provisional handler (what startup.StageHandler() returns).
	stage := func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) }
	// The real router (what main() builds).
	real := http.NewServeMux()
	real.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

	serve := func(h http.Handler) int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		return rec.Code
	}

	handler.Store(handlerBox{http.HandlerFunc(stage)})
	if got := serve(handler.Load().(handlerBox).h); got != http.StatusTeapot {
		t.Fatalf("provisional handler status = %d, want 418", got)
	}

	// The line that used to panic.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("handover panicked: %v (atomic.Value saw two different concrete types)", r)
		}
	}()
	handler.Store(handlerBox{real})

	if got := serve(handler.Load().(handlerBox).h); got != http.StatusOK {
		t.Fatalf("real handler status = %d, want 200", got)
	}
}
