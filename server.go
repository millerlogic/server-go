package server

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultMaxConns is the default value for Server.MaxConns
const DefaultMaxConns = 1024

// Server implements a generic server.
// Client messages are split by the bufio.SplitFunc in ConnSplit.
type Server struct {
	// Addr is the address of the listening socket.
	Addr string

	// Handler is called with each split from ConnSplit.
	// Each connection gets its own goroutine to read and call the handler.
	Handler Handler

	// BaseContext gets the base context (optional)
	BaseContext func(net.Listener) context.Context

	// NewConn is called for each new conn, returns the per-connection context (optional)
	// Avoid panic in NewConn; it will bubble up from the Serve call and
	// render the Server invalid, requiring Close.
	NewConn func(ctx context.Context, conn net.Conn) context.Context

	// ConnClosed is called when a connection is closed.
	// Called from the same goroutine as the conn handler.
	// err is nil if no error.
	ConnClosed func(ctx context.Context, conn net.Conn, err error)

	// ConnSplit is a function which splits up the individual messages.
	// The default is bufio.ScanLines. See bufio.SplitFunc for more info.
	ConnSplit bufio.SplitFunc

	// MaxScanTokenSize is used with bufio.Scanner
	MaxScanTokenSize int

	// MaxConns is the maximum number of concurrent client connections.
	// Defaults to DefaultMaxConns.
	// This can be updated in the NewConn callback, but not in the other callbacks.
	// Setting this to -1 will block future new connections, but allow the existing ones.
	MaxConns int

	shuttingDown int32 // atomic

	doneChan chan struct{}

	mx    sync.RWMutex
	ln    net.Listener
	conns []net.Conn
}

// NumConns gets the current number of concurrent client connections.
func (srv *Server) NumConns() int {
	srv.mx.RLock()
	n := len(srv.conns)
	srv.mx.RUnlock()
	return n
}

// Conns gets the net.Conn list.
func (srv *Server) Conns() []net.Conn {
	return srv.AppendConns(nil)
}

// AppendConns appends the net.Conn list to buf and returns the updated slice.
func (srv *Server) AppendConns(buf []net.Conn) []net.Conn {
	srv.mx.RLock()
	x := append(buf, srv.conns...)
	srv.mx.RUnlock()
	return x
}

// Close the server.
func (srv *Server) Close() error {
	atomic.StoreInt32(&srv.shuttingDown, 1)

	srv.mx.Lock()
	defer srv.mx.Unlock()

	if srv.ln == nil {
		return errors.New("not serving")
	}
	srv.ln.Close()

	for _, co := range srv.conns {
		co.Close()
	}
	srv.conns = nil

	srv.closeDoneChanLocked()

	return nil
}

// Shutdown the server by first waiting for all the connections to close gracefully,
// or stops waiting when the ctx is done.
func (srv *Server) Shutdown(ctx context.Context) error {
	atomic.StoreInt32(&srv.shuttingDown, 1)

	srv.mx.Lock()
	if srv.ln == nil {
		return errors.New("not serving")
	}
	srv.ln.Close()
	srv.mx.Unlock()

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			srv.mx.Lock()
			if len(srv.conns) == 0 {
				srv.closeDoneChanLocked()
				srv.mx.Unlock()
				return nil
			}
			srv.mx.Unlock()
		case <-ctx.Done():
			return ctx.Err()
		case <-srv.doneChan:
			return nil
		}
	}

}

func (srv *Server) isShuttingDown() bool {
	return atomic.LoadInt32(&srv.shuttingDown) != 0
}

func (srv *Server) closeDoneChanLocked() {
	select {
	case <-srv.doneChan:
	default:
		close(srv.doneChan)
	}
}

// ErrServerClosed is returned when Server.Close or Server.Shutdown is called.
var ErrServerClosed = errors.New("server closed")

// Serve allows serving on any streaming listener.
// Do not call more than once.
// If ln.Accept returns os.ErrClosed, the server will be shut down.
func (srv *Server) Serve(ln net.Listener) error {
	if srv.Handler == nil {
		panic("Handler == nil")
	}
	if srv.doneChan != nil {
		select {
		case <-srv.doneChan:
			return ErrServerClosed
		default:
		}
	}
	if srv.ln != nil {
		panic("already serving")
	}
	if srv.BaseContext == nil {
		srv.BaseContext = func(net.Listener) context.Context {
			return context.Background()
		}
	}
	if srv.NewConn == nil {
		srv.NewConn = func(ctx context.Context, conn net.Conn) context.Context {
			return ctx
		}
	}
	if srv.ConnClosed == nil {
		srv.ConnClosed = func(ctx context.Context, conn net.Conn, err error) {
		}
	}
	if srv.ConnSplit == nil {
		srv.ConnSplit = bufio.ScanLines
	}
	if srv.MaxScanTokenSize <= 0 {
		srv.MaxScanTokenSize = bufio.MaxScanTokenSize
	}
	if srv.MaxConns <= 0 {
		srv.MaxConns = DefaultMaxConns
	}
	srv.doneChan = make(chan struct{})
	baseCtx := srv.BaseContext(ln) // orig listener
	ln = &closeOnceListener{Listener: ln}
	defer ln.Close()
	srv.ln = ln

	numErrors := 0
	for {
		for srv.NumConns() >= srv.MaxConns {
			select {
			case <-srv.doneChan:
				return ErrServerClosed
			case <-time.After(250 * time.Millisecond):
			}
		}

		if srv.isShuttingDown() {
			<-srv.doneChan
			return ErrServerClosed
		}

		conn, err := ln.Accept()
		if err != nil {
			//if err == os.ErrClosed {
			if errors.Is(err, os.ErrClosed) {
				err = srv.Shutdown(context.Background())
				if err != nil {
					return err
				}
				return ErrServerClosed
			}
			select {
			case <-srv.doneChan:
				return ErrServerClosed
			default:
			}
			if ne, ok := err.(net.Error); ok && ne.Temporary() {
				time.Sleep(time.Second / 2)
				numErrors++
				if numErrors < 100 {
					continue
				}
			}
			return err
		}
		numErrors = 0

		srv.mx.Lock()
		if srv.isShuttingDown() {
			srv.mx.Unlock()
			conn.Close()
			<-srv.doneChan
			return ErrServerClosed
		}
		srv.conns = append(srv.conns, conn)
		srv.mx.Unlock()

		cctx := srv.NewConn(baseCtx, conn)
		go serveConn(cctx, conn, srv)
	}
}

