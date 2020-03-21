package server

import (
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// ListenIO converts the ReadWriteCloser into a net.Listener.
// See also: IOListener.Accept
func ListenIO(rwc io.ReadWriteCloser) *IOListener {
	if rwc == nil {
		panic("ReadWriteCloser == nil")
	}
	ln := &IOListener{
		doneChan: make(chan struct{}),
	}
	ln.conn = ioConn{stream: rwc, ln: ln}
	ln.addr.str = getInputName(rwc)
	return ln
}

// IOListener is a net.Listener on a ReadWriteCloser; see ListenIO.
type IOListener struct {
	conn     ioConn
	addr     nameAddr
	mx       sync.Mutex
	doneChan chan struct{}
	accepted bool
}

// Accept will return a Conn for the listening reader and writer,
// and will block until the Conn is closed, in which case returns os.ErrClosed
func (ln *IOListener) Accept() (net.Conn, error) {
	ln.mx.Lock()
	if !ln.accepted {
		ln.accepted = true
		ln.mx.Unlock()
		return &ln.conn, nil
	}
	ln.mx.Unlock()
	<-ln.doneChan // Wait for the previous Accept conn to be closed.
	return nil, os.ErrClosed
}

// Close the listener.
func (ln *IOListener) Close() error {
	ln.mx.Lock()
	defer ln.mx.Unlock()
	select {
	case <-ln.doneChan:
	default:
		close(ln.doneChan)
	}
	return nil
}

// Addr gets the name of the stream.
func (ln *IOListener) Addr() net.Addr {
	return &ln.addr
}

type ioConn struct {
	stream io.ReadWriteCloser
	ln     *IOListener
	closed uint32 // atomic
}

func (conn *ioConn) isClosed() bool {
	return atomic.LoadUint32(&conn.closed) != 0
}

func (conn *ioConn) Read(p []byte) (int, error) {
	if conn.isClosed() {
		return 0, os.ErrClosed
	}
	return conn.stream.Read(p)
}

func (conn *ioConn) Write(p []byte) (int, error) {
	if conn.isClosed() {
		return 0, os.ErrClosed
	}
	return conn.stream.Write(p)
}

func (conn *ioConn) LocalAddr() net.Addr {
	return &conn.ln.addr
}

func (conn *ioConn) RemoteAddr() net.Addr {
	return &conn.ln.addr
}

func (conn *ioConn) Close() error {
	if atomic.CompareAndSwapUint32(&conn.closed, 0, 1) {
		err1 := conn.stream.Close()
		err2 := conn.ln.Close() // Causes Accept to return immediately.
		if err1 != nil {
			return err1
		}
		return err2
	}
	return nil
}

func (conn *ioConn) SetDeadline(t time.Time) error {
	if conn.isClosed() {
		return os.ErrClosed
	}
	if x, ok := conn.stream.(interface{ SetDeadline(t time.Time) error }); ok {
		return x.SetDeadline(t)
	}
	return os.ErrNoDeadline
}

func (conn *ioConn) SetReadDeadline(t time.Time) error {
	if conn.isClosed() {
		return os.ErrClosed
	}
	if x, ok := conn.stream.(interface{ SetReadDeadline(t time.Time) error }); ok {
		return x.SetReadDeadline(t)
	}
	return os.ErrNoDeadline
}

func (conn *ioConn) SetWriteDeadline(t time.Time) error {
	if conn.isClosed() {
		return os.ErrClosed
	}
	if x, ok := conn.stream.(interface{ SetWriteDeadline(t time.Time) error }); ok {
		return x.SetWriteDeadline(t)
	}
	return os.ErrNoDeadline
}

func getInputName(r io.Reader) string {
	if namer, ok := r.(interface{ Name() string }); ok {
		return namer.Name()
	}
	return "input"
}

type nameAddr struct {
	str string
}

func (addr *nameAddr) Network() string {
	return ""
}

func (addr *nameAddr) String() string {
	return addr.str
}
