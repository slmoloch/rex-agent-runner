// Package applog provides the rex daemon's rotating log writer.
//
// The Writer appends to <dir>/rex.YYYY-MM-DD.log, rolling lazily on the first
// write of a new day. With RedirectStdio enabled, FDs 1 and 2 are dup'd onto
// the active file each roll, so anything writing to os.Stdout/os.Stderr
// (panics, third-party prints) ends up in the same file as slog output.
package applog

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

type Writer struct {
	dir           string
	redirectStdio bool

	mu      sync.Mutex
	file    *os.File
	dateKey string
}

// New opens (or creates and appends to) <dir>/rex.<today>.log. If
// redirectStdio is true, the process's stdout and stderr file descriptors
// are pointed at the same file so direct writes to os.Stdout/os.Stderr —
// including Go runtime panics — also land in rex.log.
func New(dir string, redirectStdio bool) (*Writer, error) {
	w := &Writer{dir: dir, redirectStdio: redirectStdio}
	if err := w.rollLocked(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if today := time.Now().Format("2006-01-02"); today != w.dateKey {
		if err := w.rollLocked(); err != nil {
			return 0, err
		}
	}
	return w.file.Write(p)
}

func (w *Writer) rollLocked() error {
	today := time.Now().Format("2006-01-02")
	if err := os.MkdirAll(w.dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(w.dir, fmt.Sprintf("rex.%s.log", today))
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	old := w.file
	w.file = f
	w.dateKey = today
	if w.redirectStdio {
		fd := int(f.Fd())
		_ = unix.Dup2(fd, 1)
		_ = unix.Dup2(fd, 2)
	}
	if old != nil {
		_ = old.Close()
	}
	return nil
}