func (srv *Server) removeConn(conn net.Conn) {
	srv.mx.Lock()
	defer srv.mx.Unlock()
	for i, c := range srv.conns {
		if conn == c {
			ilast := len(srv.conns) - 1
			srv.conns[i] = srv.conns[ilast]
			srv.conns = srv.conns[:ilast]
			break
		}
	}
}

func (srv *Server) listen() (net.Listener, error) {
	addr := srv.Addr
	var ln net.Listener
	if strings.IndexByte(addr, '/') != -1 {
		var err error
		ln, err = net.Listen("unix", addr)
		if err != nil {
			return nil, err
		}
	} else {
		if addr == "" {
			addr = "127.0.0.1:0"
		}
		var err error
		ln, err = net.Listen("tcp", addr)
		if err != nil {
			return nil, err
		}
		if srv.Addr == "" {
			srv.Addr = ln.Addr().String()
		}
	}
	return ln, nil
}

// ListenAndServe listens and serves based on srv.Addr:
// * TCP socket at host:port, or
// * a random TCP port on localhost if empty, or
// * a UNIX domain socket if containing a slash.
// Also see Serve.
func (srv *Server) ListenAndServe() error {
	ln, err := srv.listen()
	if err != nil {
		return err
	}
	return srv.Serve(ln)
}

// ListenAndServeTLS is ListenAndServe for TLS.
func (srv *Server) ListenAndServeTLS(certFile, keyFile string) error {
	ln, err := srv.listen()
	if err != nil {
		return err
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return err
	}
	tlsconfig := &tls.Config{Certificates: []tls.Certificate{cert}}
	lntls := tls.NewListener(ln, tlsconfig)
	return srv.Serve(lntls)
}

type closeOnceListener struct {
	net.Listener
	once sync.Once
	err  error
}

func (oc *closeOnceListener) Close() error {
	oc.once.Do(oc.close)
	return oc.err
}

func (oc *closeOnceListener) close() {
	oc.err = oc.Listener.Close()
}

type panicErr struct {
	r interface{}
}

func (err *panicErr) Error() string {
	return fmt.Sprintf("panic: %v", err.r)
}

func (err *panicErr) Recover() interface{} {
	return err.r
}

func (err *panicErr) Unwrap() error {
	rerr, _ := err.r.(error)
	return rerr
}

func serveConn(ctx context.Context, conn net.Conn, srv *Server) {
	var closeErr error
	defer func() {
		srv.removeConn(conn)
		conn.Close()
		if r := recover(); r != nil {
			// Overwrite closeErr if panic.
			closeErr = &panicErr{r}
		}
		srv.ConnClosed(ctx, conn, closeErr)
	}()
	scan := bufio.NewScanner(conn)
	scan.Buffer(nil, srv.MaxScanTokenSize)
	scan.Split(srv.ConnSplit)
	for scan.Scan() {
		data := scan.Bytes()
		srv.Handler.ServeData(conn, &Request{Data: data, ctx: ctx})
	}
	closeErr = scan.Err()
}

// Handler for a single split payload.
type Handler interface {
	ServeData(conn net.Conn, r *Request)
}

// HandlerFunc is a convenience wrapper to use a func as a Handler.
type HandlerFunc func(conn net.Conn, r *Request)

// ServeData calls f(conn, r)
func (f HandlerFunc) ServeData(conn net.Conn, r *Request) {
	f(conn, r)
}

// Request is for a single split payload.
type Request struct {
	Data []byte
	ctx  context.Context
}

// Context returns the context; also see WithContext.
func (r *Request) Context() context.Context {
	if r.ctx != nil {
		return r.ctx

	}
	return context.Background()
}

// WithContext returns a new Request with the specified context.
func (r *Request) WithContext(ctx context.Context) *Request {
	if ctx == nil {
		panic("ctx == nil")
	}
	r2 := *r
	r2.ctx = ctx
	return &r2
}
