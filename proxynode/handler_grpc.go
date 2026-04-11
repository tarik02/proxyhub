package proxynode

import (
	"context"
	"fmt"

	"github.com/tarik02/proxyhub/logging"
	"github.com/tarik02/proxyhub/pb/pbnode"
	"go.uber.org/zap"
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
	logging.FromContext(ctx).Info("server initiated disconnect", zap.String("reason", req.Reason))
	_ = h.proxy.CloseWithError(fmt.Errorf("%w: %s", ErrServerDisconnect, req.Reason))
	return &pbnode.DisconnectResponse{}, nil
}
