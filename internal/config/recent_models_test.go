package config

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// readConfigJSON reads and unmarshals the JSON config file at path.
func readConfigJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	baseDir := filepath.Dir(path)
	fileName := filepath.Base(path)
	b, err := fs.ReadFile(os.DirFS(baseDir), fileName)
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(b, &out))
	return out
}

// readRecentModels reads the recent_models section from the config file.
func readRecentModels(t *testing.T, path string) map[string]any {
	t.Helper()
	out := readConfigJSON(t, path)
	rm, ok := out["recent_models"].(map[string]any)
	require.True(t, ok)
	return rm
}

// testStoreWithPath creates a ConfigStore backed by a Config for recent model tests.
func testStoreWithPath(cfg *Config, dir string) *ConfigStore {
	return &ConfigStore{
		config:         cfg,
		globalDataPath: filepath.Join(dir, "config.json"),
	}
}

// configWithRecents builds a Config seeded with the given recent models for
// the large type, for exercising the pure nextRecentModels helper.
func configWithRecents(recents ...SelectedModel) *Config {
	return &Config{
		RecentModels: map[SelectedModelType][]SelectedModel{
			SelectedModelTypeLarge: recents,
		},
	}
}

func TestNextRecentModels_AddsToFront(t *testing.T) {
	t.Parallel()

	cfg := configWithRecents()
	updated, changed := nextRecentModels(cfg, SelectedModelTypeLarge, SelectedModel{Provider: "openai", Model: "gpt-4o"})
	require.True(t, changed)
	require.Equal(t, []SelectedModel{{Provider: "openai", Model: "gpt-4o"}}, updated)
}

func TestNextRecentModels_DedupeAndMoveToFront(t *testing.T) {
	t.Parallel()

	cfg := configWithRecents(
		SelectedModel{Provider: "anthropic", Model: "claude"},
		SelectedModel{Provider: "openai", Model: "gpt-4o"},
	)
	updated, changed := nextRecentModels(cfg, SelectedModelTypeLarge, SelectedModel{Provider: "openai", Model: "gpt-4o"})
	require.True(t, changed)
	require.Equal(t, []SelectedModel{
		{Provider: "openai", Model: "gpt-4o"},
		{Provider: "anthropic", Model: "claude"},
	}, updated)
}

func TestNextRecentModels_TrimsToMax(t *testing.T) {
	t.Parallel()

	var seed []SelectedModel
	for _, id := range []string{"m5", "m4", "m3", "m2", "m1"} {
		seed = append(seed, SelectedModel{Provider: "p", Model: id})
	}
	cfg := configWithRecents(seed...)

	updated, changed := nextRecentModels(cfg, SelectedModelTypeLarge, SelectedModel{Provider: "p", Model: "m6"})
	require.True(t, changed)
	require.Len(t, updated, maxRecentModelsPerType)
	require.Equal(t, SelectedModel{Provider: "p", Model: "m6"}, updated[0])
	require.Equal(t, SelectedModel{Provider: "p", Model: "m2"}, updated[maxRecentModelsPerType-1])
}

func TestNextRecentModels_SkipsEmptyValues(t *testing.T) {
	t.Parallel()

	cfg := configWithRecents()
	_, changed := nextRecentModels(cfg, SelectedModelTypeLarge, SelectedModel{Provider: "", Model: "m"})
	require.False(t, changed)
	_, changed = nextRecentModels(cfg, SelectedModelTypeLarge, SelectedModel{Provider: "p", Model: ""})
	require.False(t, changed)
}

func TestNextRecentModels_NoChangeWhenAlreadyFront(t *testing.T) {
	t.Parallel()

	entry := SelectedModel{Provider: "openai", Model: "gpt-4o"}
	cfg := configWithRecents(entry)
	_, changed := nextRecentModels(cfg, SelectedModelTypeLarge, entry)
	require.False(t, changed)
}

func TestUpdatePreferredModel_PersistsModelAndRecents(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfg := &Config{}
	cfg.setDefaults(dir, "")
	store := testStoreWithPath(cfg, dir)

	sel := SelectedModel{Provider: "openai", Model: "gpt-4o"}
	require.NoError(t, store.UpdatePreferredModel(ScopeGlobal, SelectedModelTypeLarge, sel))

	// in-memory state (read through the store; copy-on-write publishes a
	// new Config, so the seed cfg pointer is intentionally unchanged).
	require.Equal(t, sel, store.Config().Models[SelectedModelTypeLarge])
	require.Len(t, store.Config().RecentModels[SelectedModelTypeLarge], 1)

	// persisted state
	rm := readRecentModels(t, store.globalDataPath)
	large, ok := rm[string(SelectedModelTypeLarge)].([]any)
	require.True(t, ok)
	require.Len(t, large, 1)
	item := large[0].(map[string]any)
	require.Equal(t, "openai", item["provider"])
	require.Equal(t, "gpt-4o", item["model"])
}

