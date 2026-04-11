package util

import (
	"context"
	"errors"
	"net"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func GrpcServeOnConn(s *grpc.Server, conn net.Conn) error {
	if err := s.Serve(NewOnceListener(conn)); !errors.Is(err, net.ErrClosed) {
		return err
	}
	return nil
}

func GrpcClientFromConn(conn net.Conn) (*grpc.ClientConn, error) {
	return grpc.NewClient(
		"127.0.0.1",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, s string) (net.Conn, error) {
			return conn, nil
		}),
	)
}
