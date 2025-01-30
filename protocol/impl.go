package protocol

import (
	"context"

	"github.com/tarik02/proxyhub/logging"
)

func Raw2WS(ctx context.Context, id uint32, raw chan []byte, ws chan Cmd) error {
	log := logging.FromContext(ctx)
	_ = log

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case message := <-raw:
			if len(message) == 0 {
				return nil
			}

			select {
			case <-ctx.Done():
				return ctx.Err()

			case ws <- CmdData{ID: id, Bytes: message}:
			}
		}
	}
}

func WS2Raw(ctx context.Context, ws chan Cmd, raw chan []byte) error {
	log := logging.FromContext(ctx)
	_ = log

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case cmd := <-ws:
			switch cmd := cmd.(type) {
			case CmdData:
				select {
				case <-ctx.Done():
					return ctx.Err()

				case raw <- cmd.Bytes:
				}

			case CmdClose:
				return nil
			}
		}
	}
}
