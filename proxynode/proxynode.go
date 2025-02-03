package proxynode

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
	"github.com/tarik02/proxyhub/logging"
	"github.com/tarik02/proxyhub/pb"
	"github.com/tarik02/proxyhub/util"
	"github.com/tarik02/proxyhub/wsstream"
	"go.uber.org/zap"
	"google.golang.org/protobuf/encoding/protojson"
)

type Params struct {
	Version  string
	Endpoint string
	Username string
	Password string
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
	a.shutdownMu.Lock()
	defer a.shutdownMu.Unlock()

	if a.shutdown {
		return nil
	}
	a.shutdown = true

	a.shutdownErrMu.Lock()
	if a.shutdownErr == nil {
		a.shutdownErr = ErrShutdown
	}
	a.shutdownErrMu.Unlock()

	close(a.shutdownCh)

	<-a.runDoneCh
	return nil
}

func (a *Proxynode) CloseChan() <-chan struct{} {
	return a.shutdownCh
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
	session, err := yamux.Server(wsstream, yamux.DefaultConfig())
	if err != nil {
		a.exitErr(err)
		_ = conn.Close()
		return
	}

	wsstream.HandleTextMessage = func(r io.Reader) {
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
			a.exitErr(fmt.Errorf("server initiated disconnect: %s", msg.GetDisconnect().Reason))

		case *pb.Control_Motd:
			a.OnServerMessage(msg.GetMotd().Message)

		default:
			log.Warn("invalid message", zap.Any("message", msg))
		}
	}

	var wg sync.WaitGroup

	go func() {
		<-a.shutdownCh
		_ = session.GoAway()

		wg.Wait()

		if err := session.Close(); err != nil {
			a.exitErr(err)
		}
	}()

	for {
		c, err := session.AcceptStream()
		if err != nil {
			a.exitErr(err)
			break
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			a.Handler(c)
		}()
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
