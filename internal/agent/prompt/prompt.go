package prompt

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"text/template"
	"time"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/filepathext"
	"github.com/charmbracelet/crush/internal/home"
	"github.com/charmbracelet/crush/internal/shell"
	"github.com/charmbracelet/crush/internal/skills"
)

// Prompt represents a template-based prompt generator.
type Prompt struct {
	name         string
	template     string
	now          func() time.Time
	platform     string
	workingDir   string
	activeSkills []*skills.Skill
}

const dynamicSuffixMarker = "{{/* dynamic-suffix */}}"

type PromptDat struct {
	Provider           string
	Model              string
	Config             config.Config
	WorkingDir         string
	IsGitRepo          bool
	Platform           string
	Date               string
	GitStatus          string
	ContextFiles       []ContextFile
	GlobalContextFiles []ContextFile
	AvailSkillXML      string
}

type ContextFile struct {
	Path    string
	Content string
}

type ContextGroup struct {
	Key   string
	Files []ContextFile
}

type Snapshot struct {
	StablePrefix  string
	DynamicSuffix string
}

func (s Snapshot) String() string {
	if s.DynamicSuffix == "" {
		return s.StablePrefix
	}
	return s.StablePrefix + "\n\n<dynamic-context>\n" + s.DynamicSuffix + "\n</dynamic-context>"
}

// generationComponentKind labels one diffable prompt input class. Stable
// kinds rotate the stable prefix generation (model, template, context and
// skills); dynamic kinds never do (date, git). The set is closed: the
// telemetry reason mapping only understands these kinds.
const (
	componentTemplate = "template"
	componentModel    = "model"
	componentContext  = "context"
	componentSkills   = "skills"
	componentDate     = "date"
	componentGit      = "git"
)

// Generation captures a labeled content digest for every prompt input
// class so a diff between two builds names exactly which class changed.
// Change reasons are derived from this diff, never from comparing final
// prompt hashes (ImplementPlan 0.2/0.3). Digests are internal diff keys;
// only the diff result reaches telemetry, never the digests themselves.
type Generation struct {
	Stable  map[string]string
	Dynamic map[string]string
}

// ChangedStable returns the stable component kinds whose digests differ
// between prev and cur, sorted ascending. A nil prev means nothing is
// reported changed (the first observation is not a change).
func (g *Generation) ChangedStable(prev *Generation) []string {
	return changedKinds(prevStableMap(prev), g.Stable)
}

// ChangedDynamic returns the dynamic component kinds whose digests
// differ between prev and cur, sorted ascending.
func (g *Generation) ChangedDynamic(prev *Generation) []string {
	return changedKinds(prevDynamicMap(prev), g.Dynamic)
}

func prevStableMap(prev *Generation) map[string]string {
	if prev == nil {
		return nil
	}
	return prev.Stable
}

func prevDynamicMap(prev *Generation) map[string]string {
	if prev == nil {
		return nil
	}
	return prev.Dynamic
}

func changedKinds(prev, cur map[string]string) []string {
	if cur == nil || prev == nil {
		return nil
	}
	var changed []string
	for kind, digest := range cur {
		if previous, ok := prev[kind]; ok && previous != digest {
			changed = append(changed, kind)
		}
	}
	sort.Strings(changed)
	return changed
}

// PromptBuild is one complete prompt construction: the concatenated text,
// the stable/dynamic split and the labeled generation used for change
// reasoning. All three come from the same promptData pass, so they can
// never describe different inputs.
type PromptBuild struct {
	Text       string
	Snapshot   Snapshot
	Generation *Generation
}

type Option func(*Prompt)

func WithTimeFunc(fn func() time.Time) Option {
	return func(p *Prompt) {
		p.now = fn
	}
}

func WithPlatform(platform string) Option {
	return func(p *Prompt) {
		p.platform = platform
	}
}

func WithWorkingDir(workingDir string) Option {
	return func(p *Prompt) {
		p.workingDir = workingDir
	}
}

func WithSkills(active []*skills.Skill) Option {
	return func(p *Prompt) {
		p.activeSkills = append([]*skills.Skill(nil), active...)
	}
}

