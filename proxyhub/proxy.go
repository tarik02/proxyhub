package proxyhub

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/tarik02/proxyhub/logging"
	"github.com/tarik02/proxyhub/pb"
	"github.com/tarik02/proxyhub/wsstream"
	"go.uber.org/zap"
	"golang.org/x/net/proxy"
	"google.golang.org/protobuf/encoding/protojson"
)

const bridgeDrainTimeout = 5 * time.Second

type Proxy struct {
	id      string
	version string
	started time.Time

	listener net.Listener
	ws       *wsstream.WSStream
	session  *yamux.Session

	connsChan chan io.ReadWriteCloser

	shutdown      bool
	shutdownMu    sync.Mutex
	shutdownCh    chan struct{}
	shutdownErr   error
	shutdownErrMu sync.Mutex

	acceptConnsDoneCh chan struct{}
	runDoneCh         chan struct{}
	closedCh          chan struct{}
}

func NewProxy(ctx context.Context, id string, version string, listener net.Listener, ws *wsstream.WSStream, session *yamux.Session) *Proxy {
	log := logging.FromContext(ctx, zap.String("proxy_id", id)).Named("proxy")

	res := &Proxy{
		id:      id,
		version: version,
		started: time.Now(),

		listener: listener,
		ws:       ws,
		session:  session,

		connsChan: make(chan io.ReadWriteCloser),

		shutdownCh:        make(chan struct{}),
		acceptConnsDoneCh: make(chan struct{}),
		runDoneCh:         make(chan struct{}),
		closedCh:          make(chan struct{}),
	}

	go res.acceptConns(ctx)
	go res.run(ctx)

	res.ws.HandleTextMessage = func(r io.Reader) {
		b, err := io.ReadAll(r)
		if err != nil {
			log.Warn("reading text message failed", zap.Error(err))
			return
		}

		msg := &pb.Control{}
		if err := protojson.Unmarshal(b, msg); err != nil {
			log.Warn("unmarshaling message failed", zap.Error(err))
			return
		}

		switch msg.Message.(type) {
		case *pb.Control_Disconnect_:
			res.exitErr(fmt.Errorf("client initiated disconnect: %s", msg.GetDisconnect().Reason))

		default:
			log.Warn("invalid message", zap.Any("message", msg))
		}
	}

	return res
}

func (p *Proxy) ID() string {
	return p.id
}

func (p *Proxy) Version() string {
	return p.version
}

func (p *Proxy) Started() time.Time {
	return p.started
}

func (p *Proxy) Port() int {
	if p.listener != nil {
		if addr, ok := p.listener.Addr().(*net.TCPAddr); ok {
			return addr.Port
		}
	}
	return 0
}

func (p *Proxy) SendMOTD(message string) error {
	return p.SendControlMessage(&pb.Control{
		Message: &pb.Control_Motd{
			Motd: &pb.Control_MOTD{
				Message: message,
			},
		},
	})
}

func (p *Proxy) SendControlMessage(msg *pb.Control) error {
	buf, err := protojson.Marshal(msg)
	if err != nil {
		return err
	}

	return p.ws.WriteText(string(buf))
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
	p.shutdownMu.Lock()
	if p.shutdown {
		p.shutdownMu.Unlock()
		return nil
	}
	p.shutdown = true
	p.shutdownMu.Unlock()

	p.shutdownErrMu.Lock()
	if p.shutdownErr == nil {
		p.shutdownErr = ErrShutdown
	}
	p.shutdownErrMu.Unlock()

	close(p.shutdownCh)

	<-p.acceptConnsDoneCh
	<-p.runDoneCh

	close(p.closedCh)

	return nil
}

func (p *Proxy) CloseChan() <-chan struct{} {
	return p.shutdownCh
}

func (p *Proxy) acceptConns(ctx context.Context) {
	defer close(p.acceptConnsDoneCh)

	log := logging.FromContext(ctx, zap.String("proxy_id", p.id)).Named("proxy")

	go func() {
		select {
		case <-ctx.Done():
		case <-p.shutdownCh:
		}

		log.Debug("closing listener")
		_ = p.listener.Close()
	}()

	log.Debug("accepting connections")
	for {
		conn, err := p.listener.Accept()
		if err != nil {
			if p.isShutdown() {
				log.Debug("accept loop stopped during shutdown", zap.Error(err))
			} else {
				log.Debug("accept failed", zap.Error(err))
			}
			p.exitErr(err)
			break
		}

		if err := p.QueueConn(ctx, conn); err != nil {
			if err := conn.Close(); err != nil {
				log.Warn("close failed", zap.Error(err))
			}
		}
	}
}

func (p *Proxy) run(ctx context.Context) {
	defer close(p.runDoneCh)

	log := logging.FromContext(ctx, zap.String("proxy_id", p.id)).Named("proxy")
	wg := sync.WaitGroup{}

loop:
	for {
		select {
		case <-ctx.Done():
			return

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
					_ = conn.Close()
					return
				}

				bridgeConn(ctx, conn, stream, bridgeDrainTimeout)
			}(conn)
		}
	}

	log.Debug("waiting for connections to finish")
	wg.Wait()
	log.Debug("all connections finished")

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
	err       error
}

type readDeadlineSetter interface {
	SetReadDeadline(time.Time) error
}

type writeDeadlineSetter interface {
	SetWriteDeadline(time.Time) error
}

func bridgeConn(ctx context.Context, conn io.ReadWriteCloser, stream io.ReadWriteCloser, drainTimeout time.Duration) {
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

	results := make(chan bridgeResult, 2)

	go func() {
		_, err := io.Copy(stream, conn)
		results <- bridgeResult{direction: bridgeDirectionConnToStream, err: err}
	}()

	go func() {
		_, err := io.Copy(conn, stream)
		results <- bridgeResult{direction: bridgeDirectionStreamToConn, err: err}
	}()

	select {
	case <-ctx.Done():
		closeBoth()
		return

	case result := <-results:
		switch result.direction {
		case bridgeDirectionConnToStream:
			closeStream()
			if waitForBridgeDrain(ctx, results, drainTimeout) {
				closeConn()
				return
			}

		case bridgeDirectionStreamToConn:
			closeConn()
			if waitForBridgeDrain(ctx, results, drainTimeout) {
				closeStream()
				return
			}
		}

		closeBoth()
	}
}

func waitForBridgeDrain(ctx context.Context, results <-chan bridgeResult, drainTimeout time.Duration) bool {
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
		return false
	case <-results:
		return true
	case <-timer.C:
		return false
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
