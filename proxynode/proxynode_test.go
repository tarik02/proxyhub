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
	"github.com/tarik02/proxyhub/pb"
	"google.golang.org/protobuf/encoding/protojson"
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

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := testUpgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade failed: %v", err)
			return
		}
		defer conn.Close()

		message, err := protojson.Marshal(&pb.Control{
			Message: &pb.Control_Disconnect_{
				Disconnect: &pb.Control_Disconnect{
					Reason: "maintenance window",
				},
			},
		})
		if err != nil {
			t.Errorf("marshal failed: %v", err)
			return
		}

		if err := conn.WriteMessage(websocket.TextMessage, message); err != nil {
			t.Errorf("write failed: %v", err)
			return
		}

		for {
			if _, _, err := conn.NextReader(); err != nil {
				return
			}
		}
	}))
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

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := testUpgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade failed: %v", err)
			return
		}

		time.Sleep(50 * time.Millisecond)
		_ = conn.Close()
	}))
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

	releaseConn := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := testUpgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade failed: %v", err)
			return
		}
		defer conn.Close()

		<-releaseConn
	}))
	defer server.Close()
	defer close(releaseConn)

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