func TestUpdatePreferredModel_TypeIsolation(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfg := &Config{}
	cfg.setDefaults(dir, "")
	store := testStoreWithPath(cfg, dir)

	largeModel := SelectedModel{Provider: "openai", Model: "gpt-4o"}
	smallModel := SelectedModel{Provider: "anthropic", Model: "claude"}
	require.NoError(t, store.UpdatePreferredModel(ScopeGlobal, SelectedModelTypeLarge, largeModel))
	require.NoError(t, store.UpdatePreferredModel(ScopeGlobal, SelectedModelTypeSmall, smallModel))

	// Adding to large leaves small untouched.
	anotherLarge := SelectedModel{Provider: "google", Model: "gemini"}
	require.NoError(t, store.UpdatePreferredModel(ScopeGlobal, SelectedModelTypeLarge, anotherLarge))

	require.Len(t, store.Config().RecentModels[SelectedModelTypeLarge], 2)
	require.Equal(t, anotherLarge, store.Config().RecentModels[SelectedModelTypeLarge][0])
	require.Len(t, store.Config().RecentModels[SelectedModelTypeSmall], 1)
	require.Equal(t, smallModel, store.Config().RecentModels[SelectedModelTypeSmall][0])
}

func TestUpdatePreferredModels_SetsPairWithRecentsAndPins(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfg := &Config{}
	cfg.setDefaults(dir, "")
	store := testStoreWithPath(cfg, dir)

	large := SelectedModel{Provider: "openai", Model: "gpt-5"}
	small := SelectedModel{Provider: "openai", Model: "gpt-5-mini"}
	require.NoError(t, store.UpdatePreferredModels(ScopeGlobal, map[SelectedModelType]*SelectedModel{
		SelectedModelTypeLarge: &large,
		SelectedModelTypeSmall: &small,
	}))

	require.Equal(t, large, store.Config().Models[SelectedModelTypeLarge])
	require.Equal(t, small, store.Config().Models[SelectedModelTypeSmall])
	require.Equal(t, large, store.overrides.Models[SelectedModelTypeLarge])
	require.Equal(t, small, store.overrides.Models[SelectedModelTypeSmall])
	require.Equal(t, large, store.Config().RecentModels[SelectedModelTypeLarge][0])
	require.Equal(t, small, store.Config().RecentModels[SelectedModelTypeSmall][0])

	persisted := readConfigJSON(t, store.globalDataPath)
	models := persisted["models"].(map[string]any)
	require.Equal(t, "gpt-5", models["large"].(map[string]any)["model"])
	require.Equal(t, "gpt-5-mini", models["small"].(map[string]any)["model"])
}

func TestUpdatePreferredModels_RemovesPairButKeepsRecents(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfg := &Config{}
	cfg.setDefaults(dir, "")
	store := testStoreWithPath(cfg, dir)

	large := SelectedModel{Provider: "openai", Model: "gpt-5"}
	small := SelectedModel{Provider: "openai", Model: "gpt-5-mini"}
	require.NoError(t, store.UpdatePreferredModels(ScopeGlobal, map[SelectedModelType]*SelectedModel{
		SelectedModelTypeLarge: &large,
		SelectedModelTypeSmall: &small,
	}))
	require.NoError(t, store.UpdatePreferredModels(ScopeGlobal, map[SelectedModelType]*SelectedModel{
		SelectedModelTypeLarge: nil,
		SelectedModelTypeSmall: nil,
	}))

	require.NotContains(t, store.Config().Models, SelectedModelTypeLarge)
	require.NotContains(t, store.Config().Models, SelectedModelTypeSmall)
	require.NotContains(t, store.overrides.Models, SelectedModelTypeLarge)
	require.NotContains(t, store.overrides.Models, SelectedModelTypeSmall)
	require.Equal(t, large, store.Config().RecentModels[SelectedModelTypeLarge][0])
	require.Equal(t, small, store.Config().RecentModels[SelectedModelTypeSmall][0])

	persisted := readConfigJSON(t, store.globalDataPath)
	models := persisted["models"].(map[string]any)
	require.NotContains(t, models, "large")
	require.NotContains(t, models, "small")
	require.Contains(t, persisted["recent_models"].(map[string]any), "large")
	require.Contains(t, persisted["recent_models"].(map[string]any), "small")
}

func TestUpdatePreferredModels_RejectsInvalidInputWithoutPublishing(t *testing.T) {
	t.Parallel()

	valid := SelectedModel{Provider: "openai", Model: "gpt-5"}
	incomplete := SelectedModel{Provider: "openai"}
	tests := []struct {
		name    string
		scope   Scope
		updates map[SelectedModelType]*SelectedModel
	}{
		{name: "empty map", scope: ScopeGlobal, updates: map[SelectedModelType]*SelectedModel{}},
		{name: "unknown type", scope: ScopeGlobal, updates: map[SelectedModelType]*SelectedModel{"medium": &valid}},
		{name: "incomplete model", scope: ScopeGlobal, updates: map[SelectedModelType]*SelectedModel{SelectedModelTypeLarge: &incomplete}},
		{name: "unknown scope", scope: Scope(99), updates: map[SelectedModelType]*SelectedModel{SelectedModelTypeLarge: &valid}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			cfg := &Config{}
			cfg.setDefaults(dir, "")
			store := testStoreWithPath(cfg, dir)
			before := store.Config()

			err := store.UpdatePreferredModels(tt.scope, tt.updates)
			require.ErrorIs(t, err, ErrInvalidConfigMutation)
			require.Same(t, before, store.Config())
			require.Empty(t, store.overrides.Models)
		})
	}
}
