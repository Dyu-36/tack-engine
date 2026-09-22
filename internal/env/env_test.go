package env

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOsEnv_Get(t *testing.T) {
	env := &osEnv{}

	// Test getting an existing environment variable
	t.Setenv("TEST_VAR", "test_value")

	value := env.Get("TEST_VAR")
	require.Equal(t, "test_value", value)

	// Test getting a non-existent environment variable
	value = env.Get("NON_EXISTENT_VAR")
	require.Equal(t, "", value)
}

func TestOsEnv_Env(t *testing.T) {
	env := &osEnv{}

	envVars := env.Env()

	// Environment should not be empty in normal circumstances
	require.NotNil(t, envVars)
	require.Greater(t, len(envVars), 0)

	// Each environment variable should be in key=value format
	for _, envVar := range envVars {
		require.Contains(t, envVar, "=")
	}
}
