package releaseserver

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// RotatingLog is bounded to the active file plus three backups, without a
// dependency on the core process or a global log/HTTP transport replacement.
type RotatingLog struct {
	mu    sync.Mutex
	file  *os.File
	path  string
	limit int64
	size  int64
}

func OpenLog(path string, maxBytes int64) (*RotatingLog, error) {
	if maxBytes < 1 {
		return nil, fmt.Errorf("log size must be positive")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, fmt.Errorf("log must be a regular file")
	}
	return &RotatingLog{file: file, path: path, limit: maxBytes, size: info.Size()}, nil
}

func (w *RotatingLog) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return 0, os.ErrClosed
	}
	if w.size > 0 && w.size+int64(len(data)) > w.limit {
		if err := w.file.Close(); err != nil {
			return 0, err
		}
		w.file = nil
		// Remove only our exact third backup, never a user-provided directory tree.
		if err := os.Remove(w.path + ".3"); err != nil && !os.IsNotExist(err) {
			return 0, err
		}
		for i := 2; i >= 1; i-- {
			old := fmt.Sprintf("%s.%d", w.path, i)
			if err := os.Rename(old, fmt.Sprintf("%s.%d", w.path, i+1)); err != nil && !os.IsNotExist(err) {
				return 0, err
			}
		}
		if err := os.Rename(w.path, w.path+".1"); err != nil {
			return 0, err
		}
		var err error
		w.file, err = os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			return 0, err
		}
		w.size = 0
	}
	n, err := w.file.Write(data)
	w.size += int64(n)
	return n, err
}
func (w *RotatingLog) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}
