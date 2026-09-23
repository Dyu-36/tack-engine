package prompt

import (
	"log/slog"
	"os"
	"path/filepath"
	"slices"
)

var defaultContextCandidates = []string{
	"AGENTS.override.md",
	"AGENTS.md",
	"AGENTS.MD",
	"CLAUDE.md",
	"CLAUDE.MD",
}

func loadContextFileFromDir(dir, platform string) *ContextFile {
	for _, name := range defaultContextCandidates {
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if err != nil {
			if !os.IsNotExist(err) {
				slog.Warn("Could not inspect prompt context file", "path", path, "error", err)
			}
			continue
		}
		if !info.Mode().IsRegular() {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			slog.Warn("Could not read prompt context file", "path", path, "error", err)
			continue
		}
		return &ContextFile{
			Path:    canonicalRenderPath(path, platform),
			Content: string(trimUTF8BOM(data)),
		}
	}
	return nil
}

func trimUTF8BOM(data []byte) []byte {
	if len(data) >= 3 && data[0] == 0xef && data[1] == 0xbb && data[2] == 0xbf {
		return data[3:]
	}
	return data
}

func loadDefaultContextFiles(workingDir, platform string) (project, global []ContextFile, err error) {
	globalDir, err := promptGlobalConfigDir()
	if err != nil {
		return nil, nil, err
	}

	seen := make(map[string]struct{})
	if file := loadContextFileFromDir(globalDir, platform); file != nil {
		global = appendUniqueContextFile(global, *file, seen, platform)
	}

	var nearestFirst []ContextFile
	for current := filepath.Clean(workingDir); ; {
		if file := loadContextFileFromDir(current, platform); file != nil {
			nearestFirst = append(nearestFirst, *file)
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	slices.Reverse(nearestFirst)
	for _, file := range nearestFirst {
		project = appendUniqueContextFile(project, file, seen, platform)
	}
	return project, global, nil
}

func appendUniqueContextFile(dst []ContextFile, file ContextFile, seen map[string]struct{}, platform string) []ContextFile {
	key := canonicalDedupeKey(file.Path, platform)
	if _, ok := seen[key]; ok {
		return dst
	}
	seen[key] = struct{}{}
	return append(dst, file)
}

func appendUniqueContextGroups(dst []ContextFile, groups []ContextGroup, seen map[string]struct{}, platform string) []ContextFile {
	for _, group := range groups {
		for _, file := range group.Files {
			dst = appendUniqueContextFile(dst, file, seen, platform)
		}
	}
	return dst
}
