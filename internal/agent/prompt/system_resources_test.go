package prompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSystemResourcesPrecedenceAndLiteralContent(t *testing.T) {
	global, project := t.TempDir(), t.TempDir()
	t.Setenv("CRUSH_GLOBAL_CONFIG", global)
	put := func(root, name, text string) {
		t.Helper()
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	put(global, "SYSTEM.md", "global system")
	put(global, "APPEND_SYSTEM.md", "global append")
	put(filepath.Join(project, ".pi"), "SYSTEM.md", "pi fallback")
	put(filepath.Join(project, ".tack"), "SYSTEM.md", "literal {{.Config}}")
	put(filepath.Join(project, ".tack"), "APPEND_SYSTEM.md", "project append")
	p, err := NewPrompt("coder", coderTemplate(t))
	if err != nil {
		t.Fatal(err)
	}
	store := newTestStore(t, project)
	first, err := p.BuildPrompt(t.Context(), "test", "model", store)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first.Text, "literal {{.Config}}") || strings.Contains(first.Text, "global system") || strings.Contains(first.Text, "expert coding assistant") {
		t.Fatalf("incorrect replacement: %s", first.Text)
	}
	if !strings.Contains(first.Text, "global append\n\nproject append") {
		t.Fatal(first.Text)
	}
	put(filepath.Join(project, ".tack"), "SYSTEM.md", "changed")
	second, err := p.BuildPrompt(t.Context(), "test", "model", store)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(second.Text, "changed") {
		t.Fatal("resource edit not reloaded")
	}
	if len(second.Generation.ChangedStable(first.Generation)) == 0 {
		t.Fatal("replacement did not invalidate stable generation")
	}
}

func TestSystemResourcesEmptyOverrideAndLimits(t *testing.T) {
	global, project := t.TempDir(), t.TempDir()
	t.Setenv("CRUSH_GLOBAL_CONFIG", global)
	path := filepath.Join(global, "SYSTEM.md")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := loadSystemResources(project)
	if err != nil || !r.Replace || r.System != "" {
		t.Fatalf("empty override lost: %+v %v", r, err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", maxSystemResourceBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSystemResources(project); err == nil {
		t.Fatal("oversized resource accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSystemResources(project); err == nil {
		t.Fatal("directory resource accepted")
	}
}
