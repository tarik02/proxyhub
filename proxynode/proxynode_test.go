package proxynode

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
	"github.com/tarik02/proxyhub/pb"
	"github.com/tarik02/proxyhub/pb/pbhub"
	"github.com/tarik02/proxyhub/pb/pbnode"
	"github.com/tarik02/proxyhub/util"
	"github.com/tarik02/proxyhub/wsstream"
	"google.golang.org/grpc"
)

var testUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func TestProxynodeDialFailureReturnsPromptly(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	endpoint := wsURL(server.URL)
	server.Close()

	app := New(context.Background(), Params{
		Endpoint: endpoint,
	})
	defer func() { _ = app.Close() }()

	err := waitForProxynode(t, app)
	if !errors.Is(err, ErrDialFailed) {
		t.Fatalf("expected ErrDialFailed, got %v", err)
	}
}

func TestProxynodeServerDisconnectBecomesTerminalError(t *testing.T) {
	t.Parallel()

	server := newProxynodeTestServer(t, func(node pbnode.ServiceClient, conn *websocket.Conn) {
		_, _ = node.Disconnect(context.Background(), &pbnode.DisconnectRequest{
			Reason: "maintenance window",
		})
	})
	defer server.Close()

	app := New(context.Background(), Params{
		Endpoint: wsURL(server.URL),
	})
	defer func() { _ = app.Close() }()

	err := waitForProxynode(t, app)
	if !errors.Is(err, ErrServerDisconnect) {
		t.Fatalf("expected ErrServerDisconnect, got %v", err)
	}
	if !strings.Contains(err.Error(), "maintenance window") {
		t.Fatalf("expected disconnect reason in error, got %v", err)
	}
}

func TestProxynodeTransportCloseReturnsPromptly(t *testing.T) {
	t.Parallel()

	server := newProxynodeTestServer(t, func(node pbnode.ServiceClient, conn *websocket.Conn) {
		time.Sleep(50 * time.Millisecond)
		_ = conn.Close()
	})
	defer server.Close()

	app := New(context.Background(), Params{
		Endpoint: wsURL(server.URL),
	})
	defer func() { _ = app.Close() }()

	err := waitForProxynode(t, app)
	if !errors.Is(err, ErrAcceptStreamFailed) {
		t.Fatalf("expected ErrAcceptStreamFailed, got %v", err)
	}
}

func TestProxynodeShutdownReturnsErrShutdown(t *testing.T) {
	t.Parallel()

	server := newProxynodeTestServer(t, nil)
	defer server.Close()

	app := New(context.Background(), Params{
		Endpoint: wsURL(server.URL),
	})

	time.Sleep(50 * time.Millisecond)

	if err := app.Close(); err != nil {
		t.Fatalf("close failed: %v", err)
	}

	err := waitForProxynode(t, app)
	if !errors.Is(err, ErrShutdown) {
		t.Fatalf("expected ErrShutdown, got %v", err)
	}
}

type testHubService struct {
	pbhub.UnimplementedServiceServer
	helloCh chan struct{}
}

func (s *testHubService) Hello(ctx context.Context, req *pbhub.HelloRequest) (*pbhub.HelloResponse, error) {
	select {
	case s.helloCh <- struct{}{}:
	default:
	}

	return &pbhub.HelloResponse{}, nil
}

func (s *testHubService) UpdatedEgressWhitelist(ctx context.Context, req *pbhub.UpdatedEgressWhitelistRequest) (*pbhub.UpdatedEgressWhitelistResponse, error) {
	return &pbhub.UpdatedEgressWhitelistResponse{}, nil
}

func newProxynodeTestServer(t *testing.T, afterHello func(pbnode.ServiceClient, *websocket.Conn)) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := testUpgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade failed: %v", err)
			return
		}
		defer conn.Close()

		wsConn := wsstream.New(conn)
		session, err := yamux.Client(wsConn, yamux.DefaultConfig())
		if err != nil {
			t.Errorf("yamux client failed: %v", err)
			return
		}
		defer session.Close()

		control1, err := session.OpenStream()
		if err != nil {
			t.Errorf("open control stream failed: %v", err)
			return
		}
		defer control1.Close()

		control2, err := session.OpenStream()
		if err != nil {
			t.Errorf("open control stream failed: %v", err)
			return
		}
		defer control2.Close()

		grpcServer := grpc.NewServer()
		defer grpcServer.Stop()

		hub := &testHubService{helloCh: make(chan struct{}, 1)}
		pbhub.RegisterServiceServer(grpcServer, hub)

		if err := util.GrpcServeOnConn(grpcServer, control1); err != nil {
			t.Errorf("grpc serve failed: %v", err)
			return
		}

		nodeConn, err := util.GrpcClientFromConn(control2)
		if err != nil {
			t.Errorf("grpc client failed: %v", err)
			return
		}
		defer nodeConn.Close()

		if afterHello != nil {
			go func() {
				<-hub.helloCh
				afterHello(pbnode.NewServiceClient(nodeConn), conn)
			}()
		}

		<-session.CloseChan()
	}))
}

func waitForProxynode(t *testing.T, app *Proxynode) error {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := app.Wait(ctx)
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("timed out waiting for proxynode to exit")
	}

	return err
}

func wsURL(httpURL string) string {
	return "ws" + strings.TrimPrefix(httpURL, "http")
}

var _ = pb.EgressWhitelist{}
