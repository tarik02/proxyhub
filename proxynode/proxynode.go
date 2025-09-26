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
	OnServerMessage func(string)
}

func New(ctx context.Context, params Params) *Proxynode {
	app := &Proxynode{
		params: params,

		shutdown:   false,
		shutdownCh: make(chan struct{}),

		runDoneCh: make(chan struct{}),

		Handler:         func(conn *yamux.Stream) {},
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
	defer a.shutdownMu.Unlock()

	if a.shutdown {
		return nil
	}
	a.shutdown = true

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
		a.exitErr(err)
		return
	}

	log.Info("connected to server")

	wsstream := wsstream.New(conn)
	yamuxConfig := yamux.DefaultConfig()
	logging.ConfigureYamuxLogger(yamuxConfig, log)
	session, err := yamux.Server(wsstream, yamuxConfig)
	if err != nil {
		a.exitErr(err)
		_ = conn.Close()
		return
	}

	control1, err := session.AcceptStreamWithContext(ctx)
	if err != nil {
		a.exitErr(err)
		_ = conn.Close()
		return
	}
	defer func() {
		_ = control1.Close()
	}()

	control2, err := session.AcceptStreamWithContext(ctx)
	if err != nil {
		a.exitErr(err)
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

	handler := &HandlerGRPC{
		proxy: a,
	}
	pbnode.RegisterServiceServer(grpcServer, handler)

	if err := util.GrpcServeOnConn(grpcServer, control2); err != nil {
		a.exitErr(fmt.Errorf("serving gRPC on connection failed: %w", err))
		_ = conn.Close()
		return
	}

	defer func() {
		log.Debug("closing gRPC server")
		grpcServer.Stop()
	}()

	phc := pbhub.NewServiceClient(grpcClient)
	a.grpcClient = phc
	chr, err := phc.Hello(ctx, &pbhub.HelloRequest{
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

	acceptContext, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		select {
		case <-ctx.Done():
		case <-a.runDoneCh:
			return
		case <-a.shutdownCh:
		}
		log.Debug("sending go away to session")
		_ = session.GoAway()
		cancel()
	}()

	for {
		c, err := session.AcceptStreamWithContext(acceptContext)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				a.exitErr(err)
			}
			break
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			a.Handler(c)
		}()
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
		log.Debug("all handlers finished, closing session")
	}

	go func() {
		if err := session.Close(); err != nil {
			a.exitErr(err)
		}
	}()

	select {
	case <-ctx.Done():

	case <-session.CloseChan():
		log.Debug("session closed, exiting run loop")
	}
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
