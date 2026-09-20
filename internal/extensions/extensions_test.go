package extensions

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"charm.land/fantasy"
)

func TestManagerHonorsProjectTrustAndRejectsDuplicateTools(t *testing.T) {
	root := t.TempDir()
	global := filepath.Join(root, "global")
	project := filepath.Join(root, "project")
	globalExt := filepath.Join(global, "extensions", "global")
	projectExt := filepath.Join(project, ".pi", "extensions", "project")
	writeTestExtension(t, globalExt, "global-ext", "global_tool")
	writeTestExtension(t, projectExt, "project-ext", "project_tool")

	manager := NewManager(project, global)
	if err := manager.Refresh(false, nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := manager.Snapshot().Tools; len(got) != 1 || got[0].Manifest.Name != "global_tool" {
		t.Fatalf("untrusted tools = %#v", got)
	}

	if err := manager.Refresh(true, nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := manager.Snapshot().Tools; len(got) != 2 {
		t.Fatalf("trusted tools = %#v", got)
	}

	writeTestExtension(t, filepath.Join(project, ".tack", "extensions", "duplicate"), "duplicate-ext", "global_tool")
	if err := manager.Refresh(true, nil, nil); err == nil {
		t.Fatal("duplicate tool was accepted")
	}
}

func TestManagerHonorsDisabledExtensions(t *testing.T) {
	root := t.TempDir()
	global := filepath.Join(root, "global")
	project := filepath.Join(root, "project")
	writeTestExtension(t, filepath.Join(global, "extensions", "one"), "one", "one_tool")
	writeTestExtension(t, filepath.Join(global, "extensions", "two"), "two", "two_tool")
	manager := NewManager(project, global)
	if err := manager.Refresh(false, nil, []string{"one"}); err != nil {
		t.Fatal(err)
	}
	got := manager.Snapshot().Tools
	if len(got) != 1 || got[0].Extension != "two" {
		t.Fatalf("disabled extension still loaded: %#v", got)
	}
}

func TestExternalToolProtocol(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell helper fixture is unix-only")
	}
	root := t.TempDir()
	executable := filepath.Join(root, "echo-extension")
	script := `#!/bin/sh
read line
printf '%s' "$line" | grep -q '"protocol":"gotack-extension/1"' || exit 8
printf '{"content":"ok","metadata":{"verified":true}}\n'
`
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	tool := Tool{
		Extension: "test",
		Source: filepath.Join(root, ManifestName),
		Manifest: ToolManifest{
			Name: "echo", Description: "echo",
			Parameters: map[string]any{"value": map[string]any{"type": "string"}},
			Required: []string{"value"},
		},
		executable: executable,
		timeout: defaultTimeout,
	}
	call := tool.AgentTool(root)
	response, err := call.Run(context.Background(), structToToolCall(t, map[string]any{"value": "x"}))
	if err != nil {
		t.Fatal(err)
	}
	if response.IsError || response.Content != "ok" {
		t.Fatalf("response = %#v", response)
	}
}

func TestLoadManifestRejectsTrailingJSONAndInvalidRequiredField(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "extension")
	if runtime.GOOS == "windows" {
		executable += ".exe"
	}
	if err := os.WriteFile(executable, []byte("fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ManifestName)
	valid := `{"schema":1,"name":"x","executable":"` + filepath.Base(executable) + `","tools":[{"name":"tool","description":"x","parameters":{}}]}`
	if err := os.WriteFile(path, []byte(valid+"{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadManifest(path); err == nil {
		t.Fatal("trailing JSON accepted")
	}
	invalidRequired := `{"schema":1,"name":"x","executable":"` + filepath.Base(executable) + `","tools":[{"name":"tool","description":"x","parameters":{},"required":["missing"]}]}`
	if err := os.WriteFile(path, []byte(invalidRequired), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadManifest(path); err == nil {
		t.Fatal("unknown required parameter accepted")
	}
}

func structToToolCall(t *testing.T, input any) fantasy.ToolCall {
	t.Helper()
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	return fantasy.ToolCall{ID: "call-1", Name: "echo", Input: string(data)}
}

func writeTestExtension(t *testing.T, dir, name, toolName string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(dir, "extension")
	if runtime.GOOS == "windows" {
		executable += ".exe"
	}
	if err := os.WriteFile(executable, []byte("fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{
		Schema: 1, Name: name, Executable: filepath.Base(executable),
		Tools: []ToolManifest{{Name: toolName, Description: "test tool", Parameters: map[string]any{}}},
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ManifestName), data, 0o600); err != nil {
		t.Fatal(err)
	}
}
