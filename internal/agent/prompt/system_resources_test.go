package prompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSystemResourcesPrecedenceAndLiteralContent(t *testing.T) {
	global, project := t.TempDir(), t.TempDir()
	t.Setenv("TACK_GLOBAL_CONFIG", global)
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
	put(filepath.Join(project, ".pi"), "SYSTEM.md", "pi legacy system")
	put(filepath.Join(project, ".pi"), "APPEND_SYSTEM.md", "pi legacy append")
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
	if !strings.Contains(first.Text, "literal {{.Config}}") || strings.Contains(first.Text, "global system") || strings.Contains(first.Text, "pi legacy system") || strings.Contains(first.Text, "expert coding assistant") {
		t.Fatalf("incorrect replacement: %s", first.Text)
	}
	if !strings.Contains(first.Text, "project append") || strings.Contains(first.Text, "global append") || strings.Contains(first.Text, "pi legacy append") {
		t.Fatalf("incorrect append precedence: %s", first.Text)
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
	t.Setenv("TACK_GLOBAL_CONFIG", global)
	path := filepath.Join(global, "SYSTEM.md")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := loadSystemResources(project, true)
	if err != nil || !r.Replace || r.System != "" {
		t.Fatalf("empty override lost: %+v %v", r, err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", maxSystemResourceBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSystemResources(project, true); err == nil {
		t.Fatal("oversized resource accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSystemResources(project, true); err == nil {
		t.Fatal("directory resource accepted")
	}
}

func TestLoadSystemResourcesUntrustedProjectUsesOnlyGlobal(t *testing.T) {
	root := t.TempDir()
	global := filepath.Join(root, "global")
	workdir := filepath.Join(root, "project")
	for _, dir := range []string{global, filepath.Join(workdir, ".tack"), filepath.Join(workdir, ".pi")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(global, "SYSTEM.md"), []byte("global system"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(global, "APPEND_SYSTEM.md"), []byte("global append"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workdir, ".tack", "SYSTEM.md"), []byte("project system"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workdir, ".tack", "APPEND_SYSTEM.md"), []byte("project append"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workdir, ".pi", "SYSTEM.md"), []byte("pi legacy system"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TACK_GLOBAL_CONFIG", global)

	got, err := loadSystemResources(workdir, false)
	if err != nil {
		t.Fatal(err)
	}
	if got.System != "global system" || !got.Replace {
		t.Fatalf("system = %q replace=%v", got.System, got.Replace)
	}
	if got.Append != "global append" {
		t.Fatalf("append = %q", got.Append)
	}
}

func TestLoadSystemResourcesIgnoresCrushGlobalPromptRoot(t *testing.T) {
	legacy := t.TempDir()
	current := t.TempDir()
	project := t.TempDir()
	t.Setenv("CRUSH_GLOBAL_CONFIG", legacy)
	t.Setenv("TACK_GLOBAL_CONFIG", current)
	if err := os.WriteFile(filepath.Join(legacy, "SYSTEM.md"), []byte("legacy crush system"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "APPEND_SYSTEM.md"), []byte("legacy crush append"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := loadSystemResources(project, true)
	if err != nil {
		t.Fatal(err)
	}
	if got.Replace || got.System != "" || got.Append != "" {
		t.Fatalf("CRUSH_GLOBAL_CONFIG prompt resources must be ignored: %+v", got)
	}
}

func TestLoadSystemResourcesNeverFallsBackToPiDirectory(t *testing.T) {
	global, project := t.TempDir(), t.TempDir()
	t.Setenv("TACK_GLOBAL_CONFIG", global)
	legacy := filepath.Join(project, ".pi")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "SYSTEM.md"), []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "APPEND_SYSTEM.md"), []byte("legacy append"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := loadSystemResources(project, true)
	if err != nil {
		t.Fatal(err)
	}
	if got.Replace || got.System != "" || got.Append != "" {
		t.Fatalf(".pi prompt resources must be ignored: %+v", got)
	}
}
