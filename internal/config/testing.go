//go:build gotacktest

// Test-only configuration seams. These are compiled only when the
// gotacktest build tag is set — i.e. never into the shipped engine
// binary — so the production import graph stays free of test-only code.
package config

import (
	"github.com/charmbracelet/crush/internal/env"
)

// identityResolver is a no-op resolver that returns values unchanged.
// Used in client mode where variable resolution is handled server-side.
type identityResolver struct{}

func (identityResolver) ResolveValue(value string) (string, error) {
	return value, nil
}

// IdentityResolver returns a VariableResolver that passes values through
// unchanged.
func IdentityResolver() VariableResolver {
	return identityResolver{}
}

// WithExpander overrides the expansion function used by the resolver.
func WithExpander(e Expander) ShellResolverOption {
	return func(r *shellVariableResolver) {
		if e != nil {
			r.expand = e
		}
	}
}

// NewStore wraps a Config in a ConfigStore with the standard shell
// variable resolver wired up. It is the programmatic counterpart of
// [Load] for callers that already hold a Config value (e.g. embedded
// server startup paths) and for test fixtures that need a fully
// functional store without touching disk.
func NewStore(cfg *Config, loadedPaths ...string) *ConfigStore {
	return &ConfigStore{
		config:      cfg,
		loadedPaths: loadedPaths,
		resolver:    NewShellVariableResolver(env.New()),
	}
}
