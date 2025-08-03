package util

import (
	"net"
	"sync"
)

type OnceListener struct {
	connection net.Conn
	isClosed   bool
	mu         sync.Mutex
}

func NewOnceListener(conn net.Conn) net.Listener {
	return &OnceListener{
		connection: conn,
	}
}

func (l *OnceListener) Addr() net.Addr {
	return l.connection.LocalAddr()
}

func (l *OnceListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.isClosed {
		return nil, net.ErrClosed
	}
	l.isClosed = true
	return l.connection, nil
}

func (l *OnceListener) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.isClosed = true
	return nil
}

var _ net.Listener = &OnceListener{}
