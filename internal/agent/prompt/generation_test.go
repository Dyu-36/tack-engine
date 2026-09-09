package prompt

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/skills"
	"github.com/stretchr/testify/require"
)

// generationPrompt builds a minimal two-section prompt template so the
// generation diff can be exercised without the full coder template.
func generationPrompt(t *testing.T, template string, opts ...Option) *Prompt {
	t.Helper()
	opts = append([]Option{WithTimeFunc(timeNowStub)}, opts...)
	p, err := NewPrompt("generation-test", template, opts...)
	require.NoError(t, err)
	return p
}

// writeWorkspaceContextConfig seeds a workspace config whose project
// context lane points at the given file.
func writeWorkspaceContextConfig(t *testing.T, workingDir, contextFile string) {
	t.Helper()
	configJSON := `{"options": {"context_paths": ["` + filepath.Base(contextFile) + `"]}}`
	require.NoError(t, os.WriteFile(filepath.Join(workingDir, "crush.json"), []byte(configJSON), 0o644))
}

func TestGenerationStableOnIdenticalInputs(t *testing.T) {
	workingDir := t.TempDir()
	contextFile := filepath.Join(workingDir, "NOTES.md")
	require.NoError(t, os.WriteFile(contextFile, []byte("stable content"), 0o644))
	writeWorkspaceContextConfig(t, workingDir, contextFile)
	store := newTestStore(t, workingDir)

	p := generationPrompt(t, "STABLE {{range .ContextFiles}}{{.Content}}{{end}}{{/* dynamic-suffix */}}DATE={{.Date}}")
	first, err := p.BuildPrompt(t.Context(), "openai", "gpt-5.2", store)
	require.NoError(t, err)
	second, err := p.BuildPrompt(t.Context(), "openai", "gpt-5.2", store)
	require.NoError(t, err)

	require.Empty(t, first.Generation.ChangedStable(second.Generation),
		"identical inputs must not change the stable generation")
	require.Empty(t, first.Generation.ChangedDynamic(second.Generation),
		"identical inputs must not change the dynamic generation")
}

func TestGenerationDateChangeIsDynamicOnly(t *testing.T) {
	workingDir := t.TempDir()
	store := newTestStore(t, workingDir)

	morning := time.Date(2026, 9, 5, 8, 0, 0, 0, time.UTC)
	evening := time.Date(2026, 9, 5, 22, 0, 0, 0, time.UTC)
	nextDay := time.Date(2026, 9, 6, 8, 0, 0, 0, time.UTC)

	dayOne := generationPrompt(t, "STABLE{{/* dynamic-suffix */}}{{.Date}}", func(p *Prompt) { p.now = func() time.Time { return morning } })
	dayTwo := generationPrompt(t, "STABLE{{/* dynamic-suffix */}}{{.Date}}", func(p *Prompt) { p.now = func() time.Time { return evening } })
	dayThree := generationPrompt(t, "STABLE{{/* dynamic-suffix */}}{{.Date}}", func(p *Prompt) { p.now = func() time.Time { return nextDay } })

	buildOne, err := dayOne.BuildPrompt(t.Context(), "openai", "gpt-5.2", store)
	require.NoError(t, err)
	buildTwo, err := dayTwo.BuildPrompt(t.Context(), "openai", "gpt-5.2", store)
	require.NoError(t, err)
	buildThree, err := dayThree.BuildPrompt(t.Context(), "openai", "gpt-5.2", store)
	require.NoError(t, err)

	// The formatted date is day-granular; same-day builds share every
	// digest, a new day rotates only the dynamic date component.
	require.Empty(t, buildOne.Generation.ChangedStable(buildTwo.Generation))
	require.Empty(t, buildOne.Generation.ChangedDynamic(buildTwo.Generation))
	require.Empty(t, buildThree.Generation.ChangedStable(buildOne.Generation),
		"a date change must never rotate the stable generation")
	require.Equal(t, []string{"date"}, buildThree.Generation.ChangedDynamic(buildOne.Generation))
}

func TestGenerationSameSizeContextEditChangesStableOnce(t *testing.T) {
	workingDir := t.TempDir()
	contextFile := filepath.Join(workingDir, "NOTES.md")
	require.NoError(t, os.WriteFile(contextFile, []byte("ABCD"), 0o644))
	writeWorkspaceContextConfig(t, workingDir, contextFile)
	store := newTestStore(t, workingDir)

	p := generationPrompt(t, "STABLE {{range .ContextFiles}}{{.Content}}{{end}}{{/* dynamic-suffix */}}")
	before, err := p.BuildPrompt(t.Context(), "openai", "gpt-5.2", store)
	require.NoError(t, err)

	// Same byte count, different content.
	require.NoError(t, os.WriteFile(contextFile, []byte("WXYZ"), 0o644))
	after, err := p.BuildPrompt(t.Context(), "openai", "gpt-5.2", store)
	require.NoError(t, err)

	require.Equal(t, []string{"context"}, after.Generation.ChangedStable(before.Generation))
	require.Empty(t, after.Generation.ChangedDynamic(before.Generation))

	// Rebuilding unchanged content keeps the rotated generation stable.
	again, err := p.BuildPrompt(t.Context(), "openai", "gpt-5.2", store)
	require.NoError(t, err)
	require.Empty(t, again.Generation.ChangedStable(after.Generation))
}

func TestGenerationModelAndSkillsAndTemplateChanges(t *testing.T) {
	workingDir := t.TempDir()
	store := newTestStore(t, workingDir)
	template := "STABLE{{/* dynamic-suffix */}}DYNAMIC"

	base, err := generationPrompt(t, template).BuildPrompt(t.Context(), "openai", "gpt-5.2", store)
	require.NoError(t, err)

	modelSwitch, err := generationPrompt(t, template).BuildPrompt(t.Context(), "openai", "gpt-5.3", store)
	require.NoError(t, err)
	require.Equal(t, []string{"model"}, modelSwitch.Generation.ChangedStable(base.Generation))

	skillSwitch, err := generationPrompt(t, template, WithSkills([]*skills.Skill{{Name: "a"}})).BuildPrompt(t.Context(), "openai", "gpt-5.2", store)
	require.NoError(t, err)
	require.Equal(t, []string{"skills"}, skillSwitch.Generation.ChangedStable(base.Generation))

	templateSwitch, err := generationPrompt(t, "CHANGED {{/* dynamic-suffix */}}DYNAMIC").BuildPrompt(t.Context(), "openai", "gpt-5.2", store)
	require.NoError(t, err)
	require.Equal(t, []string{"template"}, templateSwitch.Generation.ChangedStable(base.Generation))
}
