package extensions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"charm.land/fantasy"
)

const (
	ManifestName     = "extension.json"
	ProtocolVersion  = "gotack-extension/1"
	defaultTimeout   = 120 * time.Second
	maxTimeout       = 30 * time.Minute
	maxStdoutBytes   = 2 << 20
	maxStderrBytes   = 64 << 10
	maxManifestBytes = 512 << 10
)

var safeName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,127}$`)
var safeToolName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,127}$`)

type Manifest struct {
	Schema         int            `json:"schema"`
	Name           string         `json:"name"`
	Executable     string         `json:"executable"`
	Args           []string       `json:"args,omitempty"`
	TimeoutSeconds int            `json:"timeout_seconds,omitempty"`
	Tools          []ToolManifest `json:"tools"`
}

type ToolManifest struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters,omitempty"`
	Required    []string       `json:"required,omitempty"`
	Parallel    bool           `json:"parallel,omitempty"`
}

type Tool struct {
	Extension  string
	Source     string
	Manifest   ToolManifest
	executable string
	args       []string
	timeout    time.Duration
}

type Snapshot struct {
	Tools []Tool
}

type Manager struct {
	workingDir string
	globalDir  string
	mu         sync.RWMutex
	snapshot   Snapshot
}

func NewManager(workingDir, globalDir string) *Manager {
	return &Manager{workingDir: workingDir, globalDir: globalDir}
}

func (m *Manager) Refresh(projectTrusted bool, additionalPaths, disabledExtensions []string) error {
	roots := make([]string, 0, 4+len(additionalPaths))
	if strings.TrimSpace(m.globalDir) != "" {
		roots = append(roots, filepath.Join(m.globalDir, "extensions"))
	}
	for _, path := range additionalPaths {
		if strings.TrimSpace(path) == "" {
			continue
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(m.workingDir, path)
		}
		roots = append(roots, filepath.Clean(path))
	}
	if projectTrusted {
		roots = append(roots,
			filepath.Join(m.workingDir, ".tack", "extensions"),
			filepath.Join(m.workingDir, ".pi", "extensions"),
		)
	}
	roots = uniquePaths(roots)
	var tools []Tool
	extensionNames := map[string]string{}
	toolNames := map[string]string{}
	for _, root := range roots {
		discovered, err := discoverRoot(root)
		if err != nil {
			return err
		}
		for _, extension := range discovered {
			if slices.Contains(disabledExtensions, extension.manifest.Name) {
				continue
			}
			if previous, exists := extensionNames[extension.manifest.Name]; exists {
				return fmt.Errorf("extension %q is declared by both %s and %s", extension.manifest.Name, previous, extension.source)
			}
			extensionNames[extension.manifest.Name] = extension.source
			for _, tool := range extension.tools {
				if previous, exists := toolNames[tool.Manifest.Name]; exists {
					return fmt.Errorf("extension tool %q is declared by both %s and %s", tool.Manifest.Name, previous, extension.source)
				}
				toolNames[tool.Manifest.Name] = extension.source
				tools = append(tools, tool)
			}
		}
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Manifest.Name < tools[j].Manifest.Name })
	m.mu.Lock()
	m.snapshot = Snapshot{Tools: tools}
	m.mu.Unlock()
	return nil
}

func (m *Manager) Snapshot() Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return Snapshot{Tools: append([]Tool(nil), m.snapshot.Tools...)}
}

type discoveredExtension struct {
	manifest Manifest
	source   string
	tools    []Tool
}

func discoverRoot(root string) ([]discoveredExtension, error) {
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read extension root %s: %w", root, err)
	}
	var manifests []string
	if info, err := os.Stat(filepath.Join(root, ManifestName)); err == nil && info.Mode().IsRegular() {
		manifests = append(manifests, filepath.Join(root, ManifestName))
	}
	for _, entry := range entries {
		if entry.IsDir() {
			path := filepath.Join(root, entry.Name(), ManifestName)
			if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
				manifests = append(manifests, path)
			}
		}
	}
	sort.Strings(manifests)
	result := make([]discoveredExtension, 0, len(manifests))
	for _, path := range manifests {
		extension, err := loadManifest(path)
		if err != nil {
			return nil, err
		}
		result = append(result, extension)
	}
	return result, nil
}

