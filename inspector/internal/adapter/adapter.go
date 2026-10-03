// Package adapter defines the contract every Glasshouse adapter implements.
// The HTTP layer and the viewer only ever see the types in this package, so
// nothing here may be specific to one target system.
package adapter

import (
	"context"
	"time"
)

// SchemaVersion is the version of the snapshot/event envelope. Bump it on any
// breaking change; static samples carry the version they were produced with.
const SchemaVersion = 1

// Target identifies the running service an adapter should attach to.
type Target struct {
	// Name is the human-readable label shown in the viewer.
	Name string
	// Endpoint is where the adapter reaches the service, e.g. a DSN for a
	// published port on 127.0.0.1. Inspector never talks to the Docker socket.
	Endpoint string
}

// Snapshot is the full structured state of the target at one moment.
type Snapshot struct {
	// Type selects the view component in the viewer, e.g. "postgres.heap_page".
	Type string `json:"type"`
	// Source names the node or instance the snapshot came from.
	Source string `json:"source"`
	// Seq is the per-source sequence number this snapshot reflects. Events with
	// a higher Seq from the same source happened after it.
	Seq       uint64    `json:"seq"`
	Timestamp time.Time `json:"timestamp"`
	// Data is adapter-specific structured state.
	Data any `json:"data"`
}

// Event is one state change observed on the target.
type Event struct {
	// ID is "<source>:<seq>", unique and sortable within a source.
	ID        string    `json:"id"`
	Seq       uint64    `json:"seq"`
	Timestamp time.Time `json:"timestamp"`
	Source    string    `json:"source"`
	// Kind is one of the kinds the adapter declares, e.g. "tuple_inserted".
	Kind string `json:"kind"`
	// CorrelationID groups causally related events. Opaque to the viewer.
	CorrelationID string `json:"correlation_id,omitempty"`
	// Detail is adapter-specific.
	Detail any `json:"detail"`
}

// Adapter is the fixed interface every target system implements.
type Adapter interface {
	// Connect attaches to the target. It must not return until the target is
	// usable, or an error describing why it is not.
	Connect(ctx context.Context, target Target) error

	// Snapshot pulls the current state on demand.
	Snapshot(ctx context.Context) (Snapshot, error)

	// StreamEvents starts a live tail. The returned event channel closes when
	// ctx is cancelled. A terminal runtime failure is sent once on the error
	// channel, after which the event channel closes. The error return reports
	// failures to start the stream at all.
	StreamEvents(ctx context.Context) (<-chan Event, <-chan error, error)
}
