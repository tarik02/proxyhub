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
	"github.com/tarik02/proxyhub/util"
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
	writeQueue chan []byte
	wg         *errgroup.Group
}

func NewProxy(id string, wsConn *websocket.Conn) *Proxy {
	return &Proxy{
		id:     id,
		wsConn: wsConn,
		conns:  make(map[uint32]*RemoteConn),
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

	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(context.Canceled)

	p.wg, ctx = errgroup.WithContext(ctx)

	connsChan := make(chan net.Conn, 16)
	p.wg.Go(func() error {
		return util.ListenToChan(l, connsChan)
	})

	p.wg.Go(func() error {
		<-ctx.Done()

		p.wg.Go(func() error {
			return p.wsConn.Close()
		})

		p.wg.Go(func() error {
			defer close(connsChan)

			if err := l.Close(); err != nil {
				log.Warn("listener close failed", zap.Error(err))
			}

			p.wg.Go(func() error {
				for conn := range connsChan {
					if err := conn.Close(); err != nil {
						log.Warn("close failed", zap.Error(err))
					}
				}
				return nil
			})

			return nil
		})

		return ctx.Err()
	})

	writeQueue := make(chan []byte, 16)
	p.writeQueue = writeQueue
	p.wg.Go(func() error {
		return p.workerSendQueue(p.wsConn, p.writeQueue)
	})

	cmdChan := make(chan protocol.Cmd, 16)
	p.wg.Go(func() error {
		defer close(cmdChan)
		return p.workerReadWS(ctx, cmdChan)
	})

	log.Info("listening", zap.String("addr", l.Addr().String()))
	go onReady()

	p.ready.Store(true)
	p.port.Store(int32(l.Addr().(*net.TCPAddr).Port)) // nolint: gosec,forcetypeassert

	for {
		select {
		case conn, ok := <-connsChan:
			if !ok {
				log.Debug("conns chan closed")
				connsChan = nil
				cancel(context.Canceled)
				break
			}
			p.handleConn(ctx, conn)

		case cmd, ok := <-cmdChan:
			if !ok {
				log.Debug("cmd chan closed")
				cmdChan = nil
				cancel(context.Canceled)
				break
			}
			switch cmd := cmd.(type) {
			case protocol.CmdData:
				conn, ok := p.conns[cmd.ID]
				if !ok {
					log.Warn("connection not found", zap.Uint32("conn_id", cmd.ID))
					continue
				}

				conn.NotifyWrite(cmd.Bytes)

			case protocol.CmdClose:
				conn, ok := p.conns[cmd.ID]
				if !ok {
					log.Warn("connection not found", zap.Uint32("conn_id", cmd.ID))
					continue
				}

				conn.NotifyClose()
			}
		}

		if connsChan == nil && cmdChan == nil {
			break
		}
	}

	log.Info("closing")

	p.ready.Store(false)

	p.mu.Lock()
	p.closed = true
	close(p.writeQueue)
	p.mu.Unlock()

	err = ctx.Err()
	if err2 := p.wg.Wait(); err2 != nil {
		err = err2
	}

	return err
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
	p.conns[id] = rconn

	p.wg.Go(func() error {
		log := logging.FromContext(ctx, zap.Uint32("conn_id", id))
		log.Info("connection accepted")

		g, ctx := errgroup.WithContext(ctx)

		if err := p.QueueToWS(protocol.CmdNew{ID: id}); err != nil {
			log.Warn("queue failed", zap.Error(err))
			return nil
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

		return nil
	})
}

func (p *Proxy) workerReadWS(ctx context.Context, cmdChan chan protocol.Cmd) error {
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

		cmdChan <- cmd
	}
}

func (p *Proxy) workerSendQueue(wsConn *websocket.Conn, writeQueue chan []byte) error {
	defer func() {
		go func() {
			for range writeQueue {
				// drain
			}
		}()
	}()
	for message := range writeQueue {
		if err := wsConn.WriteMessage(websocket.BinaryMessage, message); err != nil {
			return err
		}
	}
	return nil
}
