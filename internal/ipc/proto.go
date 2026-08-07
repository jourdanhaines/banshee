// Package ipc implements the daemon control socket: newline-delimited JSON,
// one request per connection, over a unix socket in $XDG_RUNTIME_DIR.
//
// proto.go is a frozen Phase-0 contract (protocol v1). Additive changes only.
package ipc

import (
	"os"
	"path/filepath"
)

// ProtoVersion is the IPC protocol version carried in every request.
const ProtoVersion = 1

// Ops accepted by the daemon.
const (
	OpToggle = "toggle"
	OpShow   = "show"
	OpHide   = "hide"
	OpReload = "reload" // re-read config, reindex repos, rescan plugins
	OpPing   = "ping"
	OpQuit   = "quit"
)

// Request is one client message.
type Request struct {
	V  int    `json:"v"`
	Op string `json:"op"`
	// Query optionally prefills the search box (show/toggle).
	Query string `json:"query,omitempty"`
}

// Response is the daemon's reply.
type Response struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	// Ping extras.
	Version string `json:"version,omitempty"`
	Visible bool   `json:"visible,omitempty"`
}

// RuntimeDir returns the banshee runtime directory, creating it 0700.
// Falls back to /tmp when $XDG_RUNTIME_DIR is unset.
func RuntimeDir() (string, error) {
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" {
		base = os.TempDir()
	}
	dir := filepath.Join(base, "banshee")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// SocketPath returns the control socket path.
func SocketPath() (string, error) {
	dir, err := RuntimeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "banshee.sock"), nil
}

// LockPath returns the single-instance flock path.
func LockPath() (string, error) {
	dir, err := RuntimeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "banshee.lock"), nil
}
