package proxyhub

import (
	"context"
	"errors"
	"io"
	"net"
	"reflect"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/tarik02/proxyhub/api"
	"github.com/tarik02/proxyhub/entevents"
	"github.com/tarik02/proxyhub/logging"
	"github.com/tarik02/proxyhub/pb/pbclient"
	transportproto "github.com/tarik02/proxyhub/transport"
	"github.com/tarik02/proxyhub/util"
	"go.uber.org/zap"
)

func TestClientTransportWatchProxies(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(logging.WithLogger(context.Background(), zap.NewNop()))
	defer cancel()

	hub := New(ctx)
	defer hub.Close()

	events := entevents.New[api.Proxy](ctx)
	defer events.Close()

	session := newClientTransportSession(t, ctx, hub, events)
	defer session.Close()

	controlStream, err := session.OpenStream()
	if err != nil {
		t.Fatalf("OpenStream(control) error = %v", err)
	}
	defer controlStream.Close()

	if err := transportproto.WriteHeader(controlStream, transportproto.StreamHeader{
		Version: transportproto.Version,
		Purpose: transportproto.PurposeGRPCControl,
	}); err != nil {
		t.Fatalf("WriteHeader(control) error = %v", err)
	}
	time.Sleep(20 * time.Millisecond)

	grpcClient, err := util.GrpcClientFromConn(controlStream)
	if err != nil {
		t.Fatalf("GrpcClientFromConn() error = %v", err)
	}
	defer grpcClient.Close()

	stream, err := pbclient.NewServiceClient(grpcClient).WatchProxies(ctx, &pbclient.WatchProxiesRequest{})
	if err != nil {
		t.Fatalf("WatchProxies() error = %v", err)
	}

	initEvent := mustRecvProxyEvent(t, stream)
	if got := len(initEvent.GetInit().GetProxy()); got != 0 {
		t.Fatalf("init proxy count = %d, want 0", got)
	}

	added := api.Proxy{ID: "proxy-1", Version: "v1.0.0", Port: 1080, Started: 123, EgressWhitelist: []string{"example.com:443"}}
	if err := events.Add(ctx, added.ID, added); err != nil {
		t.Fatalf("events.Add() error = %v", err)
	}
	addEvent := mustRecvProxyEvent(t, stream)
	if got := api.ProxyFromPB(addEvent.GetAdd().GetProxy()); !reflect.DeepEqual(got, added) {
		t.Fatalf("add proxy = %#v, want %#v", got, added)
	}

	updated := added
	updated.Version = "v1.0.1"
	if err := events.Update(ctx, "update", updated.ID, updated, updated); err != nil {
		t.Fatalf("events.Update() error = %v", err)
	}
	updateEvent := mustRecvProxyEvent(t, stream)
	if got := api.ProxyFromPB(updateEvent.GetUpdate().GetProxy()); !reflect.DeepEqual(got, updated) {
		t.Fatalf("update proxy = %#v, want %#v", got, updated)
	}

	if err := events.Del(ctx, updated.ID); err != nil {
		t.Fatalf("events.Del() error = %v", err)
	}
	delEvent := mustRecvProxyEvent(t, stream)
	if got := delEvent.GetDel().GetId(); got != updated.ID {
		t.Fatalf("del id = %q, want %q", got, updated.ID)
	}
}

