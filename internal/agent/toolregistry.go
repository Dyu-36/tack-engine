package agent

import (
	"slices"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent/prompt"
	"github.com/charmbracelet/crush/internal/agent/tools"
)

// toolSpec is one entry of the explicit Pi-like tool registry: the name the
// model sees, the one-line snippet the prompt advertises, and the constructor.
// The registry is the single source of truth: the prompt renders `Snippet`
// for exactly the entries that are enabled, so removing a tool removes it from
// the prompt too.
type toolSpec struct {
	Name    string
	Snippet string
	Build   func(c *coordinator) fantasy.AgentTool
}

// toolRegistry lists the enabled tools in stable, deterministic order.
func toolRegistry() []toolSpec {
	return []toolSpec{
		{
			Name:    tools.ViewToolName,
			Snippet: "Read file contents",
			Build: func(c *coordinator) fantasy.AgentTool {
				return tools.NewViewTool(c.filetracker, c.skillTracker, c.cfg.WorkingDir(), c.cfg.Config().Options.SkillsPaths...)
			},
		},
		{
			Name:    tools.BashToolName,
			Snippet: "Execute PowerShell commands (dir, Select-String, ...)",
			Build: func(c *coordinator) fantasy.AgentTool {
				return tools.NewBashTool(c.cfg.WorkingDir())
			},
		},
		{
			Name:    tools.EditToolName,
			Snippet: "Make surgical edits to files (find exact text and replace)",
			Build: func(c *coordinator) fantasy.AgentTool {
				return tools.NewEditTool(c.history, c.filetracker, c.cfg.WorkingDir())
			},
		},
		{
			Name:    tools.WriteToolName,
			Snippet: "Create or overwrite files",
			Build: func(c *coordinator) fantasy.AgentTool {
				return tools.NewWriteTool(c.history, c.filetracker, c.cfg.WorkingDir())
			},
		},
		{
			Name:    tools.GrepToolName,
			Snippet: "Search file contents for patterns (respects .gitignore)",
			Build: func(c *coordinator) fantasy.AgentTool {
				return tools.NewGrepTool(c.cfg.WorkingDir(), c.cfg.Config().Tools.Grep)
			},
		},
		{
			Name:    tools.GlobToolName,
			Snippet: "Find files by glob pattern (respects .gitignore)",
			Build: func(c *coordinator) fantasy.AgentTool {
				return tools.NewGlobTool(c.cfg.WorkingDir(), c.cfg.Config().Tools.Glob)
			},
		},
	}
}

// registrySnapshots returns the enabled tool specs and their prompt projection
// for the given tool allowlist.
func registrySnapshots(allowed []string) ([]toolSpec, []prompt.ToolInfo) {
	var (
		specs []toolSpec
		infos []prompt.ToolInfo
	)
	for _, spec := range toolRegistry() {
		if !slices.Contains(allowed, spec.Name) {
			continue
		}
		specs = append(specs, spec)
		infos = append(infos, prompt.ToolInfo{Name: spec.Name, Snippet: spec.Snippet})
	}
	return specs, infos
}

// promptGuidelines mirrors Pi's buildRules: guideline bullets are derived from
// the tools that are actually enabled, so the prompt never mentions a tool
// that is not registered.
func promptGuidelines(enabled []string) []string {
	has := func(name string) bool { return slices.Contains(enabled, name) }

	hasShell := has(tools.BashToolName)
	hasGrep := has(tools.GrepToolName)
	hasGlob := has(tools.GlobToolName)
	hasRead := has(tools.ViewToolName)
	hasEdit := has(tools.EditToolName)
	hasWrite := has(tools.WriteToolName)

	var rules []string

	if hasShell && !hasGrep && !hasGlob {
		rules = append(rules, "Use "+tools.BashToolName+" for file operations like dir, Get-ChildItem, Select-String")
	}
	if hasRead {
		rules = append(rules, "Use "+tools.ViewToolName+" to examine files instead of cat or sed")
	}
	if hasEdit {
		rules = append(rules, "Use edit for precise changes (old text must match exactly)")
	}
	if hasWrite {
		rules = append(rules, "Use write only for new files or complete rewrites")
	}
	rules = append(rules, "Be concise in your responses")
	rules = append(rules, "Show file paths clearly when working with files")

	return rules
}
