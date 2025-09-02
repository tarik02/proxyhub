package proxyhub

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"

	"github.com/elazarl/goproxy"
	"github.com/elazarl/goproxy/ext/auth"
	"github.com/tarik02/proxyhub/logging"
	"github.com/tarik02/proxyhub/util"
	"go.uber.org/zap"
)

type Proxyhub struct {
	proxies       map[string]*Proxy
	proxiesMu     sync.RWMutex
	proxiesNew    chan *Proxy
	proxiesRemove chan *Proxy

	shutdown      bool
	shutdownMu    sync.Mutex
	shutdownCh    chan struct{}
	shutdownErr   error
	shutdownErrMu sync.Mutex

	runDoneCh chan struct{}

	ValidateAPIToken func(string) bool
	OnProxyAdded     func(*Proxy)
	OnProxyRemoved   func(*Proxy)
	OnConnection     func()
}

func New(ctx context.Context) *Proxyhub {
	p := &Proxyhub{
		proxies: make(map[string]*Proxy),

		proxiesNew:    make(chan *Proxy),
		proxiesRemove: make(chan *Proxy),

		shutdownCh: make(chan struct{}),

		runDoneCh: make(chan struct{}),

		ValidateAPIToken: func(string) bool { return false },
		OnProxyAdded:     func(*Proxy) {},
		OnProxyRemoved:   func(*Proxy) {},
	}

	go p.run(ctx)

	return p
}

func (p *Proxyhub) Wait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()

	case <-p.runDoneCh:
		return p.shutdownErr
	}
}

func (p *Proxyhub) Close() error {
	p.shutdownMu.Lock()
	defer p.shutdownMu.Unlock()

	if p.shutdown {
		return nil
	}
	p.shutdown = true

	p.shutdownErrMu.Lock()
	if p.shutdownErr == nil {
		p.shutdownErr = ErrShutdown
	}
	p.shutdownErrMu.Unlock()

	close(p.shutdownCh)

	<-p.runDoneCh
	return nil
}

func (p *Proxyhub) CloseChan() <-chan struct{} {
	return p.shutdownCh
}

func (p *Proxyhub) run(ctx context.Context) {
	defer close(p.runDoneCh)

	log := logging.FromContext(ctx).Named("proxyhub")

loop:
	for {
		select {
		case <-p.shutdownCh:
			break loop

		case proxy := <-p.proxiesNew:
			p.proxiesMu.Lock()
			if oldProxy, ok := p.proxies[proxy.ID()]; ok {
				go func() {
					log.Warn("proxy already exists, disconnecting it", zap.String("proxy_id", proxy.ID()))
					_ = oldProxy.Handler().SendDisconnect(ctx, "another proxy with the same ID connected")
					_ = oldProxy.Close()
				}()
			}
			p.proxies[proxy.ID()] = proxy
			p.proxiesMu.Unlock()

			log.Info("proxy added", zap.String("proxy_id", proxy.ID()))

			go func() {
				err := proxy.Wait(ctx)

				if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, ErrShutdown) && !errors.Is(err, ErrSessionClosed) {
					log.Error("proxy error", zap.Error(err))
				}

				select {
				case <-p.shutdownCh:
				case p.proxiesRemove <- proxy:
				}
			}()

			p.OnProxyAdded(proxy)

		case proxy := <-p.proxiesRemove:
			p.proxiesMu.Lock()
			if p.proxies[proxy.ID()] != proxy {
				p.proxiesMu.Unlock()
				continue loop
			}
			delete(p.proxies, proxy.ID())
			p.proxiesMu.Unlock()

			log.Info("proxy removed", zap.String("proxy_id", proxy.ID()))

			p.OnProxyRemoved(proxy)
		}
	}

	var wg sync.WaitGroup

	log.Debug("disconnecting proxies")
	for _, proxy := range p.proxies {
		p.OnProxyRemoved(proxy)

		wg.Add(1)
		go func() {
			defer wg.Done()

			if err := proxy.Handler().SendDisconnect(ctx, "proxyhub is shutting down"); err != nil {
				log.Debug("proxy disconnect error", zap.String("proxy_id", proxy.ID()), zap.Error(err))
			}

			log.Debug("closing proxy connection", zap.String("proxy_id", proxy.ID()))
			_ = proxy.Close()
		}()
	}

	log.Debug("waiting for all proxies to be closed")
	wg.Wait()
	log.Debug("all proxies closed")
}

func (p *Proxyhub) exitErr(err error) { // nolint:unused
	p.shutdownErrMu.Lock()
	if p.shutdownErr == nil {
		p.shutdownErr = err
	}
	p.shutdownErrMu.Unlock()
	go func() {
		_ = p.Close()
	}()
}

func (p *Proxyhub) HandleJoinProxy(ctx context.Context, proxy *Proxy) error {
	select {
	case <-ctx.Done():
		return ctx.Err()

	case <-p.shutdownCh:
		return p.shutdownErr

	case p.proxiesNew <- proxy:
		return nil
	}
}

func (p *Proxyhub) GetProxyByID(id string) *Proxy {
	p.proxiesMu.RLock()
	defer p.proxiesMu.RUnlock()
	return p.proxies[id]
}

func (p *Proxyhub) handleProxy(req *http.Request, c *goproxy.ProxyCtx) (*http.Request, *http.Response) {
	ok, username, password := util.PullProxyAuth(req)
	if !ok || !p.ValidateAPIToken(password) {
		return req, auth.BasicUnauthorized(req, "proxy")
	}

	proxy := p.GetProxyByID(username)
	if proxy == nil {
		return req, goproxy.NewResponse(req, "text/plain", http.StatusNotFound, "Proxy not found")
	}

	c.RoundTripper = goproxy.RoundTripperFunc(func(req *http.Request, c *goproxy.ProxyCtx) (*http.Response, error) {
		client := http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
					return proxy.DialContext(ctx, network, addr)
				},
			},
		}
		return client.Do(req)
	})

	c.Dialer = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return proxy.DialContext(ctx, network, addr)
	}

	return req, nil
}

func (p *Proxyhub) ProxyHandler() goproxy.FuncReqHandler {
	return goproxy.FuncReqHandler(p.handleProxy)
}

func (p *Proxyhub) ProxyConnectHandler() goproxy.FuncHttpsHandler {
	return goproxy.FuncHttpsHandler(func(host string, ctx *goproxy.ProxyCtx) (*goproxy.ConnectAction, string) {
		_, res := p.handleProxy(ctx.Req, ctx) // nolint:bodyclose

		if res != nil {
			ctx.Resp = res
			return goproxy.RejectConnect, host
		}

		return nil, host
	})
}
