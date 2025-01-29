package main

import (
	"bytes"
	"context"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/tarik02/proxyhub/logging"
	"github.com/tarik02/proxyhub/protocol"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
)

type Proxy struct {
	id          string
	connCounter uint32

	ready   atomic.Bool
	started time.Time
	port    atomic.Int32

	closed bool
	mu     sync.Mutex

	wsConn     *websocket.Conn
	conns      map[uint32]*RemoteConn
	connsMutex sync.RWMutex
	writeQueue chan []byte
	wg         *errgroup.Group
}

func NewProxy(id string, wsConn *websocket.Conn) *Proxy {
	return &Proxy{
		id:         id,
		wsConn:     wsConn,
		conns:      make(map[uint32]*RemoteConn),
		writeQueue: make(chan []byte, 16),
	}
}

func (p *Proxy) ID() string {
	return p.id
}

func (p *Proxy) Ready() bool {
	return p.ready.Load()
}

func (p *Proxy) Port() int {
	return int(p.port.Load())
}

func (p *Proxy) Started() time.Time {
	return p.started
}

func (p *Proxy) ActiveConnectionsCount() int {
	p.connsMutex.RLock()
	defer p.connsMutex.RUnlock()

	return len(p.conns)
}

func (p *Proxy) Run(ctx context.Context, onReady func()) error {
	log := logging.FromContext(ctx, zap.String("proxy_id", p.id))

	p.started = time.Now()

	var lc net.ListenConfig
	l, err := lc.Listen(ctx, "tcp", "0.0.0.0:0")
	if err != nil {
		return err
	}

	log.Info("listening", zap.String("addr", l.Addr().String()))
	go onReady()

	p.ready.Store(true)
	p.port.Store(int32(l.Addr().(*net.TCPAddr).Port)) // nolint: gosec,forcetypeassert

	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(context.Canceled)

	p.wg, ctx = errgroup.WithContext(ctx)

	p.wg.Go(func() error {
		return p.acceptConns(ctx, l)
	})

	p.wg.Go(func() error {
		return p.workerSendQueue(ctx)
	})

	p.wg.Go(func() error {
		return p.workerReadWS(ctx)
	})

	<-ctx.Done()

	p.ready.Store(false)

	p.mu.Lock()
	p.closed = true
	close(p.writeQueue)
	p.mu.Unlock()

	err = ctx.Err()
	if err2 := p.wg.Wait(); err2 != nil {
		err = err2
	}

	if err2 := l.Close(); err2 != nil {
		log.Warn("listener close failed", zap.Error(err2))
	}

	return err
}

func (p *Proxy) acceptConns(ctx context.Context, l net.Listener) error {
	for {
		conn, err := l.Accept()
		if err != nil {
			return err
		}
		p.wg.Go(func() error {
			p.handleConn(ctx, conn)
			return nil
		})
	}
}

func (p *Proxy) QueueToWS(cmd protocol.Cmd) error {
	var buf bytes.Buffer
	if err := protocol.WriteCmd(&buf, cmd); err != nil {
		return err
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}

	p.writeQueue <- buf.Bytes()
	return nil
}

func (p *Proxy) handleConn(ctx context.Context, conn net.Conn) {
	id := atomic.AddUint32(&p.connCounter, 1)

	rconn := NewRemoteConn(id, conn)
	p.connsMutex.Lock()
	p.conns[id] = rconn
	p.connsMutex.Unlock()

	log := logging.FromContext(ctx, zap.Uint32("conn_id", id))
	log.Info("connection accepted")

	g, ctx := errgroup.WithContext(ctx)

	if err := p.QueueToWS(protocol.CmdNew{ID: id}); err != nil {
		log.Warn("queue failed", zap.Error(err))
		return
	}

	g.Go(func() error {
		return rconn.StartWriter(ctx)
	})

	g.Go(func() error {
		err := rconn.StartReader(ctx, func(message []byte) {
			if len(message) == 0 {
				if err := conn.Close(); err != nil {
					log.Warn("close failed", zap.Error(err))
				}
				return
			}

			// log.Debug("received", zap.ByteString("data", message))

			if err := p.QueueToWS(protocol.CmdData{ID: id, Bytes: message}); err != nil {
				log.Warn("cmd failed", zap.Error(err))
				return
			}
		})
		rconn.NotifyClose()
		return err
	})

	<-ctx.Done()

	rconn.NotifyClose()

	if err := p.QueueToWS(protocol.CmdClose{ID: id}); err != nil {
		log.Warn("queue failed", zap.Error(err))
	}

	if err := conn.Close(); err != nil {
		log.Warn("close failed", zap.Error(err))
	}

	if err := g.Wait(); err != nil {
		log.Warn("conn failed", zap.Error(err))
	}
}

func (p *Proxy) workerReadWS(ctx context.Context) error {
	log := logging.FromContext(ctx)

	for {
		mt, message, err := p.wsConn.ReadMessage()
		if err != nil {
			return err
		}

		if mt == websocket.TextMessage {
			log.Warn("unexpected text message from client", zap.String("message", string(message)))
			continue
		}

		if mt != websocket.BinaryMessage {
			log.Warn("unexpected message type", zap.Int("message_type", mt))
			continue
		}

		cmd, err := protocol.ReadCmd(bytes.NewReader(message))
		if err != nil {
			log.Warn("read failed", zap.Error(err))
			continue
		}

		switch cmd := cmd.(type) {
		case protocol.CmdData:
			p.connsMutex.RLock()
			conn, ok := p.conns[cmd.ID]
			p.connsMutex.RUnlock()
			if !ok {
				log.Warn("connection not found", zap.Uint32("conn_id", cmd.ID))
				continue
			}

			conn.NotifyWrite(cmd.Bytes)

		case protocol.CmdClose:
			p.connsMutex.RLock()
			conn, ok := p.conns[cmd.ID]
			p.connsMutex.RUnlock()
			if !ok {
				log.Warn("connection not found", zap.Uint32("conn_id", cmd.ID))
				continue
			}

			conn.NotifyClose()
		}
	}
}

func (p *Proxy) workerSendQueue(ctx context.Context) error {
	log := logging.FromContext(ctx)

	defer func() {
		err := p.wsConn.Close()
		if err != nil {
			log.Warn("close failed", zap.Error(err))
		}
	}()

	for message := range p.writeQueue {
		if err := p.wsConn.WriteMessage(websocket.BinaryMessage, message); err != nil {
			return err
		}
	}
	return nil
}
