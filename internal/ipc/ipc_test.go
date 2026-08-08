package ipc

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// echoHandler answers with the op and query it received so tests can assert the
// request survived the round trip intact.
func echoHandler(req Request) Response {
	switch req.Op {
	case OpPing:
		return Response{OK: true, Version: "test", Visible: req.Query == "visible"}
	case OpHide:
		return Response{Error: "nothing to hide"}
	default:
		return Response{OK: true, Version: req.Op + ":" + req.Query}
	}
}

func newTestServer(t *testing.T, h Handler) *Server {
	t.Helper()
	// Short path: unix socket paths are capped at ~108 bytes.
	dir, err := os.MkdirTemp("", "bansheeipc")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	srv, err := Listen(filepath.Join(dir, "b.sock"), h)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { srv.Close() })
	return srv
}

func TestSendToRoundTrip(t *testing.T) {
	srv := newTestServer(t, echoHandler)

	tests := []struct {
		name        string
		req         Request
		wantOK      bool
		wantVersion string
		wantVisible bool
		wantErrSub  string
	}{
		{name: "toggle with query", req: Request{Op: OpToggle, Query: "blacksh"}, wantOK: true, wantVersion: "toggle:blacksh"},
		{name: "show", req: Request{Op: OpShow}, wantOK: true, wantVersion: "show:"},
		{name: "ping", req: Request{Op: OpPing}, wantOK: true, wantVersion: "test"},
		{name: "ping visible", req: Request{Op: OpPing, Query: "visible"}, wantOK: true, wantVersion: "test", wantVisible: true},
		{name: "explicit version", req: Request{V: ProtoVersion, Op: OpReload}, wantOK: true, wantVersion: "reload:"},
		{name: "handler error", req: Request{Op: OpHide}, wantErrSub: "nothing to hide"},
		{name: "missing op", req: Request{Op: ""}, wantErrSub: "missing op"},
		{name: "future protocol", req: Request{V: ProtoVersion + 1, Op: OpPing}, wantErrSub: "unsupported protocol version"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := SendTo(srv.Path(), tt.req)
			if tt.wantErrSub != "" {
				if err == nil {
					t.Fatalf("SendTo(%+v) = %+v, want error containing %q", tt.req, resp, tt.wantErrSub)
				}
				if !strings.Contains(err.Error(), tt.wantErrSub) {
					t.Fatalf("error = %v, want it to contain %q", err, tt.wantErrSub)
				}
				if resp.OK {
					t.Fatalf("response ok=true alongside error %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("SendTo(%+v): %v", tt.req, err)
			}
			if resp.OK != tt.wantOK {
				t.Errorf("ok = %v, want %v", resp.OK, tt.wantOK)
			}
			if resp.Version != tt.wantVersion {
				t.Errorf("version = %q, want %q", resp.Version, tt.wantVersion)
			}
			if resp.Visible != tt.wantVisible {
				t.Errorf("visible = %v, want %v", resp.Visible, tt.wantVisible)
			}
		})
	}
}

// TestServerRawLines drives the wire format directly, including inputs the Go
// client would never produce.
func TestServerRawLines(t *testing.T) {
	srv := newTestServer(t, echoHandler)

	tests := []struct {
		name       string
		send       string
		wantOK     bool
		wantErrSub string
	}{
		{name: "valid", send: `{"v":1,"op":"toggle"}` + "\n", wantOK: true},
		{name: "no version defaults to v1", send: `{"op":"show"}` + "\n", wantOK: true},
		{name: "unknown fields ignored", send: `{"v":1,"op":"show","future":{"a":1},"query":"x"}` + "\n", wantOK: true},
		{name: "no trailing newline", send: `{"v":1,"op":"show"}`, wantOK: true},
		{name: "malformed json", send: "{not json\n", wantErrSub: "malformed request"},
		{name: "empty object", send: "{}\n", wantErrSub: "missing op"},
		{name: "bare newline", send: "\n", wantErrSub: "malformed request"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn, err := net.Dial("unix", srv.Path())
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
			if _, err := conn.Write([]byte(tt.send)); err != nil {
				t.Fatalf("write: %v", err)
			}
			if !strings.HasSuffix(tt.send, "\n") {
				// The server reads until EOF or newline; half-close so it sees EOF.
				if uc, ok := conn.(*net.UnixConn); ok {
					_ = uc.CloseWrite()
				}
			}
			raw, err := bufio.NewReader(conn).ReadBytes('\n')
			if err != nil && len(raw) == 0 {
				t.Fatalf("read: %v", err)
			}
			var resp Response
			if err := json.Unmarshal(raw, &resp); err != nil {
				t.Fatalf("unmarshal %q: %v", raw, err)
			}
			if resp.OK != tt.wantOK {
				t.Errorf("ok = %v (error %q), want %v", resp.OK, resp.Error, tt.wantOK)
			}
			if tt.wantErrSub != "" && !strings.Contains(resp.Error, tt.wantErrSub) {
				t.Errorf("error = %q, want it to contain %q", resp.Error, tt.wantErrSub)
			}
		})
	}
}

