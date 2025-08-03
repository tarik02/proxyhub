package proxyhub

import (
	"context"

	"github.com/tarik02/proxyhub/api"
)

type ProxyHandler interface {
	Ready() chan struct{}
	SendMOTD(ctx context.Context, content string) error
	SendDisconnect(ctx context.Context, reason string) error

	Info() (api.Proxy, chan struct{})
}
