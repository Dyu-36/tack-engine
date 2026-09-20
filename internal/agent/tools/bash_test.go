package tools

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
)

func newShellToolForTest(workingDir string) fantasy.AgentTool {
	return NewBashTool(workingDir)
}

func runShellTool(t *testing.T, tool fantasy.AgentTool, ctx context.Context, params BashParams) fantasy.ToolResponse {
	t.Helper()

	input, err := json.Marshal(params)
	require.NoError(t, err)

	call := fantasy.ToolCall{
		ID:    "test-call",
		Name:  BashToolName,
		Input: string(input),
	}

	resp, err := tool.Run(ctx, call)
	require.NoError(t, err)
	return resp
}

func shellCommand(posix, windows string) string {
	if runtime.GOOS == "windows" {
		return windows
	}
	return posix
}

func TestShellTool_NameIsPowerShell(t *testing.T) {
	tool := newShellToolForTest(t.TempDir())
	require.Equal(t, "powershell", tool.Info().Name)
}

func TestShellTool_CompletesWithExitCodeZero(t *testing.T) {
	tool := newShellToolForTest(t.TempDir())
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	resp := runShellTool(t, tool, ctx, BashParams{
		Command: shellCommand("echo done", "Write-Output done"),
	})

	require.False(t, resp.IsError)
	require.Contains(t, resp.Content, "done")
	var meta BashResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.Equal(t, "completed", meta.Status)
	require.NotNil(t, meta.ExitCode)
	require.Equal(t, 0, *meta.ExitCode)
}

func TestShellTool_NonZeroExitIsReported(t *testing.T) {
	tool := newShellToolForTest(t.TempDir())
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	resp := runShellTool(t, tool, ctx, BashParams{
		Command: shellCommand("exit 3", "exit 3"),
	})

	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "Exit code 3")
	var meta BashResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.Equal(t, "failed", meta.Status)
	require.NotNil(t, meta.ExitCode)
	require.Equal(t, 3, *meta.ExitCode)
}

func TestShellTool_NoBackgroundFields(t *testing.T) {
	tool := newShellToolForTest(t.TempDir())
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	resp := runShellTool(t, tool, ctx, BashParams{
		Command: shellCommand("echo hi", "Write-Output hi"),
	})

	var meta BashResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.NotContains(t, resp.Metadata, "background")
	require.NotContains(t, resp.Metadata, "shell_id")
}

func TestShellTool_UTF8Output(t *testing.T) {
	tool := newShellToolForTest(t.TempDir())
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	resp := runShellTool(t, tool, ctx, BashParams{
		Command: shellCommand("echo 'héllo wörld'", "[Console]::Write('héllo wörld')"),
	})

	require.False(t, resp.IsError)
	require.Contains(t, resp.Content, "héllo wörld")
}

func TestShellTool_TimeoutKillsProcess(t *testing.T) {
	tool := newShellToolForTest(t.TempDir())
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	resp := runShellTool(t, tool, ctx, BashParams{
		Command: shellCommand("sleep 30", "Start-Sleep -Seconds 30"),
		Timeout: 1,
	})

	require.True(t, resp.IsError)
	var meta BashResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.Equal(t, "cancelled", meta.Status)
	require.Contains(t, resp.Content, "aborted")
}

func TestShellTool_MissingCommand(t *testing.T) {
	tool := newShellToolForTest(t.TempDir())
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	resp := runShellTool(t, tool, ctx, BashParams{Command: ""})
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "missing command")
}

func TestTruncateOutputValidUTF8(t *testing.T) {
	t.Parallel()
	// CJK characters are 2 cells wide; this string is far wider than
	// MaxOutputLength so TruncateOutput must truncate it.
	content := strings.Repeat("你好世界", MaxOutputLength)

	out := TruncateOutput(content)
	require.True(t, utf8.ValidString(out), "truncated output must stay valid UTF-8")
	require.Contains(t, out, "lines truncated")
}

func TestTruncateOutputShortContent(t *testing.T) {
	t.Parallel()
	content := "short output"
	require.Equal(t, content, TruncateOutput(content))
}

func TestTruncateOutputEmoji(t *testing.T) {
	t.Parallel()
	// Emoji with ZWJ sequences should not be split.
	content := strings.Repeat("👨‍👩‍👧‍👦", MaxOutputLength)

	out := TruncateOutput(content)
	require.True(t, utf8.ValidString(out), "truncated output must stay valid UTF-8")
	require.Contains(t, out, "lines truncated")
}
