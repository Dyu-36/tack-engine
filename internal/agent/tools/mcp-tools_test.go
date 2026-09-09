package tools

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInjectCurrentSessionIDIsScopedAndTrusted(t *testing.T) {
	t.Parallel()

	input, err := injectCurrentSessionID(
		recallMCPName,
		recallSearchToolName,
		`{"query":"memory","current_session_id":"spoofed"}`,
		"trusted-session",
	)
	require.NoError(t, err)
	var args map[string]any
	require.NoError(t, json.Unmarshal([]byte(input), &args))
	require.Equal(t, "trusted-session", args["current_session_id"])
	require.Equal(t, "memory", args["query"])

	untouched, err := injectCurrentSessionID("other-mcp", recallSearchToolName, "not-json", "trusted-session")
	require.NoError(t, err)
	require.Equal(t, "not-json", untouched)
}
