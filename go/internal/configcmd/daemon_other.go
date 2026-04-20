//go:build !darwin && !linux

package configcmd

import (
	"fmt"
	"runtime"
)

// Controller returns an error on unsupported platforms.
func Controller() (DaemonController, error) {
	return nil, fmt.Errorf("rex: daemon control not supported on %s — run `rex serve` in the foreground", runtime.GOOS)
}
