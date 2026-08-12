//go:build windows

package platform

// pipe_windows.go is the Windows transport: the named pipe \\.\pipe\wt
// (docs/ARCHITECTURE.md §4.1's hosting table), implemented with the
// standard library plus raw syscall — no x/sys, no cgo, CGO_ENABLED=0
// throughout. The protocol above it is unchanged: newline-delimited JSON,
// one object per request and one per response, version negotiation on
// connect.
//
// Identity on the pipe: peer credentials do not exist the way they do on
// a unix socket. The pipe's ACL — set here at creation, granting the
// current user and denying everyone else (sec_windows.go) — is the whole
// of the identity: every connection that got through the ACL is the
// owning user, and the coordinator treats it as such
// (peercred_windows.go). That is stated rather than assumed.
//
// Bounded coverage, stated: the listener is single-instance-at-a-time —
// one pending ConnectNamedPipe between accepted connections — so a client
// dialing into that gap gets ERROR_PIPE_BUSY and the dialer retries via
// WaitNamedPipe. Accept's pending connect is unblocked on Close by
// closing the instance handle, which is the documented way to abort a
// synchronous ConnectNamedPipe; the race window is shutdown-only and
// single-user, and is accepted for that reason.

import (
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

// pipeAddr is the net.Addr of a pipe connection — the pipe name is the
// only address a named pipe has.
type pipeAddr struct{ name string }

func (a pipeAddr) Network() string { return "pipe" }
func (a pipeAddr) String() string  { return a.name }

// pipeConn is a net.Conn over one named-pipe handle, byte mode (the pipe
// is a byte stream like a socket; the newline framing is the protocol's
// business, not the pipe's).
type pipeConn struct {
	h    syscall.Handle
	name string
	once sync.Once
}

// Read reads from the pipe. ERROR_BROKEN_PIPE and a zero-byte successful
// read both mean the peer is gone — io.EOF, the socket semantics the
// protocol loop expects.
func (c *pipeConn) Read(p []byte) (int, error) {
	var done uint32
	err := syscall.ReadFile(c.h, p, &done, nil)
	if err != nil {
		if err == syscall.ERROR_BROKEN_PIPE || err == winERROR_PIPE_NOT_CONNECTED {
			return 0, io.EOF
		}
		return 0, err
	}
	if done == 0 {
		return 0, io.EOF
	}
	return int(done), nil
}

// Write writes to the pipe. A write to a peer that closed fails with
// ERROR_NO_DATA or ERROR_BROKEN_PIPE, reported as io.ErrClosedPipe.
func (c *pipeConn) Write(p []byte) (int, error) {
	var done uint32
	err := syscall.WriteFile(c.h, p, &done, nil)
	if err != nil {
		if err == syscall.ERROR_BROKEN_PIPE || err == winERROR_NO_DATA || err == winERROR_PIPE_NOT_CONNECTED {
			return int(done), io.ErrClosedPipe
		}
		return int(done), err
	}
	return int(done), nil
}

// Close closes the handle, idempotently.
func (c *pipeConn) Close() error {
	var err error
	c.once.Do(func() { err = syscall.CloseHandle(c.h) })
	return err
}

// LocalAddr and RemoteAddr are the pipe name on both ends — a named pipe
// has one address.
func (c *pipeConn) LocalAddr() net.Addr  { return pipeAddr{c.name} }
func (c *pipeConn) RemoteAddr() net.Addr { return pipeAddr{c.name} }

// Deadlines are not supported on a named pipe in this build: nothing in
// this repository sets them (the protocol loop blocks in ReadFile), and
// implementing them would need overlapped IO. Refusing is the honest
// contract — a caller that needs deadlines must not silently get none.
func (c *pipeConn) SetDeadline(t time.Time) error {
	return errors.New("deadlines are not supported on a named pipe")
}
func (c *pipeConn) SetReadDeadline(t time.Time) error {
	return errors.New("deadlines are not supported on a named pipe")
}
func (c *pipeConn) SetWriteDeadline(t time.Time) error {
	return errors.New("deadlines are not supported on a named pipe")
}

// pipeListener is a net.Listener over one named-pipe server: it creates
// one instance per Accept, waits for a client with a synchronous
// ConnectNamedPipe, and hands the connected handle to a pipeConn. The
// pending instance handle is kept so Close can abort the wait.
type pipeListener struct {
	name   string
	closed atomic.Bool
	mu     sync.Mutex
	h      syscall.Handle // the pending instance, or 0
}

// Accept waits for one client. The instance is created with
// FILE_FLAG_FIRST_PIPE_INSTANCE, so a second listener on the same name
// is refused at creation — the pipe analogue of the unix already-
// listening check.
func (l *pipeListener) Accept() (net.Conn, error) {
	for {
		if l.closed.Load() {
			return nil, net.ErrClosed
		}
		h, err := createPipeInstance(l.name)
		if err != nil {
			return nil, err
		}
		l.mu.Lock()
		l.h = h
		l.mu.Unlock()
		connectErr := connectNamedPipe(h)
		l.mu.Lock()
		l.h = 0
		l.mu.Unlock()
		if connectErr != nil {
			syscall.CloseHandle(h)
			if l.closed.Load() {
				return nil, net.ErrClosed
			}
			if connectErr == winERROR_PIPE_CONNECTED {
				// The client connected between CreateNamedPipe and
				// ConnectNamedPipe — the connection is real.
				return &pipeConn{h: h, name: l.name}, nil
			}
			return nil, fmt.Errorf("waiting for a client on %s: %w", l.name, connectErr)
		}
		return &pipeConn{h: h, name: l.name}, nil
	}
}

// Close aborts the pending Accept (closing the instance handle unblocks
// the synchronous ConnectNamedPipe) and marks the listener closed. The
// close-while-blocked race is shutdown-only: no new handle is created
// after Close, so the freed handle value cannot be reused before the
// blocked call wakes.
func (l *pipeListener) Close() error {
	l.closed.Store(true)
	l.mu.Lock()
	h := l.h
	l.h = 0
	l.mu.Unlock()
	if h != 0 {
		syscall.CloseHandle(h)
	}
	return nil
}

// Addr is the pipe name.
func (l *pipeListener) Addr() net.Addr { return pipeAddr{l.name} }

// listenPipe creates the coordinator's pipe listener: the first instance
// of name with the private ACL. Refusing to listen is the outcome when
// the ACL cannot be built or applied — a pipe anyone can reach is not
// the coordinator's transport (docs/ARCHITECTURE.md §12.2).
func listenPipe(name string) (net.Listener, error) {
	sa, err := secureSecurityAttributes()
	if err != nil {
		return nil, err
	}
	defer freeSecurityDescriptor(sa)
	h, err := createNamedPipe(name, sa)
	if err != nil {
		if err == winERROR_ACCESS_DENIED {
			return nil, fmt.Errorf("another coordinator is already listening at %s", name)
		}
		return nil, fmt.Errorf("creating the coordinator pipe at %s: %w", name, err)
	}
	syscall.CloseHandle(h) // the listener creates its instances per Accept
	return &pipeListener{name: name}, nil
}

// createPipeInstance creates one pipe instance and waits for it to be
// connected (Accept's unit of work).
func createPipeInstance(name string) (syscall.Handle, error) {
	sa, err := secureSecurityAttributes()
	if err != nil {
		return 0, err
	}
	defer freeSecurityDescriptor(sa)
	h, err := createNamedPipe(name, sa)
	if err != nil {
		return 0, err
	}
	return h, nil
}

// createNamedPipe calls CreateNamedPipeW with the byte-mode, blocking,
// first-instance parameters and the private security attributes.
func createNamedPipe(name string, sa *syscall.SecurityAttributes) (syscall.Handle, error) {
	p, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return 0, fmt.Errorf("encoding the pipe name: %w", err)
	}
	openMode := uint32(winPIPE_ACCESS_DUPLEX | winFILE_FLAG_FIRST_PIPE_INST)
	pipeMode := uint32(0) // PIPE_TYPE_BYTE | PIPE_READMODE_BYTE | PIPE_WAIT, all zero
	create := syscall.NewLazyDLL("kernel32.dll").NewProc("CreateNamedPipeW")
	var saPtr uintptr
	if sa != nil {
		saPtr = uintptr(unsafe.Pointer(sa))
	}
	h, _, e1 := create.Call(uintptr(unsafe.Pointer(p)), uintptr(openMode), uintptr(pipeMode),
		winPIPE_UNLIMITED_INSTANCES, 65536, 65536, 0, saPtr)
	if h == 0 {
		return 0, e1
	}
	return syscall.Handle(h), nil
}

