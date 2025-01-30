package main

import (
	"context"
	"errors"
	"io"
	"net"

	"github.com/tarik02/proxyhub/logging"
	"go.uber.org/zap"
)

type RemoteConn struct {
	id         uint32
	conn       net.Conn
	writeQueue chan []byte
}

func NewRemoteConn(id uint32, conn net.Conn) *RemoteConn {
	return &RemoteConn{
		id:         id,
		conn:       conn,
		writeQueue: make(chan []byte, 16),
	}
}

func (c *RemoteConn) StartWriter(ctx context.Context) error {
	log := logging.FromContext(ctx, zap.Uint32("remote_conn_id", c.id))
	defer log.Debug("writer closed")

	defer func() {
		if err := c.conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			log.Warn("close conn failed", zap.Error(err))
		}
	}()

	for message := range c.writeQueue {
		log.Debug("write", zap.ByteString("message", message))
		if len(message) == 0 {
			break
		}

		if _, err := c.conn.Write(message); err != nil {
			return err
		}
	}

	go func() {
		for range c.writeQueue {
			// drain
		}
	}()

	return nil
}

func (c *RemoteConn) StartReader(ctx context.Context, onMessage func(message []byte)) error {
	log := logging.FromContext(ctx, zap.Uint32("remote_conn_id", c.id))
	defer log.Debug("reader closed")

	for {
		buf := make([]byte, 16*1024)
		n, err := c.conn.Read(buf)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if n == 0 {
			continue
		}
		onMessage(buf[:n])
	}
}

func (c *RemoteConn) NotifyWrite(message []byte) {
	c.writeQueue <- message
}

func (c *RemoteConn) NotifyClose() {
	c.writeQueue <- nil
}
