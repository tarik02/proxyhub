package proxycatalog

import "github.com/tarik02/proxyhub/api"

type EventDisconnected struct {
	Err error
}

type EventInit []api.Proxy

type EventProxyAdd struct {
	api.Proxy `json:",inline"`
}

type EventProxyDel string

type EventProxyUpdate struct {
	api.Proxy `json:",inline"`
}
