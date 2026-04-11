package proxynode

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
	"github.com/tarik02/proxyhub/logging"
	"github.com/tarik02/proxyhub/pb"
	"github.com/tarik02/proxyhub/pb/pbhub"
	"github.com/tarik02/proxyhub/pb/pbnode"
	"github.com/tarik02/proxyhub/util"
	"github.com/tarik02/proxyhub/wsstream"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

type Params struct {
	Version         string
	Endpoint        string
	Username        string
	Password        string
	EgressWhitelist []string
}

var ErrShutdown = errors.New("shutdown")
var ErrDialFailed = errors.New("websocket dial failed")
var ErrYamuxServerFailed = errors.New("yamux server creation failed")
var ErrServerDisconnect = errors.New("server initiated disconnect")
var ErrAcceptStreamFailed = errors.New("accept stream failed")

type Proxynode struct {
	params Params

	shutdown      bool
	shutdownMu    sync.Mutex
	shutdownCh    chan struct{}
	shutdownErr   error
	shutdownErrMu sync.Mutex

	runDoneCh chan struct{}

	grpcClient pbhub.ServiceClient

	Handler         func(conn *yamux.Stream)
	OnConnected     func()
	OnServerMessage func(string)
}

func New(ctx context.Context, params Params) *Proxynode {
	app := &Proxynode{
		params: params,

		shutdownCh: make(chan struct{}),
		runDoneCh:  make(chan struct{}),

		Handler:         func(conn *yamux.Stream) {},
		OnConnected:     func() {},
		OnServerMessage: func(string) {},
	}

	go app.run(ctx)

	return app
}

func (a *Proxynode) Wait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-a.runDoneCh:
		return a.shutdownErr
	}
}

func (a *Proxynode) Close() error {
	return a.CloseWithError(ErrShutdown)
}

func (a *Proxynode) CloseWithError(err error) error {
	a.shutdownMu.Lock()
	if a.shutdown {
		a.shutdownMu.Unlock()
		return nil
	}
	a.shutdown = true
	a.shutdownMu.Unlock()

	a.shutdownErrMu.Lock()
	if a.shutdownErr == nil {
		a.shutdownErr = err
	}
	a.shutdownErrMu.Unlock()

	close(a.shutdownCh)

	<-a.runDoneCh
	return nil
}

func (a *Proxynode) CloseChan() <-chan struct{} {
	return a.shutdownCh
}

func (a *Proxynode) UpdateEgressWhitelist(ctx context.Context, whitelist []string) error {
	if a.grpcClient == nil {
		return fmt.Errorf("grpc client not initialized")
	}

	_, err := a.grpcClient.UpdatedEgressWhitelist(ctx, &pbhub.UpdatedEgressWhitelistRequest{
		EgressWhitelist: &pb.EgressWhitelist{
			Item: whitelist,
		},
	})

	return err
}

