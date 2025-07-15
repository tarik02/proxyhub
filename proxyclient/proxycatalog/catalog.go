package proxycatalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/tarik02/proxyhub/entevents"
	"github.com/tarik02/proxyhub/logging"
	"github.com/tarik02/proxyhub/proxyclient"
	"github.com/tmaxmax/go-sse"
	"go.uber.org/zap"
)

var ErrShutdown = errors.New("shutdown")
var ErrUnauthorized = errors.New("unauthorized")
var ErrNotFound = errors.New("not found")

type UnexpectedStatusError struct {
	StatusCode int
}

func (e *UnexpectedStatusError) Error() string {
	return fmt.Sprintf("unexpected status code: %d", e.StatusCode)
}

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
}

func NewClient(ctx context.Context, opts proxyclient.ClientOptions) *Client {
	c := &Client{
		shutdown:   false,
		shutdownCh: make(chan struct{}),

		readyCh:   make(chan struct{}),
		runDoneCh: make(chan struct{}),

		eventsCh: make(chan any, 128),
	}

	go c.run(ctx, opts)

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

func (c *Client) run(ctx context.Context, opts proxyclient.ClientOptions) {
	defer close(c.runDoneCh)

	defer func() {
		c.eventsCloseOnce.Do(func() {
			close(c.eventsCh)
		})
	}()

	t := time.NewTicker(10 * time.Second)
	defer t.Stop()

	for {
		err := c.connectAndProcess(ctx, opts)
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

func (c *Client) connectAndProcess(ctx context.Context, opts proxyclient.ClientOptions) error {
	log := logging.FromContext(ctx)

	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/api/proxies/live", opts.Endpoint), nil)
	if err != nil {
		return err
	}

	req.Header.Add("Authorization", fmt.Sprintf("Bearer %s", opts.Token))

	res, err := opts.HTTP.Do(req) // nolint:bodyclose
	if err != nil {
		return err
	}
	defer res.Body.Close()

	switch res.StatusCode {
	case http.StatusUnauthorized:
		c.exitErr(ErrUnauthorized)
		return nil

	case http.StatusNotFound:
		c.exitErr(ErrNotFound)
		return nil

	case http.StatusOK:
		break

	default:
		return &UnexpectedStatusError{StatusCode: res.StatusCode}
	}

	doneCh := make(chan struct{})
	defer close(doneCh)

	go func() {
		select {
		case <-c.shutdownCh:
		case <-doneCh:
		}

		_ = res.Body.Close()
	}()

	for ev, err := range sse.Read(res.Body, nil) {
		if err != nil {
			return err
		}

		log.Debug("received event from proxy server",
			zap.String("event_id", ev.LastEventID),
			zap.String("event_type", ev.Type),
			zap.String("data", ev.Data),
		)

		switch ev.Type {
		case entevents.EventTypeInit:
			data := EventInit{}
			if err := json.Unmarshal([]byte(ev.Data), &data); err != nil {
				return err
			}

			c.readyOnce.Do(func() {
				close(c.readyCh)
			})

			c.eventsCh <- data

		case entevents.EventTypeAdd:
			data := EventProxyAdd{}
			if err := json.Unmarshal([]byte(ev.Data), &data); err != nil {
				return err
			}

			c.eventsCh <- data

		case "update":
			data := EventProxyUpdate{}
			if err := json.Unmarshal([]byte(ev.Data), &data); err != nil {
				return err
			}

			c.eventsCh <- data

		case entevents.EventTypeDel:
			var data string
			if err := json.Unmarshal([]byte(ev.Data), &data); err != nil {
				return err
			}

			c.eventsCh <- EventProxyDel(data)
		}
	}

	return nil
}
