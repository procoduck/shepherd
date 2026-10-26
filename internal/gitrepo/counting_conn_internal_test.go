package gitrepo

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
)

// The SSH byte cap trips on the read that crosses it, mid-stream: a
// server that keeps sending is cut off there, not after the whole transfer
// (the post-fetch counter check alone would let it run to completion).
func TestCountingConnStopsTheReadThatCrossesTheLimit(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close() //nolint:errcheck // test cleanup
	go func() {
		chunk := make([]byte, 16)
		for {
			if _, err := server.Write(chunk); err != nil {
				return
			}
		}
	}()
	counter := newByteCounter(40)
	conn := &countingConn{Conn: client, counter: counter}
	defer conn.Close() //nolint:errcheck // test cleanup

	buf := make([]byte, 16)
	var total int
	for i := 0; i < 10; i++ {
		n, err := conn.Read(buf)
		total += n
		if err != nil {
			if !errors.Is(err, ErrRepoTooLarge) {
				t.Fatalf("read failed with %v, want ErrRepoTooLarge", err)
			}
			if total <= 40 || total > 40+16 {
				t.Fatalf("tripped after %d bytes, want on the read crossing 40", total)
			}
			return
		}
	}
	t.Fatalf("read %d bytes past a 40-byte limit without an error", total)
}

// countingDialer wraps every connection it dials in the counter.
func TestCountingDialerCountsEveryConnection(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close() //nolint:errcheck // test cleanup
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		_, _ = c.Write(make([]byte, 100)) //nolint:errcheck // test server
		_ = c.Close()                     //nolint:errcheck // test server
	}()
	counter := newByteCounter(10)
	conn, err := countingDialer(counter)(context.Background(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close() //nolint:errcheck // test cleanup
	_, err = io.ReadAll(conn)
	if !errors.Is(err, ErrRepoTooLarge) || !counter.exceeded() {
		t.Fatalf("got %v (exceeded=%v), want ErrRepoTooLarge", err, counter.exceeded())
	}
}
