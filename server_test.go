package server

import (
	"fmt"
	"io"
	"net"
	"testing"
)

func TestServer(t *testing.T) {
	r, w := io.Pipe()

	ln := ListenIO(&testRwc{r, w})

	ns := &Server{
		Handler: func(conn net.Conn, payload []byte) {
			t.Logf("From %s: %s", conn.RemoteAddr(), payload)
			//conn.Close()
		},
	}

	go func() {
		fmt.Fprintf(w, "hello\n")
		fmt.Fprintf(w, "world\n")
		t.Log("done writing, closing server")
		ns.Close()
	}()

	err := ns.Serve(ln)
	if err != nil && err != ErrServerClosed {
		t.Fatal(err)
	}
}

type testRwc struct {
	io.ReadCloser
	io.WriteCloser
}

func (rwc *testRwc) Close() error {
	rwc.ReadCloser.Close()
	return rwc.WriteCloser.Close()
}