package sessionctl

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

// sunPathMax is the longest socket path (excluding the trailing NUL) that
// net.Listen("unix", ...) can bind on the current platform. The kernel's
// sockaddr_un.sun_path field is 108 bytes on Linux and 104 bytes on
// Darwin/BSD; reserving one byte for the NUL terminator yields a usable max
// of 107 on Linux and 103 on Darwin/BSD.
func sunPathMax() int {
	if runtime.GOOS == "darwin" {
		return 103 // sun_path 104 incl NUL on Darwin/BSD
	}
	return 107 // sun_path 108 incl NUL on Linux
}

// Server listens on a Unix socket (and optionally HTTP -- deferred) and
// dispatches Requests to the per-verb Handlers in Opts.
type Server struct {
	opts   Opts
	ln     net.Listener
	httpLn net.Listener // HTTP mode deferred to CDC-15
	wg     sync.WaitGroup
	mu     sync.Mutex
	closed bool
}

// StartServer creates a Unix socket at <SocketDir>/stoke-<SessionID>.sock and
// begins accepting connections. Caller must Close() to release the socket.
func StartServer(opts Opts) (*Server, error) {
	if opts.SocketDir == "" {
		opts.SocketDir = "/tmp"
	}
	if opts.SessionID == "" {
		return nil, errors.New("sessionctl: SessionID required")
	}
	path := socketPath(opts.SocketDir, opts.SessionID)
	// Fail fast on genuinely-unbindable socket paths: the kernel's
	// sockaddr_un.sun_path field is bounded (Linux 108, Darwin/BSD 104 bytes),
	// and net.Listen would otherwise surface only an opaque "bind: invalid
	// argument". The limit is platform-aware so we reject only paths that the
	// current OS could not bind -- on Linux paths up to 107 bytes bind fine.
	if max := sunPathMax(); len(path) > max {
		return nil, fmt.Errorf("sessionctl: socket path %d bytes exceeds %d-byte sun_path limit: %s", len(path), max, path)
	}
	// Prune stale socket -- a leftover file from a crashed server would
	// otherwise make net.Listen fail with "address already in use".
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	// 0600 -- same-uid only; the socket is an RPC endpoint with no auth on
	// the socket path (AuthToken gates HTTP, not socket).
	if chmodErr := os.Chmod(path, 0o600); chmodErr != nil {
		ln.Close()
		_ = os.Remove(path)
		return nil, chmodErr
	}
	s := &Server{opts: opts, ln: ln}
	s.wg.Add(1)
	go s.serve()
	return s, nil
}

func socketPath(dir, sessionID string) string {
	return filepath.Join(dir, "stoke-"+sessionID+".sock")
}

func (s *Server) serve() {
	defer s.wg.Done()
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go func(conn net.Conn) {
			defer s.wg.Done()
			s.handleConn(conn)
		}(c)
	}
}

func (s *Server) handleConn(c net.Conn) {
	defer c.Close()
	req, err := ReadRequest(c)
	if err != nil {
		_ = WriteResponse(c, Response{
			RequestID: req.RequestID,
			OK:        false,
			Error:     "decode: " + err.Error(),
		})
		return
	}
	h, ok := s.opts.Handlers[req.Verb]
	if !ok {
		_ = WriteResponse(c, Response{
			RequestID: req.RequestID,
			OK:        false,
			Error:     "unknown verb: " + req.Verb,
		})
		return
	}
	data, errMsg, evtID := h(req)
	_ = WriteResponse(c, Response{
		RequestID: req.RequestID,
		OK:        errMsg == "",
		Data:      data,
		Error:     errMsg,
		EventID:   evtID,
	})
}

// Close shuts down the listener, removes the socket file, and waits for
// in-flight handlers to finish. Safe to call more than once.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	ln := s.ln
	httpLn := s.httpLn
	s.mu.Unlock()

	if ln != nil {
		_ = ln.Close()
	}
	if httpLn != nil {
		_ = httpLn.Close()
	}
	if s.opts.SocketDir != "" && s.opts.SessionID != "" {
		_ = os.Remove(socketPath(s.opts.SocketDir, s.opts.SessionID))
	}
	s.wg.Wait()
	return nil
}

// SocketPath returns the path to the Unix socket for this server.
func (s *Server) SocketPath() string {
	return socketPath(s.opts.SocketDir, s.opts.SessionID)
}
