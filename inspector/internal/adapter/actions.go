package adapter

import "context"

// Action is one pre-scripted operation an adapter exposes. Run takes no input
// from the browser: the operation is fixed in code, and the only thing the
// browser can choose is which named action to invoke.
type Action struct {
	Description string
	Run         func(ctx context.Context) (any, error)
}

// Actioner is implemented by adapters that expose allow-listed actions.
// Adapters that do not implement it expose none.
type Actioner interface {
	Actions() map[string]Action
}
