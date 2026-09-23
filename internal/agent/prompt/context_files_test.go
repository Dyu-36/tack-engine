package prompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDefaultContextDiscoveryMatchesPiStyle(t *testing.T) {
	global := t.TempDir()
	root := t.TempDir()
	nested := filepath.Join(root, "src", "pkg")
	require.NoError(t, os.MkdirAll(nested, 0o755))
	t.Setenv("TACK_GLOBAL_CONFIG", global)

	require.NoError(t, os.WriteFile(filepath.Join(global, "AGENTS.md"), []byte("global agents"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("root agents"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("root claude must be shadowed by AGENTS"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(nested), "AGENTS.md"), []byte("parent agents"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(nested, "AGENTS.md"), []byte("nested agents"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(nested, "AGENTS.override.md"), []byte("nested override"), 0o600))

	legacyRules := filepath.Join(root, ".cursor", "rules")
	require.NoError(t, os.MkdirAll(legacyRules, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(legacyRules, "legacy.md"), []byte("cursor legacy"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "GEMINI.md"), []byte("gemini legacy"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "CRUSH.md"), []byte("crush legacy"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".cursorrules"), []byte("cursor legacy"), 0o600))

	project, globals, err := loadDefaultContextFiles(nested, "linux")
	require.NoError(t, err)
	require.Len(t, globals, 1)
	require.Equal(t, "global agents", globals[0].Content)

	contents := make([]string, 0, len(project))
	for _, file := range project {
		contents = append(contents, file.Content)
	}
	require.Contains(t, contents, "root agents")
	require.Contains(t, contents, "parent agents")
	require.Contains(t, contents, "nested override")
	require.NotContains(t, contents, "nested agents")
	require.NotContains(t, contents, "root claude must be shadowed by AGENTS")

	joined := strings.Join(contents, "\n")
	require.NotContains(t, joined, "cursor legacy")
	require.NotContains(t, joined, "gemini legacy")
	require.NotContains(t, joined, "crush legacy")
}

func TestPromptSourcesAndSizeDiagnostics(t *testing.T) {
	global := t.TempDir()
	workdir := t.TempDir()
	t.Setenv("TACK_GLOBAL_CONFIG", global)
	require.NoError(t, os.WriteFile(filepath.Join(workdir, "AGENTS.md"), []byte("project context"), 0o600))

	p, err := NewPrompt(
		"coder",
		coderTemplate(t),
		WithTools([]ToolInfo{{Name: "read", Snippet: "Read file contents"}}, nil),
	)
	require.NoError(t, err)
	build, err := p.BuildPrompt(t.Context(), "openai", "gpt-5", newTestStore(t, workdir))
	require.NoError(t, err)

	require.Equal(t, len(build.Text), build.Bytes)
	require.Greater(t, build.ApproxTokens, 0)
	require.Contains(t, build.Sources, "builtin:coder")
	var foundContext bool
	for _, source := range build.Sources {
		if strings.HasSuffix(strings.ToLower(filepath.ToSlash(source)), "/agents.md") {
			foundContext = true
		}
	}
	require.True(t, foundContext)
}
