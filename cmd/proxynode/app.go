package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/tarik02/proxyhub/logging"
	"github.com/tarik02/proxyhub/protocol"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
)

type AppParams struct {
	Version  string
	Endpoint string
	Username string
	Password string
}

type App struct {
	params   AppParams
	wsToSend chan []byte

	closed bool
	mu     sync.Mutex

	conns   map[uint32]*Connection
	connsMu sync.RWMutex

	wg *errgroup.Group

	Handler         func(conn *Connection)
	OnServerMessage func(string)
}

func NewApp(params AppParams) *App {
	return &App{
		params:   params,
		wsToSend: make(chan []byte, 32),

		conns: make(map[uint32]*Connection),

		Handler:         func(conn *Connection) {},
		OnServerMessage: func(string) {},
	}
}

func (a *App) Run(ctx context.Context) error {
	a.wg, ctx = errgroup.WithContext(ctx)
	log := logging.FromContext(ctx)

	conn, resp, err := websocket.DefaultDialer.DialContext(ctx, a.params.Endpoint, http.Header{
		"Authorization": []string{protocol.HTTPBasicAuth(a.params.Username, a.params.Password)},
		"X-Version":     []string{a.params.Version},
	})
	if err != nil {
		return err
	}
	_ = resp.Body.Close()

	log.Info("connected")

	a.wg.Go(func() error {
		return a.workerWSSend(ctx, conn)
	})

	a.wg.Go(func() error {
		return a.workerWSRecv(ctx, conn)
	})

	<-ctx.Done()

	log.Info("closing connection")

	a.mu.Lock()
	a.closed = true
	close(a.wsToSend)
	a.mu.Unlock()

	if err := conn.Close(); err != nil {
		log.Warn("connection close error", zap.Error(err))
	}

	return a.wg.Wait()
}

func (a *App) workerWSSend(ctx context.Context, ws *websocket.Conn) error {
	_ = ctx

	for buf := range a.wsToSend {
		if err := ws.WriteMessage(websocket.BinaryMessage, buf); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) workerWSRecv(ctx context.Context, ws *websocket.Conn) error {
	log := logging.FromContext(ctx)

	for {
		mt, buf, err := ws.ReadMessage()
		if err != nil {
			if a.closed && (websocket.IsCloseError(err, websocket.CloseNormalClosure) || errors.Is(err, net.ErrClosed)) {
				return nil
			}
			log.Warn("read message failed", zap.Error(err))
			return err
		}

		if mt == websocket.TextMessage {
			log.Warn("Unexpected text message from client", zap.String("message", string(buf)))
			continue
		}

		if mt != websocket.BinaryMessage {
			log.Warn("unexpected message type", zap.Int("type", mt))
			continue
		}

		cmd, err := protocol.ReadCmd(bytes.NewReader(buf))
		if err != nil {
			log.Warn("command parsing failed", zap.Error(err))
			continue
		}

		switch cmd := cmd.(type) {
		case protocol.CmdNew:
			if err := a.wsCmdNew(ctx, cmd); err != nil {
				log.Warn("command processing failed", zap.Error(err))
				continue
			}

		case protocol.CmdClose:
			if err := a.wsCmdClose(ctx, cmd); err != nil {
				log.Warn("command processing failed", zap.Error(err))
				continue
			}

		case protocol.CmdData:
			if err := a.wsCmdData(ctx, cmd); err != nil {
				log.Warn("command processing failed", zap.Error(err))
				continue
			}

		case protocol.CmdMessage:
			go a.OnServerMessage(cmd.Message)

		default:
			log.Warn("unexpected command", zap.Any("cmd", cmd))
		}
	}
}

func (a *App) wsCmdNew(ctx context.Context, cmd protocol.CmdNew) error {
	log := logging.FromContext(ctx)
	log.Debug("new connection", zap.Uint32("id", cmd.ID))

	a.connsMu.Lock()
	if _, ok := a.conns[cmd.ID]; ok {
		a.connsMu.Unlock()
		return fmt.Errorf("connection already exists: id=%d", cmd.ID)
	}
	c := NewConnection(cmd.ID)
	a.conns[cmd.ID] = c
	a.connsMu.Unlock()

	go a.Handler(c)

	a.wg.Go(func() error {
		err := c.Process(ctx, func(cmd protocol.Cmd) {
			var buf bytes.Buffer
			err := protocol.WriteCmd(&buf, cmd)
			if err != nil {
				log.Warn("command serialization failed", zap.Error(err))
				return
			}

			a.mu.Lock()
			defer a.mu.Unlock()
			if a.closed {
				return
			}
			a.wsToSend <- buf.Bytes()
		})

		a.connsMu.Lock()
		delete(a.conns, cmd.ID)
		a.connsMu.Unlock()

		if err != nil {
			log.Warn("connection processing failed", zap.Error(err))
		}

		return nil
	})

	return nil
}

func (a *App) wsCmdClose(ctx context.Context, cmd protocol.CmdClose) error {
	log := logging.FromContext(ctx)
	log.Debug("close connection", zap.Uint32("id", cmd.ID))

	a.connsMu.RLock()
	c, ok := a.conns[cmd.ID]
	a.connsMu.RUnlock()
	if !ok {
		return fmt.Errorf("connection does not exist: id=%d", cmd.ID)
	}

	c.RequestClose()

	return nil
}

func (a *App) wsCmdData(ctx context.Context, cmd protocol.CmdData) error {
	log := logging.FromContext(ctx)
	log.Debug("data", zap.Uint32("id", cmd.ID), zap.Int("len", len(cmd.Bytes)))

	a.connsMu.RLock()
	c, ok := a.conns[cmd.ID]
	a.connsMu.RUnlock()
	if !ok {
		return fmt.Errorf("connection does not exist: id=%d", cmd.ID)
	}

	c.RequestSend(cmd.Bytes)

	return nil
}
