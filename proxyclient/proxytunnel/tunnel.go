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
	ProxyID string
}

type Tunnel struct {
	session *yamux.Session
}

func New(ctx context.Context, params TunnelParams) (*Tunnel, error) {
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

	res := &Tunnel{
		session: session,
	}
	return res, nil
}

func (p *Tunnel) Close() error {
	return p.session.Close()
}

func (p *Tunnel) CloseChan() <-chan struct{} {
	return p.session.CloseChan()
}

func (p *Tunnel) Dial(network, address string) (net.Conn, error) {
	return p.DialContext(context.Background(), network, address)
}

func (p *Tunnel) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if network == "" && address == "" {
		return p.session.OpenStream()
	}

	s, err := proxy.SOCKS5("", "", nil, p)
	if err != nil {
		return nil, err
	}

	return util.DialProxyContext(ctx, s, network, address)
}

var _ proxy.Dialer = (*Tunnel)(nil)
var _ proxy.ContextDialer = (*Tunnel)(nil)
