//go:build (darwin || linux || windows || freebsd || openbsd || netbsd) && !ios && !android

package clipboard

import "golang.design/x/clipboard"

func initClipboard() error {
	return clipboard.Init()
}
