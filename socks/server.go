package socks

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"

	"github.com/tarik02/proxyhub/logging"
	"github.com/tarik02/proxyhub/util"
	"go.uber.org/zap"
	"golang.org/x/net/proxy"
)

const (
	version byte = 0x05

	cmdConnect byte = 0x01

	addrTypeHostname byte = 0x03

	codeSuccess byte = 0x00
	codeFailure byte = 0x01
)

type Socks5Server struct {
	Dialer         proxy.Dialer
	ValidateTarget func(ctx context.Context, target string) error
}

func (s *Socks5Server) ServeConn(ctx context.Context, conn net.Conn) error {
	defer func() {
		_ = conn.Close()
	}()

	log := logging.FromContext(ctx)

	buf := make([]byte, 16*1024)

	if _, err := io.ReadFull(conn, buf[:2]); err != nil {
		return err
	}

	if buf[0] != version {
		return errors.New("unsupported version")
	}

	authMethodsCount := int(buf[1])

	log.Debug("auth methods count", zap.Int("count", authMethodsCount))

	if _, err := io.ReadFull(conn, buf[:authMethodsCount]); err != nil {
		return err
	}

	log.Debug("auth methods read done")

	if _, err := conn.Write([]byte{version, 0x00}); err != nil {
		return err
	}

	log.Debug("reading header")

	if _, err := io.ReadFull(conn, buf[:5]); err != nil {
		return err
	}

	if buf[0] != version {
		return errors.New("unsupported version")
	}

	if buf[1] != cmdConnect {
		log.Debug("unsupported command", zap.Int("command", int(buf[1])))
		return errors.New("unsupported command")
	}

	if buf[3] != addrTypeHostname {
		log.Debug("unsupported address type", zap.Int("type", int(buf[2])))
		return errors.New("unsupported address type")
	}

	l := int(buf[4])
	if _, err := io.ReadFull(conn, buf[:l+2]); err != nil {
		return err
	}

	host := string(buf[:l])
	port := binary.BigEndian.Uint16(buf[l : l+2])
	target := net.JoinHostPort(host, fmt.Sprintf("%d", port))

	if s.ValidateTarget != nil {
		if err := s.ValidateTarget(ctx, target); err != nil {
			_, _ = conn.Write([]byte{version, codeFailure, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x01, 0x01})
			return err
		}
	}

	log.Debug("connecting", zap.String("target", target))

	upstream, err := util.DialProxyContext(ctx, s.Dialer, "tcp", target)
	if err != nil {
		_, _ = conn.Write([]byte{version, codeFailure, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x01, 0x01})
		return err
	}
	defer upstream.Close()

	if _, err := conn.Write([]byte{version, codeSuccess, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x01, 0x01}); err != nil {
		return err
	}

	log.Debug("connected", zap.String("target", target))
	_, _, err = util.Copy2(ctx, upstream, conn)

	return err
}
