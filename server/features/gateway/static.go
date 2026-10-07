package gateway

import "context"

// Static routes every call to the same targets, asking each for the model
// the call names unless the target names its own. It checks no
// credentials: it is for benchmarks and tests.
type Static []Target

// Resolve implements Resolver.
func (s Static) Resolve(_ context.Context, c *Call) (*Route, error) {
	targets := make([]Target, len(s))
	for i, t := range s {
		if t.Model == "" {
			t.Model = c.Request.Model
		}
		targets[i] = t
	}
	return &Route{Targets: targets}, nil
}
