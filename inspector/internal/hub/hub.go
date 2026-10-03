// Package hub connects one running adapter to any number of viewers. It keeps
// a bounded history of recent events so a viewer that connects late, or
// reconnects, can catch up.
package hub

import (
	"context"
	"log/slog"
	"sync"

	"glasshouse/inspector/internal/adapter"
)

// DefaultHistory is how many recent events are kept for late joiners.
const DefaultHistory = 500

// subscriberBuffer bounds how far a slow viewer may fall behind before it is
// dropped. A dropped viewer reconnects and catches up from history.
const subscriberBuffer = 64

// Hub owns the event history and the subscriber set.
type Hub struct {
	mu      sync.Mutex
	history []adapter.Event
	max     int
	subs    map[chan adapter.Event]struct{}
	logger  *slog.Logger
}

// New returns a hub that keeps up to max events of history.
func New(max int, logger *slog.Logger) *Hub {
	return &Hub{
		max:    max,
		subs:   make(map[chan adapter.Event]struct{}),
		logger: logger,
	}
}

// Run pumps events from the adapter into the hub until ctx is cancelled or the
// adapter's stream closes. Terminal adapter errors are logged, not swallowed.
func (h *Hub) Run(ctx context.Context, events <-chan adapter.Event, errs <-chan error) {
	for {
		select {
		case <-ctx.Done():
			return
		case err, ok := <-errs:
			if ok && err != nil {
				h.logger.Error("adapter stream failed", "err", err)
			}
		case ev, ok := <-events:
			if !ok {
				return
			}
			h.publish(ev)
		}
	}
}

func (h *Hub) publish(ev adapter.Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.history = append(h.history, ev)
	if len(h.history) > h.max {
		h.history = h.history[len(h.history)-h.max:]
	}
	for ch := range h.subs {
		select {
		case ch <- ev:
		default:
			// Subscriber is too slow. Drop it; it will reconnect and replay.
			close(ch)
			delete(h.subs, ch)
		}
	}
}

// History returns a copy of the buffered events, oldest first.
func (h *Hub) History() []adapter.Event {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]adapter.Event, len(h.history))
	copy(out, h.history)
	return out
}

// Subscribe returns the events after afterSeq (from history, oldest first) and
// a channel of future events. The channel closes when the subscriber is
// dropped or unsubscribe is called. Callers must call unsubscribe.
func (h *Hub) Subscribe(afterSeq uint64, hasAfter bool) (replay []adapter.Event, live <-chan adapter.Event, unsubscribe func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, ev := range h.history {
		if !hasAfter || ev.Seq > afterSeq {
			replay = append(replay, ev)
		}
	}
	ch := make(chan adapter.Event, subscriberBuffer)
	h.subs[ch] = struct{}{}
	unsubscribe = func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if _, ok := h.subs[ch]; ok {
			delete(h.subs, ch)
			close(ch)
		}
	}
	return replay, ch, unsubscribe
}
