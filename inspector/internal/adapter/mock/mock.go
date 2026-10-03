// Package mock is a trivial adapter with canned, deterministic data. It exists
// so the Inspector core and viewer shell can be built and tested before any
// real target system is involved.
package mock

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"glasshouse/inspector/internal/adapter"
)

// Type is the snapshot type the mock adapter emits.
const Type = "mock.counter"

// Adapter emits a counter that advances once per tick.
type Adapter struct {
	Interval time.Duration

	mu        sync.Mutex
	connected bool
	name      string
	seq       uint64
	value     int64
}

// New returns a mock adapter that ticks every interval.
func New(interval time.Duration) *Adapter {
	return &Adapter{Interval: interval}
}

// Connect always succeeds; the mock has no external dependency.
func (a *Adapter) Connect(_ context.Context, target adapter.Target) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.connected = true
	a.name = target.Name
	return nil
}

// Snapshot returns the current counter value.
func (a *Adapter) Snapshot(_ context.Context) (adapter.Snapshot, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.connected {
		return adapter.Snapshot{}, errors.New("mock: not connected")
	}
	return adapter.Snapshot{
		Type:      Type,
		Source:    a.name,
		Seq:       a.seq,
		Timestamp: time.Now().UTC(),
		Data:      map[string]any{"value": a.value},
	}, nil
}

// Actions exposes one fixed operation: bump the counter by one.
func (a *Adapter) Actions() map[string]adapter.Action {
	return map[string]adapter.Action{
		"bump": {
			Description: "Increment the counter by one",
			Run: func(context.Context) (any, error) {
				a.mu.Lock()
				defer a.mu.Unlock()
				a.value++
				return map[string]any{"value": a.value}, nil
			},
		},
	}
}

// StreamEvents emits one tick event per interval until ctx is cancelled.
func (a *Adapter) StreamEvents(ctx context.Context) (<-chan adapter.Event, <-chan error, error) {
	a.mu.Lock()
	connected := a.connected
	a.mu.Unlock()
	if !connected {
		return nil, nil, errors.New("mock: not connected")
	}

	events := make(chan adapter.Event)
	errs := make(chan error, 1)
	go func() {
		defer close(events)
		ticker := time.NewTicker(a.Interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				a.mu.Lock()
				a.seq++
				a.value++
				seq, value, source := a.seq, a.value, a.name
				a.mu.Unlock()
				select {
				case events <- adapter.Event{
					ID:        fmt.Sprintf("%s:%d", source, seq),
					Seq:       seq,
					Timestamp: now.UTC(),
					Source:    source,
					Kind:      "counter_incremented",
					Detail:    map[string]any{"value": value},
				}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return events, errs, nil
}
