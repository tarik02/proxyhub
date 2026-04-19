package proxyhub

import (
	"errors"
	"fmt"

	"github.com/tarik02/proxyhub/api"
	"github.com/tarik02/proxyhub/entevents"
	"github.com/tarik02/proxyhub/pb"
	"github.com/tarik02/proxyhub/pb/pbclient"
)

type ClientCatalogGRPC struct {
	pbclient.UnimplementedServiceServer

	events *entevents.Manager[api.Proxy]
}

var errSkipProxyEvent = errors.New("skip proxy event")

func NewClientCatalogGRPC(events *entevents.Manager[api.Proxy]) *ClientCatalogGRPC {
	return &ClientCatalogGRPC{
		events: events,
	}
}

func (h *ClientCatalogGRPC) WatchProxies(_ *pbclient.WatchProxiesRequest, stream pbclient.Service_WatchProxiesServer) error {
	eventsCh, unsubscribe, err := h.events.Subscribe(stream.Context(), 32)
	if err != nil {
		return err
	}
	defer unsubscribe()

	for {
		select {
		case <-stream.Context().Done():
			return stream.Context().Err()

		case ev, ok := <-eventsCh:
			if !ok {
				return nil
			}

			msg, err := proxyEventToPB(ev)
			if err != nil {
				if errors.Is(err, errSkipProxyEvent) {
					continue
				}
				return err
			}

			if err := stream.Send(msg); err != nil {
				return err
			}
		}
	}
}

func proxyEventToPB(ev entevents.EntityEvent[api.Proxy]) (*pbclient.ProxyEvent, error) {
	switch ev.Type {
	case entevents.EventTypeInit:
		payload, ok := ev.Payload.([]api.Proxy)
		if !ok {
			return nil, fmt.Errorf("unexpected init payload type %T", ev.Payload)
		}

		items := make([]*pb.Proxy, 0, len(payload))
		for _, item := range payload {
			items = append(items, api.ProxyToPB(item))
		}

		return &pbclient.ProxyEvent{
			Event: &pbclient.ProxyEvent_Init{
				Init: &pbclient.ProxyEventInit{Proxy: items},
			},
		}, nil

	case entevents.EventTypeAdd:
		return &pbclient.ProxyEvent{
			Event: &pbclient.ProxyEvent_Add{
				Add: &pbclient.ProxyEventAdd{Proxy: api.ProxyToPB(ev.Entity)},
			},
		}, nil

	case "update":
		return &pbclient.ProxyEvent{
			Event: &pbclient.ProxyEvent_Update{
				Update: &pbclient.ProxyEventUpdate{Proxy: api.ProxyToPB(ev.Entity)},
			},
		}, nil

	case entevents.EventTypeDel:
		return &pbclient.ProxyEvent{
			Event: &pbclient.ProxyEvent_Del{
				Del: &pbclient.ProxyEventDel{Id: ev.ID},
			},
		}, nil

	case entevents.EventTypePing, entevents.EventTypeShutdown:
		return nil, errSkipProxyEvent

	default:
		return nil, fmt.Errorf("unexpected event type %q", ev.Type)
	}
}
