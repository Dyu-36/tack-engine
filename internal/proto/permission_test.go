package proto_test

import (
	"encoding/json"
	"testing"

	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/proto"
	"github.com/stretchr/testify/require"
)

// TestPermissionRequestParamsDecodable guards the wire round-trip of
// permission request params. Params decode as a generic map: the registered
// tool set no longer requests permission, and the dialog renders raw params.
func TestPermissionRequestParamsDecodable(t *testing.T) {
	t.Parallel()

	outbound := proto.PermissionRequest{
		ID:         "perm-1",
		SessionID:  "sess-1",
		ToolCallID: "call-1",
		ToolName:   tools.BashToolName,
		Path:       "/tmp",
		Params:     map[string]any{"command": "ls -la"},
	}
	data, err := json.Marshal(outbound)
	require.NoError(t, err)

	var inbound proto.PermissionRequest
	require.NoError(t, json.Unmarshal(data, &inbound))

	params, ok := inbound.Params.(map[string]any)
	require.True(t, ok, "params must decode as a generic map, got %T", inbound.Params)
	require.Equal(t, "ls -la", params["command"])
	require.Equal(t, tools.BashToolName, inbound.ToolName)
}
