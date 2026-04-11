package proxyhub

import (
	"context"
	"io"
	"net"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/tarik02/proxyhub/logging"
	"go.uber.org/zap"
	"golang.org/x/net/proxy"
)

const bridgeDrainTimeout = 5 * time.Second

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
	case <-p.closedCh:
		return p.shutdownErr
	}
}

func (p *Proxy) Err() error {
	p.shutdownErrMu.Lock()
	defer p.shutdownErrMu.Unlock()
	return p.shutdownErr
}

func (p *Proxy) Close() error {
	return p.CloseWithError(ErrShutdown)
}

func (p *Proxy) CloseWithError(err error) error {
	p.shutdownMu.Lock()
	if p.shutdown {
		p.shutdownMu.Unlock()
		return nil
	}
	p.shutdown = true
	p.shutdownMu.Unlock()

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
	case <-p.shutdownCh:
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
			log.Debug("session close signal received")
			p.exitErr(ErrSessionClosed)

		case conn := <-p.connsChan:
			wg.Add(1)
			go func(conn io.ReadWriteCloser) {
				defer wg.Done()

				stream, err := p.session.OpenStream()
				if err != nil {
					log.Warn("failed to open stream for connection", zap.Error(err))
					_ = conn.Close()
					return
				}

				go p.OnConnection()

				recv, sent := bridgeConn(ctx, conn, stream, bridgeDrainTimeout)
				go p.OnConnectionStats(recv, sent)
			}(conn)
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

	log.Debug("session close start")
	if err := p.session.Close(); err != nil {
		log.Debug("session close failed", zap.Error(err))
		p.exitErr(err)
	} else {
		log.Debug("session close completed")
	}
}

func (p *Proxy) isShutdown() bool {
	p.shutdownMu.Lock()
	defer p.shutdownMu.Unlock()
	return p.shutdown
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

type bridgeDirection int

const (
	bridgeDirectionConnToStream bridgeDirection = iota
	bridgeDirectionStreamToConn
)

type bridgeResult struct {
	direction bridgeDirection
	bytes     int64
	err       error
}

type readDeadlineSetter interface {
	SetReadDeadline(time.Time) error
}

type writeDeadlineSetter interface {
	SetWriteDeadline(time.Time) error
}

func bridgeConn(ctx context.Context, conn io.ReadWriteCloser, stream io.ReadWriteCloser, drainTimeout time.Duration) (recv int64, sent int64) {
	var closeConnOnce sync.Once
	var closeStreamOnce sync.Once

	closeConn := func() {
		closeConnOnce.Do(func() {
			_ = conn.Close()
		})
	}
	closeStream := func() {
		closeStreamOnce.Do(func() {
			_ = stream.Close()
		})
	}
	closeBoth := func() {
		interruptReadWrite(conn)
		interruptReadWrite(stream)
		closeConn()
		closeStream()
	}

	recordResult := func(result bridgeResult) {
		switch result.direction {
		case bridgeDirectionConnToStream:
			sent += result.bytes
		case bridgeDirectionStreamToConn:
			recv += result.bytes
		}
	}

	results := make(chan bridgeResult, 2)

	go func() {
		n, err := io.Copy(stream, conn)
		results <- bridgeResult{direction: bridgeDirectionConnToStream, bytes: n, err: err}
	}()

	go func() {
		n, err := io.Copy(conn, stream)
		results <- bridgeResult{direction: bridgeDirectionStreamToConn, bytes: n, err: err}
	}()

	select {
	case <-ctx.Done():
		closeBoth()
		return recv, sent

	case result := <-results:
		recordResult(result)

		switch result.direction {
		case bridgeDirectionConnToStream:
			closeStream()
			if second, ok := waitForBridgeDrain(ctx, results, drainTimeout); ok {
				recordResult(second)
				closeConn()
				return recv, sent
			}

		case bridgeDirectionStreamToConn:
			closeConn()
			if second, ok := waitForBridgeDrain(ctx, results, drainTimeout); ok {
				recordResult(second)
				closeStream()
				return recv, sent
			}
		}

		closeBoth()
		return recv, sent
	}
}

func waitForBridgeDrain(ctx context.Context, results <-chan bridgeResult, drainTimeout time.Duration) (bridgeResult, bool) {
	timer := time.NewTimer(drainTimeout)
	defer func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}()

	select {
	case <-ctx.Done():
		return bridgeResult{}, false
	case result := <-results:
		return result, true
	case <-timer.C:
		return bridgeResult{}, false
	}
}

func interruptReadWrite(rwc io.ReadWriteCloser) {
	now := time.Now()

	if setter, ok := rwc.(readDeadlineSetter); ok {
		_ = setter.SetReadDeadline(now)
	}
	if setter, ok := rwc.(writeDeadlineSetter); ok {
		_ = setter.SetWriteDeadline(now)
	}
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
