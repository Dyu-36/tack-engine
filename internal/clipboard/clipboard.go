// Package clipboard provides cross-platform clipboard initialization behind
// build-tag guards so that unsupported platforms (e.g., Android, iOS) compile
// without requiring CGO or platform-specific dependencies.
package clipboard

import "errors"

// ErrUnsupported is returned when clipboard access is not available on
// the current platform.
var ErrUnsupported = errors.New("clipboard operations are not supported on this platform")

// Init initializes the clipboard subsystem. On unsupported platforms it
// returns ErrUnsupported but is otherwise safe to call.
func Init() error {
	return initClipboard()
}
