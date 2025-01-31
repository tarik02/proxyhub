package main

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/tarik02/proxyhub/logging"
	"github.com/tarik02/proxyhub/protocol"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
)

type CmdHandler func(cmd protocol.Cmd)

type Connection struct {
	id     uint32
	queue  chan []byte
	closed bool
	mu     sync.Mutex

	connToWsRead  *io.PipeReader
	connToWsWrite *io.PipeWriter

	wsToConnRead  *io.PipeReader
	wsToConnWrite *io.PipeWriter
}

var _ io.ReadWriteCloser = &Connection{}

func NewConnection(id uint32) *Connection {
	connToWsRead, connToWsWrite := io.Pipe()
	wsToConnRead, wsToConnWrite := io.Pipe()

	return &Connection{
		id: id,

		queue: make(chan []byte, 32),

		connToWsRead: connToWsRead, connToWsWrite: connToWsWrite,
		wsToConnRead: wsToConnRead, wsToConnWrite: wsToConnWrite,
	}
}

func (c *Connection) Read(p []byte) (n int, err error) {
	return c.wsToConnRead.Read(p)
}

func (c *Connection) Write(p []byte) (n int, err error) {
	return c.connToWsWrite.Write(p)
}

func (c *Connection) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	close(c.queue)
	_ = c.connToWsWrite.Close()
	return nil
}

func (c *Connection) Process(ctx context.Context, handler CmdHandler) error {
	log := logging.FromContext(ctx)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	g, ctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		defer cancel()
		return c.handleConnection(ctx, handler)
	})

	g.Go(func() error {
		defer cancel()
		return c.handleQueue(ctx)
	})

	<-ctx.Done()

	handler(protocol.CmdClose{ID: c.id})

	if err := c.connToWsWrite.Close(); err != nil {
		log.Warn("connection to ws write close error", zap.Error(err))
	}

	if err := c.wsToConnWrite.Close(); err != nil {
		log.Warn("ws to connection write close error", zap.Error(err))
	}

	_ = c.Close()

	return g.Wait()
}

func (c *Connection) handleConnection(ctx context.Context, handler CmdHandler) error {
	_ = ctx

	r := c.connToWsRead

	for {
		buf := make([]byte, 16*1024)
		n, err := r.Read(buf)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if c.closed {
			return nil
		}
		handler(protocol.CmdData{ID: c.id, Bytes: buf[:n]})
	}
}

func (c *Connection) handleQueue(ctx context.Context) error {
	_ = ctx

	w := c.wsToConnWrite

	for msg := range c.queue {
		if len(msg) == 0 {
			break
		}

		_, err := w.Write(msg)
		if err != nil {
			return err
		}
	}

	return nil
}

func (c *Connection) RequestSend(bytes []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.queue <- bytes
}

func (c *Connection) RequestClose() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.queue <- nil
}
