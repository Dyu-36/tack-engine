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
	Sources []string
}

func promptGlobalConfigDir() (string, error) {
	if global := strings.TrimSpace(os.Getenv("TACK_GLOBAL_CONFIG")); global != "" {
		return filepath.Clean(global), nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("system prompt directory: %w", err)
	}
	return filepath.Join(dir, "gotack", "engine", "prompt-config"), nil
}

func loadSystemResources(workingDir string, projectTrusted bool) (systemResources, error) {
	global, err := promptGlobalConfigDir()
	if err != nil {
		return systemResources{}, err
	}
	project := filepath.Join(workingDir, ".tack")
	result := systemResources{}

	if projectTrusted {
		text, source, found, readErr := readSystemResource(project, "SYSTEM.md")
		if readErr != nil {
			return result, readErr
		}
		if found {
			result.System = text
			result.Replace = true
			result.Sources = append(result.Sources, source)
		}
	}
	if !result.Replace {
		text, source, found, readErr := readSystemResource(global, "SYSTEM.md")
		if readErr != nil {
			return result, readErr
		}
		if found {
			result.System = text
			result.Replace = true
			result.Sources = append(result.Sources, source)
		}
	}

	if projectTrusted {
		text, source, found, readErr := readSystemResource(project, "APPEND_SYSTEM.md")
		if readErr != nil {
			return result, readErr
		}
		if found {
			result.Append = text
			result.Sources = append(result.Sources, source)
			return result, nil
		}
	}
	text, source, found, readErr := readSystemResource(global, "APPEND_SYSTEM.md")
	if readErr != nil {
		return result, readErr
	}
	if found {
		result.Append = text
		result.Sources = append(result.Sources, source)
	}
	return result, nil
}

func readSystemResource(root, name string) (string, string, bool, error) {
	path := filepath.Join(root, name)
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, fmt.Errorf("read system resource %s: %w", path, err)
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return "", "", false, fmt.Errorf("system resource %s must be a regular file", path)
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxSystemResourceBytes+1))
	closeErr := file.Close()
	if readErr != nil {
		return "", "", false, fmt.Errorf("read system resource %s: %w", path, readErr)
	}
	if closeErr != nil {
		return "", "", false, closeErr
	}
	if len(data) > maxSystemResourceBytes {
		return "", "", false, fmt.Errorf("system resource %s exceeds %d bytes", path, maxSystemResourceBytes)
	}
	return strings.TrimPrefix(string(data), "\ufeff"), filepath.ToSlash(path), true, nil
}
