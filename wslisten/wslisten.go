package wslisten

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	"nhooyr.io/websocket"
)

// ListenWS returns a websocket listener;
// This satisfies net.Listener in order to Accept,
// and satisfies http.Handler to serve the websocket.
func ListenWS() *Listener {
	return &Listener{
		AcceptTimeout: DefaultAcceptTimeout,
		DefaultFormat: BinaryFormat,
		accepting:     make(chan func() (net.Conn, error)),
	}
}

const DefaultAcceptTimeout = 3 * time.Second

type Format = websocket.MessageType

const BinaryFormat = websocket.MessageBinary
const TextFormat = websocket.MessageText

type Listener struct {
	AcceptTimeout time.Duration // respond to HTTP requests if Accept not called.
	DefaultFormat Format        // Default write format; initialized to binary.
	accepting     chan func() (net.Conn, error)
	closed        int32 // atomic
}

// Accept waits for and returns the next connection to the listener.
func (ln *Listener) Accept() (net.Conn, error) {
	acc := <-ln.accepting
	if acc != nil {
		//return acc()
		c, err := acc()
		if err != nil {
			return nil, err
		}
		return &safeConnEOF{c}, nil // TODO: remove wrapper when fixed
	}
	return nil, os.ErrClosed
}

// Close closes the listener.
// Any blocked Accept operations will be unblocked and return errors.
func (ln *Listener) Close() error {
	if atomic.CompareAndSwapInt32(&ln.closed, 0, 1) {
		close(ln.accepting)
	}
	return nil
}

// Addr returns the listener's network address.
func (ln *Listener) Addr() net.Addr {
	return noAddr
}

// ServeHTTP serves the HTTP request and initiates a websocket,
// which is then returned by Accept.
func (ln *Listener) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	finishing := make(chan struct{})
	ok := int32(1)
	acc := func() (net.Conn, error) {
		if atomic.CompareAndSwapInt32(&ok, 1, 0) {
			wsConn, err := websocket.Accept(w, r, nil)
			close(finishing)
			if err != nil {
				http.Error(w, "internal error", http.StatusInternalServerError)
				return nil, err
			}
			c := websocket.NetConn(context.Background(), wsConn, ln.DefaultFormat)
			return c, nil
		}
		return nil, errors.New("timed out")
	}

	timeout := time.After(ln.AcceptTimeout)
	select {
	case ln.accepting <- acc:
		select {
		case <-finishing: // Wait to finish accepting.
		case <-timeout:
			if atomic.CompareAndSwapInt32(&ok, 1, 0) {
				http.Error(w, "timed out", http.StatusGatewayTimeout)
			}
		}
	case <-timeout:
		if atomic.CompareAndSwapInt32(&ok, 1, 0) {
			http.Error(w, "timed out", http.StatusGatewayTimeout)
		}
	}
}

type fakeAddr struct {
}

func (addr *fakeAddr) Network() string {
	return ""
}

func (addr *fakeAddr) String() string {
	return ""
}

var noAddr = &fakeAddr{}

type wsAccept struct {
	finish func() error
	conn   net.Conn
}

type safeConnEOF struct {
	net.Conn
}

func (x *safeConnEOF) Read(b []byte) (int, error) {
	n, err := x.Conn.Read(b)
	if err != nil && errors.Is(err, io.EOF) {
		if err != io.EOF {
			//log.Printf("net.Conn.Read EOF wrong error type: %v", err)
			//debug.PrintStack()
		}
		return n, io.EOF
	}
	return n, err
}

func (x *safeConnEOF) Close() error {
	err := x.Conn.Close()
	if err != nil && errors.Is(err, io.EOF) {
		if err != io.EOF {
			//log.Printf("net.Conn.Close EOF wrong error type: %v", err)
			//debug.PrintStack()
		}
		return io.EOF
	}
	return err
}
