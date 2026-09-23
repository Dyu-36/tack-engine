package prompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/skills"
	"github.com/stretchr/testify/require"
)

func coderTemplate(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "templates", "coder.md.tpl"))
	require.NoError(t, err)
	return string(raw)
}

// TestCoderTemplateMatchesPiShape validates the shipped coder.md.tpl: it must
// parse and render in the same compact section order as Pi's current prompt:
// preamble, tools, rules, optional context/skills, and cwd last.
func TestCoderTemplateMatchesPiShape(t *testing.T) {
	t.Parallel()

	p, err := NewPrompt(
		"coder",
		coderTemplate(t),
		withTimeFunc(timeNowStub),
		WithTools(
			[]ToolInfo{{Name: "read", Snippet: "Read file contents"}},
			[]string{"Be concise in your responses"},
		),
	)
	require.NoError(t, err)

	store := newTestStore(t, t.TempDir())
	text, err := p.Build(t.Context(), "openai", "gpt-5.2", store)
	require.NoError(t, err)

	require.Contains(t, text, "You are an expert coding assistant operating inside Gotack, a coding agent harness.")
	require.Contains(t, text, "<tools>")
	require.Contains(t, text, "<rules>")
	require.Contains(t, text, "<cwd>")
	require.NotContains(t, text, "Current date and time:")
	require.NotContains(t, text, "Current platform:")
	require.NotContains(t, text, "you may have access to other custom tools")

	preamble := strings.Index(text, "You are an expert coding assistant")
	tools := strings.Index(text, "<tools>")
	rules := strings.Index(text, "<rules>")
	cwd := strings.Index(text, "<cwd>")
	require.True(t, preamble >= 0 && preamble < tools && tools < rules && rules < cwd)

	for _, removed := range []string{
		"multiedit",
		"lsp_",
		"job_output",
		"job_kill",
		"sourcegraph",
		"todos",
		"tack_info",
		"tack_logs",
		"agentic_fetch",
		"list_mcp_resources",
		"read_mcp_resource",
	} {
		require.NotContains(t, strings.ToLower(text), removed, "prompt must not mention removed tool %q", removed)
	}
}

func TestCoderTemplateRendersRegistryToolsAndGuidelines(t *testing.T) {
	t.Parallel()

	tools := []ToolInfo{
		{Name: "read", Snippet: "Read file contents"},
		{Name: "powershell", Snippet: "Execute PowerShell commands"},
	}
	guidelines := []string{
		"Use read to examine files instead of cat or sed",
		"Be concise in your responses",
	}

	p, err := NewPrompt(
		"coder",
		coderTemplate(t),
		withTimeFunc(timeNowStub),
		WithTools(tools, guidelines),
	)
	require.NoError(t, err)

	store := newTestStore(t, t.TempDir())
	text, err := p.Build(t.Context(), "openai", "gpt-5.2", store)
	require.NoError(t, err)

	require.Contains(t, text, "- read: Read file contents")
	require.Contains(t, text, "- powershell: Execute PowerShell commands")
	require.Contains(t, text, "- Be concise in your responses")
	require.NotContains(t, text, "- edit:")
	require.NotContains(t, text, "- glob:")

	skilled, err := NewPrompt(
		"coder",
		coderTemplate(t),
		withTimeFunc(timeNowStub),
		WithTools(tools, guidelines),
		WithSkills([]*skills.Skill{{Name: "demo", Description: "does demo", SkillFilePath: "/x/SKILL.md"}}),
	)
	require.NoError(t, err)
	skilledText, err := skilled.Build(t.Context(), "openai", "gpt-5.2", store)
	require.NoError(t, err)
	require.Contains(t, skilledText, "<skills>")
	require.Contains(t, skilledText, "<available_skills>")
	require.Contains(t, skilledText, "with the read tool")

	unskilled, err := NewPrompt(
		"coder",
		coderTemplate(t),
		withTimeFunc(timeNowStub),
		WithSkills([]*skills.Skill{{Name: "demo", Description: "does demo", SkillFilePath: "/x/SKILL.md"}}),
	)
	require.NoError(t, err)
	unskilledText, err := unskilled.Build(t.Context(), "openai", "gpt-5.2", store)
	require.NoError(t, err)
	require.NotContains(t, unskilledText, "<available_skills>")
}

func TestPromptGenerationTracksTools(t *testing.T) {
	t.Parallel()

	build := func(tools []ToolInfo, guidelines []string) PromptBuild {
		t.Helper()
		p, err := NewPrompt(
			"coder",
			coderTemplate(t),
			withTimeFunc(timeNowStub),
			WithTools(tools, guidelines),
		)
		require.NoError(t, err)
		store := newTestStore(t, t.TempDir())
		out, err := p.BuildPrompt(t.Context(), "openai", "gpt-5.2", store)
		require.NoError(t, err)
		return out
	}

	base := build([]ToolInfo{{Name: "read", Snippet: "Read file contents"}}, []string{"Be concise in your responses"})
	same := build([]ToolInfo{{Name: "read", Snippet: "Read file contents"}}, []string{"Be concise in your responses"})
	changed := build([]ToolInfo{{Name: "read", Snippet: "Read file contents"}, {Name: "glob", Snippet: "Find files"}}, []string{"Be concise in your responses"})

	require.Empty(t, same.Generation.ChangedStable(base.Generation))
	require.Equal(t, []string{"tools"}, changed.Generation.ChangedStable(base.Generation))
}
