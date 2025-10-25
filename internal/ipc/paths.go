package ipc

import (
	"os"
	"path/filepath"
)

// SocketPath returns the preferred Unix domain socket path and ensures
// its parent directory exists with private permissions.
func SocketPath() (string, error) {
	if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
		p := filepath.Join(xdg, "ginkgo.sock")
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return "", err
		}
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	p := filepath.Join(home, ".local", "share", "ginkgo", "ipc.sock")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return "", err
	}
	return p, nil
}