func (a *Proxynode) run(ctx context.Context) {
	defer close(a.runDoneCh)

	log := logging.FromContext(ctx)

	conn, resp, err := websocket.DefaultDialer.DialContext(ctx, a.params.Endpoint, http.Header{
		"Authorization": []string{util.HTTPBasicAuth(a.params.Username, a.params.Password)},
		"X-Version":     []string{a.params.Version},
	})
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		log.Warn("websocket dial failed", zap.Error(err))
		a.exitErr(fmt.Errorf("%w: %w", ErrDialFailed, err))
		return
	}

	wsConn := wsstream.New(conn)
	yamuxConfig := yamux.DefaultConfig()
	logging.ConfigureYamuxLogger(yamuxConfig, log)
	session, err := yamux.Server(wsConn, yamuxConfig)
	if err != nil {
		log.Warn("yamux server creation failed", zap.Error(err))
		a.exitErr(fmt.Errorf("%w: %w", ErrYamuxServerFailed, err))
		_ = conn.Close()
		return
	}
	a.OnConnected()

	control1, err := session.AcceptStreamWithContext(ctx)
	if err != nil {
		log.Warn("accept control stream failed", zap.Error(err))
		a.exitErr(fmt.Errorf("%w: %w", ErrAcceptStreamFailed, err))
		_ = conn.Close()
		return
	}
	defer func() {
		_ = control1.Close()
	}()

	control2, err := session.AcceptStreamWithContext(ctx)
	if err != nil {
		log.Warn("accept control stream failed", zap.Error(err))
		a.exitErr(fmt.Errorf("%w: %w", ErrAcceptStreamFailed, err))
		_ = conn.Close()
		return
	}
	defer func() {
		_ = control2.Close()
	}()

	grpcClient, err := util.GrpcClientFromConn(control1)
	if err != nil {
		a.exitErr(fmt.Errorf("creating gRPC client failed: %w", err))
		_ = conn.Close()
		return
	}
	defer func() {
		log.Debug("closing gRPC client connection")
		_ = grpcClient.Close()
	}()

	grpcServer := grpc.NewServer()
	defer func() {
		log.Debug("closing gRPC server")
		grpcServer.Stop()
	}()

	handler := &HandlerGRPC{proxy: a}
	pbnode.RegisterServiceServer(grpcServer, handler)

	if err := util.GrpcServeOnConn(grpcServer, control2); err != nil {
		a.exitErr(fmt.Errorf("serving gRPC on control stream failed: %w", err))
		_ = conn.Close()
		return
	}

	a.grpcClient = pbhub.NewServiceClient(grpcClient)

	chr, err := a.grpcClient.Hello(ctx, &pbhub.HelloRequest{
		EgressWhitelist: &pb.EgressWhitelist{
			Item: a.params.EgressWhitelist,
		},
	})
	if err != nil {
		a.exitErr(fmt.Errorf("sending client hello failed: %w", err))
		_ = conn.Close()
		return
	}

	log.Info("received client hello response", zap.Any("response", chr))

	var wg sync.WaitGroup
	acceptCtx, cancelAccept := context.WithCancel(ctx)
	defer cancelAccept()

	go func() {
		select {
		case <-ctx.Done():
		case <-a.runDoneCh:
			return
		case <-a.shutdownCh:
		}

		log.Debug("sending go away to session")
		_ = session.GoAway()
		cancelAccept()
	}()

	for {
		c, err := session.AcceptStreamWithContext(acceptCtx)
		if err != nil {
			switch {
			case errors.Is(err, context.Canceled):
				log.Debug("accept stream stopped", zap.Error(err))
			case a.isShutdown():
				log.Debug("accept stream stopped during shutdown", zap.Error(err))
			default:
				log.Warn("accept stream failed", zap.Error(err))
				a.exitErr(fmt.Errorf("%w: %w", ErrAcceptStreamFailed, err))
			}
			break
		}

		wg.Add(1)
		go func(conn *yamux.Stream) {
			defer wg.Done()
			a.Handler(conn)
		}(c)
	}

	log.Debug("not accepting new streams, waiting for existing handlers to finish")
	wgDoneCh := make(chan struct{})
	go func() {
		wg.Wait()
		close(wgDoneCh)
	}()

	select {
	case <-ctx.Done():
		log.Debug("context done, exiting run loop")
	case <-wgDoneCh:
		log.Debug("all handlers finished")
	}

	log.Debug("session close start")
	if err := session.Close(); err != nil {
		log.Debug("session close failed", zap.Error(err))
		a.exitErr(err)
	} else {
		log.Debug("session close completed")
	}
}

func (a *Proxynode) isShutdown() bool {
	a.shutdownMu.Lock()
	defer a.shutdownMu.Unlock()
	return a.shutdown
}

func (a *Proxynode) exitErr(err error) {
	a.shutdownErrMu.Lock()
	if a.shutdownErr == nil {
		a.shutdownErr = err
	}
	a.shutdownErrMu.Unlock()

	go func() {
		_ = a.Close()
	}()
}
