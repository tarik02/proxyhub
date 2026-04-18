package proxyhub

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"

	"github.com/hashicorp/yamux"
	"github.com/tarik02/proxyhub/api"
	"github.com/tarik02/proxyhub/entevents"
	"github.com/tarik02/proxyhub/logging"
	"github.com/tarik02/proxyhub/pb/pbclient"
	transportproto "github.com/tarik02/proxyhub/transport"
	"github.com/tarik02/proxyhub/util"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

type ClientTransportHandler struct {
	hub    *Proxyhub
	events *entevents.Manager[api.Proxy]
}

func NewClientTransportHandler(hub *Proxyhub, events *entevents.Manager[api.Proxy]) *ClientTransportHandler {
	return &ClientTransportHandler{
		hub:    hub,
		events: events,
	}
}

func (h *ClientTransportHandler) HandleSession(ctx context.Context, session *yamux.Session) error {
	log := logging.FromContext(ctx).Named("client_transport")

	var wg sync.WaitGroup
	var controlMu sync.Mutex
	controlStreamOpened := false
	var controlServer *grpc.Server
	var shutdownOnce sync.Once

	shutdownSession := func() {
		shutdownOnce.Do(func() {
			_ = session.GoAway()

			controlMu.Lock()
			if controlServer != nil {
				controlServer.Stop()
			}
			controlMu.Unlock()

			_ = session.Close()
		})
	}

	go func() {
		select {
		case <-ctx.Done():
			shutdownSession()
		case <-session.CloseChan():
		}
	}()

	var resErr error

loop:
	for {
		stream, err := session.AcceptStreamWithContext(ctx)
		if err != nil {
			if isClientTransportExitErr(err, ctx, session) {
				break loop
			}
			resErr = err
			break loop
		}

		wg.Add(1)
		go func(stream *yamux.Stream) {
			defer wg.Done()

			header, err := transportproto.ReadHeader(stream)
			if err != nil {
				if !isClientTransportExitErr(err, ctx, session) {
					log.Warn("client transport stream header rejected", zap.Error(err))
				}
				_ = stream.Close()
				return
			}

			switch header.Purpose {
			case transportproto.PurposeGRPCControl:
				controlMu.Lock()
				if controlStreamOpened {
					controlMu.Unlock()
					log.Warn("rejecting duplicate gRPC control stream")
					_ = stream.Close()
					return
				}
				controlStreamOpened = true
				server := grpc.NewServer()
				controlServer = server
				controlMu.Unlock()

				pbclient.RegisterServiceServer(server, NewClientCatalogGRPC(h.events))

				if err := util.GrpcServeOnConn(server, stream); err != nil && !isClientTransportExitErr(err, ctx, session) {
					log.Warn("client catalog gRPC server stopped", zap.Error(err))
				}

				controlMu.Lock()
				if controlServer == server {
					controlServer = nil
				}
				controlMu.Unlock()

			case transportproto.PurposeProxyTunnel:
				proxy := h.hub.GetProxyByID(header.ProxyID)
				if proxy == nil {
					log.Warn("proxy tunnel requested for unknown proxy", zap.String("proxy_id", header.ProxyID))
					_ = stream.Close()
					return
				}

				if err := proxy.QueueConn(ctx, stream); err != nil {
					if !isClientTransportExitErr(err, ctx, session) {
						log.Warn("proxy tunnel dispatch failed", zap.Error(err), zap.String("proxy_id", header.ProxyID))
					}
					_ = stream.Close()
				}

			default:
				log.Warn("client transport stream has unsupported purpose", zap.String("purpose", string(header.Purpose)))
				_ = stream.Close()
			}
		}(stream)
	}

	shutdownSession()
	wg.Wait()
	return resErr
}

func isClientTransportExitErr(err error, ctx context.Context, session *yamux.Session) bool {
	if err == nil {
		return false
	}

	if errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) || errors.Is(err, ErrShutdown) || errors.Is(err, grpc.ErrServerStopped) {
		return true
	}

	if ctx.Err() != nil {
		return true
	}

	select {
	case <-session.CloseChan():
		return true
	default:
		return false
	}
}