func NewPrompt(name, promptTemplate string, opts ...Option) (*Prompt, error) {
	p := &Prompt{
		name:     name,
		template: promptTemplate,
		now:      time.Now,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p, nil
}

func (p *Prompt) Build(ctx context.Context, provider, model string, store *config.ConfigStore) (string, error) {
	build, err := p.BuildPrompt(ctx, provider, model, store)
	if err != nil {
		return "", err
	}
	return build.Text, nil
}

// BuildPrompt renders the prompt once and returns the text together with
// the stable/dynamic snapshot and the labeled generation. Every builder
// path (initial build, refresh endpoint, pre-run refresh) must use this
// single entry point so all consumers describe the same inputs.
func (p *Prompt) BuildPrompt(ctx context.Context, provider, model string, store *config.ConfigStore) (PromptBuild, error) {
	d, err := p.promptData(ctx, provider, model, store)
	if err != nil {
		return PromptBuild{}, err
	}
	stableTemplate, dynamicTemplate, _ := strings.Cut(p.template, dynamicSuffixMarker)
	stable, err := executeTemplate(p.name+"-stable", stableTemplate, d)
	if err != nil {
		return PromptBuild{}, err
	}
	dynamic, err := executeTemplate(p.name+"-dynamic", dynamicTemplate, d)
	if err != nil {
		return PromptBuild{}, err
	}
	return PromptBuild{
		Text:     (&Snapshot{StablePrefix: stable, DynamicSuffix: dynamic}).String(),
		Snapshot: Snapshot{StablePrefix: stable, DynamicSuffix: dynamic},
		Generation: &Generation{
			Stable: map[string]string{
				componentTemplate: contentDigest(p.template),
				componentModel:    contentDigest(provider + "\x00" + model),
				componentContext:  contentDigest(contextManifestDigest(d)),
				componentSkills:   contentDigest(d.AvailSkillXML),
			},
			Dynamic: map[string]string{
				componentDate: contentDigest(d.Date),
				componentGit:  contentDigest(fmt.Sprintf("%t\x00%s", d.IsGitRepo, d.GitStatus)),
			},
		},
	}, nil
}

func contentDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// contextManifestDigest is the canonical identity of both context lanes:
// ordered (lane, path, content digest) triples. Paths are the rendered
// canonical bytes, so alias casings or input order cannot change the
// manifest.
func contextManifestDigest(d PromptDat) string {
	var b strings.Builder
	appendLane := func(label string, files []ContextFile) {
		sorted := append([]ContextFile(nil), files...)
		slices.SortFunc(sorted, func(a, b ContextFile) int {
			return strings.Compare(a.Path, b.Path)
		})
		for _, file := range sorted {
			b.WriteString(label)
			b.WriteByte('\x00')
			b.WriteString(file.Path)
			b.WriteByte('\x00')
			b.WriteString(contentDigest(file.Content))
			b.WriteByte('\n')
		}
	}
	appendLane("project", d.ContextFiles)
	appendLane("global", d.GlobalContextFiles)
	return b.String()
}

func executeTemplate(name, source string, data PromptDat) (string, error) {
	if source == "" {
		return "", nil
	}
	t, err := template.New(name).Parse(source)
	if err != nil {
		return "", fmt.Errorf("parsing template: %w", err)
	}
	var output strings.Builder
	if err := t.Execute(&output, data); err != nil {
		return "", fmt.Errorf("executing template: %w", err)
	}
	return output.String(), nil
}

func processFile(filePath string) *ContextFile {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil
	}
	return &ContextFile{
		Path:    filePath,
		Content: string(content),
	}
}

func processContextPath(p string, store *config.ConfigStore) []ContextFile {
	var contexts []ContextFile
	fullPath := filepathext.SmartJoin(store.WorkingDir(), p)
	info, err := os.Stat(fullPath)
	if err != nil {
		return contexts
	}
	if info.IsDir() {
		filepath.WalkDir(fullPath, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() {
				if result := processFile(path); result != nil {
					contexts = append(contexts, *result)
				}
			}
			return nil
		})
	} else {
		result := processFile(fullPath)
		if result != nil {
			contexts = append(contexts, *result)
		}
	}
	return contexts
}

// expandPath expands ~ and environment variables in file paths
func expandPath(path string, store *config.ConfigStore) string {
	path = home.Long(path)
	// Handle environment variable expansion using the same pattern as config
	if strings.HasPrefix(path, "$") {
		if expanded, err := store.Resolver().ResolveValue(path); err == nil {
			path = expanded
		}
	}

	return path
}

