[![GoDoc](https://godoc.org/github.com/millerlogic/server-go?status.svg)](https://godoc.org/github.com/millerlogic/server-go)


# server-go
Server implements a generic network server in Go. This is just a simple time saver so you don't have to implement yet another server. By default the client protocol is line-delimited, but any valid split function can be used.

Simple TCP server:

```go
srv := &Server{Addr: ":7111", Handler: HandlerFunc(func(conn net.Conn, r *Request) {
	fmt.Printf("From %s: %s\n", conn.RemoteAddr(), r.Data)
}}
srv.ListenAndServe()
```

Function ListenIO is provided which can turn an io.ReadWriteCloser into a net.Listener.
Here's an example turning standard input and output into a server. Not extremely useful but it can be beneficial to support a consistent interface.

```go
type stream struct {
	io.Reader
	io.WriteCloser
}

s := &stream{os.Stdin, os.Stdout}
ln := ListenIO(s) // make a net.Listener

srv := &Server{Handler: HandlerFunc(func(conn net.Conn, r *Request) {
	fmt.Printf("From %s: %s\n", conn.RemoteAddr(), r.Data)
}}
srv.Serve(ln)
```
