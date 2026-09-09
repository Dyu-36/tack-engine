package agent

import "context"

// runIDContextKey is the unexported context key used to carry a
// caller-supplied RunID from the workspace HTTP boundary
// (backend.SendMessage) down into coordinator.Run without forcing a
// breaking change to the Coordinator.Run signature. The value is
// then copied onto SessionAgentCall.RunID by the coordinator so the
// agent's terminal RunComplete event can echo it back to the
// originating caller.
type runIDContextKey struct{}

// maxInputTokensContextKey carries an optional aggregate input budget from
// the HTTP request into the queued SessionAgentCall without changing the
// Coordinator.Run signature.
type maxInputTokensContextKey struct{}

// WithRunID returns ctx tagged with a per-request RunID. It is the
// boundary helper for callers that need their SendMessage→Run
// terminal event to be uniquely correlatable (e.g. `crush run`
// against a session that may be busy). Empty runIDs are stored
// as-is; downstream code treats an empty RunID as "caller did not
// supply one" and falls back to SessionID-only correlation.
func WithRunID(ctx context.Context, runID string) context.Context {
	return context.WithValue(ctx, runIDContextKey{}, runID)
}

// RunIDFromContext returns the RunID set by [WithRunID], or "" if
// none was set or the value is not a string. Exported because the
// coordinator and tests in other packages need to read it; safe to
// call on any context.
func RunIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(runIDContextKey{}).(string); ok {
		return v
	}
	return ""
}

// WithMaxInputTokens tags ctx with a positive per-run aggregate input budget.
// Non-positive values are treated as unset, matching the review budget's
// disabled semantics and preserving ordinary runs.
func WithMaxInputTokens(ctx context.Context, tokens int64) context.Context {
	if tokens <= 0 {
		return ctx
	}
	return context.WithValue(ctx, maxInputTokensContextKey{}, tokens)
}

// MaxInputTokensFromContext returns the positive budget set by
// WithMaxInputTokens, or zero when no budget is present.
func MaxInputTokensFromContext(ctx context.Context) int64 {
	if v, ok := ctx.Value(maxInputTokensContextKey{}).(int64); ok && v > 0 {
		return v
	}
	return 0
}
