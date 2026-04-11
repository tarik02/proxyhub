package proxyhub

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"testing"
	"time"
)

func TestBridgeConnAllowsRemoteDrainAfterLocalEOF(t *testing.T) {
	t.Parallel()

	conn, connPeer := newTestBridgePair(false)
	stream, streamPeer := newTestBridgePair(true)
	defer forceCloseTestBridgeConn(conn, connPeer, stream, streamPeer)

	done := make(chan struct{})
	go func() {
		bridgeConn(context.Background(), conn, stream, 200*time.Millisecond)
		close(done)
	}()

	writeExact(t, connPeer, "ping")
	readExact(t, streamPeer, "ping")

	if err := connPeer.CloseWrite(); err != nil {
		t.Fatalf("close write failed: %v", err)
	}

	writeExact(t, streamPeer, "pong")
	readExact(t, connPeer, "pong")

	if err := streamPeer.CloseWrite(); err != nil {
		t.Fatalf("close write failed: %v", err)
	}

	waitForBridgeDone(t, done, time.Second)
}

func TestBridgeConnAllowsLocalDrainAfterRemoteEOF(t *testing.T) {
	t.Parallel()

	conn, connPeer := newTestBridgePair(false)
	stream, streamPeer := newTestBridgePair(true)
	defer forceCloseTestBridgeConn(conn, connPeer, stream, streamPeer)

	done := make(chan struct{})
	go func() {
		bridgeConn(context.Background(), conn, stream, 200*time.Millisecond)
		close(done)
	}()

	writeExact(t, streamPeer, "pong")
	readExact(t, connPeer, "pong")

	if err := streamPeer.CloseWrite(); err != nil {
		t.Fatalf("close write failed: %v", err)
	}

	writeExact(t, connPeer, "ping")
	readExact(t, streamPeer, "ping")

	if err := connPeer.CloseWrite(); err != nil {
		t.Fatalf("close write failed: %v", err)
	}

	waitForBridgeDone(t, done, time.Second)
}

func TestBridgeConnTimesOutIfSecondDirectionHangs(t *testing.T) {
	t.Parallel()

	conn, connPeer := newTestBridgePair(false)
	stream, streamPeer := newTestBridgePair(true)
	defer forceCloseTestBridgeConn(conn, connPeer, stream, streamPeer)

	done := make(chan struct{})
	go func() {
		bridgeConn(context.Background(), conn, stream, 50*time.Millisecond)
		close(done)
	}()

	if err := connPeer.CloseWrite(); err != nil {
		t.Fatalf("close write failed: %v", err)
	}

	waitForBridgeDone(t, done, 500*time.Millisecond)
}

func TestBridgeConnShutdownInterruptsDrain(t *testing.T) {
	t.Parallel()

	conn, connPeer := newTestBridgePair(false)
	stream, streamPeer := newTestBridgePair(true)
	defer forceCloseTestBridgeConn(conn, connPeer, stream, streamPeer)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		bridgeConn(ctx, conn, stream, time.Second)
		close(done)
	}()

	if err := connPeer.CloseWrite(); err != nil {
		t.Fatalf("close write failed: %v", err)
	}

	cancel()
	waitForBridgeDone(t, done, 500*time.Millisecond)
}

type testBridgeConn struct {
	r                *io.PipeReader
	w                *io.PipeWriter
	halfCloseOnClose bool

	forceCloseOnce sync.Once
}

func newTestBridgePair(halfCloseOnClose bool) (*testBridgeConn, *testBridgeConn) {
	ar, aw := io.Pipe()
	br, bw := io.Pipe()

	return &testBridgeConn{
			r:                ar,
			w:                bw,
			halfCloseOnClose: halfCloseOnClose,
		}, &testBridgeConn{
			r:                br,
			w:                aw,
			halfCloseOnClose: halfCloseOnClose,
		}
}

func (c *testBridgeConn) Read(p []byte) (int, error) {
	return c.r.Read(p)
}

func (c *testBridgeConn) Write(p []byte) (int, error) {
	return c.w.Write(p)
}

func (c *testBridgeConn) Close() error {
	if c.halfCloseOnClose {
		return c.w.Close()
	}

	return c.forceClose()
}

func (c *testBridgeConn) CloseWrite() error {
	return c.w.Close()
}

func (c *testBridgeConn) SetReadDeadline(deadline time.Time) error {
	if deadline.IsZero() || deadline.After(time.Now()) {
		return nil
	}

	return c.r.CloseWithError(os.ErrDeadlineExceeded)
}

func (c *testBridgeConn) SetWriteDeadline(deadline time.Time) error {
	if deadline.IsZero() || deadline.After(time.Now()) {
		return nil
	}

	return c.w.CloseWithError(os.ErrDeadlineExceeded)
}

func (c *testBridgeConn) forceClose() error {
	var err error

	c.forceCloseOnce.Do(func() {
		err = errors.Join(
			c.r.CloseWithError(io.EOF),
			c.w.CloseWithError(io.EOF),
		)
	})

	return err
}

func writeExact(t *testing.T, w io.Writer, payload string) {
	t.Helper()

	errCh := make(chan error, 1)
	go func() {
		_, err := io.WriteString(w, payload)
		errCh <- err
	}()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("write failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for write to complete")
	}
}

func readExact(t *testing.T, r io.Reader, want string) {
	t.Helper()

	type readResult struct {
		payload string
		err     error
	}

	resultCh := make(chan readResult, 1)
	go func() {
		buf := make([]byte, len(want))
		_, err := io.ReadFull(r, buf)
		resultCh <- readResult{payload: string(buf), err: err}
	}()

	select {
	case result := <-resultCh:
		if result.err != nil {
			t.Fatalf("read failed: %v", result.err)
		}
		if result.payload != want {
			t.Fatalf("expected payload %q, got %q", want, result.payload)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for read to complete")
	}
}

func waitForBridgeDone(t *testing.T, done <-chan struct{}, timeout time.Duration) {
	t.Helper()

	select {
	case <-done:
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for bridge to finish after %s", timeout)
	}
}

func forceCloseTestBridgeConn(conns ...*testBridgeConn) {
	for _, conn := range conns {
		_ = conn.forceClose()
	}
}
