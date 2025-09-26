package proxynode

import (
	"context"
	"fmt"

	"github.com/tarik02/proxyhub/pb/pbnode"
)

type HandlerGRPC struct {
	pbnode.UnimplementedServiceServer

	proxy *Proxynode
}

func (h *HandlerGRPC) MOTD(ctx context.Context, req *pbnode.MOTDRequest) (*pbnode.MOTDResponse, error) {
	go h.proxy.OnServerMessage(req.Message)
	return &pbnode.MOTDResponse{}, nil
}

func (h *HandlerGRPC) Disconnect(ctx context.Context, req *pbnode.DisconnectRequest) (*pbnode.DisconnectResponse, error) {
	_ = h.proxy.CloseWithError(fmt.Errorf("server initiated disconnect: %s", req.Reason))
	return &pbnode.DisconnectResponse{}, nil
}