func TestSendToNotRunning(t *testing.T) {
	dir, err := os.MkdirTemp("", "bansheeipc")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	regular := filepath.Join(dir, "regular")
	if err := os.WriteFile(regular, []byte("not a socket\n"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	tests := []struct {
		name string
		path string
	}{
		{name: "missing socket", path: filepath.Join(dir, "absent.sock")},
		{name: "missing directory", path: filepath.Join(dir, "nope", "absent.sock")},
		{name: "not a socket", path: regular},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := SendTo(tt.path, Request{Op: OpPing})
			if !errors.Is(err, ErrNotRunning) {
				t.Fatalf("SendTo(%s) error = %v, want ErrNotRunning", tt.path, err)
			}
		})
	}
}

// TestListenReplacesStaleSocket covers the crash-recovery path: the previous
// daemon died without unlinking its socket.
func TestListenReplacesStaleSocket(t *testing.T) {
	dir, err := os.MkdirTemp("", "bansheeipc")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "b.sock")

	// Simulate a crashed daemon: the socket file outlives the listener.
	addr, err := net.ResolveUnixAddr("unix", path)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	stale, err := net.ListenUnix("unix", addr)
	if err != nil {
		t.Fatalf("listen stale: %v", err)
	}
	stale.SetUnlinkOnClose(false)
	if err := stale.Close(); err != nil {
		t.Fatalf("close stale: %v", err)
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("stale socket did not survive: %v", err)
	}

	srv2, err := Listen(path, echoHandler)
	if err != nil {
		t.Fatalf("Listen over stale socket: %v", err)
	}
	defer srv2.Close()

	if _, err := SendTo(path, Request{Op: OpPing}); err != nil {
		t.Fatalf("ping new server: %v", err)
	}
}

func TestListenRejectsNonSocketPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "regular")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if _, err := Listen(path, echoHandler); err == nil {
		t.Fatal("Listen over a regular file succeeded, want error")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("Listen removed a non-socket file: %v", err)
	}
}

func TestListenValidatesArgs(t *testing.T) {
	if _, err := Listen(filepath.Join(t.TempDir(), "s.sock"), nil); err == nil {
		t.Error("Listen with nil handler succeeded, want error")
	}
	if _, err := Listen("", echoHandler); err == nil {
		t.Error("Listen with empty path succeeded, want error")
	}
}

func TestServerCloseStopsServing(t *testing.T) {
	srv := newTestServer(t, echoHandler)
	if _, err := SendTo(srv.Path(), Request{Op: OpPing}); err != nil {
		t.Fatalf("ping before close: %v", err)
	}
	if err := srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := srv.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := SendTo(srv.Path(), Request{Op: OpPing}); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("ping after close error = %v, want ErrNotRunning", err)
	}
}

func TestServerConcurrentRequests(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]int{}
	srv := newTestServer(t, func(req Request) Response {
		mu.Lock()
		seen[req.Query]++
		mu.Unlock()
		return Response{OK: true, Version: req.Query}
	})

	const n = 16
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			q := string(rune('a' + i))
			resp, err := SendTo(srv.Path(), Request{Op: OpShow, Query: q})
			if err == nil && resp.Version != q {
				err = errors.New("response/request mismatch: " + resp.Version + " != " + q)
			}
			errs[i] = err
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("request %d: %v", i, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != n {
		t.Errorf("handler saw %d distinct queries, want %d", len(seen), n)
	}
}

// TestRequestLineIsBounded proves the control socket cannot be made to buffer
// without limit by a client that never sends a newline.
func TestRequestLineIsBounded(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "banshee.sock")
	srv, err := Listen(sock, func(req Request) Response { return Response{OK: true} })
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

	// Twice the cap, no newline anywhere.
	chunk := bytes.Repeat([]byte("a"), 32<<10)
	for written := 0; written < 2*MaxRequestBytes; written += len(chunk) {
		if _, err := conn.Write(chunk); err != nil {
			break // the server stopped reading, which is the point
		}
	}

	// The server answers (with a protocol error) rather than buffering forever.
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		t.Fatalf("no response to an oversized request: %v", err)
	}
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		t.Fatalf("response %q: %v", line, err)
	}
	if resp.OK {
		t.Fatalf("oversized request was accepted: %+v", resp)
	}
}

// TestVerifyRuntimeDir covers the guard on the $XDG_RUNTIME_DIR fallback: when
// the variable is unset the socket and lock land in /tmp/banshee, which any
// local user can create first — and Listen unlinks whatever socket it finds
// there, while a client's readiness ping cannot tell a hijacked socket from
// the real daemon.
func TestVerifyRuntimeDir(t *testing.T) {
	t.Run("a private directory is accepted", func(t *testing.T) {
		if err := VerifyRuntimeDir(t.TempDir()); err != nil {
			t.Fatalf("VerifyRuntimeDir = %v, want nil", err)
		}
	})

	t.Run("a world-writable directory is tightened", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "banshee")
		if err := os.Mkdir(dir, 0o777); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o777); err != nil {
			t.Fatal(err)
		}
		if err := VerifyRuntimeDir(dir); err != nil {
			t.Fatalf("VerifyRuntimeDir = %v, want nil", err)
		}
		fi, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm()&0o022 != 0 {
			t.Fatalf("mode = %v, want no group/world write", fi.Mode().Perm())
		}
	})

	t.Run("a file is rejected", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "notadir")
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := VerifyRuntimeDir(p); err == nil {
			t.Fatal("expected an error for a non-directory")
		}
	})

	t.Run("a missing directory is rejected", func(t *testing.T) {
		if err := VerifyRuntimeDir(filepath.Join(t.TempDir(), "nope")); err == nil {
			t.Fatal("expected an error for a missing directory")
		}
	})
}
