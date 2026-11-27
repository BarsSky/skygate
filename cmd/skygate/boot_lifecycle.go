// boot_lifecycle.go — B526 (2026-11-27): the background services get a lifecycle.
//
// THE PROBLEM THIS SOLVES. main() started the DB-swap watchdog, the HA elector and the
// signal context with a bare `defer X.Stop()` a few lines later. The defers have to run
// when the PROCESS ends, so the block they live in could not be moved into a helper —
// a `defer` inside a helper runs when the helper RETURNS, which would stop the watchdog
// microseconds after starting it (B372's paying lesson, written down after the
// dbmigrate-watchdog was nearly killed that way).
//
// So the stoppers are registered on an object main() owns and defers ONE Stop():
//
//	lc := &bootLifecycle{}
//	defer lc.Stop()
//	startBackgroundServices(ctx, cfg, d, app, rn, hs, adminSvc, lc)
//
// ORDER IS REVERSE OF START (the same order Go gives nested defers: the last thing
// started is the first thing stopped), Stop is IDEMPOTENT (a second call is a no-op,
// so a future `return` path cannot double-stop anything), and a nil stopper is ignored
// — a service with nothing to stop must not be a nil-func panic during shutdown.
package main

import (
	"log"
	"sync"
)

// bootLifecycle collects the stoppers of the services a boot phase started.
type bootLifecycle struct {
	mu    sync.Mutex
	stops []lifecycleStop
	done  bool
}

type lifecycleStop struct {
	name string
	stop func()
}

// Add registers a stopper. An empty name is allowed; a nil stop is not (it is a bug in
// the CALLER, and ignoring it silently would hide a service nobody stops).
func (l *bootLifecycle) Add(name string, stop func()) {
	if l == nil {
		return
	}
	if stop == nil {
		log.Printf("boot-lifecycle: %s registered a nil stopper — nothing to stop", name)
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.done {
		// Registering after Stop means the service would never be stopped. Say it
		// loudly: this is a boot-order bug, not a runtime condition.
		log.Printf("boot-lifecycle: %s was registered AFTER Stop — stopping it immediately", name)
		stop()
		return
	}
	l.stops = append(l.stops, lifecycleStop{name: name, stop: stop})
}

// Stop stops everything that was registered, in reverse order, once.
func (l *bootLifecycle) Stop() {
	if l == nil {
		return
	}
	l.mu.Lock()
	if l.done {
		l.mu.Unlock()
		return
	}
	l.done = true
	stops := make([]lifecycleStop, len(l.stops))
	copy(stops, l.stops)
	l.stops = nil
	l.mu.Unlock()

	for i := len(stops) - 1; i >= 0; i-- {
		log.Printf("boot-lifecycle: stopping %s", stops[i].name)
		stops[i].stop()
	}
}

// Len reports how many stoppers are registered (tests and the shutdown log).
func (l *bootLifecycle) Len() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.stops)
}