func TestClientTransportRoutesProxyStreamsByID(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(logging.WithLogger(context.Background(), zap.NewNop()))
	defer cancel()

	hub := New(ctx)
	defer hub.Close()

	events := entevents.New[api.Proxy](ctx)
	defer events.Close()

	session := newClientTransportSession(t, ctx, hub, events)
	defer session.Close()

	node1 := joinTestProxy(t, ctx, hub, "proxy-1")
	node2 := joinTestProxy(t, ctx, hub, "proxy-2")

	clientConn1, err := session.OpenStream()
	if err != nil {
		t.Fatalf("OpenStream(proxy-1) error = %v", err)
	}
	defer clientConn1.Close()

	if err := transportproto.WriteHeader(clientConn1, transportproto.StreamHeader{
		Version: transportproto.Version,
		Purpose: transportproto.PurposeProxyTunnel,
		ProxyID: "proxy-1",
	}); err != nil {
		t.Fatalf("WriteHeader(proxy-1) error = %v", err)
	}

	clientConn2, err := session.OpenStream()
	if err != nil {
		t.Fatalf("OpenStream(proxy-2) error = %v", err)
	}
	defer clientConn2.Close()

	if err := transportproto.WriteHeader(clientConn2, transportproto.StreamHeader{
		Version: transportproto.Version,
		Purpose: transportproto.PurposeProxyTunnel,
		ProxyID: "proxy-2",
	}); err != nil {
		t.Fatalf("WriteHeader(proxy-2) error = %v", err)
	}

	nodeConn1 := acceptTestProxyStream(t, ctx, node1)
	defer nodeConn1.Close()

	nodeConn2 := acceptTestProxyStream(t, ctx, node2)
	defer nodeConn2.Close()

	mustWriteAll(t, clientConn1, []byte("one"))
	if got := mustReadN(t, nodeConn1, 3); string(got) != "one" {
		t.Fatalf("proxy-1 payload = %q, want %q", got, "one")
	}

	mustWriteAll(t, clientConn2, []byte("two"))
	if got := mustReadN(t, nodeConn2, 3); string(got) != "two" {
		t.Fatalf("proxy-2 payload = %q, want %q", got, "two")
	}

	mustWriteAll(t, nodeConn1, []byte("uno"))
	if got := mustReadN(t, clientConn1, 3); string(got) != "uno" {
		t.Fatalf("client proxy-1 response = %q, want %q", got, "uno")
	}

	mustWriteAll(t, nodeConn2, []byte("dos"))
	if got := mustReadN(t, clientConn2, 3); string(got) != "dos" {
		t.Fatalf("client proxy-2 response = %q, want %q", got, "dos")
	}
}

