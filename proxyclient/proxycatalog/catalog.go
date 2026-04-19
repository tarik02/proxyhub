package proxycatalog

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/tarik02/proxyhub/api"
	"github.com/tarik02/proxyhub/logging"
	"github.com/tarik02/proxyhub/pb/pbclient"
	"github.com/tarik02/proxyhub/proxyclient"
	"go.uber.org/zap"
)

var ErrShutdown = errors.New("shutdown")
var ErrUnauthorized = proxyclient.ErrUnauthorized
var ErrNotFound = proxyclient.ErrNotFound

type UnexpectedStatusError = proxyclient.UnexpectedStatusError

type Client struct {
	shutdown      bool
	shutdownMu    sync.Mutex
	shutdownCh    chan struct{}
	shutdownErr   error
	shutdownErrMu sync.Mutex

	readyCh   chan struct{}
	readyOnce sync.Once
	runDoneCh chan struct{}

	eventsCh        chan any
	eventsCloseOnce sync.Once

	connectAndProcess func(context.Context) error
}

func NewClient(ctx context.Context, opts proxyclient.ClientOptions) *Client {
	c := &Client{
		shutdown:   false,
		shutdownCh: make(chan struct{}),

		readyCh:   make(chan struct{}),
		runDoneCh: make(chan struct{}),

		eventsCh: make(chan any, 128),
	}
	c.connectAndProcess = func(ctx context.Context) error {
		return c.connectAndProcessTransport(ctx, opts)
	}

	go c.run(ctx)

	return c
}

// NewLegacySSEClient keeps the previous SSE-backed proxy catalog transport.
func NewLegacySSEClient(ctx context.Context, opts proxyclient.ClientOptions) *Client {
	c := &Client{
		shutdown:   false,
		shutdownCh: make(chan struct{}),

		readyCh:   make(chan struct{}),
		runDoneCh: make(chan struct{}),

		eventsCh: make(chan any, 128),
	}
	c.connectAndProcess = func(ctx context.Context) error {
		return c.connectAndProcessLegacySSE(ctx, opts)
	}

	go c.run(ctx)

	return c
}

func (c *Client) Ready() <-chan struct{} {
	return c.readyCh
}

func (c *Client) Events() <-chan any {
	return c.eventsCh
}

func (c *Client) Wait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()

	case <-c.runDoneCh:
		return c.shutdownErr
	}
}

func (c *Client) Close() error {
	c.shutdownMu.Lock()
	defer c.shutdownMu.Unlock()

	if c.shutdown {
		return nil
	}
	c.shutdown = true

	c.shutdownErrMu.Lock()
	if c.shutdownErr == nil {
		c.shutdownErr = ErrShutdown
	}
	c.shutdownErrMu.Unlock()

	close(c.shutdownCh)

	<-c.runDoneCh
	return nil
}

func (c *Client) CloseChan() <-chan struct{} {
	return c.shutdownCh
}

func (c *Client) Err() error {
	c.shutdownErrMu.Lock()
	defer c.shutdownErrMu.Unlock()
	return c.shutdownErr
}

func (c *Client) exitErr(err error) {
	c.shutdownErrMu.Lock()
	if c.shutdownErr == nil {
		c.shutdownErr = err
	}
	c.shutdownErrMu.Unlock()
	go func() {
		_ = c.Close()
	}()
}

func (c *Client) run(ctx context.Context) {
	defer close(c.runDoneCh)

	defer func() {
		c.eventsCloseOnce.Do(func() {
			close(c.eventsCh)
		})
	}()

	t := time.NewTicker(10 * time.Second)
	defer t.Stop()

	for {
		err := c.connectAndProcess(ctx)
		c.eventsCh <- EventDisconnected{Err: err}

		select {
		case <-ctx.Done():
			return

		case <-c.shutdownCh:
			return

		case <-t.C:
		}
	}
}

func (c *Client) connectAndProcessTransport(ctx context.Context, opts proxyclient.ClientOptions) error {
	log := logging.FromContext(ctx)

	doneCh := make(chan struct{})
	defer close(doneCh)
	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		select {
		case <-c.shutdownCh:
			cancel()
		case <-doneCh:
		}
	}()

	transport, err := proxyclient.NewTransport(connCtx, opts)
	if err != nil {
		switch {
		case errors.Is(err, proxyclient.ErrUnauthorized):
			c.exitErr(ErrUnauthorized)
			return nil
		case errors.Is(err, proxyclient.ErrNotFound):
			c.exitErr(ErrNotFound)
			return nil
		default:
			return err
		}
	}
	defer func() {
		_ = transport.Close()
	}()

	stream, err := transport.WatchProxies(connCtx)
	if err != nil {
		return err
	}

	for {
		ev, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return err
			}
			return err
		}

		log.Debug("received event from proxy server", zap.String("type", proxyEventType(ev)))

		switch event := ev.Event.(type) {
		case *pbclient.ProxyEvent_Init:
			data := EventInit(make([]api.Proxy, 0, len(event.Init.Proxy)))
			for _, item := range event.Init.Proxy {
				data = append(data, api.ProxyFromPB(item))
			}

			c.readyOnce.Do(func() {
				close(c.readyCh)
			})

			c.eventsCh <- data

		case *pbclient.ProxyEvent_Add:
			c.eventsCh <- EventProxyAdd{Proxy: api.ProxyFromPB(event.Add.Proxy)}

		case *pbclient.ProxyEvent_Update:
			c.eventsCh <- EventProxyUpdate{Proxy: api.ProxyFromPB(event.Update.Proxy)}

		case *pbclient.ProxyEvent_Del:
			c.eventsCh <- EventProxyDel(event.Del.Id)
		}
	}
}

func proxyEventType(ev *pbclient.ProxyEvent) string {
	switch ev.Event.(type) {
	case *pbclient.ProxyEvent_Init:
		return "init"
	case *pbclient.ProxyEvent_Add:
		return "add"
	case *pbclient.ProxyEvent_Update:
		return "update"
	case *pbclient.ProxyEvent_Del:
		return "del"
	default:
		return "unknown"
	}
}
