package runobserve

import (
	"context"
	"time"
)

type phaseKey struct{}

func WithPhases(ctx context.Context, record func(string, time.Duration)) context.Context {
	return context.WithValue(ctx, phaseKey{}, record)
}

func Start(ctx context.Context, phase string) func() {
	record, _ := ctx.Value(phaseKey{}).(func(string, time.Duration))
	if record == nil {
		return func() {}
	}
	started := time.Now()
	return func() { record(phase, time.Since(started)) }
}