func TestClientTransportRejectsDuplicateControlStream(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(logging.WithLogger(context.Background(), zap.NewNop()))
	defer cancel()

	hub := New(ctx)
	defer hub.Close()

	events := entevents.New[api.Proxy](ctx)
	defer events.Close()

	session := newClientTransportSession(t, ctx, hub, events)
	defer session.Close()

	node := joinTestProxy(t, ctx, hub, "proxy-1")

	control1, err := session.OpenStream()
	if err != nil {
		t.Fatalf("OpenStream(control1) error = %v", err)
	}
	defer control1.Close()

	if err := transportproto.WriteHeader(control1, transportproto.StreamHeader{
		Version: transportproto.Version,
		Purpose: transportproto.PurposeGRPCControl,
	}); err != nil {
		t.Fatalf("WriteHeader(control1) error = %v", err)
	}

	grpcClient, err := util.GrpcClientFromConn(control1)
	if err != nil {
		t.Fatalf("GrpcClientFromConn(control1) error = %v", err)
	}
	defer grpcClient.Close()

	controlStream, err := pbclient.NewServiceClient(grpcClient).WatchProxies(ctx, &pbclient.WatchProxiesRequest{})
	if err != nil {
		t.Fatalf("WatchProxies(control1) error = %v", err)
	}
	_ = mustRecvProxyEvent(t, controlStream)

	control2, err := session.OpenStream()
	if err != nil {
		t.Fatalf("OpenStream(control2) error = %v", err)
	}
	defer control2.Close()

	if err := transportproto.WriteHeader(control2, transportproto.StreamHeader{
		Version: transportproto.Version,
		Purpose: transportproto.PurposeGRPCControl,
	}); err != nil {
		t.Fatalf("WriteHeader(control2) error = %v", err)
	}

	readErrCh := make(chan error, 1)
	go func() {
		var buf [1]byte
		_ = control2.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		_, err := control2.Read(buf[:])
		readErrCh <- err
	}()

	select {
	case err := <-readErrCh:
		if err == nil {
			t.Fatal("duplicate control stream stayed open")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for duplicate control stream to close")
	}

	tunnel, err := session.OpenStream()
	if err != nil {
		t.Fatalf("OpenStream(tunnel) error = %v", err)
	}
	defer tunnel.Close()

	if err := transportproto.WriteHeader(tunnel, transportproto.StreamHeader{
		Version: transportproto.Version,
		Purpose: transportproto.PurposeProxyTunnel,
		ProxyID: "proxy-1",
	}); err != nil {
		t.Fatalf("WriteHeader(tunnel) error = %v", err)
	}

	nodeConn := acceptTestProxyStream(t, ctx, node)
	defer nodeConn.Close()

	mustWriteAll(t, tunnel, []byte("ok"))
	if got := mustReadN(t, nodeConn, 2); string(got) != "ok" {
		t.Fatalf("tunnel payload = %q, want %q", got, "ok")
	}
}

func TestClientTransportShutdownClosesControlSession(t *testing.T) {
	t.Parallel()

	baseCtx := logging.WithLogger(context.Background(), zap.NewNop())
	handlerCtx, handlerCancel := context.WithCancel(baseCtx)
	defer handlerCancel()

	hub := New(baseCtx)
	defer hub.Close()

	events := entevents.New[api.Proxy](baseCtx)
	defer events.Close()

	log := zap.NewNop()
	clientConfig := yamux.DefaultConfig()
	logging.ConfigureYamuxLogger(clientConfig, log)
	serverConfig := yamux.DefaultConfig()
	logging.ConfigureYamuxLogger(serverConfig, log)

	clientConn, serverConn := net.Pipe()
	clientSession, err := yamux.Server(clientConn, clientConfig)
	if err != nil {
		t.Fatalf("yamux.Server() error = %v", err)
	}

	serverSession, err := yamux.Client(serverConn, serverConfig)
	if err != nil {
		t.Fatalf("yamux.Client() error = %v", err)
	}

	handlerDone := make(chan error, 1)
	go func() {
		handlerDone <- NewClientTransportHandler(hub, events).HandleSession(handlerCtx, serverSession)
	}()

	t.Cleanup(func() {
		_ = clientSession.Close()
		_ = serverSession.Close()
		_ = clientConn.Close()
		_ = serverConn.Close()
	})

	controlStream, err := clientSession.OpenStream()
	if err != nil {
		t.Fatalf("OpenStream(control) error = %v", err)
	}
	defer controlStream.Close()

	if err := transportproto.WriteHeader(controlStream, transportproto.StreamHeader{
		Version: transportproto.Version,
		Purpose: transportproto.PurposeGRPCControl,
	}); err != nil {
		t.Fatalf("WriteHeader(control) error = %v", err)
	}

	grpcClient, err := util.GrpcClientFromConn(controlStream)
	if err != nil {
		t.Fatalf("GrpcClientFromConn() error = %v", err)
	}
	defer grpcClient.Close()

	rpcCtx, rpcCancel := context.WithCancel(context.Background())
	defer rpcCancel()

	stream, err := pbclient.NewServiceClient(grpcClient).WatchProxies(rpcCtx, &pbclient.WatchProxiesRequest{})
	if err != nil {
		t.Fatalf("WatchProxies() error = %v", err)
	}

	initEvent := mustRecvProxyEvent(t, stream)
	if got := len(initEvent.GetInit().GetProxy()); got != 0 {
		t.Fatalf("init proxy count = %d, want 0", got)
	}

	handlerCancel()

	select {
	case err := <-handlerDone:
		if err != nil {
			t.Fatalf("HandleSession() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for HandleSession to return")
	}

	select {
	case <-clientSession.CloseChan():
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for client session to close")
	}

	recvErrCh := make(chan error, 1)
	go func() {
		_, err := stream.Recv()
		recvErrCh <- err
	}()

	select {
	case err := <-recvErrCh:
		if err == nil {
			t.Fatal("WatchProxies stream stayed open after shutdown")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for WatchProxies stream to close")
	}
}

func newClientTransportSession(t *testing.T, ctx context.Context, hub *Proxyhub, events *entevents.Manager[api.Proxy]) *yamux.Session {
	t.Helper()

	log := zap.NewNop()
	clientConfig := yamux.DefaultConfig()
	logging.ConfigureYamuxLogger(clientConfig, log)
	serverConfig := yamux.DefaultConfig()
	logging.ConfigureYamuxLogger(serverConfig, log)

	clientConn, serverConn := net.Pipe()
	clientSession, err := yamux.Server(clientConn, clientConfig)
	if err != nil {
		t.Fatalf("yamux.Server() error = %v", err)
	}

	serverSession, err := yamux.Client(serverConn, serverConfig)
	if err != nil {
		t.Fatalf("yamux.Client() error = %v", err)
	}

	handler := NewClientTransportHandler(hub, events)
	go func() {
		if err := handler.HandleSession(ctx, serverSession); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
			t.Errorf("HandleSession() error = %v", err)
		}
	}()

	t.Cleanup(func() {
		clientSession.Close()
		serverSession.Close()
		clientConn.Close()
		serverConn.Close()
	})

	return clientSession
}

func joinTestProxy(t *testing.T, ctx context.Context, hub *Proxyhub, id string) *yamux.Session {
	t.Helper()

	log := zap.NewNop()
	clientConfig := yamux.DefaultConfig()
	logging.ConfigureYamuxLogger(clientConfig, log)
	serverConfig := yamux.DefaultConfig()
	logging.ConfigureYamuxLogger(serverConfig, log)

	clientConn, serverConn := net.Pipe()
	hubSession, err := yamux.Client(clientConn, clientConfig)
	if err != nil {
		t.Fatalf("yamux.Client() error = %v", err)
	}

	nodeSession, err := yamux.Server(serverConn, serverConfig)
	if err != nil {
		t.Fatalf("yamux.Server() error = %v", err)
	}

	proxy, err := NewProxy(ctx, id, hubSession, func(proxy *Proxy) (ProxyHandler, error) {
		return NewProxyHandlerLegacy(proxy, api.Proxy{ID: id}), nil
	})
	if err != nil {
		t.Fatalf("NewProxy() error = %v", err)
	}

	if err := hub.HandleJoinProxy(ctx, proxy); err != nil {
		t.Fatalf("HandleJoinProxy() error = %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for hub.GetProxyByID(id) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("proxy %q did not register in time", id)
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Cleanup(func() {
		proxy.Close()
		nodeSession.Close()
		clientConn.Close()
		serverConn.Close()
	})

	return nodeSession
}

func acceptTestProxyStream(t *testing.T, ctx context.Context, session *yamux.Session) net.Conn {
	t.Helper()

	type result struct {
		conn net.Conn
		err  error
	}

	ch := make(chan result, 1)
	go func() {
		conn, err := session.AcceptStreamWithContext(ctx)
		ch <- result{conn: conn, err: err}
	}()

	select {
	case res := <-ch:
		if res.err != nil {
			t.Fatalf("AcceptStreamWithContext() error = %v", res.err)
		}
		return res.conn
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for proxy stream")
		return nil
	}
}

func mustRecvProxyEvent(t *testing.T, stream pbclient.Service_WatchProxiesClient) *pbclient.ProxyEvent {
	t.Helper()

	type result struct {
		event *pbclient.ProxyEvent
		err   error
	}

	ch := make(chan result, 1)
	go func() {
		event, err := stream.Recv()
		ch <- result{event: event, err: err}
	}()

	select {
	case res := <-ch:
		if res.err != nil {
			t.Fatalf("Recv() error = %v", res.err)
		}
		return res.event
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for proxy event")
		return nil
	}
}

func mustReadN(t *testing.T, r io.Reader, n int) []byte {
	t.Helper()

	type result struct {
		buf []byte
		err error
	}

	ch := make(chan result, 1)
	go func() {
		buf := make([]byte, n)
		_, err := io.ReadFull(r, buf)
		ch <- result{buf: buf, err: err}
	}()

	select {
	case res := <-ch:
		if res.err != nil {
			t.Fatalf("ReadFull() error = %v", res.err)
		}
		return res.buf
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for read")
		return nil
	}
}

func mustWriteAll(t *testing.T, w io.Writer, data []byte) {
	t.Helper()

	if _, err := w.Write(data); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
}
