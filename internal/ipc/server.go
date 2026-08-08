package ipc

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Handler answers a single decoded request. One goroutine per connection calls
// it, so implementations must be safe for concurrent use. The daemon's handler
// forwards work to the GTK main loop and waits for the result.
type Handler func(Request) Response

// ConnTimeout bounds a single client exchange (read the request line, write the
// response). A wedged client can never pin a connection goroutine for longer.
const ConnTimeout = 10 * time.Second

// MaxRequestBytes bounds one request line. A legitimate {v,op,query} line is a
// few dozen bytes; without a cap a client that streams without ever sending a
// newline could make the daemon buffer everything it can push before
// ConnTimeout fires.
const MaxRequestBytes = 64 << 10

// Server is a listening control socket. Create one with Listen; it accepts in
// the background until Close.
type Server struct {
	ln      net.Listener
	path    string
	handler Handler

	wg sync.WaitGroup

	mu     sync.Mutex
	closed bool
}

// Listen creates the unix socket at socketPath (mode 0600, parent directory
// 0700) and serves requests with h until Close is called.
//
// A socket file left behind by a crashed daemon is unlinked first. Callers are
// expected to hold the single-instance lock (see internal/daemon) before
// calling Listen, which is what makes that unlink safe: only the process that
// owns the lock may own the socket. Non-socket files at socketPath are never
// removed, and the parent directory is checked (see VerifyRuntimeDir) so the
// unlink cannot be aimed at us by whoever created the directory first.
func Listen(socketPath string, h Handler) (*Server, error) {
	if h == nil {
		return nil, errors.New("ipc: Listen requires a handler")
	}
	if socketPath == "" {
		return nil, errors.New("ipc: Listen requires a socket path")
	}
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		return nil, fmt.Errorf("ipc: create socket dir: %w", err)
	}
	// The unlink below is only safe while this directory is ours alone.
	if err := verifyParent(socketPath); err != nil {
		return nil, err
	}
	if err := removeStaleSocket(socketPath); err != nil {
		return nil, err
	}
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("ipc: listen on %s: %w", socketPath, err)
	}
	// Best effort: the runtime dir is already 0700, this narrows the socket too.
	_ = os.Chmod(socketPath, 0o600)

	s := &Server{ln: ln, path: socketPath, handler: h}
	s.wg.Add(1)
	go s.serve()
	return s, nil
}

// Path returns the socket path the server listens on.
func (s *Server) Path() string { return s.path }

// Close stops accepting, waits for in-flight requests to be answered and
// removes the socket file. It is safe to call more than once.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()

	err := s.ln.Close() // *net.UnixListener unlinks the socket file for us
	s.wg.Wait()
	return err
}

func (s *Server) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *Server) serve() {
	defer s.wg.Done()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			if s.isClosed() {
				return
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			// Transient per-connection failures (EMFILE, ECONNABORTED) must not
			// kill the daemon's control socket; back off briefly and retry.
			if errors.Is(err, net.ErrClosed) {
				return
			}
			time.Sleep(10 * time.Millisecond)
			continue
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handleConn(conn)
		}()
	}
}

// handleConn reads exactly one newline-delimited request and writes exactly one
// newline-delimited response.
func (s *Server) handleConn(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(ConnTimeout))

	line, err := bufio.NewReader(io.LimitReader(conn, MaxRequestBytes)).ReadBytes('\n')
	if err != nil && len(bytes.TrimSpace(line)) == 0 {
		// Client hung up (or timed out) without sending anything: nothing to answer.
		return
	}
	resp := s.answer(line)
	out, err := json.Marshal(resp)
	if err != nil {
		out = []byte(`{"ok":false,"error":"internal: response is not serializable"}`)
	}
	_, _ = conn.Write(append(out, '\n'))
}

// answer decodes one request line and runs the handler. Protocol-level failures
// (bad JSON, a version the daemon predates, a missing op) are answered here so
// handlers only ever see well-formed requests.
func (s *Server) answer(line []byte) Response {
	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		return Response{Error: "malformed request: " + err.Error()}
	}
	if req.V > ProtoVersion {
		return Response{Error: fmt.Sprintf("unsupported protocol version %d (daemon speaks v%d)", req.V, ProtoVersion)}
	}
	if req.Op == "" {
		return Response{Error: "missing op"}
	}
	return s.handler(req)
}

// removeStaleSocket unlinks socketPath when it is a leftover socket file.
func removeStaleSocket(socketPath string) error {
	fi, err := os.Lstat(socketPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("ipc: stat %s: %w", socketPath, err)
	}
	if fi.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("ipc: %s exists and is not a socket", socketPath)
	}
	if err := os.Remove(socketPath); err != nil {
		return fmt.Errorf("ipc: remove stale socket %s: %w", socketPath, err)
	}
	return nil
}
