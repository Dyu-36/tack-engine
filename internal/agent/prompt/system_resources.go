package prompt

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const maxSystemResourceBytes = 256 * 1024

type systemResources struct {
	System  string
	Replace bool
	Append  string
}

func loadSystemResources(workingDir string) (systemResources, error) {
	global := strings.TrimSpace(os.Getenv("CRUSH_GLOBAL_CONFIG"))
	if global == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return systemResources{}, fmt.Errorf("system prompt directory: %w", err)
		}
		global = filepath.Join(dir, "crush")
	}
	roots := []string{filepath.Join(workingDir, ".tack"), filepath.Join(workingDir, ".pi")}
	result := systemResources{}
	text, found, err := readFirstSystemResource(roots, "SYSTEM.md")
	if err != nil {
		return result, err
	}
	if !found {
		text, found, err = readFirstSystemResource([]string{global}, "SYSTEM.md")
		if err != nil {
			return result, err
		}
	}
	result.System, result.Replace = text, found
	globalAppend, _, err := readFirstSystemResource([]string{global}, "APPEND_SYSTEM.md")
	if err != nil {
		return result, err
	}
	projectAppend, _, err := readFirstSystemResource(roots, "APPEND_SYSTEM.md")
	if err != nil {
		return result, err
	}
	var sections []string
	for _, section := range []string{globalAppend, projectAppend} {
		if section != "" {
			sections = append(sections, section)
		}
	}
	result.Append = strings.Join(sections, "\n\n")
	return result, nil
}

func readFirstSystemResource(roots []string, name string) (string, bool, error) {
	for _, root := range roots {
		path := filepath.Join(root, name)
		file, err := os.Open(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", false, fmt.Errorf("read system resource %s: %w", path, err)
		}
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() {
			file.Close()
			return "", false, fmt.Errorf("system resource %s must be a regular file", path)
		}
		data, readErr := io.ReadAll(io.LimitReader(file, maxSystemResourceBytes+1))
		closeErr := file.Close()
		if readErr != nil {
			return "", false, fmt.Errorf("read system resource %s: %w", path, readErr)
		}
		if closeErr != nil {
			return "", false, closeErr
		}
		if len(data) > maxSystemResourceBytes {
			return "", false, fmt.Errorf("system resource %s exceeds %d bytes", path, maxSystemResourceBytes)
		}
		return strings.TrimPrefix(string(data), "\ufeff"), true, nil
	}
	return "", false, nil
}
