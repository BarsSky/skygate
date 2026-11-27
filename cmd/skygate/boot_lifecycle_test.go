// boot_lifecycle_test.go — B526 step 8 (2026-11-27).
//
// The lifecycle exists so the `defer wd.Stop()` / `defer el.Stop()` pair could leave
// main() WITHOUT changing when those services stop. These tests pin the three
// properties that make that true:
//
//  1. reverse order — the last thing started is the first thing stopped, exactly what
//     nested defers did;
//  2. once — a second Stop() runs nothing (a future early return cannot double-stop);
//  3. add-after-stop — a service registered too late is stopped immediately and said
//     out loud, because the alternative is a goroutine nobody ever stops.
package main

import (
	"sync"
	"testing"
)

func TestBootLifecycle_StopsInReverseOrder(t *testing.T) {
	var mu sync.Mutex
	var order []string
	lc := &bootLifecycle{}
	for _, name := range []string{"first", "second", "third"} {
		n := name
		lc.Add(n, func() {
			mu.Lock()
			order = append(order, n)
			mu.Unlock()
		})
	}
	if lc.Len() != 3 {
		t.Fatalf("Len = %d, want 3", lc.Len())
	}
	lc.Stop()

	want := []string{"third", "second", "first"}
	if len(order) != len(want) {
		t.Fatalf("stopped %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("stop order = %v, want %v (the last started must stop FIRST)", order, want)
		}
	}
}

func TestBootLifecycle_StopIsIdempotent(t *testing.T) {
	calls := 0
	lc := &bootLifecycle{}
	lc.Add("watchdog", func() { calls++ })
	lc.Stop()
	lc.Stop()
	if calls != 1 {
		t.Fatalf("stopper ran %d times, want 1 (a second Stop must be a no-op)", calls)
	}
	if lc.Len() != 0 {
		t.Fatalf("Len after Stop = %d, want 0", lc.Len())
	}
}

func TestBootLifecycle_NilStopIsNotAPanic(t *testing.T) {
	lc := &bootLifecycle{}
	lc.Add("nothing-to-stop", nil) // must not panic at exit
	if lc.Len() != 0 {
		t.Fatalf("a nil stopper must not be registered (Len = %d)", lc.Len())
	}
	lc.Stop()
}

func TestBootLifecycle_AddAfterStopStopsImmediately(t *testing.T) {
	calls := 0
	lc := &bootLifecycle{}
	lc.Stop()
	lc.Add("late", func() { calls++ })
	if calls != 1 {
		t.Fatalf("a stopper registered after Stop ran %d times, want 1 (it must not be leaked)", calls)
	}
}

func TestBootLifecycle_NilReceiverIsSafe(t *testing.T) {
	var lc *bootLifecycle
	lc.Add("x", func() {}) // must not panic: a caller may forget to build one
	lc.Stop()
	if lc.Len() != 0 {
		t.Fatal("nil lifecycle reported stoppers")
	}
}
