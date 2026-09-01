// Package dbmigrate — sse.go is the SSE broker that streams
// migration events to the /admin/database/migrate/{id}/stream
// HTTP handler. Mirrors internal/deployrun/sse.go (B194).

package dbmigrate

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// broker is the per-process in-memory event broker. Events
// are buffered in a channel per subscriber.
type broker struct {
	mu          sync.RWMutex
	subscribers map[chan SSEEvent]struct{}
}

var globalBroker = &broker{
	subscribers: map[chan SSEEvent]struct{}{},
}

// Subscribe adds a new subscriber and returns a channel
// (buffered) of events + a cancel function. The cancel
// function MUST be called by the HTTP handler when the
// client disconnects, otherwise the channel leaks.
func Subscribe() (chan SSEEvent, func()) {
	ch := make(chan SSEEvent, 64)
	globalBroker.mu.Lock()
	globalBroker.subscribers[ch] = struct{}{}
	globalBroker.mu.Unlock()
	cancel := func() {
		globalBroker.mu.Lock()
		delete(globalBroker.subscribers, ch)
		globalBroker.mu.Unlock()
		close(ch)
	}
	return ch, cancel
}

// emit broadcasts an event to all current subscribers. If
// a subscriber's channel is full, the event is dropped for
// that subscriber (we'd rather drop than block the migration
// orchestrator).
func emit(ev SSEEvent) {
	globalBroker.mu.RLock()
	defer globalBroker.mu.RUnlock()
	for ch := range globalBroker.subscribers {
		select {
		case ch <- ev:
		default:
		}
	}
}

// EmitStepLog is a small helper for steps/ to push a
// log line into the SSE broker. Keeps the steps/ code
// from having to construct an SSEEvent by hand.
func EmitStepLog(runID int64, step, msg string) {
	emit(SSEEvent{
		At:    time.Now(),
		Kind:  "step_log",
		RunID: runID,
		Step:  step,
		Log:   &StepLog{At: time.Now(), Level: "info", Msg: msg},
	})
}

// StreamHandler serves the SSE stream for a given run. It
// sends events as they come in from the broker, plus a
// 15s heartbeat so proxies don't time out.
//
// The handler is registered in handlers.go. It does NOT
// require auth (operators are not signed in to the admin
// web during a migration in some flows) but it DOES require
// the run id in the URL. The page only embeds the id after
// the admin clicks "Migrate", so the URL is not guessable.
func StreamHandler(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch, cancel := Subscribe()
	defer cancel()

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case ev := <-ch:
			emitSSE(w, ev)
			flusher.Flush()
		case <-ticker.C:
			emitSSE(w, SSEEvent{Kind: "heartbeat"})
			flusher.Flush()
		}
	}
}

// emitSSE writes one SSE event to the response writer in the
// "data: <json>\n\n" wire format.
func emitSSE(w http.ResponseWriter, ev SSEEvent) {
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "data: %s\n\n", b)
}
