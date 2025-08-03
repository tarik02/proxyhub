package util

import (
	"context"
	"net"

	"golang.org/x/net/proxy"
)

func DialProxyContext(ctx context.Context, d proxy.Dialer, network, address string) (net.Conn, error) {
	if d, ok := d.(proxy.ContextDialer); ok {
		return d.DialContext(ctx, network, address)
	}
	return dialContext(ctx, d, network, address)
}

// NOTE: COPY from proxy package
// WARNING: this can leak a goroutine for as long as the underlying Dialer implementation takes to timeout
// A Conn returned from a successful Dial after the context has been cancelled will be immediately closed.
func dialContext(ctx context.Context, d proxy.Dialer, network, address string) (net.Conn, error) {
	var (
		conn net.Conn
		done = make(chan struct{}, 1)
		err  error
	)
	go func() {
		conn, err = d.Dial(network, address)
		close(done)
		if conn != nil && ctx.Err() != nil {
			conn.Close()
		}
	}()
	select {
	case <-ctx.Done():
		err = ctx.Err()
	case <-done:
	}
	return conn, err
}
