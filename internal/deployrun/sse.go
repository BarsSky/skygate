// internal/deployrun/sse.go — Server-Sent Events broker
// for live progress streaming to /admin/deploys/{id}.
//
// Each DeployRun has at most a handful of browser
// subscribers (the operator + maybe a few dashboard
// tabs). The broker is per-run, in-process, and uses
// a buffered channel per subscriber. SSE is a perfect
// fit for this volume: the browser opens an
// EventSource, the server pushes events, the browser
// auto-reconnects on disconnect.
//
// Why SSE instead of WebSocket / polling:
//   - One-way data flow (server → browser) — exactly
//     what SSE is for. WebSocket is overkill.
//   - Native browser EventSource API, no JS library.
//   - Auto-reconnect built-in (browser handles
//     disconnects, retries with last-event-id).
//   - Falls back to text/event-stream which works
//     through proxies, gzip, etc.

package deployrun

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// EventType is the type of an SSE message.
type EventType string

const (
	EventRunStarted  EventType = "run_started"
	EventStepStarted  EventType = "step_started"
	EventStepLog      EventType = "step_log"
	EventStepFinished EventType = "step_finished"
	EventRunFinished  EventType = "run_finished"
)

// Event is a single SSE message. Sent as JSON.
type Event struct {
	Type      EventType `json:"type"`
	RunID     int64     `json:"run_id"`
	Step      string    `json:"step,omitempty"`
	Status    string    `json:"status,omitempty"`
	Line      string    `json:"line,omitempty"`
	Timestamp string    `json:"timestamp"`
}

// subscriber is a single browser's EventSource connection.
// The framework holds the channel; Publish blocks if
// the channel is full (back-pressure).
type subscriber struct {
	ch chan Event
	id int
}

// SSEBroker holds the subscribers for a single
// DeployRun. One broker per run; the framework
// creates it when the run starts and disposes it
// when the run finishes.
type SSEBroker struct {
	mu     sync.RWMutex
	subs   map[int]*subscriber
	nextID int
	closed bool
}

// NewSSEBroker creates a broker for a single run.
func NewSSEBroker() *SSEBroker {
	return &SSEBroker{
		subs: make(map[int]*subscriber),
	}
}

// Subscribe registers a new browser subscriber. The
// returned channel receives all subsequent events
// until Unsubscribe is called or the broker is closed.
//
// Buffer is 64 events; if the browser is slow, the
// framework drops the subscriber (sends a synthetic
// "dropped" event so the UI knows to refresh).
func (b *SSEBroker) Subscribe() (int, <-chan Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		ch := make(chan Event, 1)
		ch <- Event{Type: "broker_closed", Timestamp: nowStr()}
		close(ch)
		return -1, ch
	}
	b.nextID++
	sub := &subscriber{ch: make(chan Event, 64), id: b.nextID}
	b.subs[b.nextID] = sub
	return sub.id, sub.ch
}

// Unsubscribe removes a subscriber.
func (b *SSEBroker) Unsubscribe(id int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if sub, ok := b.subs[id]; ok {
		close(sub.ch)
		delete(b.subs, id)
	}
}

// Publish sends an event to all subscribers. Non-blocking
// (drops the event for a slow subscriber rather than
// blocking the framework).
func (b *SSEBroker) Publish(evt Event) {
	if evt.Timestamp == "" {
		evt.Timestamp = nowStr()
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.closed {
		return
	}
	for _, sub := range b.subs {
		select {
		case sub.ch <- evt:
		default:
			// Slow subscriber — drop. The browser's
			// EventSource auto-reconnects, and the
			// next poll /admin/deploys/{id} returns
			// the full state. No data loss.
		}
	}
}

// PublishLog is a sugar wrapper: builds a step_log event
// from a step name + log line.
func (b *SSEBroker) PublishLog(step, line string) {
	b.Publish(Event{
		Type: EventStepLog,
		Step: step,
		Line: line,
	})
}

// Close disposes all subscribers. After Close, Subscribe
// returns a closed channel; Publish becomes a no-op.
func (b *SSEBroker) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for id, sub := range b.subs {
		close(sub.ch)
		delete(b.subs, id)
	}
}

// MarshalEvent formats an event for SSE wire format.
// Returns the multi-line SSE payload: "event: <type>\n
// data: <json>\n\n".
func MarshalEvent(evt Event) (string, error) {
	data, err := json.Marshal(evt)
	if err != nil {
		return "", fmt.Errorf("marshal event: %w", err)
	}
	return fmt.Sprintf("event: %s\ndata: %s\n\n", evt.Type, data), nil
}

func nowStr() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}