// loadContextFiles loads and deduplicates context files from a list of
// paths. Dedupe keys are canonical per-OS: on Windows the filesystem is
// case-insensitive (contract v1), so alias casings of the same root are
// the same group; on case-sensitive filesystems case distinguishes
// paths. The rendered file path bytes are canonicalized identically, so
// input order can never change the rendered prompt. Groups are sorted
// lexically by key and files lexically within a group; nonexistent paths
// yield empty groups rather than errors.
func loadContextFiles(paths []string, store *config.ConfigStore, platform string) []ContextGroup {
	groups := make([]ContextGroup, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	// renderedFiles dedupes repeated files caused by overlapping roots
	// within this lane (e.g. "." and "sub"); project and global lanes
	// stay separate by design.
	renderedFiles := make(map[string]struct{})
	for _, pth := range paths {
		expanded := expandPath(pth, store)
		resolved := filepathext.SmartJoin(store.WorkingDir(), expanded)
		absolute, err := filepath.Abs(filepath.Clean(resolved))
		if err != nil {
			absolute = filepath.Clean(resolved)
		}
		pathKey := canonicalDedupeKey(absolute, platform)
		if _, ok := seen[pathKey]; ok {
			continue
		}
		seen[pathKey] = struct{}{}
		files := processContextPath(absolute, store)
		unique := files[:0:0]
		for _, file := range files {
			file.Path = canonicalRenderPath(file.Path, platform)
			// Rendered bytes must be identical no matter which alias
			// casing the user configured.
			fileKey := canonicalDedupeKey(file.Path, platform)
			if _, dup := renderedFiles[fileKey]; dup {
				continue
			}
			renderedFiles[fileKey] = struct{}{}
			unique = append(unique, file)
		}
		slices.SortFunc(unique, func(a, b ContextFile) int {
			return strings.Compare(a.Path, b.Path)
		})
		groups = append(groups, ContextGroup{Key: pathKey, Files: unique})
	}
	slices.SortFunc(groups, func(a, b ContextGroup) int {
		return strings.Compare(a.Key, b.Key)
	})
	return groups
}

// canonicalDedupeKey folds case only on Windows where the filesystem is
// case-insensitive. Other platforms keep original case.
func canonicalDedupeKey(path, platform string) string {
	if platform == "windows" {
		return strings.ToLower(path)
	}
	return path
}

// canonicalRenderPath produces the rendered path bytes for the prompt.
// On Windows the whole path is folded to lower case (including the
// drive component) so alias casings render identically; separators are
// normalized to forward slashes. Other platforms keep the path as-is.
func canonicalRenderPath(path, platform string) string {
	path = filepath.ToSlash(path)
	if platform != "windows" {
		return path
	}
	return strings.ToLower(path)
}

func (p *Prompt) promptData(ctx context.Context, provider, model string, store *config.ConfigStore) (PromptDat, error) {
	workingDir := cmp.Or(p.workingDir, store.WorkingDir())
	platform := cmp.Or(p.platform, runtime.GOOS)

	cfg := store.Config()
	contextFiles := loadContextFiles(cfg.Options.ContextPaths, store, platform)
	globalContextFiles := loadContextFiles(cfg.Options.GlobalContextPaths, store, platform)

	var availSkillXML string
	if len(p.activeSkills) > 0 {
		activeSkills := append([]*skills.Skill(nil), p.activeSkills...)
		slices.SortFunc(activeSkills, func(a, b *skills.Skill) int {
			return strings.Compare(a.Name, b.Name)
		})
		availSkillXML = skills.ToPromptXML(activeSkills)
	}

	isGit := isGitRepo(store.WorkingDir())
	data := PromptDat{
		Provider:      provider,
		Model:         model,
		Config:        *cfg,
		WorkingDir:    filepath.ToSlash(workingDir),
		IsGitRepo:     isGit,
		Platform:      platform,
		Date:          p.now().Format("1/2/2006"),
		AvailSkillXML: availSkillXML,
	}
	if isGit {
		var err error
		data.GitStatus, err = getGitStatus(ctx, store.WorkingDir())
		if err != nil {
			return PromptDat{}, err
		}
	}

	for _, group := range contextFiles {
		data.ContextFiles = append(data.ContextFiles, group.Files...)
	}
	for _, group := range globalContextFiles {
		data.GlobalContextFiles = append(data.GlobalContextFiles, group.Files...)
	}
	return data, nil
}

func isGitRepo(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

func getGitStatus(ctx context.Context, dir string) (string, error) {
	sh := shell.NewShell(&shell.Options{
		WorkingDir: dir,
	})
	branch, err := getGitBranch(ctx, sh)
	if err != nil {
		return "", err
	}
	status, err := getGitStatusSummary(ctx, sh)
	if err != nil {
		return "", err
	}
	commits, err := getGitRecentCommits(ctx, sh)
	if err != nil {
		return "", err
	}
	return branch + status + commits, nil
}

func getGitBranch(ctx context.Context, sh *shell.Shell) (string, error) {
	out, _, err := sh.Exec(ctx, "git branch --show-current 2>/dev/null")
	if err != nil {
		return "", nil
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return "", nil
	}
	return fmt.Sprintf("Current branch: %s\n", out), nil
}

func getGitStatusSummary(ctx context.Context, sh *shell.Shell) (string, error) {
	out, _, err := sh.Exec(ctx, "git status --short 2>/dev/null | head -20")
	if err != nil {
		return "", nil
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return "Status: clean\n", nil
	}
	return fmt.Sprintf("Status:\n%s\n", out), nil
}

func getGitRecentCommits(ctx context.Context, sh *shell.Shell) (string, error) {
	out, _, err := sh.Exec(ctx, "git log --oneline -n 3 2>/dev/null")
	if err != nil || out == "" {
		return "", nil
	}
	out = strings.TrimSpace(out)
	return fmt.Sprintf("Recent commits:\n%s\n", out), nil
}

func (p *Prompt) Name() string {
	return p.name
}
