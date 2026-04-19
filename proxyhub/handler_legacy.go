package proxyhub

import (
	"context"

	"github.com/tarik02/proxyhub/api"
)

type ProxyHandlerLegacy struct {
	proxy *Proxy
	info  api.Proxy
}

func NewProxyHandlerLegacy(proxy *Proxy, info api.Proxy) *ProxyHandlerLegacy {
	return &ProxyHandlerLegacy{
		proxy: proxy,
		info:  info,
	}
}

func (h *ProxyHandlerLegacy) Ready() chan struct{} {
	ready := make(chan struct{})
	close(ready)
	return ready
}

func (h *ProxyHandlerLegacy) SendMOTD(ctx context.Context, content string) error {
	return nil
}

func (h *ProxyHandlerLegacy) SendDisconnect(ctx context.Context, reason string) error {
	return nil
}

func (h *ProxyHandlerLegacy) Info() (api.Proxy, chan struct{}) {
	return h.info, nil
}

var _ ProxyHandler = (*ProxyHandlerLegacy)(nil)
