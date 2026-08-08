package ipc

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"syscall"
	"time"
)

// ErrNotRunning reports that nothing is listening on the control socket: the
// socket file is missing, is refusing connections, or is a leftover from a
// crashed daemon. Callers use errors.Is(err, ipc.ErrNotRunning) to decide
// whether to auto-spawn a daemon (see internal/daemon.EnsureDaemon).
var ErrNotRunning = errors.New("banshee daemon is not running")

// DialTimeout bounds connecting to the control socket.
const DialTimeout = 2 * time.Second

// RequestTimeout bounds one write/read exchange once connected.
const RequestTimeout = 5 * time.Second

// Send dials the daemon's control socket and performs one request/response
// exchange. The returned error is ErrNotRunning when no daemon is listening, a
// transport error when the exchange failed, or a daemon-reported error when the
// response carries ok:false — the Response is returned in every case so callers
// can inspect it.
func Send(op, query string) (Response, error) {
	path, err := SocketPath()
	if err != nil {
		return Response{}, fmt.Errorf("ipc: resolve socket path: %w", err)
	}
	// Talking to a socket in a directory somebody else owns would let them
	// answer our ping (which is exactly what EnsureDaemon polls) — see
	// VerifyRuntimeDir.
	if err := verifyParent(path); err != nil {
		return Response{}, err
	}
	return SendTo(path, Request{V: ProtoVersion, Op: op, Query: query})
}

// Ping asks the daemon for its version and whether the launcher is visible.
func Ping() (Response, error) { return Send(OpPing, "") }

// SendTo is Send against an explicit socket path, for tests and for tooling
// that talks to a non-default daemon. A zero req.V is filled in with the
// current protocol version.
func SendTo(socketPath string, req Request) (Response, error) {
	if req.V == 0 {
		req.V = ProtoVersion
	}
	conn, err := net.DialTimeout("unix", socketPath, DialTimeout)
	if err != nil {
		if isNotRunning(err) {
			return Response{}, fmt.Errorf("%w (%s)", ErrNotRunning, socketPath)
		}
		return Response{}, fmt.Errorf("ipc: dial %s: %w", socketPath, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(RequestTimeout))

	line, err := json.Marshal(req)
	if err != nil {
		return Response{}, fmt.Errorf("ipc: encode request: %w", err)
	}
	if _, err := conn.Write(append(line, '\n')); err != nil {
		return Response{}, fmt.Errorf("ipc: write request: %w", err)
	}

	raw, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil && len(raw) == 0 {
		if isNotRunning(err) {
			return Response{}, fmt.Errorf("%w (%s)", ErrNotRunning, socketPath)
		}
		return Response{}, fmt.Errorf("ipc: read response: %w", err)
	}
	var resp Response
	if err := json.Unmarshal(raw, &resp); err != nil {
		return Response{}, fmt.Errorf("ipc: malformed response: %w", err)
	}
	if !resp.OK {
		msg := resp.Error
		if msg == "" {
			msg = "request failed"
		}
		return resp, fmt.Errorf("banshee daemon: %s", msg)
	}
	return resp, nil
}

// isNotRunning classifies dial/read failures that mean "no daemon there" as
// opposed to a real fault worth reporting.
func isNotRunning(err error) bool {
	switch {
	case errors.Is(err, fs.ErrNotExist),
		errors.Is(err, syscall.ENOENT),
		errors.Is(err, syscall.ECONNREFUSED),
		errors.Is(err, syscall.ECONNRESET),
		errors.Is(err, syscall.ENOTSOCK),
		errors.Is(err, syscall.EPIPE):
		return true
	}
	return false
}