func loadManifest(path string) (discoveredExtension, error) {
	file, err := os.Open(path)
	if err != nil {
		return discoveredExtension{}, err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxManifestBytes+1))
	closeErr := file.Close()
	if readErr != nil {
		return discoveredExtension{}, fmt.Errorf("read extension manifest %s: %w", path, readErr)
	}
	if closeErr != nil {
		return discoveredExtension{}, closeErr
	}
	if len(data) > maxManifestBytes {
		return discoveredExtension{}, fmt.Errorf("extension manifest %s exceeds %d bytes", path, maxManifestBytes)
	}
	var manifest Manifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return discoveredExtension{}, fmt.Errorf("decode extension manifest %s: %w", path, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return discoveredExtension{}, fmt.Errorf("extension manifest %s contains trailing data", path)
	}
	if manifest.Schema != 1 {
		return discoveredExtension{}, fmt.Errorf("extension manifest %s uses unsupported schema %d", path, manifest.Schema)
	}
	if !safeName.MatchString(manifest.Name) {
		return discoveredExtension{}, fmt.Errorf("extension manifest %s has invalid name %q", path, manifest.Name)
	}
	if len(manifest.Tools) == 0 {
		return discoveredExtension{}, fmt.Errorf("extension %q has no tools", manifest.Name)
	}
	base := filepath.Dir(path)
	executable, err := resolveExecutable(base, manifest.Executable)
	if err != nil {
		return discoveredExtension{}, fmt.Errorf("extension %q: %w", manifest.Name, err)
	}
	timeout := defaultTimeout
	if manifest.TimeoutSeconds != 0 {
		timeout = time.Duration(manifest.TimeoutSeconds) * time.Second
		if timeout <= 0 || timeout > maxTimeout {
			return discoveredExtension{}, fmt.Errorf("extension %q timeout must be between 1 and %d seconds", manifest.Name, int(maxTimeout/time.Second))
		}
	}
	seen := map[string]struct{}{}
	tools := make([]Tool, 0, len(manifest.Tools))
	for _, spec := range manifest.Tools {
		if !safeToolName.MatchString(spec.Name) {
			return discoveredExtension{}, fmt.Errorf("extension %q has invalid tool name %q", manifest.Name, spec.Name)
		}
		if strings.TrimSpace(spec.Description) == "" {
			return discoveredExtension{}, fmt.Errorf("extension %q tool %q is missing a description", manifest.Name, spec.Name)
		}
		if _, exists := seen[spec.Name]; exists {
			return discoveredExtension{}, fmt.Errorf("extension %q repeats tool %q", manifest.Name, spec.Name)
		}
		seen[spec.Name] = struct{}{}
		if spec.Parameters == nil {
			spec.Parameters = map[string]any{}
		}
		for name := range spec.Parameters {
			if !safeToolName.MatchString(name) {
				return discoveredExtension{}, fmt.Errorf("extension %q tool %q has invalid parameter name %q", manifest.Name, spec.Name, name)
			}
		}
		requiredSeen := map[string]struct{}{}
		for _, required := range spec.Required {
			if _, ok := spec.Parameters[required]; !ok {
				return discoveredExtension{}, fmt.Errorf("extension %q tool %q requires unknown parameter %q", manifest.Name, spec.Name, required)
			}
			if _, duplicate := requiredSeen[required]; duplicate {
				return discoveredExtension{}, fmt.Errorf("extension %q tool %q repeats required parameter %q", manifest.Name, spec.Name, required)
			}
			requiredSeen[required] = struct{}{}
		}
		tools = append(tools, Tool{Extension: manifest.Name, Source: path, Manifest: spec, executable: executable, args: append([]string(nil), manifest.Args...), timeout: timeout})
	}
	return discoveredExtension{manifest: manifest, source: path, tools: tools}, nil
}

func resolveExecutable(base, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("executable is required")
	}
	path := value
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	path = filepath.Clean(path)
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("executable %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("executable %s is not a regular file", path)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("executable %s is not executable", path)
	}
	return path, nil
}

func uniquePaths(paths []string) []string {
	seen := map[string]struct{}{}
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		absolute, err := filepath.Abs(filepath.Clean(path))
		if err != nil {
			absolute = filepath.Clean(path)
		}
		key := absolute
		if runtime.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, absolute)
	}
	return result
}

type Request struct {
	Protocol  string          `json:"protocol"`
	Method    string          `json:"method"`
	Extension string          `json:"extension"`
	Tool      string          `json:"tool"`
	CallID    string          `json:"call_id"`
	Workspace string          `json:"workspace"`
	Input     json.RawMessage `json:"input"`
}

