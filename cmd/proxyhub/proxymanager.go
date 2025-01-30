package main

import (
	"context"
	"errors"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/tarik02/proxyhub/logging"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
)

var ErrProxyAlreadyExists = errors.New("proxy already exists")

type ProxyManager struct {
	wsToSend chan []byte

	closed     bool
	newProxies chan *Proxy
	mu         sync.Mutex

	proxies   map[string]*Proxy
	proxiesMu sync.RWMutex

	wg *errgroup.Group

	OnProxyAdded   func(p *Proxy)
	OnProxyRemoved func(p *Proxy)
}

func NewProxyManager() *ProxyManager {
	return &ProxyManager{
		wsToSend: make(chan []byte, 32),

		newProxies: make(chan *Proxy, 16),

		proxies: make(map[string]*Proxy),

		OnProxyAdded:   func(p *Proxy) {},
		OnProxyRemoved: func(p *Proxy) {},
	}
}

func (p *ProxyManager) GetProxies() []*Proxy {
	p.proxiesMu.RLock()
	defer p.proxiesMu.RUnlock()

	proxies := make([]*Proxy, 0, len(p.proxies))
	for _, proxy := range p.proxies {
		proxies = append(proxies, proxy)
	}

	return proxies
}

func (p *ProxyManager) Run(ctx context.Context) error {
	p.wg, ctx = errgroup.WithContext(ctx)

	p.wg.Go(func() error {
		return p.handleNewProxies(ctx)
	})

	<-ctx.Done()

	p.mu.Lock()
	p.closed = true
	close(p.newProxies)
	p.mu.Unlock()

	return p.wg.Wait()
}

func (p *ProxyManager) handleNewProxies(ctx context.Context) error {
	log := logging.FromContext(ctx)

	for proxy := range p.newProxies {
		p.wg.Go(func() error {
			err := proxy.Run(ctx, func() {
				p.OnProxyAdded(proxy)
			})
			if err != nil {
				log.Error("proxy error", zap.Error(err))
			}
			p.proxiesMu.Lock()
			delete(p.proxies, proxy.id)
			p.proxiesMu.Unlock()
			p.OnProxyRemoved(proxy)
			return nil
		})
	}

	return nil
}

func (p *ProxyManager) HandleProxy(id string, conn *websocket.Conn) error {
	p.proxiesMu.Lock()
	_, ok := p.proxies[id]
	if ok {
		p.proxiesMu.Unlock()
		return ErrProxyAlreadyExists
	}
	proxy := NewProxy(id, conn)
	p.proxies[id] = proxy
	p.proxiesMu.Unlock()

	p.mu.Lock()
	if !p.closed {
		p.newProxies <- proxy
		p.mu.Unlock()
		return nil
	}
	p.mu.Unlock()

	return conn.Close()
}
