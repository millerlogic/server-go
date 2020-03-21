package server

import (
	"context"
	"fmt"
	"io"
	"net"
	"testing"
	"time"
)

func TestServer(t *testing.T) {
	r, w := io.Pipe()

	ln := ListenIO(&testRwc{r, w})

	ns := &Server{
		NewConn: func(ctx context.Context, conn net.Conn) context.Context {
			t.Logf("New conn: %s", conn.RemoteAddr())
			return ctx
		},
		Handler: HandlerFunc(func(conn net.Conn, r *Request) {
			t.Logf("From %s: %s", conn.RemoteAddr(), r.Data)
			//conn.Close()
		}),
		ConnClosed: func(ctx context.Context, conn net.Conn, err error) {
			t.Logf("Conn closed: %s", conn.RemoteAddr())
			if err != nil && err != io.ErrClosedPipe {
				t.Error(err)
			}
		},
	}

	go func() {
		fmt.Fprintf(w, "hello\n")
		fmt.Fprintf(w, "\n")
		fmt.Fprintf(w, "world\n")
		time.Sleep(20 * time.Millisecond)
		t.Log("done writing, closing server")
		ns.Close()
	}()

	err := ns.Serve(ln)
	if err != nil && err != ErrServerClosed {
		t.Fatal(err)
	}
	t.Log("Done serving")
}

type testRwc struct {
	io.ReadCloser
	io.WriteCloser
}

func (rwc *testRwc) Close() error {
	rwc.ReadCloser.Close()
	return rwc.WriteCloser.Close()
}