type Response struct {
	Content  string `json:"content"`
	IsError  bool   `json:"is_error,omitempty"`
	StopTurn bool   `json:"stop_turn,omitempty"`
	Metadata any    `json:"metadata,omitempty"`
}

func (t Tool) AgentTool(workingDir string) fantasy.AgentTool {
	return &externalTool{tool: t, workingDir: workingDir}
}

type externalTool struct {
	tool            Tool
	workingDir      string
	providerOptions fantasy.ProviderOptions
}

func (t *externalTool) Info() fantasy.ToolInfo {
	return fantasy.ToolInfo{
		Name:       t.tool.Manifest.Name,
		Description: t.tool.Manifest.Description,
		Parameters: t.tool.Manifest.Parameters,
		Required:   append([]string(nil), t.tool.Manifest.Required...),
		Parallel:   t.tool.Manifest.Parallel,
	}
}

func (t *externalTool) ProviderOptions() fantasy.ProviderOptions { return t.providerOptions }

func (t *externalTool) SetProviderOptions(options fantasy.ProviderOptions) {
	t.providerOptions = options
}

func (t *externalTool) Run(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	input := json.RawMessage(call.Input)
	if len(input) == 0 || !json.Valid(input) {
		return fantasy.NewTextErrorResponse("extension tool received invalid JSON input"), nil
	}
	request := Request{
		Protocol: ProtocolVersion, Method: "tool.call", Extension: t.tool.Extension,
		Tool: t.tool.Manifest.Name, CallID: call.ID, Workspace: t.workingDir, Input: input,
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return fantasy.ToolResponse{}, err
	}
	runCtx, cancel := context.WithTimeout(ctx, t.tool.timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, t.tool.executable, t.tool.args...)
	cmd.Dir = filepath.Dir(t.tool.Source)
	cmd.Env = append(os.Environ(), "GOTACK_EXTENSION_PROTOCOL="+ProtocolVersion, "GOTACK_WORKSPACE="+t.workingDir)
	configureCommand(cmd)
	cmd.Cancel = func() error { return killProcessTree(cmd) }
	cmd.Stdin = bytes.NewReader(append(payload, '\n'))
	var stdout, stderr limitedBuffer
	stdout.limit = maxStdoutBytes
	stderr.limit = maxStderrBytes
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		return fantasy.ToolResponse{}, fmt.Errorf("start extension %q: %w", t.tool.Extension, err)
	}
	waitErr := cmd.Wait()
	if runCtx.Err() != nil {
		return fantasy.NewTextErrorResponse(fmt.Sprintf("extension tool %q timed out or was cancelled", t.tool.Manifest.Name)), nil
	}
	if stdout.exceeded || stderr.exceeded {
		return fantasy.NewTextErrorResponse(fmt.Sprintf("extension tool %q exceeded its output limit", t.tool.Manifest.Name)), nil
	}
	if waitErr != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = waitErr.Error()
		}
		return fantasy.NewTextErrorResponse(fmt.Sprintf("extension tool %q failed: %s", t.tool.Manifest.Name, message)), nil
	}
	var response Response
	decoder := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&response); err != nil {
		return fantasy.NewTextErrorResponse(fmt.Sprintf("extension tool %q returned invalid JSON: %v", t.tool.Manifest.Name, err)), nil
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fantasy.NewTextErrorResponse(fmt.Sprintf("extension tool %q returned trailing data", t.tool.Manifest.Name)), nil
	}
	var result fantasy.ToolResponse
	if response.IsError {
		result = fantasy.NewTextErrorResponse(response.Content)
	} else {
		result = fantasy.NewTextResponse(response.Content)
	}
	result.StopTurn = response.StopTurn
	if response.Metadata != nil {
		result = fantasy.WithResponseMetadata(result, response.Metadata)
	}
	return result, nil
}

type limitedBuffer struct {
	bytes.Buffer
	limit    int
	exceeded bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	original := len(p)
	remaining := b.limit - b.Len()
	if remaining <= 0 {
		b.exceeded = true
		return original, nil
	}
	if len(p) > remaining {
		b.exceeded = true
		_, _ = b.Buffer.Write(p[:remaining])
		return original, nil
	}
	_, _ = b.Buffer.Write(p)
	return original, nil
}
