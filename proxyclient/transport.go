package proxyclient

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
	"github.com/tarik02/proxyhub/logging"
	"github.com/tarik02/proxyhub/pb/pbclient"
	transportproto "github.com/tarik02/proxyhub/transport"
	"github.com/tarik02/proxyhub/util"
	"github.com/tarik02/proxyhub/wsstream"
	"google.golang.org/grpc"
)

type Transport struct {
	session  *yamux.Session
	wsstream *wsstream.WSStream

	shutdown      bool
	shutdownMu    sync.Mutex
	shutdownCh    chan struct{}
	shutdownOnce  sync.Once
	shutdownErr   error
	shutdownErrMu sync.Mutex

	runDoneCh chan struct{}

	cleanupOnce sync.Once

	controlMu     sync.Mutex
	controlClient *grpc.ClientConn
}

func NewTransport(ctx context.Context, opts ClientOptions) (*Transport, error) {
	log := logging.FromContext(ctx)

	dialer := opts.WSDialer
	if dialer == nil {
		dialer = websocket.DefaultDialer
	}

	wsEndpoint := fmt.Sprintf("ws%s/api/client/transport", strings.TrimPrefix(opts.Endpoint, "http"))
	conn, resp, err := dialer.DialContext(ctx, wsEndpoint, http.Header{
		"Authorization": []string{util.HTTPBearerAuth(opts.Token)},
	})
	if resp != nil {
		defer resp.Body.Close()
	}
	if err != nil {
		if resp != nil {
			switch resp.StatusCode {
			case http.StatusUnauthorized:
				return nil, ErrUnauthorized
			case http.StatusNotFound:
				return nil, ErrNotFound
			default:
				return nil, &UnexpectedStatusError{StatusCode: resp.StatusCode}
			}
		}

		return nil, fmt.Errorf("failed to connect to websocket server: %w", err)
	}

	ws := wsstream.New(conn)
	yamuxConfig := yamux.DefaultConfig()
	logging.ConfigureYamuxLogger(yamuxConfig, log)
	session, err := yamux.Server(ws, yamuxConfig)
	if err != nil {
		_ = ws.Close()
		return nil, fmt.Errorf("failed to create yamux session: %w", err)
	}

	res := &Transport{
		session:  session,
		wsstream: ws,

		shutdownCh: make(chan struct{}),
		runDoneCh:  make(chan struct{}),
	}

	go res.run()
	go func() {
		select {
		case <-ctx.Done():
			_ = res.CloseWithError(ctx.Err())
		case <-res.runDoneCh:
		}
	}()

	return res, nil
}

func (t *Transport) run() {
	defer close(t.runDoneCh)

	<-t.session.CloseChan()

	t.shutdownErrMu.Lock()
	if t.shutdownErr == nil {
		t.shutdownErr = ErrTransportClosed
	}
	t.shutdownErrMu.Unlock()

	t.closeShutdownCh()
	t.cleanup()
}

func (t *Transport) cleanup() {
	t.cleanupOnce.Do(func() {
		t.controlMu.Lock()
		if t.controlClient != nil {
			_ = t.controlClient.Close()
			t.controlClient = nil
		}
		t.controlMu.Unlock()

		_ = t.session.Close()
		_ = t.wsstream.Close()
	})
}

func (t *Transport) Close() error {
	return t.CloseWithError(ErrShutdown)
}

func (t *Transport) CloseWithError(err error) error {
	t.shutdownMu.Lock()
	defer t.shutdownMu.Unlock()

	if t.shutdown {
		return nil
	}
	t.shutdown = true

	t.shutdownErrMu.Lock()
	if t.shutdownErr == nil {
		t.shutdownErr = err
	}
	t.shutdownErrMu.Unlock()

	t.closeShutdownCh()
	t.cleanup()

	<-t.runDoneCh
	return nil
}

func (t *Transport) CloseChan() <-chan struct{} {
	return t.shutdownCh
}

func (t *Transport) Err() error {
	t.shutdownErrMu.Lock()
	defer t.shutdownErrMu.Unlock()
	return t.shutdownErr
}

func (t *Transport) closeShutdownCh() {
	t.shutdownOnce.Do(func() {
		close(t.shutdownCh)
	})
}

func (t *Transport) ensureControlClient() (*grpc.ClientConn, error) {
	t.controlMu.Lock()
	defer t.controlMu.Unlock()

	if t.controlClient != nil {
		return t.controlClient, nil
	}

	stream, err := t.session.OpenStream()
	if err != nil {
		return nil, err
	}

	if err := transportproto.WriteHeader(stream, transportproto.StreamHeader{
		Version: transportproto.Version,
		Purpose: transportproto.PurposeGRPCControl,
	}); err != nil {
		_ = stream.Close()
		return nil, err
	}

	client, err := util.GrpcClientFromConn(stream)
	if err != nil {
		_ = stream.Close()
		return nil, err
	}

	t.controlClient = client
	return client, nil
}

func (t *Transport) WatchProxies(ctx context.Context) (pbclient.Service_WatchProxiesClient, error) {
	client, err := t.ensureControlClient()
	if err != nil {
		return nil, err
	}

	return pbclient.NewServiceClient(client).WatchProxies(ctx, &pbclient.WatchProxiesRequest{})
}

func (t *Transport) OpenProxyStream(ctx context.Context, proxyID string) (net.Conn, error) {
	stream, err := t.session.OpenStream()
	if err != nil {
		return nil, err
	}

	if err := transportproto.WriteHeader(stream, transportproto.StreamHeader{
		Version: transportproto.Version,
		Purpose: transportproto.PurposeProxyTunnel,
		ProxyID: proxyID,
	}); err != nil {
		_ = stream.Close()
		return nil, err
	}

	return stream, nil
}
