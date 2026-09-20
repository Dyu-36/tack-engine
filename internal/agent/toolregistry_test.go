package agent

import (
	"testing"

	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/stretchr/testify/require"
)

// coderToolNames is the Pi-like core registry for the coder agent.
func allRegistryNames() []string {
	var names []string
	for _, spec := range toolRegistry() {
		names = append(names, spec.Name)
	}
	return names
}

func TestToolRegistryIsSmallAndComplete(t *testing.T) {
	t.Parallel()

	specs := toolRegistry()
	require.Len(t, specs, 6, "the Pi-like registry must stay small (4-6 tools)")

	for _, spec := range specs {
		require.NotEmpty(t, spec.Name)
		require.NotEmpty(t, spec.Snippet, "every registry entry needs a prompt snippet")
		require.NotNil(t, spec.Build)
	}

	require.Equal(t, []string{
		tools.ViewToolName,
		tools.BashToolName,
		tools.EditToolName,
		tools.WriteToolName,
		tools.GrepToolName,
		tools.GlobToolName,
	}, allRegistryNames())
	require.Equal(t, "powershell", tools.BashToolName)
	require.Equal(t, "read", tools.ViewToolName)
}

func TestRegistrySnapshotsFollowAllowlist(t *testing.T) {
	t.Parallel()

	specs, infos := registrySnapshots([]string{"read", "powershell"})
	require.Len(t, specs, 2)
	require.Len(t, infos, 2)
	require.Equal(t, "read", infos[0].Name)
	require.Equal(t, "powershell", infos[1].Name)
	require.Equal(t, "Read file contents", infos[0].Snippet)

	// Unknown names in the allowlist are ignored; nothing extra is built.
	specs, infos = registrySnapshots([]string{"read", "multiedit", "lsp_rename"})
	require.Len(t, specs, 1)
	require.Len(t, infos, 1)
	require.Equal(t, "read", infos[0].Name)
}

// TestPromptGuidelinesTrackEnabledTools mirrors Pi's buildRules: the bullets
// adapt to the tools that are actually registered.
func TestPromptGuidelinesTrackEnabledTools(t *testing.T) {
	t.Parallel()

	full := promptGuidelines(allRegistryNames())
	require.Contains(t, full, "Prefer grep/glob tools over powershell for file exploration (faster, respects .gitignore)")
	require.Contains(t, full, "Use read to examine files before editing. You must use this tool instead of cat or sed.")
	require.Contains(t, full, "Use edit for precise changes (old text must match exactly)")
	require.Contains(t, full, "Use write only for new files or complete rewrites")
	require.Contains(t, full, "Be concise in your responses")
	require.Contains(t, full, "Show file paths clearly when working with files")

	// Without the search tools the shell fallback bullet is used instead.
	shellOnly := promptGuidelines([]string{"read", "powershell"})
	require.Contains(t, shellOnly, "Use powershell for file operations like dir, Get-ChildItem, Select-String")
	require.NotContains(t, shellOnly, "Prefer grep/glob tools over powershell for file exploration (faster, respects .gitignore)")
	require.NotContains(t, shellOnly, "Use edit for precise changes (old text must match exactly)")
	require.NotContains(t, shellOnly, "Use write only for new files or complete rewrites")
}