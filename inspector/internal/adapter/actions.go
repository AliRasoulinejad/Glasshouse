package adapter

import "context"

// Action is one pre-scripted operation an adapter exposes. Run takes no input
// from the browser: the operation is fixed in code, and the only thing the
// browser can choose is which named action to invoke.
type Action struct {
	Description string
	// Query is the literal SQL statement Run executes, shown to the browser
	// so a reader can see exactly what a button triggers. Empty for actions
	// that run no single fixed statement (e.g. the mock adapter's "bump").
	Query string
	Run   func(ctx context.Context) (any, error)
}

// Actioner is implemented by adapters that expose allow-listed actions.
// Adapters that do not implement it expose none.
type Actioner interface {
	Actions() map[string]Action
}
