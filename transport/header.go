package transport

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

const (
	Version           = 1
	MaxHeaderSize     = 4096
	ReadHeaderTimeout = 5 * time.Second
)

type Purpose string

const (
	PurposeGRPCControl Purpose = "grpc_control"
	PurposeProxyTunnel Purpose = "proxy_tunnel"
)

var (
	ErrInvalidHeader   = errors.New("invalid stream header")
	ErrHeaderTooLarge  = errors.New("stream header too large")
	ErrHeaderRequired  = errors.New("stream header required")
	ErrUnsupportedType = errors.New("unsupported stream purpose")
)

type StreamHeader struct {
	Version int     `json:"version"`
	Purpose Purpose `json:"purpose"`
	ProxyID string  `json:"proxy_id,omitempty"`
}

func (h StreamHeader) Validate() error {
	if h.Version != Version {
		return fmt.Errorf("%w: unsupported version %d", ErrInvalidHeader, h.Version)
	}

	switch h.Purpose {
	case PurposeGRPCControl:
		if h.ProxyID != "" {
			return fmt.Errorf("%w: proxy_id is not allowed for %s", ErrInvalidHeader, h.Purpose)
		}

	case PurposeProxyTunnel:
		if h.ProxyID == "" {
			return fmt.Errorf("%w: proxy_id is required for %s", ErrInvalidHeader, h.Purpose)
		}

	default:
		return fmt.Errorf("%w: %v", ErrUnsupportedType, h.Purpose)
	}

	return nil
}

func WriteHeader(w io.Writer, header StreamHeader) error {
	if err := header.Validate(); err != nil {
		return err
	}

	body, err := json.Marshal(header)
	if err != nil {
		return err
	}
	if len(body) == 0 {
		return ErrHeaderRequired
	}
	if len(body) > MaxHeaderSize {
		return fmt.Errorf("%w: %d", ErrHeaderTooLarge, len(body))
	}

	prefix := make([]byte, 4)
	// len(body) is bounded by MaxHeaderSize (4 KiB), so this narrowing conversion is safe.
	//nolint:gosec
	binary.BigEndian.PutUint32(prefix, uint32(len(body)))

	if _, err := w.Write(prefix); err != nil {
		return err
	}
	if _, err := w.Write(body); err != nil {
		return err
	}

	return nil
}

func ReadHeader(conn net.Conn) (StreamHeader, error) {
	if err := conn.SetReadDeadline(time.Now().Add(ReadHeaderTimeout)); err != nil {
		return StreamHeader{}, err
	}
	defer func() {
		_ = conn.SetReadDeadline(time.Time{})
	}()

	return readHeaderFromReader(conn)
}

func readHeaderFromReader(r io.Reader) (StreamHeader, error) {
	var prefix [4]byte
	if _, err := io.ReadFull(r, prefix[:]); err != nil {
		return StreamHeader{}, err
	}

	size := binary.BigEndian.Uint32(prefix[:])
	if size == 0 {
		return StreamHeader{}, ErrHeaderRequired
	}
	if size > MaxHeaderSize {
		return StreamHeader{}, fmt.Errorf("%w: %d", ErrHeaderTooLarge, size)
	}

	body := make([]byte, size)
	if _, err := io.ReadFull(r, body); err != nil {
		return StreamHeader{}, err
	}

	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()

	var header StreamHeader
	if err := dec.Decode(&header); err != nil {
		return StreamHeader{}, fmt.Errorf("%w: %w", ErrInvalidHeader, err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return StreamHeader{}, fmt.Errorf("%w: trailing data", ErrInvalidHeader)
	}
	if err := header.Validate(); err != nil {
		return StreamHeader{}, err
	}

	return header, nil
}
