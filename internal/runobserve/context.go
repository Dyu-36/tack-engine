package runobserve

import (
	"context"
	"time"
)

type phaseKey struct{}

// WithPhases returns a context that reports the duration of each named phase
// recorded via [Start] to record.
func WithPhases(ctx context.Context, record func(string, time.Duration)) context.Context {
	return context.WithValue(ctx, phaseKey{}, record)
}

// Start begins timing a named phase on ctx and returns a function that reports
// its duration. It is a no-op when ctx carries no phase recorder.
func Start(ctx context.Context, phase string) func() {
	record, _ := ctx.Value(phaseKey{}).(func(string, time.Duration))
	if record == nil {
		return func() {}
	}
	started := time.Now()
	return func() { record(phase, time.Since(started)) }
}
