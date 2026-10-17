package voiceevents

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/mithrel/ginkgo/internal/ipc"
)

// SocketPath derives the separate event endpoint from ipc.SocketPath.
func SocketPath() (string, error) {
	p, err := ipc.SocketPath()
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(p, ".sock") + "-voice.sock", nil
}

func privatePath(path string) error {
	parent := filepath.Dir(path)
	real, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(parent)
	if err != nil {
		return err
	}
	if real != abs {
		return errors.New("voice socket parent contains symlink")
	}
	info, err := os.Stat(parent)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0o077 != 0 || !info.IsDir() {
		return errors.New("voice socket requires an owned private parent directory (0700)")
	}
	return nil
}

func prepareSocket(path string) error {
	if err := privatePath(path); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm()&0o077 != 0 {
		return errors.New("refusing unsafe existing voice socket")
	}
	conn, dialErr := net.DialTimeout("unix", path, 200*time.Millisecond)
	if dialErr == nil {
		conn.Close()
		return errors.New("voice socket already active")
	}
	if !errors.Is(dialErr, syscall.ECONNREFUSED) {
		return fmt.Errorf("probe voice socket: %w", dialErr)
	}
	now, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !os.SameFile(info, now) {
		return errors.New("voice socket changed during stale check")
	}
	return os.Remove(path)
}

// Server owns a private socket, not the Hub or any recording resources.
type Server struct {
	listener *net.UnixListener
	hub      *Hub
	path     string
	info     os.FileInfo
	mu       sync.Mutex
	conns    map[net.Conn]struct{}
	closed   bool
	wg       sync.WaitGroup
	once     sync.Once
	done     chan struct{}
	finished chan struct{}
	err      error // written before finished closes; protected by mu during serving
}

// Listen serves until ctx cancellation or Close. Existing active listeners and
// unrelated files are never replaced. The parent must already be private.
func Listen(ctx context.Context, path string, hub *Hub) (*Server, error) {
	if hub == nil {
		return nil, errors.New("nil voice event hub")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := prepareSocket(path); err != nil {
		return nil, err
	}
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	l.SetUnlinkOnClose(false)
	info, err := os.Lstat(path)
	if err != nil {
		l.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		l.Close()
		removeOwned(path, info)
		return nil, err
	}
	s := &Server{listener: l, hub: hub, path: path, info: info, conns: make(map[net.Conn]struct{}), done: make(chan struct{}), finished: make(chan struct{})}
	s.wg.Add(1)
	go s.accept()
	go func() {
		select {
		case <-ctx.Done():
		case <-s.done:
		}
		s.stop()
		s.wg.Wait()
		if now, err := os.Lstat(s.path); err == nil && os.SameFile(s.info, now) {
			if err := os.Remove(s.path); err != nil {
				s.mu.Lock()
				s.err = errors.Join(s.err, fmt.Errorf("remove voice socket: %w", err))
				s.mu.Unlock()
			}
		} else if err != nil && !os.IsNotExist(err) {
			s.mu.Lock()
			s.err = errors.Join(s.err, fmt.Errorf("inspect voice socket: %w", err))
			s.mu.Unlock()
		}
		close(s.finished)
	}()
	return s, nil
}

func removeOwned(path string, info os.FileInfo) {
	if now, err := os.Lstat(path); err == nil && os.SameFile(info, now) {
		_ = os.Remove(path)
	}
}

func (s *Server) accept() {
	defer s.wg.Done()
	for {
		conn, err := s.listener.AcceptUnix()
		if err != nil {
			s.mu.Lock()
			if !s.closed {
				s.err = fmt.Errorf("accept voice events: %w", err)
			}
			s.mu.Unlock()
			s.stop()
			return
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			conn.Close()
			return
		}
		// Bound even clients whose serve goroutines have not yet run.
		if len(s.conns) >= s.hub.options.MaxClients {
			s.mu.Unlock()
			conn.Close()
			continue
		}
		s.conns[conn] = struct{}{}
		s.wg.Add(1)
		s.mu.Unlock()
		go s.serve(conn)
	}
}

func (s *Server) serve(conn *net.UnixConn) {
	defer s.wg.Done()
	defer func() { conn.Close(); s.mu.Lock(); delete(s.conns, conn); s.mu.Unlock() }()
	sub, err := s.hub.subscribe()
	if err != nil {
		return
	}
	defer s.hub.unsubscribe(sub)
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		var b [1]byte
		_, _ = conn.Read(b[:])
		s.hub.unsubscribe(sub)
		conn.Close()
	}()
	defer func() { conn.Close(); <-readDone }()
	for {
		select {
		case <-sub.done:
			return
		case b := <-sub.queue:
			select {
			case <-sub.done:
				return
			default:
			}
			if err := conn.SetWriteDeadline(time.Now().Add(2 * time.Second)); err != nil {
				return
			}
			if _, err := conn.Write(b); err != nil {
				return
			}
		}
	}
}

// Close disconnects clients, joins all socket workers, and removes only its inode.
func (s *Server) Close() error {
	s.stop()
	return s.Wait()
}

// Wait joins endpoint workers and returns serving/shutdown errors. It does not
// initiate shutdown; the host must cancel Listen's context or call Close.
func (s *Server) Wait() error {
	<-s.finished
	return s.err
}

func (s *Server) stop() {
	s.once.Do(func() {
		s.mu.Lock()
		s.closed = true
		if err := s.listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			s.err = errors.Join(s.err, fmt.Errorf("close voice event listener: %w", err))
		}
		for c := range s.conns {
			c.Close()
		}
		s.mu.Unlock()
		close(s.done)
	})
}

// Watch subscribes without sending requests or controlling capture. It invokes
// consume synchronously; consume must return promptly and honor ctx if blocking.
// Cancellation closes the socket and joins its cancellation worker.
func Watch(ctx context.Context, path string, consume func(Event) error) error {
	if consume == nil {
		return errors.New("nil voice event consumer")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := privatePath(path); err != nil {
		return fmt.Errorf("connect voice event socket: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("connect voice event socket: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm()&0o077 != 0 {
		return errors.New("connect voice event socket: unsafe endpoint ownership, permissions or type")
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return fmt.Errorf("connect voice event socket: %w", err)
	}
	defer conn.Close()
	done, joined := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(joined)
		select {
		case <-ctx.Done():
			conn.Close()
		case <-done:
		}
	}()
	defer func() { close(done); <-joined }()
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 4096), MaxMessageBytes)
	var epoch string
	var seq uint64
	for scanner.Scan() {
		if len(scanner.Bytes())+1 > MaxMessageBytes {
			return errors.New("oversized voice event")
		}
		e, err := decodeWireEvent(scanner.Bytes())
		if err != nil {
			return fmt.Errorf("decode voice event: %w", err)
		}
		if e.SchemaVersion != SchemaVersion || e.EpochID == "" || len(e.EpochID) > 256 || e.ObservedAt.IsZero() {
			return errors.New("invalid voice event envelope")
		}
		if err := validateState(e.State); err != nil {
			return err
		}
		if err := validateEvent(e); err != nil {
			return err
		}
		if epoch == "" {
			if e.Type != "state" {
				return errors.New("missing initial voice state")
			}
			epoch = e.EpochID
		} else if epoch != e.EpochID || e.Seq <= seq {
			return errors.New("invalid voice event ordering")
		}
		seq = e.Seq
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := consume(e); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read voice events: %w", err)
	}
	return errors.New("voice event stream disconnected; reconnect for current state")
}
