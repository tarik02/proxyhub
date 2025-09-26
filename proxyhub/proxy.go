package proxyhub

import (
	"context"
	"io"
	"net"
	"sync"

	"github.com/hashicorp/yamux"
	"github.com/tarik02/proxyhub/logging"
	"github.com/tarik02/proxyhub/util"
	"go.uber.org/zap"
	"golang.org/x/net/proxy"
)

type Proxy struct {
	id string

	session *yamux.Session
	handler ProxyHandler

	connsChan chan io.ReadWriteCloser

	shutdown      bool
	shutdownMu    sync.Mutex
	shutdownCh    chan struct{}
	shutdownErr   error
	shutdownErrMu sync.Mutex

	runDoneCh chan struct{}
	closedCh  chan struct{}

	OnConnection      func()
	OnConnectionStats func(recv, sent int64)
}

func NewProxy(ctx context.Context, id string, session *yamux.Session, handlerFactory func(*Proxy) (ProxyHandler, error)) (*Proxy, error) {
	res := &Proxy{
		id: id,

		session: session,

		connsChan: make(chan io.ReadWriteCloser),

		shutdownCh: make(chan struct{}),
		runDoneCh:  make(chan struct{}),
		closedCh:   make(chan struct{}),

		OnConnection:      func() {},
		OnConnectionStats: func(recv, sent int64) {},
	}

	handler, err := handlerFactory(res)
	if err != nil {
		return nil, err
	}

	res.handler = handler

	go res.run(ctx)

	return res, nil
}

func (p *Proxy) ID() string {
	return p.id
}

func (p *Proxy) Handler() ProxyHandler {
	return p.handler
}

func (p *Proxy) Wait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()

	case <-p.runDoneCh:
		return p.shutdownErr
	}
}

func (p *Proxy) Close() error {
	return p.CloseWithError(ErrShutdown)
}

func (p *Proxy) CloseWithError(err error) error {
	p.shutdownMu.Lock()
	defer p.shutdownMu.Unlock()

	if p.shutdown {
		return nil
	}
	p.shutdown = true

	p.shutdownErrMu.Lock()
	if p.shutdownErr == nil {
		p.shutdownErr = err
	}
	p.shutdownErrMu.Unlock()

	close(p.shutdownCh)

	<-p.runDoneCh

	close(p.closedCh)

	return nil
}

func (p *Proxy) CloseChan() <-chan struct{} {
	return p.shutdownCh
}

func (p *Proxy) run(ctx context.Context) {
	defer close(p.runDoneCh)

	log := logging.FromContext(ctx, zap.String("proxy_id", p.id)).Named("proxy")
	wg := sync.WaitGroup{}

	log.Debug("waiting for handler to be ready")
	select {
	case <-ctx.Done():
		break

	case <-p.shutdownCh:
		break

	case <-p.handler.Ready():
		log.Debug("handler is ready")
	}

loop:
	for {
		select {
		case <-ctx.Done():
			break loop

		case <-p.shutdownCh:
			break loop

		case <-p.session.CloseChan():
			p.exitErr(ErrSessionClosed)

		case conn := <-p.connsChan:
			wg.Add(1)
			go func() {
				defer wg.Done()

				stream, err := p.session.OpenStream()
				if err != nil {
					log.Warn("failed to open stream for connection", zap.Error(err))
					_ = conn.Close()
					return
				}

				go p.OnConnection()

				recv, sent, err := util.Copy2(ctx, stream, conn)
				_ = err

				go p.OnConnectionStats(recv, sent)
			}()
		}
	}

	select {
	case <-ctx.Done():
		log.Debug("context done, exiting run loop")

	default:
		log.Debug("waiting for connections to finish")

		wgDoneCh := make(chan struct{})
		go func() {
			wg.Wait()
			close(wgDoneCh)
		}()

		select {
		case <-ctx.Done():
			log.Debug("context done while waiting for connections, exiting run loop")

		case <-wgDoneCh:
			log.Debug("all connections finished")
		}
	}

	go func() {
		log.Debug("closing session")
		if err := p.session.Close(); err != nil {
			log.Debug("session close failed", zap.Error(err))
			p.exitErr(err)
		}
	}()

	select {
	case <-ctx.Done():
		log.Debug("context done, exiting run loop")

	case <-p.session.CloseChan():
		log.Debug("session closed, exiting run loop")
	}
}

func (p *Proxy) exitErr(err error) {
	p.shutdownErrMu.Lock()
	if p.shutdownErr == nil {
		p.shutdownErr = err
	}
	p.shutdownErrMu.Unlock()
	go func() {
		_ = p.Close()
	}()
}

func (p *Proxy) QueueConn(ctx context.Context, conn io.ReadWriteCloser) error {
	select {
	case <-ctx.Done():
		return ctx.Err()

	case <-p.shutdownCh:
		return p.shutdownErr

	case p.connsChan <- conn:
		return nil
	}
}

func (p *Proxy) Dial(network, addr string) (c net.Conn, err error) {
	return p.DialContext(context.Background(), network, addr)
}

func (p *Proxy) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if network == "" && addr == "" {
		a, b := net.Pipe()

		if err := p.QueueConn(ctx, a); err != nil {
			return nil, err
		}

		return b, nil
	}

	s, err := proxy.SOCKS5("", "", nil, p)
	if err != nil {
		return nil, err
	}

	if cd, ok := s.(proxy.ContextDialer); ok {
		return cd.DialContext(ctx, network, addr)
	}

	connChan := make(chan net.Conn)
	errChan := make(chan error)

	go func() {
		conn, err := s.Dial(network, addr)
		if err != nil {
			select {
			case <-ctx.Done():
			case errChan <- err:
			}
			return
		}

		select {
		case <-ctx.Done():
			_ = conn.Close()
		case connChan <- conn:
		}
	}()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case c := <-connChan:
		return c, nil
	case err := <-errChan:
		return nil, err
	}
}
