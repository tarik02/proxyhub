package proxyhub

import (
	"context"
	"fmt"
	"sync"

	"github.com/tarik02/proxyhub/api"
	"github.com/tarik02/proxyhub/pb/pbhub"
	"github.com/tarik02/proxyhub/pb/pbnode"
	"google.golang.org/grpc"
)

type ProxyHandlerGRPC struct {
	pbhub.UnimplementedServiceServer

	proxy      *Proxy
	grpcClient *grpc.ClientConn

	readyChan chan struct{}
	readyOnce sync.Once

	info            api.Proxy
	infoChangedChan chan struct{}
	infoMu          sync.RWMutex
}

func NewProxyHandlerGRPC(proxy *Proxy, grpcClient *grpc.ClientConn, info api.Proxy) *ProxyHandlerGRPC {
	return &ProxyHandlerGRPC{
		proxy:      proxy,
		grpcClient: grpcClient,

		readyChan: make(chan struct{}),

		info:            info,
		infoChangedChan: make(chan struct{}),
	}
}

func (h *ProxyHandlerGRPC) Ready() chan struct{} {
	return h.readyChan
}

func (h *ProxyHandlerGRPC) Info() (api.Proxy, chan struct{}) {
	h.infoMu.RLock()
	defer h.infoMu.RUnlock()
	return h.info, h.infoChangedChan
}

func (h *ProxyHandlerGRPC) SendMOTD(ctx context.Context, content string) error {
	_, err := pbnode.NewServiceClient(h.grpcClient).MOTD(ctx, &pbnode.MOTDRequest{
		Message: content,
	})
	return err
}

func (h *ProxyHandlerGRPC) SendDisconnect(ctx context.Context, reason string) error {
	_, err := pbnode.NewServiceClient(h.grpcClient).Disconnect(ctx, &pbnode.DisconnectRequest{
		Reason: reason,
	})
	return err
}

func (h *ProxyHandlerGRPC) Hello(ctx context.Context, req *pbhub.HelloRequest) (*pbhub.HelloResponse, error) {
	h.readyOnce.Do(func() {
		close(h.readyChan)
	})

	h.infoMu.Lock()
	info := h.info
	info.EgressWhitelist = req.EgressWhitelist.Item
	h.info = info
	close(h.infoChangedChan)
	h.infoChangedChan = make(chan struct{})
	h.infoMu.Unlock()

	return &pbhub.HelloResponse{}, nil
}

func (h *ProxyHandlerGRPC) Disconnect(ctx context.Context, req *pbhub.DisconnectRequest) (*pbhub.DisconnectResponse, error) {
	_ = h.proxy.CloseWithError(fmt.Errorf("client initiated disconnect: %s", req.Reason))

	return &pbhub.DisconnectResponse{}, nil
}

func (h *ProxyHandlerGRPC) UpdatedEgressWhitelist(ctx context.Context, req *pbhub.UpdatedEgressWhitelistRequest) (*pbhub.UpdatedEgressWhitelistResponse, error) {
	h.infoMu.Lock()
	info := h.info
	info.EgressWhitelist = req.EgressWhitelist.Item
	h.info = info
	close(h.infoChangedChan)
	h.infoChangedChan = make(chan struct{})
	h.infoMu.Unlock()

	return &pbhub.UpdatedEgressWhitelistResponse{}, nil
}

var _ ProxyHandler = (*ProxyHandlerGRPC)(nil)
