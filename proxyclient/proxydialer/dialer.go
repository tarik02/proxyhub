package proxydialer

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/tarik02/proxyhub/proxyclient"
	"github.com/tarik02/proxyhub/util"
	"github.com/tarik02/proxyhub/wsstream"
	"golang.org/x/net/proxy"
)

type Dialer struct {
	Options proxyclient.ClientOptions
	ID      string
}

func (p *Dialer) Dial(network, address string) (net.Conn, error) {
	return p.DialContext(context.Background(), network, address)
}

func (p *Dialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if network == "" && address == "" {
		header := http.Header{}
		header.Add("Authorization", fmt.Sprintf("Bearer %s", p.Options.Token))

		wsEndpoint := fmt.Sprintf("ws%s/socks/%s", strings.TrimPrefix(p.Options.Endpoint, "http"), p.ID)
		conn, resp, err := p.Options.WSDialer.DialContext(ctx, wsEndpoint, header)
		if resp != nil {
			_ = resp.Body.Close()
		}
		if err != nil {
			return nil, err
		}

		stream := wsstream.New(conn)
		a, b := net.Pipe()

		go util.Copy2(ctx, stream, b)

		return a, nil
	}

	s, err := proxy.SOCKS5("", "", nil, p)
	if err != nil {
		return nil, err
	}

	return util.DialProxyContext(ctx, s, network, address)
}

var _ proxy.Dialer = (*Dialer)(nil)
var _ proxy.ContextDialer = (*Dialer)(nil)
