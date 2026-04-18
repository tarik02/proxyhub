package proxycatalog

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/tarik02/proxyhub/entevents"
	"github.com/tarik02/proxyhub/logging"
	"github.com/tarik02/proxyhub/proxyclient"
	"github.com/tmaxmax/go-sse"
	"go.uber.org/zap"
)

func (c *Client) connectAndProcessLegacySSE(ctx context.Context, opts proxyclient.ClientOptions) error {
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

		log.Debug("received legacy SSE event from proxy server",
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
