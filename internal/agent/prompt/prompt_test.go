package prompt

import (
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/skills"
	"github.com/stretchr/testify/require"
)

func newTestStore(t *testing.T, workingDir string) *config.ConfigStore {
	t.Helper()
	store, err := config.Load(workingDir, t.TempDir(), false)
	require.NoError(t, err)
	return store
}

func writeContextTree(t *testing.T, root string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("root agents"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "sub", "NOTES.md"), []byte("sub notes"), 0o644))
}

func renderAllContextFiles(groups []ContextGroup) string {
	out := ""
	for _, group := range groups {
		for _, file := range group.Files {
			out += file.Path + "\x00" + file.Content + "\x00"
		}
	}
	return out
}

func TestLoadContextFilesDeterministicAcrossPermutations(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	rootC := t.TempDir()
	writeContextTree(t, rootA)
	writeContextTree(t, rootB)
	writeContextTree(t, rootC)
	store := newTestStore(t, rootA)

	roots := []string{rootA, rootB, rootC}
	reference := renderAllContextFiles(loadContextFiles(roots, store, "windows"))

	rng := rand.New(rand.NewSource(42))
	for i := 0; i < 500; i++ {
		perm := append([]string(nil), roots...)
		rng.Shuffle(len(perm), func(x, y int) {
			perm[x], perm[y] = perm[y], perm[x]
		})
		got := renderAllContextFiles(loadContextFiles(perm, store, "windows"))
		require.Equal(t, reference, got, "permutation %d changed the rendered context bytes", i)
	}
}

func TestLoadContextFilesWindowsAliasesDedupeAndRenderIdentically(t *testing.T) {
	root := t.TempDir()
	writeContextTree(t, root)

	// The same root expressed with different casings and separators.
	slash := filepath.ToSlash(root)
	upper := strings.ToUpper(slash[:1]) + slash[1:]
	lower := strings.ToLower(slash)

	paths := []string{slash, upper, lower}
	store := newTestStore(t, root)
	groups := loadContextFiles(paths, store, "windows")
	require.Len(t, groups, 1, "Windows alias casings of the same root must dedupe to one group")

	// Reversed input order renders the same canonical bytes.
	reversed := []string{lower, upper, slash}
	groupsReversed := loadContextFiles(reversed, store, "windows")
	require.Len(t, groupsReversed, 1)
	require.Equal(t, renderAllContextFiles(groups), renderAllContextFiles(groupsReversed))

	// Rendered file paths are canonical: not the raw walking casing.
	for _, group := range groups {
		for _, file := range group.Files {
			require.Equal(t, strings.ToLower(file.Path), file.Path)
		}
	}
}

func TestLoadContextFilesCaseSensitivityPerPlatform(t *testing.T) {
	base := t.TempDir()
	dirA := filepath.Join(base, "Dir")
	dirB := filepath.Join(base, "dir")
	// Distinct on case-sensitive platforms; the same key on Windows
	// (contract v1 case-insensitive).
	require.NotEqual(t, canonicalDedupeKey(dirA, "linux"), canonicalDedupeKey(dirB, "linux"))
	require.Equal(t, canonicalDedupeKey(dirA, "windows"), canonicalDedupeKey(dirB, "windows"))
}

func TestLoadContextFilesMissingPathYieldsEmptyGroup(t *testing.T) {
	store := newTestStore(t, t.TempDir())
	groups := loadContextFiles([]string{filepath.Join(t.TempDir(), "does-not-exist")}, store, "windows")
	require.Len(t, groups, 1)
	require.Empty(t, groups[0].Files)
}

func TestLoadContextFilesSingleRootDoesNotRepeatFiles(t *testing.T) {
	root := t.TempDir()
	writeContextTree(t, root)
	store := newTestStore(t, root)

	groups := loadContextFiles([]string{"."}, store, "windows")
	require.Len(t, groups, 1)

	paths := map[string]struct{}{}
	for _, file := range groups[0].Files {
		paths[file.Path] = struct{}{}
	}
	require.Len(t, paths, len(groups[0].Files), "overlapping roots must not repeat files")
	require.Len(t, groups[0].Files, 2)
}

func TestSnapshotSplitsStableAndDynamic(t *testing.T) {
	p, err := NewPrompt(
		"coder",
		"STABLE-SECTION\n{{/* dynamic-suffix */}}\nDYNAMIC-SECTION",
		WithTimeFunc(timeNowStub),
	)
	require.NoError(t, err)

	store := newTestStore(t, t.TempDir())
	build, err := p.BuildPrompt(t.Context(), "openai", "gpt-5.2", store)
	require.NoError(t, err)
	snapshot := build.Snapshot
	require.NoError(t, err)

	require.Equal(t, "STABLE-SECTION\n", snapshot.StablePrefix)
	require.Equal(t, "\nDYNAMIC-SECTION", snapshot.DynamicSuffix)
	require.Less(t,
		strings.Index(snapshot.String(), "STABLE-SECTION"),
		strings.Index(snapshot.String(), "DYNAMIC-SECTION"),
		"stable prefix bytes must precede dynamic suffix bytes")
}

func timeNowStub() time.Time {
	return time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
}

func TestPromptSkillsRenderSortedAndDeterministic(t *testing.T) {
	p, err := NewPrompt(
		"coder",
		"{{if .AvailSkillXML}}{{.AvailSkillXML}}{{end}}{{/* dynamic-suffix */}}",
		WithTimeFunc(timeNowStub),
	)
	require.NoError(t, err)

	store := newTestStore(t, t.TempDir())
	first := buildSkillPrompt(t, p, store, "b-skill", "a-skill", "c-skill")
	second := buildSkillPrompt(t, p, store, "c-skill", "a-skill", "b-skill")
	require.Equal(t, first, second, "skill input order must not change rendered bytes")
	require.Less(t,
		strings.Index(first, "a-skill"),
		strings.Index(first, "b-skill"),
		"skills must render in sorted order")
}

func buildSkillPrompt(t *testing.T, p *Prompt, store *config.ConfigStore, names ...string) string {
	t.Helper()
	skillList := make([]*skills.Skill, 0, len(names))
	for _, name := range names {
		skillList = append(skillList, &skills.Skill{Name: name, Description: "desc " + name})
	}
	built, err := NewPrompt(
		p.Name(),
		"{{if .AvailSkillXML}}{{.AvailSkillXML}}{{end}}{{/* dynamic-suffix */}}",
		WithTimeFunc(timeNowStub),
		WithSkills(skillList),
	)
	require.NoError(t, err)
	out, err := built.Build(t.Context(), "openai", "gpt-5.2", store)
	require.NoError(t, err)
	return out
}
