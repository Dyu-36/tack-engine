package fsext

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/charmbracelet/crush/internal/home"
)

// LookupClosestBounded behaves like LookupClosest but constrains the
// upward search to stopDir. The walk inspects dir, then each ancestor up
// to and including stopDir, then terminates regardless of whether the
// target was found. Use this when the caller wants to avoid adopting
// matches from outside a project boundary (for example a sibling
// worktree or a parent project).
//
// If stopDir is empty, only dir itself is searched. If stopDir is not an
// ancestor of dir, the walk still terminates at the filesystem root.
// The $HOME and ownership safeguards from LookupClosest are preserved
// as outer bounds.
func LookupClosestBounded(dir, stopDir, target string) (string, bool) {
	var found string

	err := traverseUpBounded(dir, stopDir, func(cwd string, owner int) error {
		fpath := filepath.Join(cwd, target)

		err := probeEnt(fpath, owner)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}

		if err != nil {
			return fmt.Errorf("error probing file %s: %w", fpath, err)
		}

		if cwd == home.Dir() {
			return filepath.SkipAll
		}

		found = fpath
		return filepath.SkipAll
	})

	return found, err == nil && found != ""
}

// LookupBounded behaves like Lookup but constrains the upward search to
// stopDir. The walk inspects dir, then each ancestor up to and including
// stopDir, then terminates. If stopDir is empty, only dir itself is
// searched.
func LookupBounded(dir, stopDir string, targets ...string) ([]string, error) {
	if len(targets) == 0 {
		return nil, nil
	}

	var found []string

	err := traverseUpBounded(dir, stopDir, func(cwd string, owner int) error {
		for _, target := range targets {
			fpath := filepath.Join(cwd, target)
			err := probeEnt(fpath, owner)

			// skip to the next file on permission denied
			if errors.Is(err, os.ErrNotExist) ||
				errors.Is(err, os.ErrPermission) {
				continue
			}

			if err != nil {
				return fmt.Errorf("error probing file %s: %w", fpath, err)
			}

			found = append(found, fpath)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return found, nil
}

// traverseUpBounded walks up from dir, visiting each ancestor up to and
// including stopDir, then terminates. If stopDir is empty, only dir
// itself is visited; callers that want an unbounded walk should use
// traverseUp instead. If stopDir is set but is not an ancestor of dir
// the walk still stops at the filesystem root, so callers cannot
// accidentally produce an infinite walk by passing a sibling path.
//
// Boundary comparison is performed against symlink-resolved paths so
// that callers passing logically equivalent paths (a symlinked /var vs
// the underlying /private/var, for example) still terminate at the
// expected directory.
func traverseUpBounded(dir, stopDir string, walkFn func(dir string, owner int) error) error {
	cwd, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("cannot convert CWD to absolute path: %w", err)
	}

	stop := cwd
	if stopDir != "" {
		stop, err = filepath.Abs(stopDir)
		if err != nil {
			return fmt.Errorf("cannot convert stop dir to absolute path: %w", err)
		}
	}
	canonStop := canonicalize(stop)

	owner, err := Owner(dir)
	if err != nil {
		return fmt.Errorf("cannot get ownership: %w", err)
	}

	for {
		err := walkFn(cwd, owner)
		if err == nil || errors.Is(err, filepath.SkipDir) {
			if canonicalize(cwd) == canonStop {
				return nil
			}

			parent := filepath.Dir(cwd)
			if parent == cwd {
				return nil
			}

			cwd = parent
			continue
		}

		if errors.Is(err, filepath.SkipAll) {
			return nil
		}

		return err
	}
}

// canonicalize resolves any symbolic links in path. If resolution fails
// (typically because path does not exist yet) the original path is
// returned cleaned, so callers can still perform stable equality checks.
func canonicalize(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
}

// probeEnt checks if entity at given path exists and belongs to given owner
func probeEnt(fspath string, owner int) error {
	_, err := os.Stat(fspath)
	if err != nil {
		return fmt.Errorf("cannot stat %s: %w", fspath, err)
	}

	// special case for ownership check bypass
	if owner == -1 {
		return nil
	}

	fowner, err := Owner(fspath)
	if err != nil {
		return fmt.Errorf("cannot get ownership for %s: %w", fspath, err)
	}

	if fowner != owner {
		return os.ErrPermission
	}

	return nil
}