// connectNamedPipe waits for a client on an instance, synchronously.
func connectNamedPipe(h syscall.Handle) error {
	connect := syscall.NewLazyDLL("kernel32.dll").NewProc("ConnectNamedPipe")
	r1, _, e1 := connect.Call(uintptr(h), 0)
	if r1 == 0 {
		return e1
	}
	return nil
}

// dialPipe connects to the coordinator's pipe. ERROR_PIPE_BUSY means the
// listener is between instances (single-instance-at-a-time): the dialer
// waits on WaitNamedPipe and retries, up to about five seconds — the
// same bounded patience a unix client's connect has against a socket
// that is up but momentarily not accepting.
func dialPipe(name string) (net.Conn, error) {
	p, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return nil, fmt.Errorf("encoding the pipe name: %w", err)
	}
	wait := syscall.NewLazyDLL("kernel32.dll").NewProc("WaitNamedPipeW")
	const maxAttempts = 10
	for attempt := 0; ; attempt++ {
		h, err := syscall.CreateFile(p,
			syscall.GENERIC_READ|syscall.GENERIC_WRITE,
			syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE,
			nil, syscall.OPEN_EXISTING, 0, 0)
		if err == nil {
			return &pipeConn{h: h, name: name}, nil
		}
		if err != winERROR_PIPE_BUSY {
			return nil, fmt.Errorf("connecting to the coordinator at %s: %w", name, err)
		}
		if attempt >= maxAttempts {
			return nil, fmt.Errorf("connecting to the coordinator at %s: the pipe stayed busy: %w", name, err)
		}
		wait.Call(uintptr(unsafe.Pointer(p)), 500) // milliseconds; a failed wait just retries
	}
}
