package proxyhub

import (
	"context"
	"fmt"
	"io"

	"github.com/tarik02/proxyhub/logging"
	"github.com/tarik02/proxyhub/pb"
	"github.com/tarik02/proxyhub/wsstream"
	"go.uber.org/zap"
	"google.golang.org/protobuf/encoding/protojson"
)

func ReadProxyHello(ctx context.Context, wsstream *wsstream.WSStream) (*pb.Control_ClientHello, error) {
	log := logging.FromContext(ctx)
	resChan := make(chan *pb.Control_ClientHello, 1)
	errChan := make(chan error, 1)

	wsstream.HandleTextMessage = func(r io.Reader) {
		b, err := io.ReadAll(r)
		if err != nil {
			log.Warn("reading text message failed", zap.Error(err))
			return
		}

		msg := &pb.Control{}
		if err := protojson.Unmarshal(b, msg); err != nil {
			log.Warn("unmarshaling message failed", zap.Error(err))
			return
		}

		switch msg.Message.(type) {
		case *pb.Control_Disconnect_:
			errChan <- fmt.Errorf("client initiated disconnect: %s", msg.GetDisconnect().Reason)

		case *pb.Control_ClientHello_:
			resChan <- msg.GetClientHello()

		default:
			errChan <- fmt.Errorf("unexpected message, expected client hello: %v", msg)
		}
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()

	case err := <-errChan:
		return nil, err

	case res := <-resChan:
		return res, nil
	}
}
