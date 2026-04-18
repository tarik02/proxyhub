package proxytunnel

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
	"github.com/tarik02/proxyhub/logging"
	"github.com/tarik02/proxyhub/proxyclient"
	"github.com/tarik02/proxyhub/util"
	"github.com/tarik02/proxyhub/wsstream"
	"golang.org/x/net/proxy"
)

type TunnelParams struct {
	proxyclient.ClientOptions
	Transport *proxyclient.Transport
	ProxyID   string
}

type Tunnel struct {
	proxyID string

	transport    *proxyclient.Transport
	ownTransport bool

	legacySession *yamux.Session
}

func New(ctx context.Context, params TunnelParams) (*Tunnel, error) {
	transport := params.Transport
	ownTransport := false
	var err error
	if transport == nil {
		transport, err = proxyclient.NewTransport(ctx, params.ClientOptions)
		if err != nil {
			return nil, err
		}
		ownTransport = true
	}

	return &Tunnel{
		proxyID:       params.ProxyID,
		transport:     transport,
		ownTransport:  ownTransport,
		legacySession: nil,
	}, nil
}

// NewLegacyWSTunnel keeps the previous per-proxy WebSocket tunnel transport.
func NewLegacyWSTunnel(ctx context.Context, params TunnelParams) (*Tunnel, error) {
	log := logging.FromContext(ctx)

	dialer := params.WSDialer
	if dialer == nil {
		dialer = websocket.DefaultDialer
	}

	wsEndpoint := fmt.Sprintf("ws%s/proxy/%s/tunnel", strings.TrimPrefix(params.Endpoint, "http"), params.ProxyID)
	conn, resp, err := dialer.DialContext(ctx, wsEndpoint, http.Header{
		"Authorization": []string{util.HTTPBearerAuth(params.Token)},
	})
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		return nil, fmt.Errorf("failed to connect to websocket server: %w", err)
	}

	wsstream := wsstream.New(conn)
	yamuxConfig := yamux.DefaultConfig()
	logging.ConfigureYamuxLogger(yamuxConfig, log)
	session, err := yamux.Server(wsstream, yamuxConfig)
	if err != nil {
		_ = wsstream.Close()
		return nil, fmt.Errorf("failed to create yamux session: %w", err)
	}

	return &Tunnel{
		proxyID:       params.ProxyID,
		legacySession: session,
	}, nil
}

func (p *Tunnel) Close() error {
	if p.transport != nil && p.ownTransport {
		return p.transport.Close()
	}
	if p.legacySession != nil {
		return p.legacySession.Close()
	}
	return nil
}

func (p *Tunnel) CloseChan() <-chan struct{} {
	if p.transport != nil {
		return p.transport.CloseChan()
	}
	return p.legacySession.CloseChan()
}

func (p *Tunnel) Err() error {
	if p.transport != nil {
		return p.transport.Err()
	}
	return nil
}

func (p *Tunnel) Dial(network, address string) (net.Conn, error) {
	return p.DialContext(context.Background(), network, address)
}

func (p *Tunnel) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if network == "" && address == "" {
		if p.transport != nil {
			return p.transport.OpenProxyStream(ctx, p.proxyID)
		}
		return p.legacySession.OpenStream()
	}

	s, err := proxy.SOCKS5("", "", nil, p)
	if err != nil {
		return nil, err
	}

	return util.DialProxyContext(ctx, s, network, address)
}

var _ proxy.Dialer = (*Tunnel)(nil)
var _ proxy.ContextDialer = (*Tunnel)(nil)
